// Package browserfixture 提供一个可重复启动、带两个独立 origin 的本地站点。
//
// B00 建它，B01–B04 和 B07 共用同一份 fixture，不复制三套预期（计划 B00 验收条）。
//
// 为什么必须是两个 origin：R06 的原生视图读不到跨域 frame，BR10 要求
// "cross-origin 明确 Unsupported 且无副作用"。只有一个 origin 的 fixture 无法
// 证明跨域真的被挡住；用假标签页也无法证明"点击没有发生"。
//
// oracle 是服务端 counter，不是页面文案：每个写操作都落到一个内存计数器，
// 测试只认 Counter()/Counters() 的读数。页面上的数字同样来自服务端，但它只是
// 给人看的，断言不依赖它，也不依赖任何 toast/提示。
//
// 每个 Origin 的计数器相互独立：写到 A 不会改变 B 的数字，这样"跨域 frame 里的
// 点击有没有穿透到另一个 origin"才是可观测的。
package browserfixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 计数器名。测试与页面都用这些名字，避免手写字符串写错后静默变成"零"。
const (
	// CounterFormSubmit 普通表单提交成功次数。
	CounterFormSubmit = "form-submit"
	// CounterSPATarget SPA 页被替换掉的那个按钮被点击的次数。
	CounterSPATarget = "spa-target"
	// CounterSPAReplaced 替换后新节点被点击的次数。
	// 与 CounterSPATarget 分开是关键：旧引用失效时两个都应为 0；
	// 如果实现"按位置重新解析"，涨的会是这个，而不是 target。
	CounterSPAReplaced = "spa-replaced"
	// CounterSameURL 同 URL 导航后旧引用被点击的次数。
	CounterSameURL = "same-url-target"
	// CounterTrustedOnly 真正被信任的事件（isTrusted）触发的点击次数。
	CounterTrustedOnly = "trusted-only"
	// CounterTrustedRejected 缺信任证明的点击请求次数（负例计数器）。
	CounterTrustedRejected = "trusted-rejected"
	// CounterLogin 登录成功次数。
	CounterLogin = "login"
	// CounterDownload 下载开始次数。
	CounterDownload = "download"
	// CounterPopupTarget window.open 打开的新窗口加载次数。
	CounterPopupTarget = "popup-target"
	// CounterSlowLoaded 慢加载页面真正返回的次数（客户端取消则不计）。
	CounterSlowLoaded = "slow-loaded"
	// CounterFrameAction 子 frame 内按钮点击次数（记在 frame 所在 origin 上）。
	CounterFrameAction = "frame-action"
	// CounterShadowClosed closed shadow root 内按钮被点击的次数。
	// 顶层脚本读不到它，任何非零值都说明有人用坐标或其他手段绕过了。
	CounterShadowClosed = "closed-shadow-action"
	// CounterShadowLight 同一页面上光 DOM 按钮的点击次数（对照组）。
	CounterShadowLight = "closed-shadow-light"
)

// 慢加载的可配置区间（基线要求 1–3 秒）。请求值会被夹进这个区间，
// 因此"传 50ms 想要快"会被夹到 1 秒——这正是"可配但有界"的断言点。
const (
	SlowMin = 1000 * time.Millisecond
	SlowMax = 3000 * time.Millisecond
	// SlowDefault 是未指定 ms 时的延迟。
	SlowDefault = 1500 * time.Millisecond
)

// 登录 fixture 的固定凭据。它们只是 fixture 数据，写进证据也不构成凭证泄漏，
// 因为它们只在 127.0.0.1 的临时端口上有效。
const (
	FixtureUser = "fixture-user"
	FixturePass = "fixture-pass"
	// CookieName 是登录成功时种下的 cookie 名。
	CookieName = "fixture_session"
)

// Fixture 是两个 origin 的容器。
type Fixture struct {
	// Primary 是主 origin：应用页面都在这里。
	Primary *Origin
	// Secondary 是第二 origin：只用于 cross-origin frame 与"写到 A 不动 B"的对照。
	Secondary *Origin
}

// Origin 是一个真实的 HTTP origin（127.0.0.1 上的内核分配端口）。
type Origin struct {
	// Name 是稳定标签（primary/secondary），只用于日志与证据。
	Name string
	// URL 形如 http://127.0.0.1:51234，没有结尾斜杠。
	URL string

	srv *http.Server

	mu       sync.Mutex
	counters map[string]int
}

// New 启动两个 origin，端口都由内核分配（listen :0），因此并行测试不会互撞。
//
// 调用方必须 Close()，否则监听套接字会泄漏到测试进程结束。
func New() (*Fixture, error) {
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
		if err := p.origin.serve(p.ln, p.other); err != nil {
			_ = a.Close()
			_ = b.Close()
			return nil, err
		}
	}
	return &Fixture{Primary: a, Secondary: b}, nil
}

