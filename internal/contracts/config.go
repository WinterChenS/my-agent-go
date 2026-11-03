package contracts

import "encoding/json"

// ConfigDocument 是版本化配置的权威载体（基线 §5）。
// 写入必须 CompareAndSwap(expectedRevision)，外部修改冲突返回 ConfigConflict。
type ConfigDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	Revision      uint64          `json:"revision"`
	Body          json.RawMessage `json:"body"`
}

// ExecutionContext 是一次执行调用的上下文。
// ControlGeneration 用于权限/接管绑定，防止过期许可被复用（基线 §6/§7）。
type ExecutionContext struct {
	WorkspaceID       WorkspaceID `json:"workspaceId"`
	SessionID         SessionID   `json:"sessionId"`
	RunID             RunID       `json:"runId"`
	ConfigRevision    uint64      `json:"configRevision"`
	ControlGeneration uint64      `json:"controlGeneration"`
}
