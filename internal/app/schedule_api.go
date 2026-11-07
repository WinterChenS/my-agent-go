package app

// C09 的绑定层：定时任务的列表/新建/编辑/暂停/恢复/删除/立即运行，以及 occurrence 查询。
//
// 纪律（与 connector_api.go / session_api.go 同一口径）：
//   - 这里只做装配与校验，业务判断（CAS、唯一键、漏跑策略、审批超时、自动暂停）全在
//     internal/scheduler，不在这里重写一遍；
//   - 派发走应用进程里**唯一**的 RunService（经 desktopRunDelegate 解析当前实例），
//     这里不会构造第二个 run.Service、独立 semaphore 或 worker queue；
//   - 跨边界错误一律结构化（CallError），前端按 Code 分支显示本地化文案。
//
// 库固定在 <appRoot>/scheduler.db（迁移 004_schedules.sql）。调度库独立于会话/连接器库：
// 它只存任务定义与 occurrence，不复制 run 快照（那是诊断库的事）。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/run"
	"my-agent-go/internal/scheduler"
	"my-agent-go/internal/storage"
)

// schedulerDefaultPermissionRef 是新建任务时的默认权限引用。
//
// 与 run.Service 的 PolicyCeiling.Ref 同一个名字：它解析出来的就是系统/工作区/Profile
// 合并后的上限，因此"默认"既不是无限权限，也不是什么都不给。
const schedulerDefaultPermissionRef = "ceiling:workspace"

// schedulerApprovalTimeout 是无人值守审批的等待上限（与策略端点 60 秒 TTL 对齐）。
const schedulerApprovalTimeout = 60 * time.Second

// ScheduleListResult 是任务列表。
type ScheduleListResult struct {
	Items []scheduler.Schedule `json:"items"`
	Error *CallError           `json:"error,omitempty"`
}

// ScheduleResult 是单个任务操作的结果。
type ScheduleResult struct {
	Item  scheduler.Schedule `json:"item"`
	Error *CallError         `json:"error,omitempty"`
}

// ScheduleOccurrenceResult 是一次手动触发的结果（含 occurrence 的真实状态）。
type ScheduleOccurrenceResult struct {
	Item  scheduler.Occurrence `json:"item"`
	Error *CallError           `json:"error,omitempty"`
}

// ScheduleOccurrenceListResult 是一个任务的执行历史。
type ScheduleOccurrenceListResult struct {
	Items []scheduler.Occurrence `json:"items"`
	Error *CallError             `json:"error,omitempty"`
}

// ScheduleSaveRequest 是保存任务的请求。
//
// ID 为空表示新建；非空表示编辑，必须带 ExpectedRevision（CAS）。
type ScheduleSaveRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Cron     string `json:"cron"`
	TimeZone string `json:"timezone"`
	// WorkspaceID 为空表示用当前设置里的工作区。
	WorkspaceID   string `json:"workspaceId"`
	ProfileID     string `json:"profileId"`
	Prompt        string `json:"prompt"`
	PermissionRef string `json:"permissionRef"`
	OutputTarget  string `json:"outputTarget"`
	MisfirePolicy string `json:"misfirePolicy"`
	CatchUpLimit  int    `json:"catchUpLimit"`
	Enabled       bool   `json:"enabled"`
	// ExpectedRevision 是编辑时的 CAS 令牌（新建时忽略）。
	ExpectedRevision uint64 `json:"expectedRevision"`
}