// Close 关闭两个 origin。重复调用是安全的。
func (f *Fixture) Close() error {
	if f == nil {
		return nil
	}
	var errs []error
	if err := f.Primary.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := f.Secondary.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Counter 读取某个计数器。未出现过的名字返回 0（而不是"未知"）：
// "没有发生过"和"计数为 0"在断言上是同一件事。
func (o *Origin) Counter(name string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.counters[name]
}

// Counters 返回当前全部计数器的快照，用于证据留档。
func (o *Origin) Counters() map[string]int {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]int, len(o.counters))
	for k, v := range o.counters {
		out[k] = v
	}
	return out
}

// Reset 清零全部计数器。测试之间不能互相污染，所以每个用例自己开一个
// Fixture；Reset 只用于"同一测试内分阶段核对"。
func (o *Origin) Reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.counters = map[string]int{}
}

func (o *Origin) inc(name string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.counters[name]++
	return o.counters[name]
}

// Close 停止该 origin 的 HTTP 服务。
func (o *Origin) Close() error {
	if o == nil || o.srv == nil {
		return nil
	}
	srv := o.srv
	o.srv = nil
	return srv.Close()
}

func (o *Origin) serve(ln net.Listener, other string) error {
	mux := http.NewServeMux()
	o.routes(mux, other)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	o.srv = srv
	go func() {
		// 监听套接字归调用方所有；Close 时 Serve 返回 ErrServerClosed，
		// 这是正常的关停路径，不需要额外处理。
		_ = srv.Serve(ln)
	}()
	return nil
}

// routes 注册全部端点。两个 origin 提供同一套页面，但页面里注入的是各自的
// origin，因此"页面里的 origin 确实不同"可以直接从响应体断言。
func (o *Origin) routes(mux *http.ServeMux, other string) {
	mux.HandleFunc("GET /", o.page(indexPage, other))
	mux.HandleFunc("GET /form", o.page(formPage, other))
	mux.HandleFunc("GET /spa", o.page(spaPage, other))
	mux.HandleFunc("GET /same-url", o.page(sameURLPage, other))
	mux.HandleFunc("GET /cross-origin", o.page(crossOriginPage, other))
	mux.HandleFunc("GET /frame", o.page(framePage, other))
	mux.HandleFunc("GET /closed-shadow", o.page(closedShadowPage, other))
	mux.HandleFunc("GET /trusted-only", o.page(trustedOnlyPage, other))
	mux.HandleFunc("GET /login", o.page(loginPage, other))
	mux.HandleFunc("GET /private", o.handlePrivate)
	mux.HandleFunc("GET /download", o.page(downloadPage, other))
	mux.HandleFunc("GET /popup", o.page(popupPage, other))
	mux.HandleFunc("GET /popup-target", o.handlePopupTarget)

	mux.HandleFunc("POST /__form/submit", o.handleFormSubmit)
	mux.HandleFunc("POST /__spa/action", o.handleSPAAction)
	mux.HandleFunc("POST /__same-url/action", o.handleSameURLAction)
	mux.HandleFunc("POST /__trusted/click", o.handleTrustedClick)
	mux.HandleFunc("POST /__frame/action", o.handleFrameAction)
	mux.HandleFunc("POST /__shadow/action", o.handleShadowAction)
	mux.HandleFunc("POST /__login", o.handleLogin)
	mux.HandleFunc("GET /__download/report.csv", o.handleDownload)
	mux.HandleFunc("GET /slow", o.handleSlow)

	mux.HandleFunc("GET /__counters", o.handleCounters)
	mux.HandleFunc("GET /__whoami", o.handleWhoami)
	mux.HandleFunc("POST /__reset", o.handleReset)
}

// page 渲染一个页面模板，把 {{ORIGIN}} / {{OTHER}} 换成真实 origin。
func (o *Origin) page(tmpl, other string) http.HandlerFunc {
	body := strings.NewReplacer("{{ORIGIN}}", o.URL, "{{OTHER}}", other).Replace(tmpl)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(body))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- 写操作端点（counter 是唯一 oracle）----

func (o *Origin) handleFormSubmit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Confirm bool   `json:"confirm"`
		Plan    string `json:"plan"`
		Note    string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求不是合法 JSON"})
		return
	}
	n := o.inc(CounterFormSubmit)
	writeJSON(w, http.StatusOK, map[string]any{
		"count":   n,
		"name":    body.Name,
		"confirm": body.Confirm,
		"plan":    body.Plan,
		"note":    body.Note,
	})
}

func (o *Origin) handleSPAAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求不是合法 JSON"})
		return
	}
	name := CounterSPATarget
	if body.Kind == "replaced" {
		name = CounterSPAReplaced
	}
	n := o.inc(name)
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "kind": body.Kind,
		"counters": o.Counters()})
}

func (o *Origin) handleSameURLAction(w http.ResponseWriter, r *http.Request) {
	n := o.inc(CounterSameURL)
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
}

