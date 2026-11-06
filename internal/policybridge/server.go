// Package policybridge 把内部 policy.Service 暴露成一个只在本机回环可达的授权端点（F04）。
//
// 存在意义：Pi 的内置工具（write/edit/bash）与它自己的扩展、MCP 工具，执行前都不会
// 经过本进程的 Go 代码。要在"执行前"卡住它们，唯一位置是 Pi 的 tool_call 钩子
// （pi-extensions/harness-policy.ts）。该扩展必须问一个权威来源，这个来源就是本端点。
//
// 因此本端点按"对外服务"的标准做防护，而不是当成进程内调用：
//   - 只监听 127.0.0.1 的随机端口，不监听 0.0.0.0；
//   - 每 worker 一枚随机令牌，常量时间比较，不进前端 DTO；
//   - 绑定 session/run/configRevision/controlGeneration，换会话即失效；
//   - 校验 Host（防 DNS rebinding）与 Origin（防网页借用户浏览器）；
//   - 请求体限长、未知字段拒绝、方法限定；
//   - 任何失败都 fail-closed：扩展拿到非 2xx 或 deny 就不执行工具。
//
// 明确不做：这不是 OS 沙箱。它只能保证"经过 Pi 的工具调用"被检查过；
// Pi 进程本身仍以当前用户权限运行（基线 §6 也明确不宣称 shell 级隔离）。
package policybridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// 默认上限。
const (
	defaultAskTimeout   = 60 * time.Second
	defaultMaxBodyBytes = 256 << 10
)

// Binding 是本端点代表的执行身份。
//
// 它来自 RunService 的当前执行上下文；换 session 或发生接管（controlGeneration 变化）后，
// 旧端点签发的结论都不能再用——所以绑定值随端点一起固定，不逐请求接受调用方自报。
type Binding struct {
	SessionID         contracts.SessionID `json:"sessionId"`
	RunID             contracts.RunID     `json:"runId"`
	ConfigRevision    uint64              `json:"configRevision"`
	ControlGeneration uint64              `json:"controlGeneration"`
}

// Options 构造端点。
type Options struct {
	// Service 是权威策略服务，不可为空。
	Service *policy.Service
	// Binding 是本次 worker 的执行身份。
	Binding Binding
	// AskTimeout 是等待人工答复的上限，默认 60 秒（与审批 TTL 对齐）。
	AskTimeout time.Duration
	// MaxBodyBytes 是请求体上限，默认 256 KiB。
	MaxBodyBytes int64
	// OnPending 把待审批请求推给 UI。回调里拿到的结构不含令牌，
	// 可以直接进入前端 DTO 或事件流。
	OnPending func(policy.ApprovalRequest)
	// Now 便于测试注入时钟（只影响等待时长判定），生产留空。
	Now func() time.Time
	// Gate 是 Service.Authorize 之前的一道闸门（RUN06 预算、停机拒绝等）。
	//
	// 非 nil 错误 => 直接 deny：HTTP 200、decision="deny"、reason=err.Error()，
	// 不创建审批、不调策略服务、不产生任何副作用。放在 Authorize 之前是刻意的：
	// 若排在之后，被拦下的调用仍会签一条审批并弹给用户，而它永远执行不了。
	Gate func(ctx context.Context, call policy.ToolCall) error
}

// Endpoint 是交给上层的端点描述。
//
// 只带 URL 与令牌引用（TokenRef），不含令牌本体：令牌通过 Env 注入 Pi 子进程，
// 不进入任何 DTO、日志或前端可见结构。
type Endpoint struct {
	URL      string `json:"url"`
	TokenRef string `json:"tokenRef"`
}

// Server 是端点实例。
type Server struct {
	svc        *policy.Service
	binding    Binding
	askTimeout time.Duration
	maxBody    int64
	onPending  func(policy.ApprovalRequest)
	gate       func(ctx context.Context, call policy.ToolCall) error
	now        func() time.Time

	ln   net.Listener
	http *http.Server
	port int
	url  string

	token    string
	tokenRef string

	mu      sync.Mutex
	waiters map[string]chan policy.ApprovalChoice
	calls   map[string]policy.ToolCall // 待审批调用的原始形参（答复时用它验指纹）
	closed  bool
	closeCh chan struct{}
}

