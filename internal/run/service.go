package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/policybridge"
	"my-agent-go/internal/runtime/pi"
)

// 默认值。与基线 §6 对齐。
const (
	// globalMaxActive 是应用进程**唯一**的活跃上限（基线 §6）。
	//
	// 它不是 Options 里的可调项：交互会话、C08 连接器和 C09 调度共用同一个
	// admission queue，任何第二处能够改这个数的地方都会让“全局最多两个 worker”
	// 变成一句没有实现的话。
	globalMaxActive     = 2
	defaultPromptWait   = 60 * time.Second
	defaultStopGrace    = 5 * time.Second
	defaultStopDeadline = 10 * time.Second
	defaultDeltaWindow  = 50 * time.Millisecond
	maxReplayEvents     = 20000

	// 每 run 预算（基线 §6）：100 次工具调用、30 分钟。
	defaultMaxToolCalls   = 100
	defaultMaxRunDuration = 30 * time.Minute
)

// WorkerStarter 允许注入 worker 构造（测试用假 worker；生产用 pi.StartWorker）。
type WorkerStarter func(ctx context.Context, spec pi.WorkerSpec) (pi.Worker, error)

// HistorySink 记录 run 索引。实现由 internal/history 提供（依赖方向 history → run）。
//
// nil 表示不落索引（单元测试与无 DB 场景）。本包只依赖这个最小接口，
// 不认识数据库，也不认识 transcript：索引写在哪、怎么定位 Pi 会话由实现决定。
type HistorySink interface {
	RecordRun(ctx context.Context, snap contracts.RunSnapshot) error
}

// Options 构造 RunService。
//
// 注意身份字段的语义：WorkspaceID/SessionID/WorkspaceDir/ConfigRevision 是
// **兼容默认值**，只在 StartRunRequest 没带这一项时使用（旧 Start(message) 入口）。
// 服务不再把某个会话绑在实例上：同一个实例服务所有会话、所有提交者，
// 每个 run 的身份与策略由请求给出（见 ExecutionBinding）。
type Options struct {
	WorkspaceID  contracts.WorkspaceID
	SessionID    contracts.SessionID
	WorkspaceDir string
	AgentDir     string

	// Installation 是已定位并认证的 Pi。Executable 必填。
	Installation pi.PiInstallation
	// Isolation 是项目资源隔离参数；必须通过 Validate 才能启动。
	Isolation pi.IsolationSpec

	// Provider/Model 交给 Pi 的 --provider/--model。空则用 Pi 自己的默认。
	Provider string
	Model    string

	// ConfigRevision 是这次 run 依据的配置版本，写进快照供审计（MOD04）。
	ConfigRevision uint64

	// ModelEnv 是模型凭证的环境变量（形如 NAME=secret），只经子进程环境交付，
	// 不写进 models.json。与 HarnessEnv 分开是因为二者的生命周期与来源不同。
	ModelEnv []string

	// ManagedSkillIDs 是这一轮通过 --skill 显式加载的托管技能 ID，仅用于快照
	// （RES02：旧 run 保持快照）。真正让技能生效的是 Isolation.Skills 里的路径，
	// 两者由同一次技能编译产出，ID 与路径不会分叉。
	//
	// 空表示没有显式加载托管技能，**不表示这一轮没有任何技能**：Pi 自己发现的
	// 技能由 SkillAutoDiscovery 描述，清单没有可信来源。
	ManagedSkillIDs []string

	// SkillAutoDiscovery 是这一轮 Pi 侧技能自动发现的开关状态
	// （contracts.SkillDiscoveryEnabled / contracts.SkillDiscoveryDisabled）。
	//
	// 必须由绑定层从**实际传给 Pi 的参数**推出来，不在这里写死 "enabled"：
	// 一旦有人把 -ns 加回隔离参数，写死值会立刻变成谎话。留空表示调用方没提供，
	// 快照里如实留空（界面显示未知），不替它猜。
	SkillAutoDiscovery string

	// Policy 为空时不启用执行前授权端点。生产路径必须提供：
	// 没有端点时策略扩展 fail-closed，工具调用一律被拒。
	Policy *policy.Service

	// PolicyCeiling 是系统/工作区/Profile 合并出的权限上限（C06a）。
	// 每个 run 请求的快照都要先过它：只能收紧，不能扩大。
	// 零值等于"不给任何权限"（filesystem=none），也就是 fail-closed 的默认。
	PolicyCeiling PolicyCeiling
	// PolicySnapshot 是请求没带快照时使用的默认快照（旧 Start 入口）。
	// 同样受 PolicyCeiling 约束。
	PolicySnapshot PolicySnapshot
	// PermissionResolver 把请求里的 PermissionRef 解析成不可变快照。
	// 为空时只认识 PolicyCeiling.Ref 这一个引用；解析不了一律结构化失败。
	PermissionResolver PermissionResolver

	// HarnessEnv 是自有 MCP 服务的端点与令牌（harnessmcp.Server.Env()）。
	// run 包只负责透传给 worker，不依赖 harnessmcp 包本身：
	// 谁提供端点谁负责写 mcp.json，两边共用同一份环境变量。
	HarnessEnv []string

	// Attachments 是 app 托管的附件只读存储（C06f）。为空时带附件的提交会被拒。
	// run 只经它取字节，不自己开第二套 blob store：blob 的所有权在绑定层。
	Attachments AttachmentReader
	// Clarification 是本产品自带澄清扩展的身份与超时（C06f）。
	// Enabled=false 时不建澄清桥，也不把任何工具当作本地无副作用澄清。
	Clarification ClarificationOptions

	StartWorker WorkerStarter
	Now         func() time.Time

	// Clock 是停止与预算计时的时钟；为空时由 Now + time.After 组成（生产路径）。
	Clock Clock
	// StopGrace 是"等待 worker 有序退出"的宽限期，默认 5 秒。
	StopGrace time.Duration
	// StopDeadline 是从发起停止算起的清理结论时限，默认 10 秒。
	StopDeadline time.Duration
	// MaxToolCalls 是每 run 的工具调用上限，默认 100。
	MaxToolCalls int
	// MaxRunDuration 是每 run 的运行时长上限，默认 30 分钟。
	MaxRunDuration time.Duration

	// History 是 run 索引写入器（RUN07）。为空表示不落索引。
	//
	// 写入是**同步**的，发生在状态迁移之后（不在 emit 里，emit 太热）。
	// 写到数据库会占住调用方的栈：事件泵的 goroutine 会等这次写完成再继续。
	// 这是刻意的取舍——索引落后于事件流，比索引与状态互相追逐更容易解释；
	// 单连接 SQLite 本来也是串行的，异步写只会把串行点藏进后台 goroutine。
	//
	// 写失败不会让 run 失败：只发一条 diagnostic{kind:"history.record_failed"}。
	// 丢一次索引更新很糟，但把一次可重扫的索引故障变成"这一轮 run 失败"更糟——
	// run 的实际副作用已经发生，把状态报成 failed 会误导用户重试。
	History HistorySink
}

// Service 管理 run 的生命周期。并发安全。
type Service struct {
	opts Options

	mu     sync.Mutex
	runs   map[contracts.RunID]*runState
	queue  []contracts.RunID
	active int
	closed bool
	// cleanupFailed 记录"上次清理未完成"的会话与原因（RUN04）。
	// 只存在内存里：应用重启后没有残留的 owned 进程可以继承，安全性由进程边界保证。
	cleanupFailed map[contracts.SessionID]string

	subMu sync.RWMutex
	subs  []*subscriber

	runSeq atomic.Uint64
}

// subscriber 是事件订阅者。用指针而不是裸函数值，是因为 Go 里函数值只有和 nil
// 才能比较：没有身份就无法「只退订一次」，断开时也就无法精确判断订阅者是否归零。
type subscriber struct {
	fn     func(contracts.Event)
	active bool
}

