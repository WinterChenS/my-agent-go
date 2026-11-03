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
