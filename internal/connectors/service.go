package connectors

// 连接器服务：注册表 + 能力范围 + 审计 + 限流 + 唯一的策略执行点。
//
// 每一次写/外发都走同一条路径：
//
//	catalog 校验 → 启用/能力/资源范围 → 参数 schema → 限流
//	→ internal/policy（allow / deny / ask）→ 取凭证（只按 ref）
//	→ 驱动派发 → 结果（含 OutcomeUnknown）→ 审计
//
// 拒绝、限流、审批失败、取不到凭证都发生在派发**之前**，因此外部副作用计数
// 必须为 0。这个顺序是 CONN02/CONN06 的实现，不是注释里的承诺。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/policy"
)

// 观察目录相关常量（外部 oracle 的约定）。
const (
	// ObservationDirEnv 是集成 runner 传来证据目录的环境变量。
	ObservationDirEnv = "ANVIL_OBSERVATION_DIR"
	// ObservationCounterFile 是 fixture 副作用计数快照的文件名。
	ObservationCounterFile = "connector-counter.json"
	// ObservationAuditFile 是审计镜像的文件名。
	ObservationAuditFile = "audit.jsonl"
)

// ObservationDir 返回观察目录（未设置时为空）。
func ObservationDir() string { return strings.TrimSpace(os.Getenv(ObservationDirEnv)) }

const (
	defaultRateRequests   = 30
	defaultRatePerSeconds = 60
	defaultApprovalWait   = 60 * time.Second
	defaultPollInterval   = 20 * time.Millisecond
)

// PolicyGate 是连接器唯一授权入口，由 *policy.Service 实现。
//
// 只暴露需要的四个方法：连接器不自己实现策略，也不缓存"上次是 allow"。
type PolicyGate interface {
	Authorize(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall) (policy.Result, error)
	Pending(ctx context.Context, id string) (policy.ApprovalRequest, error)
	ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call policy.ToolCall) error
	ResolveApproval(ctx context.Context, id string, choice policy.ApprovalChoice, call policy.ToolCall) error
}

// Options 构造连接器服务。
type Options struct {
	// Definitions 是定义存储（生产用 SQLStore）。
	Definitions DefinitionStore
	// Audit 是审计存储；为空时按观察目录退化到 JSONL，再为空则拒绝构造。
	Audit AuditStore
	// Credentials 是 OS 凭证库（或显式声明的会话内存库）。
	Credentials credentials.Store
	// Policy 是唯一授权点，不可为空。
	Policy PolicyGate
	// Drivers 是初始注册的驱动（测试与固定端点场景）。
	Drivers []Driver
	// DriverFactory 按定义构造驱动（端点来自定义本身，mail 需要）。
	// 与 Drivers 二选一或并用；同 kind 的显式注册优先。
	DriverFactory func(def Definition) (Driver, error)
	// Now 便于测试注入时钟。
	Now func() time.Time
	// ObservationDir 覆盖 ANVIL_OBSERVATION_DIR（测试用）。
	ObservationDir string
	// OnPending 把待审批请求推给 UI（不带凭证）。
	OnPending func(PendingApproval)
	// ApprovalWait 是等待人工答复的上限，默认 60 秒。
	ApprovalWait time.Duration
	// PollInterval 是等待审批的轮询间隔，默认 20ms。
	PollInterval time.Duration
}

// pendingCall 是"正在等待答复"的调用上下文，供 ResolveApproval 复原参数指纹。
type pendingCall struct {
	exec    contracts.ExecutionContext
	call    policy.ToolCall
	pending PendingApproval
}

// Service 是应用级连接器服务。并发安全。
type Service struct {
	defs  DefinitionStore
	audit AuditStore
	creds credentials.Store
	gate  PolicyGate

	now          func() time.Time
	onPending    func(PendingApproval)
	approvalWait time.Duration
	pollInterval time.Duration

	mu        sync.Mutex
	drivers   map[Kind]Driver
	factory   func(def Definition) (Driver, error)
	built     map[string]builtDriver
	rate      map[string][]time.Time
	pending   map[string]pendingCall
	seq       uint64
	lastAudit error
}

