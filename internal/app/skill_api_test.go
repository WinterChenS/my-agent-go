package app

// C03 绑定层测试：守 RES01–RES04 里"必须真的落到运行时"的那几条。
//
// 这里不重复 resources 包的单测，只测绑定层特有的行为：
//  1. 启停/编辑技能会改变真实传给 Pi 的参数（不是只改菜单）；
//  2. 有 run 在跑时改技能不会打断它，界面如实显示"下次生效"；
//  3. 系统策略扩展不可关闭。

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

// fixtureSkill 在临时目录造一个可导入的技能，返回其目录。
func fixtureSkill(t *testing.T, name, desc string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建技能目录: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n按步骤执行。\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("写 SKILL.md: %v", err)
	}
	return dir
}

// prepareRuntimeDesktop 让 Desktop 具备构建运行时的最低条件：
// 一个"存在"的 Pi 入口、一个策略扩展、一个工作目录。
func prepareRuntimeDesktop(t *testing.T) *Desktop {
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
	t.Setenv("MYAGENT_POLICY_EXT", policy)

	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("建工作目录: %v", err)
	}
	d.mu.Lock()
	d.settings.PiPath = piPath
	d.settings.Workspace = work
	d.settings.Provider = "fixture"
	d.settings.Model = "fixture-model"
	d.settings.BaseURL = "http://127.0.0.1:1/v1"
	d.mu.Unlock()
	return d
}

// TestSkillLifecycleThroughBindings 覆盖导入→列举→禁用→删除的真实落盘。
func TestSkillLifecycleThroughBindings(t *testing.T) {
	d, root := newTestDesktop(t)
	ctx := context.Background()

	src := fixtureSkill(t, "demo-skill", "绑定层测试用")
	imported := d.ImportSkill(src, "user", "")
	if imported.Error != nil {
		t.Fatalf("导入失败: %s: %s", imported.Error.Code, imported.Error.Message)
	}
	if imported.Item.ID != "demo-skill" || !imported.Item.Enabled || !imported.Item.Available {
		t.Fatalf("导入结果不对: %+v", imported.Item)
	}
	// 真实落盘在产品托管目录里，而不是源目录旁边。
	wantPath := filepath.Join(root, "skills", "demo-skill", "SKILL.md")
	if imported.Item.Path != wantPath {
		t.Fatalf("托管路径 = %s，期望 %s", imported.Item.Path, wantPath)
	}

	list := d.ListSkills()
	if list.Error != nil {
		t.Fatalf("列举失败: %s", list.Error.Message)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "demo-skill" {
		t.Fatalf("列表内容不对: %+v", list.Items)
	}

	read := d.ReadSkill("demo-skill")
	if read.Error != nil {
		t.Fatalf("读取内容失败: %s", read.Error.Message)
	}
	if !strings.Contains(read.Content, "description: 绑定层测试用") {
		t.Fatalf("内容不对: %q", read.Content)
	}

	disabled := d.SetSkillEnabled("demo-skill", list.Revision, false)
	if disabled.Error != nil {
		t.Fatalf("禁用失败: %s", disabled.Error.Message)
	}
	if disabled.Item.Enabled {
		t.Fatal("禁用后仍显示启用")
	}

	list = d.ListSkills()
	if del := d.DeleteSkill(list.Revision, "demo-skill"); del.Error != nil {
		t.Fatalf("删除失败: %s", del.Error.Message)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("删除后文件仍在: %v", err)
	}
	if got := len(d.ListSkills().Items); got != 0 {
		t.Fatalf("删除后列表应为空，实际 %d 条", got)
	}
	_ = ctx
}

// TestSkillToggleChangesRuntimeArgs 守 RES02/RES04 的核心：
// 启停必须真的改变交给 Pi 的 --skill 参数，而不是只改界面。
func TestSkillToggleChangesRuntimeArgs(t *testing.T) {
	d := prepareRuntimeDesktop(t)

	src := fixtureSkill(t, "args-skill", "参数断言用")
	if res := d.ImportSkill(src, "user", ""); res.Error != nil {
		t.Fatalf("导入失败: %s", res.Error.Message)
	}

	svc, err := d.ensureRuntime()
	if err != nil {
		t.Fatalf("构建运行时失败: %v", err)
	}
	args := strings.Join(svc.IsolationArgs(), " ")
	if !strings.Contains(args, "--skill") {
		t.Fatalf("启用技能后参数里应出现 --skill，实际: %s", args)
	}
	if !strings.Contains(args, "args-skill") {
		t.Fatalf("参数里应包含该技能路径，实际: %s", args)
	}
	// 加了技能不代表放开"项目级可执行资源"的其它切断（-na/-nc/-ne 仍必须在）。
	// 技能自动发现按 2026-11-04 方向有意保持打开，不应再出现 -ns。
	if strings.Contains(args, "-ns") {
		t.Fatalf("不应再带 -ns（本地技能自动发现按设计保持打开）: %s", args)
	}
	for _, flag := range []string{"-na", "-nc", "-ne", "-np", "--no-themes"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("加载技能时其余隔离参数仍必须在，缺 %s，实际: %s", flag, args)
		}
	}

	list := d.ListSkills()
	if res := d.SetSkillEnabled("args-skill", list.Revision, false); res.Error != nil {
		t.Fatalf("禁用失败: %s", res.Error.Message)
	}

	svc2, err := d.ensureRuntime()
	if err != nil {
		t.Fatalf("重建运行时失败: %v", err)
	}
	if svc2 == svc {
		t.Fatal("禁用技能后运行时没有重建：Pi 仍会加载已禁用的技能")
	}
	args2 := strings.Join(svc2.IsolationArgs(), " ")
	if strings.Contains(args2, "args-skill") {
		t.Fatalf("禁用后参数里不应再有该技能，实际: %s", args2)
	}

	// RES04 的外部观察（filesystem）：产品写进隔离 agentDir 的配置，从磁盘读回。
	// 事实只取磁盘上的字节，不复述上面的内存断言（那些断言自己会失败）。
	if facts := observeAgentConfig(t, d.agentDir()); facts != nil {
		writeAppObservation(t, facts, nil)
	}
}

