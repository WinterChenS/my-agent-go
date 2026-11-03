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
	// ApprovalTTL 审批有效期，默认 60 秒（基线 §6）。
	ApprovalTTL time.Duration
	// Now 便于测试注入时钟，生产留空用 time.Now。
	Now func() time.Time
}

// Service 是策略服务。并发安全。
type Service struct {
	perm    config.Permissions
	trusted map[string]bool
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
	key, err := approvalKey(call)
	if err != nil {
		return Result{}, err
	}

	// 1. 项目来源信任。
	if origin := strings.TrimSpace(call.Origin); strings.HasPrefix(origin, "project:") {
		root := strings.TrimPrefix(origin, "project:")
		if !s.trusted[root] {
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
// 拒绝条件（都返回错误且不产生任何放行效果）：审批不存在、已过期、
// 已被消费、结论与请求不匹配。
func (s *Service) ResolveApproval(ctx context.Context, id string, choice ApprovalChoice, call ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
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
	if a.Consumed {
		return contracts.NewAppError(contracts.CodeUnauthorized, "审批已被消费，不能重复使用", false)
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
		a.Consumed = true
		a.Choice = ApprovalDeny
		return nil
	case ApprovalOnce:
		a.Consumed = true
		a.Choice = ApprovalOnce
		return nil
	case ApprovalSessionScope:
		a.Consumed = true
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
// 执行前必须再查一次——排队期间会话可能被接管（controlGeneration 变化）。
func (s *Service) ValidateBinding(ctx context.Context, exec contracts.ExecutionContext, id string, call ToolCall) error {
	if err := ctx.Err(); err != nil {
		return err
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
	return nil
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