// NewService 构造服务并注册驱动。
func NewService(opts Options) (*Service, error) {
	if opts.Definitions == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "连接器服务缺少定义存储", false)
	}
	if opts.Credentials == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "连接器服务缺少凭证存储", false)
	}
	if opts.Policy == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "连接器服务缺少策略服务（写路径必须先过授权）", false)
	}
	audit := opts.Audit
	if audit == nil {
		dir := strings.TrimSpace(opts.ObservationDir)
		if dir == "" {
			dir = ObservationDir()
		}
		if dir == "" {
			return nil, contracts.NewAppError(contracts.CodeConfigMissing, "连接器服务缺少审计存储（没有审计就不允许外发）", false)
		}
		file, err := NewJSONLAudit(filepath.Join(dir, ObservationAuditFile))
		if err != nil {
			return nil, err
		}
		audit = file
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Service{
		defs:         opts.Definitions,
		audit:        audit,
		creds:        opts.Credentials,
		gate:         opts.Policy,
		now:          now,
		onPending:    opts.OnPending,
		approvalWait: opts.ApprovalWait,
		pollInterval: opts.PollInterval,
		drivers:      map[Kind]Driver{},
		factory:      opts.DriverFactory,
		built:        map[string]builtDriver{},
		rate:         map[string][]time.Time{},
		pending:      map[string]pendingCall{},
	}
	if s.approvalWait <= 0 {
		s.approvalWait = defaultApprovalWait
	}
	if s.pollInterval <= 0 {
		s.pollInterval = defaultPollInterval
	}
	for _, d := range opts.Drivers {
		if err := s.Register(d); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// SetPendingSink 在构造之后设置审批推送回调（绑定层需要先拿到服务引用时用）。
func (s *Service) SetPendingSink(fn func(PendingApproval)) {
	s.mu.Lock()
	s.onPending = fn
	s.mu.Unlock()
}

// Register 注册一个驱动。
//
// 拒绝重复 kind：两个驱动抢同一个 kind 时，"谁在发这封信"就不可回答了。
func (s *Service) Register(d Driver) error {
	if d == nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "注册空驱动", false)
	}
	kind := d.Kind()
	if !kind.Known() {
		return contracts.NewAppError(contracts.CodeUnsupported, "未实现的连接器类型: "+string(kind), false)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.drivers[kind]; exists {
		return contracts.NewAppError(contracts.CodeConfigConflict, "连接器类型重复注册: "+string(kind), false)
	}
	s.drivers[kind] = d
	return nil
}

// Driver 返回某个 kind 的显式注册驱动。
func (s *Service) Driver(kind Kind) (Driver, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.drivers[kind]
	return d, ok
}

// builtDriver 是按定义构造出来的驱动缓存。
//
// 缓存键必须是**定义 ID**，不能是 kind：同一个 kind 可以有多个定义（两个邮箱），
// 而端点属于定义。按 kind 缓存会让 B 定义的健康检查打到 A 的服务器上——
// "看起来连上了"的最坏形态。
type builtDriver struct {
	revision  uint64
	endpoints Endpoints
	driver    Driver
}

// driverFor 返回该定义要用的驱动。
//
// 端点属于**定义**（不是全局配置），所以 mail 这类驱动必须按定义构造；
// 显式注册的驱动优先，便于测试注入"只计数、不发包"的实现。
func (s *Service) driverFor(def Definition) (Driver, error) {
	if d, ok := s.Driver(def.Kind); ok {
		return d, nil
	}
	s.mu.Lock()
	factory := s.factory
	if cached, ok := s.built[def.ID]; ok && cached.revision == def.Revision && cached.endpoints == def.Endpoints {
		s.mu.Unlock()
		return cached.driver, nil
	}
	s.mu.Unlock()
	if factory == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "没有可用的连接器驱动: "+string(def.Kind), false)
	}
	driver, err := factory(def)
	if err != nil {
		return nil, err
	}
	if driver == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "连接器驱动工厂返回空驱动: "+string(def.Kind), false)
	}
	if driver.Kind() != def.Kind {
		return nil, contracts.NewAppError(contracts.CodeInternal,
			"驱动类型与定义不一致: "+string(driver.Kind())+" != "+string(def.Kind), false)
	}
	s.mu.Lock()
	s.built[def.ID] = builtDriver{revision: def.Revision, endpoints: def.Endpoints, driver: driver}
	s.mu.Unlock()
	return driver, nil
}

// LastAuditError 返回最近一次忽略型审计写入失败（诊断展示用）。
func (s *Service) LastAuditError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAudit
}

