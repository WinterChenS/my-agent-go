package app

// M1ENV：模型配置能从环境变量读到，并且**看得出来**是谁给的。
//
// 守四件事（每件都对应一个真实缺陷形状）：
//  1. 设置里没写模型时，ANVIL_PROVIDER / ANVIL_MODEL / ANVIL_BASE_URL 必须真的
//     进到启动参数与 models.json 里（不是"文档里写过"的假旋钮）；
//  2. 设置里写了就由设置说了算——环境变量只补缺席的字段，绝不顶替用户配置；
//  3. ANVIL_MODEL_API_KEY 只以引用形式存在：明文不进 settings.json、不进日志，
//     但真的经子进程环境交给了 Pi；
//  4. 谁都没说话时行为与今天一致：有默认值时用默认值，显式清空的配置仍是 CONFIG_MISSING。

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// prepareModelEnvDesktop 与 prepareRuntimeDesktop 同一套夹具，但**不**往内存设置里
// 写模型字段：模型必须由环境变量提供，这才是这条任务要证明的路径。
func prepareModelEnvDesktop(t *testing.T) (*Desktop, string) {
	t.Helper()
	d, root := newTestDesktop(t)

	dir := filepath.Join(root, "fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建 fixture 目录: %v", err)
	}
	piPath := filepath.Join(dir, "pi.exe")
	if err := os.WriteFile(piPath, []byte("stub"), 0o755); err != nil {
		t.Fatalf("写 Pi 桩: %v", err)
	}
	policy := filepath.Join(dir, "harness-policy.ts")
	if err := os.WriteFile(policy, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写策略扩展桩: %v", err)
	}
	t.Setenv("ANVIL_POLICY_EXT", policy)

	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("建工作目录: %v", err)
	}
	d.mu.Lock()
	d.settings.PiPath = piPath
	d.settings.Workspace = work
	d.mu.Unlock()
	return d, root
}

// startAndCaptureSpec 起一次真实（替身 worker）的 run，返回交给子进程的规格。
func startAndCaptureSpec(t *testing.T, d *Desktop) pi.WorkerSpec {
	t.Helper()
	ws := d.CreateWorkspace(d.Settings().Workspace)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}

	var (
		mu   sync.Mutex
		spec pi.WorkerSpec
		got  bool
	)
	d.mu.Lock()
	d.workerStarter = func(ctx context.Context, s pi.WorkerSpec) (pi.Worker, error) {
		mu.Lock()
		spec, got = s, true
		mu.Unlock()
		return newC06aWorker(), nil
	}
	d.mu.Unlock()

	if _, err := d.Start("你好"); err != nil {
		t.Fatalf("Start 失败（环境变量给的模型应足以起一轮）: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !got {
		t.Fatal("worker 没有被启动：Start 没有走到拉起 Pi 那一步")
	}
	return spec
}

