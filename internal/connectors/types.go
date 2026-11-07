// Package connectors 是 Anvil V1 的连接器框架（C08a）。
//
// 三条不可让步的边界：
//   - 凭证只进 OS 凭证库（internal/credentials）；配置、审计、日志、DTO 里只出现
//     credential ref。任何"把 token 顺手放进 DTO 方便前端"的写法都算越界。
//   - 每条写/外发动作在派发前必须经过 internal/policy；拒绝或派发前失败时，
//     外部副作用计数必须是 0。
//   - 请求派发之后的超时/断线是 OutcomeUnknown：既不是普通错误，也不是重试理由。
//     非幂等动作不得自动重放。
//
// 本包只定义框架与 mail 驱动所需的公共类型；具体平台适配器（dingtalk/feishu/
// slack/notion/github）不在 C08a 范围内，也不在这里放"看起来实现了"的空壳。
package connectors

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// Kind 是连接器类型。
type Kind string

const (
	// KindMail 是 IMAP/SMTP 邮箱连接器（C08a 唯一落地的适配器）。
	KindMail Kind = "mail"
)

// KnownKinds 返回本构建真正支持的连接器类型。
//
// 这张表是"实现了什么"的唯一答案：没在这里出现的 kind 一律结构化拒绝，
// 而不是悄悄注册一个空驱动。
func KnownKinds() []Kind { return []Kind{KindMail} }

// Known 判定 kind 是否已实现。
func (k Kind) Known() bool {
	for _, known := range KnownKinds() {
		if known == k {
			return true
		}
	}
	return false
}

// Capability 是连接器能力范围。
//
// 能力按"动作能干什么"划分，而不是按平台划分：同一条策略（例如"只允许读"）
// 在六个平台上必须是同一件事，否则"某个平台宽松一点"就会变成常态。
type Capability string

const (
	CapabilityMailRead Capability = "mail.read"
	CapabilityMailSend Capability = "mail.send"
)

// Risk 是动作的风险等级。
//
// 由 Anvil 的 action catalog 决定，驱动不能自行降级（CONN04）：驱动声明的风险
// 与 catalog 不一致时直接结构化失败，不做"取较小值"。
type Risk string

const (
	RiskRead         Risk = "read"
	RiskWrite        Risk = "write"
	RiskExternalSend Risk = "external-send"
)

// Rank 给出风险宽严序（未知值按最高风险）。
func (r Risk) Rank() int {
	switch r {
	case RiskRead:
		return 1
	case RiskWrite:
		return 2
	case RiskExternalSend:
		return 3
	}
	return 3
}

// Idempotency 是一条动作对"重复执行"的显式决策（brief 要求的显式幂等判定）。
//
// 注意这不是驱动的自述：catalog 说了算。非幂等动作拿到 OutcomeUnknown 之后
// 不允许任何自动重放。
type Idempotency string

const (
	// IdempotencyReadOnly 表示读动作：重复执行不产生额外副作用。
	IdempotencyReadOnly Idempotency = "read-only"
	// IdempotencyKey 表示平台支持幂等键：只有带上同一个键才允许重放。
	IdempotencyKey Idempotency = "idempotency-key"
	// IdempotencyNone 表示非幂等：未知结果不得重放。
	IdempotencyNone Idempotency = "none"
)