// schedulerService 返回应用级调度服务（懒加载单例）。
func (d *Desktop) schedulerService() (*scheduler.Service, error) {
	d.schedulerMu.Lock()
	defer d.schedulerMu.Unlock()
	if d.schedulerSvc != nil {
		return d.schedulerSvc, nil
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(d.root, "scheduler.db"))
	if err != nil {
		return nil, err
	}
	// 迁移在这里做：任务页可能在用户还没跑过任何 run 时就被打开。
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store, err := scheduler.NewStore(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	permissions := effectivePermissions(d.Settings())
	svc, err := scheduler.New(scheduler.Options{
		Store: store,
		// 唯一的执行通道：这里给的是"当前那个 RunService"的委托件，
		// 运行时因换模型重建之后，委托件会自动指向新实例。
		Runner:        desktopRunDelegate{d: d},
		NewSession:    d.createScheduledSession,
		WorkspaceRoot: d.sessionWorkspaceRoot,
		Ceiling: run.PolicyCeiling{
			Permissions: permissions,
			Ref:         schedulerDefaultPermissionRef,
		},
		DefaultPolicy: run.PolicySnapshot{Permissions: permissions},
		// ConfigRevision 留 0：由 RunService 用它自己的默认配置版本补上，
		// 两处各写一份版本号只会出现"调度提交的 run 与交互 run 记了不同来源"。
		ApprovalTimeout: schedulerApprovalTimeout,
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := svc.Start(ctx); err != nil {
		_ = svc.Close(ctx)
		_ = db.Close()
		return nil, err
	}
	d.schedulerSvc = svc
	d.schedulerDB = db
	return svc, nil
}

// createScheduledSession 为一次定时触发建一个**普通会话**。
//
// 它与用户在聊天页开的会话是同一张表、同一条生命周期，只是 kind 不同：
// 目录只列普通会话（chat），所以调度会话不会污染用户的历史列表。
// 005_session_catalog.sql 把 kind 的取值留给各自任务登记，而会话目录的
// CreateSession 只建 chat——所以这里在建完之后显式改成本任务登记的 kind。
func (d *Desktop) createScheduledSession(ctx context.Context, workspaceID contracts.WorkspaceID) (contracts.SessionID, error) {
	cat, err := d.sessionCatalog()
	if err != nil {
		return "", err
	}
	sess, err := cat.CreateSession(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	if d.sessionDB == nil {
		return sess.ID, nil
	}
	if _, uerr := d.sessionDB.SQL().ExecContext(ctx,
		`UPDATE sessions SET kind = ? WHERE id = ?`, scheduler.SessionKind, string(sess.ID)); uerr != nil {
		return "", contracts.NewAppError(contracts.CodeInternal,
			"标记调度会话失败（会话已建，但目录归类未落地）: "+uerr.Error(), true)
	}
	return sess.ID, nil
}

// desktopRunDelegate 把"当前那个 RunService"暴露成 scheduler.Runner。
//
// 它自己不是服务：没有队列、没有 semaphore、没有 worker 管理，只是把调用转给
// 应用当前持有的唯一实例。运行时重建（换模型/技能/MCP）之后委托件自动指向新实例，
// 调度器不会一直往已经关闭的旧服务里提交。
type desktopRunDelegate struct {
	d *Desktop
}

// currentRunService 返回当前 RunService；没有时按需构建（与聊天路径同一个入口）。
func (d *Desktop) currentRunService() (*run.Service, error) {
	d.mu.Lock()
	svc := d.svc
	d.mu.Unlock()
	if svc != nil {
		return svc, nil
	}
	return d.ensureRuntime()
}

func (r desktopRunDelegate) Start(ctx context.Context, req run.StartRunRequest) (contracts.RunSnapshot, error) {
	svc, err := r.d.currentRunService()
	if err != nil {
		return contracts.RunSnapshot{}, err
	}
	return svc.Start(ctx, req)
}

func (r desktopRunDelegate) Abort(ctx context.Context, id contracts.RunID) error {
	svc, err := r.d.currentRunService()
	if err != nil {
		return err
	}
	return svc.Abort(ctx, id)
}

func (r desktopRunDelegate) Get(id contracts.RunID) (contracts.RunSnapshot, error) {
	svc, err := r.d.currentRunService()
	if err != nil {
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeNotFound,
			"运行时不可用，无法查询 run: "+string(id), false)
	}
	return svc.Get(id)
}

func (r desktopRunDelegate) Subscribe(fn func(contracts.Event)) func() {
	return r.d.subscribePersistentRunEvents(fn)
}

// subscribePersistentRunEvents 注册一个跨运行时重建仍然有效的事件订阅者。
//
// 为什么需要它：run.Service 会因模型/技能/MCP 变化被重建，旧订阅随旧服务一起消失。
// 调度器如果只订阅当时的实例，重建之后所有 run 事件都会丢——occurrence 会永远停在
// running，重启对账也只能把它当成"未知"。这里在每次构建新运行时把订阅者重新挂上。
func (d *Desktop) subscribePersistentRunEvents(fn func(contracts.Event)) func() {
	if fn == nil {
		return func() {}
	}
	d.mu.Lock()
	d.persistentSubs = append(d.persistentSubs, fn)
	svc := d.svc
	idx := len(d.persistentSubs) - 1
	d.mu.Unlock()

	if svc != nil {
		_ = svc.Subscribe(fn)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			d.mu.Lock()
			if idx < len(d.persistentSubs) {
				d.persistentSubs[idx] = func(contracts.Event) {}
			}
			d.mu.Unlock()
		})
	}
}

// ListSchedules 返回全部未删除的定时任务。
func (d *Desktop) ListSchedules() ScheduleListResult {
	svc, err := d.schedulerService()
	if err != nil {
		return ScheduleListResult{Items: []scheduler.Schedule{}, Error: toCallError(err)}
	}
	items, err := svc.List(context.Background())
	if err != nil {
		return ScheduleListResult{Items: []scheduler.Schedule{}, Error: toCallError(err)}
	}
	if items == nil {
		items = []scheduler.Schedule{}
	}
	return ScheduleListResult{Items: items}
}

// SaveSchedule 新建或编辑任务（编辑走 revision CAS）。
func (d *Desktop) SaveSchedule(req ScheduleSaveRequest) ScheduleResult {
	svc, err := d.schedulerService()
	if err != nil {
		return ScheduleResult{Error: toCallError(err)}
	}
	ctx := context.Background()

	workspaceID := contracts.WorkspaceID(strings.TrimSpace(req.WorkspaceID))
	if workspaceID == "" {
		current := d.CurrentWorkspace()
		if current.Error != nil {
			return ScheduleResult{Error: current.Error}
		}
		workspaceID = current.Item.ID
	}
	// 工作区必须存在且已获信任：定时任务无人值守，不能靠"用户当时在旁边"当信任依据。
	wsSvc, _, serr := d.coreServices()
	if serr != nil {
		return ScheduleResult{Error: toCallError(serr)}
	}
	if aerr := wsSvc.AssertRunnable(ctx, workspaceID); aerr != nil {
		return ScheduleResult{Error: toCallError(aerr)}
	}
	profileID := contracts.ProfileID(strings.TrimSpace(req.ProfileID))
	if profileID != "" {
		_, ps, perr := d.coreServices()
		if perr != nil {
			return ScheduleResult{Error: toCallError(perr)}
		}
		if _, gerr := ps.Get(ctx, profileID); gerr != nil {
			return ScheduleResult{Error: toCallError(gerr)}
		}
	}

	permissionRef := strings.TrimSpace(req.PermissionRef)
	if permissionRef == "" {
		permissionRef = schedulerDefaultPermissionRef
	}

	sch := scheduler.Schedule{
		ID:            strings.TrimSpace(req.ID),
		Name:          strings.TrimSpace(req.Name),
		Cron:          strings.TrimSpace(req.Cron),
		TimeZone:      strings.TrimSpace(req.TimeZone),
		WorkspaceID:   workspaceID,
		ProfileID:     profileID,
		Prompt:        req.Prompt,
		PermissionRef: permissionRef,
		OutputTarget:  strings.TrimSpace(req.OutputTarget),
		MisfirePolicy: scheduler.MisfirePolicy(strings.TrimSpace(req.MisfirePolicy)),
		CatchUpLimit:  req.CatchUpLimit,
		Enabled:       req.Enabled,
	}
	if sch.Name == "" {
		sch.Name = sch.Cron
	}

	var out scheduler.Schedule
	if sch.ID == "" {
		// ID 由绑定层生成：界面只负责内容，不负责主键（避免前端拼出重复/可猜测的 ID）。
		id, ierr := newScheduleID()
		if ierr != nil {
			return ScheduleResult{Error: toCallError(ierr)}
		}
		sch.ID = id
		out, err = svc.Create(ctx, sch)
	} else {
		out, err = svc.Update(ctx, sch, req.ExpectedRevision)
	}
	if err != nil {
		return ScheduleResult{Error: toCallError(err)}
	}
	return ScheduleResult{Item: out}
}

// PauseSchedule 暂停任务（CAS）。
func (d *Desktop) PauseSchedule(id string, expectedRevision uint64) ScheduleResult {
	svc, err := d.schedulerService()
	if err != nil {
		return ScheduleResult{Error: toCallError(err)}
	}
	out, err := svc.Pause(context.Background(), strings.TrimSpace(id), expectedRevision)
	if err != nil {
		return ScheduleResult{Error: toCallError(err)}
	}
	return ScheduleResult{Item: out}
}

// ResumeSchedule 恢复任务（CAS）；下一跳按当前时刻重算，不补暂停期间每一跳。
func (d *Desktop) ResumeSchedule(id string, expectedRevision uint64) ScheduleResult {
	svc, err := d.schedulerService()
	if err != nil {
		return ScheduleResult{Error: toCallError(err)}
	}
	out, err := svc.Resume(context.Background(), strings.TrimSpace(id), expectedRevision)
	if err != nil {
		return ScheduleResult{Error: toCallError(err)}
	}
	return ScheduleResult{Item: out}
}

// DeleteSchedule 软删除任务（已产生的 run/会话/产物保留）。
func (d *Desktop) DeleteSchedule(id string, expectedRevision uint64) SessionOpResult {
	svc, err := d.schedulerService()
	if err != nil {
		return SessionOpResult{Error: toCallError(err)}
	}
	if err := svc.Delete(context.Background(), strings.TrimSpace(id), expectedRevision); err != nil {
		return SessionOpResult{Error: toCallError(err)}
	}
	return SessionOpResult{}
}

// RunScheduleNow 立即触发一次（手动运行同样过权限上限检查与唯一键）。
func (d *Desktop) RunScheduleNow(id string) ScheduleOccurrenceResult {
	svc, err := d.schedulerService()
	if err != nil {
		return ScheduleOccurrenceResult{Error: toCallError(err)}
	}
	out, err := svc.RunNow(context.Background(), strings.TrimSpace(id))
	if err != nil {
		return ScheduleOccurrenceResult{Error: toCallError(err)}
	}
	return ScheduleOccurrenceResult{Item: out}
}

// ScheduleOccurrences 返回一个任务的执行历史（时间升序）。
func (d *Desktop) ScheduleOccurrences(id string, limit int) ScheduleOccurrenceListResult {
	svc, err := d.schedulerService()
	if err != nil {
		return ScheduleOccurrenceListResult{Items: []scheduler.Occurrence{}, Error: toCallError(err)}
	}
	items, err := svc.Occurrences(context.Background(), strings.TrimSpace(id), limit)
	if err != nil {
		return ScheduleOccurrenceListResult{Items: []scheduler.Occurrence{}, Error: toCallError(err)}
	}
	if items == nil {
		items = []scheduler.Occurrence{}
	}
	return ScheduleOccurrenceListResult{Items: items}
}

// ScheduleOccurrenceEvents 返回一次执行的完整状态轨迹（"它到底走过哪几步"）。
func (d *Desktop) ScheduleOccurrenceEvents(scheduleID string, scheduledAt string) ScheduleOccurrenceEventListResult {
	svc, err := d.schedulerService()
	if err != nil {
		return ScheduleOccurrenceEventListResult{Items: []scheduler.OccurrenceEvent{}, Error: toCallError(err)}
	}
	at, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(scheduledAt))
	if perr != nil {
		return ScheduleOccurrenceEventListResult{Items: []scheduler.OccurrenceEvent{}, Error: toCallError(
			contracts.NewAppError(contracts.CodeUnsupported, "occurrence 时刻不是合法的 RFC3339 时间: "+scheduledAt, false))}
	}
	items, err := svc.OccurrenceEvents(context.Background(), strings.TrimSpace(scheduleID), at.UTC())
	if err != nil {
		return ScheduleOccurrenceEventListResult{Items: []scheduler.OccurrenceEvent{}, Error: toCallError(err)}
	}
	if items == nil {
		items = []scheduler.OccurrenceEvent{}
	}
	return ScheduleOccurrenceEventListResult{Items: items}
}

// newScheduleID 生成一个新的任务 ID。
func newScheduleID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成任务 ID 失败: "+err.Error(), false)
	}
	return "sch-" + hex.EncodeToString(buf), nil
}

// ScheduleOccurrenceEventListResult 是一次执行的状态轨迹。
type ScheduleOccurrenceEventListResult struct {
	Items []scheduler.OccurrenceEvent `json:"items"`
	Error *CallError                  `json:"error,omitempty"`
}