// Start 在回环随机端口上启动端点。
func Start(ctx context.Context, opts Options) (*Server, error) {
	if opts.Service == nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "策略端点缺少策略服务", false)
	}
	if strings.TrimSpace(string(opts.Binding.SessionID)) == "" {
		return nil, contracts.NewAppError(contracts.CodeInternal, "策略端点缺少会话绑定", false)
	}
	token, err := newToken()
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, err.Error(), false)
	}

	// 端口交给 OS 分配：固定端口会与残留进程/其它实例冲突，
	// 而"能 bind 成功但请求被旧进程接走"这种故障极难排查。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "策略端点监听失败: "+err.Error(), false)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	s := &Server{
		svc:        opts.Service,
		binding:    opts.Binding,
		askTimeout: opts.AskTimeout,
		maxBody:    opts.MaxBodyBytes,
		onPending:  opts.OnPending,
		gate:       opts.Gate,
		now:        opts.Now,
		ln:         ln,
		port:       port,
		url:        fmt.Sprintf("http://127.0.0.1:%d", port),
		token:      token,
		// 引用值不含令牌本体，只用于让持有方知道"去哪个密钥槽取"。
		tokenRef: fmt.Sprintf("policybridge:%d", port),
		waiters:  map[string]chan policy.ApprovalChoice{},
		calls:    map[string]policy.ToolCall{},
		closeCh:  make(chan struct{}),
	}
	if s.askTimeout <= 0 {
		s.askTimeout = defaultAskTimeout
	}
	if s.maxBody <= 0 {
		s.maxBody = defaultMaxBodyBytes
	}
	if s.now == nil {
		s.now = time.Now
	}

	mux := http.NewServeMux()
	mux.Handle("POST /v1/authorize", s.guard(s.handleAuthorize))
	mux.Handle("POST /v1/approvals/{id}/resolve", s.guard(s.handleResolve))
	mux.Handle("POST /v1/approvals/{id}/revoke", s.guard(s.handleRevoke))
	mux.Handle("GET /healthz", s.guard(s.handleHealth))

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// 写超时必须长于等待人工答复的时间，否则正常审批会被写超时打断。
		WriteTimeout: s.askTimeout + 10*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Serve 返回只可能是监听层故障；关闭路径已由 Close 处理。
			_ = err
		}
	}()
	go func() {
		// 上层 context 取消即关闭，避免端点比会话活得更久。
		select {
		case <-ctx.Done():
			_ = s.Close(context.Background())
		case <-s.closeCh:
		}
	}()
	return s, nil
}

// Endpoint 返回端点描述（不含令牌）。
func (s *Server) Endpoint() Endpoint { return Endpoint{URL: s.url, TokenRef: s.tokenRef} }

// Env 返回注入 Pi 子进程的环境变量。
//
// 令牌只能这样交付：写在命令行会被进程列表看到，写进配置文件会落盘。
// 这也是本包唯一对外暴露令牌的地方。
func (s *Server) Env() []string {
	return []string{
		"MYAGENT_POLICY_URL=" + s.url,
		"MYAGENT_POLICY_TOKEN=" + s.token,
		"MYAGENT_POLICY_SESSION=" + string(s.binding.SessionID),
	}
}

// Port 返回实际监听的端口，便于测试与诊断断言"确实是随机端口"。
func (s *Server) Port() int { return s.port }

// Close 关闭端点，并让所有仍在等待的调用 fail-closed。
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pending := make([]string, 0, len(s.waiters))
	for id := range s.waiters {
		pending = append(pending, id)
	}
	s.mu.Unlock()

	close(s.closeCh)

	// 未决审批一律撤销：服务没了就不该留下"以后还能批"的许可。
	for _, id := range pending {
		_ = s.svc.RevokeApproval(context.Background(), id)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.http.Shutdown(shutdownCtx)
}

