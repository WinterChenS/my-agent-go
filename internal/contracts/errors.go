package contracts

import "strings"

// 统一错误码：前端必须按 Code 分支处理，禁止靠翻译文案解析错误。
// 新增错误时在此集中登记，避免各处散落字符串。
const (
	CodeInternal                   = "INTERNAL_ERROR"
	CodeConfigConflict             = "CONFIG_CONFLICT"
	CodeNotFound                   = "NOT_FOUND"
	CodeProtocolRecordTooLarge     = "PROTOCOL_RECORD_TOO_LARGE"
	CodeQueryTooShort              = "QUERY_TOO_SHORT"
	CodeUnsupported                = "UNSUPPORTED"
	CodeUnauthorized               = "UNAUTHORIZED"
	CodeVerificationFailed         = "VERIFICATION_FAILED"
	CodeNeedsHuman                 = "NEEDS_HUMAN"
	CodeCredentialStoreUnavailable = "CREDENTIAL_STORE_UNAVAILABLE"
	CodeStaleSnapshot              = "STALE_SNAPSHOT"
	CodeOutcomeUnknown             = "OUTCOME_UNKNOWN"
	CodeControlUncertain           = "CONTROL_UNCERTAIN"
	CodeCleanupFailed              = "CLEANUP_FAILED"
	CodeConfigMissing              = "CONFIG_MISSING"
	CodeUnknownTarget              = "UNKNOWN_TARGET"
	CodeUnknownGate                = "UNKNOWN_GATE"
	CodeUnknownSuite               = "UNKNOWN_SUITE"
	CodeMissingRequiredCase        = "MISSING_REQUIRED_CASE"
	CodeStaleRevision              = "STALE_REVISION"
	CodeReportHashMismatch         = "REPORT_HASH_MISMATCH"
	CodeReviewerMissing            = "REVIEWER_MISSING"
	CodeEmptySuite                 = "EMPTY_SUITE"
	CodeAllSkipped                 = "ALL_SKIPPED"
	CodeRunnerMissing              = "RUNNER_MISSING"
	CodeDependencyMissing          = "DEPENDENCY_MISSING"

	// 模型连接测试的分类（C02 / MOD02）。
	// 分开而不是统一成"连接失败"：401 要用户改凭证，429 只需等待，
	// 超时可能是端点慢或网络问题——把三者混成一句话等于让用户自己猜。
	CodeRateLimited         = "RATE_LIMITED"
	CodeProviderUnreachable = "PROVIDER_UNREACHABLE"
	CodeProviderError       = "PROVIDER_ERROR"

	// RPC / worker 相关（F03）。
	// 语义约定：这些错误都不可自动重放。命令是否已在 Pi 侧生效未知，
	// 自动重试会造成重复执行副作用（基线 §6「不自动重放未知结果调用」）。
	CodeRpcTimeout       = "RPC_TIMEOUT"
	CodeRpcCancelled     = "RPC_CANCELLED"
	CodeRpcWriteFailed   = "RPC_WRITE_FAILED"
	CodeRpcWorkerExited  = "RPC_WORKER_EXITED"
	CodeRpcInvalidCall   = "RPC_INVALID_CALL"
	CodeRpcCommandFailed = "RPC_COMMAND_FAILED"
	CodeWorkerClosed     = "WORKER_CLOSED"
	CodeWorkerStartFail  = "WORKER_START_FAILED"

	// CodeBudgetExceeded 表示本次 run 的预算（100 次工具调用 / 30 分钟）已用尽。
	// 与 CLEANUP_FAILED 分开：预算用尽是正常终止理由，不是清理失败。
	CodeBudgetExceeded = "BUDGET_EXCEEDED"

	// CodeCompatibility 表示 Pi 的完成语义无法识别（协议不匹配）。
	// 与 PROVIDER_ERROR 分开：provider 报错是模型侧失败，这里是宿主与 Pi 的
	// 协议契约对不上——前者重试可能好，后者只有换版本/修适配才能好。
	CodeCompatibility = "COMPATIBILITY"

	// CodeInvalidInput 表示入参本身不合法（不在允许集合里、格式不对、超范围）。
	//
	// 与 UNSUPPORTED 分开：UNSUPPORTED 是"这个功能/协议我们没做"，属于能力缺口，
	// 用户换什么都不行；INVALID_INPUT 是"你给的这一个值不行"，换一个就能过。
	// 两者混用会让 UI 把"填错语言代码"显示成"不支持多语言"。
	CodeInvalidInput = "INVALID_INPUT"
)

// AppError 是统一的错误结构。Code 机器可解析，Retryable 指示是否可重试。
// 任何跨边界错误都应携带此结构，前端据 Code 决定文案与动作。
//
// Params/Operation/Cause 是 U00 的增量（都可省略）：
//   - Params 给前端做本地化替换（例如 {"field":"locale","value":"fr-FR"}）。
//     前端绝不解析 Go 返回的 Message 决定文案，只按 Code + Params 取消息表。
//   - Operation 标明失败的操作（如 "preferences.save"），便于排查与埋点。
//   - Cause 是**脱敏后的摘要**，用于把"哪一步坏了"说清楚；
//     不放堆栈、不放路径里的用户名、不放任何秘密（SPEC07：日志/诊断零命中）。
type AppError struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable"`
	Params    map[string]string `json:"params,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Cause     string            `json:"cause,omitempty"`
}

// Error 实现 error 接口，便于在 Go 错误链中传递。
func (e *AppError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Code + ": " + e.Message
}

// NewAppError 构造一个 AppError。
//
// 只填三个既有字段：老调用方一行不改也能编译，且 JSON 与历史完全一致。
func NewAppError(code, message string, retryable bool) *AppError {
	return &AppError{Code: code, Message: message, Retryable: retryable}
}

// WithParams 附上本地化参数（防御性拷贝，调用方后续改自己的 map 不影响错误）。
//
// 传入 nil 或空映射等价于不设：序列化后不会出现 "params": null，
// 前端因此可以安全地区分"没有参数"与"参数为空"。
func (e *AppError) WithParams(params map[string]string) *AppError {
	if e == nil || len(params) == 0 {
		return e
	}
	cp := make(map[string]string, len(params))
	for k, v := range params {
		cp[k] = v
	}
	e.Params = cp
	return e
}

// WithOperation 标明失败的操作（如 "preferences.save"）。
func (e *AppError) WithOperation(op string) *AppError {
	if e == nil {
		return e
	}
	e.Operation = op
	return e
}

// WithCause 附上脱敏原因摘要。
//
// 只收单行摘要：多行内容通常意味着有人把 %+v 的堆栈直接塞进来，
// 那会把内部实现和路径泄漏到用户界面与诊断包里。调用方必须自己先脱敏。
func (e *AppError) WithCause(cause string) *AppError {
	if e == nil {
		return e
	}
	// 只保留第一行，去掉可能的堆栈；空串等价于不设。
	if i := strings.IndexAny(cause, "\r\n"); i >= 0 {
		cause = cause[:i]
	}
	e.Cause = strings.TrimSpace(cause)
	return e
}