// ActionDescriptor 是一条可调用动作。
//
// 它同时是策略、MCP 工具声明和 UI 动作卡片的唯一来源：三处各写一份迟早会
// 出现"界面说只读、实际能发信"。
type ActionDescriptor struct {
	ID          string         `json:"id"`
	Kind        Kind           `json:"kind"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Risk        Risk           `json:"risk"`
	Capability  Capability     `json:"capability"`
	Idempotency Idempotency    `json:"idempotency"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Replayable 表示"结果未知时是否可以自动重放"。
//
// 只有平台幂等键（并且调用方真的带了键）才算可重放；读动作重放不产生副作用，
// 但它不是"写成功"的证据，所以不在自动重放语义里讨论。
func (a ActionDescriptor) Replayable(idempotencyKey string) bool {
	if a.Idempotency != IdempotencyKey {
		return false
	}
	return strings.TrimSpace(idempotencyKey) != ""
}

// HealthStatus 是连接健康状态。
//
// connected 只能来自一次真实的、通过认证的健康调用；缺凭证或缺少用户提供的
// 应用注册时是 blocked——不是 connected，也不是"未知所以乐观"。
type HealthStatus string

const (
	HealthConnected    HealthStatus = "connected"
	HealthDisconnected HealthStatus = "disconnected"
	HealthBlocked      HealthStatus = "blocked"
	HealthError        HealthStatus = "error"
)

// Remediation 是修复动作的稳定标识（前端据它决定显示哪个按钮/文案键）。
type Remediation string

const (
	RemediationNone               Remediation = "none"
	RemediationAuthorize          Remediation = "authorize"
	RemediationAddCredentials     Remediation = "add-credentials"
	RemediationFixSettings        Remediation = "fix-settings"
	RemediationReauthorize        Remediation = "reauthorize"
	RemediationEnableConnector    Remediation = "enable-connector"
	RemediationCheckNetwork       Remediation = "check-network"
	RemediationMissingAppRegister Remediation = "register-app"
)

// Health 是一次健康检查结论。
type Health struct {
	Status      HealthStatus `json:"status"`
	Detail      string       `json:"detail,omitempty"`
	Remediation Remediation  `json:"remediation"`
	CheckedAt   time.Time    `json:"checkedAt"`
	// LatencyMS 是健康调用的耗时，便于区分"凭证不对"和"网络很慢"。
	LatencyMS int64 `json:"latencyMs,omitempty"`
}

// Connected 是"可以对外宣称已连接"的唯一判据。
func (h Health) Connected() bool { return h.Status == HealthConnected }

// Credential 是驱动在**一次调用期间**持有的凭证材料。
//
// Ref 永远可以展示与记录；Secret 只用于本次请求，禁止写进 Detail、审计、事件
// 或 DTO。String/GoString 都做了遮蔽，避免 fmt 一次性把密码打进日志。
type Credential struct {
	Ref    string
	Secret []byte
}

// String 遮蔽凭证内容。
func (c Credential) String() string { return "credential(ref=" + c.Ref + ", secret=REDACTED)" }

// GoString 让 %#v 也不泄露。
func (c Credential) GoString() string { return c.String() }

// Bytes 返回凭证副本，供驱动在请求里使用。
func (c Credential) Bytes() []byte {
	out := make([]byte, len(c.Secret))
	copy(out, c.Secret)
	return out
}

// Invocation 是一次驱动调用。
//
// Execution 由服务端绑定（每 run 的 session/run/configRevision），不接受调用参数
// 自报——CONN08 的"伪造 session 到不了 adapter handler"就落在这里。
type Invocation struct {
	Execution      contracts.ExecutionContext
	ConnectorID    string
	ActionID       string
	Arguments      json.RawMessage
	IdempotencyKey string
	Credential     Credential
}

// Outcome 是一次动作的结果类别。
type Outcome string

const (
	// OutcomeDone 表示请求已派发且得到确定成功。
	OutcomeDone Outcome = "done"
	// OutcomeFailed 表示确定失败；Dispatched 说明请求是否已发出去。
	OutcomeFailed Outcome = "failed"
	// OutcomeUnknown 表示请求已派发但结果未知（超时、断线、进程中断）。
	// 它是一等结果，不是错误字符串：上层必须把它当"可能已经发生"来处理。
	OutcomeUnknown Outcome = "outcome_unknown"
)

// Valid 判定结果类别是否合法。
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeDone, OutcomeFailed, OutcomeUnknown:
		return true
	}
	return false
}

