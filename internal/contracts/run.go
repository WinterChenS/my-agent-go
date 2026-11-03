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

// RunSnapshot 是 run 的持久化快照（基线 §6）。
// Error 仅在 failed/interrupted 等异常态存在，正常态为 nil（omitempty）。
type RunSnapshot struct {
	RunID          RunID       `json:"runId"`
	SessionID      SessionID   `json:"sessionId"`
	WorkspaceID    WorkspaceID `json:"workspaceId"`
	State          string      `json:"state"`
	LastSeq        uint64      `json:"lastSeq"`
	ConfigRevision uint64      `json:"configRevision"`
	PiVersion      string      `json:"piVersion"`
	Error          *AppError   `json:"error,omitempty"`
}
