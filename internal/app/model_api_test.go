package app

// C02 绑定层测试。
//
// 守三件事：
//  1. 模型定义能走完整生命周期（保存 / 列举 / 设为当前 / 删除）；
//  2. 密钥只进 OS 凭证库，配置目录里查不到明文（SEC07）；
//  3. 换模型不能打断正在跑的 run（MOD04 的安全点）。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/resources"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
)

func demoModel() resources.ModelDefinition {
	return resources.ModelDefinition{
		ID:            "m-demo",
		ProviderID:    "demo",
		ModelID:       "demo-model",
		API:           "openai-completions",
		BaseURL:       "http://127.0.0.1:8080/v1",
		InputKinds:    []string{"text"},
		ContextWindow: 128000,
		MaxOutput:     8192,
	}
}

// scanForSecret 扫目录里的明文与 base64 变体。
func scanForSecret(t *testing.T, root, secret string) []string {
	t.Helper()
	variants := [][]byte{
		[]byte(secret),
		[]byte(base64.StdEncoding.EncodeToString([]byte(secret))),
		[]byte(base64.RawStdEncoding.EncodeToString([]byte(secret))),
		[]byte(base64.URLEncoding.EncodeToString([]byte(secret))),
	}
	var hits []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, v := range variants {
			if len(v) > 0 && strings.Contains(string(data), string(v)) {
				hits = append(hits, path)
				break
			}
		}
		return nil
	})
	return hits
}