// settleDecision 是一条被延后执行的结算决定。
type settleDecision struct {
	aborted bool
}

type runState struct {
	id contracts.RunID

	mu    sync.Mutex
	snap  contracts.RunSnapshot
	text  string
	seq   uint64
	state []contracts.Event // 重放用；注意与 snap.State 区分命名

	worker pi.Worker
	bridge *policybridge.Server
	token  string
	// budget 是本次 run 的预算闸门；Policy 未启用时它仍然存在（工具调用预算
	// 与时间预算都要记账），只是没有端点来调用它。
	budget *budgetGate
	// usage 汇总 Pi 报的用量（只在 rs.mu 下读写）。
	usage usageAccumulator
	// stopRequested 表示"这次停止是本进程发起的"（Abort 或预算用尽）。
	// 它不属于状态机，只影响一件事：事件流关闭时不要把主动停止误报成
	// "Pi 意外中断"，也不要在清理结果出来之前抢先写下终态。
	stopRequested bool

	// pending 是等待人工答复的审批。
	pending map[string]policy.ApprovalRequest

	// compactionInProgress / retrying 表示 Pi 正处在「上下文压缩」或「自动重试」之间。
	// 这两段时间里 agent_settled 不代表本轮结束：提前结算会把一个马上还要继续跑的
	// run 报成 completed（RUN01）。
	compactionInProgress bool
	retrying             bool
	// pendingSettle 非 nil 表示已收到 agent_settled，但被上面两个状态挡住。
	pendingSettle *settleDecision
	// lastStopReason / lastErrorMessage 是最后一条助手 message_end 的结束原因，
	// 真实 Pi 记录（provider-error.jsonl）里两条字段都有；stopReason=error
	// 表示这条消息是 provider 失败而不是正常回答。
	lastStopReason   string
	lastErrorMessage string
	// retryFailed / retryErrorMessage 记录最后一次 auto_retry_end 的结论。
	retryFailed       bool
	retryErrorMessage string

	cancel   context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once
	// finished 在 run 释放时关闭：运行时长预算的计时器据此退出，
	// 不会在 run 结束很久之后突然"超时"。
	finished     chan struct{}
	finishedOnce sync.Once
	released     atomic.Bool

	// binding 是 admission 时解析并冻结的身份与策略（C06a）。
	// 只写一次（Start 建 runState 时），之后只读：run 期间不得重新解析。
	binding ExecutionBinding
	// attachBroker 是本次 run 的只读附件端点（C06f）；nil 表示这次 run 没有附件。
	attachBroker *attachmentBroker
	// clarifications 是等待用户回答的澄清（C06f），按 Pi 的 extension UI 请求 ID 索引。
	// 只在 rs.mu 下读写。
	clarifications map[string]*pendingClarification
	// clarificationOpts / clarificationToken 是 admission 时冻结的澄清桥配置与
	// per-run 身份令牌（C06f）。令牌只经子进程环境与 UI 标题前缀流通。
	clarificationOpts  ClarificationOptions
	clarificationToken string
	// queuePos 是这个 run 最近一次上报的排队位次（0 表示不在队列里）。
	// 只在 Service.mu 下读写。
	queuePos int
}

// New 创建 RunService。
func New(opts Options) *Service {
	if opts.StartWorker == nil {
		opts.StartWorker = pi.StartWorker
	}
	if opts.Clock == nil {
		// 只给了 Now（既有字段）时，时钟的 Now 跟着它，定时器仍是真实时间：
		// 把 Now 换成假时间的人不该连带伪造"已经过了 5 秒"。
		opts.Clock = realClock{now: opts.Now}
	}
	if opts.Now == nil {
		opts.Now = opts.Clock.Now
	}
	if opts.StopGrace <= 0 {
		opts.StopGrace = defaultStopGrace
	}
	if opts.StopDeadline <= opts.StopGrace {
		// 结论时限必须晚于升级时限，否则"先升级再判定"没有意义。
		opts.StopDeadline = defaultStopDeadline
		if opts.StopDeadline <= opts.StopGrace {
			opts.StopDeadline = opts.StopGrace
		}
	}
	if opts.MaxToolCalls <= 0 {
		opts.MaxToolCalls = defaultMaxToolCalls
	}
	if opts.MaxRunDuration <= 0 {
		opts.MaxRunDuration = defaultMaxRunDuration
	}
	return &Service{
		opts:          opts,
		runs:          map[contracts.RunID]*runState{},
		cleanupFailed: map[contracts.SessionID]string{},
	}
}

// StartRunRequest 描述一次提交。
//
// 身份与策略是 per-run 输入：同一个 RunService 同时服务多个会话与 C08/C09 的
// 自动化提交，所以这里没有"默认会话"以外的实例级绑定。空字段表示沿用
// Options 里的兼容默认值（旧 Start(message) 路径就是全空）。
type StartRunRequest struct {
	Message string `json:"message"`

	// SessionID 是这次 run 所属会话；空表示沿用服务默认会话。
	SessionID contracts.SessionID `json:"sessionId,omitempty"`
	// WorkspaceID 是这次 run 所属工作区；空表示沿用服务默认工作区。
	WorkspaceID contracts.WorkspaceID `json:"workspaceId,omitempty"`
	// WorkspaceDir 是这次 run 的工作目录（worker 的 cwd）；空表示沿用默认值。
	WorkspaceDir string `json:"workspaceDir,omitempty"`
	// ProfileID 是这次 run 使用的 Profile；空表示没有指定。
	ProfileID contracts.ProfileID `json:"profileId,omitempty"`
	// PermissionRef 指向一份不可变策略快照。它只能解析成不超过
	// 系统/工作区/Profile 上限的快照：解析不了或是更宽，都是结构化错误。
	PermissionRef string `json:"permissionRef,omitempty"`
	// PolicySnapshot 是调用方要求的权限快照。只能收紧，不能扩大；
	// 为空时用 Options.PolicySnapshot（再受上限约束）。
	PolicySnapshot *PolicySnapshot `json:"policySnapshot,omitempty"`
	// ConfigRevision 是这次 run 依据的配置版本；0 表示沿用服务默认值。
	ConfigRevision uint64 `json:"configRevision,omitempty"`
	// Attachments 是这次 run 要投递的附件引用（C06f）。
	//
	// 只带元数据、不带路径与字节，并且会在 admission 时随 workspace/policy 一起
	// 冻结进 ExecutionBinding：run 期间界面换不了它们。
	Attachments []AttachmentRef `json:"attachments,omitempty"`
}

// Subscribe 注册事件订阅者，返回幂等的退订函数。回调在独立 goroutine 中执行，
// 避免阻塞事件泵。
//
// 退订函数是 RUN04 断开侧契约的一部分：最后一个订阅者消失意味着 UI 已经不在了，
// 此时等待中的审批必须 fail-closed（见 denyPendingOnDisconnect），不能让用户看不见的
// 请求继续等 60 秒 TTL。服务关闭之后调用退订同样安全。
func (s *Service) Subscribe(fn func(contracts.Event)) (unsubscribe func()) {
	if fn == nil {
		return func() {}
	}
	sub := &subscriber{fn: fn, active: true}
	s.subMu.Lock()
	s.subs = append(s.subs, sub)
	s.subMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.subMu.Lock()
			if !sub.active {
				s.subMu.Unlock()
				return
			}
			sub.active = false
			for i, cur := range s.subs {
				if cur == sub {
					s.subs = append(s.subs[:i], s.subs[i+1:]...)
					break
				}
			}
			remaining := len(s.subs)
			s.subMu.Unlock()
			if remaining == 0 {
				s.denyPendingOnDisconnect()
			}
		})
	}
}