// ListDefinitions 返回全部定义。
func (s *Service) ListDefinitions(ctx context.Context) ([]Definition, error) {
	return s.defs.List(ctx)
}

// GetDefinition 读取单条定义。
func (s *Service) GetDefinition(ctx context.Context, id string) (Definition, error) {
	return s.defs.Get(ctx, id)
}

// SaveDefinition 保存定义（CAS）。凭证材料不在这里：只接受 ref。
func (s *Service) SaveDefinition(ctx context.Context, def Definition) (Definition, error) {
	return s.defs.Put(ctx, def)
}

// DeleteDefinition 删除定义（不隐式删凭证；撤销授权要显式 Disconnect）。
func (s *Service) DeleteDefinition(ctx context.Context, id string) error {
	return s.defs.Delete(ctx, id)
}

// ListStatuses 返回全部连接器状态（含真实健康检查）。
func (s *Service) ListStatuses(ctx context.Context) ([]Status, error) {
	defs, err := s.defs.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(defs))
	for _, def := range defs {
		out = append(out, s.statusOf(ctx, def))
	}
	return out, nil
}

// Status 返回单条连接器状态。
func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	def, err := s.defs.Get(ctx, id)
	if err != nil {
		return Status{}, err
	}
	return s.statusOf(ctx, def), nil
}

// Actions 返回某个连接器在 catalog 里的动作（禁用时为 nil）。
//
// 用 catalog 而不是驱动自述：工具目录与 UI 动作卡片必须与策略看到的是同一份。
func (s *Service) Actions(ctx context.Context, id string) ([]ActionDescriptor, error) {
	def, err := s.defs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return actionsFor(def), nil
}

// Connect 用用户提供的凭证完成一次授权（C08a 的 mail 走应用专用密码）。
//
// 凭证只写进 OS 凭证库，ref 已经在定义里；返回的状态来自**真实认证**的健康调用，
// 因此失败时不会显示 connected。
func (s *Service) Connect(ctx context.Context, id string, secret []byte) (Status, error) {
	def, err := s.defs.Get(ctx, id)
	if err != nil {
		return Status{}, err
	}
	if len(secret) == 0 {
		return Status{}, contracts.NewAppError(contracts.CodeUnsupported, "凭证内容为空", false)
	}
	ref := strings.TrimSpace(def.CredentialRef)
	if ref == "" {
		return Status{}, contracts.NewAppError(contracts.CodeConfigMissing, "连接器没有 credential ref，拒绝保存凭证", false)
	}
	if err := s.creds.Put(ctx, ref, secret); err != nil {
		return Status{}, err
	}
	s.appendAudit(ctx, AuditEntry{
		ConnectorID:   def.ID,
		ActionID:      "connect",
		Kind:          def.Kind,
		Decision:      "n/a",
		Outcome:       "credential_saved",
		CredentialRef: ref,
		Detail:        "凭证已写入 OS 凭证库，配置与审计只保留 ref",
	})
	return s.statusOf(ctx, def), nil
}

// Disconnect 撤销授权：删除凭证并留审计。
//
// 不删除定义（用户可能只是想换一个账号）；下一次调用会因为取不到凭证而失败。
func (s *Service) Disconnect(ctx context.Context, id string) (Status, error) {
	def, err := s.defs.Get(ctx, id)
	if err != nil {
		return Status{}, err
	}
	ref := strings.TrimSpace(def.CredentialRef)
	if ref != "" {
		if err := s.creds.Delete(ctx, ref); err != nil {
			return Status{}, err
		}
	}
	s.appendAudit(ctx, AuditEntry{
		ConnectorID:   def.ID,
		ActionID:      "disconnect",
		Kind:          def.Kind,
		Decision:      "n/a",
		Outcome:       "revoked",
		CredentialRef: ref,
		Detail:        "授权已撤销，凭证已从 OS 凭证库删除",
	})
	return s.statusOf(ctx, def), nil
}

// Health 执行一次真实健康检查（不读缓存）。
func (s *Service) Health(ctx context.Context, id string) (Health, error) {
	def, err := s.defs.Get(ctx, id)
	if err != nil {
		return Health{}, err
	}
	return s.health(ctx, def), nil
}

