package policybridge

// SEC04.runtime / SEC06 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写，artifact 覆盖写；没设置该变量（本地 focused 运行）时
// 只落到临时目录，不往证据树里写。
//
// 两个外部观察源：
//   - wire.jsonl：客户端 http.RoundTripper 记下**真正发出的字节**与收到的状态码
//     （method/path/选中头部/请求体/响应体/状态）。Authorization 只记 present/absent，
//     令牌本体永不落盘；
//   - executions.jsonl：工具处理器（真正执行副作用的那一处）每执行一次追加一行，
//     同时把副作用真的写到磁盘上。被拒/超时/撤销/关闭的路径不会有任何一行。
//
// 断言与观察值都从这两个文件读回；测试进程里的 counter 只作为既有断言保留。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/policy"
)

func bridgeObservationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// bridgeArtifactDir 返回 artifact 的落盘目录（未配置观察目录时退回临时目录）。
func bridgeArtifactDir(t *testing.T) string {
	t.Helper()
	if dir := bridgeObservationDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
		}
		return dir
	}
	return t.TempDir()
}

// maxRecordedBody 是 wire 账本里单个请求/响应体的记录上限。
//
// 超限只记前 N 字节并标记 truncated：否则"超大请求体"那条负例会把 64KiB 的
// 垃圾写进证据。截断是显式记录的，不会被误读成完整内容。
const maxRecordedBody = 8 << 10

// wireRecord 是一条真实的 HTTP 往返记录（由客户端 RoundTripper 写出）。
type wireRecord struct {
	Seq           int             `json:"seq"`
	TS            string          `json:"ts"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	Host          string          `json:"host"`
	Authorization string          `json:"authorization"` // present/absent（绝不记令牌本体）
	Session       string          `json:"session,omitempty"`
	Origin        string          `json:"origin,omitempty"`
	ContentType   string          `json:"contentType,omitempty"`
	BodyBytes     int             `json:"bodyBytes"`
	Body          json.RawMessage `json:"body,omitempty"`
	Truncated     bool            `json:"bodyTruncated,omitempty"`
	Status        int             `json:"status,omitempty"`
	ResponseBytes int             `json:"responseBytes,omitempty"`
	Response      json.RawMessage `json:"response,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// wireLog 是客户端侧的往返记录器：它包住真正的 Transport，记录后原样放行。
//
// 为什么记在客户端：这条 case 要证明的是"被拒的请求根本没有到策略层"，
// 所以需要的是**实际发出去的字节**与服务端回的状态码，而不是被测实现的自述。
type wireLog struct {
	inner http.RoundTripper
	path  string

	mu  sync.Mutex
	seq int
}

func newWireLog(t *testing.T, path string) *wireLog {
	t.Helper()
	// 账本必须存在：一条请求都没有也是观察值，不能靠文件缺失来表达。
	//
	// 只在文件不存在时创建，绝不截断：同一个 case 的多条测试共用一份账本，
	// 后跑的那条把先跑的记录清掉会让证据只剩最后一次测试的内容。
	ensureLedger(t, path, "wire")
	return &wireLog{inner: bridgeTransport(), path: path}
}

// ensureLedger 保证账本文件存在且不截断已有内容。
func ensureLedger(t *testing.T, path, kind string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("创建 %s 账本失败: %v", kind, err)
	}
}

// bridgeTransport 是显式不走代理的 Transport（本机常设 http_proxy，
// 默认 Transport 会把 127.0.0.1 的请求交给代理，测出来的不是本机端点）。
func bridgeTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return tr
}

func (w *wireLog) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := wireRecord{
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Method:        req.Method,
		Path:          req.URL.Path,
		Host:          req.Host,
		Authorization: "absent",
		Session:       req.Header.Get("X-MyAgent-Session"),
		Origin:        req.Header.Get("Origin"),
		ContentType:   req.Header.Get("Content-Type"),
	}
	if req.Header.Get("Authorization") != "" {
		// 只记"有没有带"，不记值：令牌落盘就等于把凭证写进了证据树。
		rec.Authorization = "present"
	}
	if body := drainRequestBody(req); len(body) > 0 {
		rec.BodyBytes = len(body)
		rec.Body, rec.Truncated = clipBody(body)
	}

	resp, err := w.inner.RoundTrip(req)
	if err != nil {
		rec.Error = err.Error()
		w.append(rec)
		return nil, err
	}
	rec.Status = resp.StatusCode
	if payload, readErr := io.ReadAll(resp.Body); readErr == nil {
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(payload))
		rec.ResponseBytes = len(payload)
		rec.Response, rec.Truncated = clipBody(payload)
		if rec.Truncated && rec.Body != nil {
			rec.Truncated = true
		}
	}
	w.append(rec)
	return resp, nil
}

