package browser

// B05：BrowserService —— 在契约 backend 之上加"控制权 + 每 tab 串行队列 + 授权"。
//
// 分层刻意如此：
//   - backend（B01–B03 / 测试替身）只管把输入交给引擎；
//   - 本文件决定"该不该交、什么时候交、交之前谁在控制"。
//
// 不变量（每条都有对应用例，见 control_test.go）：
//  1. 每个 tab 同时只有一个动作在引擎里跑（串行、FIFO、有界队列）。
//  2. 写动作在**入队前**和**派发前**各过一次授权；任何一次不成立都没有输入发出。
//  3. 接管先递增代数并清空队列，再决定 Pausing/Human：绝不出现"显示 Human
//     之后还有 agent 输入落到页面"。
//  4. 派发前的中止（接管/导航/取消/授权失效）返回 CodeActionRevoked，
//     已派发但超时的返回 OUTCOME_UNKNOWN 且不重放，并把状态压到 ControlUncertain。
//  5. 4/session、12/total 在创建 native 实例**之前**判定；超限不记账、不创建。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
)

// 服务默认值。全部可由 ServiceOptions 覆盖（测试需要在毫秒级跑完）。
const (
	// defaultTakeoverSettleHint 只是说明用的文案阈值，不参与判定。
	defaultActionTimeout = ActionTimeout
	defaultMaxQueued     = MaxQueuedActionsPerTab
)

// ServiceOptions 构造 BrowserService。
type ServiceOptions struct {
	// Backend 是真实引擎（或测试替身），不可为空。
	Backend BrowserBackend
	// Authorizer 是写动作的授权点。为 nil 时**所有写动作都被拒绝**：
	// 浏览器动作默认需要审批，不能因为"没配授权器"就默认放行。
	Authorizer Authorizer
	// MaxQueuedPerTab 是每 tab 待派发队列上限，<=0 用 MaxQueuedActionsPerTab。
	MaxQueuedPerTab int
	// ActionTimeout 是单次派发的超时，<=0 用 ActionTimeout（15s）。
	ActionTimeout time.Duration
	// Now 便于测试注入时钟。
	Now func() time.Time
}

// snapshotRecord 是一份快照在服务侧的有效期判据。
//
// backend 自己也有 RefRegistry，但"代数"只有服务知道（接管/恢复会递增），
// 所以服务必须自己记一份：动作只认**当前代数**下产生的快照。
type snapshotRecord struct {
	id           string
	generation   uint64
	navigationID string
	observedAt   time.Time
}

// queuedAction 是一个已入队、等待派发的写动作。
type queuedAction struct {
	seq          uint64
	tab          TabID
	exec         contracts.ExecutionContext
	action       Action
	snapshot     snapshotRecord
	generation   uint64
	epoch        uint64
	token        AuthorizationToken
	request      AuthorizationRequest
	enqueuedAt   time.Time
	canceled     bool
	dispatchedAt time.Time
	done         chan struct{}
	res          ActionResult
	err          error
}

// tabState 是服务对一个 tab 的全部认知。
type tabState struct {
	id    TabID
	owner contracts.SessionID
	url   string

	navigationID string
	generation   uint64 // 控制权代数：接管/恢复各递增一次
	epoch        uint64 // 队列纪元：接管/恢复/导航时递增，让旧队列项失效
	mode         ControlMode
	detail       string

	snapshots     map[string]snapshotRecord
	freshSnapshot string // 当前代数下观察到的最新快照（Resume 的前置条件）

	queue    []*queuedAction
	queued   int
	inflight int
	closed   bool

	wake chan struct{}
}

// Service 是浏览器工具的执行面。并发安全。
type Service struct {
	backend       BrowserBackend
	authz         Authorizer
	maxQueued     int
	actionTimeout time.Duration
	now           func() time.Time
	limits        *SessionTabs

	mu       sync.Mutex
	tabs     map[TabID]*tabState
	seq      uint64
	timeline []TimelineEntry
	closed   bool
	closeCh  chan struct{}
}

