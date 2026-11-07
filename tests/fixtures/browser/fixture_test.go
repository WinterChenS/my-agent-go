package browserfixture

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newFixture 启动一个全新的双 origin fixture，并在测试结束时关掉。
//
// 每个用例一份：计数器是本包的内存状态，复用同一份会让"提交前 0、提交一次 1"
// 这种断言变成"上一次残留 +1"，也就是互相污染。
//
// 配置了 ANVIL_OBSERVATION_DIR 时改用"服务端请求日志"变体：每个 handler 处理完
// 一次请求就追加一行，整条 case 的请求都落在同一份 fixture-requests.jsonl 里。
// 测试结束时从**整份日志**重算观察值（最后跑的那条写下的就是完整总量）。
func newFixture(t *testing.T) *Fixture {
	t.Helper()
	logPath := ""
	if dir := browserObservationDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建观察目录失败: %v", err)
		}
		logPath = filepath.Join(dir, "fixture-requests.jsonl")
	}
	f, err := startFixture(t, logPath)
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	// 同一个 case 的多条测试共用一份请求日志：记下本条测试的起点，
	// 每条测试自己的请求才能和别的测试分得开。
	baseline := 0
	if logPath != "" {
		baseline = len(readRequestLog(t, logPath))
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("关闭 fixture: %v", err)
		}
		if logPath != "" {
			observeFixtureRun(t, f, logPath, baseline)
		}
	})
	return f
}

// startFixture 在 logPath 非空时启动带服务端请求日志的变体，否则就是原样 New()。
func startFixture(t *testing.T, logPath string) (*Fixture, error) {
	t.Helper()
	if logPath == "" {
		return New()
	}
	return newLoggingFixture(t, logPath)
}

type response struct {
	status int
	body   string
	header http.Header
}

func do(t *testing.T, req *http.Request) response {
	t.Helper()
	client := &http.Client{
		Timeout: 10 * time.Second,
		// 本 fixture 的 303 是登录流程的一部分，测试要自己看清跳转与 Set-Cookie，
		// 不能被自动跟随吞掉。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("读取响应体: %v", err)
	}
	return response{status: res.StatusCode, body: string(b), header: res.Header}
}

func get(t *testing.T, target string, headers map[string]string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(t, req)
}

func postJSON(t *testing.T, target string, payload any, headers map[string]string) response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("编码请求体: %v", err)
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(http.MethodPost, target, body)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(t, req)
}

