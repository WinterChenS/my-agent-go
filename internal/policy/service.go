// Package policy 是执行前授权服务（基线 §6/§7，F04）。
//
// 定位：所有工具调用在真正执行前必须经过 Authorize；Decision 只有
// allow / deny / ask 三种，ask 会生成一个绑定具体参数的一次性审批请求。
// 这里不是 OS 沙箱，也不试图做沙箱——它负责回答"这一次调用允不允许执行"。
package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// Decision 是授权结论。
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
	DecisionAsk   Decision = "ask"
)

// Source 标识工具通道。不同通道必须各自过策略，不能因为"某个通道检查过"就放行其它通道。
type Source string

const (
	SourceBuiltin     Source = "builtin"      // Pi 内置 write/edit/bash 等
	SourceHarnessMCP  Source = "harness-mcp"  // 自有 MCP
	SourceExternalMCP Source = "external-mcp" // 用户/项目 MCP
	SourceExtension   Source = "extension"    // Pi 扩展工具
	SourceCodeMode    Source = "codemode"     // codemode 嵌套工具
)

// ToolCall 是一次待授权的工具调用。
type ToolCall struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Source        Source          `json:"source"`
	Arguments     json.RawMessage `json:"arguments"`
	Origin        string          `json:"origin"`        // 来源归属：user / project:<path>
	ResourceScope string          `json:"resourceScope"` // 目标资源范围（路径/主机等）
}