// TestModelEnvFeedsStartWhenSettingsAreEmpty 是这条任务的主用例：
// 设置里什么都没有，环境变量必须一路走到 --provider/--model 与 models.json。
func TestModelEnvFeedsStartWhenSettingsAreEmpty(t *testing.T) {
	const secret = "{{API_KEY_m1env_feed}}"
	t.Setenv("ANVIL_PROVIDER", "envprov")
	t.Setenv("ANVIL_MODEL", "envmodel")
	t.Setenv("ANVIL_BASE_URL", "http://127.0.0.1:9/v1")
	t.Setenv("ANVIL_MODEL_API_KEY", secret)

	d, root := prepareModelEnvDesktop(t)

	// 1) 生效设置与状态直接反映环境值（就绪检查读的就是这两个）。
	if got := d.Settings().Provider; got != "envprov" {
		t.Fatalf("设置里的 provider = %q，期望环境值 envprov", got)
	}
	st := d.Status()
	if st.Provider != "envprov" || st.Model != "envmodel" {
		t.Fatalf("Status 的模型字段 = %s/%s，期望 envprov/envmodel", st.Provider, st.Model)
	}
	if cur := d.CurrentModel(); cur.Provider != "envprov" || cur.Model != "envmodel" || cur.Source != "settings" {
		t.Fatalf("CurrentModel = %+v，期望 legacy 路径上的 envprov/envmodel", cur)
	}
	// 发送前就绪检查读的就是这三件事（CurrentModel 的 provider/model + settings.baseUrl）。
	// 它们都不再是"未配置 / 占位端点"，所以界面上的 MODEL_MISSING / MODEL_ENDPOINT
	// 阻塞不再成立——这就是"env 让一轮对话真的能发出去"的判定依据。
	rev := d.SettingsWithRevision()
	if rev.Error != nil {
		t.Fatalf("读设置: %+v", rev.Error)
	}
	if rev.Settings.BaseURL != "http://127.0.0.1:9/v1" {
		t.Fatalf("设置页读到的 baseUrl = %q，期望环境值（就绪检查用它判断占位端点）", rev.Settings.BaseURL)
	}

	// 2) 来源必须可查询，且被环境补上的四项要显式列出来（不允许静默覆盖）。
	eff := d.EffectiveConfig()
	if eff.Model.Provider.Source != SourceEnv || eff.Model.Provider.EnvVar != "ANVIL_PROVIDER" {
		t.Fatalf("provider 来源 = %+v，期望 env/ANVIL_PROVIDER", eff.Model.Provider)
	}
	if eff.Model.Provider.Value != "envprov" || eff.Model.Model.Value != "envmodel" ||
		eff.Model.BaseURL.Value != "http://127.0.0.1:9/v1" {
		t.Fatalf("生效模型 = %+v，期望环境给的三项", eff.Model)
	}
	if eff.Model.APIKeyRef.Value != "env:ANVIL_MODEL_API_KEY" {
		t.Fatalf("apiKeyRef = %q，期望引用而不是密钥", eff.Model.APIKeyRef.Value)
	}
	for _, want := range []string{"provider", "model", "baseUrl", "apiKeyRef"} {
		if !containsString(eff.Model.OverriddenByEnv, want) {
			t.Fatalf("OverriddenByEnv 缺 %q（环境覆盖必须显示出来）: %v", want, eff.Model.OverriddenByEnv)
		}
	}

	// 3) 真正交给 Pi 的：启动参数 + models.json。
	spec := startAndCaptureSpec(t, d)
	if !argsContainPair(spec.Args, "--provider", "envprov") {
		t.Fatalf("启动参数里没有 --provider envprov: %v", spec.Args)
	}
	if !argsContainPair(spec.Args, "--model", "envmodel") {
		t.Fatalf("启动参数里没有 --model envmodel: %v", spec.Args)
	}
	modelsRaw := mustRead(t, filepath.Join(d.agentDir(), "models.json"))
	var doc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(modelsRaw), &doc); err != nil {
		t.Fatalf("models.json 不是合法 JSON: %v", err)
	}
	prov, ok := doc.Providers["envprov"]
	if !ok {
		t.Fatalf("models.json 里没有 envprov provider: %s", modelsRaw)
	}
	if prov.BaseURL != "http://127.0.0.1:9/v1" {
		t.Fatalf("models.json 的 baseUrl = %q，期望环境值", prov.BaseURL)
	}
	if len(prov.Models) != 1 || prov.Models[0].ID != "envmodel" {
		t.Fatalf("models.json 的模型列表 = %+v，期望只有 envmodel", prov.Models)
	}
	// 磁盘上只有 $ENV 引用，密钥原文只在子进程环境里。
	if !strings.HasPrefix(prov.APIKey, "$") {
		t.Fatalf("models.json 的 apiKey = %q，期望 $ENV 引用而不是明文", prov.APIKey)
	}
	envName := strings.TrimPrefix(prov.APIKey, "$")
	if got, ok := c06aEnvValue(spec.Env, envName); !ok || got != secret {
		t.Fatalf("子进程环境里应带着密钥（%s），实际存在=%v 值=%q", envName, ok, got)
	}

	// 4) 密钥不落盘：settings.json 里既没有明文，也没有那条自动生成的引用。
	if err := d.SaveSettings(d.Settings()); err != nil {
		t.Fatalf("保存设置: %v", err)
	}
	settingsRaw := mustRead(t, filepath.Join(root, "settings.json"))
	if strings.Contains(settingsRaw, secret) {
		t.Fatalf("settings.json 里出现了密钥明文: %s", settingsRaw)
	}
	if strings.Contains(settingsRaw, "ANVIL_MODEL_API_KEY") {
		t.Fatalf("settings.json 里出现了自动生成的密钥引用（环境值不该被固化）: %s", settingsRaw)
	}
	for _, leaked := range []string{"envprov", "envmodel", "http://127.0.0.1:9/v1"} {
		if strings.Contains(settingsRaw, leaked) {
			t.Fatalf("环境值 %q 被写进了 settings.json: %s", leaked, settingsRaw)
		}
	}
	if hits := scanForSecret(t, root, secret); len(hits) > 0 {
		t.Fatalf("配置目录能扫到密钥明文: %v", hits)
	}
	// 保存之后环境值仍然生效（只是不落盘），否则下一次 run 的模型会莫名变空。
	if got := d.Settings().Provider; got != "envprov" {
		t.Fatalf("保存之后 provider = %q，环境值应继续生效", got)
	}
}