// TestFixtureTwoDistinctOrigins 证明"两个 origin 确实不同"。
//
// 三条证据：监听端口由内核分配且不相等；服务端看到的 Origin 头与自身 origin 的
// 比较结果为 false；页面 HTML 里写死的是各自 origin。
func TestFixtureTwoDistinctOrigins(t *testing.T) {
	f := newFixture(t)

	ua, err := url.Parse(f.Primary.URL)
	if err != nil {
		t.Fatalf("解析 primary URL: %v", err)
	}
	ub, err := url.Parse(f.Secondary.URL)
	if err != nil {
		t.Fatalf("解析 secondary URL: %v", err)
	}
	t.Logf("origin A(primary)=%s", f.Primary.URL)
	t.Logf("origin B(secondary)=%s", f.Secondary.URL)
	if f.Primary.URL == f.Secondary.URL {
		t.Fatalf("两个 origin 必须是不同 URL，实际都是 %s", f.Primary.URL)
	}
	if ua.Host == ub.Host {
		t.Fatalf("两个 origin 必须监听不同端口，实际都是 %s", ua.Host)
	}
	for name, u := range map[string]*url.URL{"primary": ua, "secondary": ub} {
		if u.Hostname() != "127.0.0.1" {
			t.Errorf("%s 应只监听回环地址，实际 %s", name, u.Hostname())
		}
		if u.Port() == "" || u.Port() == "0" {
			t.Errorf("%s 应使用内核分配的真实端口，实际 %q", name, u.Port())
		}
	}

	// Origin 头核对：拿 B 的 origin 去请求 A，A 必须判为跨域。
	whoA := get(t, f.Primary.URL+"/__whoami", map[string]string{"Origin": f.Secondary.URL})
	var infoA struct {
		SelfOrigin   string `json:"selfOrigin"`
		OriginHeader string `json:"originHeader"`
		SameOrigin   bool   `json:"sameOrigin"`
		Host         string `json:"host"`
	}
	if err := json.Unmarshal([]byte(whoA.body), &infoA); err != nil {
		t.Fatalf("解析 whoami: %v\n%s", err, whoA.body)
	}
	if infoA.SelfOrigin != f.Primary.URL || infoA.OriginHeader != f.Secondary.URL {
		t.Fatalf("whoami 回显不符: %+v", infoA)
	}
	if infoA.SameOrigin {
		t.Fatalf("A 收到 B 的 Origin 头不应判为同源: %+v", infoA)
	}
	if infoA.Host != ua.Host {
		t.Fatalf("Host 头不符: %q vs %q", infoA.Host, ua.Host)
	}

	whoA2 := get(t, f.Primary.URL+"/__whoami", map[string]string{"Origin": f.Primary.URL})
	if !strings.Contains(whoA2.body, `"sameOrigin":true`) {
		t.Fatalf("同 origin 请求应判为同源: %s", whoA2.body)
	}

	// 页面 HTML 里注入的是各自 origin：这是"页面里的 origin 也不同"的可离线断言。
	pageA := get(t, f.Primary.URL+"/", nil).body
	if !strings.Contains(pageA, `data-origin="`+f.Primary.URL+`"`) {
		t.Fatalf("primary 首页应写死自己的 origin\n%s", pageA)
	}
	if strings.Contains(pageA, `data-origin="`+f.Secondary.URL+`"`) {
		t.Fatalf("primary 首页不能声称自己属于 secondary origin")
	}
	// 首页会**引用**另一个 origin（用于导航与对照），但不属于它。
	if !strings.Contains(pageA, `data-other="`+f.Secondary.URL+`"`) {
		t.Fatalf("primary 首页应把 secondary 标为另一个 origin")
	}
	pageB := get(t, f.Secondary.URL+"/frame", nil).body
	if !strings.Contains(pageB, `data-origin="`+f.Secondary.URL+`"`) {
		t.Fatalf("secondary 的 frame 页应写死自己的 origin\n%s", pageB)
	}

	// cross-origin 页必须真的把 iframe 指到另一个 origin。
	crossPage := get(t, f.Primary.URL+"/cross-origin", nil).body
	if !strings.Contains(crossPage, "src=\""+f.Secondary.URL+"/frame\"") {
		t.Fatalf("cross-origin 页的 iframe 必须指向 secondary origin\n%s", crossPage)
	}
	// frame 内容只能由 secondary 提供。
	if res := get(t, f.Primary.URL+"/frame", nil); !strings.Contains(res.body, `data-origin="`+f.Primary.URL+`"`) {
		t.Fatalf("primary 也提供 /frame，内容应带 primary 的 origin")
	}
}

