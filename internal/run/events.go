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
	// EventBudgetExceeded 预算用尽。payload 见 budgetExceededPayload。
	EventBudgetExceeded = "budget.exceeded"
	// EventClarificationRequested 需要用户回答一个内容澄清问题（C06f）。
	// payload: {"requestId","kind"(select|confirm|input|editor),"question","options"?}
	//
	// 它**不是**审批：回答它不产生任何许可（见 clarification.go）。
	EventClarificationRequested = "clarification.requested"
	// EventClarificationResolved 澄清已收口。
	// payload: {"requestId","outcome"(value|confirmed|cancelled),"kind"?, "value"?,"confirmed"?,"reason"?}
	EventClarificationResolved = "clarification.resolved"
)

// runErrorPayload 是 EventRunError 的载荷。
type runErrorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// statePayload 是 EventRunState 的载荷。
//
// QueuePosition 只在排队时出现（omitempty）：前端据此显示“你前面还有几个”，
// 服务内部字段不算证据，名次必须从事件里读。
type statePayload struct {
	State         string `json:"state"`
	Previous      string `json:"previous"`
	QueuePosition int    `json:"queuePosition,omitempty"`
}
