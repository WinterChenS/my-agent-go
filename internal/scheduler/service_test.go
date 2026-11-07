package scheduler

// C09：本地持久定时任务调度器的行为测试。
//
// 三条纪律贯穿整个文件：
//   - 队列语义只测一次真身：调度提交必须落进应用那个**唯一**的 RunService
//     （全局活跃上限 2、共享 FIFO），所以这里出现的是真 run.Service + 假 worker，
//     而不是"再写一个队列然后自己证明自己"；
//   - 时间由 ManualClock 推进，正确性不依赖 sleep；只有"等异步 goroutine 落地"
//     才用轮询（那是在等事件送达，不是在等时间流逝）；
//   - occurrence 的唯一键 (schedule_id, scheduled_at) 是时钟回跳/重启安全的基础，
//     每个相关用例都同时断言"库里只有一条"和"worker 只起过一次"。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
	"my-agent-go/internal/storage"
)

// ---------------------------------------------------------------------------
// 断言与查询辅助
// ---------------------------------------------------------------------------

// assertCode 断言错误是带指定 Code 的结构化 AppError。
func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，实际成功", want)
	}
	var appErr *contracts.AppError
	if !errors.As(err, &appErr) || appErr == nil {
		t.Fatalf("跨边界错误必须是结构化 AppError（前端按 Code 分支），实际 %T: %v", err, err)
	}
	if appErr.Code != want {
		t.Fatalf("错误码 = %s，期望 %s（%s）", appErr.Code, want, appErr.Message)
	}
}

// ---------------------------------------------------------------------------
// 假 Runner：给不需要真队列的用例用（同步派发，完全确定性）
// ---------------------------------------------------------------------------

type fakeRunner struct {
	mu sync.Mutex

	startErr error
	// finishImmediately 为 true 时 Start 直接返回 completed，用来让"同一任务的
	// 多条 occurrence"能在一个 Tick 里连续派发（否则第二条会被不重入规则拦下）。
	finishImmediately bool

	starts []run.StartRunRequest
	// calls 统计**尝试派发**次数（含失败）：startErr 用例要看“有没有再派一次”。
	calls  int
	snaps  map[contracts.RunID]contracts.RunSnapshot
	subs   []func(contracts.Event)
	aborts []contracts.RunID
	seq    int
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{snaps: map[contracts.RunID]contracts.RunSnapshot{}}
}

func (f *fakeRunner) Start(_ context.Context, req run.StartRunRequest) (contracts.RunSnapshot, error) {
	f.mu.Lock()
	f.calls++
	if f.startErr != nil {
		err := f.startErr
		f.mu.Unlock()
		return contracts.RunSnapshot{}, err
	}
	f.seq++
	id := contracts.RunID(fmt.Sprintf("run-fake-%d", f.seq))
	state := contracts.RunStateRunning
	if f.finishImmediately {
		state = contracts.RunStateCompleted
	}
	snap := contracts.RunSnapshot{
		RunID:       id,
		SessionID:   req.SessionID,
		WorkspaceID: req.WorkspaceID,
		State:       state,
	}
	f.starts = append(f.starts, req)
	f.snaps[id] = snap
	subs := append([]func(contracts.Event){}, f.subs...)
	f.mu.Unlock()

	// 事件在锁外广播：订阅者会回头查 Runner.Get，持锁广播必然自锁。
	f.broadcast(subs, snap)
	return snap, nil
}

func (f *fakeRunner) broadcast(subs []func(contracts.Event), snap contracts.RunSnapshot) {
	for _, fn := range subs {
		fn(runStateEvent(snap, snap.State))
	}
}

func (f *fakeRunner) Abort(_ context.Context, id contracts.RunID) error {
	f.mu.Lock()
	f.aborts = append(f.aborts, id)
	snap, ok := f.snaps[id]
	if ok {
		snap.State = contracts.RunStateCancelled
		f.snaps[id] = snap
	}
	subs := append([]func(contracts.Event){}, f.subs...)
	f.mu.Unlock()
	if !ok {
		return contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	f.broadcast(subs, snap)
	return nil
}

func (f *fakeRunner) Get(id contracts.RunID) (contracts.RunSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap, ok := f.snaps[id]
	if !ok {
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	return snap, nil
}

func (f *fakeRunner) Subscribe(fn func(contracts.Event)) func() {
	f.mu.Lock()
	f.subs = append(f.subs, fn)
	idx := len(f.subs) - 1
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.subs[idx] = func(contracts.Event) {}
			f.mu.Unlock()
		})
	}
}

// finish 模拟"这一轮真的跑完了"：更新快照并同步广播事件。
func (f *fakeRunner) finish(t *testing.T, id contracts.RunID, state string) {
	t.Helper()
	f.mu.Lock()
	snap, ok := f.snaps[id]
	if !ok {
		f.mu.Unlock()
		t.Fatalf("未知 run: %s", id)
	}
	snap.State = state
	f.snaps[id] = snap
	subs := append([]func(contracts.Event){}, f.subs...)
	f.mu.Unlock()
	f.broadcast(subs, snap)
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRunner) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *fakeRunner) lastStart() run.StartRunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) == 0 {
		return run.StartRunRequest{}
	}
	return f.starts[len(f.starts)-1]
}

func (f *fakeRunner) lastRunID() contracts.RunID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return contracts.RunID(fmt.Sprintf("run-fake-%d", f.seq))
}

// runStateEvent 构造一条与 run.Service 同形状的 run.state 事件。
func runStateEvent(snap contracts.RunSnapshot, state string) contracts.Event {
	payload, _ := json.Marshal(map[string]any{"state": state})
	return contracts.Event{
		SchemaVersion: contracts.EventSchemaVersion,
		EventID:       string(snap.RunID) + "-state",
		WorkspaceID:   snap.WorkspaceID,
		SessionID:     snap.SessionID,
		RunID:         snap.RunID,
		Seq:           1,
		Type:          run.EventRunState,
		OccurredAt:    time.Now(),
		Payload:       payload,
	}
}

// ---------------------------------------------------------------------------
// 测试夹具
// ---------------------------------------------------------------------------

type harness struct {
	t     *testing.T
	dir   string
	work  string
	db    *storage.DB
	store *Store
	clock *ManualClock
	svc   *Service

	fake *fakeRunner
	// runSvc 只在"真队列"用例里非空。
	runSvc *run.Service

	opts Options
}

const harnessApprovalTimeout = 2 * time.Minute