// TestCounterSemantics 是 fixture 的核心 oracle 测试：
// 提交前 0，提交一次 1，且写到 A 不会改变 B。
func TestCounterSemantics(t *testing.T) {
	f := newFixture(t)

	if got := f.Primary.Counter(CounterFormSubmit); got != 0 {
		t.Fatalf("新 fixture 的 form-submit 应为 0，实际 %d", got)
	}
	t.Logf("提交前 A.form-submit=%d B.form-submit=%d",
		f.Primary.Counter(CounterFormSubmit), f.Secondary.Counter(CounterFormSubmit))

	first := postJSON(t, f.Primary.URL+"/__form/submit", map[string]any{
		"name": "张三", "confirm": true, "plan": "pro", "note": "第一次",
	}, nil)
	if first.status != http.StatusOK {
		t.Fatalf("首次提交应 200，实际 %d %s", first.status, first.body)
	}
	if got := f.Primary.Counter(CounterFormSubmit); got != 1 {
		t.Fatalf("提交一次后 form-submit 应为 1，实际 %d", got)
	}
	t.Logf("提交一次后 A.form-submit=%d B.form-submit=%d",
		f.Primary.Counter(CounterFormSubmit), f.Secondary.Counter(CounterFormSubmit))

	second := postJSON(t, f.Primary.URL+"/__form/submit", map[string]any{
		"name": "李四", "confirm": false, "plan": "basic", "note": "第二次",
	}, nil)
	if second.status != http.StatusOK {
		t.Fatalf("二次提交应 200，实际 %d", second.status)
	}
	if got := f.Primary.Counter(CounterFormSubmit); got != 2 {
		t.Fatalf("提交两次后 form-submit 应为 2，实际 %d", got)
	}

	// 服务端回显必须包含提交内容，便于把 counter 与"哪一次提交"对上。
	if !strings.Contains(second.body, "李四") {
		t.Fatalf("响应体应回显提交内容: %s", second.body)
	}

	// 两个 origin 的计数器独立：primary 的两次提交不能影响 secondary。
	if got := f.Secondary.Counter(CounterFormSubmit); got != 0 {
		t.Fatalf("primary 的提交不应改变 secondary 的计数器，实际 %d", got)
	}

	// 通过 HTTP 读到的计数必须与内存读数一致（页面显示用的就是这个端点）。
	counters := get(t, f.Primary.URL+"/__counters", nil)
	var httpCounters map[string]int
	if err := json.Unmarshal([]byte(counters.body), &httpCounters); err != nil {
		t.Fatalf("解析 /__counters: %v", err)
	}
	if httpCounters[CounterFormSubmit] != 2 {
		t.Fatalf("/__counters 与内存读数不一致: %v", httpCounters)
	}

	// 非 JSON 请求体必须被拒绝且不计数：坏请求不能悄悄算一次成功。
	bad := postJSON(t, f.Primary.URL+"/__form/submit", nil, map[string]string{"Content-Type": "application/json"})
	if bad.status == http.StatusOK {
		t.Fatal("空请求体不应返回 200")
	}
	if got := f.Primary.Counter(CounterFormSubmit); got != 2 {
		t.Fatalf("被拒绝的请求不应计数，实际 %d", got)
	}

	// Reset 是测试内分阶段核对用的，不是跨用例隔离手段。
	if res := postJSON(t, f.Primary.URL+"/__reset", nil, nil); res.status != http.StatusOK {
		t.Fatalf("/__reset 应 200，实际 %d", res.status)
	}
	if got := f.Primary.Counter(CounterFormSubmit); got != 0 {
		t.Fatalf("Reset 后应为 0，实际 %d", got)
	}
}

// TestSPACountersDistinguishTargetAndReplacement 证明"旧引用指向的节点被断开"
// 这件事有可区分的 oracle。
//
// 两个计数器分开：旧引用失效 -> 都没涨；实现按位置重新解析 -> 涨的是 replaced。
// 只有一个计数器时无法区分"正确拒绝"和"点错了地方"。
func TestSPACountersDistinguishTargetAndReplacement(t *testing.T) {
	f := newFixture(t)

	page := get(t, f.Primary.URL+"/spa", nil).body
	// 新节点是运行时创建的，所以只断言它的 id 出现在脚本里，而不是 HTML 里。
	if !strings.Contains(page, `id="spa-target"`) || !strings.Contains(page, "spa-target-replaced") {
		t.Fatalf("SPA 页必须同时声明旧节点与新节点\n%s", page)
	}

	if res := postJSON(t, f.Primary.URL+"/__spa/action", map[string]any{"kind": "target"}, nil); res.status != http.StatusOK {
		t.Fatalf("点击旧节点应 200，实际 %d", res.status)
	}
	if got := f.Primary.Counter(CounterSPATarget); got != 1 {
		t.Fatalf("spa-target 应为 1，实际 %d", got)
	}
	if got := f.Primary.Counter(CounterSPAReplaced); got != 0 {
		t.Fatalf("spa-replaced 不应被带动，实际 %d", got)
	}

	if res := postJSON(t, f.Primary.URL+"/__spa/action", map[string]any{"kind": "replaced"}, nil); res.status != http.StatusOK {
		t.Fatalf("点击新节点应 200，实际 %d", res.status)
	}
	if got := f.Primary.Counter(CounterSPAReplaced); got != 1 {
		t.Fatalf("spa-replaced 应为 1，实际 %d", got)
	}
	if got := f.Primary.Counter(CounterSPATarget); got != 1 {
		t.Fatalf("spa-target 不应再增长，实际 %d", got)
	}
}