// ResolveApproval 答复一条连接器动作的待审批请求。
//
// 只有 Service 知道那次调用绑定的是哪个执行上下文与参数，因此答复必须经这里
// 复原后再交给策略服务——否则审批与调用之间就少了绑定校验。
func (s *Service) ResolveApproval(ctx context.Context, approvalID string, choice policy.ApprovalChoice) error {
	s.mu.Lock()
	pc, ok := s.pending[approvalID]
	s.mu.Unlock()
	if !ok {
		return contracts.NewAppError(contracts.CodeNotFound, "没有等待中的连接器审批: "+approvalID, false)
	}
	return s.gate.ResolveApproval(ctx, approvalID, choice, pc.call)
}

// Invoke 执行一次连接器动作。
//
// 返回的 error 只用于"调用请求本身不成立"（未知连接器/动作、参数非法、存储故障）；
// 策略拒绝、限流、审批失败、缺凭证都以 OutcomeFailed 的结果返回，便于 MCP/UI
// 用同一套文案展示，同时不影响"副作用计数为 0"的判定。
func (s *Service) Invoke(ctx context.Context, req InvokeRequest) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	def, err := s.defs.Get(ctx, req.ConnectorID)
	if err != nil {
		return Result{}, err
	}
	action, ok := LookupAction(def.Kind, strings.TrimSpace(req.ActionID))
	if !ok {
		s.appendAudit(ctx, AuditEntry{
			ConnectorID: def.ID, ActionID: req.ActionID, Kind: def.Kind,
			Decision: "n/a", Outcome: "unknown_action",
			Reason:    "动作不在 Anvil action catalog 内",
			SessionID: string(req.Execution.SessionID), RunID: string(req.Execution.RunID),
		})
		return Result{}, contracts.NewAppError(
			contracts.CodeUnsupported,
			fmt.Sprintf("%s 不支持动作 %q（未在 catalog 中的动作不会被驱动执行）", def.Kind, req.ActionID),
			false,
		)
	}
	toolName := ToolName(def, action)

	if !def.Enabled {
		return s.refused(ctx, def, action, req, toolName, "",
			policy.DecisionDeny, "denied", "连接器未启用", RemediationEnableConnector)
	}
	if !def.HasCapability(action.Capability) {
		return s.refused(ctx, def, action, req, toolName, "",
			policy.DecisionDeny, "denied", "能力未授予: "+string(action.Capability), RemediationFixSettings)
	}
	args, aerr := validateArguments(action, req.Arguments)
	if aerr != nil {
		s.appendAudit(ctx, AuditEntry{
			ConnectorID: def.ID, ActionID: action.ID, Kind: def.Kind,
			Decision: "n/a", Outcome: "invalid_arguments", Reason: aerr.Error(),
			CredentialRef: def.CredentialRef, ToolName: toolName,
			SessionID: string(req.Execution.SessionID), RunID: string(req.Execution.RunID),
		})
		return Result{}, aerr
	}
	if err := checkAllowedResources(def, action, args); err != nil {
		return s.refused(ctx, def, action, req, toolName, "",
			policy.DecisionDeny, "denied", err.Error(), RemediationFixSettings)
	}
	if err := s.takeRate(def); err != nil {
		return s.refused(ctx, def, action, req, toolName, "",
			policy.DecisionDeny, "rate_limited", err.Error(), RemediationNone)
	}

	driver, derr := s.driverFor(def)
	if derr != nil {
		return Result{}, derr
	}
	if err := s.verifyDriverActions(ctx, driver); err != nil {
		return Result{}, err
	}

	call := policy.ToolCall{
		ID:            s.nextCallID(),
		Name:          toolName,
		Source:        policy.SourceHarnessMCP,
		Arguments:     args,
		Origin:        "connector:" + def.ID,
		ResourceScope: resourceScope(def, action, args),
	}

	decision, approvalID, derr := s.authorize(ctx, req.Execution, call)
	if derr != nil {
		return Result{}, derr
	}
	switch decision {
	case policy.DecisionDeny:
		return s.refused(ctx, def, action, req, toolName, approvalID,
			policy.DecisionDeny, "denied", "策略拒绝：本次外发未获允许", RemediationNone)
	case policy.DecisionAsk:
		approved, reason := s.awaitApproval(ctx, req.Execution, call, approvalID, PendingApproval{
			ApprovalID:  approvalID,
			ConnectorID: def.ID,
			ActionID:    action.ID,
			Risk:        action.Risk,
			ToolName:    toolName,
			Arguments:   args,
		})
		if !approved {
			return s.refused(ctx, def, action, req, toolName, approvalID,
				policy.DecisionAsk, "approval_denied", "未获批准，请求未派发: "+reason, RemediationNone)
		}
	}

	cred, cerr := s.credential(ctx, def)
	if cerr != nil {
		s.appendAudit(ctx, AuditEntry{
			ConnectorID: def.ID, ActionID: action.ID, Kind: def.Kind,
			Decision: "allow", Outcome: "blocked", Reason: cerr.Error(),
			CredentialRef: def.CredentialRef, ToolName: toolName,
			SessionID: string(req.Execution.SessionID), RunID: string(req.Execution.RunID),
		})
		return Result{
			Outcome:     OutcomeFailed,
			ConnectorID: def.ID,
			ActionID:    action.ID,
			Detail:      "未派发：缺少可用凭证（" + cerr.Error() + "）",
			CompletedAt: s.now().UTC(),
		}, nil
	}

	res := s.dispatch(ctx, driver, action, Invocation{
		Execution:      req.Execution,
		ConnectorID:    def.ID,
		ActionID:       action.ID,
		Arguments:      args,
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
		Credential:     cred,
	})
	redacted := redactSecret(&res, cred)

	entry := AuditEntry{
		ConnectorID:    def.ID,
		ActionID:       action.ID,
		Kind:           def.Kind,
		Decision:       "allow",
		Outcome:        string(res.Outcome),
		Reason:         res.Detail,
		CredentialRef:  cred.Ref,
		IdempotencyKey: res.IdempotencyKey,
		ToolName:       toolName,
		SessionID:      string(req.Execution.SessionID),
		RunID:          string(req.Execution.RunID),
	}
	if redacted {
		entry.Reason = "驱动返回内容含凭证材料，已遮蔽"
	}
	s.appendAudit(ctx, entry)
	return res, nil
}