// TestModelEnvDoesNotOverrideSettings 守"设置永远优先"。
func TestModelEnvDoesNotOverrideSettings(t *testing.T) {
	t.Setenv("ANVIL_PROVIDER", "envprov")
	t.Setenv("ANVIL_MODEL", "envmodel")
	t.Setenv("ANVIL_BASE_URL", "http://127.0.0.1:9/v1")

	root := t.TempDir()
	saved := Settings{
		Provider:  "saved-prov",
		Model:     "saved-model",
		BaseURL:   "http://saved.example/v1",
		Workspace: filepath.Join(root, "ws"),
	}
	if err := os.MkdirAll(saved.Workspace, 0o755); err != nil {
		t.Fatalf("建工作目录: %v", err)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatalf("序列化设置: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), raw, 0o600); err != nil {
		t.Fatalf("写 settings.json: %v", err)
	}

	d, err := New(root)
	if err != nil {
		t.Fatalf("构造 Desktop: %v", err)
	}
	t.Cleanup(d.Shutdown)

	eff := d.EffectiveConfig()
	for _, f := range []struct {
		name  string
		field ConfigField
		want  string
	}{
		{"provider", eff.Model.Provider, "saved-prov"},
		{"model", eff.Model.Model, "saved-model"},
		{"baseUrl", eff.Model.BaseURL, "http://saved.example/v1"},
	} {
		if f.field.Source != SourceSettings {
			t.Fatalf("%s 来源 = %q，期望 settings（设置优先于环境变量）", f.name, f.field.Source)
		}
		if f.field.Value != f.want {
			t.Fatalf("%s = %q，期望设置里的 %q", f.name, f.field.Value, f.want)
		}
	}
	if len(eff.Model.OverriddenByEnv) != 0 {
		t.Fatalf("设置写了这三项时不该有环境覆盖，实际 %v", eff.Model.OverriddenByEnv)
	}

	// 启动路径也必须用设置里的端点，不是环境变量那个。
	setup, err := d.resolveModelSetup(context.Background())
	if err != nil {
		t.Fatalf("resolveModelSetup: %v", err)
	}
	if setup.Provider != "saved-prov" || setup.Model != "saved-model" {
		t.Fatalf("启动用的模型 = %s/%s，期望设置里的 saved-prov/saved-model", setup.Provider, setup.Model)
	}
	if !strings.Contains(string(setup.ModelsJSON), "http://saved.example/v1") {
		t.Fatalf("models.json 里没有设置里的 baseUrl: %s", setup.ModelsJSON)
	}
}

// TestModelEnvKeyIsNotLogged 守密钥"不落盘、不进日志"的另一半：日志。
func TestModelEnvKeyIsNotLogged(t *testing.T) {
	const secret = "{{API_KEY_m1env_logs}}"
	t.Setenv("ANVIL_PROVIDER", "envprov")
	t.Setenv("ANVIL_MODEL", "envmodel")
	t.Setenv("ANVIL_BASE_URL", "http://127.0.0.1:9/v1")
	t.Setenv("ANVIL_MODEL_API_KEY", secret)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	d, root := prepareModelEnvDesktop(t)
	if _, err := d.resolveModelSetup(context.Background()); err != nil {
		t.Fatalf("resolveModelSetup: %v", err)
	}
	_ = d.Settings()
	_ = d.EffectiveConfig()
	if err := d.SaveSettings(d.Settings()); err != nil {
		t.Fatalf("保存设置: %v", err)
	}
	logs := buf.String()
	if strings.Contains(logs, secret) {
		t.Fatalf("日志里出现了密钥明文: %s", logs)
	}
	if hits := scanForSecret(t, root, secret); len(hits) > 0 {
		t.Fatalf("配置目录能扫到密钥明文: %v", hits)
	}
}

// TestModelEnvUnsetKeepsTodaysBehaviour 守"谁都没说话时行为不变"。
//
// 两种情况分开断言，因为它们今天就是两种不同的事实：
//   - 没有配置文件：用内置默认值（fixture 端点），界面据此显示"未配置真模型"；
//   - 配置里显式清空了端点/模型：那是用户说的，仍然 CONFIG_MISSING（不被默认值掩盖）。
func TestModelEnvUnsetKeepsTodaysBehaviour(t *testing.T) {
	t.Run("没有配置文件时用默认值", func(t *testing.T) {
		d, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("构造 Desktop: %v", err)
		}
		t.Cleanup(d.Shutdown)

		eff := d.EffectiveConfig()
		if eff.Model.Provider.Source != SourceDefault || eff.Model.Provider.Value != "fixture" {
			t.Fatalf("provider = %+v，期望 default/fixture", eff.Model.Provider)
		}
		if eff.Model.BaseURL.Source != SourceDefault || eff.Model.BaseURL.Value != "http://127.0.0.1:0/v1" {
			t.Fatalf("baseUrl = %+v，期望默认占位端点（就绪检查据此拦住发送）", eff.Model.BaseURL)
		}
		if eff.Model.APIKeyRef.Source != SourceNone || eff.Model.APIKeyRef.Value != "" {
			t.Fatalf("apiKeyRef = %+v，没有环境密钥时应为空", eff.Model.APIKeyRef)
		}
		if len(eff.Model.OverriddenByEnv) != 0 {
			t.Fatalf("没有环境变量时不该有 OverriddenByEnv: %v", eff.Model.OverriddenByEnv)
		}
	})

	t.Run("显式清空的模型配置仍是 CONFIG_MISSING", func(t *testing.T) {
		root := t.TempDir()
		written := Settings{Workspace: filepath.Join(root, "ws")}
		if err := os.MkdirAll(written.Workspace, 0o755); err != nil {
			t.Fatalf("建工作目录: %v", err)
		}
		raw, err := json.Marshal(written)
		if err != nil {
			t.Fatalf("序列化设置: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "settings.json"), raw, 0o600); err != nil {
			t.Fatalf("写 settings.json: %v", err)
		}
		d, err := New(root)
		if err != nil {
			t.Fatalf("构造 Desktop: %v", err)
		}
		t.Cleanup(d.Shutdown)

		eff := d.EffectiveConfig()
		if eff.Model.Provider.Source != SourceSettings || eff.Model.Provider.Value != "" {
			t.Fatalf("显式清空的 provider = %+v，期望 settings/空（环境变量不得顶替）", eff.Model.Provider)
		}

		_, setupErr := d.resolveModelSetup(context.Background())
		var appErr *contracts.AppError
		if !asAppError(setupErr, &appErr) || appErr.Code != contracts.CodeConfigMissing {
			t.Fatalf("模型配置为空时应 CONFIG_MISSING，实际 %v", setupErr)
		}
	})
}

// argsContainPair 判断参数里是否有 flag 紧跟 value。
func argsContainPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