// TestTrustedOnlyRejectsUntrustedInput 是 trusted-only 的负例。
//
// 两层闸门：页面侧 event.isTrusted（合成事件不发请求），服务端侧要求
// X-Event-Trusted 证明。这里断言服务端闸门——不浏览器化就能证明
// "非 trusted 输入不计数"，而不是只在页面上写一句提示。
func TestTrustedOnlyRejectsUntrustedInput(t *testing.T) {
	f := newFixture(t)

	page := get(t, f.Primary.URL+"/trusted-only", nil).body
	if !strings.Contains(page, "isTrusted") {
		t.Fatalf("trusted-only 页面必须用 event.isTrusted 作为页面侧闸门\n%s", page)
	}
	if !strings.Contains(page, "X-Event-Trusted") {
		t.Fatalf("trusted-only 页面必须带信任证明头\n%s", page)
	}

	// 负例：没有信任证明（脚本合成点击会走到这一步）。
	denied := postJSON(t, f.Primary.URL+"/__trusted/click", nil, nil)
	if denied.status != http.StatusForbidden {
		t.Fatalf("缺 X-Event-Trusted 应 403，实际 %d %s", denied.status, denied.body)
	}
	t.Logf("缺 X-Event-Trusted: status=%d trusted-only=%d trusted-rejected=%d",
		denied.status, f.Primary.Counter(CounterTrustedOnly), f.Primary.Counter(CounterTrustedRejected))
	if got := f.Primary.Counter(CounterTrustedOnly); got != 0 {
		t.Fatalf("非 trusted 输入不得计数，trusted-only=%d", got)
	}
	if got := f.Primary.Counter(CounterTrustedRejected); got != 1 {
		t.Fatalf("被拒绝的尝试应记录 1 次，实际 %d", got)
	}

	// 负例：头存在但值不对，同样不能计数（不能用"有头就算"糊过去）。
	spoofed := postJSON(t, f.Primary.URL+"/__trusted/click", nil, map[string]string{"X-Event-Trusted": "1"})
	if spoofed.status != http.StatusForbidden {
		t.Fatalf("X-Event-Trusted=1 不等于 true，应 403，实际 %d", spoofed.status)
	}
	if got := f.Primary.Counter(CounterTrustedOnly); got != 0 {
		t.Fatalf("伪造的信任证明不得计数，trusted-only=%d", got)
	}

	// 正例：真实 trusted 事件（页面侧只有 isTrusted 才会带这个头）。
	accepted := postJSON(t, f.Primary.URL+"/__trusted/click", nil, map[string]string{"X-Event-Trusted": "true"})
	if accepted.status != http.StatusOK {
		t.Fatalf("带信任证明应 200，实际 %d %s", accepted.status, accepted.body)
	}
	if got := f.Primary.Counter(CounterTrustedOnly); got != 1 {
		t.Fatalf("trusted 输入应计数 1，实际 %d", got)
	}
	t.Logf("带 X-Event-Trusted=true: status=%d trusted-only=%d trusted-rejected=%d",
		accepted.status, f.Primary.Counter(CounterTrustedOnly), f.Primary.Counter(CounterTrustedRejected))
	if got := f.Primary.Counter(CounterTrustedRejected); got != 2 {
		t.Fatalf("拒绝计数应保持 2（不被接受路径重置），实际 %d", got)
	}

	// 另一 origin 完全不受影响。
	if got := f.Secondary.Counter(CounterTrustedOnly); got != 0 {
		t.Fatalf("secondary 的 trusted-only 不应变化，实际 %d", got)
	}
}

// TestSlowLoadIsWaitable 证明慢加载真的会等，且延迟被夹在 1–3 秒。
func TestSlowLoadIsWaitable(t *testing.T) {
	f := newFixture(t)

	// 故意传 1ms：夹具必须把它夹到 1 秒，否则"慢加载"只是一个标签。
	start := time.Now()
	res := get(t, f.Primary.URL+"/slow?ms=1", nil)
	elapsed := time.Since(start)

	if res.status != http.StatusOK {
		t.Fatalf("慢加载应 200，实际 %d", res.status)
	}
	if !strings.Contains(res.body, "slow-ready") {
		t.Fatalf("慢加载响应体不符: %s", res.body)
	}
	if elapsed < SlowMin-50*time.Millisecond {
		t.Fatalf("ms=1 应被夹到 %s，实际只用了 %s", SlowMin, elapsed)
	}
	if got := f.Primary.Counter(CounterSlowLoaded); got != 1 {
		t.Fatalf("完成的慢加载应计数 1，实际 %d", got)
	}

	// 夹紧规则逐项核对（不必真的等 3 秒）。
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", SlowDefault},
		{"abc", SlowDefault},
		{"-5", SlowDefault},
		{"0", SlowDefault},
		{"1", SlowMin},
		{"500", SlowMin},
		{"1500", 1500 * time.Millisecond},
		{"3000", SlowMax},
		{"10000", SlowMax},
	}
	for _, c := range cases {
		if got := clampSlow(c.raw); got != c.want {
			t.Errorf("clampSlow(%q) = %s，期望 %s", c.raw, got, c.want)
		}
	}
}