// Start 接受一次请求并启动（或排队）run。
//
// 返回时的状态一定是真实状态：能立刻启动就是 running，槽位满就是 queued。
// 不会出现"返回 running 但其实还在排队"这种谎报。
//
// 旧入口（只填 Message）仍然有效：身份与策略从 Options 的默认值补齐，
// 它走的还是这同一个队列，不是另一条轻量路径。
func (s *Service) Start(ctx context.Context, req StartRunRequest) (contracts.RunSnapshot, error) {
	text := strings.TrimSpace(req.Message)
	if text == "" {
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeUnsupported, "消息为空", false)
	}

	// 身份与策略在 admission 时解析并冻结（C06a）。解析失败就不建 run：
	// 越权的提交不应该在队列里留下一个"等会儿再跑"的影子。
	id := contracts.RunID(fmt.Sprintf("run-%d-%d", s.opts.Now().Unix(), s.runSeq.Add(1)))
	binding, err := s.resolveBinding(req, id)
	if err != nil {
		return contracts.RunSnapshot{}, err
	}
	if err := s.validate(binding); err != nil {
		return contracts.RunSnapshot{}, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeInternal, "服务已关闭", false)
	}
	// 清理结果未知的会话不得自动重启：放行等于把"不确定"显示成"清理成功"（RUN04）。
	if reason, blocked := s.cleanupFailed[binding.SessionID()]; blocked {
		s.mu.Unlock()
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeCleanupFailed,
			fmt.Sprintf("会话 %s 上次清理未完成，拒绝重启：%s", binding.SessionID(), reason), false)
	}
	rs := &runState{
		id:      id,
		text:    text,
		binding: binding,
		snap: contracts.RunSnapshot{
			RunID:          id,
			SessionID:      binding.SessionID(),
			WorkspaceID:    binding.WorkspaceID,
			State:          contracts.RunStateQueued,
			ConfigRevision: binding.ConfigRevision(),
			PiVersion:      s.opts.Installation.Version,
			Provider:       s.opts.Provider,
			Model:          s.opts.Model,
			// 两段技能事实分开写：托管显式加载的 ID 清单 + Pi 自动发现的开关。
			// 空数组照样序列化成 []（不是 null/缺失），避免被读成"没记录"。
			ManagedSkills:      append([]string{}, s.opts.ManagedSkillIDs...),
			SkillAutoDiscovery: s.opts.SkillAutoDiscovery,
			// 预算限额从 run 创建时就写进快照：界面在第一次工具调用之前就能
			// 显示"0/100、30 分钟"，而不是等用过之后才突然出现。
			Budget: &contracts.RunBudget{
				MaxToolCalls:  s.opts.MaxToolCalls,
				MaxDurationMs: s.opts.MaxRunDuration.Milliseconds(),
			},
		},
		pending:  map[string]policy.ApprovalRequest{},
		done:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	// 澄清桥的配置与 per-run 身份令牌在 admission 时冻结：run 期间不得重新解析。
	// 拿不到令牌就不建桥（宁可没有澄清，也不要一个无法验证身份的"澄清"）。
	rs.clarificationOpts = s.opts.Clarification.normalize()
	if rs.clarificationOpts.Enabled {
		token, terr := newClarificationToken()
		if terr != nil {
			s.mu.Unlock()
			return contracts.RunSnapshot{}, terr
		}
		rs.clarificationToken = token
	}
	s.runs[id] = rs
	canStart := s.active < globalMaxActive && len(s.queue) == 0
	if canStart {
		s.active++
	} else {
		// 排队不启动：这是 RUN03 的核心断言——第三个 run 不能偷偷跑起来。
		// 入队与位次上报在同一个临界区里完成，避免"入队了但位次还是 0"。
		s.queue = append(s.queue, id)
		s.publishQueuePositionsLocked()
	}
	s.mu.Unlock()

	if !canStart {
		// 排队也是一个真实状态：索引里必须能看到它，否则重启后 UI 只知道
		// "有个 run"，不知道它当时还没拿到槽位。
		s.recordHistory(rs)
		return rs.snapshot(), nil
	}

	// 必须真的迁移状态，而不是只发一条事件：否则内部仍是 queued，
	// 后面 queued → running 会被状态表拒绝。
	if err := s.transition(rs, contracts.RunStateStarting); err != nil {
		return rs.snapshot(), err
	}
	if err := s.launch(ctx, rs, text); err != nil {
		return rs.snapshot(), err
	}
	return rs.snapshot(), nil
}

// resolveBinding 在 admission 解析 per-run 身份与策略，并冻结成 ExecutionBinding。
//
// 解析顺序：请求显式字段 → Options 默认值。策略快照分三步：
//  1. 请求带 PermissionRef 时先解析成快照（解析不了就是结构化失败）；
//  2. 请求同时带 PolicySnapshot 时两者必须描述同一份策略（不允许两个真话）；
//  3. 最终快照必须过 PolicyCeiling.Narrow：只能收紧，不能扩大。
func (s *Service) resolveBinding(req StartRunRequest, id contracts.RunID) (ExecutionBinding, error) {
	sessionID := req.SessionID
	if strings.TrimSpace(string(sessionID)) == "" {
		sessionID = s.opts.SessionID
	}
	workspaceID := req.WorkspaceID
	if strings.TrimSpace(string(workspaceID)) == "" {
		workspaceID = s.opts.WorkspaceID
	}
	workspaceDir := strings.TrimSpace(req.WorkspaceDir)
	if workspaceDir == "" {
		workspaceDir = s.opts.WorkspaceDir
	}
	revision := req.ConfigRevision
	if revision == 0 {
		revision = s.opts.ConfigRevision
	}
	ref := strings.TrimSpace(req.PermissionRef)

	want := s.opts.PolicySnapshot
	if ref != "" {
		resolved, err := s.resolvePermissionRef(ref)
		if err != nil {
			return ExecutionBinding{}, err
		}
		if req.PolicySnapshot != nil && !samePermissions(resolved.Permissions, req.PolicySnapshot.Permissions) {
			return ExecutionBinding{}, contracts.NewAppError(contracts.CodeConfigConflict,
				fmt.Sprintf("permissionRef %s 与 policySnapshot 描述的不是同一份策略：不能同时声明两个权限结论", ref), false)
		}
		want = resolved
	}
	if req.PolicySnapshot != nil {
		want = *req.PolicySnapshot
	}
	snapshot, err := s.opts.PolicyCeiling.Narrow(want)
	if err != nil {
		return ExecutionBinding{}, err
	}

	return ExecutionBinding{
		ExecutionContext: contracts.ExecutionContext{
			WorkspaceID:       workspaceID,
			SessionID:         sessionID,
			RunID:             id,
			ConfigRevision:    revision,
			ControlGeneration: 1,
		},
		WorkspaceID:    workspaceID,
		WorkspaceDir:   workspaceDir,
		ProfileID:      req.ProfileID,
		PermissionRef:  ref,
		PolicySnapshot: snapshot,
		// 附件引用与工作区/策略同一条规矩：admission 时冻结，run 期间不得替换。
		Attachments: append([]AttachmentRef{}, req.Attachments...),
	}, nil
}

// resolvePermissionRef 把引用解析成快照。解析不了就失败，不回退到"最宽的那份"。
func (s *Service) resolvePermissionRef(ref string) (PolicySnapshot, error) {
	if s.opts.PermissionResolver != nil {
		return s.opts.PermissionResolver.ResolvePermission(ref)
	}
	if ceilingRef := strings.TrimSpace(s.opts.PolicyCeiling.Ref); ceilingRef != "" && ceilingRef == ref {
		return PolicySnapshot{
			Permissions: s.opts.PolicyCeiling.Permissions,
			Revision:    s.opts.PolicyCeiling.Revision,
		}, nil
	}
	return PolicySnapshot{}, contracts.NewAppError(contracts.CodeNotFound,
		"未知的 permissionRef: "+ref+"（没有可用的策略目录）", false)
}

// samePermissions 比较两份权限是否描述同一件事（文件系统等级归一后比较）。
func samePermissions(a, b config.Permissions) bool {
	nameA, okA := config.NormalizeFilesystem(a.Filesystem)
	nameB, okB := config.NormalizeFilesystem(b.Filesystem)
	return okA && okB && nameA == nameB && a.Shell == b.Shell && a.Network == b.Network
}

