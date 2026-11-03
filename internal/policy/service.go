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

	// 4. 需要审批：签发一次性审批，绑定 session/参数/revision/controlGeneration。
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
	needNetwork := strings.Contains(name, "http") || strings.Contains(name, "fetch") ||
		strings.Contains(name, "browser") || strings.Contains(name, "web")
	if needNetwork && !s.perm.Network {
		return "当前有效配置未授予网络权限", true
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