// TestSkillChangeIsDeferredDuringRun 守 RES02 的"运行中沿用旧 revision 且如实显示"。
func TestSkillChangeIsDeferredDuringRun(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	if res := d.ImportSkill(fixtureSkill(t, "busy-skill", "运行中改启用"), "user", ""); res.Error != nil {
		t.Fatalf("导入失败: %s", res.Error.Message)
	}

	// 先按正常路径建出运行时（此时没有 run 在跑）。
	if _, err := d.ensureRuntime(); err != nil {
		t.Fatalf("构建运行时失败: %v", err)
	}

	// 换成一个"有活跃 run"的替身，保留 runtimeKey：
	// 这条测试关心的是"有 run 在跑时 ensureRuntime 会不会把它关掉"。
	work := t.TempDir()
	// 替身服务也必须带策略扩展：没有它的 IsolationSpec 会被 Validate 拒绝，
	// 那不是这条测试要证明的事。
	stubPolicy := filepath.Join(work, "harness-policy.ts")
	if err := os.WriteFile(stubPolicy, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写策略扩展桩: %v", err)
	}
	stubSvc := run.New(run.Options{
		WorkspaceID:  "ws-test",
		SessionID:    "sess-test",
		WorkspaceDir: work,
		AgentDir:     filepath.Join(work, "agent"),
		Installation: pi.PiInstallation{ID: "test:pi", Executable: filepath.Join(work, "pi.exe")},
		Isolation:    pi.IsolationSpec{PolicyExtension: stubPolicy},
		StartWorker: func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
			return newStubWorker(), nil
		},
	})
	t.Cleanup(func() { _ = stubSvc.Close(context.Background()) })
	if _, err := stubSvc.Start(context.Background(), run.StartRunRequest{Message: "跑着"}); err != nil {
		t.Fatalf("启动 run: %v", err)
	}
	if !eventuallyTrue(2*time.Second, func() bool { return stubSvc.ActiveCount() == 1 }) {
		t.Fatal("run 没有进入活跃状态")
	}
	d.mu.Lock()
	d.svc = stubSvc
	d.mu.Unlock()

	list := d.ListSkills()
	if res := d.SetSkillEnabled("busy-skill", list.Revision, false); res.Error != nil {
		t.Fatalf("禁用失败: %s", res.Error.Message)
	}

	svc, err := d.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime 失败: %v", err)
	}
	if svc != stubSvc {
		t.Fatal("改技能把正在跑的运行时关掉了")
	}
	if stubSvc.ActiveCount() != 1 {
		t.Fatalf("活跃 run 数不应变化，实际 %d", stubSvc.ActiveCount())
	}

	// 界面必须能说清"还没生效"。
	after := d.ListSkills()
	if !after.Pending {
		t.Fatal("有 run 在跑时改技能必须标为待生效，否则界面在骗人")
	}
	if !after.ActiveRun {
		t.Fatal("ActiveRun 应为 true")
	}
	if status := d.Status(); !status.SkillsPending {
		t.Fatal("状态里的 SkillsPending 应为 true")
	}
	// 运行中的技能集合仍是旧的（含 busy-skill）。
	if len(after.RuntimeSkills) != 1 || after.RuntimeSkills[0] != "busy-skill" {
		t.Fatalf("运行中的技能应保持旧快照，实际 %v", after.RuntimeSkills)
	}
}

