package contracts

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
)

// AppError 是统一的错误结构。Code 机器可解析，Retryable 指示是否可重试。
// 任何跨边界错误都应携带此结构，前端据 Code 决定文案与动作。
type AppError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Error 实现 error 接口，便于在 Go 错误链中传递。
func (e *AppError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Code + ": " + e.Message
}

// NewAppError 构造一个 AppError。
func NewAppError(code, message string, retryable bool) *AppError {
	return &AppError{Code: code, Message: message, Retryable: retryable}
}
