//go:build windows

package nativetest

// U07 的驱动层：在**构建后的 Wails 应用**里读页面事实。
//
// 为什么不是 Playwright：Playwright 渲染的是同一份 React，但没有 Wails 宿主、
// 没有真实 WebView2、没有窗口/DPI/显示会话，也没有 Go API 背后的状态。前端
// 组件通过不等于 native 应用工作（master plan 的原话）。Playwright 只做预检。
//
// 通道：主窗口 WebView2 的 DevTools 协议（cmd/myagent 的 ANVIL_UI_CDP_PORT）。
// 它给的是**应用进程自己渲染出来的页面**：布局、可访问性树、计算样式、像素截图。
// 补充的外部事实来自 Win32（窗口矩形、DPI、进程）与磁盘（config/SQLite/sha256）。
//
// 纪律：
//   - 页面事实只从 CDP 读，不从内存变量"假装"；
//   - 窗口几何只从 GetWindowRect/GetClientRect/GetDpiForWindow 读；
//   - 截图落盘，证据里的 hash 由磁盘字节重新算出（见 observation_u07_test.go）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/coder/websocket"
	"golang.org/x/sys/windows"
)

// ---------- CDP 客户端 ----------

// cdpClient 是 DevTools 协议的最小客户端：一条连接、按 id 配对响应。
//
// 只实现需要的部分（发送命令、等回包、跳过事件），但**不**吞掉错误：
// 协议级 error 直接返回，调用方必须处理——截图失败被当成"空截图"是最容易
// 让人误判"页面没问题"的那类 bug。
type cdpClient struct {
	conn    *websocket.Conn
	ctx     context.Context
	mu      sync.Mutex
	nextID  int
	readMu  sync.Mutex
	pending map[int]chan cdpEnvelope
	closed  chan struct{}
}

type cdpEnvelope struct {
	ID     int              `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
	Result *json.RawMessage `json:"result"`
	Error  *cdpError        `json:"error"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("CDP error %d: %s", e.Code, e.Message) }

// cdpTarget 是 /json/list 里的一条可连接页面。
type cdpTarget struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// waitCDPTargets 轮询 http://127.0.0.1:<port>/json/list，直到出现页面或超时。
func waitCDPTargets(ctx context.Context, port int, deadline time.Duration) ([]cdpTarget, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	end := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(end) {
		targets, err := httpJSONTargets(ctx, url)
		if err == nil && len(targets) > 0 {
			return targets, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("超时未出现 CDP 目标")
	}
	return nil, lastErr
}

func httpJSONTargets(ctx context.Context, url string) ([]cdpTarget, error) {
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// 用 net/http 的默认客户端：端口在本机，且 WebView2 的 /json/list 不需要鉴权。
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("/json/list 返回 %d", res.StatusCode)
	}
	var targets []cdpTarget
	if err := json.NewDecoder(res.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// dialCDP 连上一条页面级 DevTools 连接并启动读循环。
func dialCDP(ctx context.Context, wsURL string) (*cdpClient, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	// 截图 base64 可以到几 MB；默认读限太小会直接把连接打断。
	conn.SetReadLimit(64 << 20)
	c := &cdpClient{
		conn:    conn,
		ctx:     context.Background(),
		pending: map[int]chan cdpEnvelope{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *cdpClient) readLoop() {
	defer close(c.closed)
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			c.mu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var env cdpEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.ID == 0 {
			continue // 事件，不参与请求配对
		}
		c.mu.Lock()
		ch, ok := c.pending[env.ID]
		if ok {
			delete(c.pending, env.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- env
		}
	}
}

// Call 发一条 CDP 命令并等它的结果。
func (c *cdpClient) Call(method, params string, timeout time.Duration) (json.RawMessage, error) {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan cdpEnvelope, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	payload := fmt.Sprintf(`{"id":%d,"method":%q,"params":%s}`, id, method, params)
	wctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	if err := c.conn.Write(wctx, websocket.MessageText, []byte(payload)); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case env, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s: 连接已关闭", method)
		}
		if env.Error != nil {
			return nil, env.Error
		}
		if env.Result == nil {
			return nil, fmt.Errorf("%s: 响应缺少 result", method)
		}
		return *env.Result, nil
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: %s 内没有响应", method, timeout)
	}
}

// Eval 在页面主世界求值并返回 JSON 值（returnByValue）。
func (c *cdpClient) Eval(expr string) (json.RawMessage, error) {
	params, err := json.Marshal(map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return nil, err
	}
	raw, err := c.Call("Runtime.evaluate", string(params), 30*time.Second)
	if err != nil {
		return nil, err
	}
	var res struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.ExceptionDetails != nil {
		return nil, fmt.Errorf("页面求值抛出异常: %s %s", res.ExceptionDetails.Text, res.ExceptionDetails.Exception.Description)
	}
	if len(res.Result.Value) == 0 {
		// undefined（无返回值）：给调用方一个明确的 null，而不是空切片。
		return json.RawMessage("null"), nil
	}
	return res.Result.Value, nil
}

// EvalString 求值一个返回字符串的表达式。
func (c *cdpClient) EvalString(expr string) (string, error) {
	raw, err := c.Eval(expr)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("表达式结果不是字符串: %s", string(raw))
	}
	return s, nil
}

// EvalJSON 求值一个返回 JSON 字符串的表达式并解码到 out。
// 页面里的对象一律先 JSON.stringify：CDP 的 returnByValue 对复杂对象能省一次
// 序列化，但拿到的 RawMessage 在测试里解码更容易对不上字段名，字符串更稳。
func (c *cdpClient) EvalJSON(expr string, out any) error {
	s, err := c.EvalString(expr)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("页面返回的不是合法 JSON: %v（前 200 字节: %s）", err, head(s, 200))
	}
	return nil
}