// dispatch 调用驱动并把"结果类别"归一。
//
// 驱动的 error 只表示"确定没有派发成功"；派发之后的超时/断线必须用
// OutcomeUnknown 结果表达。这里不做任何重试——非幂等动作的自动重放是事故，
// 不是容错。
func (s *Service) dispatch(ctx context.Context, driver Driver, action ActionDescriptor, inv Invocation) Result {
	res, err := driver.Invoke(ctx, inv)
	if err != nil {
		return Result{
			Outcome:     OutcomeFailed,
			ConnectorID: inv.ConnectorID,
			ActionID:    inv.ActionID,
			Detail:      err.Error(),
			Dispatched:  false,
			// 驱动报错但没声明"已派发"：只允许调用方人工再试一次，
			// 服务自己绝不重放。
			SafeToRetry: true,
			CompletedAt: s.now().UTC(),
		}
	}
	if !res.Outcome.Valid() {
		// 认不出的结论按"未知"处理：既不能写成成功，也不能当成可安全重试的失败。
		res.Outcome = OutcomeUnknown
		res.Detail = "驱动返回了无法识别的结果类别，按未知结果处理"
	}
	res.ConnectorID = inv.ConnectorID
	res.ActionID = inv.ActionID
	if res.IdempotencyKey == "" {
		res.IdempotencyKey = inv.IdempotencyKey
	}
	if res.CompletedAt.IsZero() {
		res.CompletedAt = s.now().UTC()
	}
	res.SafeToRetry = res.Outcome == OutcomeFailed && !res.Dispatched
	return res
}

// authorize 调策略服务，把结论归一成 allow/deny/ask。
func (s *Service) authorize(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall) (policy.Decision, string, error) {
	res, err := s.gate.Authorize(ctx, exec, call)
	if err != nil {
		return "", "", err
	}
	switch res.Decision {
	case policy.DecisionAllow, policy.DecisionDeny:
		return res.Decision, res.ApprovalID, nil
	case policy.DecisionAsk:
		if strings.TrimSpace(res.ApprovalID) == "" {
			return "", "", contracts.NewAppError(contracts.CodeInternal, "策略要求审批但没有给出审批 ID，拒绝执行", false)
		}
		return policy.DecisionAsk, res.ApprovalID, nil
	}
	return "", "", contracts.NewAppError(contracts.CodeInternal, "未知策略结论: "+string(res.Decision), false)
}

