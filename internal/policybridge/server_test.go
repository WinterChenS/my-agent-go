package policybridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// harness 把"策略服务 + 端点 + 客户端 + 副作用计数器"装成一套可测装置。
//
// 计数器不是在测 HTTP，而是替身：只有决策为 allow 时才 +1，用来证明
// "被拒/超时/撤销的路径上副作用恒为 0"。
type harness struct {
	t       *testing.T
	svc     *policy.Service
	srv     *Server
	client  *Client
	root    string
	exec    contracts.ExecutionContext
	counter *int32
	pending chan policy.ApprovalRequest
	// 外部观察装置（SEC04.runtime/SEC06）：wire 账本由客户端 RoundTripper 写，
	// 执行账本由真正执行副作用的工具处理器写。两者都不参与断言，
	// 断言一律从磁盘读回（见 observation_test.go）。
	wire       *wireLog
	executions *execLog
	// 账本起点下标：多条测试共用一份账本时用来切出"本条测试的记录"。
	wireBaseline int
	execBaseline int
}

func newHarness(t *testing.T, opts ...func(*Options)) *harness {
	t.Helper()
	root := t.TempDir()
	guard, err := policy.NewPathGuard(root)
	if err != nil {
		t.Fatalf("构造路径守卫: %v", err)
	}
	svc := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace", Shell: false, Network: false},
		PathGuard:   guard,
		ApprovalTTL: 60 * time.Second,
	})
	exec := contracts.ExecutionContext{
		WorkspaceID:       "ws",
		SessionID:         "sess-1",
		RunID:             "run-1",
		ConfigRevision:    7,
		ControlGeneration: 1,
	}
	pending := make(chan policy.ApprovalRequest, 8)
	o := Options{
		Service:    svc,
		Binding:    Binding{SessionID: exec.SessionID, RunID: exec.RunID, ConfigRevision: exec.ConfigRevision, ControlGeneration: exec.ControlGeneration},
		AskTimeout: 2 * time.Second,
		OnPending:  func(p policy.ApprovalRequest) { pending <- p },
	}
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, o)
	if err != nil {
		t.Fatalf("启动端点: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })

	h := &harness{
		t: t, svc: svc, srv: srv, root: root, exec: exec,
		client:  NewClient(srv.Endpoint(), srv.token, exec.SessionID, 0),
		counter: new(int32),
		pending: pending,
	}
	h.initObservation(t)
	return h
}

// attempt 模拟 Pi 侧真实流程：先问策略端点，只有 allow 才真的执行工具。
//
// "执行"落在磁盘上（写目标文件）并由工具处理器自己追加一行执行账本：
// "被拒/超时/撤销/关闭的路径没有副作用"因此可以由文件证明，而不是只看计数器。
func (h *harness) attempt(ctx context.Context, call policy.ToolCall) (policy.Decision, string) {
	h.t.Helper()
	dec, reason, err := h.client.Authorize(ctx, call)
	if err != nil {
		return policy.DecisionDeny, err.Error()
	}
	if dec == policy.DecisionAllow {
		atomic.AddInt32(h.counter, 1)
		h.runTool(call)
	}
	return dec, reason
}

// runTool 是策略放行之后的真实副作用：写目标文件，并由这里自己记账。
//
// 写失败也记一行（status=failed）：让"以为执行了其实没写成"在账本里可见，
// 而不是静默变成一次看似成功的执行。
func (h *harness) runTool(call policy.ToolCall) {
	target := strings.TrimSpace(call.ResourceScope)
	argv := "write " + target
	var writeErr error
	if target != "" {
		writeErr = os.WriteFile(target, []byte("executed:"+call.Name), 0o644)
	}
	h.executions.record(call.Name, target, argv, writeErr)
}

func (h *harness) writeCall(scope string) policy.ToolCall {
	return policy.ToolCall{
		ID: "call-1", Name: "write", Source: policy.SourceBuiltin,
		Arguments:     json.RawMessage(`{"content":"x"}`),
		ResourceScope: scope,
	}
}

// TestEndpointIsLoopbackRandomPortAndTokenStaysOutOfDTO 覆盖端点暴露面的最小要求。
func TestEndpointIsLoopbackRandomPortAndTokenStaysOutOfDTO(t *testing.T) {
	h := newHarness(t)
	ep := h.srv.Endpoint()

	if !strings.HasPrefix(ep.URL, "http://127.0.0.1:") {
		t.Fatalf("端点必须只绑定回环，实际 %q", ep.URL)
	}
	if !strings.Contains(ep.URL, fmt.Sprint(h.srv.Port())) || h.srv.Port() == 0 {
		t.Fatalf("端点 URL 端口与监听端口不一致: %q / %d", ep.URL, h.srv.Port())
	}
	if strings.Contains(ep.TokenRef, h.srv.token) {
		t.Fatal("Endpoint 不得包含令牌本体")
	}
	if len(h.srv.token) != 64 {
		t.Fatalf("令牌应为 32 字节十六进制，实际长度 %d", len(h.srv.token))
	}
	if h2 := newHarness(t); h2.srv.token == h.srv.token {
		t.Fatal("不同端点实例的令牌必须不同")
	}

	// 环境变量是唯一交付令牌的通道，必须能直接注入 Pi 子进程。
	env := strings.Join(h.srv.Env(), ";")
	if !strings.Contains(env, "MYAGENT_POLICY_TOKEN="+h.srv.token) {
		t.Fatalf("Env 未携带令牌: %s", env)
	}
}

// TestRejectsUnauthenticatedAndForeignRequests 覆盖 SEC04：
// 错令牌 / 无令牌 / 错会话 / 伪造 Host / 网页 Origin / 非法请求体，全部到不了工具。
func TestRejectsUnauthenticatedAndForeignRequests(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	scope := filepath.Join(h.root, "f.txt")

	type reqCase struct {
		name    string
		mutate  func(*http.Request)
		body    string
		wantMin int
	}
	cases := []reqCase{
		{
			name:    "无令牌",
			mutate:  func(r *http.Request) { r.Header.Del("Authorization") },
			wantMin: http.StatusUnauthorized,
		},
		{
			name:    "错令牌",
			mutate:  func(r *http.Request) { r.Header.Set("Authorization", "Bearer deadbeef") },
			wantMin: http.StatusUnauthorized,
		},
		{
			name:    "令牌长度不同（需常量时间比较不收短路影响）",
			mutate:  func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+h.srv.token[:63]) },
			wantMin: http.StatusUnauthorized,
		},
		{
			name:    "令牌放在 query 里也不行",
			mutate:  func(r *http.Request) { r.Header.Del("Authorization"); r.URL.RawQuery = "token=" + h.srv.token },
			wantMin: http.StatusUnauthorized,
		},
		{
			name:    "伪造 Host（DNS rebinding）",
			mutate:  func(r *http.Request) { r.Host = "evil.example" },
			wantMin: http.StatusForbidden,
		},
		{
			name:    "网页 Origin",
			mutate:  func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
			wantMin: http.StatusForbidden,
		},
		{
			name:    "其它会话",
			mutate:  func(r *http.Request) { r.Header.Set("X-MyAgent-Session", "sess-2") },
			wantMin: http.StatusForbidden,
		},
		{
			name:    "未知字段",
			mutate:  func(r *http.Request) {},
			body:    `{"toolName":"write","source":"builtin","resourceScope":"` + jsonEscape(scope) + `","arguments":{},"autoApprove":true}`,
			wantMin: http.StatusBadRequest,
		},
		{
			name:    "未知工具通道",
			mutate:  func(r *http.Request) {},
			body:    `{"toolName":"write","source":"magic","arguments":{},"resourceScope":"` + jsonEscape(scope) + `"}`,
			wantMin: http.StatusBadRequest,
		},
		{
			name:    "超大请求体",
			mutate:  func(r *http.Request) {},
			body:    `{"toolName":"` + strings.Repeat("A", int(defaultMaxBodyBytes)+1024) + `"}`,
			wantMin: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				body = `{"toolCallId":"c1","toolName":"write","source":"builtin","arguments":{"content":"x"},"resourceScope":"` + jsonEscape(scope) + `"}`
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.srv.Endpoint().URL+"/v1/authorize", strings.NewReader(body))
			if err != nil {
				t.Fatalf("构造请求: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+h.srv.token)
			req.Header.Set("X-MyAgent-Session", string(h.exec.SessionID))
			if tc.mutate != nil {
				tc.mutate(req)
			}
			// 裸 client：测试要求"这些请求根本进不到策略层"，所以不能借用已封装的 Client。
			// 它仍然装上 wire 记录器：被拒的字节本身就是要观察的东西。
			resp, err := h.rawClient(5 * time.Second).Do(req)
			if err != nil {
				t.Fatalf("发送请求: %v", err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode < tc.wantMin {
				t.Fatalf("应被拒（>=%d），实际 %d", tc.wantMin, resp.StatusCode)
			}
			if got := atomic.LoadInt32(h.counter); got != 0 {
				t.Fatalf("被拒路径不得产生副作用，实际 %d", got)
			}
		})
	}

	// ── 外部观察：从 wire 账本读回"真的发出去并被拒"的请求 ──
	// 断言不再只看进程内的状态码变量：账本记的是实际发出的字节与服务端回的结论。
	own := h.ownWire(t)
	rejectedOnWire := 0
	for _, rec := range own {
		if rec.Status >= 400 || rec.Error != "" {
			rejectedOnWire++
		}
	}
	if rejectedOnWire != len(cases) {
		t.Fatalf("wire 账本里应有 %d 条被拒请求，实际 %d 条", len(cases), rejectedOnWire)
	}
	for _, rec := range own {
		if rec.Status == http.StatusOK {
			t.Fatalf("负例请求不得被接受，账本里有 200：%s %s", rec.Method, rec.Path)
		}
	}
	if got := executedCount(h.ownExecutions(t)); got != 0 {
		t.Fatalf("被拒路径不得产生执行，执行账本里有 %d 条", got)
	}
	h.observe(t, map[string]any{"deniedCases": len(cases), "rejectedOnWire": rejectedOnWire})
}

// TestApprovedOnceSideEffectIsExactlyOne 覆盖 SEC02/SEC03 的核心：
// 批准一次只执行一次；同一条许可不能被重放；不同文件不能复用。
func TestApprovedOnceSideEffectIsExactlyOne(t *testing.T) {
	h := newHarness(t)
	scope := filepath.Join(h.root, "a.txt")
	call := h.writeCall(scope)

	done := make(chan struct{})
	var dec policy.Decision
	var reason string
	go func() {
		dec, reason = h.attempt(context.Background(), call)
		close(done)
	}()

	var ap policy.ApprovalRequest
	select {
	case ap = <-h.pending:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到待审批请求")
	}
	if ap.ToolName != "write" {
		t.Fatalf("审批工具名 = %q", ap.ToolName)
	}
	if err := h.client.Resolve(context.Background(), ap.ID, policy.ApprovalOnce); err != nil {
		t.Fatalf("批准: %v", err)
	}
	<-done
	if dec != policy.DecisionAllow {
		t.Fatalf("批准后应为 allow，实际 %s（%s）", dec, reason)
	}
	if got := atomic.LoadInt32(h.counter); got != 1 {
		t.Fatalf("批准一次应只执行一次，实际 %d", got)
	}

	// 同一许可不得重放：直接用旧审批 ID 再尝试执行前复查。
	if err := h.svc.ValidateBinding(context.Background(), h.exec, ap.ID, call); err == nil {
		t.Fatal("已被领取的一次性许可不得再次通过执行前复查")
	}

	// 不同文件不能复用同一结论：换 scope 再走一遍，必须重新要求审批（而不是直接 allow）。
	other := h.writeCall(filepath.Join(h.root, "b.txt"))
	otherDone := make(chan policy.Decision, 1)
	go func() {
		dec, _ := h.attempt(context.Background(), other)
		otherDone <- dec
	}()
	var ap2 policy.ApprovalRequest
	select {
	case ap2 = <-h.pending:
	case <-time.After(3 * time.Second):
		t.Fatal("换文件后应重新要求审批，实际没有新的待审批请求（可能复用了旧结论）")
	}
	if ap2.ID == ap.ID {
		t.Fatal("不同文件不得共用同一条审批")
	}
	// 这次拒绝，确认拒绝路径同样不产生副作用。
	if err := h.client.Resolve(context.Background(), ap2.ID, policy.ApprovalDeny); err != nil {
		t.Fatalf("拒绝: %v", err)
	}
	if dec := <-otherDone; dec != policy.DecisionDeny {
		t.Fatalf("拒绝后应 deny，实际 %s", dec)
	}
	if got := atomic.LoadInt32(h.counter); got != 1 {
		t.Fatalf("只应有第一次的 1 次副作用，实际 %d", got)
	}

	// ── 外部观察：放行/拒绝的结论与执行次数全部从磁盘读回 ──
	own := h.ownWire(t)
	execs := h.ownExecutions(t)
	if got := allowedForScope(own, scope); got != 1 {
		t.Fatalf("账本里该文件应恰好 1 次 allow，实际 %d 次", got)
	}
	if got := deniedForScope(own, other.ResourceScope); got != 1 {
		t.Fatalf("换文件后被拒绝的调用应恰好 1 次 deny，实际 %d 次", got)
	}
	if got := executedCount(execs); got != 1 {
		t.Fatalf("执行账本里应恰好 1 次执行，实际 %d 次: %+v", got, execs)
	}
	if execs[0].Path != scope {
		t.Fatalf("执行的目标文件应是 %s，账本里是 %s", scope, execs[0].Path)
	}
	if body := readIfExists(scope); body != "executed:write" {
		t.Fatalf("被放行的执行应真的写出目标文件，实际内容 %q", body)
	}
	if _, err := os.Stat(other.ResourceScope); err == nil {
		t.Fatal("被拒绝的调用不得写出目标文件")
	}
	h.observe(t, map[string]any{
		"approvedScope":    scope,
		"deniedScope":      other.ResourceScope,
		"allowedOnce":      allowedForScope(own, scope),
		"deniedOther":      deniedForScope(own, other.ResourceScope),
		"executionsOnDisk": executedCount(execs),
	})
}

// TestDenyForOutOfScopePath 覆盖 SEC05 与 SEC02 的组合：越界直接拒，且不签发审批。
func TestDenyForOutOfScopePath(t *testing.T) {
	h := newHarness(t)
	outside := filepath.Join(h.root, "..", "escape.txt")
	dec, reason := h.attempt(context.Background(), h.writeCall(outside))
	if dec != policy.DecisionDeny {
		t.Fatalf("越界写必须 deny，实际 %s", dec)
	}
	if !strings.Contains(reason, "越界") {
		t.Fatalf("拒绝原因应说明越界，实际 %q", reason)
	}
	if got := atomic.LoadInt32(h.counter); got != 0 {
		t.Fatalf("越界路径不得产生副作用，实际 %d", got)
	}
}

// TestAskTimeoutDeniesWithoutSideEffect 覆盖 SEC06：审批超时不执行。
func TestAskTimeoutDeniesWithoutSideEffect(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.AskTimeout = 250 * time.Millisecond })
	dec, reason := h.attempt(context.Background(), h.writeCall(filepath.Join(h.root, "c.txt")))
	if dec != policy.DecisionDeny {
		t.Fatalf("超时必须 deny，实际 %s", dec)
	}
	if !strings.Contains(reason, "超时") {
		t.Fatalf("拒绝原因应说明超时，实际 %q", reason)
	}
	if got := atomic.LoadInt32(h.counter); got != 0 {
		t.Fatalf("超时路径不得产生副作用，实际 %d", got)
	}

	// ── 外部观察：超时结论与"零执行"都从磁盘读回 ──
	scope := filepath.Join(h.root, "c.txt")
	own := h.ownWire(t)
	if got := deniedForScope(own, scope); got != 1 {
		t.Fatalf("账本里超时请求应恰好 1 次 deny，实际 %d 次", got)
	}
	if got := executedCount(h.ownExecutions(t)); got != 0 {
		t.Fatalf("超时路径不得执行，执行账本里有 %d 条", got)
	}
	if _, err := os.Stat(scope); err == nil {
		t.Fatal("超时路径不得写出目标文件")
	}
	h.observe(t, map[string]any{"timeoutDenied": deniedForScope(own, scope), "timeoutScope": scope})
}

