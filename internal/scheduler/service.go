package scheduler

// C09 的调度服务：什么时候开一轮会话、开不了时记什么、重启后怎么对账。
//
// 三条不可协商的约束：
//  1. **只有一个执行通道**。派发走应用进程里那唯一的 run.Service（Runner 就是
//     它，接口方法签名直接对齐 *run.Service）。调度器不建 run.Service、不建
//     semaphore、不建 worker queue、不直接调 Pi。全局活跃上限 2 与 FIFO 由
//     run.Service 自己保证，调度只是又一个提交者。
//  2. **occurrence 状态来自真实 run 事件**。queued/running/waiting_approval/
//     completed/failed/cancelled 都是 run.state 事件的投影，不是"派发成功就写完成"。
//  3. **无人值守不等于放开审批**。调度触发的写操作照样停在 waiting_approval，
//     等到审批超时就落 failed，副作用为 0 且不重放；连续两次失败经 CAS 停用任务。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/run"
)

const (
	// defaultApprovalTimeout 是无人值守审批的默认等待上限（与策略服务 60 秒 TTL 对齐）。
	//
	// 关键在于"等到上限之后必须失败"：调度任务没有人在旁边，无限等待只会一直占着
	// 全局两个槽位中的一个，把交互会话也一起饿死。
	defaultApprovalTimeout = 60 * time.Second
	// maxMisfireScan 是单次补算漏跑时刻的上限（防止停机一年时扫到天荒地老）。
	maxMisfireScan = 10000
	// maxMisfireSkipRecords 是每次 tick 最多补记多少条 skipped。
	// 更早的漏跑不再单独落行：last/next 已经越过了它们，唯一键也不会让它们复活。
	maxMisfireSkipRecords = 64
)

// Runner 是调度器**唯一**允许提交 run 的入口。
//
// 方法签名与 *run.Service 完全一致，所以生产装配就是把那个全局单例传进来——
// 不存在"调度器自己的 Runner 实现"这条旁路（除了测试用的假件）。
type Runner interface {
	Start(ctx context.Context, req run.StartRunRequest) (contracts.RunSnapshot, error)
	Abort(ctx context.Context, id contracts.RunID) error
	Get(id contracts.RunID) (contracts.RunSnapshot, error)
	Subscribe(fn func(contracts.Event)) (unsubscribe func())
}

// SessionFactory 创建普通会话（由绑定层提供：会话目录是它的所有者）。
type SessionFactory func(ctx context.Context, workspaceID contracts.WorkspaceID) (contracts.SessionID, error)

// WorkspaceRoot 把工作区 ID 解析成真实根路径（worker 的 cwd）。
type WorkspaceRoot func(contracts.WorkspaceID) (string, error)

// Options 构造调度服务。
type Options struct {
	Store  *Store
	Runner Runner
	// Clock 为空时用真实时间（生产）。
	Clock Clock
	// NewSession 必填：没有真实会话就无法派发。
	NewSession SessionFactory
	// WorkspaceRoot 可选；为空时由 RunService 用自己的默认工作目录。
	WorkspaceRoot WorkspaceRoot

	// Ceiling 是系统/工作区/Profile 合并出的权限上限；DefaultPolicy 是
	// PermissionRef 为空时使用的默认快照。两者都由绑定层给同一份事实。
	Ceiling       run.PolicyCeiling
	DefaultPolicy run.PolicySnapshot
	// Resolver 把 PermissionRef 解析成不可变快照（与 RunService 同一个解析器）。
	Resolver run.PermissionResolver

	// ConfigRevision 是这次派发依据的配置版本（写进 run 快照供审计）。
	ConfigRevision uint64
	// ApprovalTimeout 是无人值守审批的等待上限，默认 60 秒。
	ApprovalTimeout time.Duration
	// DisableAutoTick 为 true 时不启动后台循环，调用方必须自己调用 Tick。
	//
	// 测试用它保证确定性：同一时刻只可能有一个 Tick 在跑，时钟推进不会与后台
	// 循环赛跑。生产不设。
	DisableAutoTick bool
}