// awaitApproval 等待人工答复。
//
// 判定顺序刻意固定：先看审批是否仍待处理，再取最终结论。反过来（先
// ValidateBinding）会在 once 许可上把执行权提前消费掉，第二次复查必然失败。
func (s *Service) awaitApproval(ctx context.Context, exec contracts.ExecutionContext, call policy.ToolCall, approvalID string, pa PendingApproval) (bool, string) {
	pa.ExpiresAt = s.now().UTC().Add(s.approvalWait)
	s.mu.Lock()
	s.pending[approvalID] = pendingCall{exec: exec, call: call, pending: pa}
	sink := s.onPending
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, approvalID)
		s.mu.Unlock()
	}()

	if sink != nil {
		sink(pa)
	}

	deadline := time.Now().Add(s.approvalWait)
	for {
		if _, perr := s.gate.Pending(ctx, approvalID); perr != nil {
			// 不再是待审批：取最终结论（once 在这里被原子领取一次执行权）。
			if verr := s.gate.ValidateBinding(ctx, exec, approvalID, call); verr != nil {
				return false, verr.Error()
			}
			return true, ""
		}
		if time.Now().After(deadline) {
			return false, "等待人工确认超时"
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err().Error()
		case <-time.After(s.pollInterval):
		}
	}
}

// refused 记录一条"未派发"的拒绝并返回结果。
//
// 审计写入失败时返回错误而不是静默放过：没有审计的外发等于没有外发记录。
func (s *Service) refused(ctx context.Context, def Definition, action ActionDescriptor, req InvokeRequest, toolName, approvalID string, decision policy.Decision, outcome, reason string, remediation Remediation) (Result, error) {
	entry := AuditEntry{
		ConnectorID:   def.ID,
		ActionID:      action.ID,
		Kind:          def.Kind,
		Decision:      string(decision),
		Outcome:       outcome,
		Reason:        reason,
		CredentialRef: def.CredentialRef,
		ApprovalID:    approvalID,
		ToolName:      toolName,
		SessionID:     string(req.Execution.SessionID),
		RunID:         string(req.Execution.RunID),
	}
	if err := s.recordAudit(ctx, entry); err != nil {
		return Result{}, err
	}
	detail := "未派发：" + reason
	if remediation != "" && remediation != RemediationNone {
		detail += "（修复动作: " + string(remediation) + "）"
	}
	return Result{
		Outcome:     OutcomeFailed,
		ConnectorID: def.ID,
		ActionID:    action.ID,
		Detail:      detail,
		Dispatched:  false,
		SafeToRetry: false,
		CompletedAt: s.now().UTC(),
	}, nil
}

// credential 按 ref 从 OS 凭证库取一次凭证材料。
//
// 取不到就是"未授权"，绝不回退到配置里的任何明文（配置里也没有）。
func (s *Service) credential(ctx context.Context, def Definition) (Credential, error) {
	ref := strings.TrimSpace(def.CredentialRef)
	if ref == "" {
		return Credential{}, contracts.NewAppError(contracts.CodeConfigMissing, "连接器没有 credential ref", false)
	}
	secret, err := s.creds.Get(ctx, ref)
	if err != nil {
		return Credential{}, err
	}
	return Credential{Ref: ref, Secret: secret}, nil
}

// statusOf 组装状态（health 是真实调用）。
func (s *Service) statusOf(ctx context.Context, def Definition) Status {
	return Status{
		ID:               def.ID,
		Kind:             def.Kind,
		DisplayName:      def.DisplayName,
		Enabled:          def.Enabled,
		CredentialRef:    def.CredentialRef,
		Capabilities:     append([]Capability{}, def.Capabilities...),
		AllowedResources: append([]string{}, def.AllowedResources...),
		Revision:         def.Revision,
		Endpoints:        def.Endpoints,
		Health:           s.health(ctx, def),
		Actions:          actionsFor(def),
	}
}

// actionsFor 返回该定义当前真正可用的动作（catalog ∩ 已授予能力）。
func actionsFor(def Definition) []ActionDescriptor {
	out := []ActionDescriptor{}
	if !def.Enabled {
		return out
	}
	for _, a := range Catalog(def.Kind) {
		if def.HasCapability(a.Capability) {
			out = append(out, a)
		}
	}
	return out
}