// TestRevokeWhileQueuedDenies 覆盖"已排队许可撤销后再次校验"。
func TestRevokeWhileQueuedDenies(t *testing.T) {
	h := newHarness(t)
	call := h.writeCall(filepath.Join(h.root, "d.txt"))

	done := make(chan policy.Decision, 1)
	go func() {
		dec, _ := h.attempt(context.Background(), call)
		done <- dec
	}()
	var ap policy.ApprovalRequest
	select {
	case ap = <-h.pending:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到待审批请求")
	}

	// 撤销之后即使再批准也不认。
	if err := h.client.Revoke(context.Background(), ap.ID); err != nil {
		t.Fatalf("撤销: %v", err)
	}
	if err := h.client.Resolve(context.Background(), ap.ID, policy.ApprovalOnce); err == nil {
		t.Fatal("已撤销的审批不得被批准")
	}
	if dec := <-done; dec != policy.DecisionDeny {
		t.Fatalf("撤销后必须 deny，实际 %s", dec)
	}
	if got := atomic.LoadInt32(h.counter); got != 0 {
		t.Fatalf("撤销路径不得产生副作用，实际 %d", got)
	}

	// ── 外部观察：撤销后的结论与"零执行"都从磁盘读回 ──
	own := h.ownWire(t)
	if got := deniedForScope(own, call.ResourceScope); got != 1 {
		t.Fatalf("账本里被撤销的请求应恰好 1 次 deny，实际 %d 次", got)
	}
	if got := executedCount(h.ownExecutions(t)); got != 0 {
		t.Fatalf("撤销路径不得执行，执行账本里有 %d 条", got)
	}
	if _, err := os.Stat(call.ResourceScope); err == nil {
		t.Fatal("撤销路径不得写出目标文件")
	}
	h.observe(t, map[string]any{"revokedDenied": deniedForScope(own, call.ResourceScope), "revokedScope": call.ResourceScope})
}

