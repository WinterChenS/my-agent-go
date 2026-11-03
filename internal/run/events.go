package run

// 产品事件类型。前端只认这些类型；Pi 的原生 type 只在 payload.raw 里出现。
//
// 为什么不直接把 Pi 原生事件透传给 UI：原生事件属于 Pi 版本契约（1.5.0 的
// message_update/assistantMessageEvent 结构），换版本就会变；产品事件是我们自己的
// 稳定契约，适配层负责映射，UI 不承担版本兼容。
const (
	// EventRunState 状态变更。payload: {"state":"running","previous":"starting"}
	EventRunState = "run.state"
	// EventMessageDelta 助手文本增量。payload: {"text":"..."}
	EventMessageDelta = "message.delta"
	// EventMessageDone 一条助手消息结束。payload: {"stopReason":"..."}
	EventMessageDone = "message.done"
	// EventToolStart 工具开始执行。payload: {"toolCallId","toolName","args"}
	EventToolStart = "tool.start"
	// EventToolEnd 工具执行结束。payload: {"toolCallId","toolName","isError","text","exitCode"}
	EventToolEnd = "tool.end"
	// EventApprovalRequest 需要人工授权。payload: {"approvalId","toolName","source","scope","reason"}
	EventApprovalRequest = "approval.request"
	// EventApprovalResolved 审批已答复。payload: {"approvalId","choice"}
	EventApprovalResolved = "approval.resolved"
	// EventRunError run 失败。payload: {"code","message","retryable"}
	EventRunError = "run.error"
	// EventDiagnostic 协议/运行时诊断。payload: {"kind","message"}
	EventDiagnostic = "diagnostic"
)

// runErrorPayload 是 EventRunError 的载荷。
type runErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// statePayload 是 EventRunState 的载荷。
type statePayload struct {
	State    string `json:"state"`
	Previous string `json:"previous"`
}
