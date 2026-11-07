package app

// C09 绑定层测试。
//
// 守三件事：
//   - 绑定层只做装配：CAS、cron/时区校验、唯一键、漏跑策略都在 internal/scheduler，
//     这里只验证"穿过绑定层之后行为与错误码不变"；
//   - 调度会真的开一个**普通会话**：它建在会话目录里，但 kind 是调度会话，
//     所以不会混进用户的聊天列表；
//   - 删除任务是软删除：已经产生的会话仍然在库里（SCH07）。
//
// 全部在 t.TempDir() 里跑：临时 appData、临时库，不碰真实配置。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
	"my-agent-go/internal/scheduler"
	"my-agent-go/internal/storage"
)

// scheduleFixture 准备一个已信任的工作区，并返回可用的调度路径。
func scheduleFixture(t *testing.T) (*Desktop, string, string) {
	t.Helper()
	d := prepareRuntimeDesktop(t)
	work := d.Settings().Workspace
	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	return d, work, string(ws.Item.ID)
}

// saveSchedule 保存一份任务并断言成功。
func saveSchedule(t *testing.T, d *Desktop, req ScheduleSaveRequest) scheduler.Schedule {
	t.Helper()
	res := d.SaveSchedule(req)
	if res.Error != nil {
		t.Fatalf("保存任务失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	return res.Item
}

// TestScheduleAPICreateEditPauseResumeDelete 覆盖 SCH02 的绑定层口径：
// 新建 → 列表可见 → CAS 编辑（旧 revision 被拒）→ 暂停/恢复 → 软删除后列表不可见。
func TestScheduleAPICreateEditPauseResumeDelete(t *testing.T) {
	d, work, wsID := scheduleFixture(t)

	created := saveSchedule(t, d, ScheduleSaveRequest{
		Name:         "每日待办整理",
		Cron:         "0 9 * * MON-FRI",
		TimeZone:     "Asia/Shanghai",
		WorkspaceID:  wsID,
		Prompt:       "整理今日待办并生成摘要",
		OutputTarget: "workspace:reports/daily.md",
		Enabled:      true,
	})
	if created.ID == "" {
		t.Fatal("新建任务必须返回 ID")
	}
	if created.PermissionRef != schedulerDefaultPermissionRef {
		t.Fatalf("未指定权限引用时应落到上限引用，实际 %q", created.PermissionRef)
	}
	if created.MisfirePolicy != scheduler.DefaultMisfirePolicy || created.CatchUpLimit != scheduler.DefaultCatchUpLimit {
		t.Fatalf("漏跑策略默认值不对: %+v", created)
	}
	if created.NextScheduledAt.IsZero() || !created.NextScheduledAt.After(time.Now().Add(-time.Hour)) {
		t.Fatalf("必须给出下一次唤醒时刻，实际 %s", created.NextScheduledAt)
	}

	list := d.ListSchedules()
	if list.Error != nil {
		t.Fatalf("ListSchedules: %+v", list.Error)
	}
	if len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("列表内容不对: %+v", list.Items)
	}

	// 编辑成功 → revision+1。
	created.Name = "每日待办整理（改）"
	edited := saveSchedule(t, d, ScheduleSaveRequest{
		ID: created.ID, Name: created.Name, Cron: created.Cron, TimeZone: created.TimeZone,
		WorkspaceID: wsID, Prompt: created.Prompt, Enabled: true,
		ExpectedRevision: created.Revision,
	})
	if edited.Revision != created.Revision+1 {
		t.Fatalf("编辑后 revision 应为 %d，实际 %d", created.Revision+1, edited.Revision)
	}

	// 拿旧 revision 再提交 → CONFIG_CONFLICT（前端据此提示"已被别处修改"）。
	conflict := d.SaveSchedule(ScheduleSaveRequest{
		ID: created.ID, Name: "并发编辑", Cron: created.Cron, TimeZone: created.TimeZone,
		WorkspaceID: wsID, Prompt: created.Prompt, Enabled: true,
		ExpectedRevision: created.Revision,
	})
	if conflict.Error == nil {
		t.Fatal("过期 revision 必须被拒")
	}
	if conflict.Error.Code != contracts.CodeConfigConflict {
		t.Fatalf("错误码 = %s，期望 %s", conflict.Error.Code, contracts.CodeConfigConflict)
	}

	paused := d.PauseSchedule(created.ID, edited.Revision)
	if paused.Error != nil {
		t.Fatalf("暂停失败: %+v", paused.Error)
	}
	if paused.Item.Enabled {
		t.Fatal("暂停后 enabled 必须为 false")
	}
	resumed := d.ResumeSchedule(created.ID, paused.Item.Revision)
	if resumed.Error != nil {
		t.Fatalf("恢复失败: %+v", resumed.Error)
	}
	if !resumed.Item.Enabled || resumed.Item.NextScheduledAt.IsZero() {
		t.Fatalf("恢复后应重新启用并算出下一跳，实际 %+v", resumed.Item)
	}

	if del := d.DeleteSchedule(created.ID, resumed.Item.Revision); del.Error != nil {
		t.Fatalf("删除失败: %+v", del.Error)
	}
	after := d.ListSchedules()
	if after.Error != nil {
		t.Fatalf("ListSchedules: %+v", after.Error)
	}
	if len(after.Items) != 0 {
		t.Fatalf("软删除后列表应为空，实际 %+v", after.Items)
	}
	// 再删一次：任务已不存在，给 NOT_FOUND 而不是静默成功。
	if again := d.DeleteSchedule(created.ID, resumed.Item.Revision); again.Error == nil {
		t.Fatal("删除已删除的任务必须报错")
	}
	_ = work

	// SCH02 的外部观察：绑定层操作之后调度库里的真实行（软删除后 enabled/revision）。
	observeScheduleDB(t, filepath.Join(d.root, "scheduler.db"), map[string]any{"bindingLifecycle": true})
}

// TestScheduleAPIRejectsUntrustedWorkspaceAndBadCron 覆盖绑定层的两道闸门：
// 未信任的工作区不能挂定时任务（无人值守没有"用户当时在旁边"可依赖），
// 坏 cron/时区必须带结构化错误码穿过绑定层。
func TestScheduleAPIRejectsUntrustedWorkspaceAndBadCron(t *testing.T) {
	d, root := newTestDesktop(t)
	dir := filepath.Join(root, "untrusted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	ws := d.CreateWorkspace(dir)
	if ws.Error != nil {
		t.Fatalf("登记工作区: %+v", ws.Error)
	}

	base := ScheduleSaveRequest{
		Name: "未信任工作区的任务", Cron: "0 9 * * *", TimeZone: "Etc/UTC",
		WorkspaceID: string(ws.Item.ID), Prompt: "写日报", Enabled: true,
	}
	res := d.SaveSchedule(base)
	if res.Error == nil {
		t.Fatal("未信任工作区必须被拒")
	}
	if res.Error.Code != contracts.CodeUnauthorized {
		t.Fatalf("错误码 = %s，期望 %s", res.Error.Code, contracts.CodeUnauthorized)
	}

	// 信任之后坏 cron/坏时区仍然要被拒（校验不能被"信任"顺手放过）。
	if tr := d.SetWorkspaceTrust(string(ws.Item.ID), true); tr.Error != nil {
		t.Fatalf("信任工作区: %+v", tr.Error)
	}
	tooManyFields := base
	tooManyFields.Cron = "0 0 9 * * *"
	if r := d.SaveSchedule(tooManyFields); r.Error == nil || r.Error.Code != contracts.CodeUnsupported {
		t.Fatalf("6 段 cron 应为 UNSUPPORTED，实际 %+v", r.Error)
	}
	localZone := base
	localZone.TimeZone = "Local"
	if r := d.SaveSchedule(localZone); r.Error == nil || r.Error.Code != contracts.CodeUnsupported {
		t.Fatalf("Local 时区应为 UNSUPPORTED，实际 %+v", r.Error)
	}
}

// TestScheduleAPIRunsInScheduledSessionAndDeleteKeepsIt 覆盖 SCH07 的绑定层部分：
// 立即运行会真的开一个调度会话（kind=scheduled，因此不在聊天列表里）；
// 删掉任务之后，这个会话仍然在库里。
func TestScheduleAPIRunsInScheduledSessionAndDeleteKeepsIt(t *testing.T) {
	d, _, wsID := scheduleFixture(t)
	// 假 worker：只验证"调度派发走的是真实 RunService"，不在这里跑真 Pi。
	d.mu.Lock()
	d.workerStarter = func(context.Context, pi.WorkerSpec) (pi.Worker, error) {
		return newC06aWorker(), nil
	}
	d.mu.Unlock()

	created := saveSchedule(t, d, ScheduleSaveRequest{
		Name: "手动触发一次", Cron: "0 3 * * *", TimeZone: "Etc/UTC",
		WorkspaceID: wsID, Prompt: "生成周报草稿", Enabled: true,
	})

	run := d.RunScheduleNow(created.ID)
	if run.Error != nil {
		t.Fatalf("立即运行失败: %s: %s", run.Error.Code, run.Error.Message)
	}
	if run.Item.SessionID == "" {
		t.Fatalf("立即运行必须绑定一个真实会话，实际 %+v", run.Item)
	}
	if run.Item.Attempts != 1 {
		t.Fatalf("一次派发的 attempts 应为 1，实际 %d", run.Item.Attempts)
	}
	// 状态来自真实 run：假 worker 不会自己结算，所以这里只断言"已经是 run 在场"的
	// 那几种状态之一（具体投影由 internal/scheduler 的测试守）。
	switch run.Item.State {
	case scheduler.StateQueued, scheduler.StateRunning, scheduler.StateWaitingApproval, scheduler.StateCompleted:
	default:
		if !scheduler.IsTerminalOccurrence(run.Item.State) {
			t.Fatalf("立即运行后的状态不合预期: %s", run.Item.State)
		}
	}

	// 调度会话在会话目录里存在，但 kind 是 scheduled，因此不在聊天列表里。
	cat, err := d.sessionCatalog()
	if err != nil {
		t.Fatalf("打开会话目录: %v", err)
	}
	sess, err := cat.GetSession(context.Background(), run.Item.SessionID)
	if err != nil {
		t.Fatalf("调度会话必须建在会话目录里: %v", err)
	}
	if sess.Kind != scheduler.SessionKind {
		t.Fatalf("会话 kind = %q，期望 %q", sess.Kind, scheduler.SessionKind)
	}
	listed := listSessionIDs(t, d.ListSessions(SessionListQuery{}))
	for _, id := range listed {
		if id == string(sess.ID) {
			t.Fatal("调度会话不得出现在聊天会话列表里")
		}
	}

	// 删除任务：软删除，occurrence 与会话都还在。
	cur := d.ListSchedules()
	if cur.Error != nil || len(cur.Items) != 1 {
		t.Fatalf("读取任务列表: %+v", cur.Error)
	}
	if del := d.DeleteSchedule(created.ID, cur.Items[0].Revision); del.Error != nil {
		t.Fatalf("删除任务失败: %+v", del.Error)
	}
	if _, err := cat.GetSession(context.Background(), run.Item.SessionID); err != nil {
		t.Fatalf("删除任务不得删除它产生的会话: %v", err)
	}
	db, err := storage.Open(context.Background(), filepath.Join(d.root, "scheduler.db"))
	if err != nil {
		t.Fatalf("打开调度库: %v", err)
	}
	defer db.Close()
	var occurrences int
	if err := db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ?`, created.ID).Scan(&occurrences); err != nil {
		t.Fatalf("统计 occurrence: %v", err)
	}
	if occurrences != 1 {
		t.Fatalf("删除任务不得删除 occurrence，实际 %d 条", occurrences)
	}

	// SCH02/SCH07 的外部观察：调度库与会话库里的真实行。
	observeScheduleDB(t, filepath.Join(d.root, "scheduler.db"), map[string]any{"deleteKeepsOccurrence": true})
	observeAppSessions(t, filepath.Join(d.root, "sessions.db"), map[string]any{"scheduledSessionKept": run.Item.SessionID})
}
