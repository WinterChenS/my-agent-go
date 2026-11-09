package policy

// 权限档位（ask / full_access）的判定口径。
//
// 规格：docs/specs/2026-11-08-permission-mode-full-access.md。
//
// 这里只钉住两件容易搞混的事：
//   1. full_access 只跳过**人工审批**这一步，权限硬边界一条都不放宽；
//   2. 档位是 per-run 钉住的：同一条 run 前后步骤一致，运行中切开关只影响下一条。

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// autoAllowService 构造一个“权限足够、但每个写动作本来都要审批”的服务。
//
// shell 已授予、文件系统限制在工作区：只有这样才测得出档位**只**跳过审批，
// 而不是靠“反正本来也没权限”蒙混过去。
func autoAllowService(t *testing.T, autoAllow bool) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	guard, err := NewPathGuard(root)
	if err != nil {
		t.Fatalf("建路径守卫失败: %v", err)
	}
	svc := New(Options{
		Permissions: config.Permissions{Filesystem: "workspace", Network: false, Shell: true},
		PathGuard:   guard,
		AutoAllow:   autoAllow,
		ApprovalTTL: 60 * time.Second,
		Now:         newClock().Now,
	})
	return svc, root
}

// writeIn 构造一次工作区内的写入调用（本来一定会 ask）。
func writeIn(root, name string) ToolCall {
	return ToolCall{
		ID:            "call-" + name,
		Name:          name,
		Source:        SourceBuiltin,
		Arguments:     jsonArgs(`{"path":"a.txt","content":"你好"}`),
		Origin:        "user",
		ResourceScope: filepath.Join(root, "a.txt"),
	}
}

func execFor(runID, session string) contracts.ExecutionContext {
	return contracts.ExecutionContext{
		WorkspaceID:       "ws",
		SessionID:         contracts.SessionID(session),
		RunID:             contracts.RunID(runID),
		ConfigRevision:    3,
		ControlGeneration: 1,
	}
}

// TestAutoAllowSkipsOnlyHumanApproval：同一批调用在 ask 档位下 ask、在 AutoAllow 下
// allow 且原因可识别（ReasonAutoAllowed），这样调用方/诊断能区分“人批的”和“档位放的”。
func TestAutoAllowSkipsOnlyHumanApproval(t *testing.T) {
	ctx := context.Background()

	askSvc, askRoot := autoAllowService(t, false)
	autoSvc, autoRoot := autoAllowService(t, true)

	for _, tool := range []string{"write", "bash"} {
		res, err := askSvc.Authorize(ctx, execFor("run-ask", "s1"), writeIn(askRoot, tool))
		if err != nil {
			t.Fatalf("%s: Authorize(ask) 出错: %v", tool, err)
		}
		if res.Decision != DecisionAsk || res.ApprovalID == "" {
			t.Fatalf("%s: 默认档位必须签发审批，实际 %+v", tool, res)
		}

		res, err = autoSvc.Authorize(ctx, execFor("run-auto", "s1"), writeIn(autoRoot, tool))
		if err != nil {
			t.Fatalf("%s: Authorize(full_access) 出错: %v", tool, err)
		}
		if res.Decision != DecisionAllow {
			t.Fatalf("%s: full_access 下应直接 allow，实际 %+v", tool, res)
		}
		if res.Reason != ReasonAutoAllowed {
			t.Fatalf("%s: 自动放行的原因必须可识别，实际 %q", tool, res.Reason)
		}
		if res.ApprovalID != "" {
			t.Fatalf("%s: 自动放行不得签发审批（否则前端还会弹卡），实际 %q", tool, res.ApprovalID)
		}
	}
}

