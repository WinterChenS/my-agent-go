package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	browserfixture "my-agent-go/tests/fixtures/browser"
)

// shellServer 提供 spike 的壳页面。
//
// 为什么不内嵌 HTML 到 Wails Assets：这个 spike 只需要一个页面，用本地 HTTP
// 服务更直接，也更方便把页面上报的 slot 几何、探针结果原样打印到 stdout
// （证据要能和截图逐行对上）。它不参与产品构建，也不构成前端方案。
type shellServer struct {
	ln        net.Listener
	srv       *http.Server
	state     *spikeState
	primary   string
	secondary string
}

func newShellServer(fixture *browserfixture.Fixture, state *spikeState) (*shellServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听壳页面端口: %w", err)
	}
	s := &shellServer{
		ln:        ln,
		state:     state,
		primary:   fixture.Primary.URL,
		secondary: fixture.Secondary.URL,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	mux.HandleFunc("GET /probe.json", s.handleProbe)
	mux.HandleFunc("POST /slot", s.handleSlot)
	// B01a：spike 用这两个端点驱动“打开应用内 dialog”与收集页面回报的状态。
	mux.HandleFunc("POST /dialog", func(w http.ResponseWriter, _ *http.Request) {
		s.state.requestDialog()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /dialog-state", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Open bool `json:"open"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "dialog 状态不是合法 JSON", http.StatusBadRequest)
			return
		}
		s.state.setPageDialogOpen(body.Open)
		fmt.Printf("B01|dialog|open=%t\n", body.Open)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// URL 是壳页面的地址，直接交给 Wails 窗口。
func (s *shellServer) URL() string { return "http://" + s.ln.Addr().String() }

// Close 停止壳页面服务；重复调用安全。
func (s *shellServer) Close() error {
	if s == nil || s.srv == nil {
		return nil
	}
	srv := s.srv
	s.srv = nil
	return srv.Close()
}

// autoOpenDialog 读取 MYAGENT_SPIKE_OPEN_DIALOG。
//
// 它只替代"人手点一下按钮"，不替代对遮挡关系的验证：真正要证明的是
// 原生网页视图打开后 dialog 仍在其上方，那属于 B01。
func autoOpenDialog() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MYAGENT_SPIKE_OPEN_DIALOG"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func boolLiteral(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func (s *shellServer) handlePage(w http.ResponseWriter, _ *http.Request) {
	title := spikeWindowTitle
	slotHead := "右侧 BrowserSlot（B00 占位）"
	nativeNote := ""
	if s.state.nativeMode {
		title = nativeWindowTitle
		slotHead = "右侧 BrowserSlot（B01a 原生 WebView2）"
		nativeNote = "B01a 模式：这块区域被原生 WebView2 子窗口（独立 HWND）覆盖；" +
			"上面这段 HTML 只在子窗口隐藏时（或 PrintWindow 截图里）看得到。"
	}
	body := strings.NewReplacer(
		"{{PRIMARY}}", s.primary,
		"{{SECONDARY}}", s.secondary,
		"{{SLOT_ID}}", slotID,
		"{{AUTO_DIALOG}}", boolLiteral(autoOpenDialog()),
		"{{TITLE}}", title,
		"{{SLOT_HEAD}}", slotHead,
		"{{NATIVE_NOTE}}", nativeNote,
	).Replace(shellPage)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

func (s *shellServer) handleProbe(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.state.snapshot())
}

// handleSlot 接收页面上报的槽位几何。
//
// 这就是 B07 UpdateBrowserSlot 的形状：CSS px + revision，旧的 revision 被丢弃。
// B00 只是把它打出来，证明"几何能真的从 WebView 里量到"。
func (s *shellServer) handleSlot(w http.ResponseWriter, r *http.Request) {
	var rep slotReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		http.Error(w, "slot 几何不是合法 JSON", http.StatusBadRequest)
		return
	}
	rep.ReportedAt = time.Now().Format(time.RFC3339Nano)
	accepted := s.state.setSlot(rep)
	if accepted {
		fmt.Printf("B00|slot|rev=%d slotId=%q left=%.1f top=%.1f width=%.1f height=%.1f visible=%t zoom=%.2f dpr=%.2f viewport=%.0fx%.0f\n",
			rep.Revision, rep.SlotID, rep.Left, rep.Top, rep.Width, rep.Height,
			rep.Visible, rep.Zoom, rep.DevicePixelRatio, rep.CSSWidth, rep.CSSHeight)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = fmt.Fprintf(w, `{"accepted":%t,"revision":%d}`, accepted, rep.Revision)
}

// shellPage 是 spike 的唯一页面。
//
// 结构就是最终产品要的样子：左侧列表、中间聊天、右侧 BrowserSlot，外加一个
// 应用内 dialog。差别只有一处——右侧那块是空的，并且写明为什么空。
const shellPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>{{TITLE}}</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  html, body { height: 100%; }
  body { margin: 0; overflow: hidden; background: #0f1218; color: #dfe3ea;
         font: 13px/1.55 system-ui, "Segoe UI", "Microsoft YaHei", sans-serif; }
  code { background: #1b212b; padding: 1px 4px; border-radius: 3px; font-size: 11.5px; }
  .app { display: grid; grid-template-columns: 216px minmax(0, 1fr) 452px; height: 100vh; }
  .pane { border-right: 1px solid #232833; padding: 14px; overflow: auto; min-width: 0; }
  h2 { font-size: 11px; text-transform: uppercase; letter-spacing: .09em;
       color: #8b95a7; margin: 0 0 10px; }
  h2 + h2 { margin-top: 20px; }
  .dim { color: #8b95a7; }
  .warn { color: #ffb454; }
  .verdict { font-weight: 700; }
  .AS_EXPECTED { color: #7ddc8a; }
  .UNEXPECTED_SUCCESS { color: #ff6b6b; }
  .UNEXPECTED_ERROR { color: #ffb454; }
  .WARN { color: #ffb454; }
  .PENDING { color: #8b95a7; }
  pre { background: #141922; border: 1px solid #232833; border-radius: 4px;
        padding: 8px; margin: 6px 0; font-size: 11.5px; white-space: pre-wrap;
        overflow: auto; max-height: 260px; }
  .step { padding: 5px 0; border-bottom: 1px dashed #232833; }
  .slot { display: flex; flex-direction: column; padding: 10px; min-width: 0; }
  .slot-frame { flex: 1; border: 2px dashed #e05656; border-radius: 6px;
                display: flex; flex-direction: column; background: #171a20; }
  .slot-head { background: #3a1a1a; color: #ffb3b3; padding: 8px 10px;
               font-weight: 600; border-bottom: 1px solid #e05656; }
  .slot-body { padding: 14px; color: #ffd9d9; overflow: auto; }
  .missing { font-size: 16px; font-weight: 700; color: #ff8080; margin: 0 0 8px; }
  button { background: #253044; color: #dfe3ea; border: 1px solid #38465c;
           border-radius: 4px; padding: 5px 10px; font: inherit; cursor: pointer; }
  button:hover { background: #2e3b52; }
  dialog { background: #161b24; color: #dfe3ea; border: 1px solid #3a4453;
           border-radius: 8px; padding: 0; min-width: 460px; }
  dialog::backdrop { background: rgba(0, 0, 0, .55); }
  .dlg-head { padding: 12px 16px; border-bottom: 1px solid #232833; font-weight: 600; }
  .dlg-body { padding: 16px; }
  .dlg-foot { padding: 12px 16px; border-top: 1px solid #232833; text-align: right; }
</style>
</head>
<body>
<div class="app">
  <aside class="pane">
    <h2>左侧（占位）</h2>
    <p>工作区 / 会话列表</p>
    <p class="dim">B00 只做契约与 fixture，这里不放业务 UI。</p>

    <h2>fixture · primary</h2>
    <p><code>{{PRIMARY}}</code></p>
    <pre id="fx-primary-counters">读取中…</pre>

    <h2>fixture · secondary</h2>
    <p><code>{{SECONDARY}}</code></p>
    <pre id="fx-secondary-counters">读取中…</pre>
  </aside>

  <main class="pane">
    <h2>中间（占位）</h2>
    <p>聊天 / 工具结果</p>
    <p><button id="open-dialog" type="button">打开应用内 dialog</button></p>
    <p class="dim">dialog 用于 B01 验证「native 网页不能遮住应用内弹层」。
      现在右侧没有原生视图，所以它还遮不住任何东西。</p>

    <h2>探针结论</h2>
    <p>verdict：<span id="probe-verdict" class="verdict PENDING">PENDING</span></p>
    <p class="dim">AS_EXPECTED = 该调用按预期返回了 NOT_IMPLEMENTED；
      UNEXPECTED_SUCCESS 才是"冒充成功"。</p>
    <div id="probe">等待探针…</div>
  </main>

  <section class="slot">
    <h2>{{SLOT_HEAD}}</h2>
    <div class="slot-frame" id="browser-slot" data-slot-id="{{SLOT_ID}}">
      <div class="slot-head">原生网页视图应该在这里</div>
      <div class="slot-body">
        <p class="missing">这里没有原生网页视图</p>
        <p>{{NATIVE_NOTE}}</p>
        <p>预期：同一主窗口右侧的 WebView2 子视图铺满这块（R06）。</p>
        <p>实际：<code>BrowserSurface.Attach</code> 返回
          <code>NOT_IMPLEMENTED</code>，块内只有这段 HTML。</p>
        <p class="warn">不是 iframe，不是浮窗，不是截图流——就是没有。</p>
        <pre id="slot-geometry">slot 几何未上报</pre>
      </div>
    </div>
  </section>
</div>

<dialog id="app-dialog">
  <div class="dlg-head">应用内 dialog</div>
  <div class="dlg-body">
    <p>这是应用自己的模态层，和右侧网页没有关系。</p>
    <p class="dim">B01 要用它验证：原生子视图打开时，这个 dialog 不能被网页盖住。</p>
  </div>
  <div class="dlg-foot"><button id="close-dialog" type="button">关闭</button></div>
</dialog>

<script>
var PRIMARY = '{{PRIMARY}}';
var SECONDARY = '{{SECONDARY}}';

var slotEl = document.getElementById('browser-slot');
var revision = 0;

function reportSlot() {
  var r = slotEl.getBoundingClientRect();
  revision += 1;
  var payload = {
    slotId: slotEl.dataset.slotId,
    revision: revision,
    left: r.left,
    top: r.top,
    width: r.width,
    height: r.height,
    visible: r.width > 0 && r.height > 0,
    zoom: 1,
    devicePixelRatio: window.devicePixelRatio || 1,
    cssWidth: document.documentElement.clientWidth,
    cssHeight: document.documentElement.clientHeight,
    reportedAt: new Date().toISOString()
  };
  document.getElementById('slot-geometry').textContent =
    'slot=' + payload.slotId + '  rev=' + payload.revision + '\n' +
    'left=' + payload.left.toFixed(1) + '  top=' + payload.top.toFixed(1) + '\n' +
    'width=' + payload.width.toFixed(1) + '  height=' + payload.height.toFixed(1) + '\n' +
    'zoom=' + payload.zoom + '  devicePixelRatio=' + payload.devicePixelRatio + '\n' +
    'viewport=' + payload.cssWidth + 'x' + payload.cssHeight;
  fetch('/slot', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  }).catch(function () {});
}

reportSlot();
var slotTimer = null;
window.addEventListener('resize', function () {
  clearTimeout(slotTimer);
  slotTimer = setTimeout(reportSlot, 120);
});

function renderProbe(data) {
  var verdictEl = document.getElementById('probe-verdict');
  verdictEl.textContent = data.verdict;
  verdictEl.className = 'verdict ' + data.verdict;
  var host = document.getElementById('probe');
  host.textContent = '';
  var steps = data.steps || [];
  if (steps.length === 0) {
    host.textContent = '探针还没跑（等待原生窗口句柄）…';
    return;
  }
  steps.forEach(function (st) {
    var row = document.createElement('div');
    row.className = 'step';
    var head = document.createElement('div');
    head.className = st.result;
    head.textContent = st.result + '  ·  ' + st.name +
      (st.code ? '  [' + st.code + ']' : '');
    var body = document.createElement('div');
    body.className = 'dim';
    body.textContent = st.got;
    row.appendChild(head);
    row.appendChild(body);
    host.appendChild(row);
  });
}

function pollProbe() {
  fetch('/probe.json').then(function (r) { return r.json(); })
    .then(function (data) {
      renderProbe(data);
      // B01a：spike 通过 /dialog 要求打开应用内 dialog（遮挡证据用）。
      if (data.openDialog && !dlg.open) { dlg.showModal(); reportDialogState(); }
    }).catch(function () {});
}

pollProbe();
setInterval(pollProbe, 500);

function loadCounters(base, id) {
  var el = document.getElementById(id);
  fetch(base + '/__counters').then(function (r) { return r.json(); })
    .then(function (m) { el.textContent = JSON.stringify(m, null, 1); })
    .catch(function (e) { el.textContent = '读取失败: ' + e; });
}
loadCounters(PRIMARY, 'fx-primary-counters');
loadCounters(SECONDARY, 'fx-secondary-counters');
setInterval(function () {
  loadCounters(PRIMARY, 'fx-primary-counters');
  loadCounters(SECONDARY, 'fx-secondary-counters');
}, 2000);

var dlg = document.getElementById('app-dialog');
document.getElementById('open-dialog').addEventListener('click', function () { dlg.showModal(); reportDialogState(); });
document.getElementById('close-dialog').addEventListener('click', function () { dlg.close(); reportDialogState(); });
function reportDialogState() {
  fetch('/dialog-state', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ open: dlg.open })
  }).catch(function () {});
}
dlg.addEventListener('close', reportDialogState);
// MYAGENT_SPIKE_OPEN_DIALOG=1 时自动打开，等价于脚本点一次按钮，方便截图留证。
if ({{AUTO_DIALOG}}) { dlg.showModal(); reportDialogState(); }
</script>
</body>
</html>`