// health 执行真实健康检查。
//
// connected 只有一个来源：驱动完成一次通过认证的调用。这里不缓存结论，
// 也不把"配置齐了"当成"连上了"。
func (s *Service) health(ctx context.Context, def Definition) Health {
	now := s.now().UTC()
	blocked := func(detail string, rem Remediation) Health {
		return Health{Status: HealthBlocked, Detail: detail, Remediation: rem, CheckedAt: now}
	}
	if !def.Enabled {
		return Health{
			Status:      HealthDisconnected,
			Detail:      "连接器未启用",
			Remediation: RemediationEnableConnector,
			CheckedAt:   now,
		}
	}
	driver, derr := s.driverFor(def)
	if derr != nil {
		return blocked("没有可用驱动: "+derr.Error(), RemediationFixSettings)
	}
	ref := strings.TrimSpace(def.CredentialRef)
	if ref == "" {
		return blocked("没有 credential ref，无法取凭证", RemediationAddCredentials)
	}
	secret, err := s.creds.Get(ctx, ref)
	if err != nil {
		return blocked("凭证不可用（需要重新授权）: "+err.Error(), RemediationAddCredentials)
	}
	cred := Credential{Ref: ref, Secret: secret}
	started := time.Now()
	h, derr := driver.Diagnose(ctx, cred)
	latency := time.Since(started).Milliseconds()
	if derr != nil {
		return blocked("健康检查失败: "+derr.Error(), RemediationCheckNetwork)
	}
	if h.Status == "" {
		return Health{
			Status:      HealthError,
			Detail:      "驱动没有给出健康状态（不显示 connected）",
			Remediation: RemediationCheckNetwork,
			CheckedAt:   now,
			LatencyMS:   latency,
		}
	}
	if h.CheckedAt.IsZero() {
		h.CheckedAt = now
	}
	if h.LatencyMS == 0 {
		h.LatencyMS = latency
	}
	if h.Remediation == "" {
		h.Remediation = RemediationNone
	}
	if redactSecret(&Result{Detail: h.Detail}, cred) {
		h.Detail = "健康检查返回内容含凭证材料，已遮蔽"
	}
	return h
}

// takeRate 做本地滑动窗口限流。
//
// 在策略之前：被限流的调用不该弹审批（用户批准了也执行不了）。
func (s *Service) takeRate(def Definition) error {
	limit := def.RateLimit
	if limit.Requests == 0 && limit.PerSeconds == 0 {
		limit = RateLimit{Requests: defaultRateRequests, PerSeconds: defaultRatePerSeconds}
	}
	if limit.Requests <= 0 || limit.PerSeconds <= 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "限流参数不完整（requests/perSeconds 都要 > 0）", false)
	}
	window := time.Duration(limit.PerSeconds) * time.Second
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]time.Time, 0, len(s.rate[def.ID])+1)
	for _, at := range s.rate[def.ID] {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	if len(kept) >= limit.Requests {
		s.rate[def.ID] = kept
		return contracts.NewAppError(
			contracts.CodeRateLimited,
			fmt.Sprintf("连接器超出本地限流（%d 次/%d 秒），本次未派发", limit.Requests, limit.PerSeconds),
			false,
		)
	}
	s.rate[def.ID] = append(kept, now)
	return nil
}

// verifyDriverActions 校验驱动自述动作与 catalog 一致。
func (s *Service) verifyDriverActions(ctx context.Context, driver Driver) error {
	caps := driver.Capabilities()
	for _, a := range Catalog(driver.Kind()) {
		found := false
		for _, c := range caps {
			if c == a.Capability {
				found = true
				break
			}
		}
		if !found {
			return contracts.NewAppError(
				contracts.CodeUnsupported,
				"驱动没有声明能力: "+string(a.Capability),
				false,
			)
		}
	}
	reported, err := driver.Actions(ctx)
	if err != nil {
		return err
	}
	byID := map[string]ActionDescriptor{}
	for _, r := range reported {
		byID[r.ID] = r
	}
	for _, c := range Catalog(driver.Kind()) {
		r, ok := byID[c.ID]
		if !ok {
			return contracts.NewAppError(contracts.CodeUnsupported, "驱动缺少 catalog 动作: "+c.ID, false)
		}
		if err := verifyDriverDescriptor(c, r); err != nil {
			return err
		}
	}
	for _, r := range reported {
		if _, ok := LookupAction(driver.Kind(), r.ID); !ok {
			return contracts.NewAppError(contracts.CodeUnsupported, "驱动声明了 catalog 之外的动作: "+r.ID, false)
		}
	}
	return nil
}