// publishQueuePositionsLocked 把当前队列位次上报给 UI。必须持有 s.mu。
//
// 位次变化也要上报：排队 run 从第 2 位变成第 1 位却没人告诉前端，
// 界面就只能一直显示一个过期的名次。
func (s *Service) publishQueuePositionsLocked() {
	for i, id := range s.queue {
		rs, ok := s.runs[id]
		if !ok {
			continue
		}
		pos := i + 1
		if rs.queuePos == pos {
			continue
		}
		rs.queuePos = pos
		s.emit(rs, EventRunState, statePayload{
			State:         contracts.RunStateQueued,
			Previous:      contracts.RunStateQueued,
			QueuePosition: pos,
		})
	}
}

// validate 检查启动前置条件。缺任何一项都不能"先跑起来再说"。
func (s *Service) validate(binding ExecutionBinding) error {
	if strings.TrimSpace(s.opts.Installation.Executable) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "未指定 Pi 可执行文件", false)
	}
	if strings.TrimSpace(binding.WorkspaceDir) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "未指定工作区目录", false)
	}
	if strings.TrimSpace(s.opts.AgentDir) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "未指定隔离 agentDir", false)
	}
	// 隔离参数不完整时拒绝启动：带着"项目资源还能加载"的状态跑 agent 更危险。
	return s.opts.Isolation.Validate()
}

// IsolationArgs 返回**实际**交给 Pi 的隔离参数。
//
// 诊断展示必须读这里，而不是在展示侧另建一份"看起来一样"的参数：
// 两处各写一份的历史结果是界面显示 --no-mcp、实际却启用了 harness MCP，
// 诊断信息说谎比没有诊断信息更糟——它会把人带去错误的方向。
func (s *Service) IsolationArgs() []string { return s.opts.Isolation.Args() }

// launch 启动 worker、建策略端点、下发 prompt。失败时负责资源清理与终态收尾。
//
// 这里的每一个与身份相关的东西（工作目录、会话、配置 revision、策略快照、
// 端点绑定）都读 rs.binding：它是 admission 时冻结的那一份。实例上的 Options
// 只提供与 run 无关的安装件与隔离参数。
func (s *Service) launch(ctx context.Context, rs *runState, text string) error {
	binding := rs.binding
	runCtx, cancel := context.WithCancel(context.Background())
	rs.cancel = cancel

	// 预算闸门必须在建端点之前就位：端点拿到的 Gate 就是它。
	gate := s.newBudgetGate(rs)
	rs.budget = gate

	// 每个 run 一个策略端点：审批绑定 session+run，跨 run 不共享结论。
	var env []string
	if s.opts.Policy != nil {
		srv, err := policybridge.Start(runCtx, policybridge.Options{
			Service: s.opts.Policy,
			Binding: policybridge.Binding{
				SessionID:         binding.SessionID(),
				RunID:             rs.id,
				ConfigRevision:    binding.ConfigRevision(),
				ControlGeneration: binding.ExecutionContext.ControlGeneration,
			},
			// 预算在执行前授权之前判定：超限的调用连审批都不会创建。
			Gate: gate.check,
			// C06f：产品自带的澄清工具是**本地无副作用**的。判据是身份
			// （工具名 + 加载它的扩展路径），不是工具名——第三方同名工具
			// 路径不同，仍然走下面正常的 policy.Authorize（ask/deny）。
			LocalTools: func(call policy.ToolCall, identity string) (policy.Result, bool) {
				if rs.clarificationOpts.MatchesToolCall(call, identity) {
					return policy.Result{
						Decision: policy.DecisionAllow,
						Reason:   "产品自带的本地无副作用澄清工具（按扩展身份识别）",
					}, true
				}
				return policy.Result{}, false
			},
			OnPending: func(p policy.ApprovalRequest) {
				rs.mu.Lock()
				rs.pending[p.ID] = p
				rs.mu.Unlock()
				_ = s.transition(rs, contracts.RunStateWaitingApproval)
				s.emit(rs, EventApprovalRequest, map[string]any{
					"approvalId":    p.ID,
					"toolName":      p.ToolName,
					"toolCallId":    p.ToolCallID,
					"arguments":     p.Arguments,
					"expiresAtUnix": p.ExpiresAtUnix,
				})
			},
		})
		if err != nil {
			cancel()
			s.finishFailed(rs, err)
			return err
		}
		rs.bridge = srv
		env = append(env, srv.Env()...)
		rs.token = tokenFromEnv(env)
	}

	// 自有 MCP 的端点与令牌：与策略端点同样是"只经环境变量交付"，
	// 不写进 mcp.json 明文（那里只有 ${...} 引用）。
	env = append(env, s.opts.ModelEnv...)
	env = append(env, s.opts.HarnessEnv...)

	// C06f：附件只经每 run 一枚令牌的只读端点交付。端点必须在启动 worker 之前
	// 就绪（令牌要进环境）；WorkerSpec 落地之后它就随 run 一起关。
	if len(binding.Attachments) > 0 {
		broker, berr := startAttachmentBroker(runCtx, s.opts.Attachments, binding)
		if berr != nil {
			cancel()
			s.closeBridge(rs)
			s.finishFailed(rs, berr)
			return berr
		}
		rs.mu.Lock()
		rs.attachBroker = broker
		rs.mu.Unlock()
		env = append(env, broker.Env()...)
	}
	// 澄清身份令牌：产品自带的澄清扩展把它放进每个 UI 请求的标题前缀，
	// Go 侧据此确认"这个请求来自产品自带的澄清工具"（不是猜工具名）。
	if rs.clarificationToken != "" {
		env = append(env, EnvClarificationToken+"="+rs.clarificationToken)
	}

	// per-run 身份与策略**最后**追加：同名的实例级环境变量不能把这一份事实
	// 盖掉（os/exec 同名去重时以最后一次出现为准）。
	env = append(env, binding.Env()...)

	args := append([]string{"--mode", "rpc"}, s.opts.Isolation.Args()...)
	// 会话必须落在隔离 agentDir 下：否则 Pi 会写进用户真实配置目录，
	// 既污染用户数据，也让"这次 run 到底读了哪些会话"无法审计。
	args = append(args, "--session-dir", filepath.Join(s.opts.AgentDir, "sessions"))
	if s.opts.Provider != "" {
		args = append(args, "--provider", s.opts.Provider)
	}
	if s.opts.Model != "" {
		args = append(args, "--model", s.opts.Model)
	}

	w, err := s.opts.StartWorker(ctx, pi.WorkerSpec{
		Installation: s.opts.Installation,
		WorkingDir:   binding.WorkspaceDir,
		AgentDir:     s.opts.AgentDir,
		Env:          env,
		Args:         args,
	})
	if err != nil {
		cancel()
		s.closeBridge(rs)
		s.finishFailed(rs, err)
		return err
	}
	rs.worker = w

	go s.pump(rs, runCtx)

	// prompt 只下发一次：写失败或超时都不自动重放（可能已部分送达）。
	prompt, _ := json.Marshal(map[string]string{"type": "prompt", "message": text})
	callCtx, callCancel := context.WithTimeout(ctx, defaultPromptWait)
	defer callCancel()
	if _, err := w.Call(callCtx, prompt); err != nil {
		go s.stopWorker(rs)
		s.finishFailed(rs, err)
		return err
	}
	if err := s.transition(rs, contracts.RunStateRunning); err != nil {
		return err
	}
	s.startRunBudget(rs, gate)
	return nil
}

// stopWorker 异步关闭 worker，不在调用方栈上阻塞。
func (s *Service) stopWorker(rs *runState) {
	if rs.worker != nil {
		_ = rs.worker.Close(context.Background())
	}
}

