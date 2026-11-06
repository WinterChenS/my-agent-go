package browserfixture

// BR00 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写，artifact 覆盖写；没设置该变量（本地 focused 运行）时
// 什么也不写。
//
// 两个观察源都在**服务端**，不在断言路径上：
//   - fixture-requests.jsonl：每个 origin 的 HTTP handler 在真正处理完一次请求后
//     追加一行（origin、method、path、Origin 头、状态码、响应字节数）。它证明的是
//     "请求真的到达了服务端、服务端真的回了什么"；
//   - counters.json：测试用 HTTP 去 /__counters 取回的服务端计数快照（before/after）。
//     counter 是 B00 的 oracle，页面上的数字只是给人看的。
//
// 为什么不改 server.go：本次任务的可改文件只到 *_test.go。所以这里用同包的
// Origin.routes 重新装一层"记录 + 原样转发"的 handler——跑的还是同一套页面与
// 同一套计数器，只是外面多了一层记录。它不改变任何服务端语义。

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureLogOpenKind 是请求日志自述行的 kind：读取时跳过。
// 它让"一条请求都没有"的日志仍然是非空文件（证据不允许用空文件表达"什么都没发生"）。
const fixtureLogOpenKind = "fixture-log-open"

func browserObservationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// requestLogRecord 是服务端为一次真实请求写下的记录。
type requestLogRecord struct {
	Kind         string `json:"kind,omitempty"`
	TS           string `json:"ts"`
	Origin       string `json:"origin"`
	SelfOrigin   string `json:"selfOrigin"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	RawQuery     string `json:"rawQuery,omitempty"`
	OriginHeader string `json:"originHeader,omitempty"`
	Host         string `json:"host"`
	Status       int    `json:"status"`
	Bytes        int    `json:"bytes"`
}

// statusRecorder 记录 handler 真正写出的状态码与字节数。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

var fixtureLogMu sync.Mutex

func ensureFixtureLog(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	}
	head, err := json.Marshal(map[string]any{
		"kind": fixtureLogOpenKind,
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		"note": "本行只是自述，不计入请求条数",
	})
	if err != nil {
		t.Fatalf("编码请求日志自述行失败: %v", err)
	}
	if err := os.WriteFile(path, append(head, 0x0a), 0o644); err != nil {
		t.Fatalf("创建请求日志 %s 失败: %v", path, err)
	}
}

func appendRequestLog(path string, rec requestLogRecord) {
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	fixtureLogMu.Lock()
	defer fixtureLogMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, 0x0a))
	_ = f.Close()
}

// serveWithRequestLog 与 Origin.serve 等价，只是把 handler 包一层记录器。
//
// 记录在 next.ServeHTTP 之后写：状态码与字节数是 handler 真正写出来的，
// 不是测试推测的。
func (o *Origin) serveWithRequestLog(ln net.Listener, other, logPath string) error {
	mux := http.NewServeMux()
	o.routes(mux, other)
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		mux.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		appendRequestLog(logPath, requestLogRecord{
			TS:           time.Now().UTC().Format(time.RFC3339Nano),
			Origin:       o.Name,
			SelfOrigin:   o.URL,
			Method:       r.Method,
			Path:         r.URL.Path,
			RawQuery:     r.URL.RawQuery,
			OriginHeader: r.Header.Get("Origin"),
			Host:         r.Host,
			Status:       status,
			Bytes:        rec.bytes,
		})
	})
	srv := &http.Server{Handler: logged, ReadHeaderTimeout: 5 * time.Second}
	o.srv = srv
	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

// newLoggingFixture 启动两个 origin，并让每个 handler 把请求写进 logPath。
func newLoggingFixture(t *testing.T, logPath string) (*Fixture, error) {
	t.Helper()
	ensureFixtureLog(t, logPath)
	lnA, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听 primary origin: %w", err)
	}
	lnB, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = lnA.Close()
		return nil, fmt.Errorf("监听 secondary origin: %w", err)
	}
	a := &Origin{Name: "primary", URL: "http://" + lnA.Addr().String(), counters: map[string]int{}}
	b := &Origin{Name: "secondary", URL: "http://" + lnB.Addr().String(), counters: map[string]int{}}
	for _, p := range []struct {
		origin *Origin
		ln     net.Listener
		other  string
	}{
		{a, lnA, b.URL},
		{b, lnB, a.URL},
	} {
		if err := p.origin.serveWithRequestLog(p.ln, p.other, logPath); err != nil {
			_ = a.Close()
			_ = b.Close()
			return nil, err
		}
	}
	return &Fixture{Primary: a, Secondary: b}, nil
}

// readRequestLog 从磁盘读回服务端请求日志（跳过自述行）。
func readRequestLog(t *testing.T, path string) []requestLogRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回请求日志 %s 失败: %v", path, err)
	}
	var out []requestLogRecord
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec requestLogRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("请求日志记录不是合法 JSON: %v（原文 %s）", err, line)
		}
		if rec.Kind == fixtureLogOpenKind {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// requestOrigins 取日志里出现过的 origin（排序后去重）。
func requestOrigins(recs []requestLogRecord) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 2)
	for _, rec := range recs {
		if rec.SelfOrigin == "" || seen[rec.SelfOrigin] {
			continue
		}
		seen[rec.SelfOrigin] = true
		out = append(out, rec.SelfOrigin)
	}
	sort.Strings(out)
	return out
}

// countersSnapshot 是用 HTTP 从 /__counters 取回的服务端计数（before/after 各一份）。
type countersSnapshot struct {
	Primary   map[string]int `json:"primary"`
	Secondary map[string]int `json:"secondary"`
	FetchedAt string         `json:"fetchedAt"`
}

// fetchCounters 真的走一次 HTTP 去读服务端计数。
func fetchCounters(t *testing.T, f *Fixture) countersSnapshot {
	t.Helper()
	snap := countersSnapshot{
		Primary:   map[string]int{},
		Secondary: map[string]int{},
		FetchedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	for _, p := range []struct {
		key    string
		origin *Origin
		out    *map[string]int
	}{
		{"primary", f.Primary, &snap.Primary},
		{"secondary", f.Secondary, &snap.Secondary},
	} {
		res := get(t, p.origin.URL+"/__counters", nil)
		if res.status != http.StatusOK {
			t.Fatalf("读取 %s 的计数器失败：状态 %d", p.key, res.status)
		}
		if err := json.Unmarshal([]byte(res.body), p.out); err != nil {
			t.Fatalf("解析 %s 的计数器失败: %v（%s）", p.key, err, res.body)
		}
	}
	return snap
}

// writeFixtureObservation 把观察值与 artifact 合并写进观察目录。
func writeFixtureObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := browserObservationDir()
	if dir == "" {
		return
	}
	fixtureObsMu.Lock()
	defer fixtureObsMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
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

var fixtureObsMu sync.Mutex

// observeFixtureRun 在一条 fixture 测试结束后，从**整份请求日志**重算观察值。
//
// 为什么每条测试都调一次：整个 case 共用一份日志，而 observation.json 是合并写。
// 每条测试都从整个文件重算，于是最后跑的那条写下的就是完整总量
// （不依赖测试执行顺序）。
func observeFixtureRun(t *testing.T, f *Fixture, logPath string, baseline int) {
	t.Helper()
	if browserObservationDir() == "" {
		return
	}
	all := readRequestLog(t, logPath)
	if baseline > len(all) {
		t.Fatalf("请求日志比起点还短（%d < %d）", len(all), baseline)
	}
	recs := all[baseline:]
	statusCount := func(code int) int {
		n := 0
		for _, rec := range recs {
			if rec.Status == code {
				n++
			}
		}
		return n
	}
	origins := requestOrigins(recs)
	writeFixtureObservation(t, map[string]any{
		"origins":          origins,
		"requestCount":     len(all),
		"ownRequests":      len(recs),
		"okResponses":      statusCount(http.StatusOK),
		"rejected403":      statusCount(http.StatusForbidden),
		"fixturePrimary":   f.Primary.URL,
		"fixtureSecondary": f.Secondary.URL,
		"recordedBy":       "fixture 服务端 handler 追加的 fixture-requests.jsonl",
		"byTest:" + t.Name(): map[string]any{
			"ownRequests": len(recs),
			"origins":     origins,
			"okResponses": statusCount(http.StatusOK),
			"rejected403": statusCount(http.StatusForbidden),
		},
	}, nil)
}

// TestBR00FixtureRequestLogAndCounters 是 BR00 的外部观察版本：
// 两个 origin 独立、counter 可核对、trusted-only 负例确实拒非 trusted 输入，
// 三条结论都由服务端写的日志与 /__counters 快照证明。
func TestBR00FixtureRequestLogAndCounters(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "fixture-requests.jsonl")
	if dir := browserObservationDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建观察目录失败: %v", err)
		}
		logPath = filepath.Join(dir, "fixture-requests.jsonl")
	}
	f, err := newLoggingFixture(t, logPath)
	if err != nil {
		t.Fatalf("启动带请求日志的 fixture: %v", err)
	}
	// 同一个 case 的多条测试共用一份请求日志：先记下起点，后面只断言"本条测试
	// 发出的那几条"。总量（requestCount/origins）仍然从整份日志算。
	baseline := len(readRequestLog(t, logPath))
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("关闭 fixture: %v", err)
		}
	}()

	before := fetchCounters(t, f)

	// 1) primary 上一次真实表单提交。
	if res := postJSON(t, f.Primary.URL+"/__form/submit", map[string]any{"name": "br00", "confirm": true}, nil); res.status != http.StatusOK {
		t.Fatalf("表单提交应 200，实际 %d", res.status)
	}
	// 2) trusted-only 负例：缺信任证明必须 403，且只涨 rejected 计数。
	rejected := postJSON(t, f.Primary.URL+"/__trusted/click", nil, nil)
	if rejected.status != http.StatusForbidden {
		t.Fatalf("缺 X-Event-Trusted 的点击必须 403，实际 %d", rejected.status)
	}
	// 3) trusted-only 正例：带信任证明必须 200。
	accepted := postJSON(t, f.Primary.URL+"/__trusted/click", nil, map[string]string{"X-Event-Trusted": "true"})
	if accepted.status != http.StatusOK {
		t.Fatalf("带信任证明的点击应 200，实际 %d", accepted.status)
	}
	// 4) secondary 上也发一次请求：证明两个 origin 各自独立地处理请求。
	if res := get(t, f.Secondary.URL+"/__whoami", nil); res.status != http.StatusOK {
		t.Fatalf("secondary 的 whoami 应 200，实际 %d", res.status)
	}

	after := fetchCounters(t, f)

	// ── 观察值：请求日志 + 计数器快照，全部从磁盘/服务端读回 ──
	all := readRequestLog(t, logPath)
	if baseline > len(all) {
		t.Fatalf("请求日志比起点还短（%d < %d）", len(all), baseline)
	}
	recs := all[baseline:]
	origins := requestOrigins(recs)
	if len(origins) != 2 {
		t.Fatalf("请求日志里应出现两个不同 origin，实际 %v", origins)
	}
	if origins[0] == origins[1] {
		t.Fatalf("两个 origin 必须不同，实际 %v", origins)
	}
	// 每条请求都必须真的被服务端处理过（状态码非 0）。
	for _, rec := range recs {
		if rec.Status == 0 {
			t.Fatalf("请求日志里有未处理的请求: %+v", rec)
		}
	}
	// 负例的 403 必须在服务端日志里留下痕迹。
	forbidden := 0
	for _, rec := range recs {
		if rec.Path == "/__trusted/click" && rec.Status == http.StatusForbidden {
			forbidden++
		}
	}
	if forbidden != 1 {
		t.Fatalf("请求日志里应恰好 1 条 403 的 trusted-only 请求，实际 %d 条", forbidden)
	}
	// 计数器：before/after 的差值就是这几次真实写操作。
	if got := after.Primary[CounterFormSubmit] - before.Primary[CounterFormSubmit]; got != 1 {
		t.Fatalf("form-submit 计数应 +1，实际 %d（before=%d after=%d）",
			got, before.Primary[CounterFormSubmit], after.Primary[CounterFormSubmit])
	}
	if got := after.Primary[CounterTrustedRejected] - before.Primary[CounterTrustedRejected]; got != 1 {
		t.Fatalf("trusted-rejected 计数应 +1，实际 %d", got)
	}
	if got := after.Primary[CounterTrustedOnly] - before.Primary[CounterTrustedOnly]; got != 1 {
		t.Fatalf("trusted-only 计数应 +1，实际 %d", got)
	}
	// origin 独立性：secondary 上的请求不得影响 primary 的计数。
	if got := after.Secondary[CounterFormSubmit]; got != before.Secondary[CounterFormSubmit] {
		t.Fatalf("secondary 的 form-submit 不应变化（before=%d after=%d）",
			before.Secondary[CounterFormSubmit], got)
	}

	snapshot, err := json.MarshalIndent(map[string]any{
		"before":    before,
		"after":     after,
		"fetchedBy": "HTTP GET /__counters（服务端计数器，不是页面文案）",
	}, "", "  ")
	if err != nil {
		t.Fatalf("序列化 counters.json 失败: %v", err)
	}

	writeFixtureObservation(t, map[string]any{
		"origins":             origins,
		"requestCount":        len(all),
		"ownRequestCount":     len(recs),
		"countersBefore":      before.Primary,
		"countersAfter":       after.Primary,
		"secondaryBefore":     before.Secondary,
		"secondaryAfter":      after.Secondary,
		"trustedOnlyRejected": after.Primary[CounterTrustedRejected],
		"trustedOnlyAccepted": after.Primary[CounterTrustedOnly],
		"forbiddenResponses":  forbidden,
		"recordedBy":          "fixture 服务端 handler 追加的 fixture-requests.jsonl；counters.json 由 HTTP /__counters 取回",
	}, map[string][]byte{"counters.json": snapshot})
}