// NewService 构造服务。
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Backend == nil {
		return nil, appErr(contracts.CodeInternal, "BrowserService 缺少 backend：不能在没有引擎的情况下假装能观察/动作")
	}
	s := &Service{
		backend:       opts.Backend,
		authz:         opts.Authorizer,
		maxQueued:     opts.MaxQueuedPerTab,
		actionTimeout: opts.ActionTimeout,
		now:           opts.Now,
		limits:        NewSessionTabs(),
		tabs:          map[TabID]*tabState{},
		closeCh:       make(chan struct{}),
	}
	if s.maxQueued <= 0 {
		s.maxQueued = defaultMaxQueued
	}
	if s.actionTimeout <= 0 {
		s.actionTimeout = defaultActionTimeout
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Capabilities 透传 backend 的实测能力（MCP 层据此决定注册哪些工具）。
func (s *Service) Capabilities() CapabilitySet { return s.backend.Capabilities() }

// Close 停掉全部队列并把未派发的动作撤销。
//
// 撤销而不是静默丢弃：调用方（模型/UI）必须拿到"没有发出"的结论，
// 否则它会以为动作还在排队、稍后自己会生效。
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.closeCh)
	for _, st := range s.tabs {
		st.closed = true
		s.drainLocked(st, revokedError("服务关闭，队列已清空"))
	}
	s.appendTimelineLocked(TimelineEntry{Kind: TimelineServiceClosed, Detail: "服务关闭"})
	s.mu.Unlock()
}

// OpenTab 在创建 native 实例**之前**判定 tab 限额。
//
// 顺序不可换：限额判定必须在 CreateTab 之前，否则第 5/第 13 个 tab 会先建出
// 一个真实实例再被拒绝（BR12 的判定看的就是 backend 的创建计数）。
func (s *Service) OpenTab(ctx context.Context, exec contracts.ExecutionContext, opts TabOptions) (TabID, error) {
	if strings.TrimSpace(string(exec.SessionID)) == "" {
		return "", appErr(contracts.CodeUnauthorized, "创建 tab 需要会话身份：无会话不创建任何实例")
	}
	if err := opts.Validate(); err != nil {
		return "", err
	}
	owner := opts.OwnerSessionID
	if strings.TrimSpace(string(owner)) == "" {
		owner = exec.SessionID
	}
	if owner != exec.SessionID {
		// 归属只能是自己：允许代别人建 tab 等于绕过跨 session 隔离。
		return "", appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"不能为其它会话创建 tab（请求会话 %s，归属 %s）", exec.SessionID, owner))
	}
	opts.OwnerSessionID = owner

	if err := s.limits.Reserve(exec.SessionID); err != nil {
		// 超限：不记账、不创建实例，计数原样。
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelineTabLimitExceeded, SessionID: exec.SessionID, Detail: err.Error(),
			Queued: s.limits.CountFor(exec.SessionID), Inflight: s.limits.Total(),
		})
		return "", err
	}
	tab, err := s.backend.CreateTab(ctx, opts)
	if err != nil {
		s.limits.Release(exec.SessionID)
		return "", err
	}

	s.mu.Lock()
	st := &tabState{
		id:         tab,
		owner:      exec.SessionID,
		url:        opts.URL,
		generation: 1,
		epoch:      1,
		mode:       ControlAgent,
		snapshots:  map[string]snapshotRecord{},
		wake:       make(chan struct{}, 1),
	}
	s.tabs[tab] = st
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineTabCreated, TabID: tab, SessionID: exec.SessionID, Generation: st.generation,
		Mode: st.mode, Detail: opts.URL,
	})
	s.mu.Unlock()

	go s.runQueue(st)
	return tab, nil
}

// CloseTab 关闭 tab：队列先撤销，再释放 backend 实例与限额。
func (s *Service) CloseTab(ctx context.Context, exec contracts.ExecutionContext, tab TabID) error {
	s.mu.Lock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	st.closed = true
	st.epoch++
	s.drainLocked(st, revokedError("tab 已关闭，队列已清空"))
	counts := s.limits.CountFor(st.owner)
	s.mu.Unlock()

	err = s.backend.CloseTab(ctx, tab)

	s.mu.Lock()
	delete(s.tabs, tab)
	s.limits.Release(exec.SessionID)
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineTabClosed, TabID: tab, SessionID: exec.SessionID, Queued: counts,
		ErrorCode: errorCode(err),
	})
	s.mu.Unlock()
	return err
}

