package app

// C01 绑定层测试。
//
// 这里守的是"绑定层不该只是转发"的两件事：
// 一是启动前的信任闸门真的拦得住（不是靠 UI 灰按钮），
// 二是错误必须结构化带出 Code（前端按 Code 分支，不解析文案）。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/profile"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
)

func newTestDesktop(t *testing.T) (*Desktop, string) {
	t.Helper()
	root := t.TempDir()
	d, err := New(root)
	if err != nil {
		t.Fatalf("构造 Desktop: %v", err)
	}
	t.Cleanup(d.Shutdown)
	return d, root
}

func TestStartRejectsWithoutWorkspace(t *testing.T) {
	d, _ := newTestDesktop(t)
	// 默认设置会给出一个已创建但未信任的工作区；这里显式清空，
	// 单独覆盖"压根没配工作区"这条路径。
	d.settings.Workspace = ""

	_, err := d.Start("你好")
	if err == nil {
		t.Fatal("没有工作区时必须拒绝启动")
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr.Code != contracts.CodeConfigMissing {
		t.Fatalf("期望 CONFIG_MISSING，实际 %v", err)
	}
	if !strings.Contains(appErr.Message, "工作区") {
		t.Fatalf("错误要指明缺什么，实际 %q", appErr.Message)
	}
}

// TestStartRejectsUntrustedWorkspace 是 C01 的核心断言：
// 目录存在不等于允许执行代码，未信任必须在绑定层被拒。
func TestStartRejectsUntrustedWorkspace(t *testing.T) {
	d, root := newTestDesktop(t)
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	d.settings.Workspace = work

	_, err := d.Start("你好")
	if err == nil {
		t.Fatal("未信任的工作区必须拒绝启动")
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr.Code != contracts.CodeUnauthorized {
		t.Fatalf("期望 UNAUTHORIZED，实际 %v", err)
	}
	// 错误要给出可执行的下一步，而不是只说"不行"。
	if !strings.Contains(appErr.Message, "信任") {
		t.Fatalf("错误里应含修复路径，实际 %q", appErr.Message)
	}
}

// TestWorkspaceLifecycleThroughBindings 覆盖 UI01 需要的绑定面：
// 添加 → 列出 → 信任 → 回读状态。
func TestWorkspaceLifecycleThroughBindings(t *testing.T) {
	d, root := newTestDesktop(t)
	work := filepath.Join(root, "proj")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}

	created := d.CreateWorkspace(work)
	if created.Error != nil {
		t.Fatalf("添加工作区: %+v", created.Error)
	}
	if created.Item.Trusted {
		t.Fatal("新工作区默认未信任")
	}

	// 重复添加返回同一条，不制造第二个工作区。
	again := d.CreateWorkspace(work)
	if again.Error != nil {
		t.Fatalf("重复添加: %+v", again.Error)
	}
	if again.Item.ID != created.Item.ID {
		t.Fatalf("同一路径应复用同一工作区：%s ≠ %s", again.Item.ID, created.Item.ID)
	}

	list := d.ListWorkspaces()
	if list.Error != nil {
		t.Fatalf("列举: %+v", list.Error)
	}
	if len(list.Items) != 1 {
		t.Fatalf("应有 1 个工作区，实际 %d", len(list.Items))
	}

	trusted := d.SetWorkspaceTrust(string(created.Item.ID), true)
	if trusted.Error != nil {
		t.Fatalf("设置信任: %+v", trusted.Error)
	}
	if !trusted.Item.Trusted {
		t.Fatal("回读的信任状态应为 true")
	}

	// 信任后 Start 才不再被闸门拦住（这里仍会因没有 Pi 而失败，
	// 但错误码不该再是 UNAUTHORIZED）。
	d.settings.Workspace = work
	_, err := d.Start("你好")
	if err == nil {
		t.Fatal("没有可用的 Pi 时不应启动成功")
	}
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && appErr.Code == contracts.CodeUnauthorized {
		t.Fatalf("信任后不应再报未授权: %v", appErr)
	}
}

// TestStatusIsolationIsBoundToRuntimeSpec 守住"诊断不说谎"。
//
// 曾经 Status 自己拼一份 IsolationSpec、ensureRuntime 用另一份，结果是设置页
// 显示 `--no-mcp`、实际却启用了 harness MCP（审批与自有 MCP 都在工作）。
// 诊断信息说谎比没有诊断信息更糟——它会把人带去错误的方向。
func TestStatusIsolationIsBoundToRuntimeSpec(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 未启动：参数来自 isolationSpec() 这个唯一构造点。
	pre := d.Status()
	if strings.Contains(pre.Isolation, "--no-mcp") {
		t.Fatalf("harness MCP 是启用的，诊断不该出现 --no-mcp: %q", pre.Isolation)
	}
	if !strings.Contains(pre.Isolation, "-e builtin:mcp") {
		t.Fatalf("诊断应显示 -e builtin:mcp: %q", pre.Isolation)
	}
	for _, flag := range []string{"-na", "-nc", "-ne", "-ns", "-np", "--no-themes"} {
		if !strings.Contains(pre.Isolation, flag) {
			t.Fatalf("诊断缺少隔离参数 %s: %q", flag, pre.Isolation)
		}
	}

	ext := filepath.Join(t.TempDir(), "harness-policy.ts")
	if err := os.WriteFile(ext, []byte("// stub"), 0o600); err != nil {
		t.Fatalf("写策略扩展: %v", err)
	}

	// 已启动：展示值必须等于运行时那份 spec 的参数。
	// 这里故意让运行时 spec 与 isolationSpec(ext) 不同 —— 若 Status 又回去
	// 自己拼一份，两个断言就会互相矛盾，从而把回归暴露出来。
	runtimeSpec := pi.IsolationSpec{PolicyExtension: ext, HarnessMCP: false}
	svc := run.New(run.Options{Isolation: runtimeSpec})
	d.mu.Lock()
	d.svc = svc
	d.policyExt = ext
	d.mu.Unlock()

	got := d.Status().Isolation
	want := strings.Join(svc.IsolationArgs(), " ")
	if got != want {
		t.Fatalf("诊断展示必须等于实际启动参数：\n展示 %q\n实际 %q", got, want)
	}
	if !strings.Contains(got, ext) {
		t.Fatalf("诊断应显示真正加载的策略扩展 %s: %q", ext, got)
	}
}

// TestWorkspaceBindingReportsStructuredError 坏输入也要带 Code，不能是裸字符串。
func TestWorkspaceBindingReportsStructuredError(t *testing.T) {
	d, _ := newTestDesktop(t)
	res := d.CreateWorkspace(filepath.Join(t.TempDir(), "不存在"))
	if res.Error == nil {
		t.Fatal("不存在的目录应失败")
	}
	if res.Error.Code != contracts.CodeNotFound {
		t.Fatalf("期望 NOT_FOUND，实际 %s（%s）", res.Error.Code, res.Error.Message)
	}
}

// TestProfileConflictThroughBindings 覆盖"外部修改后保存展示冲突和差异，不能覆盖"。
func TestProfileConflictThroughBindings(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 模拟资源库已定义模型（C02 之后由模型服务写入）。
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		t.Fatalf("配置存储: %v", err)
	}
	if _, err := store.CompareAndSwap(context.Background(), config.KeyUser, 0,
		[]byte(`{"schemaVersion":1,"models":[{"id":"m-gpt"}]}`)); err != nil {
		t.Fatalf("写入模型: %v", err)
	}

	first := d.SaveProfile("p1", "ws-1", 0,
		profile.Profile{Name: "初版", ModelRef: "m-gpt"})
	if first.Error != nil {
		t.Fatalf("首次保存: %+v", first.Error)
	}

	// 另一个窗口改了 name 和模型。
	other := d.SaveProfile("p1", "ws-1", first.Item.Revision,
		profile.Profile{Name: "别人的改动", ModelRef: "m-gpt", ToolPolicy: profile.ToolPolicy{Filesystem: "read"}})
	if other.Error != nil {
		t.Fatalf("第二个 writer: %+v", other.Error)
	}

	// 我的保存基于旧 revision：必须冲突。
	mine := profile.Profile{Name: "我的改动", ModelRef: "m-gpt"}
	conflicted := d.SaveProfile("p1", "ws-1", first.Item.Revision, mine)
	if conflicted.Error == nil {
		t.Fatal("过期 revision 的保存必须失败")
	}
	if conflicted.Error.Code != contracts.CodeConfigConflict {
		t.Fatalf("期望 CONFIG_CONFLICT，实际 %s", conflicted.Error.Code)
	}

	// 冲突后能拿到差异，且差异里点出被改的字段。
	diff := d.ProfileConflict("p1", mine)
	if diff.Error != nil {
		t.Fatalf("取冲突差异: %+v", diff.Error)
	}
	if diff.Current.Name != "别人的改动" {
		t.Fatalf("差异里的当前版本应是磁盘上的版本，实际 %q", diff.Current.Name)
	}
	var sawName, sawPolicy bool
	for _, f := range diff.Diff {
		if f.Field == "name" {
			sawName = true
			if f.Theirs != "别人的改动" {
				t.Fatalf("name 差异的对方值不对: %+v", f)
			}
		}
		if f.Field == "toolPolicy" {
			sawPolicy = true
		}
	}
	if !sawName || !sawPolicy {
		t.Fatalf("差异应包含 name 与 toolPolicy，实际 %+v", diff.Diff)
	}

	// 源文件没被我的保存覆盖。
	cur := d.GetProfile("p1")
	if cur.Error != nil {
		t.Fatalf("读取: %+v", cur.Error)
	}
	if cur.Item.Name != "别人的改动" {
		t.Fatalf("冲突不得覆盖源文件，实际 %q", cur.Item.Name)
	}
}