// newHarness 在临时目录里建一个落盘的调度器（不使用内存库）。
func newHarness(t *testing.T, now time.Time, mutate ...func(*Options)) *harness {
	t.Helper()
	return newHarnessWithRunner(t, now, nil, mutate...)
}

// newHarnessWithRunner 允许传入真正的 run.Service 作为唯一提交入口。
func newHarnessWithRunner(t *testing.T, now time.Time, runner Runner, mutate ...func(*Options)) *harness {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("建工作目录: %v", err)
	}
	db, err := storage.Open(context.Background(), filepath.Join(dir, "scheduler.db"))
	if err != nil {
		t.Fatalf("打开调度库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("构造存储失败: %v", err)
	}

	h := &harness{t: t, dir: dir, work: work, db: db, store: store, runSvc: nil}

	var sessSeq atomic.Int64
	perms := config.Permissions{Filesystem: "workspace"}
	opts := Options{
		Store:           store,
		Clock:           NewManualClock(now),
		DisableAutoTick: true,
		NewSession: func(context.Context, contracts.WorkspaceID) (contracts.SessionID, error) {
			return contracts.SessionID(fmt.Sprintf("ses-sched-%d", sessSeq.Add(1))), nil
		},
		WorkspaceRoot:   func(contracts.WorkspaceID) (string, error) { return work, nil },
		Ceiling:         run.PolicyCeiling{Permissions: perms, Ref: "ceiling:workspace"},
		DefaultPolicy:   run.PolicySnapshot{Permissions: perms},
		ApprovalTimeout: harnessApprovalTimeout,
		ConfigRevision:  7,
	}
	if runner == nil {
		h.fake = newFakeRunner()
		opts.Runner = h.fake
	} else {
		opts.Runner = runner
		if svc, ok := runner.(*run.Service); ok {
			h.runSvc = svc
		}
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	svc, err := New(opts)
	if err != nil {
		t.Fatalf("构造调度服务失败: %v", err)
	}
	h.svc = svc
	h.clock = opts.Clock.(*ManualClock)
	h.opts = opts
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("启动调度服务失败: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return h
}

// newHarness 的 options 复用（restart 用）：把本次的 Options 换成新时钟。
func (h *harness) restart(now time.Time) *harness {
	h.t.Helper()
	if err := h.svc.Close(context.Background()); err != nil {
		h.t.Fatalf("关闭旧服务失败: %v", err)
	}
	opts := h.opts
	opts.Clock = NewManualClock(now)
	svc, err := New(opts)
	if err != nil {
		h.t.Fatalf("重启后构造调度服务失败: %v", err)
	}
	h.svc = svc
	h.opts = opts
	h.clock = opts.Clock.(*ManualClock)
	if err := svc.Start(context.Background()); err != nil {
		h.t.Fatalf("重启后启动失败: %v", err)
	}
	h.t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return h
}

func (h *harness) advance(d time.Duration) { h.clock.Advance(d) }

func (h *harness) tick() {
	h.t.Helper()
	if err := h.svc.Tick(context.Background()); err != nil {
		h.t.Fatalf("Tick 失败: %v", err)
	}
}

func (h *harness) create(sch Schedule) Schedule {
	h.t.Helper()
	out, err := h.svc.Create(context.Background(), sch)
	if err != nil {
		h.t.Fatalf("创建任务失败: %v", err)
	}
	return out
}

func (h *harness) occurrences(scheduleID string) []Occurrence {
	h.t.Helper()
	out, err := h.svc.Occurrences(context.Background(), scheduleID, 500)
	if err != nil {
		h.t.Fatalf("读取 occurrence 失败: %v", err)
	}
	return out
}

func (h *harness) schedule(id string) Schedule {
	h.t.Helper()
	out, err := h.svc.Get(context.Background(), id)
	if err != nil {
		h.t.Fatalf("读取任务失败: %v", err)
	}
	return out
}

// assertUniqueKeyRejectsDuplicate 直接打库，证明唯一键真的在数据库层面存在
// （而不是"应用层刚好没重复写"）。
func (h *harness) assertUniqueKeyRejectsDuplicate(t *testing.T, scheduleID string, at time.Time) {
	t.Helper()
	created, err := h.store.BeginOccurrence(context.Background(), scheduleID, at, h.clock.Now())
	if err != nil {
		t.Fatalf("重复记录 occurrence 失败: %v", err)
	}
	if created {
		t.Fatal("唯一键失效：同一 (schedule_id, scheduled_at) 被记录了两次")
	}
	_, rawErr := h.db.SQL().ExecContext(context.Background(),
		`INSERT INTO schedule_occurrences(schedule_id, scheduled_at, state, reason, session_id, run_id, attempts, created_at, updated_at)
		 VALUES(?, ?, 'queued', '', '', '', 0, ?, ?)`,
		scheduleID, formatTS(at), formatTS(at), formatTS(at))
	if rawErr == nil {
		t.Fatal("数据库层面必须拒绝重复的 (schedule_id, scheduled_at)")
	}
	if !strings.Contains(strings.ToUpper(rawErr.Error()), "UNIQUE") {
		t.Fatalf("期望 UNIQUE 约束错误，实际 %v", rawErr)
	}
}

// countDispatched 统计真正派发过的 occurrence（有 run 的那些）。
func countDispatched(occs []Occurrence) int {
	n := 0
	for _, o := range occs {
		if o.Attempts > 0 || o.RunID != "" {
			n++
		}
	}
	return n
}

func countState(occs []Occurrence, state string) int {
	n := 0
	for _, o := range occs {
		if o.State == state {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// SCH02：CAS
// ---------------------------------------------------------------------------

// TestScheduleCASRejectsConcurrentEdit 覆盖 SCH02 的编辑并发面：
// 两个写者拿着同一个 revision 提交，只有一个赢；输的那个拿到 CONFIG_CONFLICT
// （前端据此提示"已被别处修改"并重读）。
//
// 同时钉住另一件事：调度器自己的记账（last/next/current）**不**推进 revision，
// 否则用户每开一次编辑框都可能因为后台 tick 而冲突。
func TestScheduleCASRejectsConcurrentEdit(t *testing.T) {
	h := newHarness(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	created := h.create(validSchedule())
	if created.Revision != 1 {
		t.Fatalf("新建 revision 应为 1，实际 %d", created.Revision)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	names := []string{"编辑者 A", "编辑者 B"}
	for i := range names {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sch := created
			sch.Name = names[i]
			_, errs[i] = h.svc.Update(context.Background(), sch, created.Revision)
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		assertCode(t, err, contracts.CodeConfigConflict)
	}
	if wins != 1 {
		t.Fatalf("同一 revision 上必须恰好一个写者成功，实际成功 %d 个", wins)
	}

	final := h.schedule(created.ID)
	if final.Revision != 2 {
		t.Fatalf("成功编辑后 revision 应为 2，实际 %d", final.Revision)
	}
	if !strings.HasPrefix(final.Name, "编辑者") {
		t.Fatalf("落库的是赢家的值，实际 %q", final.Name)
	}

	// 后台记账不推进 revision。
	h.advance(9*time.Hour + 30*time.Minute)
	h.tick()
	after := h.schedule(created.ID)
	if after.Revision != final.Revision {
		t.Fatalf("调度记账不得推进 revision：%d → %d", final.Revision, after.Revision)
	}

	// SCH02 的外部观察：CAS 冲突后库里的 revision 与 schedules 行。
	observeScheduler(t, h.db.Path(), map[string]any{"casEdits": 2})
}

// ---------------------------------------------------------------------------
// SCH03：唯一的 RunService / 全局上限 2 / 共享 FIFO
// ---------------------------------------------------------------------------

// workerTracker 记录真实 RunService 启动过的 worker（"进程计数"，不是服务字段）。
type workerTracker struct {
	mu       sync.Mutex
	specs    []pi.WorkerSpec
	workers  []*testWorker
	started  int32
	onPrompt func(w *testWorker)
}

func (tr *workerTracker) starter() run.WorkerStarter {
	return func(_ context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		w := newTestWorker(spec)
		tr.mu.Lock()
		tr.specs = append(tr.specs, spec)
		tr.workers = append(tr.workers, w)
		tr.started++
		hook := tr.onPrompt
		tr.mu.Unlock()
		if hook != nil {
			w.onPrompt = hook
		}
		return w, nil
	}
}

func (tr *workerTracker) count() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return int(tr.started)
}

func (tr *workerTracker) worker(i int) *testWorker {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if i >= len(tr.workers) {
		return nil
	}
	return tr.workers[i]
}

// newRealRunService 构造应用进程里那**唯一**的 run.Service（worker 用假件）。
func newRealRunService(t *testing.T, tracker *workerTracker) (*run.Service, string) {
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
	svc := run.New(run.Options{
		WorkspaceID:  "ws-sched",
		SessionID:    "sess-interactive-default",
		WorkspaceDir: work,
		AgentDir:     filepath.Join(dir, "agent"),
		Installation: pi.PiInstallation{ID: "test:pi", Executable: filepath.Join(dir, "pi.exe")},
		Isolation:    pi.IsolationSpec{PolicyExtension: ext},
		Policy: policy.New(policy.Options{
			Permissions: perms,
			PathGuard:   guard,
			ApprovalTTL: time.Minute,
		}),
		PolicyCeiling:  run.PolicyCeiling{Permissions: perms, Ref: "ceiling:workspace"},
		PolicySnapshot: run.PolicySnapshot{Permissions: perms},
		StartWorker:    tracker.starter(),
		ConfigRevision: 7,
		Now:            time.Now,
	})
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, work
}

// TestSchedulerUsesGlobalRunLimitTwo 覆盖 SCH03：
// 两个交互会话 + 一条调度 occurrence → 全局仍然最多两个 worker，
// 第三条留在**同一个** FIFO 里排队（读 worker 计数与事件里的排队位次，
// 不读服务内部字段）。
func TestSchedulerUsesGlobalRunLimitTwo(t *testing.T) {
	tracker := &workerTracker{}
	runSvc, work := newRealRunService(t, tracker)
	h := newHarnessWithRunner(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), runSvc,
		func(o *Options) { o.WorkspaceRoot = func(contracts.WorkspaceID) (string, error) { return work, nil } })

	ctx := context.Background()
	first, err := runSvc.Start(ctx, run.StartRunRequest{
		Message: "交互会话一", SessionID: "sess-interactive-a", WorkspaceID: "ws-sched", WorkspaceDir: work,
	})
	if err != nil {
		t.Fatalf("启动交互会话一失败: %v", err)
	}
	second, err := runSvc.Start(ctx, run.StartRunRequest{
		Message: "交互会话二", SessionID: "sess-interactive-b", WorkspaceID: "ws-sched", WorkspaceDir: work,
	})
	if err != nil {
		t.Fatalf("启动交互会话二失败: %v", err)
	}
	if first.State != contracts.RunStateRunning || second.State != contracts.RunStateRunning {
		t.Fatalf("两个交互会话都应 running，实际 %s / %s", first.State, second.State)
	}

	sch := validSchedule()
	sch.ID = "sch-global-limit"
	sch.Cron = "0 9 * * *"
	sch.PermissionRef = "ceiling:workspace"
	created := h.create(sch)
	h.clock.Set(created.NextScheduledAt)
	h.tick()

	occs := h.occurrences(created.ID)
	if len(occs) != 1 {
		t.Fatalf("应记录 1 条 occurrence，实际 %d", len(occs))
	}
	if occs[0].State != StateQueued {
		t.Fatalf("全局已满时调度 occurrence 必须是 queued（共享 FIFO），实际 %s", occs[0].State)
	}
	if occs[0].RunID == "" || occs[0].SessionID == "" {
		t.Fatalf("occurrence 必须绑定真实 run/session，实际 %+v", occs[0])
	}
	// 进程计数：只有那两个交互 worker 起来了。
	if got := tracker.count(); got != 2 {
		t.Fatalf("全局上限 2：排队中的调度 run 不得启动 worker，实际 %d 个", got)
	}
	// 排队位次来自真实 run 事件（前端唯一能看到队列事实的地方）。
	pos := queuedPositionFromEvents(t, runSvc, occs[0].RunID)
	if pos != 1 {
		t.Fatalf("调度 run 在共享队列里应为第 1 位，实际 %d", pos)
	}

	// 一个交互 run 结算 → 排队的调度 run 拿到槽位。
	if w := tracker.worker(0); w != nil {
		w.settle()
	}
	if !awaitCondition(t, 10*time.Second, func() bool {
		o, gerr := h.svc.Occurrence(ctx, created.ID, occs[0].ScheduledAt)
		return gerr == nil && o.State == StateRunning
	}) {
		cur, _ := h.svc.Occurrence(ctx, created.ID, occs[0].ScheduledAt)
		t.Fatalf("空出槽位后调度 run 应启动（occurrence 应为 running），实际 %s", cur.State)
	}
	if got := tracker.count(); got != 3 {
		t.Fatalf("第三个 run 启动后 worker 计数应为 3，实际 %d", got)
	}
	if got := runSvc.ActiveCount(); got != 2 {
		t.Fatalf("全局活跃数必须仍然是 2，实际 %d", got)
	}

	// SCH03 的外部观察：调度 run 落进同一个 run 服务（库里 runs 行 + occurrence 行）。
	observeScheduler(t, h.db.Path(), map[string]any{"globalRunLimit": 2})
}

// queuedPositionFromEvents 从 run 事件里读排队位次。
func queuedPositionFromEvents(t *testing.T, svc *run.Service, id contracts.RunID) int {
	t.Helper()
	pos := 0
	evs, err := svc.Replay(id, 0)
	if err != nil {
		t.Fatalf("重放 run 事件失败: %v", err)
	}
	for _, ev := range evs {
		if ev.Type != run.EventRunState {
			continue
		}
		var p struct {
			State         string `json:"state"`
			QueuePosition int    `json:"queuePosition"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("解析 run.state 失败: %v", err)
		}
		if p.State == contracts.RunStateQueued && p.QueuePosition > 0 {
			pos = p.QueuePosition
		}
	}
	return pos
}

// ---------------------------------------------------------------------------
// SCH04：不重入 / 漏跑策略 / 连续失败自动暂停
// ---------------------------------------------------------------------------

// TestSameScheduleDoesNotOverlap 覆盖"同一任务不重入"：
// 上一条还没到终态时，下一条记 skipped + previous_still_running，不排第二次队。
func TestSameScheduleDoesNotOverlap(t *testing.T) {
	h := newHarness(t, time.Date(2025, 6, 1, 8, 59, 0, 0, time.UTC))
	sch := validSchedule()
	sch.ID = "sch-overlap"
	sch.Cron = "0 * * * *"
	sch.TimeZone = "Etc/UTC"
	created := h.create(sch)

	h.advance(2 * time.Minute) // 09:01
	h.tick()
	if got := h.fake.startCount(); got != 1 {
		t.Fatalf("第一条应派发，实际派发 %d 次", got)
	}

	h.advance(time.Hour) // 10:01，上一条还在跑
	h.tick()

	occs := h.occurrences(created.ID)
	if len(occs) != 2 {
		t.Fatalf("应记录 2 条 occurrence（第二条是 skipped），实际 %d 条", len(occs))
	}
	if occs[1].State != StateSkipped {
		t.Fatalf("不重入的第二条应为 skipped，实际 %s", occs[1].State)
	}
	if occs[1].Reason != ReasonPreviousStillRunning {
		t.Fatalf("跳过原因必须是 %q，实际 %q", ReasonPreviousStillRunning, occs[1].Reason)
	}
	if got := h.fake.startCount(); got != 1 {
		t.Fatalf("不重入：不得为第二条再起一个 run，实际派发 %d 次", got)
	}

	// SCH04 的外部观察：同一任务只允许一条 running occurrence。
	observeScheduler(t, h.db.Path(), map[string]any{"overlapForbidden": true})
}

// TestRestartAppliesConfiguredMisfirePolicy 覆盖 SCH04 的漏跑策略：
// 停机 10 天回来后，补跑由 CatchUpLimit 界定（默认只补 1 次），
// 不是把 10 条全跑一遍，也不是一条都不跑。
func TestRestartAppliesConfiguredMisfirePolicy(t *testing.T) {
	h := newHarness(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	// 让每一条 run 立刻完成：否则补跑第 2/3 条会被"不重入"拦下，
	// 测到的就是另一条规则（不重入由 TestSameScheduleDoesNotOverlap 负责）。
	h.fake.finishImmediately = true

	base := validSchedule()
	base.Cron = "0 9 * * *"
	base.TimeZone = "Etc/UTC"

	catchUpDefault := base
	catchUpDefault.ID = "sch-misfire-default"

	skipAll := base
	skipAll.ID = "sch-misfire-skip"
	skipAll.MisfirePolicy = MisfireSkip

	catchUpThree := base
	catchUpThree.ID = "sch-misfire-three"
	catchUpThree.CatchUpLimit = 3

	for _, sch := range []Schedule{catchUpDefault, skipAll, catchUpThree} {
		created := h.create(sch)
		if want := time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC); !created.NextScheduledAt.Equal(want) {
			t.Fatalf("%s 的首次唤醒 = %s，期望 %s", created.ID,
				created.NextScheduledAt.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}

	// 应用停机 10 天：同一个库、新的服务实例、时钟跳到 06-11T12:00Z。
	h.restart(time.Date(2025, 6, 11, 12, 0, 0, 0, time.UTC))
	h.tick()

	defaultOccs := h.occurrences(catchUpDefault.ID)
	if got := countDispatched(defaultOccs); got != DefaultCatchUpLimit {
		t.Fatalf("默认策略应只补跑 %d 次，实际 %d 次", DefaultCatchUpLimit, got)
	}
	// 补跑的是**最近**那一次（09:00Z 06-11），更早的记为 skipped。
	last := defaultOccs[len(defaultOccs)-1]
	if want := time.Date(2025, 6, 11, 9, 0, 0, 0, time.UTC); !last.ScheduledAt.Equal(want) {
		t.Fatalf("补跑应落在最近一次漏跑 %s，实际 %s",
			want.Format(time.RFC3339), last.ScheduledAt.UTC().Format(time.RFC3339))
	}
	if got := countState(defaultOccs, StateSkipped); got != 10 {
		t.Fatalf("其余 10 条漏跑应记 skipped，实际 %d 条", got)
	}
	for _, o := range defaultOccs {
		if o.State == StateSkipped && o.Reason != ReasonMisfireBeyondCatchUp {
			t.Fatalf("漏跑跳过原因必须是 %q，实际 %q", ReasonMisfireBeyondCatchUp, o.Reason)
		}
	}

	skipOccs := h.occurrences(skipAll.ID)
	if got := countDispatched(skipOccs); got != 0 {
		t.Fatalf("MisfireSkip 策略不得补跑，实际 %d 次", got)
	}
	if got := countState(skipOccs, StateSkipped); got != 11 {
		t.Fatalf("MisfireSkip 应把 11 条漏跑都记 skipped，实际 %d 条", got)
	}
	for _, o := range skipOccs {
		if o.State == StateSkipped && o.Reason != ReasonMisfireSkipped {
			t.Fatalf("策略跳过的原因必须是 %q，实际 %q", ReasonMisfireSkipped, o.Reason)
		}
	}

	threeOccs := h.occurrences(catchUpThree.ID)
	if got := countDispatched(threeOccs); got != 3 {
		t.Fatalf("CatchUpLimit=3 应补跑 3 次，实际 %d 次", got)
	}
	if got := countState(threeOccs, StateSkipped); got != 8 {
		t.Fatalf("CatchUpLimit=3 时其余 8 条应为 skipped，实际 %d 条", got)
	}

	// 三条任务最终都指向下一次未来唤醒。
	for _, id := range []string{catchUpDefault.ID, skipAll.ID, catchUpThree.ID} {
		got := h.schedule(id)
		if want := time.Date(2025, 6, 12, 9, 0, 0, 0, time.UTC); !got.NextScheduledAt.Equal(want) {
			t.Fatalf("%s 的下一跳 = %s，期望 %s", id,
				got.NextScheduledAt.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}

	// SCH02/SCH04 的外部观察：漏跑补跑条数与唯一键都写在库里。
	observeScheduler(t, h.db.Path(), map[string]any{"misfirePolicyApplied": true})
}

// TestBackwardClockJumpDoesNotDuplicateOccurrence 覆盖 SCH02 的时钟回跳：
// 唯一键赢。这里刻意让 run 立刻完成（不重入规则不会挡），所以"没有第二条"
// 只能由 (schedule_id, scheduled_at) 来解释。
func TestBackwardClockJumpDoesNotDuplicateOccurrence(t *testing.T) {
	h := newHarness(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	h.fake.finishImmediately = true

	sch := validSchedule()
	sch.ID = "sch-backward"
	sch.Cron = "0 9 * * *"
	sch.TimeZone = "Etc/UTC"
	created := h.create(sch)

	h.clock.Set(time.Date(2025, 6, 1, 10, 0, 0, 0, time.UTC))
	h.tick()
	if got := h.fake.startCount(); got != 1 {
		t.Fatalf("第一次应派发 1 次，实际 %d 次", got)
	}
	fired := h.schedule(created.ID)
	if !fired.LastScheduledAt.Equal(time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("LastScheduledAt = %s", fired.LastScheduledAt.UTC().Format(time.RFC3339))
	}

	// 系统时钟回跳两小时：09:00Z 又"未来"了一次。
	h.clock.Set(time.Date(2025, 6, 1, 8, 0, 0, 0, time.UTC))
	h.tick()
	h.tick()
	if got := h.fake.startCount(); got != 1 {
		t.Fatalf("时钟回跳不得重复触发：实际派发 %d 次", got)
	}
	if got := len(h.occurrences(created.ID)); got != 1 {
		t.Fatalf("时钟回跳不得新增 occurrence：实际 %d 条", got)
	}
	if got := h.schedule(created.ID); !got.NextScheduledAt.Equal(time.Date(2025, 6, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("回跳后下一跳不得倒退，实际 %s", got.NextScheduledAt.UTC().Format(time.RFC3339))
	}
	h.assertUniqueKeyRejectsDuplicate(t, created.ID, time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC))

	// SCH01 的外部观察：时钟回跳后同一 occurrence 键只有一行。
	observeScheduler(t, h.db.Path(), map[string]any{"backwardClockJump": true})
}

// TestTwoConsecutiveFailuresAutoPause 覆盖 SCH04 的后半段：
// 连续两次失败经 CAS 把任务停用（revision+1），第三次 tick 不再派发。
func TestTwoConsecutiveFailuresAutoPause(t *testing.T) {
	h := newHarness(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	h.fake.startErr = contracts.NewAppError(contracts.CodeWorkerStartFail, "worker 起不来", true)

	sch := validSchedule()
	sch.ID = "sch-autopause"
	sch.Cron = "* * * * *"
	sch.TimeZone = "Etc/UTC"
	created := h.create(sch)

	h.advance(1 * time.Minute)
	h.tick()
	afterFirst := h.schedule(created.ID)
	if afterFirst.ConsecutiveFailures != 1 || !afterFirst.Enabled {
		t.Fatalf("第一次失败后应为 failures=1 且仍启用，实际 %+v", afterFirst)
	}
	if afterFirst.Revision != created.Revision {
		t.Fatalf("普通记账不得推进 revision：%d → %d", created.Revision, afterFirst.Revision)
	}

	h.advance(1 * time.Minute)
	h.tick()
	paused := h.schedule(created.ID)
	if paused.Enabled {
		t.Fatal("连续两次失败必须自动停用")
	}
	if paused.ConsecutiveFailures != AutoPauseAfterFailures {
		t.Fatalf("ConsecutiveFailures = %d，期望 %d", paused.ConsecutiveFailures, AutoPauseAfterFailures)
	}
	if paused.Revision != afterFirst.Revision+1 {
		t.Fatalf("自动停用必须走 CAS 并推进 revision：%d → %d", afterFirst.Revision, paused.Revision)
	}

	occs := h.occurrences(created.ID)
	if len(occs) != 2 || occs[0].State != StateFailed || occs[1].State != StateFailed {
		t.Fatalf("两条 occurrence 都应是 failed，实际 %+v", occs)
	}

	h.advance(1 * time.Minute)
	h.tick()
	if got := h.fake.callCount(); got != 2 {
		t.Fatalf("停用后第三次 tick 不得派发，实际累计派发 %d 次", got)
	}
	if got := len(h.occurrences(created.ID)); got != 2 {
		t.Fatalf("停用后不得新增 occurrence，实际 %d 条", got)
	}

	// SCH04 的外部观察：连续失败后 schedules.enabled=0 在库上。
	observeScheduler(t, h.db.Path(), map[string]any{"autoPaused": true})
}

// TestOccurrenceSkippedWhenPermissionRefInvalidOrTooBroad 覆盖 SCH07 的权限引用面：
// 派发前解析不到、或比系统/工作区/Profile 上限更宽，都只记 skipped + 原因，
// 不建会话、不起 run。
func TestOccurrenceSkippedWhenPermissionRefInvalidOrTooBroad(t *testing.T) {
	resolver := stubResolver{
		"permission:wide": run.PolicySnapshot{
			Permissions: config.Permissions{Filesystem: "unrestricted", Shell: true, Network: true},
		},
	}
	h := newHarness(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), func(o *Options) {
		o.Resolver = resolver
	})

	invalid := validSchedule()
	invalid.ID = "sch-perm-invalid"
	invalid.Cron = "0 9 * * *"
	invalid.TimeZone = "Etc/UTC"
	invalid.PermissionRef = "permission:missing"

	wide := validSchedule()
	wide.ID = "sch-perm-wide"
	wide.Cron = "0 9 * * *"
	wide.TimeZone = "Etc/UTC"
	wide.PermissionRef = "permission:wide"

	invalidCreated := h.create(invalid)
	wideCreated := h.create(wide)

	h.clock.Set(time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC))
	h.tick()

	if got := h.fake.startCount(); got != 0 {
		t.Fatalf("权限引用不合法时不得派发 run，实际派发 %d 次", got)
	}
	cases := []struct {
		scheduleID string
		reason     string
	}{
		{invalidCreated.ID, ReasonPermissionRefInvalid},
		{wideCreated.ID, ReasonPermissionRefExceedsCeiling},
	}
	for _, tc := range cases {
		occs := h.occurrences(tc.scheduleID)
		if len(occs) != 1 || occs[0].State != StateSkipped {
			t.Fatalf("%s 应记 1 条 skipped，实际 %+v", tc.scheduleID, occs)
		}
		if occs[0].Reason != tc.reason {
			t.Fatalf("%s 的跳过原因 = %q，期望 %q", tc.scheduleID, occs[0].Reason, tc.reason)
		}
		if occs[0].RunID != "" {
			t.Fatalf("%s 不应绑定 run，实际 %s", tc.scheduleID, occs[0].RunID)
		}
	}

	// SCH07 的外部观察：越界权限引用只留下 skipped occurrence，没有 run 行。
	observeScheduler(t, h.db.Path(), map[string]any{"permissionRefRejected": true})
}

type stubResolver map[string]run.PolicySnapshot

func (r stubResolver) ResolvePermission(ref string) (run.PolicySnapshot, error) {
	if snap, ok := r[ref]; ok {
		return snap, nil
	}
	return run.PolicySnapshot{}, contracts.NewAppError(contracts.CodeNotFound, "未知的 permissionRef: "+ref, false)
}

// ---------------------------------------------------------------------------
// SCH05：无人值守审批
// ---------------------------------------------------------------------------

// testWorker 是"按脚本吐事件"的假 Pi。它通过 WorkerSpec.Env 拿到策略端点，
// 像真 Pi 扩展那样请求一次执行前授权——这就是 waiting_approval 的真实来源。
type testWorker struct {
	events chan pi.RawEvent
	env    []string
	scope  string

	onPrompt func(*testWorker)
	started  sync.Once

	mu           sync.Mutex
	closed       bool
	decision     policy.Decision
	authorizeErr error

	authorizeCalls atomic.Int32
	sideEffects    atomic.Int32
}

func newTestWorker(spec pi.WorkerSpec) *testWorker {
	return &testWorker{
		events: make(chan pi.RawEvent, 32),
		env:    spec.Env,
		scope:  filepath.Join(spec.WorkingDir, "notes.md"),
	}
}

func (w *testWorker) Call(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	w.started.Do(func() {
		if w.onPrompt != nil {
			go w.onPrompt(w)
		}
	})
	return json.RawMessage(`{"type":"response","command":"prompt","success":true}`), nil
}

func (w *testWorker) Events() <-chan pi.RawEvent { return w.events }

func (w *testWorker) Close(_ context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.events)
	}
	return nil
}

func (w *testWorker) PID() int                   { return 0 }
func (w *testWorker) StderrTail() (string, bool) { return "", false }
func (w *testWorker) ExitCode() (int, bool)      { return 0, false }
func (w *testWorker) Limitations() []string      { return nil }

func (w *testWorker) send(ev pi.RawEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.events <- ev
}

func (w *testWorker) settle() {
	w.send(pi.RawEvent{
		Type:   pi.TypeAgentSettled,
		Record: json.RawMessage(`{"type":"agent_settled","aborted":false}`),
	})
}

// authorizeWrite 像真 Pi 扩展一样问一次"能不能写"。只有拿到 allow 才会产生副作用。
func (w *testWorker) authorizeWrite() {
	url, _ := envValue(w.env, "MYAGENT_POLICY_URL")
	token, _ := envValue(w.env, "MYAGENT_POLICY_TOKEN")
	sess, _ := envValue(w.env, "MYAGENT_POLICY_SESSION")

	w.authorizeCalls.Add(1)
	client := policybridge.NewClient(policybridge.Endpoint{URL: url}, token, contracts.SessionID(sess), 0)
	decision, _, err := client.Authorize(context.Background(), policy.ToolCall{
		ID:            "call-write",
		Name:          "write",
		Source:        policy.SourceBuiltin,
		Origin:        "user",
		Arguments:     json.RawMessage(`{"path":"notes.md"}`),
		ResourceScope: w.scope,
	})

	w.mu.Lock()
	w.decision = decision
	w.authorizeErr = err
	w.mu.Unlock()

	if err == nil && decision == policy.DecisionAllow {
		w.sideEffects.Add(1)
	}
	w.settle()
}

func (w *testWorker) result() (policy.Decision, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.decision, w.authorizeErr
}

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"="), true
		}
	}
	return "", false
}

// unattendedApprovalScenario 把"调度任务触发一次需要审批的写操作"推到
// waiting_approval，并且**不**回答审批。
func unattendedApprovalScenario(t *testing.T) (*harness, *workerTracker, Schedule, Occurrence) {
	t.Helper()
	tracker := &workerTracker{}
	tracker.onPrompt = func(w *testWorker) { w.authorizeWrite() }
	runSvc, work := newRealRunService(t, tracker)
	h := newHarnessWithRunner(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), runSvc,
		func(o *Options) { o.WorkspaceRoot = func(contracts.WorkspaceID) (string, error) { return work, nil } })

	sch := validSchedule()
	sch.ID = "sch-unattended"
	sch.Cron = "0 9 * * *"
	sch.TimeZone = "Etc/UTC"
	sch.PermissionRef = "ceiling:workspace"
	created := h.create(sch)

	h.clock.Set(time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC))
	h.tick()

	if !awaitCondition(t, 10*time.Second, func() bool {
		occs := h.occurrences(created.ID)
		return len(occs) == 1 && occs[0].State == StateWaitingApproval
	}) {
		occs := h.occurrences(created.ID)
		t.Fatalf("需要审批的调度 run 应停在 waiting_approval，实际 %+v", occs)
	}
	occs := h.occurrences(created.ID)
	return h, tracker, created, occs[0]
}

// TestUnattendedWriteWaitsForApproval 覆盖 SCH05 的前半段：
// 没有人在场也不能绕开审批——等待期间副作用计数为 0；超时后 occurrence 记 failed。
func TestUnattendedWriteWaitsForApproval(t *testing.T) {
	h, tracker, created, occ := unattendedApprovalScenario(t)
	ctx := context.Background()

	worker := tracker.worker(0)
	if worker == nil {
		t.Fatal("调度 run 必须真的启动 worker")
	}
	if got := worker.sideEffects.Load(); got != 0 {
		t.Fatalf("等待审批期间副作用计数必须是 0，实际 %d", got)
	}
	if worker.authorizeCalls.Load() != 1 {
		t.Fatalf("应恰好发起 1 次授权请求，实际 %d", worker.authorizeCalls.Load())
	}

	// 无人值守超时：时钟推过 ApprovalTimeout。
	h.advance(harnessApprovalTimeout)
	h.tick()

	if !awaitCondition(t, 10*time.Second, func() bool {
		cur, err := h.svc.Occurrence(ctx, created.ID, occ.ScheduledAt)
		return err == nil && cur.State == StateFailed
	}) {
		cur, _ := h.svc.Occurrence(ctx, created.ID, occ.ScheduledAt)
		t.Fatalf("审批超时后 occurrence 必须落 failed，实际 %s", cur.State)
	}
	cur, _ := h.svc.Occurrence(ctx, created.ID, occ.ScheduledAt)
	if cur.Reason != ReasonApprovalTimeout {
		t.Fatalf("失败原因必须是 %q，实际 %q", ReasonApprovalTimeout, cur.Reason)
	}
	if got := worker.sideEffects.Load(); got != 0 {
		t.Fatalf("审批超时不得产生任何副作用，实际计数 %d", got)
	}
	if decision, _ := worker.result(); decision == policy.DecisionAllow {
		t.Fatal("超时后等待中的工具调用不得被放行")
	}
	if got := h.schedule(created.ID); got.ConsecutiveFailures != 1 {
		t.Fatalf("一次失败后 failures 应为 1，实际 %d", got.ConsecutiveFailures)
	}

	// SCH05 的外部观察：无人值守仍是 waiting_approval，且没有任何写入落地。
	observeScheduler(t, h.db.Path(), map[string]any{"unattendedWaitingApproval": true})
}

// TestApprovalExpiryFailsOccurrenceWithoutReplay 覆盖 SCH05 的后半段：
// 过期失败只发生过一次派发，没有第二次尝试（不重放）。
func TestApprovalExpiryFailsOccurrenceWithoutReplay(t *testing.T) {
	h, tracker, created, occ := unattendedApprovalScenario(t)
	ctx := context.Background()

	h.advance(harnessApprovalTimeout)
	h.tick()
	if !awaitCondition(t, 10*time.Second, func() bool {
		cur, err := h.svc.Occurrence(ctx, created.ID, occ.ScheduledAt)
		return err == nil && cur.State == StateFailed
	}) {
		t.Fatalf("审批超时后 occurrence 必须落 failed")
	}

	// 再推几次 tick：既不能重放这条，也不能凭"失败"再派发一次。
	for i := 0; i < 3; i++ {
		h.addMisfireFreeAdvance()
		h.tick()
	}

	worker := tracker.worker(0)
	if got := tracker.count(); got != 1 {
		t.Fatalf("同一条 occurrence 只能派发一次，实际起过 %d 个 worker", got)
	}
	if got := worker.authorizeCalls.Load(); got != 1 {
		t.Fatalf("授权请求不得重放，实际 %d 次", got)
	}
	if got := worker.sideEffects.Load(); got != 0 {
		t.Fatalf("副作用必须为 0，实际 %d", got)
	}
	cur, err := h.svc.Occurrence(ctx, created.ID, occ.ScheduledAt)
	if err != nil {
		t.Fatalf("读取 occurrence 失败: %v", err)
	}
	if cur.State != StateFailed || cur.Attempts != 1 {
		t.Fatalf("过期失败必须是终态且 attempts=1，实际 %+v", cur)
	}
	if got := countDispatched(h.occurrences(created.ID)); got != 1 {
		t.Fatalf("不得再次派发，实际派发 %d 条", got)
	}

	// SCH05 的外部观察：审批过期 → failed，且不重放（库里没有第二条 attempt）。
	observeScheduler(t, h.db.Path(), map[string]any{"approvalExpired": true})
}

// addMisfireFreeAdvance 推进 2 秒：越过"立即重试"可能用到的同一秒，
// 但不跨过下一次 09:00（不引入新的漏跑，避免混淆本用例的结论）。
func (h *harness) addMisfireFreeAdvance() { h.clock.Advance(2 * time.Second) }

// TestOccurrenceStatesComeFromRealEvents 覆盖 occurrence 状态来源：
// 状态必须是真 run 事件投影出来的（queued → running → waiting_approval →
// running → completed），而不是"派发成功就写 completed"。
func TestOccurrenceStatesComeFromRealEvents(t *testing.T) {
	h, tracker, created, occ := unattendedApprovalScenario(t)
	ctx := context.Background()

	// 人真的回答了审批（once）→ 工具拿到 allow → 副作用 1 次 → 本轮结算。
	approvalID := awaitApprovalRequest(t, h.runSvc, occ.RunID)
	if err := h.runSvc.ResolveApproval(ctx, occ.RunID, approvalID, "once"); err != nil {
		t.Fatalf("答复审批失败: %v", err)
	}

	if !awaitCondition(t, 10*time.Second, func() bool {
		cur, err := h.svc.Occurrence(ctx, created.ID, occ.ScheduledAt)
		return err == nil && cur.State == StateCompleted
	}) {
		cur, _ := h.svc.Occurrence(ctx, created.ID, occ.ScheduledAt)
		t.Fatalf("结算后 occurrence 应为 completed，实际 %s", cur.State)
	}

	worker := tracker.worker(0)
	if got := worker.sideEffects.Load(); got != 1 {
		t.Fatalf("批准后副作用必须恰好 1 次，实际 %d", got)
	}

	events, err := h.svc.OccurrenceEvents(ctx, created.ID, occ.ScheduledAt)
	if err != nil {
		t.Fatalf("读取 occurrence 事件失败: %v", err)
	}
	got := make([]string, 0, len(events))
	for _, ev := range events {
		got = append(got, ev.State)
	}
	want := []string{StateQueued, StateRunning, StateWaitingApproval, StateRunning, StateCompleted}
	if strings.Join(got, "→") != strings.Join(want, "→") {
		t.Fatalf("occurrence 状态序列 = %s，期望 %s", strings.Join(got, "→"), strings.Join(want, "→"))
	}

	// 终态之后不再有新的 occurrence 状态写入。
	h.advance(2 * time.Second)
	h.tick()
	again, _ := h.svc.OccurrenceEvents(ctx, created.ID, occ.ScheduledAt)
	if len(again) != len(want) {
		t.Fatalf("终态后不得再写 occurrence 状态：%d → %d 条", len(want), len(again))
	}
	if got := h.schedule(created.ID); got.ConsecutiveFailures != 0 {
		t.Fatalf("成功的一轮必须把失败计数清零，实际 %d", got.ConsecutiveFailures)
	}

	// SCH05 的外部观察：occurrence 状态来自真实 run 事件，append-only 记录在库里。
	observeScheduler(t, h.db.Path(), map[string]any{"statesFromRealEvents": true})
}

// awaitApprovalRequest 从 run 事件里找第一条审批请求 ID。
func awaitApprovalRequest(t *testing.T, svc *run.Service, runID contracts.RunID) string {
	t.Helper()
	var id string
	if !awaitCondition(t, 10*time.Second, func() bool {
		evs, err := svc.Replay(runID, 0)
		if err != nil {
			return false
		}
		for _, ev := range evs {
			if ev.Type != run.EventApprovalRequest {
				continue
			}
			var p struct {
				ApprovalID string `json:"approvalId"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				continue
			}
			if p.ApprovalID != "" {
				id = p.ApprovalID
				return true
			}
		}
		return false
	}) {
		t.Fatalf("run %s 没有产生审批请求", runID)
	}
	return id
}

// awaitCondition 轮询等待**异步事件送达**（不是在等时间流逝）。
func awaitCondition(t *testing.T, limit time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// TestBackgroundLoopFiresAfterCreate 证明后台循环真的在工作：
// 循环在"此刻没有任何到期任务"时会睡着，新建任务必须把它唤醒（否则刚加的任务
// 要等到下一个外部事件才会被看见）。这里不用 Tick：正确性来自循环本身的唤醒。
func TestBackgroundLoopFiresAfterCreate(t *testing.T) {
	h := newHarness(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), func(o *Options) {
		// 打开后台循环（其余用例用显式 Tick 保持确定性）。
		o.DisableAutoTick = false
	})

	sch := validSchedule()
	sch.ID = "sch-loop"
	sch.Cron = "* * * * *"
	sch.TimeZone = "Etc/UTC"
	ctx := context.Background()
	created, err := h.svc.Create(ctx, sch)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 推进到第一跳之后：循环要么已经挂上定时器（会触发），要么按"已经过期"
	// 立刻重算——两种路径都应当派发这一跳。
	h.clock.Set(created.NextScheduledAt)
	// 等的是"派发真的发生"（occurrence 行先进库、派发随后发生，这是刻意的顺序：
	// 先占唯一键才能保证重放被挡住）。
	if !awaitCondition(t, 10*time.Second, func() bool {
		return h.fake.callCount() == 1
	}) {
		t.Fatalf("后台循环没有按唤醒时刻触发定时任务：派发 %d 次", h.fake.callCount())
	}
	if got := len(h.occurrences(created.ID)); got != 1 {
		t.Fatalf("应只落 1 条 occurrence，实际 %d 条", got)
	}

	// SCH03 的外部观察：新建任务后后台循环触发的真实 occurrence 行。
	observeScheduler(t, h.db.Path(), map[string]any{"backgroundLoopFired": true})
}

// TestRestartKeepsUnknownOutcomeAndNeverReplays 覆盖 SCH05 的重启口径：
// 结果未知的 occurrence 保持 outcome_unknown，且不会因为"没结论"被重放。
func TestRestartKeepsUnknownOutcomeAndNeverReplays(t *testing.T) {
	h := newHarness(t, time.Date(2025, 6, 1, 8, 59, 0, 0, time.UTC))
	sch := validSchedule()
	sch.ID = "sch-unknown"
	sch.Cron = "0 9 * * *"
	sch.TimeZone = "Etc/UTC"
	created := h.create(sch)

	h.clock.Set(time.Date(2025, 6, 1, 9, 0, 0, 0, time.UTC))
	h.tick()
	occs := h.occurrences(created.ID)
	if len(occs) != 1 || occs[0].RunID == "" {
		t.Fatalf("应先派发一条，实际 %+v", occs)
	}

	// 模拟"重启后这个 run 已经不在了"：新服务看不到任何 run 快照。
	gone := newFakeRunner() // 空快照表：Get 一律 NOT_FOUND
	h.opts.Runner = gone
	h.restart(time.Date(2025, 6, 1, 9, 30, 0, 0, time.UTC))

	cur, err := h.svc.Occurrence(context.Background(), created.ID, occs[0].ScheduledAt)
	if err != nil {
		t.Fatalf("读取 occurrence 失败: %v", err)
	}
	if cur.State != StateOutcomeUnknown {
		t.Fatalf("结果未知的 occurrence 必须保持 outcome_unknown，实际 %s", cur.State)
	}
	if cur.Reason != ReasonRestartRunMissing {
		t.Fatalf("未知原因应为 %q，实际 %q", ReasonRestartRunMissing, cur.Reason)
	}

	// 再推进若干 tick：不得重放这一条。
	for i := 0; i < 3; i++ {
		h.advance(time.Minute)
		h.tick()
	}
	if got := len(h.occurrences(created.ID)); got != 1 {
		t.Fatalf("未知结果不得重放，实际 occurrence 数 %d", got)
	}
	if got := gone.startCount(); got != 0 {
		t.Fatalf("未知结果不得重新派发 run，实际派发 %d 次", got)
	}

	// SCH05 的外部观察：未知结果在库里保持 outcome_unknown，重启后不重放。
	observeScheduler(t, h.db.Path(), map[string]any{"unknownOutcomeKept": true})
}