// Service 是调度服务。并发安全。
type Service struct {
	opts  Options
	clock Clock

	mu     sync.Mutex
	closed bool
	unsub  func()
	// occByRun 是 runID → occurrence 的内存索引：run 事件只知道 runID，
	// 必须能立刻找回"这是哪一跳"。重启时从库里重建。
	occByRun map[contracts.RunID]occKey
	// deadlines 是等待审批的截止时刻（无人值守超时判据）。
	deadlines map[occKey]time.Time
	// lastSeq 记录每个 run 已经应用过的事件序号。
	//
	// run.Service 用 `go fn(ev)` 广播事件，投递顺序**不保证**与 seq 一致：
	// 迟到的 "running" 可能排在 "waiting_approval" 之后落地，把已经等待审批的
	// occurrence 改回 running（实测 5 次里 3 次）。所以按 seq 单调过滤，
	// 只应用"比上一次更新"的事件。
	lastSeq map[contracts.RunID]uint64

	// eventMu 串行化"按 seq 应用 run 事件"。
	//
	// 只做 seq 比较不够：两个 Goroutine 可以同时读到旧的 lastSeq 再各自写库，
	// 迟到的 running 照样会盖掉 waiting_approval（实测可复现）。所以比较与写库
	// 必须在同一把锁里完成。
	eventMu sync.Mutex

	stop     chan struct{}
	stopOnce sync.Once
	// poke 让后台循环立刻重算下一次唤醒。
	//
	// 没有它，一个空闲的循环（此刻没有任何到期任务）会一直睡着：用户新建任务之后
	// 任务要等到下一个外部事件才被看见，等于"刚加的任务不按时跑"。
	// 带缓冲 1、非阻塞发送：重复 poke 无意义，也不会挡住调用方。
	poke chan struct{}
}

// occKey 是 occurrence 的稳定身份。
//
// 用 formatTS 字符串而不是 time.Time 做 map 键：time.Time 的 == 还比较单调时钟
// 读数与 location 指针，同一个瞬时会有多个"不相等"的值，用它做键迟早丢记录。
type occKey struct {
	scheduleID string
	at         string
}

func keyOf(scheduleID string, at time.Time) occKey {
	return occKey{scheduleID: scheduleID, at: formatTS(at)}
}

func (k occKey) time() time.Time { return parseTS(k.at) }

// New 构造调度服务。
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "调度器缺少存储", false)
	}
	if opts.Runner == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "调度器缺少 RunService（不可自建执行通道）", false)
	}
	if opts.NewSession == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "调度器缺少会话创建入口", false)
	}
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.ApprovalTimeout <= 0 {
		opts.ApprovalTimeout = defaultApprovalTimeout
	}
	return &Service{
		opts:      opts,
		clock:     opts.Clock,
		occByRun:  map[contracts.RunID]occKey{},
		deadlines: map[occKey]time.Time{},
		lastSeq:   map[contracts.RunID]uint64{},
		stop:      make(chan struct{}),
		poke:      make(chan struct{}, 1),
	}, nil
}

// Start 订阅 run 事件、对账未决 occurrence、重建唤醒顺序，然后（可选）启动后台循环。
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return contracts.NewAppError(contracts.CodeInternal, "调度服务已关闭", false)
	}
	if s.unsub == nil {
		s.unsub = s.opts.Runner.Subscribe(s.onRunEvent)
	}
	s.mu.Unlock()

	if err := s.reconcile(ctx); err != nil {
		return err
	}
	if err := s.plan(ctx); err != nil {
		return err
	}
	if !s.opts.DisableAutoTick {
		go s.loop(ctx)
	}
	return nil
}

// Close 退订并停掉后台循环。
func (s *Service) Close(_ context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	unsub := s.unsub
	s.unsub = nil
	s.mu.Unlock()

	if unsub != nil {
		unsub()
	}
	s.stopOnce.Do(func() { close(s.stop) })
	return nil
}