// TestSystemExtensionCannotBeDisabledThroughBindings 守 RES04：
// 拒绝发生在绑定层，不是靠界面不画按钮。
func TestSystemExtensionCannotBeDisabledThroughBindings(t *testing.T) {
	d := prepareRuntimeDesktop(t)

	list := d.ListExtensions()
	if list.Error != nil {
		t.Fatalf("列举扩展失败: %s", list.Error.Message)
	}
	var found bool
	for _, e := range list.Items {
		if e.System {
			found = true
			if e.CanDisable || !e.Enabled {
				t.Fatalf("系统扩展应为不可关闭且启用，实际 %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("列表里必须有系统策略扩展")
	}

	res := d.SetExtensionEnabled(resources.SystemExtensionID, list.Revision, false)
	if res.Error == nil {
		t.Fatal("通过绑定层关闭系统扩展必须失败")
	}
	if !strings.Contains(res.Error.Message, "不可关闭") {
		t.Fatalf("错误应说明不可关闭，实际 %q", res.Error.Message)
	}
}

// ---------------------------------------------------------------- U04：链接导入与试运行

// dryRunStubWorker 在收到 prompt 后依次发一条文本增量与 agent_settled。
//
// 真实 Pi 的完成信号就是 agent_settled（run 包里唯一的终态依据），
// 所以试运行的收尾逻辑必须能被这条事件驱动，而不是靠 sleep 或轮询猜。
type dryRunStubWorker struct {
	events chan pi.RawEvent
	once   sync.Once
	calls  int
}

func newDryRunStubWorker() *dryRunStubWorker {
	return &dryRunStubWorker{events: make(chan pi.RawEvent, 8)}
}

func (w *dryRunStubWorker) Call(ctx context.Context, command json.RawMessage) (json.RawMessage, error) {
	w.calls++
	if w.calls == 1 {
		w.events <- pi.RawEvent{
			Type:   "message_update",
			Record: json.RawMessage(`{"assistantMessageEvent":{"type":"text_delta","delta":"试运行输出"}}`),
		}
		w.events <- pi.RawEvent{
			Type:   pi.TypeAgentSettled,
			Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`),
		}
	}
	return json.RawMessage(`{"type":"response","success":true}`), nil
}

func (w *dryRunStubWorker) Events() <-chan pi.RawEvent { return w.events }
func (w *dryRunStubWorker) Close(ctx context.Context) error {
	w.once.Do(func() { close(w.events) })
	return nil
}
func (w *dryRunStubWorker) PID() int                   { return 1 }
func (w *dryRunStubWorker) StderrTail() (string, bool) { return "", false }
func (w *dryRunStubWorker) ExitCode() (int, bool)      { return 0, false }
func (w *dryRunStubWorker) Limitations() []string      { return nil }

// resolverFunc / peerConn 是绑定层测试里的最小替身。
type resolverFunc func(ctx context.Context, host string) ([]net.IP, error)

func (f resolverFunc) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return f(ctx, host)
}

type peerConn struct {
	net.Conn
	peer net.Addr
}

func (c *peerConn) RemoteAddr() net.Addr { return c.peer }

// TestCreateSkillThroughBinding 覆盖本地新建在绑定层的落盘。
func TestCreateSkillThroughBinding(t *testing.T) {
	d, root := newTestDesktop(t)
	res := d.CreateSkill("local-skill", "user", "")
	if res.Error != nil {
		t.Fatalf("新建失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	if res.Item.ID != "local-skill" || !res.Item.Available || res.Item.Origin != resources.OriginCreated {
		t.Fatalf("新建结果不对: %+v", res.Item)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "local-skill", "SKILL.md")); err != nil {
		t.Fatalf("文件没落盘: %v", err)
	}
	if dup := d.CreateSkill("local-skill", "user", ""); dup.Error == nil || dup.Error.Code != contracts.CodeConfigConflict {
		t.Fatalf("重名必须返回 CONFIG_CONFLICT，实际 %+v", dup.Error)
	}
}

// TestInspectSkillLinkRejectsLoopbackAndKeepsManagedDir：
// 本机地址是最常见的 SSRF 目标，必须在绑定层就被拒。
func TestInspectSkillLinkRejectsLoopbackAndKeepsManagedDir(t *testing.T) {
	d, root := newTestDesktop(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("---\nname: loop\ndescription: 本机地址\n---\n"))
	}))
	t.Cleanup(srv.Close)

	res := d.InspectSkillLink(srv.URL + "/SKILL.md")
	if len(res.Rejections) == 0 {
		t.Fatalf("本机地址必须被拒绝，实际 %+v", res)
	}
	if res.Rejections[0].Rule != resources.LinkRuleSpecialPurpose {
		t.Fatalf("拒绝规则 = %s，期望 %s", res.Rejections[0].Rule, resources.LinkRuleSpecialPurpose)
	}
	if res.StagedPath != "" {
		t.Fatalf("被拒绝时不应给出暂存路径: %s", res.StagedPath)
	}
	if _, err := os.Stat(filepath.Join(root, "skills")); !os.IsNotExist(err) {
		t.Fatalf("检查链接不应触碰托管目录: %v", err)
	}
}

// TestInspectSkillLinkImportsInspectedCopy 覆盖"检查 → 确认导入 → 暂存清理"。
//
// 解析器与拨号器被替换成"解析到公网地址、实际连本机 TLS 服务"：
// 校验逻辑仍然是真的，只是把终点换成了可观测的本地服务。
func TestInspectSkillLinkImportsInspectedCopy(t *testing.T) {
	d, root := newTestDesktop(t)
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	w, err := zw.Create("demo/SKILL.md")
	if err != nil {
		t.Fatalf("建 zip: %v", err)
	}
	_, _ = w.Write([]byte("---\nname: linked-skill\ndescription: 来自链接\n---\n正文\n"))
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip: %v", err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body.Bytes())
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	public := net.ParseIP("93.184.216.34")
	addr := srv.Listener.Addr().String()
	original := newSkillLinkInspector
	newSkillLinkInspector = func(appRoot string) *resources.LinkInspector {
		in := resources.NewLinkInspector(appRoot)
		in.RootCAs = pool
		in.Resolve = resolverFunc(func(ctx context.Context, host string) ([]net.IP, error) {
			return []net.IP{public}, nil
		})
		in.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			conn, dialErr := dialer.DialContext(ctx, network, addr)
			if dialErr != nil {
				return nil, dialErr
			}
			return &peerConn{Conn: conn, peer: &net.TCPAddr{IP: public, Port: 443}}, nil
		}
		return in
	}
	t.Cleanup(func() { newSkillLinkInspector = original })

	insp := d.InspectSkillLink("https://example.com/demo.zip")
	if insp.Error != nil {
		t.Fatalf("检查失败: %s: %s", insp.Error.Code, insp.Error.Message)
	}
	if len(insp.Rejections) != 0 {
		t.Fatalf("合法链接被拒: %+v", insp.Rejections)
	}
	if insp.Name != "linked-skill" || insp.StagedPath == "" {
		t.Fatalf("检查结果不完整: %+v", insp)
	}
	imported := d.ImportSkill(insp.StagedPath, "user", "")
	if imported.Error != nil {
		t.Fatalf("导入失败: %s: %s", imported.Error.Code, imported.Error.Message)
	}
	if imported.Item.ID != "linked-skill" {
		t.Fatalf("导入结果不对: %+v", imported.Item)
	}
	entries, err := os.ReadDir(filepath.Join(root, "skill-links"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("导入成功后仍残留暂存目录: %v", entries)
	}
}

// TestDryRunSkillUsesIsolatedSandboxAndCleansUp 是 RES05 的核心断言。
func TestDryRunSkillUsesIsolatedSandboxAndCleansUp(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	var (
		mu    sync.Mutex
		specs []pi.WorkerSpec
	)
	d.mu.Lock()
	d.workerStarter = func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		mu.Lock()
		specs = append(specs, spec)
		mu.Unlock()
		return newDryRunStubWorker(), nil
	}
	work := d.settings.Workspace
	d.mu.Unlock()

	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s", ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s", res.Error.Message)
	}
	if imp := d.ImportSkill(fixtureSkill(t, "dry-skill", "试运行绑定层测试"), "user", ""); imp.Error != nil {
		t.Fatalf("导入失败: %s: %s", imp.Error.Code, imp.Error.Message)
	}

	userBefore, err := resources.TreeDigest(d.agentDir(), work)
	if err != nil {
		t.Fatalf("计算用户数据 hash: %v", err)
	}

	res := d.DryRunSkill("dry-skill", "按技能给我一个计划")
	if res.Error != nil {
		t.Fatalf("试运行失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	if res.State != contracts.RunStateCompleted {
		t.Fatalf("试运行状态 = %s，期望 completed", res.State)
	}
	if !strings.Contains(res.Output, "试运行输出") {
		t.Fatalf("试运行没有返回模型输出: %q", res.Output)
	}
	if !res.TempCleaned {
		t.Fatal("试运行结束后必须清理临时目录")
	}
	if !res.SkillUnchanged {
		t.Fatal("试运行不应改动托管技能")
	}
	if !res.UserStateUnchanged {
		t.Fatal("试运行不应改动用户 agentDir/工作区")
	}
	if res.Policy.Filesystem != "read" || res.Policy.Network || res.Policy.Shell {
		t.Fatalf("试运行策略不是只读: %+v", res.Policy)
	}

	mu.Lock()
	if len(specs) != 1 {
		mu.Unlock()
		t.Fatalf("期望启动 1 个 worker，实际 %d", len(specs))
	}
	spec := specs[0]
	mu.Unlock()
	if spec.AgentDir == d.agentDir() {
		t.Fatalf("试运行用了用户真实 agentDir: %s", spec.AgentDir)
	}
	if spec.WorkingDir == work {
		t.Fatalf("试运行用了用户真实工作区: %s", spec.WorkingDir)
	}
	if !strings.Contains(spec.AgentDir, "dryrun-") {
		t.Fatalf("试运行 agentDir 不在临时沙箱里: %s", spec.AgentDir)
	}
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "--no-mcp") {
		t.Fatalf("试运行隔离参数缺少 --no-mcp: %s", joined)
	}
	if strings.Contains(joined, "builtin:mcp") {
		t.Fatalf("试运行不应重新启用内置 MCP: %s", joined)
	}
	if strings.Count(joined, "--skill") != 1 || !strings.Contains(joined, "dry-skill") {
		t.Fatalf("试运行应只显式加载待测技能: %s", joined)
	}
	if _, err := os.Stat(spec.AgentDir); !os.IsNotExist(err) {
		t.Fatalf("临时 agentDir 未清理: %v", err)
	}

	userAfter, err := resources.TreeDigest(d.agentDir(), work)
	if err != nil {
		t.Fatalf("再次计算用户数据 hash: %v", err)
	}
	for path, digest := range userBefore {
		if userAfter[path] != digest {
			t.Fatalf("用户数据被改动: %s", path)
		}
	}
}

// TestDryRunSkillBlocksWithoutPrompt：没有 prompt 就不启动任何进程。
func TestDryRunSkillBlocksWithoutPrompt(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	if imp := d.ImportSkill(fixtureSkill(t, "no-prompt", "缺 prompt"), "user", ""); imp.Error != nil {
		t.Fatalf("导入失败: %s", imp.Error.Message)
	}
	res := d.DryRunSkill("no-prompt", "   ")
	if res.State != "blocked" || res.Error == nil || res.Error.Code != contracts.CodeUnsupported {
		t.Fatalf("空 prompt 必须 blocked: %+v", res)
	}
	if res.TempCleaned {
		t.Fatal("没有创建沙箱时不应声称清理过")
	}
}
