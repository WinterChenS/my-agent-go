package fixtures

// C08b：五个 HTTP 平台（钉钉/飞书/Slack/Notion/GitHub）的确定性 fixture。
//
// 它与 servers.go 里的 SMTP/IMAP/OAuth 是同一套纪律：
//   - 只绑 127.0.0.1 的随机端口，Close 幂等，测试用 t.Cleanup 关闭；
//   - 副作用计数器原子写进 $ANVIL_OBSERVATION_DIR/connector-counter.json，
//     并且**每个平台各自一段**（CONN02 要求分平台核对，加总就分不出是谁干的）；
//   - 每一个收到的 HTTP 请求都进请求日志（内存 + connector-http-requests.jsonl），
//     这样"限流后被拒的那次到底有没有发出去"不靠被测代码自述，而是从日志数。
//
// 一个 server 同时模拟五个平台的 REST 形状（用 Platform 选择），
// 因为它们的差别只是路由与包封，而"计数/日志/停滞"这套观察装置的语义必须一致。

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 平台标识（与 connectors.Kind 的取值一致）。
const (
	PlatformDingtalk = "dingtalk"
	PlatformFeishu   = "feishu"
	PlatformSlack    = "slack"
	PlatformNotion   = "notion"
	PlatformGitHub   = "github"
)

// RequestLogFile 是 HTTP fixture 的请求日志文件名（观察目录内）。
const RequestLogFile = "connector-http-requests.jsonl"

// RequestRecord 是一次被 fixture 收到的 HTTP 请求。
//
// 它是 CONN02/CONN06 的外部证据：读动作可以被数，写动作可以被数，
// "被限流所以没有发出去"也能被数成 0。
type RequestRecord struct {
	At       string `json:"at"`
	Platform string `json:"platform"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Query    string `json:"query,omitempty"`
	Status   int    `json:"status"`
	// WriteApplied 表示这次请求真的产生了平台侧写副作用。
	WriteApplied bool `json:"writeApplied"`
}

// WriteRecord 是一次已应用的写（内容断言用）。
type WriteRecord struct {
	At       string `json:"at"`
	Platform string `json:"platform"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Text     string `json:"text,omitempty"`
}

// APIOptions 是 HTTP fixture 的配置。
type APIOptions struct {
	// Platform 选择路由形状：dingtalk/feishu/slack/notion/github。
	Platform string
	// ObservationDir 覆盖 $ANVIL_OBSERVATION_DIR。
	ObservationDir string
	// Counter 是共享计数器（与 SMTP/IMAP 写同一份快照）。
	Counter *Counter
	// PageSize 是单页记录数（默认 2，便于用两页证明游标行为）。
	PageSize int
	// AppKey / AppSecret 是钉钉/飞书唯一接受的应用注册凭据。
	AppKey    string
	AppSecret string
	// Token 是 Slack/Notion/GitHub 唯一接受的 bearer 令牌。
	Token string
	// StallAfterApply 让写请求在**应用副作用之后**不再回复：
	// 客户端只会看到超时——这正是 OutcomeUnknown 的现实来源。
	StallAfterApply bool
}

func (o APIOptions) platform() string {
	p := strings.ToLower(strings.TrimSpace(o.Platform))
	if p == "" {
		return PlatformGitHub
	}
	return p
}

func (o APIOptions) pageSize() int {
	if o.PageSize <= 0 {
		return 2
	}
	return o.PageSize
}

func (o APIOptions) counter() *Counter {
	if o.Counter != nil {
		return o.Counter
	}
	return NewCounter(o.dir())
}

func (o APIOptions) dir() string {
	if strings.TrimSpace(o.ObservationDir) != "" {
		return strings.TrimSpace(o.ObservationDir)
	}
	return ObservationDir()
}

// API 是五个平台的 HTTP fixture。
type API struct {
	ln      net.Listener
	srv     *http.Server
	opts    APIOptions
	counter *Counter
	done    chan struct{}

	mu       sync.Mutex
	requests []RequestRecord
	writes   []WriteRecord
	tokens   map[string]bool
	docs     map[string]string
	docRev   int
	pages    map[string]string
	issues   map[int]string
	closed   bool
}

// NewAPI 在回环随机端口上启动 fixture。
func NewAPI(opts APIOptions) (*API, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fixtures: HTTP 监听失败: %w", err)
	}
	a := &API{
		ln:      ln,
		opts:    opts,
		counter: opts.counter(),
		done:    make(chan struct{}),
		tokens:  map[string]bool{},
		docs:    map[string]string{"doc-fixture": "第一段\n第二段\n第三段\n第四段\n第五段"},
		docRev:  1,
		pages:   map[string]string{"page-1": "页面 1", "page-2": "页面 2"},
		issues:  map[int]string{1: "初始议题一", 2: "初始议题二"},
	}
	if tok := strings.TrimSpace(opts.Token); tok != "" {
		a.tokens[tok] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handle)
	a.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// 写超时必须长于测试里的 ctx 截止时间，否则"客户端超时"会先变成服务端超时。
		WriteTimeout: 60 * time.Second,
	}
	go func() {
		if err := a.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			_ = err
		}
	}()
	return a, nil
}

// Addr 返回 host:port。
func (a *API) Addr() string { return a.ln.Addr().String() }

// BaseURL 返回 http://host:port。
func (a *API) BaseURL() string { return "http://" + a.Addr() }

// Counter 返回共享计数器。
func (a *API) Counter() *Counter { return a.counter }