// TestLoginCookieIsServerSideOracle 用 cookie 与 counter 两处证据核对登录。
func TestLoginCookieIsServerSideOracle(t *testing.T) {
	f := newFixture(t)

	if res := get(t, f.Primary.URL+"/private", nil); res.status != http.StatusUnauthorized {
		t.Fatalf("未登录访问受保护页面应 401，实际 %d", res.status)
	}

	form := url.Values{"user": {FixtureUser}, "pass": {FixturePass}}

	req, err := http.NewRequest(http.MethodPost, f.Primary.URL+"/__login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("构造登录请求: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login := do(t, req)
	if login.status != http.StatusSeeOther {
		t.Fatalf("登录成功应 303 跳转，实际 %d %s", login.status, login.body)
	}
	cookies := login.header.Values("Set-Cookie")
	if len(cookies) == 0 {
		t.Fatal("登录成功必须下发 Set-Cookie")
	}
	if !strings.Contains(cookies[0], CookieName+"=") || !strings.Contains(cookies[0], "HttpOnly") {
		t.Fatalf("cookie 名/HttpOnly 不符: %v", cookies)
	}
	if got := f.Primary.Counter(CounterLogin); got != 1 {
		t.Fatalf("登录成功后 counter=1，实际 %d", got)
	}

	cookieValue := strings.SplitN(strings.TrimPrefix(cookies[0], CookieName+"="), ";", 2)[0]
	private := get(t, f.Primary.URL+"/private", map[string]string{"Cookie": CookieName + "=" + cookieValue})
	if private.status != http.StatusOK {
		t.Fatalf("带 cookie 访问应 200，实际 %d %s", private.status, private.body)
	}

	// 错误凭据：不种 cookie、不计数。
	badForm := url.Values{"user": {FixtureUser}, "pass": {"wrong"}}
	badReq, err := http.NewRequest(http.MethodPost, f.Primary.URL+"/__login", strings.NewReader(badForm.Encode()))
	if err != nil {
		t.Fatalf("构造登录请求: %v", err)
	}
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bad := do(t, badReq)
	if len(bad.header.Values("Set-Cookie")) != 0 {
		t.Fatalf("错误凭据不应种 cookie，实际 %v", bad.header.Values("Set-Cookie"))
	}
	if got := f.Primary.Counter(CounterLogin); got != 1 {
		t.Fatalf("错误凭据不应增加 login 计数，实际 %d", got)
	}
}

// TestDownloadAndPopupHaveServerSideEvidence 覆盖下载与新窗口两个"外部副作用"。
func TestDownloadAndPopupHaveServerSideEvidence(t *testing.T) {
	f := newFixture(t)

	dl := get(t, f.Primary.URL+"/__download/report.csv", nil)
	if dl.status != http.StatusOK {
		t.Fatalf("下载应 200，实际 %d", dl.status)
	}
	disp := dl.header.Get("Content-Disposition")
	if !strings.HasPrefix(disp, "attachment") || !strings.Contains(disp, "report.csv") {
		t.Fatalf("下载必须带 attachment filename，实际 %q", disp)
	}
	if got := f.Primary.Counter(CounterDownload); got != 1 {
		t.Fatalf("下载开始应计数 1，实际 %d", got)
	}

	popupPage := get(t, f.Primary.URL+"/popup", nil).body
	if !strings.Contains(popupPage, "window.open('/popup-target'") {
		t.Fatalf("popup 页必须真的调用 window.open\n%s", popupPage)
	}
	if got := f.Primary.Counter(CounterPopupTarget); got != 0 {
		t.Fatalf("尚未打开新窗口时 popup-target 应为 0，实际 %d", got)
	}
	if res := get(t, f.Primary.URL+"/popup-target", nil); res.status != http.StatusOK {
		t.Fatalf("popup 目标页应 200，实际 %d", res.status)
	}
	if got := f.Primary.Counter(CounterPopupTarget); got != 1 {
		t.Fatalf("新窗口加载后 popup-target 应为 1，实际 %d", got)
	}
}

// TestClosedShadowAndFramePagesDeclareTheHardParts 记录"页面上确实存在难点"。
//
// 这一条只做静态断言——它证明 fixture 真的提供了 closed shadow 与 cross-origin
// frame，而不是在页面上写一段注释。行为断言由 B01–B04 的原生用例执行，
// 本轮没有浏览器，不能把静态断言冒充成行为通过。
func TestClosedShadowAndFramePagesDeclareTheHardParts(t *testing.T) {
	f := newFixture(t)

	shadow := get(t, f.Primary.URL+"/closed-shadow", nil).body
	if !strings.Contains(shadow, "attachShadow({ mode: 'closed' })") {
		t.Fatalf("closed-shadow 页必须真的创建 closed shadow root\n%s", shadow)
	}
	if strings.Contains(shadow, "shadowRoot") {
		t.Fatalf("closed-shadow 页不应暴露 shadowRoot 引用（那样就不是 closed 了）")
	}
	// 两个计数器分开：closed 内的点击与光 DOM 的点击不能混在一起。
	postJSON(t, f.Primary.URL+"/__shadow/action?kind=closed", nil, nil)
	postJSON(t, f.Primary.URL+"/__shadow/action?kind=light", nil, nil)
	if got := f.Primary.Counter(CounterShadowClosed); got != 1 {
		t.Fatalf("closed 计数应为 1，实际 %d", got)
	}
	if got := f.Primary.Counter(CounterShadowLight); got != 1 {
		t.Fatalf("light 计数应为 1，实际 %d", got)
	}

	frame := get(t, f.Primary.URL+"/frame", nil).body
	if !strings.Contains(frame, `id="frame-target"`) {
		t.Fatalf("frame 页必须有可点击元素\n%s", frame)
	}
	if res := postJSON(t, f.Primary.URL+"/__frame/action", nil, nil); res.status != http.StatusOK {
		t.Fatalf("frame 内点击应 200，实际 %d", res.status)
	}
	if got := f.Primary.Counter(CounterFrameAction); got != 1 {
		t.Fatalf("frame-action 应为 1，实际 %d", got)
	}
}

// TestFixtureCloseReleasesPorts 证明 Close 真的关掉了监听，用例之间不会互相干扰。
func TestFixtureCloseReleasesPorts(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("启动 fixture: %v", err)
	}
	addr := f.Primary.URL
	if res := get(t, addr+"/__counters", nil); res.status != http.StatusOK {
		t.Fatalf("关闭前应可访问，实际 %d", res.status)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭 fixture: %v", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(addr + "/__counters")
	if err == nil {
		_ = res.Body.Close()
		t.Fatalf("关闭后不应还能访问 %s（实际状态 %d）", addr, res.StatusCode)
	}

	// 关两次必须安全：Cleanup 与显式 Close 可能同时发生。
	if err := f.Close(); err != nil {
		t.Fatalf("重复 Close 应安全: %v", err)
	}
}

// TestEveryPageIsServedWithItsOwnOrigin 兜住"页面链接指向 404"这类低级错误。
//
// 每个页面都必须 200 且响应体里带自己的 origin；/download 的链接必须指向真实端点。
// 这一条不做行为断言，只保证 fixture 本身是完整的——否则 B01 跑到某个页面才发现
// 404，会把"夹具缺页"误读成"原生视图打不开"。
func TestEveryPageIsServedWithItsOwnOrigin(t *testing.T) {
	f := newFixture(t)

	pages := []string{
		"/", "/form", "/spa", "/same-url", "/cross-origin", "/frame",
		"/closed-shadow", "/trusted-only", "/login", "/download", "/popup",
		"/slow?ms=1000", "/noise",
		"/huge?nodes=400&labelLen=6&textChars=200",
	}
	for _, p := range pages {
		res := get(t, f.Primary.URL+p, nil)
		if res.status != http.StatusOK {
			t.Errorf("%s 应 200，实际 %d", p, res.status)
			continue
		}
		if !strings.Contains(res.body, `data-origin="`+f.Primary.URL+`"`) {
			t.Errorf("%s 的响应体没有带自己的 origin", p)
		}
	}

	dl := get(t, f.Primary.URL+"/download", nil)
	if !strings.Contains(dl.body, "/__download/report.csv") {
		t.Fatalf("/download 必须链到真实下载端点\n%s", dl.body)
	}
}