// reconcile 是重启对账：把库里未决的 occurrence 与真实 run 对齐。
//
// 口径：run 还在（能查到快照）就按快照的终态投影；查不到就记 outcome_unknown 并
// **永不重放**——"结果未知"是一个结论，重跑一遍等于用重复副作用换一个更舒服的状态字。
func (s *Service) reconcile(ctx context.Context) error {
	active, err := s.opts.Store.ActiveOccurrences(ctx)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	for _, occ := range active {
		key := keyOf(occ.ScheduleID, occ.ScheduledAt)
		s.mu.Lock()
		if occ.RunID != "" {
			s.occByRun[occ.RunID] = key
		}
		if occ.State == StateWaitingApproval {
			s.deadlines[key] = now.Add(s.opts.ApprovalTimeout)
		}
		s.mu.Unlock()

		if occ.RunID == "" {
			// 派发到一半就崩了：连 run 都没建起来，结论只能是未知。
			if _, _, ferr := s.applyOccurrenceState(ctx, key, StateOutcomeUnknown, ReasonRestartRunMissing, occurrencePatch{}, now); ferr != nil {
				return ferr
			}
			continue
		}
		snap, gerr := s.opts.Runner.Get(occ.RunID)
		if gerr != nil {
			if _, _, ferr := s.applyOccurrenceState(ctx, key, StateOutcomeUnknown, ReasonRestartRunMissing, occurrencePatch{}, now); ferr != nil {
				return ferr
			}
			continue
		}
		if state := occurrenceStateOfRun(snap.State); state != "" {
			// 同一个进程内重启服务时 run 可能还活着：终态投影，非终态按原状态继续等事件。
			if _, _, ferr := s.applyOccurrenceState(ctx, key, state, "", occurrencePatch{}, now); ferr != nil {
				return ferr
			}
		}
	}
	return nil
}

// plan 重建唤醒顺序：只给"已启用但还没有 next"的任务补算下一跳。
//
// 不重算已有 next：next 是上一次调度已经入账的决定，重启后重新按 now 算会
// 把已经错过的时刻整体抹掉（漏跑策略就永远不会触发）。
func (s *Service) plan(ctx context.Context) error {
	list, err := s.opts.Store.List(ctx, false)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	for _, sch := range list {
		if !sch.Enabled || !sch.NextScheduledAt.IsZero() {
			continue
		}
		next, nerr := s.nextWakeAfter(sch, now)
		if nerr != nil {
			continue
		}
		if err := s.opts.Store.Bookkeeping(ctx, sch.ID, bookkeeping{next: next, hasNext: true}, now); err != nil {
			return err
		}
	}
	return nil
}

// nextWakeAfter 计算 from 之后的下一个计划瞬时。
func (s *Service) nextWakeAfter(sch Schedule, from time.Time) (time.Time, error) {
	spec, err := parseCron(sch.Cron)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := loadLocation(sch.TimeZone)
	if err != nil {
		return time.Time{}, contracts.NewAppError(contracts.CodeUnsupported,
			"未知的 IANA 时区: "+sch.TimeZone, false)
	}
	return spec.nextAfter(from, loc)
}

// loop 是后台循环：等到"下一次唤醒或审批截止"再跑一次 Tick。
func (s *Service) loop(ctx context.Context) {
	for {
		next := s.nextWake(ctx)
		var timer <-chan time.Time
		if !next.IsZero() {
			d := next.Sub(s.clock.Now())
			if d <= 0 {
				d = time.Millisecond
			}
			timer = s.clock.After(d)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-s.poke:
		case <-timer:
		}
		_ = s.Tick(ctx)
	}
}

// pokeLoop 通知后台循环重算唤醒时刻（非阻塞，循环未启动时也安全）。
func (s *Service) pokeLoop() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// nextWake 返回下一次需要醒来的时刻（任务下一跳与审批截止的较早者）。
func (s *Service) nextWake(ctx context.Context) time.Time {
	var next time.Time
	list, err := s.opts.Store.List(ctx, false)
	if err == nil {
		for _, sch := range list {
			if !sch.Enabled || sch.NextScheduledAt.IsZero() {
				continue
			}
			if next.IsZero() || sch.NextScheduledAt.Before(next) {
				next = sch.NextScheduledAt
			}
		}
	}
	s.mu.Lock()
	for _, d := range s.deadlines {
		if next.IsZero() || d.Before(next) {
			next = d
		}
	}
	s.mu.Unlock()
	return next
}