// pump 把 Pi 原始事件映射成产品事件，并处理最终 idle。
func (s *Service) pump(rs *runState, ctx context.Context) {
	defer s.closeDone(rs)

	var (
		pending   strings.Builder
		lastFlush = time.Now()
		ticker    = time.NewTicker(defaultDeltaWindow)
	)
	defer ticker.Stop()

	flush := func() {
		if pending.Len() == 0 {
			return
		}
		s.emit(rs, EventMessageDelta, map[string]any{"text": pending.String()})
		pending.Reset()
		lastFlush = time.Now()
	}

	events := rs.worker.Events()
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case <-ticker.C:
			// 50ms 文本合并：流式 delta 逐条推 UI 会造成大量无意义重排（RUN02）。
			if time.Since(lastFlush) >= defaultDeltaWindow {
				flush()
			}
		case ev, ok := <-events:
			if !ok {
				flush()
				if rs.isStopRequested() {
					// 停止是本进程发起的：终态由 stopRun/预算路径按清理结论决定。
					// 这里再记一次 interrupted 会把"正常取消"谎报成"清理未完成"。
					return
				}
				// 事件流关闭（进程退出）而没有 settled：这是"结果未知"，不是完成。
				s.interrupted(rs, "Pi 事件流在最终 idle 之前关闭（进程退出或输出中断）")
				return
			}
			// 文本必须先于后续非文本事件落地（RUN02）：否则界面会先显示 tool.start，
			// 再补上「我来执行一个命令」这句说明，叙述顺序颠倒；事件日志里 delta
			// 也会排到工具结果之后，回放时同样错位。
			if ev.Type != "message_update" {
				flush()
			}
			if s.handleRaw(rs, ev, &pending) {
				flush()
				return
			}
		}
	}
}