// CaptureScreenshot 返回 PNG 字节。
func (c *cdpClient) CaptureScreenshot(clip string) ([]byte, error) {
	params := `{"format":"png","captureBeyondViewport":false}`
	if clip != "" {
		params = fmt.Sprintf(`{"format":"png","captureBeyondViewport":false,"clip":%s}`, clip)
	}
	raw, err := c.Call("Page.captureScreenshot", params, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var res struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.Data == "" {
		return nil, fmt.Errorf("截图返回空数据")
	}
	decoded, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 100 {
		return nil, fmt.Errorf("截图像素过少（%d 字节），不是有效 PNG", len(decoded))
	}
	return decoded, nil
}

// axNode 是 Accessibility.getFullAXTree 的一个节点（只取需要的字段）。
type axNode struct {
	NodeID  string `json:"nodeId"`
	Ignored bool   `json:"ignored"`
	Role    struct {
		Value string `json:"value"`
	} `json:"role"`
	Name struct {
		Value string `json:"value"`
	} `json:"name"`
	Properties []struct {
		Name  string `json:"name"`
		Value struct {
			Type  string `json:"type"`
			Value any    `json:"value"`
		} `json:"value"`
	} `json:"properties"`
}

// AXTree 拉取真实渲染器的可访问性树。
//
// 依赖 ANVIL_UI_A11Y=1（cmd/myagent 的 --force-renderer-accessibility）：
// 没有它 WebView2 只给一个空的 "Chrome Legacy Window"，那时不应该悄悄跳过，
// 而应该在没有节点时直接失败——空树不是"没有无障碍问题"。
func (c *cdpClient) AXTree() ([]axNode, error) {
	if _, err := c.Call("Accessibility.enable", `{}`, 10*time.Second); err != nil {
		return nil, err
	}
	raw, err := c.Call("Accessibility.getFullAXTree", `{"depth":32}`, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var res struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Nodes, nil
}

// Close 关掉连接。
func (c *cdpClient) Close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

// ---------- 构建后的 Wails 应用 ----------

// u07Build 是一次运行内共享的构建产物（8 条 case 各跑一次 go test，但同一进程内
// 只构建一次：npm build 4s + go build 增量 3s，重复 8 次没有意义）。
type u07Build struct {
	once sync.Once
	dir  string
	err  error
	log  []string
}

var u07SharedBuild u07Build

// buildU07App 构建 frontend/dist 与真实应用可执行文件。
//
// 硬性要求：
//   - npm 用基线 Node（PATH 首位放 D:\Program Files\nodejs，AGENTS.md §4）；
//   - 构建失败必须让用例失败，不能拿一份旧的二进制继续跑（那会把"代码变了但
//     没重新构建"变成一条通过）。
func buildU07App(t *testing.T) (string, string) {
	t.Helper()
	u07SharedBuild.once.Do(func() {
		root, err := repoRoot()
		if err != nil {
			u07SharedBuild.err = err
			return
		}
		dir, err := os.MkdirTemp("", "anvil-u07-build-")
		if err != nil {
			u07SharedBuild.err = err
			return
		}
		u07SharedBuild.dir = dir

		// 1) 前端产物（Vite）。用显式 PATH 保证解析到基线 Node。
		npm := `D:\Program Files\nodejs\npm.cmd`
		cmd := exec.Command(npm, "--prefix", "frontend", "run", "build")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), baselineNodePath())
		if out, err := cmd.CombinedOutput(); err != nil {
			u07SharedBuild.err = fmt.Errorf("npm --prefix frontend run build 失败: %v\n%s", err, tail(string(out), 4000))
			return
		}
		u07SharedBuild.log = append(u07SharedBuild.log, "frontend build ok")

		// 2) 应用可执行文件。CGO_ENABLED=0：本机不需要 cgo，且与 X03 的构建口径一致。
		exe := filepath.Join(dir, "anvil-u07.exe")
		build := exec.Command("go", "build", "-o", exe, "./cmd/myagent")
		build.Dir = root
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			u07SharedBuild.err = fmt.Errorf("go build ./cmd/myagent 失败: %v\n%s", err, tail(string(out), 4000))
			return
		}
		u07SharedBuild.log = append(u07SharedBuild.log, "app build ok")
	})
	if u07SharedBuild.err != nil {
		t.Fatalf("构建应用失败（证据必须来自构建后的应用）: %v", u07SharedBuild.err)
	}
	return filepath.Join(u07SharedBuild.dir, "anvil-u07.exe"), u07SharedBuild.dir
}