// Tick 是一次同步推进：先收口审批超时，再处理到期（含漏跑补算）的任务。
//
// 它是幂等的：同一时刻跑两次不会多出 occurrence（唯一键），也不会重放已失败的一跳。
func (s *Service) Tick(ctx context.Context) error {
	now := s.clock.Now()
	if err := s.expireApprovalTimeouts(ctx, now); err != nil {
		return err
	}
	list, err := s.opts.Store.List(ctx, false)
	if err != nil {
		return err
	}
	for _, sch := range list {
		if !sch.Enabled || sch.NextScheduledAt.IsZero() || sch.NextScheduledAt.After(now) {
			continue
		}
		if err := s.processSchedule(ctx, sch, now); err != nil {
			return err
		}
	}
	return nil
}

// processSchedule 处理一条任务到期（可能同时有多个漏跑时刻）。
func (s *Service) processSchedule(ctx context.Context, sch Schedule, now time.Time) error {
	spec, err := parseCron(sch.Cron)
	if err != nil {
		// cron 坏了不该让整个调度停摆：其他任务照常跑，这条停在原地。
		return nil
	}
	loc, err := loadLocation(sch.TimeZone)
	if err != nil {
		return nil
	}

	missed := make([]time.Time, 0, 4)
	cur := sch.NextScheduledAt
	for !cur.After(now) && len(missed) < maxMisfireScan {
		missed = append(missed, cur)
		advanced, nerr := spec.nextAfter(cur, loc)
		if nerr != nil || !advanced.After(cur) {
			break
		}
		cur = advanced
	}
	if len(missed) == 0 {
		return nil
	}
	// 计划先入账（last/next 单调前沿）：即使随后崩在派发中间，也不会让同一跳
	// 被"未来"重新算出来；真的重复触发会被唯一键挡住。
	newNext := cur
	if !newNext.After(now) {
		if recalculated, nerr := spec.nextAfter(now, loc); nerr == nil {
			newNext = recalculated
		}
	}
	if err := s.opts.Store.Bookkeeping(ctx, sch.ID, bookkeeping{
		last: missed[len(missed)-1], hasLast: true,
		next: newNext, hasNext: true,
	}, now); err != nil {
		return err
	}

	catchUp := 0
	if sch.MisfirePolicy == MisfireCatchUp {
		catchUp = sch.CatchUpLimit
	}
	start := len(missed) - catchUp
	if start < 0 {
		start = 0
	}
	skipReason := ReasonMisfireBeyondCatchUp
	if sch.MisfirePolicy == MisfireSkip {
		skipReason = ReasonMisfireSkipped
	}
	recorded := 0
	for i := 0; i < start; i++ {
		if recorded >= maxMisfireSkipRecords {
			break
		}
		if _, err := s.recordSkip(ctx, sch.ID, missed[i], skipReason, now); err != nil {
			return err
		}
		recorded++
	}
	for i := start; i < len(missed); i++ {
		if _, err := s.fire(ctx, sch.ID, missed[i], now, false); err != nil {
			return err
		}
	}
	return nil
}

// recordSkip 把一次"没有派发"的计划时刻记成 skipped（终态，不会补上）。
func (s *Service) recordSkip(ctx context.Context, scheduleID string, at time.Time, reason string, now time.Time) (Occurrence, error) {
	occ, created, err := s.beginOccurrence(ctx, scheduleID, at, now)
	if err != nil {
		return Occurrence{}, err
	}
	if !created {
		return occ, nil
	}
	out, _, err := s.applyOccurrenceState(ctx, keyOf(scheduleID, at), StateSkipped, reason, occurrencePatch{}, now)
	return out, err
}

// beginOccurrence 建 occurrence 行；created=false 表示这一跳已经存在。
func (s *Service) beginOccurrence(ctx context.Context, scheduleID string, at, now time.Time) (Occurrence, bool, error) {
	created, err := s.opts.Store.BeginOccurrence(ctx, scheduleID, at, now)
	if err != nil {
		return Occurrence{}, false, err
	}
	occ, err := s.opts.Store.Occurrence(ctx, scheduleID, at)
	if err != nil {
		return Occurrence{}, false, err
	}
	return occ, created, nil
}

