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
	// EventPolicyAutoAllowed 一次调用被“允许完全访问”放行（跳过人工审批）。
	// payload: {"runId","sessionId","toolName","toolCallId"?,"source"?,"resourceScope"?,"permissionMode","reason"}
	//
	// 为什么必须逐条发：跳过审批 ≠ 不留痕。审批卡不出现，但“被放行了什么”
	// 必须能追溯（诊断页查得到），否则完全访问就是一片黑。
	// 它和其它 run 事件走同一个 sink（订阅者 / run_events），不另开记录通道。
	EventPolicyAutoAllowed = "policy.auto_allowed"
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

// autoAllowedPayload 是 EventPolicyAutoAllowed 的载荷。
//
// run/session 在事件封套里也有，但这里再写一份：诊断页把 run_events 按 type 过滤后
// 直接读这一行就能拼出“谁、在哪个会话、哪个工具被放行”，不用再回查封套。
//
// 参数只放 ResourceScope（已归一的真实资源）与工具名，不放完整参数：
// 事件会进诊断包，完整命令/参数属于敏感内容，不在这一条里给（事件流也一样）。
type autoAllowedPayload struct {
	RunID          string `json:"runId"`
	SessionID      string `json:"sessionId"`
	ToolName       string `json:"toolName"`
	ToolCallID     string `json:"toolCallId,omitempty"`
	Source         string `json:"source,omitempty"`
	ResourceScope  string `json:"resourceScope,omitempty"`
	PermissionMode string `json:"permissionMode"`
	Reason         string `json:"reason"`
}