// handleRaw 处理一条原始事件；返回 true 表示本轮已终止。
func (s *Service) handleRaw(rs *runState, ev pi.RawEvent, pending *strings.Builder) bool {
	if ev.Type == "message_update" {
		if delta, ok := textDelta(ev.Record); ok {
			pending.WriteString(delta)
			return false
		}
	}

	switch ev.Type {
	case "tool_execution_start":
		var e struct {
			ToolCallID string          `json:"toolCallId"`
			ToolName   string          `json:"toolName"`
			Args       json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(ev.Record, &e); err == nil {
			s.emit(rs, EventToolStart, map[string]any{
				"toolCallId": e.ToolCallID, "toolName": e.ToolName, "args": e.Args,
			})
		}
	case "tool_execution_end":
		var e struct {
			ToolCallID string `json:"toolCallId"`
			ToolName   string `json:"toolName"`
			IsError    bool   `json:"isError"`
			Result     struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				StructuredContent struct {
					ExitCode *int `json:"exit_code"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(ev.Record, &e); err == nil {
			var text strings.Builder
			for _, c := range e.Result.Content {
				text.WriteString(c.Text)
			}
			s.emit(rs, EventToolEnd, map[string]any{
				"toolCallId": e.ToolCallID, "toolName": e.ToolName,
				"isError": e.IsError, "text": text.String(),
				"exitCode": e.Result.StructuredContent.ExitCode,
			})
		}
	case "message_end":
		var e struct {
			Message piMessage `json:"message"`
		}
		_ = json.Unmarshal(ev.Record, &e)
		if e.Message.Role == "assistant" {
			s.recordUsage(rs, e.Message)
			stopReason := strings.TrimSpace(e.Message.StopReason)
			rs.mu.Lock()
			rs.lastStopReason = stopReason
			rs.lastErrorMessage = strings.TrimSpace(e.Message.ErrorMessage)
			rs.mu.Unlock()
			payload := map[string]any{}
			if stopReason != "" {
				// 没拿到 stopReason 就不写这个键：编一个 "" 等于把“Pi 没说”
				// 谎报成“Pi 说是空的”。
				payload["stopReason"] = stopReason
			}
			s.emit(rs, EventMessageDone, payload)
		}
	case "agent_end":
		// agent_end 会把整轮消息重发一遍：只补 message_end 没覆盖到的用量，
		// 重复的同一条靠去重键挡掉（否则费用会翻倍）。
		s.recordAgentEndUsage(rs, ev.Record)
	case "compaction_start":
		rs.mu.Lock()
		rs.compactionInProgress = true
		rs.mu.Unlock()
	case "compaction_end":
		return s.finishBlockingPhase(rs, func() { rs.compactionInProgress = false })
	case "auto_retry_start":
		rs.mu.Lock()
		rs.retrying = true
		rs.mu.Unlock()
	case "auto_retry_end":
		var e struct {
			Success    bool   `json:"success"`
			FinalError string `json:"finalError"`
		}
		_ = json.Unmarshal(ev.Record, &e)
		return s.finishBlockingPhase(rs, func() {
			rs.retrying = false
			rs.retryFailed = !e.Success
			rs.retryErrorMessage = strings.TrimSpace(e.FinalError)
		})
	case pi.TypeAgentSettled:
		// agent_settled 是唯一的完成信号，而它的契约是
		// {"type":"agent_settled","aborted":bool}。形状对不上时不能猜：
		// 猜 completed 就是把“不知道”报成“做完了”（基线 §6）。
		var h struct {
			Aborted *bool `json:"aborted"`
		}
		if err := json.Unmarshal(ev.Record, &h); err != nil {
			s.failCompatibility(rs, "载荷不是预期的 JSON 对象: "+err.Error())
			return true
		}
		if h.Aborted == nil {
			s.failCompatibility(rs, "缺少 aborted 布尔字段")
			return true
		}
		return s.handleSettled(rs, *h.Aborted)
	case pi.TypeProtocolDiagnostic:
		var d struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Record, &d)
		s.emit(rs, EventDiagnostic, map[string]any{"kind": d.Kind, "message": d.Message})
	case pi.TypeExtensionUIRequest:
		// C06f：扩展 UI 请求 → 类型化澄清事件（或者 fail-closed 拒绝）。
		// 它不终结本轮，所以不返回 true。
		s.handleExtensionUIRequest(rs, ev)
	}
	return false
}

// textDelta 从 message_update 记录里取文本增量。
func textDelta(raw json.RawMessage) (string, bool) {
	var e struct {
		AssistantMessageEvent struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", false
	}
	if e.AssistantMessageEvent.Type != "text_delta" {
		return "", false
	}
	return e.AssistantMessageEvent.Delta, true
}

// settle 处理 agent_settled：这是唯一的完成信号。
//
// 三次落点：
//  1. compaction / auto_retry 正在进行 → 挂起（settle.deferred），等它们结束后补做；
//  2. aborted 或已在 stopping → cancelled；
//  3. 最后一条助手消息以 error 结束或 auto_retry_end.success==false → failed
//     （PROVIDER_ERROR）；否则才是 completed。
//
// 返回值与 handleRaw 一致：true 表示本轮已终结，事件泵可以退出。
func (s *Service) handleSettled(rs *runState, aborted bool) bool {
	rs.mu.Lock()
	if blocker := rs.settlementBlockerLocked(); blocker != "" {
		rs.pendingSettle = &settleDecision{aborted: aborted}
		rs.mu.Unlock()
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":      "settle.deferred",
			"message":   fmt.Sprintf("%s 进行中收到 agent_settled，结算延后到它结束", blocker),
			"blockedBy": blocker,
		})
		return false
	}
	rs.mu.Unlock()
	s.applySettle(rs, aborted)
	return true
}

// settlementBlockerLocked 返回当前阻止结算的原因；空表示不阻塞。只在 rs.mu 下调用。
func (rs *runState) settlementBlockerLocked() string {
	switch {
	case rs.compactionInProgress:
		return "compaction"
	case rs.retrying:
		return "auto_retry"
	}
	return ""
}

// finishBlockingPhase 结束一个“阻止结算”的阶段（压缩或重试），并在无人再挡且
// 已有挂起的 settle 时补做结算。返回 true 表示本轮已终结。
func (s *Service) finishBlockingPhase(rs *runState, clear func()) bool {
	rs.mu.Lock()
	clear()
	pending := rs.pendingSettle
	blocked := rs.settlementBlockerLocked() != ""
	rs.mu.Unlock()
	if pending == nil || blocked {
		return false
	}
	s.applySettle(rs, pending.aborted)
	return true
}

// applySettle 按当前的停止原因与重试结论落地终态。
func (s *Service) applySettle(rs *runState, aborted bool) {
	rs.mu.Lock()
	rs.pendingSettle = nil
	state := rs.snap.State
	stopReason := rs.lastStopReason
	errorMessage := rs.lastErrorMessage
	retryFailed := rs.retryFailed
	retryErrorMessage := rs.retryErrorMessage
	rs.mu.Unlock()

	// waiting_approval → completed/failed 不是合法迁移。旧实现把迁移错误丢掉，
	// 结果是「这一轮其实已经结束，界面却永远停在等待确认」。审批是“人此刻在场”
	// 的决定：本轮结束就没人会回答了，必须先 fail-closed 释放等待，再落终态。
	if state == contracts.RunStateWaitingApproval {
		s.abandonPendingApprovals(rs, "run_settled")
		_ = s.transition(rs, contracts.RunStateRunning)
		state = rs.snapshot().State
	}

	if aborted || state == contracts.RunStateStopping {
		_ = s.transition(rs, contracts.RunStateCancelled)
		s.release(rs)
		return
	}
	if stopReason == "error" || retryFailed {
		msg := errorMessage
		if msg == "" {
			msg = retryErrorMessage
		}
		if msg == "" {
			msg = "provider 调用失败：Pi 报告本轮以 error 结束"
		}
		s.failProvider(rs, msg)
		return
	}
	_ = s.transition(rs, contracts.RunStateCompleted)
	s.release(rs)
}

// abandonPendingApprovals 在结算时释放所有等待中的审批，并给出拒绝结论。
//
// 为什么记成 deny 而不是“消失”：界面把 approval.resolved 当作“这条请求不再是
// 待办”的唯一信号；没有它，审批卡片会一直挂在那儿等一个永远不会来的答复。
// 而这一轮已经结束，工具调用不可能再被放行，deny 正是真实的最终结果。
// 不经过 bridge：run 结算后桥会关闭，阻塞在 Authorize 上的调用方拿到的就是错误，
// 这里只负责把“不会有人回答”这件事记录下来。
func (s *Service) abandonPendingApprovals(rs *runState, reason string) {
	rs.mu.Lock()
	ids := make([]string, 0, len(rs.pending))
	for id := range rs.pending {
		ids = append(ids, id)
	}
	rs.pending = map[string]policy.ApprovalRequest{}
	rs.mu.Unlock()

	for _, id := range ids {
		s.emit(rs, EventApprovalResolved, map[string]any{
			"approvalId": id,
			"choice":     string(policy.ApprovalDeny),
			"reason":     reason,
		})
	}
}

// failProvider 把“provider 真的失败了”落成 failed + PROVIDER_ERROR。
// 重试耗尽也走这里：Pi 自己的重试已经用完，Harness 不能把它报成 completed。
func (s *Service) failProvider(rs *runState, message string) {
	if !IsTerminal(rs.snapshot().State) {
		_ = s.transition(rs, contracts.RunStateFailed)
	}
	s.emit(rs, EventRunError, runErrorPayload{
		Code: contracts.CodeProviderError, Message: message, Retryable: true,
	})
	s.release(rs)
}

// failCompatibility 处理“完成语义不认识”：终态 failed + COMPATIBILITY，不是诊断后继续跑。
func (s *Service) failCompatibility(rs *runState, detail string) {
	if !IsTerminal(rs.snapshot().State) {
		_ = s.transition(rs, contracts.RunStateFailed)
	}
	s.emit(rs, EventRunError, runErrorPayload{
		Code:      contracts.CodeCompatibility,
		Message:   "无法识别的完成语义（Pi 协议不匹配）: " + detail,
		Retryable: false,
	})
	s.release(rs)
}

// denyPendingOnDisconnect 在最后一个事件订阅者消失时，把等待中的审批全部拒绝。
//
// 为什么要 fail-closed：审批是“人此刻在场”的决定。UI 已经不在了，继续等 60 秒
// TTL 只会让工具白等、用户在界面上也看不到这个请求；放行更是把孤立的等待变成
// 未授权的副作用（RUN04）。
func (s *Service) denyPendingOnDisconnect() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	runs := make([]*runState, 0, len(s.runs))
	for _, rs := range s.runs {
		runs = append(runs, rs)
	}
	s.mu.Unlock()

	for _, rs := range runs {
		rs.mu.Lock()
		ids := make([]string, 0, len(rs.pending))
		for id := range rs.pending {
			ids = append(ids, id)
		}
		rs.mu.Unlock()
		for _, id := range ids {
			s.denyApproval(rs, id, "ui_disconnected")
		}
	}
}

// denyApproval 经策略端点把一条等待中的审批回答成 deny，并把 run 从
// waiting_approval 拉回 running：工具随即拿到 deny，不再卡在审批上。
//
// 正常的 ResolveApproval 路径可能与断开赛跑，所以“找不到/已经答复”只记诊断，
// 不当成 run 失败。
func (s *Service) denyApproval(rs *runState, approvalID, reason string) {
	if rs.bridge == nil {
		return
	}
	client := policybridge.NewClient(rs.bridge.Endpoint(), rs.token, rs.snapshot().SessionID, 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Resolve(ctx, approvalID, policy.ApprovalDeny)

	rs.mu.Lock()
	_, still := rs.pending[approvalID]
	delete(rs.pending, approvalID)
	state := rs.snap.State
	rs.mu.Unlock()

	if !still {
		// 正常答复路径已经处理过这条审批：不重复发“已拒绝”，也不改状态。
		return
	}
	if err != nil {
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":    "approval.disconnect_deny_skipped",
			"message": fmt.Sprintf("断开时拒绝审批 %s 失败：%v", approvalID, err),
		})
		return
	}
	s.emit(rs, EventApprovalResolved, map[string]any{
		"approvalId": approvalID,
		"choice":     string(policy.ApprovalDeny),
		"reason":     reason,
	})
	if state == contracts.RunStateWaitingApproval {
		_ = s.transition(rs, contracts.RunStateRunning)
	}
}

// Abort 停止一个 run。
//
// 排队中的 run 直接取消且永不启动；运行中的 run 先进入 stopping（UI 2 秒内可见），
// 再由 worker 有序关闭（静默宽限 → 关 stdin → 超时强杀）。
func (s *Service) Abort(ctx context.Context, id contracts.RunID) error {
	s.mu.Lock()
	rs, ok := s.runs[id]
	if !ok {
		s.mu.Unlock()
		return contracts.NewAppError(contracts.CodeNotFound, "run 不存在: "+string(id), false)
	}
	queuedIdx := -1
	for i, q := range s.queue {
		if q == id {
			queuedIdx = i
			break
		}
	}
	if queuedIdx >= 0 {
		s.queue = append(s.queue[:queuedIdx], s.queue[queuedIdx+1:]...)
		rs.queuePos = 0
		// 后面排队的人名次前移了，必须如实上报：否则界面会一直显示旧名次。
		s.publishQueuePositionsLocked()
	}
	s.mu.Unlock()

	if queuedIdx >= 0 {
		_ = s.transition(rs, contracts.RunStateCancelled)
		s.closeDone(rs)
		return nil
	}

	state := rs.snapshot().State
	if IsTerminal(state) {
		return nil
	}

	// 先标记"停止是本进程发起的"，再迁移状态：pump 一旦看到事件流关闭，
	// 就不会把它误报成 Pi 意外中断（那个谎会盖掉真实的清理结论）。
	rs.mu.Lock()
	rs.stopRequested = true
	rs.mu.Unlock()

	// C06f：停止时先把未回答的澄清收口为 cancelled。
	// 不先收口的话，Pi 侧的 dialog 还在等一个永远不会来的答复，
	// 有序停止就变成等到超时才强杀。
	s.cancelPendingClarifications(rs, "run_stopped")

	_ = s.transition(rs, contracts.RunStateStopping)
	go s.stopRun(rs)
	return nil
}

// stopRun 执行有序停止，并守住"必须有明确清理结果"的底线（RUN04）。
//
// 时间线（基线 §6）：
//  1. stopping 由 Abort 同步迁移，2 秒内可见，不依赖这里；
//  2. StopGrace（默认 5 秒）仍未退出 → 强制终止 worker 进程树 + 诊断事件；
//  3. StopDeadline（默认 10 秒，总计）还没有明确清理结果 → interrupted +
//     CLEANUP_FAILED，并阻止同一会话重启。
//
// "进程被杀了"不等于"清理完成"：升级之后仍然要等 Close 的结论（RPC05）。
func (s *Service) stopRun(rs *runState) {
	done := make(chan error, 1)
	go func() { done <- s.stopWorkerWait(rs) }()

	select {
	case err := <-done:
		s.finishStopped(rs, err)
		return
	case <-s.after(s.opts.StopGrace):
	}

	s.escalateStop(rs)
	select {
	case err := <-done:
		s.finishStopped(rs, err)
		return
	case <-s.after(s.opts.StopDeadline - s.opts.StopGrace):
	}

	reason := fmt.Sprintf("停止超时：%s 内未拿到明确的清理结果", s.opts.StopDeadline)
	s.markCleanupFailed(rs, reason)
	s.finishInterrupted(rs, reason)
}

// finishStopped 按清理结果收尾：只有 Close 明确返回成功才算 cancelled。
func (s *Service) finishStopped(rs *runState, err error) {
	if err != nil {
		// 清理未完成时不能显示"已取消"：如实报 interrupted + 原因，并阻断会话重启。
		s.markCleanupFailed(rs, err.Error())
		s.finishInterrupted(rs, err.Error())
		return
	}
	if st := rs.snapshot().State; st == contracts.RunStateStopping {
		_ = s.transition(rs, contracts.RunStateCancelled)
	}
	s.release(rs)
}

// escalateStop 在宽限过后强制终止 worker 进程树，并如实记一条诊断。
//
// 诊断必须说明"到哪个时限才升级"：静默强杀会让排查者误以为进程是自己退出的。
func (s *Service) escalateStop(rs *runState) {
	fk, ok := rs.worker.(interface{ ForceKill() error })
	if !ok {
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":    "stop.escalation_unavailable",
			"message": fmt.Sprintf("停止超过 %s 仍未退出，但当前 worker 不支持强制终止进程树", s.opts.StopGrace),
		})
		return
	}
	msg := fmt.Sprintf("停止超过 %s 仍未退出，已强制终止 worker 进程树", s.opts.StopGrace)
	if err := fk.ForceKill(); err != nil {
		msg += "（强制终止失败：" + err.Error() + "）"
	}
	s.emit(rs, EventDiagnostic, map[string]any{"kind": "stop.escalated", "message": msg})
}

func (s *Service) stopWorkerWait(rs *runState) error {
	if rs.worker == nil {
		return nil
	}
	return rs.worker.Close(context.Background())
}

func (s *Service) finishFailed(rs *runState, cause error) {
	code := contracts.CodeInternal
	msg := cause.Error()
	if ae, ok := cause.(*contracts.AppError); ok {
		code = ae.Code
		msg = ae.Message
	}
	if !IsTerminal(rs.snapshot().State) {
		_ = s.transition(rs, contracts.RunStateFailed)
	}
	s.emit(rs, EventRunError, runErrorPayload{Code: code, Message: msg})
	s.release(rs)
}

func (s *Service) finishInterrupted(rs *runState, reason string) {
	if !IsTerminal(rs.snapshot().State) {
		_ = s.transition(rs, contracts.RunStateInterrupted)
	}
	s.emit(rs, EventRunError, runErrorPayload{
		Code: contracts.CodeCleanupFailed, Message: reason, Retryable: false,
	})
	s.release(rs)
}

func (s *Service) interrupted(rs *runState, reason string) {
	if IsTerminal(rs.snapshot().State) {
		s.release(rs)
		return
	}
	s.finishInterrupted(rs, reason)
}

func (s *Service) closeBridge(rs *runState) {
	if rs.bridge != nil {
		_ = rs.bridge.Close(context.Background())
	}
}

// closeAttachmentBroker 关闭本次 run 的附件端点（幂等）。
//
// 端点随 run 关闭：run 结束后即使有人拿着 URL+令牌过来，也只能拿到 CLOSED。
func (s *Service) closeAttachmentBroker(rs *runState) {
	rs.mu.Lock()
	broker := rs.attachBroker
	rs.attachBroker = nil
	rs.mu.Unlock()
	if broker != nil {
		_ = broker.Close()
	}
}

// release 释放并发槽位并按 FIFO 启动下一个排队 run。重复调用无副作用。
func (s *Service) release(rs *runState) {
	if !rs.released.CompareAndSwap(false, true) {
		return
	}
	// C06f：run 终止时把未回答的澄清收口为 cancelled 并清理 pending。
	// 必须在关 worker 之前发：发不出去的答复只能当诊断，但"永远挂着的请求"不是选项。
	s.cancelPendingClarifications(rs, "run_terminated")
	s.closeAttachmentBroker(rs)
	rs.finishedOnce.Do(func() { close(rs.finished) })
	s.closeBridge(rs)
	if rs.cancel != nil {
		rs.cancel()
	}
	// 正常结束（completed/failed）不经过 stopRun，所以这里必须自己关 worker。
	//
	// 不关的后果是真实存在的：Pi 在 agent_settled 之后仍在跑，自己的工作目录、
	// 事件流句柄都还占着（C06f 的真实 Pi 端到端测试就是被它卡在 TempDir 上）。
	// 关闭是幂等的：停止路径已经关过时这里返回同一个结果，不会二次杀进程。
	go func() {
		if err := s.stopWorkerWait(rs); err != nil {
			s.emit(rs, EventDiagnostic, map[string]any{
				"kind":    "run.worker_close_failed",
				"message": "run 结束后关闭 worker 未完成：" + err.Error(),
			})
		}
	}()

	s.mu.Lock()
	if s.active > 0 {
		s.active--
	}
	if s.closed || len(s.queue) == 0 {
		s.mu.Unlock()
		return
	}
	nextID := s.queue[0]
	s.queue = s.queue[1:]
	// 队列缩短了：还排在后面的人名次前移，如实上报。
	s.publishQueuePositionsLocked()
	next, ok := s.runs[nextID]
	if ok {
		next.queuePos = 0
		s.active++
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	if IsTerminal(next.snapshot().State) {
		s.release(next)
		return
	}
	_ = s.transition(next, contracts.RunStateStarting)
	if err := s.launch(context.Background(), next, next.text); err != nil {
		return
	}
}

// transition 迁移状态并发出事件；非法迁移返回错误且不改变状态。
func (s *Service) transition(rs *runState, to string) error {
	rs.mu.Lock()
	from := rs.snap.State
	if from == to {
		rs.mu.Unlock()
		return nil
	}
	if !CanTransition(from, to) {
		rs.mu.Unlock()
		return contracts.NewAppError(contracts.CodeInternal,
			fmt.Sprintf("非法状态迁移: %s → %s", from, to), false)
	}
	rs.snap.State = to
	if IsTerminal(to) {
		// 终态必须给出 usage 结论：没看到用量就是 unavailable，
		// 不能留空让界面把"没数据"显示成 0 token / 0 元。
		rs.finalizeUsageLocked()
	}
	rs.mu.Unlock()

	s.emit(rs, EventRunState, statePayload{State: to, Previous: from})
	// 在状态与 seq 都已更新（emit 里推进 seq）之后才写索引：sink 看到的
	// 必须是迁移**后**的事实，而不是一个马上就被覆盖的旧快照。
	// 不放在 emit 里：emit 是本服务最热的路径（每个 delta 都走它）。
	s.recordHistory(rs)
	return nil
}

// recordHistory 把当前快照写给 HistorySink（RUN07）。
//
// 失败只发诊断，不让 run 变失败：索引是**派生**数据（可以从 transcript 与事件
// 重建），而 run 已经产生真实副作用。把一次写盘故障升级成"这一轮失败了"，
// 用户会看到一个其实做完的 run 被标成失败并去重试。
//
// 用 context.Background() 而不是 runCtx：终态迁移往往发生在 run 被取消之后，
// 用已取消的 ctx 写索引会把"取消"错报成"索引写入失败"。写入由调用方同步
// 等待（不做后台 goroutine，RUN07 禁止常驻历史 worker）。
func (s *Service) recordHistory(rs *runState) {
	if s.opts.History == nil {
		return
	}
	snap := rs.snapshot()
	if err := s.opts.History.RecordRun(context.Background(), snap); err != nil {
		s.emit(rs, EventDiagnostic, map[string]any{
			"kind":    "history.record_failed",
			"message": fmt.Sprintf("写入历史索引失败（run %s，状态 %s）：%v", snap.RunID, snap.State, err),
		})
	}
}

// emit 写入事件（seq 单调递增并保留用于重放）并广播给订阅者。
func (s *Service) emit(rs *runState, typ string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"error":"payload marshal failed"}`)
	}

	rs.mu.Lock()
	rs.seq++
	ev := contracts.Event{
		SchemaVersion: contracts.EventSchemaVersion,
		EventID:       fmt.Sprintf("%s-%d", rs.id, rs.seq),
		// 事件归属读**这个 run 自己的**身份：一个服务同时服务多个会话，
		// 把实例上的 session 写进事件会让前端把 A 会话的事件挂到 B 会话下。
		WorkspaceID: rs.snap.WorkspaceID,
		SessionID:   rs.snap.SessionID,
		RunID:       rs.id,
		Seq:         rs.seq,
		Type:        typ,
		OccurredAt:  s.opts.Now().UTC(),
		Payload:     raw,
	}
	rs.state = append(rs.state, ev)
	if len(rs.state) > maxReplayEvents {
		rs.state = rs.state[len(rs.state)-maxReplayEvents:]
	}
	rs.snap.LastSeq = rs.seq
	rs.mu.Unlock()

	s.subMu.RLock()
	subs := make([]func(contracts.Event), 0, len(s.subs))
	for _, sub := range s.subs {
		if sub.active {
			subs = append(subs, sub.fn)
		}
	}
	s.subMu.RUnlock()
	for _, fn := range subs {
		go fn(ev)
	}
}

// snapshot 返回当前快照的副本。
func (rs *runState) snapshot() contracts.RunSnapshot {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.snap
}

func (s *Service) closeDone(rs *runState) {
	rs.doneOnce.Do(func() { close(rs.done) })
}

// Get 返回 run 快照。
func (s *Service) Get(id contracts.RunID) (contracts.RunSnapshot, error) {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return contracts.RunSnapshot{}, contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	return rs.snapshot(), nil
}

// Replay 返回 afterSeq 之后的事件，用于重连补发（不重复、不丢）。
func (s *Service) Replay(id contracts.RunID, afterSeq uint64) ([]contracts.Event, error) {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return nil, contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]contracts.Event, 0, len(rs.state))
	for _, ev := range rs.state {
		if ev.Seq > afterSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// ResolveApproval 提交一次人工授权结论。
//
// 必须经策略端点转发：端点在等这个答复，直接改策略服务状态会让等待方超时。
func (s *Service) ResolveApproval(ctx context.Context, id contracts.RunID, approvalID string, choice string) error {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return contracts.NewAppError(contracts.CodeNotFound, "run 不存在", false)
	}
	if rs.bridge == nil {
		return contracts.NewAppError(contracts.CodeInternal, "该 run 没有启用授权端点", false)
	}
	c, err := parseChoice(choice)
	if err != nil {
		return err
	}
	client := policybridge.NewClient(rs.bridge.Endpoint(), rs.token, rs.snapshot().SessionID, 10*time.Second)
	if err := client.Resolve(ctx, approvalID, c); err != nil {
		return err
	}
	rs.mu.Lock()
	delete(rs.pending, approvalID)
	state := rs.snap.State
	rs.mu.Unlock()

	s.emit(rs, EventApprovalResolved, map[string]any{"approvalId": approvalID, "choice": string(c)})
	if state == contracts.RunStateWaitingApproval {
		_ = s.transition(rs, contracts.RunStateRunning)
	}
	return nil
}

func parseChoice(v string) (policy.ApprovalChoice, error) {
	switch policy.ApprovalChoice(strings.TrimSpace(v)) {
	case policy.ApprovalOnce:
		return policy.ApprovalOnce, nil
	case policy.ApprovalSessionScope:
		return policy.ApprovalSessionScope, nil
	case policy.ApprovalDeny:
		return policy.ApprovalDeny, nil
	}
	return "", contracts.NewAppError(contracts.CodeUnsupported, "未知审批选项: "+v, false)
}

func tokenFromEnv(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "MYAGENT_POLICY_TOKEN=") {
			return strings.TrimPrefix(kv, "MYAGENT_POLICY_TOKEN=")
		}
	}
	return ""
}