// Navigate 导航到 url，并让旧代数下的队列项/快照立即失效。
//
// 不做这一步的话，"导航后队列里还有旧 ref 的写动作"就会在导航完成后才被
// backend 拒绝——那已经不是"不派发"而是"派发了才发现不对"。这里在派发前就挡掉。
func (s *Service) Navigate(ctx context.Context, exec contracts.ExecutionContext, tab TabID, url string) error {
	s.mu.Lock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	st.epoch++
	st.navigationID = ""
	st.url = url
	st.freshSnapshot = ""
	s.drainLocked(st, revokedError("页面已导航，旧快照/旧队列动作失效"))
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineNavigated, TabID: tab, SessionID: exec.SessionID, Generation: st.generation,
		Mode: st.mode, Detail: url,
	})
	s.mu.Unlock()
	return s.backend.Navigate(ctx, tab, url)
}

// Observe 观察页面。读操作在任何控制状态下都允许（读不会改变页面）。
//
// 观察会更新两份事实：
//   - 当前文档身份（navigationID），会话内/工具输出与面板看同一个值；
//   - "当前代数下已有新快照"，这是 Resume 的前置条件；恢复后第一次 observe
//     把 Resuming 推向 Agent。
func (s *Service) Observe(ctx context.Context, exec contracts.ExecutionContext, tab TabID, opts ObserveOptions) (Snapshot, error) {
	s.mu.Lock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		s.mu.Unlock()
		return Snapshot{}, err
	}
	s.mu.Unlock()

	snap, err := s.backend.Observe(ctx, tab, opts)
	if err != nil {
		return Snapshot{}, err
	}
	if snap.TabID == "" {
		snap.TabID = tab
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	st, err = s.ownedLocked(exec, tab)
	if err != nil {
		return Snapshot{}, err
	}
	snap.ControlGeneration = st.generation
	if st.navigationID != snap.NavigationID {
		st.navigationID = snap.NavigationID
	}
	st.snapshots[snap.ID] = snapshotRecord{
		id:           snap.ID,
		generation:   st.generation,
		navigationID: snap.NavigationID,
		observedAt:   s.now(),
	}
	st.freshSnapshot = snap.ID
	if st.mode == ControlResuming {
		st.mode = ControlAgent
		st.detail = ""
	}
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineObserved, TabID: tab, SessionID: exec.SessionID,
		SnapshotID: snap.ID, NavigationID: snap.NavigationID, Generation: st.generation,
		Mode: st.mode, Queued: st.queued, Inflight: st.inflight,
		Detail: fmt.Sprintf("elements=%d", len(snap.Elements)),
	})
	return snap, nil
}

// Control 返回当前控制状态（面板与工具输出共用同一份事实）。
func (s *Service) Control(exec contracts.ExecutionContext, tab TabID) (ControlState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		return ControlState{}, err
	}
	return st.controlLocked(), nil
}

// TabInfo 返回面板/工具输出必须一致的那组事实。
func (s *Service) TabInfo(exec contracts.ExecutionContext, tab TabID) (TabInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		return TabInfo{}, err
	}
	return TabInfo{
		TabID:             st.id,
		URL:               st.url,
		NavigationID:      st.navigationID,
		ControlGeneration: st.generation,
		OwnerSessionID:    st.owner,
		Mode:              st.mode,
		Queued:            st.queued,
		InFlight:          st.inflight,
	}, nil
}