// Result 是授权结论加上可展示的原因与审批句柄。
//
// 审批 ID 必须能回到前端，所以返回结构体而不是裸 Decision——
// 计划里的 (Decision, error) 表达不了"这一次 ask 对应哪条审批"。
type Result struct {
	Decision   Decision `json:"decision"`
	ApprovalID string   `json:"approvalId,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// Options 构造策略服务。
type Options struct {
	// Permissions 来自编译后的有效配置（权限只能收紧，见 config.Merge）。
	Permissions config.Permissions
	// TrustedOrigins 是已获信任的项目根路径集合；不在其中的项目来源一律不执行。
	TrustedOrigins map[string]bool
	// PathGuard 是资源范围守卫（SEC05）。为 nil 时文件类工具因无法确定资源范围
	// 而被拒绝——不允许"没配守卫就默认放行"。
	PathGuard *PathGuard
	// ApprovalTTL 审批有效期，默认 60 秒（基线 §6）。
	ApprovalTTL time.Duration
	// AutoAllow 是服务级默认档位：为 true 时，本应 ask 的调用直接 allow
	// （见 ReasonAutoAllowed）。默认 false = 逐次审批。
	//
	// per-run 档位（PinRunPermissionMode）一旦登记就覆盖这个默认值：交互 run
	// 可以在 full_access 下跑，无人值守 run 同时钉成 ask，两者不会互相污染。
	AutoAllow bool
	// Now 便于测试注入时钟，生产留空用 time.Now。
	Now func() time.Time
}

// Service 是策略服务。并发安全。
type Service struct {
	perm    config.Permissions
	trusted map[string]bool
	paths   *PathGuard
	ttl     time.Duration
	now     func() time.Time

	mu        sync.Mutex
	approvals map[string]*approval
	sessionOK map[string]map[string]time.Time // sessionID -> toolKey -> expiry（session_scope 许可）
	seq       uint64

	// 审批档位（2026-11-08 权限模式增量规格）。
	//
	// runModes 是**admission 时钉住**的 per-run 档位：run 期间改设置只影响下一条
	// run，同一条 run 的前后步骤永远适用同一档位（否则回放时无法解释）。
	// 未登记的 run 用 autoAllow（服务级默认）。
	//
	// onAutoAllow 是自动放行的留痕回调（由 run.Service 装上，发 policy.auto_allowed）。
	// 它不是"另一个决策者"：结论已经定了，回调只负责记录，且不得在持锁时调用。
	autoAllow   bool
	runModes    map[contracts.RunID]string
	onAutoAllow func(exec contracts.ExecutionContext, call ToolCall, mode string)
}

// New 创建策略服务。
func New(opts Options) *Service {
	ttl := opts.ApprovalTTL
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	trusted := opts.TrustedOrigins
	if trusted == nil {
		trusted = map[string]bool{}
	}
	return &Service{
		perm:      opts.Permissions,
		trusted:   trusted,
		paths:     opts.PathGuard,
		ttl:       ttl,
		now:       now,
		approvals: map[string]*approval{},
		sessionOK: map[string]map[string]time.Time{},
		autoAllow: opts.AutoAllow,
		runModes:  map[contracts.RunID]string{},
	}
}

// Authorize 判定一次工具调用是否可执行。
//
// 判定顺序（先硬规则、后人工审批）：
//  1. 项目来源未受信任 → deny（项目自动发现的扩展/MCP/!command 不得执行）
//  2. 超出已编译权限（shell/network/filesystem）→ deny
//  3. 其余写操作/外部资源访问 → ask，返回绑定参数与 session 的审批 ID
func (s *Service) Authorize(ctx context.Context, exec contracts.ExecutionContext, call ToolCall) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(call.Name) == "" {
		return Result{}, contracts.NewAppError(contracts.CodeUnsupported, "工具名为空", false)
	}

	// 0. 资源范围归一：许可必须绑定"实际会被读写的资源"，而不是调用方写的字符串。
	//    越界在这里就拒绝，不进入审批环节——越界不是"可以商量的事"。
	if norm, err := s.normalizeScope(call); err != nil {
		return Result{Decision: DecisionDeny, Reason: err.Error()}, nil
	} else if norm.ResourceScope != call.ResourceScope {
		call = norm
	}

	key, err := approvalKey(call)
	if err != nil {
		return Result{}, err
	}

	// 1. 项目来源信任。
	if origin := strings.TrimSpace(call.Origin); strings.HasPrefix(origin, "project:") {
		root := strings.TrimPrefix(origin, "project:")
		if !s.isTrustedProject(root) {
			return Result{Decision: DecisionDeny, Reason: "项目未获信任，禁止执行其扩展/MCP/命令"}, nil
		}
	}

	// 2. 权限硬边界。
	if reason, denied := s.violatesPermission(call); denied {
		return Result{Decision: DecisionDeny, Reason: reason}, nil
	}

	// 3. 会话级许可（session_scope）未过期则直接放行。
	if s.hasSessionGrant(string(exec.SessionID), key, call.Name) {
		return Result{Decision: DecisionAllow, Reason: "命中本会话授权"}, nil
	}

	// 4. 允许完全访问：只跳过**人工审批**这一步。
	//
	// 走到这里的调用已经过了资源范围归一、项目信任与权限硬边界——越界、未授予
	// shell、未知通道在上面就 deny 了，不会被档位放行。
	// 留痕是硬要求：跳过审批 ≠ 不留痕，每次自动放行都要发一条 policy.auto_allowed。
	if mode, auto := s.autoAllowFor(exec.RunID); auto {
		s.notifyAutoAllowed(exec, call, mode)
		return Result{Decision: DecisionAllow, Reason: ReasonAutoAllowed}, nil
	}

	// 5. 需要审批：签发一次性审批，绑定 session/参数/revision/controlGeneration。
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := "apr-" + strconv.FormatUint(s.seq, 10) + "-" + key[:8]
	s.approvals[id] = &approval{
		ID:                id,
		SessionID:         exec.SessionID,
		RunID:             exec.RunID,
		ToolCallID:        call.ID,
		ToolName:          call.Name,
		Arguments:         call.Arguments,
		ArgsHash:          key,
		ConfigRevision:    exec.ConfigRevision,
		ControlGeneration: exec.ControlGeneration,
		ExpiresAt:         s.now().Add(s.ttl),
	}
	return Result{Decision: DecisionAsk, ApprovalID: id, Reason: "需要人工确认"}, nil
}

// filesystemTools 是需要绑定具体文件资源范围的工具名。
//
// 这些工具的参数里带路径，若不归一到真实路径，审批就等于"批准了一个字符串"：
// 用户可以批准 <root>/a.txt，而实际写入经 junction 落到了根外。
var filesystemTools = map[string]bool{
	"read": true, "write": true, "edit": true, "apply_patch": true,
	"ls": true, "glob": true, "grep": true, "cat": true,
	"mkdir": true, "rm": true, "delete": true, "mv": true, "move": true, "cp": true, "copy": true,
}

// normalizeScope 把文件类工具的 ResourceScope 换成解析后的真实路径。
//
// 失败即拒绝：文件类工具没有可用守卫、没给范围、或范围越界，都不进入审批。
func (s *Service) normalizeScope(call ToolCall) (ToolCall, error) {
	if !filesystemTools[strings.ToLower(call.Name)] {
		return call, nil
	}
	if s.paths == nil {
		return call, denyPath("文件类工具缺少资源范围守卫，无法确定实际资源")
	}
	scope := strings.TrimSpace(call.ResourceScope)
	if scope == "" {
		return call, denyPath("文件类工具未声明资源范围，无法绑定许可")
	}
	dec, err := s.paths.Check(scope)
	if err != nil {
		return call, err
	}
	call.ResourceScope = dec.Real
	return call, nil
}

// isTrustedProject 判定一个项目根是否已获信任。
//
// 比较用真实路径 + 大小写折叠：链接形式的项目目录、盘符大小写差异都不应改变
// 信任结论；同时也不允许"在根内新建一个同名链接"就冒充受信任项目。
func (s *Service) isTrustedProject(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return false
	}
	real, err := resolveReal(abs)
	if err != nil {
		real = filepath.Clean(abs)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := foldKey(real)
	for k := range s.trusted {
		if foldKey(k) == key {
			return true
		}
	}
	return false
}

// TrustOrigin 记录一个受信任的项目根。
//
// 根按真实路径登记：链接形式的项目目录若只按字面登记，进入后发现"project:<字面路径>"
// 与登记的键对不上，会变成误拒；反过来按真实路径登记才能让同一项目的多种写法一致。
func (s *Service) TrustOrigin(root string) error {
	if strings.TrimSpace(root) == "" {
		return contracts.NewAppError(contracts.CodeInternal, "信任根为空", false)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "信任根无法绝对化: "+err.Error(), false)
	}
	real, err := resolveReal(abs)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "信任根无法解析: "+err.Error(), false)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.trusted == nil {
		s.trusted = map[string]bool{}
	}
	s.trusted[real] = true
	return nil
}

// violatesPermission 检查调用是否超出已编译权限。
func (s *Service) violatesPermission(call ToolCall) (string, bool) {
	switch call.Source {
	case SourceBuiltin, SourceExtension, SourceHarnessMCP, SourceExternalMCP, SourceCodeMode:
	default:
		return "未知工具通道，拒绝执行", true
	}

	name := strings.ToLower(call.Name)
	needShell := name == "bash" || name == "shell" || name == "exec" || name == "run_command"
	if needShell && !s.perm.Shell {
		return "当前有效配置未授予 shell 权限", true
	}
	// 浏览器工具不走通用"网络权限"闸门：它们是**产品内代理**能力。
	//
	// 理由（2026-11-08 实测踩到）：模型调用 browser_tabs/browser_act 时，真正的出口是
	// 本进程的 browser.Service——它只操作已经挂载的 WebView2、按 origin/navigationId
	// 校验、写动作还要走 BrowserPolicyAuthorizer（默认 ask，人工批准）。模型拿这些
	// 工具**不能**发起任意出网请求，所以用"网络权限"拦它们只会把一个产品能力整体
	// 关掉：用户看到的是"未获授权：当前有效配置未授予网络权限"，而浏览器面板明明可用。
	// 真正需要网络权限的是 http/fetch/web 这类任意出网工具，它们在下面继续受限。
	if !isBrowserTool(name) {
		needNetwork := strings.Contains(name, "http") || strings.Contains(name, "fetch") ||
			strings.Contains(name, "web")
		if needNetwork && !s.perm.Network {
			return "当前有效配置未授予网络权限", true
		}
	}
	needWrite := name == "write" || name == "edit" || name == "apply_patch"
	if needWrite {
		switch strings.ToLower(s.perm.Filesystem) {
		case "workspace", "unrestricted":
		default:
			return "当前有效配置未授予文件写入权限", true
		}
	}
	return "", false
}

// isBrowserTool 判断是否是产品内代理的浏览器工具（浏览器能力有自己的授权路径）。
func isBrowserTool(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "browser_")
}

func (s *Service) hasSessionGrant(sessionID, key, toolName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	grants, ok := s.sessionOK[sessionID]
	if !ok {
		return false
	}
	exp, ok := grants[key]
	if !ok {
		// 同一工具的会话级许可：允许同工具同参数的后续调用。
		exp, ok = grants["tool:"+toolName]
	}
	if !ok {
		return false
	}
	if s.now().After(exp) {
		delete(grants, key)
		return false
	}
	return true
}

// ResolveApproval 处理一次审批决定。
//
// 拒绝条件（都返回错误且不产生任何放行效果）：审批不存在、已撤销、已过期、
// 已答复过、参数/工具与请求不匹配。
//
// 答复 ≠ 执行：这里只记结论，真正"领取一次执行"发生在 ValidateBinding。
func (s *Service) ResolveApproval(ctx context.Context, id string, choice ApprovalChoice, call ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 与 Authorize 用同一套归一并校验：审批签发时绑定的是真实路径，
	// 这里若拿字面路径算指纹，同一文件换个拼写就会"对不上自己的审批"。
	if norm, err := s.normalizeScope(call); err != nil {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批校验失败: "+err.Error(), false)
	} else {
		call = norm
	}
	key, err := approvalKey(call)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.approvals[id]
	if !ok {
		return contracts.NewAppError(contracts.CodeNotFound, "审批不存在: "+id, false)
	}
	if a.Revoked {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批已撤销", false)
	}
	if a.Resolved {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批已答复，不能重复使用", false)
	}
	if s.now().After(a.ExpiresAt) {
		delete(s.approvals, id)
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批已过期", false)
	}
	// 绑定校验：参数、工具、会话任一变化都不认这条审批。
	if a.ArgsHash != key {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批与当前调用参数不一致（参数已变更）", false)
	}
	if a.ToolName != call.Name {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批与当前工具不一致", false)
	}

	switch choice {
	case ApprovalDeny:
		a.Resolved = true
		a.Choice = ApprovalDeny
		return nil
	case ApprovalOnce:
		a.Resolved = true
		a.Choice = ApprovalOnce
		return nil
	case ApprovalSessionScope:
		a.Resolved = true
		a.Choice = ApprovalSessionScope
		grants := s.sessionOK[string(a.SessionID)]
		if grants == nil {
			grants = map[string]time.Time{}
			s.sessionOK[string(a.SessionID)] = grants
		}
		grants[key] = s.now().Add(s.ttl)
		grants["tool:"+a.ToolName] = s.now().Add(s.ttl)
		return nil
	default:
		return contracts.NewAppError(contracts.CodeUnsupported, "未知审批选项: "+string(choice), false)
	}
}

// ValidateBinding 校验一次已授权调用仍然对得上审批或会话许可。
// 执行前必须再查一次——排队期间会话可能被接管（controlGeneration 变化），
// 配置可能已改，参数（含文件真实路径）也可能已变。
func (s *Service) ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 与 Authorize 用同一套归一：否则审批记录在真实路径上、校验却拿字面路径算指纹，
	// 结果要么误拒、要么在链接拼写不同时误判为"另一条调用"。
	if norm, err := s.normalizeScope(call); err != nil {
		return contracts.NewAppError(contracts.CodeUnauthorized, "执行前复查失败: "+err.Error(), false)
	} else {
		call = norm
	}
	key, err := approvalKey(call)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.approvals[id]
	if !ok {
		return contracts.NewAppError(contracts.CodeNotFound, "审批不存在: "+id, false)
	}
	if a.Revoked {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批已撤销，不得执行", false)
	}
	if s.now().After(a.ExpiresAt) {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批已过期", false)
	}
	if a.SessionID != exec.SessionID {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批属于其它会话，拒绝", false)
	}
	if a.ConfigRevision != exec.ConfigRevision {
		return contracts.NewAppError(contracts.CodeUnauthorized, "配置已变更，审批失效", false)
	}
	if a.ControlGeneration != exec.ControlGeneration {
		return contracts.NewAppError(contracts.CodeUnauthorized, "控制权已变更，审批失效", false)
	}
	if a.ArgsHash != key {
		return contracts.NewAppError(contracts.CodeUnauthorized, "参数已变更，审批失效", false)
	}

	// 未答复时这里是纯预检（不领取执行权），供"排队期间"复核。
	// 已答复时按结论处理：deny 直接拒；once 在此原子领取一次执行权。
	if !a.Resolved {
		return nil
	}
	switch a.Choice {
	case ApprovalDeny:
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批结论为拒绝", false)
	case ApprovalOnce:
		if a.Executed {
			return contracts.NewAppError(contracts.CodeUnauthorized, "该一次性许可已被执行，不能重复执行", false)
		}
		a.Executed = true
		return nil
	case ApprovalSessionScope:
		return nil
	default:
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批结论异常", false)
	}
}

// Guard 是"授权 + 执行"的唯一入口：只有 allow 才真正执行 effect。
// 返回 Decision 便于调用方上报；effect 为 nil 时只做判定。
func (s *Service) Guard(ctx context.Context, exec contracts.ExecutionContext, call ToolCall, effect func(context.Context) error) (Result, error) {
	res, err := s.Authorize(ctx, exec, call)
	if err != nil {
		return res, err
	}
	if res.Decision != DecisionAllow || effect == nil {
		return res, nil
	}
	return res, effect(ctx)
}

// approvalKey 是参数指纹：同参数才认同一条审批。
func approvalKey(call ToolCall) (string, error) {
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage("null")
	}
	if !json.Valid(args) {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "工具参数不是合法 JSON", false)
	}
	var buf strings.Builder
	buf.WriteString(string(call.Source))
	buf.WriteString("\x00")
	buf.WriteString(call.Name)
	buf.WriteString("\x00")
	buf.WriteString(call.ResourceScope)
	buf.WriteString("\x00")
	buf.Write(canonical(args))
	sum := sha256.Sum256([]byte(buf.String()))
	return hex.EncodeToString(sum[:]), nil
}

func canonical(raw json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}