// ActiveCount 返回当前活跃 run 数（诊断/测试用）。
func (s *Service) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// QueuedCount 返回排队 run 数（诊断/测试用）。
func (s *Service) QueuedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Close 关闭服务：停掉所有 run 并释放资源。
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	runs := make([]*runState, 0, len(s.runs))
	for _, rs := range s.runs {
		runs = append(runs, rs)
	}
	s.mu.Unlock()

	var firstErr error
	for _, rs := range runs {
		if IsTerminal(rs.snapshot().State) {
			continue
		}
		if err := s.Abort(ctx, rs.id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// after 把 duration 换成时钟通道；d<=0 时立刻触发（"总时限已到"这种边界）。
func (s *Service) after(d time.Duration) <-chan time.Time {
	if d <= 0 {
		ch := make(chan time.Time, 1)
		ch <- s.opts.Clock.Now()
		return ch
	}
	return s.opts.Clock.After(d)
}

// isStopRequested 报告本次 run 的停止是否由本进程发起。
func (rs *runState) isStopRequested() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.stopRequested
}

// markCleanupFailed 记录"这个会话上次清理未完成"及原因，阻止它被自动重启。
//
// 按 run 自己的会话记：同一个服务上别的会话没理由因为这一轮清理失败而被拦住。
func (s *Service) markCleanupFailed(rs *runState, reason string) {
	sessionID := rs.snapshot().SessionID
	s.mu.Lock()
	s.cleanupFailed[sessionID] = reason
	s.mu.Unlock()
}

// ClearCleanupFailure 清除"上次清理未完成"的阻断标记，允许同一会话再次启动。
//
// 只有显式的用户/操作员动作才能调用它：清理结果未知时自动放行，等于把
// "不确定"显示成"清理成功"（RUN04 明确禁止）。阻断只存在本次进程内存里，
// 应用重启同样会清掉——重启后没有残留的 owned 进程可以被继承。
func (s *Service) ClearCleanupFailure(sessionID contracts.SessionID) {
	s.mu.Lock()
	delete(s.cleanupFailed, sessionID)
	s.mu.Unlock()
}

// EnsureAgentDir 保证隔离 agentDir 存在。
func EnsureAgentDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "agentDir 为空", false)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建 agentDir 失败: "+err.Error(), false)
	}
	return nil
}