// TakeOver 把控制权交给人工。
//
// 顺序是全部安全性的来源，不能换：
//  1. 先递增代数（旧 ref / 旧审批 / 旧队列项立即失效）；
//  2. 再清空队列（每一项拿到"未派发"的结构化结论）；
//  3. 最后才决定状态：还有已派发动作在飞 → Pausing（绝不显示 Human），否则 Human。
func (s *Service) TakeOver(ctx context.Context, exec contracts.ExecutionContext, tab TabID) (ControlState, error) {
	s.mu.Lock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		s.mu.Unlock()
		return ControlState{}, err
	}
	if st.mode == ControlHuman && st.inflight == 0 && st.queued == 0 {
		// 幂等：已经完成接管，不重复递增代数（否则每次点"接管"都会让
		// 正在等待的审批全部失效）。
		cs := st.controlLocked()
		s.mu.Unlock()
		return cs, nil
	}

	previous := st.mode
	st.generation++
	st.epoch++
	st.freshSnapshot = ""
	revoked := s.drainLocked(st, revokedError("人工接管：已入队但未派发的动作被撤销"))
	if st.inflight > 0 {
		st.mode = ControlPausing
		st.detail = fmt.Sprintf("%d 个已派发的动作尚未静止：已提交给引擎的输入无法撤回",
			st.inflight)
	} else {
		st.mode = ControlHuman
		st.detail = ""
	}
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineTakeOver, TabID: tab, SessionID: exec.SessionID,
		Generation: st.generation, Mode: st.mode, Queued: st.queued, Inflight: st.inflight,
		Detail: fmt.Sprintf("previous=%s revoked=%d", previous, revoked),
	})
	cs := st.controlLocked()
	s.mu.Unlock()
	return cs, nil
}

// Resume 把控制权交还 agent。
//
// 两道前置条件都是基线口径：
//   - 必须存在**接管之后**（当前代数）产生的快照，否则拒绝（STALE_SNAPSHOT）——
//     不允许 agent 用接管前的 ref 继续写；
//   - 必须确认静止（没有已派发动作在飞），否则 CONTROL_UNCERTAIN。
//
// 成功后递增代数并进入 Resuming：还要用**新快照**再观察一次才回到 Agent。
func (s *Service) Resume(ctx context.Context, exec contracts.ExecutionContext, tab TabID) (ControlState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		return ControlState{}, err
	}
	switch st.mode {
	case ControlHuman, ControlPausing:
	case ControlResuming:
		return ControlState{}, appErr(contracts.CodeControlUncertain,
			"该 tab 已在恢复过程中：需要先 observe 取新快照")
	default:
		return ControlState{}, appErr(contracts.CodeUnsupported, fmt.Sprintf(
			"当前控制状态 %q 不能恢复给 agent（只有人工接管之后才能恢复）", st.mode))
	}
	if st.inflight > 0 {
		return ControlState{}, controlUncertainError(fmt.Sprintf(
			"还有 %d 个已派发动作未静止：不能确认静止前不恢复 agent 控制", st.inflight))
	}
	if st.queued > 0 {
		return ControlState{}, controlUncertainError(fmt.Sprintf(
			"还有 %d 个已入队动作未清理：接管应已清空队列，这里拒绝恢复", st.queued))
	}
	if st.freshSnapshot == "" {
		// 稳定拒绝：调用方拿到 STALE_SNAPSHOT 就知道"先 observe"。
		return ControlState{}, appErr(contracts.CodeStaleSnapshot,
			"恢复 agent 控制前必须重新 observe：接管之前的 ref/代数一律不再有效")
	}
	if rec, ok := st.snapshots[st.freshSnapshot]; !ok || rec.generation != st.generation {
		// freshSnapshot 记录了别名但代数对不上：同样按"没有新快照"处理。
		st.freshSnapshot = ""
		return ControlState{}, appErr(contracts.CodeStaleSnapshot,
			"恢复 agent 控制前必须重新 observe：没有属于当前控制代数的快照")
	}

	st.generation++
	st.epoch++
	st.freshSnapshot = ""
	st.mode = ControlResuming
	st.detail = ""
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineResume, TabID: tab, SessionID: exec.SessionID,
		Generation: st.generation, Mode: st.mode, Queued: st.queued, Inflight: st.inflight,
	})
	return st.controlLocked(), nil
}