// TestAutoAllowKeepsPermissionBoundaries：越权调用在 full_access 下仍然 deny。
//
// 五条边界各测一次：工作区外文件、未授予 shell、未授予网络、未知通道、未受信任项目来源。
func TestAutoAllowKeepsPermissionBoundaries(t *testing.T) {
	ctx := context.Background()
	svc, root := autoAllowService(t, true)
	outside := filepath.Join(filepath.Dir(root), "outside "+root[len(root)-4:]+".txt")

	// 1) 工作区外文件系统：越界在进入审批环节之前就拒绝，与档位无关。
	res, err := svc.Authorize(ctx, execFor("run-auto", "s1"), ToolCall{
		ID: "c-out", Name: "write", Source: SourceBuiltin,
		Arguments:     jsonArgs(`{"path":"../x.txt","content":"x"}`),
		Origin:        "user",
		ResourceScope: outside,
	})
	if err != nil {
		t.Fatalf("Authorize(越界写) 出错: %v", err)
	}
	if res.Decision != DecisionDeny {
		t.Fatalf("工作区外写入必须 deny（full_access 不放宽范围），实际 %+v", res)
	}
	if !strings.Contains(res.Reason, "工作区") && !strings.Contains(res.Reason, "越界") {
		t.Fatalf("拒绝原因应说清是范围问题，实际 %q", res.Reason)
	}

	// 2) 未授予 shell：档位不能把权限开关顶掉。
	noShell := New(Options{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: false},
		AutoAllow:   true,
		Now:         newClock().Now,
	})
	res, err = noShell.Authorize(ctx, execFor("run-auto", "s1"), ToolCall{
		ID: "c-sh", Name: "bash", Source: SourceBuiltin,
		Arguments: jsonArgs(`{"command":"echo hi"}`), Origin: "user",
	})
	if err != nil {
		t.Fatalf("Authorize(未授予 shell) 出错: %v", err)
	}
	if res.Decision != DecisionDeny || !strings.Contains(res.Reason, "shell") {
		t.Fatalf("未授予 shell 必须 deny，实际 %+v", res)
	}

	// 3) 未授予网络：同上，出网工具不被档位放行。
	res, err = svc.Authorize(ctx, execFor("run-auto", "s1"), ToolCall{
		ID: "c-net", Name: "http_get", Source: SourceExtension,
		Arguments: jsonArgs(`{"url":"https://example.com"}`), Origin: "user",
	})
	if err != nil {
		t.Fatalf("Authorize(未授予网络) 出错: %v", err)
	}
	if res.Decision != DecisionDeny || !strings.Contains(res.Reason, "网络") {
		t.Fatalf("未授予网络必须 deny，实际 %+v", res)
	}

	// 4) 未知工具通道：不认识就是不放行，档位不改变这一点。
	res, err = svc.Authorize(ctx, execFor("run-auto", "s1"), ToolCall{
		ID: "c-chan", Name: "mystery", Source: Source("mystery-channel"),
		Arguments: jsonArgs(`{}`), Origin: "user",
	})
	if err != nil {
		t.Fatalf("Authorize(未知通道) 出错: %v", err)
	}
	if res.Decision != DecisionDeny || !strings.Contains(res.Reason, "未知工具通道") {
		t.Fatalf("未知通道必须 deny，实际 %+v", res)
	}

	// 5) 未受信任的项目来源：即使有 full_access，项目自动发现的东西也不执行。
	res, err = svc.Authorize(ctx, execFor("run-auto", "s1"), ToolCall{
		ID: "c-proj", Name: "mcp_custom", Source: SourceExternalMCP,
		Arguments: jsonArgs(`{}`), Origin: "project:" + root,
	})
	if err != nil {
		t.Fatalf("Authorize(未信任项目) 出错: %v", err)
	}
	if res.Decision != DecisionDeny || !strings.Contains(res.Reason, "未获信任") {
		t.Fatalf("未信任项目来源必须 deny，实际 %+v", res)
	}
}