// fire 是一次真正的派发尝试。
//
// 顺序是刻意的：先占唯一键，再判"能不能派"（暂停/删除/不重入/权限/会话），
// 最后才提交给 RunService。这样"没派成"的那些时刻同样留下可解释的记录，
// 而不是静默消失。
func (s *Service) fire(ctx context.Context, scheduleID string, at, now time.Time, manual bool) (Occurrence, error) {
	occ, created, err := s.beginOccurrence(ctx, scheduleID, at, now)
	if err != nil {
		return Occurrence{}, err
	}
	key := keyOf(scheduleID, at)
	if !created {
		// 唯一键命中：这一跳已经发生过。绝不重放。
		return occ, nil
	}

	sch, gerr := s.opts.Store.Get(ctx, scheduleID)
	if gerr != nil {
		if isNotFound(gerr) {
			return s.finishOccurrence(ctx, key, StateSkipped, ReasonScheduleDeleted, now)
		}
		return Occurrence{}, gerr
	}
	if sch.Deleted() {
		return s.finishOccurrence(ctx, key, StateSkipped, ReasonScheduleDeleted, now)
	}
	if !sch.Enabled && !manual {
		return s.finishOccurrence(ctx, key, StateSkipped, ReasonSchedulePaused, now)
	}

	if _, busy, oerr := s.opts.Store.OtherActiveOccurrence(ctx, scheduleID, at); oerr != nil {
		return Occurrence{}, oerr
	} else if busy {
		return s.finishOccurrence(ctx, key, StateSkipped, ReasonPreviousStillRunning, now)
	}

	snapshot, perr := s.resolvePolicy(sch.PermissionRef)
	if perr != nil {
		return s.finishOccurrence(ctx, key, StateSkipped, permissionSkipReason(perr), now)
	}

	sessionID, serr := s.opts.NewSession(ctx, sch.WorkspaceID)
	if serr != nil {
		return s.finishOccurrence(ctx, key, StateFailed, ReasonSessionCreateFailed, now)
	}

	workspaceDir := ""
	if s.opts.WorkspaceRoot != nil {
		dir, derr := s.opts.WorkspaceRoot(sch.WorkspaceID)
		if derr != nil {
			return s.finishOccurrence(ctx, key, StateFailed, ReasonDispatchFailed, now)
		}
		workspaceDir = dir
	}

	snap, rerr := s.opts.Runner.Start(ctx, run.StartRunRequest{
		Message:      sch.Prompt,
		SessionID:    sessionID,
		WorkspaceID:  sch.WorkspaceID,
		WorkspaceDir: workspaceDir,
		ProfileID:    sch.ProfileID,
		// 权限引用与解析后的不可变快照都带上：RunService 会再核一次
		// （ref 与 snapshot 必须描述同一件事，且不得比上限更宽）。
		PermissionRef:  sch.PermissionRef,
		PolicySnapshot: &snapshot,
		ConfigRevision: s.opts.ConfigRevision,
	})
	if rerr != nil {
		return s.finishOccurrence(ctx, key, StateFailed, ReasonDispatchFailed, now)
	}

	// 先登记 run→occurrence 索引，再写库：事件可能在下一纳秒就到，
	// 索引晚一步就等于丢掉那一跳的状态。
	s.mu.Lock()
	s.occByRun[snap.RunID] = key
	s.mu.Unlock()

	if berr := s.opts.Store.Bookkeeping(ctx, scheduleID, bookkeeping{
		sessionID: sessionID, hasSession: true,
		runID: snap.RunID, hasRun: true,
	}, now); berr != nil {
		return Occurrence{}, berr
	}

	state := occurrenceStateOfRun(snap.State)
	if state == "" {
		state = StateQueued
	}
	// 只允许从 queued 出发写状态：快照可能已经过时（见 Store.setOccurrenceStateFrom）。
	out, _, aerr := s.applyOccurrenceStateFrom(ctx, key, StateQueued, state, "", occurrencePatch{
		sessionID: sessionID, hasSession: true,
		runID: snap.RunID, hasRun: true,
		attemptsDelta: 1,
	}, now)
	if aerr != nil {
		return Occurrence{}, aerr
	}
	return out, nil
}