// Execute 执行一次写动作：授权 → 入队 → 串行派发。
//
// 返回时机：动作真正结束（成功/失败/被撤销）之后。调用方拿到的结论永远是
// "这次输入到底发出了没有"的答案，而不是"排上了队"。
func (s *Service) Execute(ctx context.Context, exec contracts.ExecutionContext, tab TabID, action Action) (ActionResult, error) {
	if err := action.Validate(); err != nil {
		return ActionResult{}, err
	}

	s.mu.Lock()
	st, err := s.ownedLocked(exec, tab)
	if err != nil {
		s.mu.Unlock()
		return ActionResult{}, err
	}
	if err := modeRejectsWrite(st.mode, st.detail); err != nil {
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelineWriteRejected, TabID: tab, SessionID: exec.SessionID,
			Action: action.Kind, Ref: action.Ref, SnapshotID: action.SnapshotID,
			Generation: st.generation, Mode: st.mode, ErrorCode: errorCode(err),
			Queued: st.queued, Inflight: st.inflight, Detail: err.Error(),
		})
		s.mu.Unlock()
		return ActionResult{}, err
	}
	rec, err := s.snapshotForActionLocked(st, action.SnapshotID)
	if err != nil {
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelineWriteRejected, TabID: tab, SessionID: exec.SessionID,
			Action: action.Kind, Ref: action.Ref, SnapshotID: action.SnapshotID,
			Generation: st.generation, Mode: st.mode, ErrorCode: errorCode(err),
			Queued: st.queued, Inflight: st.inflight, Detail: err.Error(),
		})
		s.mu.Unlock()
		return ActionResult{}, err
	}
	if st.queued >= s.maxQueued {
		s.mu.Unlock()
		return ActionResult{}, appErr(CodeQueueFull, fmt.Sprintf(
			"该 tab 已有 %d 个待派发动作（上限 %d）：本次没有入队、没有派发",
			st.queued, s.maxQueued))
	}
	if s.authz == nil {
		s.mu.Unlock()
		return ActionResult{}, appErr(contracts.CodeUnauthorized,
			"没有配置浏览器动作授权器：写动作默认拒绝（浏览器动作需要审批）")
	}
	req := AuthorizationRequest{
		TabID:        tab,
		SessionID:    exec.SessionID,
		Action:       action,
		Origin:       OriginOf(st.url),
		URL:          st.url,
		NavigationID: st.navigationID,
		Generation:   st.generation,
	}
	generation := st.generation
	epoch := st.epoch
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineAuthorizeRequested, TabID: tab, SessionID: exec.SessionID,
		Action: action.Kind, Ref: action.Ref, SnapshotID: action.SnapshotID,
		Generation: generation, Mode: st.mode, Queued: st.queued, Inflight: st.inflight,
		Detail: req.Origin,
	})
	s.mu.Unlock()

	// 授权可能阻塞（等人工答复/走策略端点）：绝不能在锁里做。
	token, aerr := s.authz.Authorize(ctx, exec, req)

	s.mu.Lock()
	st, err = s.ownedLocked(exec, tab)
	if err != nil {
		s.mu.Unlock()
		return ActionResult{}, err
	}
	if aerr != nil {
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelineAuthorizeDenied, TabID: tab, SessionID: exec.SessionID,
			Action: action.Kind, Ref: action.Ref, SnapshotID: action.SnapshotID,
			Generation: st.generation, Mode: st.mode, ErrorCode: errorCode(aerr),
			Queued: st.queued, Inflight: st.inflight, Detail: aerr.Error(),
		})
		s.mu.Unlock()
		return ActionResult{}, aerr
	}
	// 等授权期间可能发生接管/导航/换会话：这些都必须让"入队"本身失效。
	if st.generation != generation || st.epoch != epoch {
		s.mu.Unlock()
		return ActionResult{}, revokedError("等待授权期间控制权或页面已经改变")
	}
	if err := modeRejectsWrite(st.mode, st.detail); err != nil {
		s.mu.Unlock()
		return ActionResult{}, err
	}
	if _, err := s.snapshotForActionLocked(st, action.SnapshotID); err != nil {
		s.mu.Unlock()
		return ActionResult{}, err
	}
	if st.queued >= s.maxQueued {
		s.mu.Unlock()
		return ActionResult{}, appErr(CodeQueueFull, fmt.Sprintf(
			"该 tab 已有 %d 个待派发动作（上限 %d）：本次没有入队、没有派发",
			st.queued, s.maxQueued))
	}

	entry := &queuedAction{
		seq:        s.nextSeqLocked(),
		tab:        tab,
		exec:       exec,
		action:     action,
		snapshot:   rec,
		generation: st.generation,
		epoch:      st.epoch,
		token:      token,
		request:    req,
		enqueuedAt: s.now(),
		done:       make(chan struct{}),
	}
	st.queue = append(st.queue, entry)
	st.queued++
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineEnqueued, TabID: tab, SessionID: exec.SessionID,
		Action: action.Kind, Ref: action.Ref, SnapshotID: action.SnapshotID,
		Generation: st.generation, Mode: st.mode, Queued: st.queued, Inflight: st.inflight,
	})
	// 唤醒该 tab 的执行者（缓冲 1，重复唤醒无害）。
	select {
	case st.wake <- struct{}{}:
	default:
	}
	s.mu.Unlock()

	select {
	case <-entry.done:
		return entry.res, entry.err
	case <-ctx.Done():
		// 调用方放弃等待：动作如果还没派发，就撤销它（"取消后不再派发新动作"）；
		// 已派发的部分只能等它自己结束，返回"结果未知"而不是假装取消成功。
		s.mu.Lock()
		if !entry.dispatchedAt.IsZero() {
			s.appendTimelineLocked(TimelineEntry{
				Kind: TimelineOutcomeUnknown, TabID: tab, SessionID: exec.SessionID,
				Action: action.Kind, Ref: action.Ref, Generation: entry.generation,
				Mode: st.mode, ErrorCode: contracts.CodeOutcomeUnknown,
				Detail: "调用方在派发后取消：结果未知，不重放",
			})
			s.mu.Unlock()
			return ActionResult{}, appErr(contracts.CodeOutcomeUnknown,
				"动作已派发，调用方取消等待：结果未知，不得自动重放")
		}
		entry.canceled = true
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelineCanceled, TabID: tab, SessionID: exec.SessionID,
			Action: action.Kind, Ref: action.Ref, Generation: entry.generation,
			Mode: st.mode, ErrorCode: contracts.CodeControlUncertain,
			Detail: "调用方取消：入队动作被撤销，没有派发",
		})
		s.mu.Unlock()
		return ActionResult{}, revokedError("调用方取消，入队动作已撤销")
	}
}

