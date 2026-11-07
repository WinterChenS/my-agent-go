package app

// U01 的工作台绑定层测试（UI05）。
//
// 两类断言缺一不可：
//   - 有数据时，卡片上的数字/条目必须能追溯到本地库（不是原型演示数字）；
//   - 来源读不出来时，那一段必须是 unavailable（Count 为 nil），不是 0——
//     0 会被当成"确实没有"，而读不出来需要用户修。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"my-agent-go/internal/search"
)

// writeRunUsage 直接写 run_usage（C07 诊断写入者的表），模拟真实运行落库。
func writeRunUsage(t *testing.T, d *Desktop, runID, sessionID, state, errorCode string, updated time.Time) {
	t.Helper()
	db, err := d.indexDB()
	if err != nil {
		t.Fatalf("indexDB: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `
		INSERT INTO run_usage(run_id, session_id, workspace_id, state, usage_availability,
		                     error_code, error_message, error_retryable, started_at, updated_at)
		VALUES(?, ?, 'ws-a', ?, 'unavailable', ?, '', 0, ?, ?)`,
		runID, sessionID, state, errorCode,
		updated.Add(-time.Minute).UTC().Format(time.RFC3339Nano),
		updated.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("写 run_usage: %v", err)
	}
}

// writeRunEvent 写一条 run_events（脱敏后的载荷）。
func writeRunEvent(t *testing.T, d *Desktop, runID string, seq int, typ, payload string, at time.Time) {
	t.Helper()
	db, err := d.indexDB()
	if err != nil {
		t.Fatalf("indexDB: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `
		INSERT INTO run_events(run_id, seq, event_id, session_id, type, occurred_at, payload, payload_truncated)
		VALUES(?, ?, ?, '', ?, ?, ?, 0)`,
		runID, seq, runID+"-"+typ, typ, at.UTC().Format(time.RFC3339Nano), payload); err != nil {
		t.Fatalf("写 run_events: %v", err)
	}
}

func TestWorkbenchSummaryReadsRealRunsApprovalsAndArtifacts(t *testing.T) {
	d, _ := newTestDesktop(t)
	now := time.Now().UTC()

	// 当前运行：一条 waiting_approval；历史：completed / failed。
	writeRunUsage(t, d, "run-wait", "sess-1", "waiting_approval", "", now)
	writeRunUsage(t, d, "run-done", "sess-2", "completed", "", now.Add(-2*time.Hour))
	writeRunUsage(t, d, "run-fail", "sess-3", "failed", "PROVIDER_ERROR", now.Add(-3*time.Hour))

	// 审批：ap-1 未答复，ap-2 已答复（答复过的不再出现）。
	writeRunEvent(t, d, "run-wait", 1, "approval.request",
		`{"approvalId":"ap-1","toolName":"shell"}`, now.Add(-30*time.Second))
	writeRunEvent(t, d, "run-wait", 2, "approval.request",
		`{"approvalId":"ap-2","toolName":"file.write"}`, now.Add(-20*time.Second))
	writeRunEvent(t, d, "run-wait", 3, "approval.resolved",
		`{"approvalId":"ap-2","choice":"allow_once"}`, now.Add(-10*time.Second))

	// 最近产物（C07 索引）。
	db, err := d.indexDB()
	if err != nil {
		t.Fatalf("indexDB: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(), `
		INSERT INTO artifacts(id, workspace_id, run_id, session_id, display_name, canonical_path,
		                     path_key, sha256, size_bytes, media_type, created_at, updated_at)
		VALUES('art-1', 'ws-a', 'run-done', 'sess-2', 'report.md', '/work/ws-a/report.md',
		       '/work/ws-a/report.md', 'sha', 42, 'text/markdown', ?, ?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("写 artifacts: %v", err)
	}

	sum := d.WorkbenchSummary()
	if sum.Error != nil {
		t.Fatalf("WorkbenchSummary: %+v", sum.Error)
	}

	// 当前运行：只有未结算的那一条。
	if sum.CurrentRuns.Availability != WorkbenchAvailable {
		t.Fatalf("当前运行可用性 = %q", sum.CurrentRuns.Availability)
	}
	if sum.CurrentRuns.Count == nil || *sum.CurrentRuns.Count != 1 {
		t.Fatalf("当前运行计数 = %v，期望 1", sum.CurrentRuns.Count)
	}
	if len(sum.Runs) != 1 || sum.Runs[0].RunID != "run-wait" {
		t.Fatalf("当前运行条目不对: %+v", sum.Runs)
	}

	// 待审批：请求过、没答复的那一条。
	if sum.PendingApprovals.Count == nil || *sum.PendingApprovals.Count != 1 {
		t.Fatalf("待审批计数 = %v，期望 1", sum.PendingApprovals.Count)
	}
	if len(sum.Approvals) != 1 || sum.Approvals[0].ApprovalID != "ap-1" {
		t.Fatalf("待审批条目不对: %+v", sum.Approvals)
	}
	if sum.Approvals[0].ToolName != "shell" {
		t.Fatalf("待审批缺少工具名: %+v", sum.Approvals[0])
	}

	// 健康：24 小时内的完成/失败计数与最近一次结论。
	if sum.Health.Availability != WorkbenchAvailable {
		t.Fatalf("健康可用性 = %q", sum.Health.Availability)
	}
	if sum.HealthInfo.Completed24h != 1 || sum.HealthInfo.Failed24h != 1 {
		t.Fatalf("健康计数不对: %+v", sum.HealthInfo)
	}
	if sum.HealthInfo.LastRunID != "run-wait" || sum.HealthInfo.LastState != "waiting_approval" {
		t.Fatalf("最近 run 结论不对: %+v", sum.HealthInfo)
	}

	// 产物：索引里有几条就是几条（不做磁盘核对）。
	if sum.RecentArtifacts.Count == nil || *sum.RecentArtifacts.Count != 1 {
		t.Fatalf("产物计数 = %v", sum.RecentArtifacts.Count)
	}
	if sum.Artifacts[0].DisplayName != "report.md" || sum.Artifacts[0].Size != 42 {
		t.Fatalf("产物条目不对: %+v", sum.Artifacts[0])
	}
}

func TestWorkbenchMarksUnreadableSourceUnavailableNotZero(t *testing.T) {
	d, root := newTestDesktop(t)
	// 先走一次索引库：真实使用中工作台打开时它已经就绪，这里显式摆出同一前提，
	// 才能把"只有一个来源坏掉"和"整个数据根都坏了"区分开。
	if _, err := d.indexDB(); err != nil {
		t.Fatalf("indexDB: %v", err)
	}

	// 让调度库打不开：停掉启动时拉起的调度服务并关掉它的库，然后把应用数据根
	// 指到一个"父路径是普通文件"的位置——建目录必然失败，schedulerService() 也就
	// 打不开 `scheduler.db`。其余来源（会话目录/产物/run 索引）用的是已经打开的
	// 句柄，不受影响，这正是要验证的"一段坏掉不连坐整页"。
	d.schedulerMu.Lock()
	svc := d.schedulerSvc
	db := d.schedulerDB
	d.schedulerSvc = nil
	d.schedulerDB = nil
	d.schedulerMu.Unlock()
	if svc != nil {
		_ = svc.Close(context.Background())
	}
	if db != nil {
		_ = db.Close()
	}
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("写阻塞文件: %v", err)
	}
	d.mu.Lock()
	d.root = filepath.Join(blocker, "db")
	d.mu.Unlock()

	sum := d.WorkbenchSummary()

	if sum.Schedules.Availability != WorkbenchUnavailable {
		t.Fatalf("调度库打不开时应为 unavailable，实际 %+v", sum.Schedules)
	}
	// 关键：不可用时不能给一个 0 充数。
	if sum.Schedules.Count != nil {
		t.Fatalf("不可用的段不能带计数（会被读成 0 条）：%+v", sum.Schedules)
	}
	if sum.Schedules.ReasonCode == "" {
		t.Fatalf("不可用必须带机器可读原因：%+v", sum.Schedules)
	}
	// 其它来源不受影响：一段坏掉不等于整页坏掉。
	if sum.RecentSessions.Availability != WorkbenchAvailable {
		t.Fatalf("会话来源不该被调度故障连坐：%+v", sum.RecentSessions)
	}
	if sum.Error != nil {
		t.Fatalf("单段失败不该升级为整页错误: %+v", sum.Error)
	}
}

func TestWorkbenchRecentSessionsFollowCatalogOrderAcrossRestart(t *testing.T) {
	d, root := newTestDesktop(t)
	wsDir := filepath.Join(root, "wb-ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatalf("建工作区目录: %v", err)
	}
	ws := d.CreateWorkspace(wsDir)
	if ws.Error != nil {
		t.Fatalf("CreateWorkspace: %+v", ws.Error)
	}
	setCurrentWorkspace(d, wsDir)

	mk := func(title string) string {
		t.Helper()
		created := d.CreateSession(string(ws.Item.ID))
		if created.Error != nil {
			t.Fatalf("CreateSession: %+v", created.Error)
		}
		renamed := d.RenameSession(string(created.Item.ID), created.Item.Revision, title)
		if renamed.Error != nil {
			t.Fatalf("RenameSession: %+v", renamed.Error)
		}
		return string(renamed.Item.ID)
	}
	first := mk("gamma 会话")
	second := mk("beta 会话")
	// 置顶优先：顺序由 C06 目录决定，工作台不自己排序。
	if pinned := d.PinSession(second, 2, true); pinned.Error != nil {
		t.Fatalf("PinSession: %+v", pinned.Error)
	}

	titles := func(sum WorkbenchSummaryResult) []string {
		out := make([]string, 0, len(sum.Sessions))
		for _, s := range sum.Sessions {
			out = append(out, s.Title)
		}
		return out
	}

	before := d.WorkbenchSummary()
	if got := titles(before); len(got) != 2 || got[0] != "beta 会话" || got[1] != "gamma 会话" {
		t.Fatalf("工作台顺序不对: %v", got)
	}

	// 模拟重启：同一个数据根重新装配 Desktop，顺序必须一致（来自库里，不是内存）。
	d.Shutdown()
	restarted, err := New(root)
	if err != nil {
		t.Fatalf("重启后构造 Desktop: %v", err)
	}
	t.Cleanup(restarted.Shutdown)
	after := restarted.WorkbenchSummary()
	if got := titles(after); len(got) != 2 || got[0] != "beta 会话" || got[1] != "gamma 会话" {
		t.Fatalf("重启后工作台顺序变了: %v", got)
	}
	if first == "" || first == second {
		t.Fatalf("会话 id 不该相同: %q %q", first, second)
	}
}

func TestWorkbenchSearchAvailabilityIsIndependentFromWorkbench(t *testing.T) {
	d, _ := newTestDesktop(t)
	// 搜索索引未建立与工作台无关：工作台照常可用，搜索显示 unavailable。
	if st := d.SearchIndexStatus(); st.Availability != search.AvailabilityUnavailable {
		t.Fatalf("初始搜索索引状态 = %+v", st)
	}
	sum := d.WorkbenchSummary()
	if sum.RecentSessions.Availability != WorkbenchAvailable {
		t.Fatalf("工作台不该被搜索索引状态影响: %+v", sum.RecentSessions)
	}
}