// Result 是一次动作的结果。
//
// 不含任何凭证字段：驱动若把秘密塞进 Detail，服务会遮蔽并记一条审计。
type Result struct {
	Outcome     Outcome         `json:"outcome"`
	ConnectorID string          `json:"connectorId"`
	ActionID    string          `json:"actionId"`
	Detail      string          `json:"detail,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	// IdempotencyKey 是真正带出去的那个键：外部 oracle 可以拿它对账。
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Dispatched 表示请求是否已经离开本进程。
	Dispatched bool `json:"dispatched"`
	// SafeToRetry 只在"确定没有副作用"时为 true；OutcomeUnknown 恒为 false。
	SafeToRetry bool      `json:"safeToRetry"`
	CompletedAt time.Time `json:"completedAt"`
}

// Driver 是一种连接器的最小接口。
//
// 边界：
//   - Diagnose 必须是**通过认证**的真实健康调用（连上并登录），不能只看配置；
//   - Actions 返回的动作必须与 action catalog 一致，风险不得更低；
//   - Invoke 不负责授权：授权由 Service 在派发前完成，驱动拿到的凭证已经是
//     "这一次允许使用的"。
type Driver interface {
	Kind() Kind
	Capabilities() []Capability
	Actions(ctx context.Context) ([]ActionDescriptor, error)
	Diagnose(ctx context.Context, cred Credential) (Health, error)
	Invoke(ctx context.Context, inv Invocation) (Result, error)
}

// ConnectorDriver 是 Driver 的别名（任务书用这个名字称呼框架接口）。
type ConnectorDriver = Driver

// RateLimit 是每连接器的本地限流配置。
//
// 0 表示不限流；生产默认值由服务补上，避免"忘了配就等于无限调用"。
type RateLimit struct {
	Requests   int `json:"requests"`
	PerSeconds int `json:"perSeconds"`
}

// Endpoints 是连接器需要的**非敏感**连接参数。
//
// 这里永远没有密码/令牌：密码进凭证库，配置里只留 CredentialRef。
type Endpoints struct {
	// IMAPAddr / SMTPAddr 是 mail 的服务器地址（host:port）。
	IMAPAddr string `json:"imapAddr,omitempty"`
	SMTPAddr string `json:"smtpAddr,omitempty"`
	// Username 是登录名（同时作为 IMAP/SMTP 的认证用户）。
	Username string `json:"username,omitempty"`
	// From 是发件人地址。
	From string `json:"from,omitempty"`
	// Secure 表示使用隐式 TLS。false 只允许用于回环 fixture：
	// 不允许"TLS 失败就退回明文"这种静默降级。
	Secure bool `json:"secure,omitempty"`
}

// Definition 是一条连接器定义（可持久化、可版本化）。
type Definition struct {
	ID            string       `json:"id"`
	Kind          Kind         `json:"kind"`
	DisplayName   string       `json:"displayName"`
	CredentialRef string       `json:"credentialRef"`
	Enabled       bool         `json:"enabled"`
	Capabilities  []Capability `json:"capabilities"`
	// AllowedResources 是这条连接器允许触达的资源范围（收件人/频道/仓库等）。
	AllowedResources []string  `json:"allowedResources"`
	RateLimit        RateLimit `json:"rateLimit"`
	Endpoints        Endpoints `json:"endpoints"`
	// Revision 是 CAS 版本：保存时提供旧版本，版本不符直接失败，不静默覆盖。
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Clone 返回深拷贝，避免调用方改到服务内部持有的定义。
func (d Definition) Clone() Definition {
	out := d
	out.Capabilities = append([]Capability{}, d.Capabilities...)
	out.AllowedResources = append([]string{}, d.AllowedResources...)
	return out
}

// HasCapability 判定定义是否授予某能力。
func (d Definition) HasCapability(c Capability) bool {
	for _, got := range d.Capabilities {
		if got == c {
			return true
		}
	}
	return false
}

// Validate 校验定义的结构（不检查凭证是否存在）。
func (d Definition) Validate() error {
	if strings.TrimSpace(d.ID) == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "连接器 ID 为空", false)
	}
	if !d.Kind.Known() {
		return contracts.NewAppError(contracts.CodeUnsupported, "不支持的连接器类型: "+string(d.Kind), false)
	}
	if strings.TrimSpace(d.CredentialRef) == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "连接器必须声明 credential ref（凭证只能进 OS 凭证库）", false)
	}
	for _, c := range d.Capabilities {
		if !KnownCapability(d.Kind, c) {
			return contracts.NewAppError(contracts.CodeUnsupported, "未知能力范围: "+string(c), false)
		}
	}
	if d.RateLimit.Requests < 0 || d.RateLimit.PerSeconds < 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "限流参数不能为负", false)
	}
	return nil
}

// Status 是给 UI/MCP 的连接器状态（不含任何凭证材料）。
type Status struct {
	ID               string       `json:"id"`
	Kind             Kind         `json:"kind"`
	DisplayName      string       `json:"displayName"`
	Enabled          bool         `json:"enabled"`
	CredentialRef    string       `json:"credentialRef"`
	Capabilities     []Capability `json:"capabilities"`
	AllowedResources []string     `json:"allowedResources"`
	Revision         uint64       `json:"revision"`
	// Endpoints 是非敏感连接参数（没有密码/令牌）：编辑页需要显示它们。
	Endpoints Endpoints          `json:"endpoints"`
	Health    Health             `json:"health"`
	Actions   []ActionDescriptor `json:"actions"`
}

// InvokeRequest 是一次调用请求。
//
// Execution 由服务端绑定（MCP handler 的构造参数），不从工具参数读取。
type InvokeRequest struct {
	Execution      contracts.ExecutionContext
	ConnectorID    string
	ActionID       string
	Arguments      json.RawMessage
	IdempotencyKey string
}

// PendingApproval 是"需要人工确认"的推送载荷。
//
// 不含凭证；Arguments 是调用方给的参数（已经过 schema 校验与身份字段拒绝）。
type PendingApproval struct {
	ApprovalID  string          `json:"approvalId"`
	ConnectorID string          `json:"connectorId"`
	ActionID    string          `json:"actionId"`
	Risk        Risk            `json:"risk"`
	ToolName    string          `json:"toolName"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	ExpiresAt   time.Time       `json:"expiresAt"`
}