// checkAllowedResources 校验外发目标是否在定义声明的资源范围内（CONN08）。
//
// 写/外发动作必须显式声明范围；空范围不是"不限制"，而是"未配置"，直接拒绝。
func checkAllowedResources(def Definition, action ActionDescriptor, args json.RawMessage) error {
	if action.Risk == RiskRead {
		return nil
	}
	patterns := def.AllowedResources
	if len(patterns) == 0 {
		return contracts.NewAppError(contracts.CodeUnauthorized, "连接器未声明允许的资源范围，拒绝外发", false)
	}
	if def.Kind == KindMail && action.ID == "send" {
		var in struct {
			To []string `json:"to"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return contracts.NewAppError(contracts.CodeUnsupported, "send 参数不是合法 JSON: "+err.Error(), false)
		}
		if len(in.To) == 0 {
			return contracts.NewAppError(contracts.CodeUnsupported, "send 至少需要一个收件人", false)
		}
		for _, addr := range in.To {
			if !matchResource(patterns, addr) {
				return contracts.NewAppError(
					contracts.CodeUnauthorized,
					"收件人不在允许的资源范围内: "+addr,
					false,
				)
			}
		}
	}
	return nil
}

// matchResource 支持三种写法：*（任意）、@domain / domain（整个域）、完整地址。
func matchResource(patterns []string, addr string) bool {
	target := strings.ToLower(strings.TrimSpace(addr))
	if target == "" {
		return false
	}
	at := strings.LastIndex(target, "@")
	for _, raw := range patterns {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.Contains(p, "@") && !strings.HasPrefix(p, "@") {
			if p == target {
				return true
			}
			continue
		}
		domain := strings.TrimPrefix(p, "@")
		if at >= 0 && target[at+1:] == domain {
			return true
		}
	}
	return false
}

// resourceScope 是策略绑定的资源范围（进审批指纹）。
func resourceScope(def Definition, action ActionDescriptor, args json.RawMessage) string {
	if def.Kind == KindMail && action.ID == "send" {
		var in struct {
			To []string `json:"to"`
		}
		_ = json.Unmarshal(args, &in)
		if len(in.To) > 0 {
			return "mail:to=" + strings.Join(in.To, ",")
		}
	}
	return "connector:" + def.ID + ":" + action.ID
}

// ToolName 返回动作对应的工具名（MCP 与审计共用同一个名字）。
func ToolName(def Definition, action ActionDescriptor) string {
	return "connector_" + string(def.Kind) + "_" + action.ID
}

// recordAudit 写入一条审计并返回错误（用于派发前：没有审计就不派发）。
//
// 审计**不能**随调用方的 ctx 一起取消：派发后的超时正是最需要留下记录的时刻。
// 取消信号只表达"调用方不要结果了"，不表达"这件事没发生过"。
func (s *Service) recordAudit(ctx context.Context, e AuditEntry) error {
	if e.At.IsZero() {
		e.At = s.now().UTC()
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.audit.Append(auditCtx, e); err != nil {
		s.mu.Lock()
		s.lastAudit = err
		s.mu.Unlock()
		return err
	}
	return nil
}

// appendAudit 是"已经发生的事实"的审计：失败只记录，不改变调用结果。
func (s *Service) appendAudit(ctx context.Context, e AuditEntry) {
	if err := s.recordAudit(ctx, e); err != nil {
		_ = err
	}
}

// nextCallID 生成调用 ID（只用于审计与审批绑定，不含身份信息）。
func (s *Service) nextCallID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return "conn-" + itoa(s.seq)
}

// redactSecret 把结果里出现的凭证材料换成占位符。
//
// 这是最后一道兜底：驱动不该把凭证写进 Detail/Data，但"不该"不是保证。
func redactSecret(res *Result, cred Credential) bool {
	if res == nil || len(cred.Secret) == 0 {
		return false
	}
	changed := false
	if bytes.Contains([]byte(res.Detail), cred.Secret) {
		res.Detail = string(bytes.ReplaceAll([]byte(res.Detail), cred.Secret, []byte("[REDACTED]")))
		changed = true
	}
	if len(res.Data) > 0 && bytes.Contains(res.Data, cred.Secret) {
		res.Data = bytes.ReplaceAll(res.Data, cred.Secret, []byte("[REDACTED]"))
		changed = true
	}
	return changed
}