// Requests 返回收到的 HTTP 请求（按到达顺序）。
func (a *API) Requests() []RequestRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]RequestRecord, len(a.requests))
	copy(out, a.requests)
	return out
}

// RequestsFor 返回命中某路径的请求数（限流/重放断言的精确口径）。
func (a *API) RequestsFor(path string) []RequestRecord {
	out := []RequestRecord{}
	for _, rec := range a.Requests() {
		if rec.Path == path {
			out = append(out, rec)
		}
	}
	return out
}

// Writes 返回已应用的写（内容断言用）。
func (a *API) Writes() []WriteRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]WriteRecord, len(a.writes))
	copy(out, a.writes)
	return out
}

// Close 关闭服务器（幂等）。
func (a *API) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	close(a.done)
	err := a.srv.Close()
	_ = a.counter.Persist()
	return err
}

// ---------------------------------------------------------------------------
// 请求处理
// ---------------------------------------------------------------------------

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 || s.status == http.StatusOK {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (a *API) handle(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w}
	// 请求在**进入处理前**就记下：写请求可能因为 StallAfterApply 卡住不返回，
	// 若等到返回才计数，"平台到底收到几次"就永远看不到 1。
	a.mu.Lock()
	a.requests = append(a.requests, RequestRecord{
		At:       time.Now().UTC().Format(time.RFC3339Nano),
		Platform: a.opts.platform(),
		Method:   r.Method,
		Path:     r.URL.Path,
		Query:    r.URL.RawQuery,
	})
	idx := len(a.requests) - 1
	a.mu.Unlock()
	a.counter.Update(func(sn *Snapshot) { sn.HTTPRequests++ })

	applied := a.serve(sw, r)
	status := sw.status
	if status == 0 {
		status = http.StatusOK
	}
	a.mu.Lock()
	a.requests[idx].Status = status
	a.requests[idx].WriteApplied = applied
	rec := a.requests[idx]
	a.mu.Unlock()
	if dir := a.opts.dir(); dir != "" {
		if line, err := json.Marshal(rec); err == nil {
			appendLine(filepath.Join(dir, RequestLogFile), line)
		}
	}
}

func (a *API) serve(w http.ResponseWriter, r *http.Request) bool {
	switch a.opts.platform() {
	case PlatformDingtalk:
		return a.serveDingtalk(w, r)
	case PlatformFeishu:
		return a.serveFeishu(w, r)
	case PlatformSlack:
		return a.serveSlack(w, r)
	case PlatformNotion:
		return a.serveNotion(w, r)
	case PlatformGitHub:
		return a.serveGitHub(w, r)
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "unsupported_platform"})
	return false
}

// maybeStall 在"副作用已经发生"之后卡住不回复。
//
// 阻塞点必须是 Close 能解开的，否则 t.Cleanup 会挂住整轮测试。
func (a *API) maybeStall() {
	if !a.opts.StallAfterApply {
		return
	}
	<-a.done
}

// apply 记录一次平台侧写副作用，并留下内容证据。
func (a *API) apply(action, target, text string, bump func(*Snapshot)) bool {
	a.mu.Lock()
	a.writes = append(a.writes, WriteRecord{
		At:       time.Now().UTC().Format(time.RFC3339Nano),
		Platform: a.opts.platform(),
		Action:   action,
		Target:   target,
		Text:     text,
	})
	a.mu.Unlock()
	a.counter.Update(bump)
	a.maybeStall()
	return true
}

// ---------------------------------------------------------------------------
// 认证与令牌
// ---------------------------------------------------------------------------

func (a *API) requireBearer(w http.ResponseWriter, r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	got := ""
	if strings.HasPrefix(header, "Bearer ") {
		got = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	a.mu.Lock()
	ok := got != "" && a.tokens[got]
	a.mu.Unlock()
	if ok {
		return true
	}
	a.counter.Update(func(sn *Snapshot) { sn.HTTPUnauthorized++ })
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error":   "invalid_token",
		"message": "fixture 拒绝该访问令牌",
	})
	return false
}

func (a *API) checkApp(key, secret string) bool {
	if strings.TrimSpace(a.opts.AppSecret) == "" {
		return true
	}
	return key == a.opts.AppKey && secret == a.opts.AppSecret
}

func (a *API) issueToken(prefix string) string {
	token := prefix + "-fixture-token"
	a.mu.Lock()
	a.tokens[token] = true
	a.mu.Unlock()
	return token
}

// ---------------------------------------------------------------------------
// 分页
// ---------------------------------------------------------------------------

// pageOf 解析游标（"dt-cursor-2" → 2）；空/无法解析表示第一页。
func pageOf(cursor string) int {
	raw := cursor
	if idx := strings.LastIndex(cursor, "-"); idx >= 0 {
		raw = cursor[idx+1:]
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return 1
}

// paginate 切出一页记录并给出下一页游标。
//
// 游标是"第几页"的美化形式：稳定、可重放、不随记录内容变化。
func paginate(records []map[string]any, cursor string, size int, prefix string) ([]map[string]any, string, bool) {
	if size <= 0 {
		size = 2
	}
	page := pageOf(cursor)
	start := (page - 1) * size
	if start > len(records) {
		start = len(records)
	}
	end := start + size
	if end > len(records) {
		end = len(records)
	}
	hasMore := end < len(records)
	next := ""
	if hasMore {
		next = prefix + strconv.Itoa(page+1)
	}
	return records[start:end], next, hasMore
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request", "message": msg})
}

func decodeBody(r *http.Request, out any) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(out)
}

// appendLine 往观察目录里追加一行（失败不影响 fixture 的协议行为）。
func appendLine(path string, line []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}
