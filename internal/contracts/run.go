package contracts

// Run 状态枚举（基线 §6）。
// 只有对应 Pi 版本的最终 idle/settled 条件满足才标 completed；prompt accepted 不表示完成。
const (
	RunStateQueued          = "queued"
	RunStateStarting        = "starting"
	RunStateRunning         = "running"
	RunStateWaitingApproval = "waiting_approval"
	RunStateStopping        = "stopping"
	RunStateCompleted       = "completed"
	RunStateFailed          = "failed"
	RunStateCancelled       = "cancelled"
	RunStateInterrupted     = "interrupted"
)

// Pi 技能自动发现的开关状态（RunSnapshot.SkillAutoDiscovery）。
//
// 背景（2026-11-04）：产品不再用 -ns 抑制 Pi 的本地技能发现，Pi 会自己加载
// $HOME/.agents/skills、项目及祖先目录 .agents/skills 等位置里它发现的技能。
// 那份清单只存在于 Pi 进程内，产品没有可信来源，所以快照只记录"发现开着还是关着"，
// 不记录清单；界面遇到空值必须显示"未知"，不能编造或显示成"没有技能"。
const (
	SkillDiscoveryEnabled  = "enabled"
	SkillDiscoveryDisabled = "disabled"
)

// RunUsage.Availability 的取值（RUN06）。
//
// 只有这两种，不允许第三种"看起来像 0"的中间态：没看到用量就是 unavailable，
// 看到用量才写数字。把"没报告"写成 0 会让用户以为自己这一轮没花钱。
const (
	UsageReported    = "reported"
	UsageUnavailable = "unavailable"
)

// RunBudget.ExceededReason 的取值（RUN06）。
const (
	BudgetReasonToolCalls = "tool_calls"
	BudgetReasonTime      = "time"
)

// RunUsage 是一次 run 的用量报告（RUN06）。
//
// 缺失用量时只有 Availability="unavailable"，其余字段因为 omitempty 一律不出现：
// 快照里没有 costUsd 这个键，才等于"不知道费用"；写成 0 是把未知冒充成确定。
// 反过来，provider 明确报告 0 时 CostUSD 必须指向 0（有键、值为 0），两者不可混同。
type RunUsage struct {
	Availability string   `json:"availability"`
	InputTokens  int64    `json:"inputTokens,omitempty"`
	OutputTokens int64    `json:"outputTokens,omitempty"`
	TotalTokens  int64    `json:"totalTokens,omitempty"`
	CostUSD      *float64 `json:"costUsd,omitempty"`     // nil => 不出现；提供方报告的 0 保留为 0
	PriceSource  string   `json:"priceSource,omitempty"` // "provider" 提供方账单；"catalog" Pi 按本地价目表估算
	Estimated    bool     `json:"estimated"`
}

// RunBudget 是一次 run 的预算执行情况（RUN06）。
//
// 记的是"实际用了多少、有没有超、为什么超"，而不是只记限额：
// 只写限额的话，界面无法解释一次 run 为什么失败。
type RunBudget struct {
	MaxToolCalls   int    `json:"maxToolCalls"`
	MaxDurationMs  int64  `json:"maxDurationMs"`
	ToolCalls      int    `json:"toolCalls"`
	Exceeded       bool   `json:"exceeded"`
	ExceededReason string `json:"exceededReason,omitempty"` // "tool_calls" | "time"
}

// RunSnapshot 是 run 的持久化快照（基线 §6）。
// Error 仅在 failed/interrupted 等异常态存在，正常态为 nil（omitempty）。
//
// 技能相关字段刻意分成两段事实。旧实现只有一个 Skills 列表（托管且启用的 ID），
// 却被读作"这一轮实际加载的技能"：开启自动发现后，空列表并不等于"没有技能"——
// Pi 可能已经加载了用户级技能。这两件事必须分开写，界面才能说真话。
type RunSnapshot struct {
	RunID          RunID       `json:"runId"`
	SessionID      SessionID   `json:"sessionId"`
	WorkspaceID    WorkspaceID `json:"workspaceId"`
	State          string      `json:"state"`
	LastSeq        uint64      `json:"lastSeq"`
	ConfigRevision uint64      `json:"configRevision"`
	PiVersion      string      `json:"piVersion"`
	// ManagedSkills 是这一轮通过 --skill 显式加载的托管技能 ID（RES02：旧 run 保持快照）。
	//
	// 空数组 = 确实没有显式加载任何托管技能，而**不是**"这一轮没有加载任何技能"：
	// Pi 自己发现的技能不在这个列表里（见 SkillAutoDiscovery）。
	// 不加 omitempty：空数组本身就是一条事实，字段缺失会被读成"没记录过"。
	ManagedSkills []string `json:"managedSkills"`
	// SkillAutoDiscovery 是这一轮 Pi 侧技能自动发现的开关状态
	// （SkillDiscoveryEnabled / SkillDiscoveryDisabled）。
	// 空值表示这次 run 没有记录（旧快照），界面必须显示"未知"，
	// 不能默认成 enabled 或 disabled。
	SkillAutoDiscovery string `json:"skillAutoDiscovery,omitempty"`
	// Provider/Model 是这次 run 实际使用的 provider 键与模型名（MOD04）。
	// 记录"真实使用"而不是"设置里写着"：模型切换后，旧 run 的审计线索不能跟着变。
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Budget 是这次 run 的预算执行情况；run 创建时就写入限额，之后随工具调用与
	// 时间推进实时更新（不是只在结束时补记），界面才能显示"已用 87/100"。
	Budget *RunBudget `json:"budget,omitempty"`
	// Usage 是用量报告。run 进入终态时一定会有一份：看到用量是 reported，
	// 没看到就是 unavailable——不能因为"没数据"就把字段留空让界面自己猜。
	Usage *RunUsage `json:"usage,omitempty"`
	Error *AppError `json:"error,omitempty"`
}
