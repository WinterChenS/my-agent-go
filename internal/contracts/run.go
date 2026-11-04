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
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
	Error    *AppError `json:"error,omitempty"`
}
