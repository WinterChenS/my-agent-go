// Package scheduler 是 C09 的本地持久定时任务调度器（后端）。
//
// 边界（与 plan 的 C09 一致）：
//   - 调度器只回答"什么时候该开一轮会话"。它**不**实现 agent loop、不调 Pi、
//     不建第二套执行通道：所有派发都交给应用进程里那唯一的 run.Service
//     （全局 admission queue，全局活跃上限 2），因此交互会话与定时任务争的是
//     同一个 FIFO，而不是"两套队列各跑两个"。
//   - 定义与 occurrence 落 SQLite（迁移 004_schedules.sql）。occurrence 的唯一键
//     (schedule_id, scheduled_at) 是时钟回跳、进程重启、DST 两侧只触发一次的
//     地基——不是"应用层尽量不重复"。
//   - 时间语义一律走 IANA 时区名 + 可控时钟。DSL 与 DST 规则都从运行时 tzdata
//     读取（见 clock.go 的 time/tzdata 导入），绝不写死偏移量。
package scheduler

import (
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// occurrence 状态。语义与 run 状态机对齐，但不相等：
//   - queued/running/waiting_approval 是"有 run 在场"的三种状态，从真实 run 事件
//     投影而来；
//   - completed/failed/cancelled 是终态；
//   - skipped 表示这一跳没有派发（原因见 Reason*），它同样是终态——被跳过的一次
//     不会在下一个 tick 里"补上"，否则跳过就没有意义；
//   - outcome_unknown 表示结果未知（重启后 run 已经不在了）。它保持未知、**不重放**：
//     把"不确定"重跑一遍等于用重复副作用换一个更舒服的状态字。
const (
	StateQueued          = "queued"
	StateRunning         = "running"
	StateWaitingApproval = "waiting_approval"
	StateCompleted       = "completed"
	StateFailed          = "failed"
	StateCancelled       = "cancelled"
	StateSkipped         = "skipped"
	StateOutcomeUnknown  = "outcome_unknown"
)

// 跳过一个 occurrence 的原因。原因必须是稳定的机器可读串：界面按它本地化，
// 不允许拿 Go 文案去猜。
const (
	// ReasonPreviousStillRunning：同一任务的上一条还没到终态（不重入）。
	ReasonPreviousStillRunning = "previous_still_running"
	// ReasonPermissionRefInvalid：权限引用解析不到（失效/被删除）。
	ReasonPermissionRefInvalid = "permission_ref_invalid"
	// ReasonPermissionRefExceedsCeiling：权限引用比系统/工作区/Profile 上限更宽。
	ReasonPermissionRefExceedsCeiling = "permission_ref_exceeds_ceiling"
	// ReasonMisfireBeyondCatchUp：漏跑但超出 CatchUpLimit，不补跑。
	ReasonMisfireBeyondCatchUp = "misfire_beyond_catch_up"
	// ReasonMisfireSkipped：漏跑策略就是"不补"。
	ReasonMisfireSkipped = "misfire_skipped"
	// ReasonApprovalTimeout：无人值守审批等待超时（副作用 0，不重放）。
	ReasonApprovalTimeout = "approval_timeout"
	// ReasonRunInterrupted：run 被中断（不等于成功）。
	ReasonRunInterrupted = "run_interrupted"
	// ReasonRestartRunMissing：重启后找不到这一轮 run，结果未知。
	ReasonRestartRunMissing = "restart_run_missing"
	// ReasonSchedulePaused：任务已暂停却收到了触发。
	ReasonSchedulePaused = "schedule_paused"
	// ReasonScheduleDeleted：任务已删除却收到了触发。
	ReasonScheduleDeleted = "schedule_deleted"
	// ReasonSessionCreateFailed：创建普通会话失败（没有可用执行身份）。
	ReasonSessionCreateFailed = "session_create_failed"
	// ReasonDispatchFailed：提交给 RunService 失败（拒绝/配置缺失等）。
	ReasonDispatchFailed = "dispatch_failed"
)

// MisfirePolicy 是"应用没在跑时错过的那些时刻怎么办"。
type MisfirePolicy string

const (
	// MisfireCatchUp 补跑，但补跑条数由 Schedule.CatchUpLimit 界定。
	MisfireCatchUp MisfirePolicy = "catch_up"
	// MisfireSkip 一条都不补，只记 skipped。
	MisfireSkip MisfirePolicy = "skip"
)

// SessionKind 是调度触发生成的会话 kind。
//
// 005_session_catalog.sql 把 kind 的取值留给各自任务登记：会话目录只列普通会话
// （chat），调度会话必须有自己的 kind，否则它会混进用户的历史列表。
const SessionKind = "scheduled"

const (
	// DefaultMisfirePolicy 是未指定时的默认漏跑策略。
	//
	// 选 catch_up 的理由：定时任务的价值在"最终执行过"，一次都不补等于任务在
	// 停机期间静默消失；但补跑必须有界（见 DefaultCatchUpLimit），否则打开电脑
	// 的瞬间会把停机一周的任务连开几十轮。
	DefaultMisfirePolicy = MisfireCatchUp
	// DefaultCatchUpLimit 是默认补跑条数：**只补一次**，不是每个漏掉的 tick 都补。
	DefaultCatchUpLimit = 1
	// MaxCatchUpLimit 是补跑条数上限。再大就等于"把停机期间的全部历史重跑一遍"，
	// 那既不是用户想要的，也违反"未知结果不重放"的保守口径。
	MaxCatchUpLimit = 10
	// AutoPauseAfterFailures 是自动暂停阈值：连续两次失败就停用（CAS）。
	AutoPauseAfterFailures = 2
)

// Schedule 是一条本地持久定时任务定义。
//
// Revision 是 CAS 令牌：用户可见字段的每次被接受的变更 +1；调度器自己的记账
// （Last/Next/Current/ConsecutiveFailures）不推进它。
type Schedule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Cron     string `json:"cron"`
	TimeZone string `json:"timezone"`
	// WorkspaceID 决定工作目录与工作区上限；ProfileID 可空。
	WorkspaceID contracts.WorkspaceID `json:"workspaceId"`
	ProfileID   contracts.ProfileID   `json:"profileId,omitempty"`
	// Prompt 是交给新一轮会话的任务文本。
	Prompt string `json:"prompt"`
	// PermissionRef 指向一份不可变权限快照。派发前必须证明它不比
	// 系统/工作区/Profile 上限更宽；解析不到或更宽都只记 skipped。
	PermissionRef string `json:"permissionRef,omitempty"`
	// OutputTarget 是产出落点的标签（本任务后端只持久化，不做投递）。
	OutputTarget  string        `json:"outputTarget,omitempty"`
	MisfirePolicy MisfirePolicy `json:"misfirePolicy"`
	// CatchUpLimit 是"补跑几条"。0 视为未指定 → DefaultCatchUpLimit（1）；
	// 想要"一条都不补"请显式选 MisfireSkip，而不是填 0——0 既可能被读成
	// "没填"也可能被读成"零条"，这种二义性在停机恢复场景里代价很高。
	CatchUpLimit int    `json:"catchUpLimit"`
	Enabled      bool   `json:"enabled"`
	Revision     uint64 `json:"revision"`

	// LastScheduledAt 是最近一次**已入账**的计划时刻（不是"跑完了"）。
	LastScheduledAt time.Time `json:"lastScheduledAt"`
	// NextScheduledAt 是下一次唤醒时刻（UTC 瞬时）。
	NextScheduledAt time.Time `json:"nextScheduledAt"`
	// CurrentSessionID/CurrentRunID 是最近一次派发产生的会话与 run（真实 ID）。
	CurrentSessionID contracts.SessionID `json:"currentSessionId,omitempty"`
	CurrentRunID     contracts.RunID     `json:"currentRunId,omitempty"`
	// ConsecutiveFailures 是连续失败次数；任一次 completed 清零。
	ConsecutiveFailures int       `json:"consecutiveFailures"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
	// DeletedAt 非零表示已软删除（行保留，run/会话/产物不受影响）。
	DeletedAt time.Time `json:"deletedAt,omitempty"`
}

// Deleted 报告任务是否已软删除。
func (s Schedule) Deleted() bool { return !s.DeletedAt.IsZero() }

// Occurrence 是一次计划时刻的落地记录。
//
// (ScheduleID, ScheduledAt) 是主键：同一次计划时刻在库里只能有一行。
type Occurrence struct {
	ScheduleID  string              `json:"scheduleId"`
	ScheduledAt time.Time           `json:"scheduledAt"`
	State       string              `json:"state"`
	Reason      string              `json:"reason,omitempty"`
	SessionID   contracts.SessionID `json:"sessionId,omitempty"`
	RunID       contracts.RunID     `json:"runId,omitempty"`
	// Attempts 是派发次数。永远 ≤ 1：失败/超时都不重放，所以它也是
	// "有没有重放过"的直接证据。
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// OccurrenceEvent 是一次状态轨迹（只追加）。
type OccurrenceEvent struct {
	Seq         int64               `json:"seq"`
	ScheduleID  string              `json:"scheduleId"`
	ScheduledAt time.Time           `json:"scheduledAt"`
	At          time.Time           `json:"at"`
	State       string              `json:"state"`
	Previous    string              `json:"previous,omitempty"`
	Reason      string              `json:"reason,omitempty"`
	SessionID   contracts.SessionID `json:"sessionId,omitempty"`
	RunID       contracts.RunID     `json:"runId,omitempty"`
}

// occurrenceTransitions 是 occurrence 的合法状态迁移表。
//
// 与 run 的状态机一样，误迁移会让界面显示一个不存在的结论（例如"跳过"直接变
// "完成"），所以不在表内的一律拒绝写入，而不是"尽量迁就"。
var occurrenceTransitions = map[string][]string{
	StateQueued: {
		StateRunning,
		StateWaitingApproval,
		StateCompleted,
		StateFailed,
		StateCancelled,
		StateSkipped,
		StateOutcomeUnknown,
	},
	StateRunning: {
		StateWaitingApproval,
		StateCompleted,
		StateFailed,
		StateCancelled,
		StateOutcomeUnknown,
	},
	StateWaitingApproval: {
		StateRunning,
		StateCompleted,
		StateFailed,
		StateCancelled,
		StateOutcomeUnknown,
	},
	// 终态不再迁出：跳过的不会补上，失败/超时的不会重放。
	StateCompleted:      {},
	StateFailed:         {},
	StateCancelled:      {},
	StateSkipped:        {},
	StateOutcomeUnknown: {},
}

// IsTerminalOccurrence 报告 occurrence 是否已到终态。
func IsTerminalOccurrence(state string) bool {
	next, ok := occurrenceTransitions[state]
	return ok && len(next) == 0
}

// canTransitionOccurrence 判定 from → to 是否合法（from 为空表示新建）。
func canTransitionOccurrence(from, to string) bool {
	if from == "" {
		return to == StateQueued
	}
	if from == to {
		return false
	}
	for _, s := range occurrenceTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// validate 校验并归一任务定义。
//
// 失败一律返回结构化错误码（前端按 Code 分支），并且**不做宽容解释**：
// 六段 cron、Local、非 IANA 缩写都不会"就近当成某个合法值"。
func (s *Schedule) validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "任务 ID 不能为空", false)
	}
	if strings.TrimSpace(string(s.WorkspaceID)) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "任务必须绑定工作区（否则无法确定执行范围）", false)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "任务提示词不能为空", false)
	}
	if strings.TrimSpace(s.Cron) == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "cron 表达式不能为空", false)
	}
	if _, err := parseCron(s.Cron); err != nil {
		return err
	}
	if err := validateTimeZone(s.TimeZone); err != nil {
		return err
	}

	if s.MisfirePolicy == "" {
		s.MisfirePolicy = DefaultMisfirePolicy
	}
	switch s.MisfirePolicy {
	case MisfireCatchUp, MisfireSkip:
	default:
		return contracts.NewAppError(contracts.CodeUnsupported,
			"未知的漏跑策略: "+string(s.MisfirePolicy), false)
	}
	if s.CatchUpLimit == 0 {
		s.CatchUpLimit = DefaultCatchUpLimit
	}
	if s.CatchUpLimit < 0 || s.CatchUpLimit > MaxCatchUpLimit {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"补跑条数必须在 0 与上限之间，且不能是负数", false)
	}
	return nil
}

// validateTimeZone 只接受 IANA 的 "Area/Location" 形式的时区名。
//
// 为什么必须带斜杠：Go 的 tzdata 里存在 EST/MST/HST 这类固定偏移的"缩写文件"，
// time.LoadLocation("EST") 会**成功**，但它既不带 DST 规则也不带地区语义——
// 用户以为选了"美东"，实际拿到的是一个永不切换的 -5。Local 同理（依赖机器设置）。
// 所以这里按名字形状直接拒绝，并如实说明原因，而不是"加载成功就算通过"。
func validateTimeZone(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing, "时区不能为空（必须是 IANA 名，例如 Asia/Shanghai）", false)
	}
	if !strings.Contains(trimmed, "/") {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"时区必须是 IANA 的 Area/Location 名（例如 Asia/Shanghai）："+trimmed+
				" 这类缩写/Local 不带地区与夏令时语义", false)
	}
	if _, err := loadLocation(trimmed); err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"未知的 IANA 时区: "+trimmed, false)
	}
	return nil
}

// occurrenceStateOfRun 把 run 状态映射成 occurrence 状态；空表示"不改变"。
//
// stopping 刻意不映射：它只是"正在停"，结论还没出来；把中间态写进 occurrence
// 会让界面在 200ms 内显示一个既不是成功也不是失败的第三种终态。
func occurrenceStateOfRun(state string) string {
	switch state {
	case contracts.RunStateQueued:
		return StateQueued
	case contracts.RunStateStarting, contracts.RunStateRunning:
		return StateRunning
	case contracts.RunStateWaitingApproval:
		return StateWaitingApproval
	case contracts.RunStateCompleted:
		return StateCompleted
	case contracts.RunStateFailed, contracts.RunStateInterrupted:
		return StateFailed
	case contracts.RunStateCancelled:
		return StateCancelled
	}
	return ""
}
