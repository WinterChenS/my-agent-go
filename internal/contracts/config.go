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

// EffectiveConfig 是五层配置（默认→用户→工作区→Profile→会话覆盖）合并后的有效结果（基线 §5）。
//
// Values 是规范化后的最终配置；Provenance 以 JSON 路径为键记录每个有效值来自哪一层，
// 用来满足"所有有效值可查询来源"。AgentDir 是本产品独立的 PI_CODING_AGENT_DIR，
// 与用户真实 ~/.pi/agent 无关（CFG04）。
//
// Warnings 记录被规则拒绝的输入（例如项目层试图替换 credentialRef），
// 拒绝必须留痕，不能静默吞掉。
type EffectiveConfig struct {
	Revision   uint64            `json:"revision"`
	Hash       string            `json:"hash"`
	Values     json.RawMessage   `json:"values"`
	Provenance map[string]string `json:"provenance"`
	AgentDir   string            `json:"agentDir"`
	Warnings   []string          `json:"warnings,omitempty"`
}