// guard 是认证与来源校验的统一入口，顺序固定：Host → Origin → 令牌 → 会话。
//
// 任何一步失败都在触达策略服务与工具之前返回，因此不会产生副作用。
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, s.port) {
			writeError(w, http.StatusForbidden, "FORBIDDEN_HOST", "Host 不是本机回环端点")
			return
		}
		if !originAllowed(r.Header.Get("Origin"), s.port) {
			writeError(w, http.StatusForbidden, "FORBIDDEN_ORIGIN", "请求来自其它来源")
			return
		}
		if !tokenMatches(bearerToken(r), s.token) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "令牌无效")
			return
		}
		if got := r.Header.Get("X-MyAgent-Session"); got != string(s.binding.SessionID) {
			writeError(w, http.StatusForbidden, "FORBIDDEN_SESSION", "会话不匹配")
			return
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			writeError(w, http.StatusServiceUnavailable, "CLOSED", "策略端点已关闭")
			return
		}
		next(w, r)
	}
}

// authorizeRequest 是扩展发来的授权请求。
//
// 字段全部显式，且解码时拒绝未知字段：扩展被篡改后塞进来的"额外授权线索"不会被静默忽略。
type authorizeRequest struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Source        string          `json:"source"`
	Origin        string          `json:"origin"`
	Arguments     json.RawMessage `json:"arguments"`
	ResourceScope string          `json:"resourceScope"`
}