// baselineNodePath 把基线 Node 放到 PATH 首位（AGENTS.md §4：PATH 首个 node 是
// 微信/WorkBuddy 的旧版本，Vite 会在 node:fs/promises 上直接崩）。
func baselineNodePath() string {
	sep := string(os.PathListSeparator)
	parts := []string{`D:\Program Files\nodejs`}
	if wd, err := os.Getwd(); err == nil {
		parts = append(parts, filepath.Join(wd, "..", "..", ".tools", "bin"))
	}
	parts = append(parts, os.Getenv("PATH"))
	return "PATH=" + strings.Join(parts, sep)
}

// repoRoot 从包目录向上找 go.mod。
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(wd, "go.mod")); statErr == nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("找不到仓库根（go.mod）")
		}
		wd = parent
	}
}

// u07App 是**运行中的构建后应用**：真进程、真 Wails 窗口、真 WebView2。
type u07App struct {
	exe      string
	home     string
	port     int
	proc     *os.Process
	pid      uint32
	hwnd     uintptr
	cdp      *cdpClient
	stdout   *os.File
	stderr   *os.File
	guide    string
	stopOnce *sync.Once
}

// startU07App 启动应用并连上它的主窗口 DevTools 通道。
//
// 隔离：ANVIL_HOME 指向临时目录，不碰用户真实配置/登录（AGENTS.md §6）。
// 故意**不**设置 ANVIL_PI_* 之类的覆盖：Pi 状态就该是本机的真实状态。
func startU07App(t *testing.T) *u07App {
	t.Helper()
	exe, _ := buildU07App(t)
	home := t.TempDir()
	port, err := freePort()
	if err != nil {
		t.Fatalf("分配 CDP 端口失败: %v", err)
	}
	return launchU07App(t, exe, home, port)
}