// runQueue 是每个 tab 唯一的执行者：串行、FIFO。
func (s *Service) runQueue(st *tabState) {
	for {
		s.mu.Lock()
		if s.closed || st.closed {
			s.mu.Unlock()
			return
		}
		if len(st.queue) == 0 {
			s.mu.Unlock()
			select {
			case <-st.wake:
			case <-s.closeCh:
				return
			}
			continue
		}
		entry := st.queue[0]
		st.queue = st.queue[1:]
		// queued 只算"还没被取出来派发"的动作：执行者一旦取走，它就不再
		// 是"排队中"，而是 in-flight 的候选。
		if st.queued > 0 {
			st.queued--
		}
		s.mu.Unlock()
		s.runEntry(st, entry)
	}
}

// runEntry 派发一个动作：派发前复查 → 交给引擎 → 收尾。
func (s *Service) runEntry(st *tabState, entry *queuedAction) {
	// 1) 派发前状态检查：接管/导航/取消/非 Agent 都在这里被挡住，输入不会发出。
	s.mu.Lock()
	if reason := s.dispatchBlockedLocked(st, entry); reason != nil {
		s.finishLocked(st, entry, ActionResult{}, reason, TimelineRevoked)
		s.mu.Unlock()
		return
	}
	st.inflight++
	s.mu.Unlock()

	// 2) 第二次授权检查：就在把输入交给引擎之前。
	//
	// 这一步在锁外（可能走 HTTP/等人工），所以之后必须再核一次控制权：
	// 复查期间被接管，仍然不能派发。
	recheckErr := error(nil)
	if s.authz != nil {
		recheckErr = s.authz.Recheck(context.Background(), entry.exec,
			s.currentRequest(st, entry), entry.token)
	}

	s.mu.Lock()
	if recheckErr != nil {
		st.inflight--
		if st.inflight == 0 && st.mode == ControlPausing {
			st.mode = ControlHuman
			st.detail = ""
		}
		s.finishLocked(st, entry, ActionResult{}, recheckErr, TimelineAuthorizeDenied)
		s.mu.Unlock()
		return
	}
	if reason := s.dispatchBlockedLocked(st, entry); reason != nil {
		st.inflight--
		if st.inflight == 0 && st.mode == ControlPausing {
			st.mode = ControlHuman
			st.detail = ""
		}
		s.finishLocked(st, entry, ActionResult{}, reason, TimelineRevoked)
		s.mu.Unlock()
		return
	}
	entry.dispatchedAt = s.now()
	s.appendTimelineLocked(TimelineEntry{
		Kind: TimelineDispatched, TabID: st.id, SessionID: entry.exec.SessionID,
		Action: entry.action.Kind, Ref: entry.action.Ref, SnapshotID: entry.action.SnapshotID,
		Generation: entry.generation, Mode: st.mode, Queued: st.queued, Inflight: st.inflight,
		Detail: entry.request.Origin,
	})
	s.mu.Unlock()

	// 3) 派发。用独立 context：调用方断开不能把"已经在引擎里的输入"变成
	//    "没发生过"，否则我们会误报"未派发"。
	dispatchCtx, cancel := context.WithTimeout(context.Background(), s.actionTimeout)
	res, actErr := s.backend.Act(dispatchCtx, st.id, entry.action)
	timedOut := actErr != nil && dispatchCtx.Err() == context.DeadlineExceeded
	cancel()

	s.mu.Lock()
	st.inflight--
	entry.res = res
	switch {
	case timedOut:
		// 已派发、结果未知：不重放，并把状态压到 ControlUncertain。
		entry.err = appErr(contracts.CodeOutcomeUnknown, fmt.Sprintf(
			"动作已派发给引擎但 %s 内没有结果：结果未知，不得自动重放；"+
				"在确认静止之前该 tab 保持 ControlUncertain", s.actionTimeout))
		st.mode = ControlUncertain
		st.detail = "已派发输入超时，结果未知"
		s.appendTimelineLocked(TimelineEntry{
			Kind: TimelineOutcomeUnknown, TabID: st.id, SessionID: entry.exec.SessionID,
			Action: entry.action.Kind, Ref: entry.action.Ref, SnapshotID: entry.action.SnapshotID,
			Generation: entry.generation, Mode: st.mode, ErrorCode: contracts.CodeOutcomeUnknown,
			Queued: st.queued, Inflight: st.inflight, Detail: entry.err.Error(),
		})
	case actErr != nil:
		entry.err = actErr
	default:
		entry.err = nil
	}
	if st.inflight == 0 && st.mode == ControlPausing {
		st.mode = ControlHuman
		st.detail = ""
	}
	s.finishLocked(st, entry, res, entry.err, TimelineCompleted)
	s.mu.Unlock()
}