// TestRunPermissionModeIsPinnedPerRun：档位按 run 钉住，互不影响；未登记回落服务默认。
func TestRunPermissionModeIsPinnedPerRun(t *testing.T) {
	ctx := context.Background()
	svc, root := autoAllowService(t, false)
	call := writeIn(root, "write")

	if err := svc.PinRunPermissionMode("run-full", config.PermissionModeFullAccess); err != nil {
		t.Fatalf("钉住 full_access 失败: %v", err)
	}
	if err := svc.PinRunPermissionMode("run-ask", config.PermissionModeAsk); err != nil {
		t.Fatalf("钉住 ask 失败: %v", err)
	}

	res, err := svc.Authorize(ctx, execFor("run-full", "s1"), call)
	if err != nil {
		t.Fatalf("Authorize(run-full) 出错: %v", err)
	}
	if res.Decision != DecisionAllow || res.Reason != ReasonAutoAllowed {
		t.Fatalf("full_access 的 run 应自动放行，实际 %+v", res)
	}

	res, err = svc.Authorize(ctx, execFor("run-ask", "s1"), call)
	if err != nil {
		t.Fatalf("Authorize(run-ask) 出错: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("ask 的 run 必须仍然签发审批，实际 %+v", res)
	}

	// 未登记的 run 回落服务默认（这里是 false = ask）：不允许“没登记就放肆”。
	res, err = svc.Authorize(ctx, execFor("run-unknown", "s1"), call)
	if err != nil {
		t.Fatalf("Authorize(未登记 run) 出错: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("未登记 run 必须按服务默认（ask）处理，实际 %+v", res)
	}

	// 服务级默认开着时，显式钉成 ask 的 run 仍然要审批（无人值守的口径）。
	svc.SetAutoAllow(true)
	res, err = svc.Authorize(ctx, execFor("run-ask", "s1"), call)
	if err != nil {
		t.Fatalf("Authorize(服务默认开、run 钉 ask) 出错: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("per-run 钉住的 ask 必须压过服务级默认，实际 %+v", res)
	}

	// 释放后回到服务默认：run 终止后不会留下“曾经放行过”的记录。
	svc.ReleaseRunPermissionMode("run-full")
	if _, ok := svc.RunPermissionMode("run-full"); ok {
		t.Fatal("释放后不应再有这条 run 的档位登记")
	}
	if got, ok := svc.RunPermissionMode("run-ask"); !ok || got != config.PermissionModeAsk {
		t.Fatalf("run-ask 的档位登记被误删: %q (存在=%v)", got, ok)
	}

	// 认不出的档位是错误，不是“按更宽处理”。
	if err := svc.PinRunPermissionMode("run-bad", "yolo"); err == nil {
		t.Fatal("未知档位必须拒绝钉住")
	}
}

// TestAutoAllowObserverSeesNormalizedCall：留痕回调在**释放锁之后**被调用，
// 拿到的是已经归一过的调用（真实资源路径）与生效档位。
func TestAutoAllowObserverSeesNormalizedCall(t *testing.T) {
	ctx := context.Background()
	svc, root := autoAllowService(t, true)

	var (
		mu    sync.Mutex
		calls []ToolCall
		modes []string
	)
	svc.SetAutoAllowObserver(func(exec contracts.ExecutionContext, call ToolCall, mode string) {
		mu.Lock()
		calls = append(calls, call)
		modes = append(modes, mode)
		mu.Unlock()
	})

	call := writeIn(root, "write")
	if _, err := svc.Authorize(ctx, execFor("run-auto", "s1"), call); err != nil {
		t.Fatalf("Authorize 出错: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("每次自动放行必须通知一次，实际 %d 次", len(calls))
	}
	if modes[0] != config.PermissionModeFullAccess {
		t.Fatalf("通知里必须带上生效档位，实际 %q", modes[0])
	}
	if calls[0].ResourceScope != call.ResourceScope {
		t.Fatalf("通知里的资源范围应是归一后的真实路径 %q，实际 %q", call.ResourceScope, calls[0].ResourceScope)
	}

	// ask 档位下不得触发留痕（没有放行就没有可记的事）。
	svc.SetAutoAllow(false)
	if _, err := svc.Authorize(ctx, execFor("run-ask2", "s1"), writeIn(root, "write")); err != nil {
		t.Fatalf("Authorize(ask) 出错: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("ask 档位不得触发自动放行留痕，实际 %d 次", len(calls))
	}
}
