package scheduler

// 无人值守 run 的审批档位（规格 docs/specs/2026-11-08-permission-mode-full-access.md 第 4 条）。
//
// 定时任务旁边没有人答复审批：它必须**一律**按 ask 提交，即使设置里开着
// “允许完全访问”。否则用户白天开过一次开关，夜里每个任务都永久免审，
// 而且出事时没有任何人当场同意过。
//
// 断言点是 run 合同（StartRunRequest → ExecutionBinding 快照），不是日志行：
// 档位最终要进 run 的绑定快照，日志说什么不算证据。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
)

// TestScheduledDispatchAlwaysAsks：定时触发与“立即运行”都按 ask 提交。
func TestScheduledDispatchAlwaysAsks(t *testing.T) {
	now := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, now)

	sch := validSchedule()
	sch.ID = "sch-unattended"
	sch.Cron = "0 9 * * *"
	sch.TimeZone = "Etc/UTC"
	created := h.create(sch)
	h.clock.Set(created.NextScheduledAt)
	h.tick()

	occs := h.occurrences(created.ID)
	if len(occs) != 1 {
		t.Fatalf("应记录 1 条 occurrence，实际 %d", len(occs))
	}
	if occs[0].RunID == "" {
		t.Fatalf("派发失败，occurrence 没有 run：%+v", occs[0])
	}
	if got := h.fake.startCount(); got != 1 {
		t.Fatalf("应派发 1 次，实际 %d", got)
	}
	req := h.fake.lastStart()
	if req.PermissionMode != config.PermissionModeAsk {
		t.Fatalf("定时任务的 run 必须按 ask 提交（无人值守不得免审），实际 %q", req.PermissionMode)
	}
	if config.PermissionModeAutoApproves(req.PermissionMode) {
		t.Fatalf("无人值守的档位不得是自动放行，实际 %q", req.PermissionMode)
	}

	// 手动“立即运行”同样是无人值守：档位不因触发方式改变。
	manual := validSchedule()
	manual.ID = "sch-unattended-manual"
	manual.Cron = "0 3 * * *"
	manual.TimeZone = "Etc/UTC"
	createdManual := h.create(manual)
	h.clock.Set(createdManual.NextScheduledAt.Add(time.Minute))
	if _, err := h.svc.RunNow(context.Background(), createdManual.ID); err != nil {
		t.Fatalf("立即运行失败: %v", err)
	}
	if got := h.fake.lastStart().PermissionMode; got != config.PermissionModeAsk {
		t.Fatalf("立即运行也必须按 ask 提交，实际 %q", got)
	}
}

// TestScheduledRunKeepsAskWhenPolicyDefaultAutoAllows：调度侧不提供“跟随设置”的
// 开关——它写死 ask。这里用一个服务级默认 full_access 的执行侧来证明：
// 提交里若没钉 ask，这条 run 就会变成免审。
func TestScheduledRunKeepsAskWhenPolicyDefaultAutoAllows(t *testing.T) {
	tracker := &workerTracker{}
	runSvc, pol, work := newRunServiceDefaultingToFullAccess(t, tracker)
	h := newHarnessWithRunner(t, time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC), runSvc,
		func(o *Options) {
			o.WorkspaceRoot = func(contracts.WorkspaceID) (string, error) { return work, nil }
		})

	sch := validSchedule()
	sch.ID = "sch-unattended-real"
	sch.Cron = "0 9 * * *"
	sch.TimeZone = "Etc/UTC"
	created := h.create(sch)
	h.clock.Set(created.NextScheduledAt)
	h.tick()

	occs := h.occurrences(created.ID)
	if len(occs) != 1 || occs[0].RunID == "" {
		t.Fatalf("应派发一条真实 run，实际 %+v", occs)
	}
	binding, ok := runSvc.Binding(occs[0].RunID)
	if !ok {
		t.Fatalf("找不到 run %s 的 binding", occs[0].RunID)
	}
	if binding.PermissionMode != config.PermissionModeAsk || binding.AutoApproves() {
		t.Fatalf("无人值守 run 的绑定快照必须是 ask，实际 %q", binding.PermissionMode)
	}

	// 行为对照：同一份策略服务默认会放行，但这条 run 的调用仍然要人批。
	call := policy.ToolCall{
		ID: "c-write", Name: "write", Source: policy.SourceBuiltin,
		Arguments:     json.RawMessage(`{"path":"a.txt","content":"x"}`),
		Origin:        "user",
		ResourceScope: filepath.Join(work, "a.txt"),
	}
	res, err := pol.Authorize(context.Background(), binding.ExecutionContext, call)
	if err != nil {
		t.Fatalf("Authorize 出错: %v", err)
	}
	if res.Decision != policy.DecisionAsk {
		t.Fatalf("无人值守 run 的写动作必须仍然 ask，实际 %+v", res)
	}

	// 对照：交互提交在同一服务上仍可显式要 full_access——钉住的是这一条 run，
	// 不是整个服务。
	attended, err := runSvc.Start(context.Background(), run.StartRunRequest{
		Message:        "交互：完全访问",
		SessionID:      "sess-attended",
		WorkspaceID:    "ws-sched",
		WorkspaceDir:   work,
		PermissionMode: config.PermissionModeFullAccess,
	})
	if err != nil {
		t.Fatalf("交互提交失败: %v", err)
	}
	if b, _ := runSvc.Binding(attended.RunID); !b.AutoApproves() {
		t.Fatalf("交互 run 应保留 full_access，实际 %q", b.PermissionMode)
	}
}

// newRunServiceDefaultingToFullAccess 构造应用进程里那唯一的 run.Service，
// 但策略服务的**服务级默认**是 full_access（模拟“用户开着完全访问”）。
func newRunServiceDefaultingToFullAccess(t *testing.T, tracker *workerTracker) (*run.Service, *policy.Service, string) {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("建工作目录: %v", err)
	}
	ext := filepath.Join(dir, "harness-policy.ts")
	if err := os.WriteFile(ext, []byte("export default function(){}"), 0o644); err != nil {
		t.Fatalf("写策略扩展桩: %v", err)
	}
	guard, err := policy.NewPathGuard(work)
	if err != nil {
		t.Fatalf("构造路径守卫: %v", err)
	}
	perms := config.Permissions{Filesystem: "workspace"}
	pol := policy.New(policy.Options{
		Permissions: perms,
		PathGuard:   guard,
		ApprovalTTL: time.Minute,
		AutoAllow:   true,
		Now:         time.Now,
	})
	svc := run.New(run.Options{
		WorkspaceID:    "ws-sched",
		SessionID:      "sess-interactive-default",
		WorkspaceDir:   work,
		AgentDir:       filepath.Join(dir, "agent"),
		Installation:   pi.PiInstallation{ID: "test:pi", Executable: filepath.Join(dir, "pi.exe")},
		Isolation:      pi.IsolationSpec{PolicyExtension: ext},
		Policy:         pol,
		PolicyCeiling:  run.PolicyCeiling{Permissions: perms, Ref: "ceiling:workspace"},
		PolicySnapshot: run.PolicySnapshot{Permissions: perms},
		StartWorker:    tracker.starter(),
		ConfigRevision: 7,
		Now:            time.Now,
	})
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, pol, work
}