// handleTrustedClick 是 trusted-only 页面的服务端闸门。
//
// 页面侧的闸门是 event.isTrusted（合成事件连请求都不发）。这里再要求一个
// 显式信任证明头，于是"非 trusted 输入不计数"在没有浏览器的情况下也可断言：
// 缺头的请求返回 403、只增加 rejected 计数，trusted-only 保持 0。
func (o *Origin) handleTrustedClick(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Event-Trusted") != "true" {
		o.inc(CounterTrustedRejected)
		writeJSON(w, http.StatusForbidden, map[string]any{
			"accepted": o.Counter(CounterTrustedOnly),
			"rejected": o.Counter(CounterTrustedRejected),
			"error":    "缺少 X-Event-Trusted：非 trusted 事件不得计入",
		})
		return
	}
	o.inc(CounterTrustedOnly)
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": o.Counter(CounterTrustedOnly),
		"rejected": o.Counter(CounterTrustedRejected),
	})
}

func (o *Origin) handleFrameAction(w http.ResponseWriter, r *http.Request) {
	n := o.inc(CounterFrameAction)
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
}

func (o *Origin) handleShadowAction(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	name := CounterShadowClosed
	if kind == "light" {
		name = CounterShadowLight
	}
	n := o.inc(name)
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "kind": kind})
}

func (o *Origin) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单无法解析", http.StatusBadRequest)
		return
	}
	if r.PostFormValue("user") != FixtureUser || r.PostFormValue("pass") != FixturePass {
		http.Redirect(w, r, "/private", http.StatusSeeOther)
		return
	}
	o.inc(CounterLogin)
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "ok:fixture",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/private", http.StatusSeeOther)
}

// handlePrivate 用 cookie 是否存在判定登录态；这是登录态可核对的 oracle 之一
// （另一个是 CounterLogin）。
func (o *Origin) handlePrivate(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(CookieName)
	if err != nil || !strings.HasPrefix(c.Value, "ok:") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><h1>未登录（401）</h1>"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><h1>登录态有效</h1><p>cookie=%s</p>", c.Value)
}

func (o *Origin) handleDownload(w http.ResponseWriter, r *http.Request) {
	o.inc(CounterDownload)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
	_, _ = w.Write([]byte("id,name\n1,fixture\n2,browser\n"))
}

// handlePopupTarget 记录 window.open 打开的新窗口真的加载了。
//
// 注意它证明的是"新窗口被导航过"，不能证明新窗口归属哪个 session；
// 归属由 B06 的 new-window 代理用例核对。
func (o *Origin) handlePopupTarget(w http.ResponseWriter, r *http.Request) {
	o.inc(CounterPopupTarget)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>popup target</title>"+
		"<h1 id=\"popup-target\">window.open 打开的新窗口</h1><p>origin=%s</p>", o.URL)
}

// handleSlow 延迟 1–3 秒再响应，并在真正返回时计数。
//
// 客户端取消（context 结束）时不计数——否则"慢加载等待成功"会因为请求被取消
// 而看起来像成功。
func (o *Origin) handleSlow(w http.ResponseWriter, r *http.Request) {
	delay := clampSlow(r.URL.Query().Get("ms"))
	select {
	case <-time.After(delay):
	case <-r.Context().Done():
		return
	}
	n := o.inc(CounterSlowLoaded)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>slow</title>"+
		"<body data-origin=\"%s\"><h1 id=\"slow-ready\">慢加载完成</h1><p>等待 %s</p><p>count=%d</p>",
		o.URL, delay, n)
}

// clampSlow 把请求的 ms 夹进 [SlowMin, SlowMax]；缺失或非法值取 SlowDefault。
//
// 夹紧而不是拒绝：fixture 要能回答"1–3 秒可配"，同时不让测试退化成 50ms 的
// 假慢加载。
func clampSlow(raw string) time.Duration {
	if raw == "" {
		return SlowDefault
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return SlowDefault
	}
	d := time.Duration(ms) * time.Millisecond
	if d < SlowMin {
		return SlowMin
	}
	if d > SlowMax {
		return SlowMax
	}
	return d
}

// ---- 只读观测端点 ----

func (o *Origin) handleCounters(w http.ResponseWriter, r *http.Request) {
	// 只读观测允许跨 origin 读取：spike 的壳页面在自己的端口上，需要能显示两个
	// fixture origin 的计数。写端点不开 CORS。
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, o.Counters())
}

// handleWhoami 回显服务端看到的 Origin 头与自身 origin。
//
// "两个 origin 确实不同"要靠它证明：请求 A 时带上 B 的 Origin 头，
// sameOrigin 必须是 false。
func (o *Origin) handleWhoami(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	originHeader := r.Header.Get("Origin")
	cookie := ""
	if c, err := r.Cookie(CookieName); err == nil {
		cookie = c.Value
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"selfOrigin":   o.URL,
		"originHeader": originHeader,
		"sameOrigin":   originHeader == o.URL,
		"host":         r.Host,
		"cookie":       cookie,
	})
}

func (o *Origin) handleReset(w http.ResponseWriter, r *http.Request) {
	o.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}