// TestCloseDeniesPendingAndRefusesNewWork 覆盖"策略服务失联即不执行"。
func TestCloseDeniesPendingAndRefusesNewWork(t *testing.T) {
	h := newHarness(t)
	call := h.writeCall(filepath.Join(h.root, "e.txt"))

	done := make(chan policy.Decision, 1)
	go func() {
		dec, _ := h.attempt(context.Background(), call)
		done <- dec
	}()
	select {
	case <-h.pending:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到待审批请求")
	}

	if err := h.srv.Close(context.Background()); err != nil {
		t.Fatalf("关闭端点: %v", err)
	}
	if dec := <-done; dec != policy.DecisionDeny {
		t.Fatalf("端点关闭后未决调用必须 deny，实际 %s", dec)
	}
	if got := atomic.LoadInt32(h.counter); got != 0 {
		t.Fatalf("关闭路径不得产生副作用，实际 %d", got)
	}

	// 关闭后新请求也拿不到 allow。
	closedScope := filepath.Join(h.root, "f.txt")
	if dec, _, err := h.client.Authorize(context.Background(), h.writeCall(closedScope)); err == nil && dec == policy.DecisionAllow {
		t.Fatal("端点关闭后不得返回 allow")
	}

	// ── 外部观察：关闭路径同样零执行，且账本里没有 allow ──
	own := h.ownWire(t)
	for _, rec := range own {
		if decisionOf(rec) == string(policy.DecisionAllow) {
			t.Fatalf("端点关闭路径不得出现 allow，账本里有：%s", rec.Path)
		}
	}
	if got := executedCount(h.ownExecutions(t)); got != 0 {
		t.Fatalf("关闭路径不得执行，执行账本里有 %d 条", got)
	}
	for _, target := range []string{call.ResourceScope, closedScope} {
		if _, err := os.Stat(target); err == nil {
			t.Fatalf("关闭路径不得写出目标文件: %s", target)
		}
	}
	h.observe(t, map[string]any{
		"closedDenied":  deniedForScope(own, call.ResourceScope),
		"closedScope":   call.ResourceScope,
		"closedNewWork": closedScope,
	})
}