// resolvePolicy 在派发前证明权限引用不比上限更宽。
//
// 复用 run.PolicyCeiling.Narrow（同一份判定，不在这里复印一套）：解析不到是
// permission_ref_invalid，比上限宽是 permission_ref_exceeds_ceiling，两者都只
// 记 skipped——越权不会被"顺手改小"，也不会带着未知权限跑起来。
func (s *Service) resolvePolicy(ref string) (run.PolicySnapshot, error) {
	trimmed := strings.TrimSpace(ref)
	want := s.opts.DefaultPolicy
	if trimmed != "" {
		switch {
		case s.opts.Resolver != nil:
			resolved, err := s.opts.Resolver.ResolvePermission(trimmed)
			if err != nil {
				return run.PolicySnapshot{}, err
			}
			want = resolved
		case strings.TrimSpace(s.opts.Ceiling.Ref) == trimmed:
			want = run.PolicySnapshot{
				Permissions: s.opts.Ceiling.Permissions,
				Revision:    s.opts.Ceiling.Revision,
			}
		default:
			return run.PolicySnapshot{}, contracts.NewAppError(contracts.CodeNotFound,
				"未知的 permissionRef: "+trimmed+"（没有可用的策略目录）", false)
		}
	}
	return s.opts.Ceiling.Narrow(want)
}

// permissionSkipReason 把权限解析失败分类成稳定的机器可读原因。
func permissionSkipReason(err error) string {
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && appErr != nil && appErr.Code == contracts.CodeUnauthorized {
		return ReasonPermissionRefExceedsCeiling
	}
	return ReasonPermissionRefInvalid
}

// finishOccurrence 把一跳收口成非派发终态（skipped/failed）。
func (s *Service) finishOccurrence(ctx context.Context, key occKey, state, reason string, now time.Time) (Occurrence, error) {
	out, _, err := s.applyOccurrenceState(ctx, key, state, reason, occurrencePatch{}, now)
	return out, err
}

// applyOccurrenceState 是唯一的状态写入口：迁移 + 轨迹 + 计数 + 审批截止。
func (s *Service) applyOccurrenceState(ctx context.Context, key occKey, state, reason string,
	patch occurrencePatch, now time.Time) (Occurrence, bool, error) {
	return s.applyOccurrenceStateFrom(ctx, key, "", state, reason, patch, now)
}

// applyOccurrenceStateFrom 在 applyOccurrenceState 之上加"期望的当前状态"。
func (s *Service) applyOccurrenceStateFrom(ctx context.Context, key occKey, expectFrom, state, reason string,
	patch occurrencePatch, now time.Time) (Occurrence, bool, error) {

	out, changed, err := s.opts.Store.setOccurrenceStateFrom(ctx, key.scheduleID, key.time(), expectFrom, state, reason, patch, now)
	if err != nil {
		return Occurrence{}, false, err
	}
	if out.State != state {
		// 库里仍是旧状态（终态先写者赢）：不要因为“这次没改成”就去动审批截止。
		return out, false, nil
	}

	s.mu.Lock()
	switch state {
	case StateWaitingApproval:
		s.deadlines[key] = now.Add(s.opts.ApprovalTimeout)
	default:
		delete(s.deadlines, key)
	}
	s.mu.Unlock()

	if changed && IsTerminalOccurrence(state) {
		s.recordOutcome(ctx, key.scheduleID, state, now)
	}
	return out, changed, nil
}

// recordOutcome 维护"连续失败"计数，并在达到阈值时经 CAS 自动暂停任务。
//
// 计数口径：completed 清零；failed +1；其余终态（cancelled/skipped/
// outcome_unknown）不改计数——用户自己取消不是任务不健康，结果未知更不该被
// 当成"再失败一次就停用"的理由。
func (s *Service) recordOutcome(ctx context.Context, scheduleID, state string, now time.Time) {
	sch, err := s.opts.Store.Get(ctx, scheduleID)
	if err != nil {
		return
	}
	failures := sch.ConsecutiveFailures
	switch state {
	case StateCompleted:
		failures = 0
	case StateFailed:
		failures++
	default:
		return
	}
	if failures == sch.ConsecutiveFailures {
		return
	}
	if err := s.opts.Store.Bookkeeping(ctx, scheduleID, bookkeeping{failures: failures, hasFailures: true}, now); err != nil {
		return
	}
	if failures >= AutoPauseAfterFailures && sch.Enabled {
		_, _, _ = s.opts.Store.PauseForFailures(ctx, scheduleID, failures, now)
	}
}