// launchU07App 用给定的可执行文件/数据目录/端口启动应用并连上 DevTools 通道。
//
// 与 startU07App 分开是为了 THEME02 的重启证据：同一个 ANVIL_HOME 再启动一次，
// 观察首屏主题。重启时**不**注册 t.Cleanup（由用例显式 Stop，避免重复等待）。
func launchU07App(t *testing.T, exe, home string, port int) *u07App {
	t.Helper()
	stdout, err := os.Create(filepath.Join(home, "app-stdout.log"))
	if err != nil {
		t.Fatalf("创建 stdout 日志失败: %v", err)
	}
	stderr, err := os.Create(filepath.Join(home, "app-stderr.log"))
	if err != nil {
		stdout.Close()
		t.Fatalf("创建 stderr 日志失败: %v", err)
	}

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(),
		"ANVIL_HOME="+home,
		"ANVIL_UI_A11Y=1",
		fmt.Sprintf("ANVIL_UI_CDP_PORT=%d", port),
	)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		stdout.Close()
		stderr.Close()
		t.Fatalf("启动应用失败: %v", err)
	}
	app := &u07App{exe: exe, home: home, port: port, proc: cmd.Process, pid: uint32(cmd.Process.Pid), stdout: stdout, stderr: stderr}
	app.stopOnce = &sync.Once{}
	t.Cleanup(app.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	hwnd, err := waitTopWindow(ctx, app.pid, u07WindowTimeout)
	if err != nil {
		t.Fatalf("等不到应用主窗口: %v\n--- stderr ---\n%s", err, app.Logs())
	}
	app.hwnd = hwnd

	targets, err := waitCDPTargets(ctx, port, u07WindowTimeout)
	if err != nil {
		t.Fatalf("等不到 DevTools 通道: %v\n--- stderr ---\n%s", err, app.Logs())
	}
	var page *cdpTarget
	for i := range targets {
		if targets[i].Type == "page" && strings.HasPrefix(targets[i].URL, "http://wails.localhost") {
			page = &targets[i]
			break
		}
	}
	if page == nil {
		t.Fatalf("DevTools 目标里没有主页面: %+v", targets)
	}
	app.guide = page.URL
	cdp, err := dialCDP(ctx, page.WebSocketDebuggerURL)
	if err != nil {
		t.Fatalf("连接主页面 DevTools 失败: %v", err)
	}
	app.cdp = cdp
	t.Cleanup(func() { cdp.Close() })

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ready, err := app.ready()
		if err == nil && ready {
			return app
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("应用界面 60s 内没有进入可用状态\n--- stderr ---\n%s", app.Logs())
	return nil
}

// ready 判断首屏是否已经渲染完成。
func (a *u07App) ready() (bool, error) {
	s, err := a.cdp.EvalString(`(()=>{
		const root=document.getElementById('root');
		const buttons=[...document.querySelectorAll('button,nav a,input,select')];
		return JSON.stringify({children: root?root.childElementCount:0, controls: buttons.length});
	})()`)
	if err != nil {
		return false, err
	}
	var st struct {
		Children int `json:"children"`
		Controls int `json:"controls"`
	}
	if err := json.Unmarshal([]byte(s), &st); err != nil {
		return false, err
	}
	return st.Children > 0 && st.Controls >= 3, nil
}

// Logs 返回应用 stdout+stderr（失败时的现场）。
func (a *u07App) Logs() string {
	var buf bytes.Buffer
	for _, p := range []string{filepath.Join(a.home, "app-stdout.log"), filepath.Join(a.home, "app-stderr.log")} {
		if b, err := os.ReadFile(p); err == nil {
			buf.WriteString("### " + p + "\n")
			buf.Write(b)
		}
	}
	return tail(buf.String(), 6000)
}

// Stop 关闭应用（走真实窗口关闭：WM_CLOSE；超时才杀进程）。可重复调用。
func (a *u07App) Stop() {
	if a == nil || a.proc == nil || a.stopOnce == nil {
		return
	}
	a.stopOnce.Do(func() {
		if a.hwnd != 0 {
			postMessage(a.hwnd, wmClose, 0, 0)
		}
		done := make(chan struct{})
		go func() {
			_, _ = a.proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = a.proc.Kill()
			<-done
		}
		if a.stdout != nil {
			a.stdout.Close()
		}
		if a.stderr != nil {
			a.stderr.Close()
		}
	})
}

// u07WindowTimeout 是等窗口/通道/界面就绪的单步预算。
const u07WindowTimeout = 45 * time.Second

// ---------- 页面动作 ----------

// PageFacts 是一次页面快照的事实（全部由页面自己算出来）。
type PageFacts struct {
	Route       string   `json:"route"`
	Locale      string   `json:"locale"`
	Theme       string   `json:"theme"`
	DPR         float64  `json:"dpr"`
	ViewportW   float64  `json:"viewportW"`
	ViewportH   float64  `json:"viewportH"`
	ScrollW     float64  `json:"scrollW"`
	ScrollH     float64  `json:"scrollH"`
	DocW        float64  `json:"docW"`
	DocH        float64  `json:"docH"`
	H1          string   `json:"h1"`
	Headings    []string `json:"headings"`
	Controls    int      `json:"controls"`
	Clipped     []string `json:"clipped"`
	Offscreen   []string `json:"offscreen"`
	Overlaps    []string `json:"overlaps"`
	VisibleText string   `json:"visibleText"`
}

// pageFactsExpr 在页面里计算布局事实。
//
// "不回退到假数据"的关键：这里只读真实 DOM 的 getBoundingClientRect / scrollWidth，
// 不读常量；裁剪与不可达按**交互元素**判定（用户点不到才是缺陷），重叠只算
// 交互元素之间的实质性覆盖（>=4px 交叠），避免把图标/装饰的贴边当成重叠。
const pageFactsExpr = `(()=>{
	const de=document.documentElement;
	const ix=[]; // interactive
	for (const el of document.querySelectorAll('button,a[href],select,input,textarea,[tabindex]:not([tabindex="-1"]),[role="button"],[role="tab"],[role="link"]')) {
		const r=el.getBoundingClientRect();
		if (r.width<=0 || r.height<=0) continue;
		if (getComputedStyle(el).visibility==='hidden' || getComputedStyle(el).display==='none') continue;
		ix.push({el, r});
	}
	const label=(el)=>{
		const t=(el.getAttribute('aria-label')||el.textContent||el.getAttribute('placeholder')||el.tagName).trim().replace(/\s+/g,' ');
		return t.length>60?t.slice(0,60)+'…':t;
	};
	const vw=window.innerWidth, vh=window.innerHeight;
	const clipped=[], offscreen=[], overlaps=[];
	const scrollRoot=de.scrollWidth>vw+1;
	for (const {el,r} of ix) {
		const name=label(el);
		// 水平裁剪：元素超出视口左/右，或父级 overflow 把它切掉。
		if (r.left < -0.5 || r.right > vw + 0.5) clipped.push(name+' ['+Math.round(r.left)+','+Math.round(r.right)+'] vw='+vw);
		// 纵向：允许页面纵向滚动（桌面表单会变长），但元素必须真的可达：
		// 完全在文档外、或渲染高度为 0 才算不可达。
		if (r.top + window.scrollY > de.scrollHeight + 1 || r.height < 1) offscreen.push(name);
	}
	// 交互元素两两覆盖（只比较同一屏内的）。
	for (let i=0;i<ix.length;i++) for (let j=i+1;j<ix.length;j++) {
		const a=ix[i].r, b=ix[j].r;
		const w=Math.min(a.right,b.right)-Math.max(a.left,b.left);
		const h=Math.min(a.bottom,b.bottom)-Math.max(a.top,b.top);
		if (w>4 && h>4) overlaps.push(label(ix[i].el)+' × '+label(ix[j].el)+' '+Math.round(w)+'x'+Math.round(h));
	}
	const h1=document.querySelector('h1');
	const headings=[...document.querySelectorAll('h1,h2,h3')].map(e=>(e.textContent||'').trim()).filter(Boolean).slice(0,12);
	return JSON.stringify({
		route: location.pathname+location.search,
		locale: de.lang,
		localePref: (window.localStorage.getItem('anvil.locale')||''),
		theme: de.dataset.theme||'',
		themePref: (window.localStorage.getItem('anvil.theme')||''),
		dpr: window.devicePixelRatio,
		viewportW: vw, viewportH: vh,
		scrollW: de.scrollWidth, scrollH: de.scrollHeight,
		docW: document.body?document.body.scrollWidth:0,
		docH: document.body?document.body.scrollHeight:0,
		h1: h1?(h1.textContent||'').trim():'',
		headings, controls: ix.length,
		clipped: clipped.slice(0,20), offscreen: offscreen.slice(0,20),
		overlaps: overlaps.slice(0,20),
		visibleText: (document.body?document.body.innerText:'').replace(/\s+/g,' ').slice(0,4000),
		scrollRootClipped: scrollRoot,
	});
})()`

// Facts 读一次页面事实。
func (a *u07App) Facts(t *testing.T) PageFacts {
	t.Helper()
	var f PageFacts
	if err := a.cdp.EvalJSON(pageFactsExpr, &f); err != nil {
		t.Fatalf("读取页面事实失败: %v", err)
	}
	return f
}

// rawFacts 返回带额外字段的 JSON（例如 scrollRootClipped），供证据落盘。
func (a *u07App) rawFacts(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := a.cdp.EvalJSON(pageFactsExpr, &m); err != nil {
		t.Fatalf("读取页面事实失败: %v", err)
	}
	return m
}

// Click 按选择器真实点击（CDP 输入事件，走渲染器的命中测试，不是 el.click()）。
func (a *u07App) Click(selector string) error {
	box, err := a.cdp.EvalString(fmt.Sprintf(`(()=>{
		const el=document.querySelector(%q);
		if(!el) return JSON.stringify({found:false});
		el.scrollIntoView({block:'center', inline:'center'});
		const r=el.getBoundingClientRect();
		return JSON.stringify({found:true, x:r.left+r.width/2, y:r.top+r.height/2, w:r.width, h:r.height});
	})()`, selector))
	if err != nil {
		return err
	}
	var b struct {
		Found      bool `json:"found"`
		X, Y, W, H float64
	}
	if err := json.Unmarshal([]byte(box), &b); err != nil {
		return err
	}
	if !b.Found {
		return fmt.Errorf("找不到元素 %s", selector)
	}
	for _, ev := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := fmt.Sprintf(`{"type":%q,"x":%v,"y":%v,"button":"left","clickCount":1}`, ev, b.X, b.Y)
		if _, err := a.cdp.Call("Input.dispatchMouseEvent", params, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// ClickByText 点击文本完全匹配（去除首尾空白）的第一个元素。
//
// 导航按钮没有稳定 id（文案来自 catalog），按文案点又正好顺带证明了"这个语言下
// 这个入口真的叫这个名字"。匹配在页面里做，点击仍走 CDP 命中测试。
func (a *u07App) ClickByText(tag, text string) error {
	expr := fmt.Sprintf(`(()=>{
		const norm=(s)=>(s||'').replace(/\s+/g,' ').trim();
		const want=norm(%q);
		const els=[...document.querySelectorAll(%q)];
		const hit=els.find(e=>norm(e.textContent)===want) || els.find(e=>norm(e.textContent).startsWith(want));
		if(!hit) return JSON.stringify({found:false, candidates: els.map(e=>norm(e.textContent)).slice(0,40)});
		hit.scrollIntoView({block:'center'});
		const r=hit.getBoundingClientRect();
		return JSON.stringify({found:true, x:r.left+r.width/2, y:r.top+r.height/2});
	})()`, text, tag)
	out, err := a.cdp.EvalString(expr)
	if err != nil {
		return err
	}
	var b struct {
		Found      bool `json:"found"`
		X, Y       float64
		Candidates []string `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		return err
	}
	if !b.Found {
		return fmt.Errorf("找不到文本为 %q 的 %s；候选: %v", text, tag, b.Candidates)
	}
	for _, ev := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := fmt.Sprintf(`{"type":%q,"x":%v,"y":%v,"button":"left","clickCount":1}`, ev, b.X, b.Y)
		if _, err := a.cdp.Call("Input.dispatchMouseEvent", params, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// TypeText 往当前焦点元素输入文本（CDP insertText：真实输入事件，不是直接改 value）。
func (a *u07App) TypeText(text string) error {
	params, err := json.Marshal(map[string]any{"text": text})
	if err != nil {
		return err
	}
	_, err = a.cdp.Call("Input.insertText", string(params), 10*time.Second)
	return err
}

// Focus 让元素获得真实焦点（DOM.focus + 可见性检查由调用方做）。
func (a *u07App) Focus(selector string) error {
	_, err := a.cdp.EvalString(fmt.Sprintf(`(()=>{const el=document.querySelector(%q); if(!el) return "missing"; el.focus(); return document.activeElement===el?"ok":"not-focused";})()`, selector))
	return err
}

// SetSelect 通过真实 UI 改变一个 <select> 的值并派发 input/change 事件。
//
// 为什么不是键盘：select 的原生下拉在 WebView2 里是独立窗口，CDP 点击够不到
// 选项；用 value + change 事件走的是 React 的 onChange 通道，页面自己的状态机
// 照常运转（不是"绕过界面直接写后端"）。
func (a *u07App) SetSelect(selector, value string) error {
	got, err := a.cdp.EvalString(fmt.Sprintf(`(()=>{
		const el=document.querySelector(%q);
		if(!el) return "missing";
		const setter=Object.getOwnPropertyDescriptor(window.HTMLSelectElement.prototype,'value').set;
		setter.call(el, %q);
		el.dispatchEvent(new Event('change', {bubbles:true}));
		return el.value;
	})()`, selector, value))
	if err != nil {
		return err
	}
	if got != value {
		return fmt.Errorf("select(%s) 未变成 %q（实际 %q）", selector, value, got)
	}
	return nil
}

// WaitText 等到页面可见文本包含 substr（或超时）。
func (a *u07App) WaitText(substr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		text, err := a.cdp.EvalString(`(()=>document.body?document.body.innerText:'')()`)
		if err == nil && strings.Contains(text, substr) {
			return nil
		}
		time.Sleep(120 * time.Millisecond)
	}
	text, _ := a.cdp.EvalString(`(()=>document.body?document.body.innerText:'')()`)
	return fmt.Errorf("%s 内页面文本没有出现 %q；当前文本前 400 字: %s", timeout, substr, head(text, 400))
}

// ---------- 窗口与显示 ----------

var (
	uUser32   = windows.NewLazySystemDLL("user32.dll")
	uKernel32 = windows.NewLazySystemDLL("kernel32.dll")
)

type uRect struct{ Left, Top, Right, Bottom int32 }

const (
	wmClose       = 0x0010
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

// enumWindows 收集所有顶层窗口。
func enumWindows() []uintptr {
	var out []uintptr
	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		out = append(out, hwnd)
		return 1
	})
	procEnum := uUser32.NewProc("EnumWindows")
	procEnum.Call(cb, 0)
	return out
}

// windowPID 返回窗口所属进程。
func windowPID(hwnd uintptr) uint32 {
	var pid uint32
	uUser32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// waitTopWindow 等目标进程出现一个可见的顶层窗口。
func waitTopWindow(ctx context.Context, pid uint32, timeout time.Duration) (uintptr, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, h := range enumWindows() {
			if windowPID(h) != pid {
				continue
			}
			if !windowVisible(h) {
				continue
			}
			if class := windowClass(h); class == "" || strings.Contains(class, "Legacy") {
				continue
			}
			if w, hgt := clientSize(h); w < 200 || hgt < 200 {
				continue
			}
			return h, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("进程 %d 没有可见顶层窗口", pid)
}

func windowVisible(hwnd uintptr) bool {
	v, _, _ := uUser32.NewProc("IsWindowVisible").Call(hwnd)
	return v != 0
}

func windowClass(hwnd uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := uUser32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

func windowRect(hwnd uintptr) uRect {
	var r uRect
	uUser32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func clientSize(hwnd uintptr) (int32, int32) {
	var r uRect
	uUser32.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r.Right - r.Left, r.Bottom - r.Top
}

func windowDPI(hwnd uintptr) uint32 {
	dpi, _, _ := uUser32.NewProc("GetDpiForWindow").Call(hwnd)
	if dpi == 0 {
		return 96
	}
	return uint32(dpi)
}

func postMessage(hwnd uintptr, msg uint32, wparam, lparam uintptr) {
	uUser32.NewProc("PostMessageW").Call(hwnd, uintptr(msg), wparam, lparam)
}

// SetClientSize 让客户区（= WebView2 视口）达到给定的物理像素尺寸。
//
// 直接 SetWindowPos 到目标尺寸会把边框算进去，客户区总是小一圈；不同 DPI 下
// 边框的物理像素还会变。所以按差值收敛：最多三轮，误差超过 2px 就失败，
// 免得"以为测了 880×600，其实是 864×584"。
func (a *u07App) SetClientSize(t *testing.T, w, h int) {
	t.Helper()
	if a.hwnd == 0 {
		t.Fatalf("没有窗口句柄，无法设置尺寸")
	}
	curW, curH := clientSize(a.hwnd)
	outer := windowRect(a.hwnd)
	targetOuterW := int32(w) + (outer.Right - outer.Left - curW)
	targetOuterH := int32(h) + (outer.Bottom - outer.Top - curH)
	for attempt := 0; attempt < 4; attempt++ {
		uUser32.NewProc("SetWindowPos").Call(a.hwnd, 0,
			uintptr(8), uintptr(8),
			uintptr(targetOuterW), uintptr(targetOuterH),
			uintptr(swpNoZOrder|swpNoActivate))
		time.Sleep(250 * time.Millisecond)
		curW, curH = clientSize(a.hwnd)
		if u07AbsInt32(curW-int32(w)) <= 2 && u07AbsInt32(curH-int32(h)) <= 2 {
			return
		}
		outer = windowRect(a.hwnd)
		targetOuterW = int32(w) + (outer.Right - outer.Left - curW)
		targetOuterH = int32(h) + (outer.Bottom - outer.Top - curH)
	}
	cw, ch := clientSize(a.hwnd)
	t.Fatalf("无法把客户区调到 %d×%d（当前 %d×%d）", w, h, cw, ch)
}

func u07AbsInt32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// ShowWindow 让窗口可见/正常化（截图前必须，最小化时 CDP 截图会超时）。
func (a *u07App) ShowWindow() {
	const swRestore = 9
	uUser32.NewProc("ShowWindow").Call(a.hwnd, swRestore)
	time.Sleep(150 * time.Millisecond)
}

// ScreenMetrics 是主显示器的事实（外部判据，不是页面自报）。
type ScreenMetrics struct {
	WidthPx      int32  `json:"widthPx"`
	HeightPx     int32  `json:"heightPx"`
	WorkWidthPx  int32  `json:"workWidthPx"`
	WorkHeightPx int32  `json:"workHeightPx"`
	DPI          uint32 `json:"dpi"`
}

// screenMetrics 用 GetSystemMetrics 读主显示器与工作区。
func screenMetrics(hwnd uintptr) ScreenMetrics {
	get := func(idx int) int32 {
		v, _, _ := uUser32.NewProc("GetSystemMetrics").Call(uintptr(idx))
		return int32(v)
	}
	// SPI_GETWORKAREA = 0x0030
	var work uRect
	uUser32.NewProc("SystemParametersInfoW").Call(0x0030, 0, uintptr(unsafe.Pointer(&work)), 0)
	return ScreenMetrics{
		WidthPx:      get(0), // SM_CXSCREEN
		HeightPx:     get(1), // SM_CYSCREEN
		WorkWidthPx:  work.Right - work.Left,
		WorkHeightPx: work.Bottom - work.Top,
		DPI:          windowDPI(hwnd),
	}
}

// ---------- 证据落盘 ----------

// sha256Hex 是证据里的截图/文件 hash。
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeObsArtifact 落盘一个证据文件（绝对路径，调用方给 case 自己的目录）。
//
// 与 b01b 的 writeArtifact 分开命名：那个按 MYAGENT_B01B_OUT 的相对目录写，
// 这个按 native runner 注入的 observation 目录写，混用会让证据落错地方。
func writeObsArtifact(t *testing.T, absPath string, data []byte) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("创建证据目录失败: %v", err)
	}
	if err := os.WriteFile(absPath, data, 0o644); err != nil {
		t.Fatalf("写证据文件 %s 失败: %v", absPath, err)
	}
}

// observationFile 返回观察目录里的绝对路径。
func observationFile(t *testing.T, name string) string {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		t.Fatalf("没有 ANVIL_OBSERVATION_DIR，用例必须由 native runner 执行")
	}
	return filepath.Join(dir, filepath.FromSlash(name))
}

// writeScreenshot 把截图落盘并返回它的 sha256（hash 从**写下去的字节**算）。
func (a *u07App) writeScreenshot(t *testing.T, name string) string {
	t.Helper()
	png, err := a.cdp.CaptureScreenshot("")
	if err != nil {
		t.Fatalf("截图 %s 失败: %v", name, err)
	}
	writeObsArtifact(t, observationFile(t, name), png)
	return sha256Hex(png)
}

// head/tail 是日志截断（避免证据里塞几十 KB）。
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// freePort 找一个空闲的本机端口给 DevTools 通道用。
//
// 不要写死端口：并行/残留进程会让两条 case 抢同一个端口，症状是"证据随机失败"。
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