// drainRequestBody 读出请求体后放回，保证真正发出的字节与记录的一致。
func drainRequestBody(req *http.Request) []byte {
	if req.Body == nil {
		return nil
	}
	b, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil
	}
	req.Body = io.NopCloser(bytes.NewReader(b))
	return b
}

// clipBody 把超长内容截断并保留"是否截断"的事实。
func clipBody(b []byte) (json.RawMessage, bool) {
	if len(b) > maxRecordedBody {
		out, _ := json.Marshal(string(b[:maxRecordedBody]))
		return out, true
	}
	if json.Valid(b) {
		return json.RawMessage(b), false
	}
	out, _ := json.Marshal(string(b))
	return out, false
}

func (w *wireLog) append(rec wireRecord) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	rec.Seq = w.seq
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// readWireRecords 从磁盘读回 wire 账本。
func readWireRecords(t *testing.T, path string) []wireRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回 wire 账本 %s 失败: %v", path, err)
	}
	var out []wireRecord
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec wireRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("wire 记录不是合法 JSON: %v（原文 %s）", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// decisionOf 从 wire 记录的响应体里取策略结论（不是从客户端返回值取）。
func decisionOf(rec wireRecord) string {
	if len(rec.Response) == 0 {
		return ""
	}
	var out struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(rec.Response, &out); err != nil {
		return ""
	}
	return out.Decision
}

// scopeOf 从 wire 记录的请求体里取 ResourceScope（用来把某条请求归属到场景）。
func scopeOf(rec wireRecord) string {
	if len(rec.Body) == 0 {
		return ""
	}
	var out struct {
		ResourceScope string `json:"resourceScope"`
	}
	if err := json.Unmarshal(rec.Body, &out); err != nil {
		return ""
	}
	return out.ResourceScope
}

// executionRecord 是工具处理器真正执行副作用时追加的一行。
type executionRecord struct {
	Seq    int    `json:"seq"`
	TS     string `json:"ts"`
	Tool   string `json:"tool"`
	Path   string `json:"path"`
	Argv   string `json:"argv"`
	PID    int    `json:"pid"`
	Error  string `json:"error,omitempty"`
	Status string `json:"status"`
}

// execLog 是执行账本（追加写）。
type execLog struct {
	path string

	mu  sync.Mutex
	seq int
}

func newExecLog(t *testing.T, path string) *execLog {
	t.Helper()
	ensureLedger(t, path, "执行")
	return &execLog{path: path}
}

// record 记下一次真实执行。error 非空表示副作用没写成（也算一条事实，但不能算执行）。
func (e *execLog) record(tool, target, argv string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	rec := executionRecord{
		Seq: e.seq, TS: time.Now().UTC().Format(time.RFC3339Nano),
		Tool: tool, Path: target, Argv: argv, PID: os.Getpid(), Status: "executed",
	}
	if err != nil {
		rec.Error = err.Error()
		rec.Status = "failed"
	}
	line, mErr := json.Marshal(rec)
	if mErr != nil {
		return
	}
	f, openErr := os.OpenFile(e.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// readExecutions 从磁盘读回执行账本。
func readExecutions(t *testing.T, path string) []executionRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回执行账本 %s 失败: %v", path, err)
	}
	var out []executionRecord
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec executionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("执行记录不是合法 JSON: %v（原文 %s）", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// executedCount 只数**真的写成功**的执行（失败行单独可见，但不算执行）。
func executedCount(records []executionRecord) int {
	n := 0
	for _, rec := range records {
		if rec.Error == "" {
			n++
		}
	}
	return n
}

// initObservation 给 harness 装上两个外部观察装置。
//
// 未配置 ANVIL_OBSERVATION_DIR 时落到临时目录：本地 focused 运行也要真的记录，
// 只是不往证据树里写。
func (h *harness) initObservation(t *testing.T) {
	t.Helper()
	dir := bridgeArtifactDir(t)
	h.wire = newWireLog(t, filepath.Join(dir, "wire.jsonl"))
	h.executions = newExecLog(t, filepath.Join(dir, "executions.jsonl"))
	// 封装 Client 的请求也要被记录：直接把它的 Transport 换成记录器。
	if h.client != nil && h.client.hc != nil {
		h.client.hc.Transport = h.wire
	}
	// 记账本起点：同一个 case 的多条测试共用一份账本，靠这个下标切出"本条测试的记录"。
	h.wireBaseline = len(readWireRecords(t, h.wire.path))
	h.execBaseline = len(readExecutions(t, h.executions.path))
}

// ownWire 返回本条测试产生的 wire 记录（从账本起点切出来，仍然来自磁盘）。
func (h *harness) ownWire(t *testing.T) []wireRecord {
	t.Helper()
	all := readWireRecords(t, h.wire.path)
	if h.wireBaseline > len(all) {
		t.Fatalf("wire 账本比起点还短（%d < %d）：账本被别的测试截断了", len(all), h.wireBaseline)
	}
	return all[h.wireBaseline:]
}

// ownExecutions 返回本条测试产生的执行记录。
func (h *harness) ownExecutions(t *testing.T) []executionRecord {
	t.Helper()
	all := readExecutions(t, h.executions.path)
	if h.execBaseline > len(all) {
		t.Fatalf("执行账本比起点还短（%d < %d）", len(all), h.execBaseline)
	}
	return all[h.execBaseline:]
}

// rawClient 返回带 wire 记录的裸 http.Client。
//
// 负例要求"这些请求根本进不到策略层"，所以不能用封装 Client；但字节仍必须被
// 记录下来，否则"被拒"就只剩测试进程的断言。
func (h *harness) rawClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: h.wire}
}

// observe 把两条账本读回来，算成观察值写进 observation.json。
//
// 为什么每条测试都调一次：同一个 case 的多条测试共用一份账本，而
// observation.json 是合并写。每条测试都从**整个文件**重算总量，于是
// 最后跑的那条写下的就是完整总量（不依赖测试执行顺序）；
// 每条测试自己的快照另外留在 byTest:<name> 里。
func (h *harness) observe(t *testing.T, extra map[string]any) {
	t.Helper()
	wire := readWireRecords(t, h.wire.path)
	execs := readExecutions(t, h.executions.path)

	// 令牌绝不能出现在 wire 账本里：只记 present/absent。
	raw, err := os.ReadFile(h.wire.path)
	if err != nil {
		t.Fatalf("读回 wire 账本失败: %v", err)
	}
	if strings.Contains(string(raw), h.srv.token) {
		t.Fatalf("wire 账本里出现了令牌本体：令牌绝不能落盘")
	}

	statusCount := func(recs []wireRecord, pred func(int) bool) int {
		n := 0
		for _, rec := range recs {
			if pred(rec.Status) {
				n++
			}
		}
		return n
	}
	transportErrors := func(recs []wireRecord) int {
		n := 0
		for _, rec := range recs {
			if rec.Error != "" {
				n++
			}
		}
		return n
	}
	toolsOf := func(recs []executionRecord) []string {
		out := make([]string, 0, len(recs))
		for _, rec := range recs {
			out = append(out, rec.Tool)
		}
		return out
	}

	// 本条测试自己的切片（从磁盘账本的起点下标切出），用于 byTest 快照与场景归属。
	mine := h.ownWire(t)
	myExecs := h.ownExecutions(t)
	own := map[string]any{
		"requests":        len(mine),
		"rejected":        statusCount(mine, func(s int) bool { return s >= 400 }),
		"accepted":        statusCount(mine, func(s int) bool { return s == http.StatusOK }),
		"transportErrors": transportErrors(mine),
		"executions":      executedCount(myExecs),
		"executedTools":   toolsOf(myExecs),
		"wirePath":        h.wire.path,
		"execPath":        h.executions.path,
	}
	for k, v := range extra {
		own[k] = v
	}

	// 顶层事实是**整份账本**的总量：多条测试共用一份账本，最后跑的那条
	// 写下的就是完整总量，不依赖测试执行顺序。
	facts := map[string]any{
		"requests":           len(wire),
		"rejected":           statusCount(wire, func(s int) bool { return s >= 400 }),
		"accepted":           statusCount(wire, func(s int) bool { return s == http.StatusOK }),
		"transportErrors":    transportErrors(wire),
		"executions":         executedCount(execs),
		"executedTools":      toolsOf(execs),
		"tokenInWireLog":     false,
		"byTest:" + t.Name(): own,
	}
	for k, v := range extra {
		facts[k] = v
	}
	writeBridgeObservation(t, facts)
}

// writeBridgeObservation 把观察值合并写进观察目录。
func writeBridgeObservation(t *testing.T, facts map[string]any) {
	t.Helper()
	dir := bridgeObservationDir()
	if dir == "" {
		return // 本地 focused 运行：不落盘
	}
	bridgeObsMu.Lock()
	defer bridgeObsMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", err)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == t.Name() {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, t.Name())
	}
	merged["tests"] = tests
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

var bridgeObsMu sync.Mutex

// deniedForScope 数某个资源范围上被拒的授权请求（从 wire 账本读回）。
//
// 用资源范围归属场景而不是靠测试自报：账本里记的是真正发出去的请求体，
// 响应体里是服务端真的给出的结论。
func deniedForScope(wire []wireRecord, scope string) int {
	n := 0
	for _, rec := range wire {
		if scopeOf(rec) == scope && decisionOf(rec) == "deny" {
			n++
		}
	}
	return n
}

// allowedForScope 数某个资源范围上被放行的授权请求。
func allowedForScope(wire []wireRecord, scope string) int {
	n := 0
	for _, rec := range wire {
		if scopeOf(rec) == scope && decisionOf(rec) == "allow" {
			n++
		}
	}
	return n
}

// readIfExists 读文件内容，不存在时返回空串（用于"文件根本没生成"这种情形）。
func readIfExists(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// TestApprovedOnceSideEffectIsExactlyOneObservedOnWire 是 SEC04.runtime 的
// 外部观察版本：同样的"批准一次只执行一次"，但结论一律从 wire 账本与执行账本
// 读回——账本里记的是实际发出的请求体、服务端回的结论，以及工具处理器真正
// 写出的那个文件。
func TestApprovedOnceSideEffectIsExactlyOneObservedOnWire(t *testing.T) {
	h := newHarness(t)
	scope := filepath.Join(h.root, "wire-once.txt")
	call := h.writeCall(scope)

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
	if err := h.client.Resolve(context.Background(), ap.ID, policy.ApprovalOnce); err != nil {
		t.Fatalf("批准: %v", err)
	}
	if dec := <-done; dec != policy.DecisionAllow {
		t.Fatalf("批准后应 allow，实际 %s", dec)
	}

	own := h.ownWire(t)
	execs := h.ownExecutions(t)
	if got := allowedForScope(own, scope); got != 1 {
		t.Fatalf("wire 账本里应恰好 1 次 allow，实际 %d 次（共 %d 条请求）", got, len(own))
	}
	// 令牌本体绝不能出现在账本里。
	raw, err := os.ReadFile(h.wire.path)
	if err != nil {
		t.Fatalf("读回 wire 账本失败: %v", err)
	}
	if strings.Contains(string(raw), h.srv.token) {
		t.Fatal("wire 账本里出现了令牌本体")
	}
	if got := executedCount(execs); got != 1 {
		t.Fatalf("执行账本里应恰好 1 次执行，实际 %d 次: %+v", got, execs)
	}
	if execs[0].Path != scope {
		t.Fatalf("执行目标应是 %s，账本里是 %s", scope, execs[0].Path)
	}
	if got := readIfExists(scope); got != "executed:write" {
		t.Fatalf("被放行的执行应真的写出目标文件，实际内容 %q", got)
	}
	// 一次性许可不得重放：再走一次执行前复查必须失败，且不产生新执行。
	if err := h.svc.ValidateBinding(context.Background(), h.exec, ap.ID, call); err == nil {
		t.Fatal("已被领取的一次性许可不得再次通过执行前复查")
	}
	if got := executedCount(h.ownExecutions(t)); got != 1 {
		t.Fatalf("重放检查不得产生新执行，执行账本里是 %d 条", got)
	}

	h.observe(t, map[string]any{
		"approvedScope":     scope,
		"allowedOnce":       allowedForScope(own, scope),
		"executionsOnDisk":  executedCount(execs),
		"executedTarget":    execs[0].Path,
		"targetFileContent": readIfExists(scope),
	})
}

// TestAskTimeoutDeniesWithoutSideEffectObservedOnWire 是 SEC06 的外部观察版本：
// 审批超时后不得有任何副作用，结论与"零执行"都从磁盘读回。
func TestAskTimeoutDeniesWithoutSideEffectObservedOnWire(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.AskTimeout = 250 * time.Millisecond })
	scope := filepath.Join(h.root, "wire-timeout.txt")
	dec, reason := h.attempt(context.Background(), h.writeCall(scope))
	if dec != policy.DecisionDeny {
		t.Fatalf("超时必须 deny，实际 %s", dec)
	}
	if !strings.Contains(reason, "超时") {
		t.Fatalf("拒绝原因应说明超时，实际 %q", reason)
	}

	own := h.ownWire(t)
	if got := deniedForScope(own, scope); got != 1 {
		t.Fatalf("wire 账本里超时请求应恰好 1 次 deny，实际 %d 次", got)
	}
	if got := allowedForScope(own, scope); got != 0 {
		t.Fatalf("超时路径不得出现 allow，账本里有 %d 次", got)
	}
	if got := executedCount(h.ownExecutions(t)); got != 0 {
		t.Fatalf("超时路径不得执行，执行账本里有 %d 条", got)
	}
	if _, err := os.Stat(scope); err == nil {
		t.Fatal("超时路径不得写出目标文件")
	}

	h.observe(t, map[string]any{
		"timeoutDenied":    deniedForScope(own, scope),
		"timeoutScope":     scope,
		"executionsOnDisk": executedCount(h.ownExecutions(t)),
	})
}