// onRunEvent 是真实 run 事件的投影入口（在 run.Service 的 goroutine 里被调用）。
func (s *Service) onRunEvent(ev contracts.Event) {
	if ev.Type != run.EventRunState {
		return
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return
	}

	s.eventMu.Lock()
	defer s.eventMu.Unlock()

	s.mu.Lock()
	key, ok := s.occByRun[ev.RunID]
	if ok && !s.closed {
		if prev, seen := s.lastSeq[ev.RunID]; seen && ev.Seq <= prev {
			s.mu.Unlock()
			return
		}
		s.lastSeq[ev.RunID] = ev.Seq
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	state := occurrenceStateOfRun(payload.State)
	if state == "" {
		return
	}
	reason := ""
	if payload.State == contracts.RunStateInterrupted {
		reason = ReasonRunInterrupted
	}
	// 事件在 run 的 goroutine 里到达，用 Background：关服务时不该把"记账"
	// 当成被取消的写（那会让 occurrence 永远停在 running）。
	_, _, _ = s.applyOccurrenceState(context.Background(), key, state, reason, occurrencePatch{}, s.clock.Now())
}

// expireApprovalTimeouts 收口无人值守审批超时。
//
// 顺序必须是"先落 failed，再终止 run"：反过来的话，Abort 引发的 cancelled 事件
// 会先写进 occurrence，把 approval_timeout 这个更有信息量的结论覆盖掉。
// 而这一跳无论谁先写都是终态，所以先写者赢正是我们想要的语义。
func (s *Service) expireApprovalTimeouts(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	expired := make([]occKey, 0, len(s.deadlines))
	for key, deadline := range s.deadlines {
		if !now.Before(deadline) {
			expired = append(expired, key)
		}
	}
	s.mu.Unlock()

	for _, key := range expired {
		occ, err := s.opts.Store.Occurrence(ctx, key.scheduleID, key.time())
		if err != nil {
			s.forgetDeadline(key)
			continue
		}
		if occ.State != StateWaitingApproval {
			s.forgetDeadline(key)
			continue
		}
		out, changed, aerr := s.applyOccurrenceState(ctx, key, StateFailed, ReasonApprovalTimeout, occurrencePatch{}, now)
		if aerr != nil {
			return aerr
		}
		s.forgetDeadline(key)
		if !changed || out.RunID == "" {
			// 这一跳在我们落结论之前已经自己收口了（例如工具调用先失败）：
			// 不再去终止 run——"先写者赢"就是这里的语义。
			continue
		}
		if occ.RunID == "" {
			continue
		}
		if aerr := s.opts.Runner.Abort(ctx, occ.RunID); aerr != nil && !isNotFound(aerr) {
			// 终止失败也要留痕：这一跳已经按超时判失败，run 是否真停下必须可查。
			_, _, _ = s.applyOccurrenceState(ctx, key, StateOutcomeUnknown, ReasonApprovalTimeout,
				occurrencePatch{}, now)
		}
	}
	return nil
}

func (s *Service) forgetDeadline(key occKey) {
	s.mu.Lock()
	delete(s.deadlines, key)
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// 任务定义 API
// ---------------------------------------------------------------------------

// Create 新建任务并算出第一跳。
func (s *Service) Create(ctx context.Context, sch Schedule) (Schedule, error) {
	if err := sch.validate(); err != nil {
		return Schedule{}, err
	}
	now := s.clock.Now()
	next, err := s.nextWakeAfter(sch, now)
	if err != nil {
		return Schedule{}, err
	}
	sch.CreatedAt = now
	sch.UpdatedAt = now
	sch.LastScheduledAt = time.Time{}
	sch.NextScheduledAt = next
	sch.CurrentSessionID = ""
	sch.CurrentRunID = ""
	sch.ConsecutiveFailures = 0
	out, err := s.opts.Store.Create(ctx, sch)
	if err != nil {
		return Schedule{}, err
	}
	s.pokeLoop()
	return out, nil
}

// Update 编辑任务（CAS）。改 cron/时区会按当前时刻重算下一跳。
func (s *Service) Update(ctx context.Context, sch Schedule, expected uint64) (Schedule, error) {
	if err := sch.validate(); err != nil {
		return Schedule{}, err
	}
	now := s.clock.Now()
	next, err := s.nextWakeAfter(sch, now)
	if err != nil {
		return Schedule{}, err
	}
	updated, err := s.opts.Store.Update(ctx, sch.ID, expected, func(cur *Schedule) error {
		cur.Name = sch.Name
		cur.Cron = sch.Cron
		cur.TimeZone = sch.TimeZone
		cur.WorkspaceID = sch.WorkspaceID
		cur.ProfileID = sch.ProfileID
		cur.Prompt = sch.Prompt
		cur.PermissionRef = sch.PermissionRef
		cur.OutputTarget = sch.OutputTarget
		cur.MisfirePolicy = sch.MisfirePolicy
		cur.CatchUpLimit = sch.CatchUpLimit
		cur.Enabled = sch.Enabled
		cur.NextScheduledAt = next
		return nil
	}, now)
	if err != nil {
		return Schedule{}, err
	}
	s.pokeLoop()
	return updated, nil
}

// Pause 暂停任务（CAS）。
func (s *Service) Pause(ctx context.Context, id string, expected uint64) (Schedule, error) {
	out, err := s.opts.Store.Update(ctx, id, expected, func(cur *Schedule) error {
		cur.Enabled = false
		return nil
	}, s.clock.Now())
	if err != nil {
		return Schedule{}, err
	}
	s.pokeLoop()
	return out, nil
}

// Resume 恢复任务（CAS）。下一跳按当前时刻重新算：恢复不该补回暂停期间的每一跳。
func (s *Service) Resume(ctx context.Context, id string, expected uint64) (Schedule, error) {
	now := s.clock.Now()
	var next time.Time
	out, err := s.opts.Store.Update(ctx, id, expected, func(cur *Schedule) error {
		cur.Enabled = true
		if computed, cerr := s.nextWakeAfter(*cur, now); cerr == nil {
			next = computed
		}
		cur.NextScheduledAt = next
		return nil
	}, now)
	if err != nil {
		return Schedule{}, err
	}
	s.pokeLoop()
	return out, nil
}

// Delete 软删除任务（已产生的 run/会话/产物不受影响）。
func (s *Service) Delete(ctx context.Context, id string, expected uint64) error {
	if err := s.opts.Store.Delete(ctx, id, expected, s.clock.Now()); err != nil {
		return err
	}
	s.pokeLoop()
	return nil
}

// RunNow 立即派发一次（手动触发）。
//
// 手动触发不受"已暂停"限制：用户点了"立即运行"就是明确要跑一次。但它同样
// 走唯一键与不重入规则，并且同样需要过权限上限检查。
func (s *Service) RunNow(ctx context.Context, id string) (Occurrence, error) {
	if _, err := s.opts.Store.Get(ctx, id); err != nil {
		return Occurrence{}, err
	}
	now := s.clock.Now()
	return s.fire(ctx, id, now, now, true)
}

// Get 读取一条任务。
func (s *Service) Get(ctx context.Context, id string) (Schedule, error) {
	return s.opts.Store.Get(ctx, id)
}

// List 列出未删除的任务。
func (s *Service) List(ctx context.Context) ([]Schedule, error) {
	return s.opts.Store.List(ctx, false)
}

// Occurrences 列出某任务的 occurrence（时间升序）。
func (s *Service) Occurrences(ctx context.Context, scheduleID string, limit int) ([]Occurrence, error) {
	return s.opts.Store.Occurrences(ctx, scheduleID, limit)
}

// Occurrence 读取一条 occurrence。
func (s *Service) Occurrence(ctx context.Context, scheduleID string, at time.Time) (Occurrence, error) {
	return s.opts.Store.Occurrence(ctx, scheduleID, at)
}

// OccurrenceEvents 读取一条 occurrence 的状态轨迹。
func (s *Service) OccurrenceEvents(ctx context.Context, scheduleID string, at time.Time) ([]OccurrenceEvent, error) {
	return s.opts.Store.OccurrenceEvents(ctx, scheduleID, at)
}