// TestOriginFromOwnEndpointIsAccepted 说明 Origin 校验不是"一律拒浏览器"：
// 来自端点自身的 Origin 是合法的（例如以后用内置 WebView 发起）。
func TestOriginFromOwnEndpointIsAccepted(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.AskTimeout = 300 * time.Millisecond })
	body := `{"toolCallId":"c1","toolName":"mcp_probe","source":"external-mcp","arguments":{},"origin":"user"}`
	req, err := http.NewRequest(http.MethodPost, h.srv.Endpoint().URL+"/v1/authorize", strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.srv.token)
	req.Header.Set("X-MyAgent-Session", string(h.exec.SessionID))
	req.Header.Set("Origin", h.srv.Endpoint().URL)

	resp, err := h.rawClient(3 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("发送请求: %v", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("同源请求应通过认证，实际 %d: %s", resp.StatusCode, payload)
	}
	// 该调用需要人工确认，会等到超时；这里只验证它已进入策略层（返回了合法 JSON 结论）。
	var out authorizeResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("解析响应: %v (%s)", err, payload)
	}
	if out.Decision != string(policy.DecisionDeny) {
		t.Fatalf("无人批准时应 deny，实际 %s", out.Decision)
	}

	// ── 外部观察：同源请求确实被接受（200 + 策略结论），执行账本为空 ──
	own := h.ownWire(t)
	if len(own) != 1 {
		t.Fatalf("本条测试应只发出 1 条请求，账本里是 %d 条", len(own))
	}
	if own[0].Status != http.StatusOK {
		t.Fatalf("同源请求应被接受，账本里状态是 %d", own[0].Status)
	}
	if own[0].Origin != h.srv.Endpoint().URL {
		t.Fatalf("账本里的 Origin 应是端点自身，实际 %q", own[0].Origin)
	}
	if got := executedCount(h.ownExecutions(t)); got != 0 {
		t.Fatalf("无人批准时不得执行，执行账本里有 %d 条", got)
	}
	h.observe(t, map[string]any{"ownOriginAccepted": true, "ownOriginStatus": own[0].Status})
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