// finishLocked 收尾一个队列项：写结论、减计数、通知等待者。
func (s *Service) finishLocked(st *tabState, entry *queuedAction, res ActionResult, err error, kind string) {
	if entry.done == nil {
		return
	}
	select {
	case <-entry.done:
		// 已收尾过：不要重复关闭 channel。
		return
	default:
	}
	entry.res = res
	entry.err = err
	s.appendTimelineLocked(TimelineEntry{
		Kind: kind, TabID: st.id, SessionID: entry.exec.SessionID,
		Action: entry.action.Kind, Ref: entry.action.Ref, SnapshotID: entry.action.SnapshotID,
		Generation: entry.generation, Mode: st.mode, ErrorCode: errorCode(err),
		Queued: st.queued, Inflight: st.inflight,
		Detail: detailOf(entry, err),
	})
	close(entry.done)
}

// dispatchBlockedLocked 回答"这一项现在还能不能派发"。
//
// 任何一条不成立都返回结构化错误，调用方据此撤销（输入不发出）。
func (s *Service) dispatchBlockedLocked(st *tabState, entry *queuedAction) error {
	if s.closed || st.closed {
		return revokedError("服务或 tab 已关闭")
	}
	if entry.canceled {
		return revokedError("调用方取消")
	}
	if entry.epoch != st.epoch {
		return revokedError("控制权/页面纪元已变化（接管或导航）")
	}
	if entry.generation != st.generation {
		return revokedError("控制权代数已变化（接管或恢复）")
	}
	if err := modeRejectsWrite(st.mode, st.detail); err != nil {
		return err
	}
	if entry.snapshot.navigationID != st.navigationID {
		return revokedError(fmt.Sprintf(
			"快照属于文档 %s，当前文档是 %q：旧引用不能派发", entry.snapshot.navigationID, st.navigationID))
	}
	return nil
}