type authorizeResponse struct {
	Decision   string `json:"decision"`
	ApprovalID string `json:"approvalId,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req authorizeRequest
	if !decodeStrict(w, r, s.maxBody, &req) {
		return
	}
	if strings.TrimSpace(req.ToolName) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "toolName 不能为空")
		return
	}
	if len(req.Arguments) > 0 && !json.Valid(req.Arguments) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "arguments 不是合法 JSON")
		return
	}
	source, ok := parseSource(req.Source)
	if !ok {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "未知的工具通道: "+req.Source)
		return
	}

	call := policy.ToolCall{
		ID:            req.ToolCallID,
		Name:          req.ToolName,
		Source:        source,
		Arguments:     req.Arguments,
		Origin:        req.Origin,
		ResourceScope: req.ResourceScope,
	}
	// 闸门排在策略服务之前：非 nil 错误直接 deny，不签发审批、不产生副作用。
	// 顺序不能反——排在 Authorize 之后的话，被预算拦下的调用仍会弹一个
	// 永远执行不了的确认框，用户只能靠猜。
	if s.gate != nil {
		if gerr := s.gate(r.Context(), call); gerr != nil {
			writeJSON(w, http.StatusOK, authorizeResponse{
				Decision: string(policy.DecisionDeny),
				Reason:   gerr.Error(),
			})
			return
		}
	}

	exec := contracts.ExecutionContext{
		SessionID:         s.binding.SessionID,
		RunID:             s.binding.RunID,
		ConfigRevision:    s.binding.ConfigRevision,
		ControlGeneration: s.binding.ControlGeneration,
	}

	res, err := s.svc.Authorize(r.Context(), exec, call)
	if err != nil {
		writeError(w, http.StatusBadRequest, "POLICY_ERROR", err.Error())
		return
	}
	switch res.Decision {
	case policy.DecisionAllow:
		writeJSON(w, http.StatusOK, authorizeResponse{Decision: string(res.Decision), Reason: res.Reason})
		return
	case policy.DecisionDeny:
		writeJSON(w, http.StatusOK, authorizeResponse{Decision: string(res.Decision), Reason: res.Reason})
		return
	}

	// 需要人工确认：先登记等待者，再把请求推给 UI。
	// 顺序不能反——先推 UI 的话，答复可能赶在等待者登记之前到达。
	s.registerWaiter(res.ApprovalID, call)
	if s.onPending != nil {
		if p, perr := s.svc.Pending(r.Context(), res.ApprovalID); perr == nil {
			s.onPending(p)
		}
	}

	choice, werr := s.wait(r.Context(), res.ApprovalID)
	if werr != nil {
		writeJSON(w, http.StatusOK, authorizeResponse{
			Decision: string(policy.DecisionDeny),
			Reason:   werr.Error(),
		})
		return
	}
	if choice == policy.ApprovalDeny {
		s.forget(res.ApprovalID)
		writeJSON(w, http.StatusOK, authorizeResponse{
			Decision: string(policy.DecisionDeny),
			Reason:   "用户拒绝执行",
		})
		return
	}

	// 结论已给出，但"批准"不等于"可以随便执行"：这里做执行前复查并领取一次性执行权。
	// 排队期间可能已被接管、配置可能已改、参数可能已换。
	if verr := s.svc.ValidateBinding(r.Context(), exec, res.ApprovalID, call); verr != nil {
		s.forget(res.ApprovalID)
		writeJSON(w, http.StatusOK, authorizeResponse{
			Decision: string(policy.DecisionDeny),
			Reason:   "执行前复查未通过: " + verr.Error(),
		})
		return
	}
	s.forget(res.ApprovalID)
	writeJSON(w, http.StatusOK, authorizeResponse{
		Decision:   string(policy.DecisionAllow),
		ApprovalID: res.ApprovalID,
	})
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Choice string `json:"choice"`
	}
	if !decodeStrict(w, r, s.maxBody, &body) {
		return
	}
	choice := policy.ApprovalChoice(body.Choice)
	switch choice {
	case policy.ApprovalOnce, policy.ApprovalSessionScope, policy.ApprovalDeny:
	default:
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "未知审批选项: "+body.Choice)
		return
	}

	s.mu.Lock()
	call, ok := s.calls[id]
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "没有待审批的调用: "+id)
		return
	}
	// 用登记时的原始形参校验指纹：答复方不能借同一条审批调换参数。
	if err := s.svc.ResolveApproval(r.Context(), id, choice, call); err != nil {
		writeError(w, http.StatusConflict, codeOf(err), err.Error())
		return
	}
	s.deliver(id, choice)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.RevokeApproval(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, codeOf(err), err.Error())
		return
	}
	s.deliver(id, policy.ApprovalDeny)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": s.binding.SessionID,
	})
}

func (s *Server) registerWaiter(id string, call policy.ToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[id] = call
	if _, ok := s.waiters[id]; !ok {
		// 带缓冲：答复方可能先于等待者取出而写入。
		s.waiters[id] = make(chan policy.ApprovalChoice, 1)
	}
}

func (s *Server) waiter(id string) chan policy.ApprovalChoice {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.waiters[id]; ok {
		return ch
	}
	ch := make(chan policy.ApprovalChoice, 1)
	s.waiters[id] = ch
	return ch
}

func (s *Server) deliver(id string, c policy.ApprovalChoice) {
	ch := s.waiter(id)
	select {
	case ch <- c:
	default:
		// 已有结论在队列里，不覆盖。
	}
}

func (s *Server) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waiters, id)
	delete(s.calls, id)
}

var errAskTimeout = errors.New("等待人工确认超时，已拒绝执行")

// wait 等待人工答复。超时或调用方断开都必须撤销审批并返回拒绝——
// 不执行是唯一安全的默认。
func (s *Server) wait(ctx context.Context, id string) (policy.ApprovalChoice, error) {
	ch := s.waiter(id)
	timer := time.NewTimer(s.askTimeout)
	defer timer.Stop()
	select {
	case c := <-ch:
		return c, nil
	case <-timer.C:
		_ = s.svc.RevokeApproval(context.Background(), id)
		return policy.ApprovalDeny, errAskTimeout
	case <-ctx.Done():
		// 扩展断开（Pi 退出/请求取消）：不能留一条"等会儿还能批"的审批。
		_ = s.svc.RevokeApproval(context.Background(), id)
		return policy.ApprovalDeny, errors.New("请求已取消")
	case <-s.closeCh:
		return policy.ApprovalDeny, errors.New("策略端点已关闭")
	}
}

func parseSource(v string) (policy.Source, bool) {
	switch policy.Source(strings.TrimSpace(v)) {
	case policy.SourceBuiltin, policy.SourceHarnessMCP, policy.SourceExternalMCP,
		policy.SourceExtension, policy.SourceCodeMode:
		return policy.Source(strings.TrimSpace(v)), true
	}
	return "", false
}

// decodeStrict 限长 + 拒绝未知字段地解码请求体。
func decodeStrict(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "只接受 application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "请求体超过上限")
			return false
		}
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体不合法: "+err.Error())
		return false
	}
	// 只允许单个 JSON 值，杜绝"前面的合法、后面的被忽略"。
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体只能包含一个 JSON 对象")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func codeOf(err error) string {
	var app *contracts.AppError
	if errors.As(err, &app) {
		return app.Code
	}
	return "INTERNAL_ERROR"
}