// TestMain 保证测试不因宿主代理环境变量误连代理。
func TestMain(m *testing.M) {
	for _, k := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "all_proxy", "ALL_PROXY"} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

// TestGateDeniesBeforeAuthorizationAndCreatesNoApproval 覆盖 RUN06 的预算闸门接点：
// 闸门必须在策略服务之前拦截——不创建审批、不产生副作用，连审批序号都不能被占用。
//
// 为什么强调"在 Authorize 之前"：闸门如果挂在判定结果之后，被预算拦下的调用仍然会
// 签发一条审批并推给 UI，用户会看到一个永远执行不了的确认框。
func TestGateDeniesBeforeAuthorizationAndCreatesNoApproval(t *testing.T) {
	var pendingCount int32
	h := newHarness(t, func(o *Options) {
		prev := o.OnPending
		o.OnPending = func(p policy.ApprovalRequest) {
			atomic.AddInt32(&pendingCount, 1)
			prev(p)
		}
		o.Gate = func(ctx context.Context, call policy.ToolCall) error {
			if call.Name == "write" {
				return fmt.Errorf("预算用尽：测试闸门拒绝 %s", call.Name)
			}
			return nil
		}
	})

	dec, reason := h.attempt(context.Background(), h.writeCall(filepath.Join(h.root, "gated.txt")))
	if dec != policy.DecisionDeny {
		t.Fatalf("闸门报错必须 deny，实际 %s", dec)
	}
	if !strings.Contains(reason, "预算用尽") {
		t.Fatalf("拒绝原因应带闸门的消息，实际 %q", reason)
	}
	if got := atomic.LoadInt32(h.counter); got != 0 {
		t.Fatalf("闸门拒绝不得产生副作用，实际 %d", got)
	}
	if got := atomic.LoadInt32(&pendingCount); got != 0 {
		t.Fatalf("闸门拒绝不得推送审批请求，实际 %d 条", got)
	}
	select {
	case p := <-h.pending:
		t.Fatalf("闸门拒绝不得创建审批，实际收到 %+v", p)
	default:
	}

	// "没有创建审批"的硬证据：策略服务的审批 id 带单调序号（apr-<seq>-...）。
	// 闸门若在 Authorize 之后才拦，序号已经被占用，下面这条真实审批就不会是第 1 条。
	res, err := h.svc.Authorize(context.Background(), h.exec, h.writeCall(filepath.Join(h.root, "real.txt")))
	if err != nil {
		t.Fatalf("直接授权失败: %v", err)
	}
	if res.Decision != policy.DecisionAsk {
		t.Fatalf("写操作应进入审批环节，实际 %s（%s）", res.Decision, res.Reason)
	}
	if !strings.HasPrefix(res.ApprovalID, "apr-1-") {
		t.Fatalf("闸门拒绝的调用不应占用审批序号，实际 %s", res.ApprovalID)
	}
	if _, err := h.svc.Pending(context.Background(), res.ApprovalID); err != nil {
		t.Fatalf("真实审批应可查到: %v", err)
	}
}