// currentRequest 用**当前**状态重建授权请求（派发前复查要用最新 origin/文档/代数）。
func (s *Service) currentRequest(st *tabState, entry *queuedAction) AuthorizationRequest {
	req := entry.request
	req.URL = st.url
	req.Origin = OriginOf(st.url)
	req.NavigationID = st.navigationID
	req.Generation = st.generation
	return req
}

// snapshotForActionLocked 校验动作引用的快照是否属于当前代数与当前文档。
func (s *Service) snapshotForActionLocked(st *tabState, snapshotID string) (snapshotRecord, error) {
	rec, ok := st.snapshots[snapshotID]
	if !ok {
		return snapshotRecord{}, appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"快照 %q 不属于该 tab 或已失效：必须先 observe", snapshotID))
	}
	if rec.generation != st.generation {
		return snapshotRecord{}, appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"快照 %q 属于控制代 %d，当前是 %d：接管/恢复之后必须重新 observe",
			snapshotID, rec.generation, st.generation))
	}
	if rec.navigationID != st.navigationID || st.navigationID == "" {
		return snapshotRecord{}, appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"快照 %q 属于文档 %s，当前文档是 %q：必须先 observe",
			snapshotID, rec.navigationID, st.navigationID))
	}
	return rec, nil
}

// ownedLocked 取出 tab 并核对会话归属。
//
// 跨 session 拒绝是稳定结论：不区分"不存在"与"不是你的"，避免枚举别人的 tab。
func (s *Service) ownedLocked(exec contracts.ExecutionContext, tab TabID) (*tabState, error) {
	if strings.TrimSpace(string(exec.SessionID)) == "" {
		return nil, appErr(contracts.CodeUnauthorized, "缺少会话身份：浏览器操作必须绑定会话")
	}
	st, ok := s.tabs[tab]
	if !ok || st.closed {
		return nil, appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"tab %q 不属于当前会话（会话 %s）：跨 session 操作 tab 一律拒绝", tab, exec.SessionID))
	}
	if st.owner != exec.SessionID {
		return nil, appErr(contracts.CodeUnauthorized, fmt.Sprintf(
			"tab %q 属于会话 %s，当前会话是 %s：跨 session 操作 tab 一律拒绝",
			tab, st.owner, exec.SessionID))
	}
	return st, nil
}

// drainLocked 清空队列并让每一项拿到撤销结论。
func (s *Service) drainLocked(st *tabState, cause error) int {
	drained := st.queue
	st.queue = nil
	st.queued = 0
	for _, entry := range drained {
		entry.canceled = true
		s.finishLocked(st, entry, ActionResult{}, cause, TimelineRevoked)
	}
	return len(drained)
}

func (s *Service) nextSeqLocked() uint64 {
	s.seq++
	return s.seq
}

// controlLocked 生成 ControlState（同一把锁内，保证几个字段互相自洽）。
func (st *tabState) controlLocked() ControlState {
	return ControlState{
		TabID:        st.id,
		Mode:         st.mode,
		Generation:   st.generation,
		NavigationID: st.navigationID,
		Queued:       st.queued,
		InFlight:     st.inflight,
		Detail:       st.detail,
	}
}

func detailOf(entry *queuedAction, err error) string {
	if err != nil {
		return err.Error()
	}
	if entry.action.Kind == "" {
		return ""
	}
	return string(entry.action.Kind) + " 已交付引擎（Delivered 不等于业务成功）"
}