// TestModelLifecycleThroughBindings 覆盖保存→列举→设为当前→删除，
// 并确认密钥没有落到配置目录。
func TestModelLifecycleThroughBindings(t *testing.T) {
	d, root := newTestDesktop(t)
	const secret = "sk-sentinel-c02-never-on-disk-7d1f"

	def := demoModel()
	def.CredentialRef = "cred-demo"
	saved := d.SaveModel(0, def, secret)
	if saved.Error != nil {
		t.Fatalf("保存模型失败: %s: %s", saved.Error.Code, saved.Error.Message)
	}
	if saved.Revision != 1 {
		t.Fatalf("首次保存 revision 应为 1，实际 %d", saved.Revision)
	}

	if hits := scanForSecret(t, root, secret); len(hits) > 0 {
		t.Fatalf("配置目录出现明文密钥: %v", hits)
	}

	list := d.ListModels()
	if list.Error != nil {
		t.Fatalf("列举失败: %s", list.Error.Message)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "m-demo" {
		t.Fatalf("列表内容不对: %+v", list.Items)
	}
	if len(list.Options.APIs) == 0 || len(list.Options.ThinkingLevels) == 0 {
		t.Fatalf("下拉选项必须来自后端白名单: %+v", list.Options)
	}

	act := d.SetActiveModel("m-demo")
	if act.Error != nil {
		t.Fatalf("设为当前模型失败: %s", act.Error.Message)
	}
	cur := d.CurrentModel()
	if cur.Error != nil {
		t.Fatalf("读当前模型失败: %s", cur.Error.Message)
	}
	if cur.Source != "resources" || cur.Ref != "m-demo" || cur.Provider != "demo" || cur.Model != "demo-model" {
		t.Fatalf("当前模型不对: %+v", cur)
	}
	// 设置里写的是引用而不是密钥。
	if strings.Contains(mustRead(t, filepath.Join(root, "settings.json")), secret) {
		t.Fatal("settings.json 里出现明文密钥")
	}

	// MOD04 的外部观察：设为当前模型之后，磁盘上的 settings.json 必须写的是
	// 引用（modelRef）而不是密钥，且 agentDir 里没有任何内联 apiKey 值。
	observeModelSwitch(t, root, d.agentDir(), map[string]any{
		"currentModelSource": cur.Source,
		"currentModelRef":    cur.Ref,
	})

	del := d.DeleteModel(list.Revision, "m-demo")
	if del.Error != nil {
		t.Fatalf("删除失败: %s", del.Error.Message)
	}
	if got := d.ListModels(); len(got.Items) != 0 {
		t.Fatalf("删除后仍列出 %d 条", len(got.Items))
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return string(b)
}

// TestSaveModelRejectsSecretWithoutCredentialRef：只填密钥不给引用时拒绝，
// 而不是"帮你随便生成一个"——引用是用户后续换凭证的把手。
func TestSaveModelRejectsSecretWithoutCredentialRef(t *testing.T) {
	d, _ := newTestDesktop(t)
	res := d.SaveModel(0, demoModel(), "sk-x")
	if res.Error == nil || res.Error.Code != contracts.CodeUnsupported {
		t.Fatalf("期望 UNSUPPORTED，实际 %+v", res.Error)
	}
}

// TestSetActiveModelRejectsMissing：把不存在的模型设为当前必须报 NOT_FOUND，
// 不能写进设置里等下次启动才发现。
func TestSetActiveModelRejectsMissing(t *testing.T) {
	d, _ := newTestDesktop(t)
	act := d.SetActiveModel("m-nope")
	if act.Error == nil || act.Error.Code != contracts.CodeNotFound {
		t.Fatalf("期望 NOT_FOUND，实际 %+v", act.Error)
	}
	d.mu.Lock()
	ref := d.settings.ModelRef
	d.mu.Unlock()
	if ref != "" {
		t.Fatalf("失败的设置不应落盘，实际 %q", ref)
	}
}

// TestStartRejectsDeletedActiveModel：当前模型被删掉后，启动必须给出可修复的
// 原因（DEPENDENCY_MISSING），而不是让 Pi 抛 Unknown provider。
func TestStartRejectsDeletedActiveModel(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 信任默认工作区（Start 的第一道闸门）。
	cur := d.CurrentWorkspace()
	if cur.Error != nil {
		t.Fatalf("登记工作区: %s", cur.Error.Message)
	}
	if tr := d.SetWorkspaceTrust(string(cur.Item.ID), true); tr.Error != nil {
		t.Fatalf("信任工作区: %s", tr.Error.Message)
	}

	saved := d.SaveModel(0, demoModel(), "")
	if saved.Error != nil {
		t.Fatalf("保存模型: %s", saved.Error.Message)
	}
	if act := d.SetActiveModel("m-demo"); act.Error != nil {
		t.Fatalf("设为当前: %s", act.Error.Message)
	}
	if del := d.DeleteModel(saved.Revision, "m-demo"); del.Error != nil {
		t.Fatalf("删除模型: %s", del.Error.Message)
	}

	_, err := d.Start("你好")
	if err == nil {
		t.Fatal("当前模型不存在时必须拒绝启动")
	}
	appErr, ok := err.(*contracts.AppError)
	if !ok {
		t.Fatalf("期望契约错误，实际 %T: %v", err, err)
	}
	if appErr.Code != contracts.CodeDependencyMissing {
		t.Fatalf("期望 DEPENDENCY_MISSING，实际 %s: %s", appErr.Code, appErr.Message)
	}
	if !strings.Contains(appErr.Message, "模型") {
		t.Fatalf("错误要指明是模型的问题，实际 %q", appErr.Message)
	}
}

// stubWorker 是一个永不结束的假 Pi：用来制造"有 run 在跑"的状态。
type stubWorker struct {
	events chan pi.RawEvent
	once   sync.Once
}

func newStubWorker() *stubWorker { return &stubWorker{events: make(chan pi.RawEvent, 8)} }

func (s *stubWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"type":"response","success":true}`), nil
}
func (s *stubWorker) Events() <-chan pi.RawEvent { return s.events }
func (s *stubWorker) Close(ctx context.Context) error {
	s.once.Do(func() { close(s.events) })
	return nil
}
func (s *stubWorker) PID() int                   { return 1 }
func (s *stubWorker) StderrTail() (string, bool) { return "", false }
func (s *stubWorker) ExitCode() (int, bool)      { return 0, false }
func (s *stubWorker) Limitations() []string      { return nil }

// TestModelSwitchDoesNotInterruptRunningRun 是 MOD04 的核心断言：
// 换模型只改"下一次运行"，正在跑的那一轮不能被关掉。
func TestModelSwitchDoesNotInterruptRunningRun(t *testing.T) {
	d, root := newTestDesktop(t)
	if saved := d.SaveModel(0, demoModel(), ""); saved.Error != nil {
		t.Fatalf("保存模型: %s", saved.Error.Message)
	}

	work := t.TempDir()
	ext := filepath.Join(work, "harness-policy.ts")
	if err := os.WriteFile(ext, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写策略扩展桩: %v", err)
	}
	svc := run.New(run.Options{
		WorkspaceID:  "ws-test",
		SessionID:    "sess-test",
		WorkspaceDir: work,
		AgentDir:     filepath.Join(work, "agent"),
		Installation: pi.PiInstallation{ID: "test:pi", Executable: filepath.Join(work, "pi.exe")},
		Isolation:    pi.IsolationSpec{PolicyExtension: ext},
		Provider:     "old",
		Model:        "old-model",
		StartWorker: func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
			return newStubWorker(), nil
		},
	})
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	d.mu.Lock()
	d.svc = svc
	d.runtimeKey = "settings||old|old-model|0"
	d.mu.Unlock()

	if _, err := svc.Start(context.Background(), run.StartRunRequest{Message: "跑着"}); err != nil {
		t.Fatalf("启动 run: %v", err)
	}
	if !eventuallyTrue(2*time.Second, func() bool { return svc.ActiveCount() == 1 }) {
		t.Fatal("run 没有进入活跃状态")
	}

	act := d.SetActiveModel("m-demo")
	if act.Error != nil {
		t.Fatalf("设为当前模型: %s", act.Error.Message)
	}
	if !act.Deferred {
		t.Fatal("有 run 在跑时必须如实告知：新模型要等空闲才生效")
	}
	d.mu.Lock()
	same := d.svc == svc
	d.mu.Unlock()
	if !same {
		t.Fatal("换模型把正在跑的运行时关掉了")
	}
	if svc.ActiveCount() != 1 {
		t.Fatalf("活跃 run 数不应变化，实际 %d", svc.ActiveCount())
	}

	// MOD04 的外部观察：换模型只落到磁盘上的 settings.json（下一次运行用它），
	// 而正在跑的这一轮用的 agentDir 配置没有被动过。
	observeModelSwitch(t, root, d.agentDir(), map[string]any{
		"switchDeferredBecauseRunning": act.Deferred,
		"activeRunsAtSwitch":           svc.ActiveCount(),
	})
}

// TestActivatingModelDropsLegacyPlaintextKey：切到资源库模型后，
// 设置里的明文密钥必须被清掉——它已经没有任何读取路径，
// 留在磁盘上只会让 SEC07 的"配置里没有秘密"变成一句空话。
func TestActivatingModelDropsLegacyPlaintextKey(t *testing.T) {
	d, root := newTestDesktop(t)
	const legacy = "sk-legacy-plaintext-9c1e"
	s := d.Settings()
	s.APIKey = legacy
	if err := d.SaveSettings(s); err != nil {
		t.Fatalf("保存设置: %v", err)
	}
	if !strings.Contains(mustRead(t, filepath.Join(root, "settings.json")), legacy) {
		t.Fatal("前置条件不成立：明文密钥应先在 settings.json 里")
	}

	if saved := d.SaveModel(0, demoModel(), ""); saved.Error != nil {
		t.Fatalf("保存模型: %s", saved.Error.Message)
	}
	if act := d.SetActiveModel("m-demo"); act.Error != nil {
		t.Fatalf("设为当前: %s", act.Error.Message)
	}

	if strings.Contains(mustRead(t, filepath.Join(root, "settings.json")), legacy) {
		t.Fatal("切到资源库模型后，settings.json 里仍留着明文密钥")
	}
	if hits := scanForSecret(t, root, legacy); len(hits) > 0 {
		t.Fatalf("配置目录仍能扫到旧明文密钥: %v", hits)
	}
}

// TestActivatingModelScrubsStaleModelsJSON 守 SEC07 的一条真实泄漏路径。
//
// C01 的旧路径把明文密钥写进 agentDir/models.json。用户切到凭证库模型后，
// 那个文件已经没有任何读取者了——但它还在磁盘上，一份诊断包就能把它带走。
// 切换时必须把它覆盖成 $ENV 引用，而不是"反正没人读了"。
func TestActivatingModelScrubsStaleModelsJSON(t *testing.T) {
	d, root := newTestDesktop(t)
	const stale = "sk-magpie-legacy-leftover-4a19"

	agentDir := d.agentDir()
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatalf("建 agentDir: %v", err)
	}
	staleDoc := `{"providers":{"deepseek":{"api":"openai-completions","apiKey":"` + stale + `","baseUrl":"http://127.0.0.1:1/v1","models":[]}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(staleDoc), 0o600); err != nil {
		t.Fatalf("写旧 models.json: %v", err)
	}

	def := demoModel()
	def.ProviderID = "deepseek"
	def.CredentialRef = "cred-demo"
	if saved := d.SaveModel(0, def, "sk-new-secret-for-model"); saved.Error != nil {
		t.Fatalf("保存模型: %s", saved.Error.Message)
	}
	if act := d.SetActiveModel("m-demo"); act.Error != nil {
		t.Fatalf("设为当前: %s", act.Error.Message)
	}

	got := mustRead(t, filepath.Join(agentDir, "models.json"))
	if strings.Contains(got, stale) {
		t.Fatalf("旧明文密钥仍留在 models.json 里：\n%s", got)
	}
	if !strings.Contains(got, "\"apiKey\": \"$"+resources.CredentialEnvName("cred-demo")+"\"") {
		t.Fatalf("models.json 应只写环境变量引用，实际：\n%s", got)
	}
	if hits := scanForSecret(t, root, stale); len(hits) > 0 {
		t.Fatalf("切换后仍能扫到旧明文密钥: %v", hits)
	}
}

func eventuallyTrue(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
