package browserfixture

// 页面模板。{{ORIGIN}} / {{OTHER}} 由 Origin.page 换成真实 origin，
// 因此响应体本身就带"这个页面属于哪个 origin"的证据。
//
// 约定：页面里的数字来自服务端（fetch 返回的 count），只是给人看的；
// 自动化断言一律读 Counter()，不读这些数字，也不读任何 toast。

import (
	"strconv"
	"strings"
)

const indexPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>Browser fixture</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}code{background:#f4f4f4;padding:1px 4px}</style>
</head><body data-origin="{{ORIGIN}}" data-other="{{OTHER}}">
<h1>Browser fixture · <span id="origin">{{ORIGIN}}</span></h1>
<p>页面里的 location.origin：<code id="live-origin">pending</code></p>
<p>另一个 origin：<code id="other-origin">{{OTHER}}</code></p>
<ul>
  <li><a id="link-form" href="/form">普通表单（input/select/textarea/checkbox/button）</a></li>
  <li><a id="link-spa" href="/spa">SPA 节点替换</a></li>
  <li><a id="link-same-url" href="/same-url">同 URL 导航</a></li>
  <li><a id="link-cross-origin" href="/cross-origin">cross-origin iframe</a></li>
  <li><a id="link-closed-shadow" href="/closed-shadow">closed shadow root</a></li>
  <li><a id="link-trusted-only" href="/trusted-only">trusted-only button</a></li>
  <li><a id="link-login" href="/login">登录 cookie</a></li>
  <li><a id="link-download" href="/download">下载</a></li>
  <li><a id="link-popup" href="/popup">window.open 新窗口</a></li>
  <li><a id="link-slow" href="/slow?ms=1500">慢加载（1–3s 可配）</a></li>
  <li><a id="link-huge" href="/huge?nodes=400&amp;labelLen=6&amp;textChars=200">巨型页面（元素/文本/JSON 限额）</a></li>
</ul>
<p>本 origin 服务端计数：<code id="counters">pending</code></p>
<script>
document.getElementById('live-origin').textContent = location.origin;
fetch('/__counters').then(function (r) { return r.json(); }).then(function (m) {
  document.getElementById('counters').textContent = JSON.stringify(m);
});
</script>
</body></html>`

const formPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>普通表单</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}label{display:block;margin:8px 0}</style>
</head><body data-origin="{{ORIGIN}}">
<h1 id="form-title">普通表单</h1>
<form id="fixture-form">
  <label>姓名 <input id="f-name" name="name" type="text" value=""></label>
  <label>套餐
    <select id="f-plan" name="plan">
      <option value="basic">basic</option>
      <option value="pro">pro</option>
    </select>
  </label>
  <label>备注 <textarea id="f-note" name="note" rows="3" cols="30"></textarea></label>
  <label><input id="f-confirm" name="confirm" type="checkbox"> 我已确认</label>
  <button id="f-submit" type="submit">提交</button>
  <button id="f-disabled" type="button" disabled>禁用按钮</button>
</form>
<p>服务端提交次数：<strong id="submit-count">0</strong></p>
<script>
var form = document.getElementById('fixture-form');
form.addEventListener('submit', function (e) {
  e.preventDefault();
  var payload = {
    name: document.getElementById('f-name').value,
    plan: document.getElementById('f-plan').value,
    note: document.getElementById('f-note').value,
    confirm: document.getElementById('f-confirm').checked
  };
  fetch('/__form/submit', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  }).then(function (r) { return r.json(); }).then(function (j) {
    document.getElementById('submit-count').textContent = String(j.count);
  });
});
</script>
</body></html>`

const spaPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>SPA 节点替换</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1>SPA 节点替换</h1>
<div id="spa-host"><button id="spa-target" type="button">点我计数（随后会被替换）</button></div>
<button id="spa-replace" type="button">替换节点</button>
<p>服务端计数：target=<strong id="spa-target-count">0</strong> replaced=<strong id="spa-replaced-count">0</strong></p>
<script>
function inc(kind) {
  return fetch('/__spa/action', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ kind: kind })
  }).then(function (r) { return r.json(); }).then(function (j) {
    document.getElementById('spa-target-count').textContent = String(j.counters['spa-target'] || 0);
    document.getElementById('spa-replaced-count').textContent = String(j.counters['spa-replaced'] || 0);
  });
}
document.getElementById('spa-target').addEventListener('click', function () { inc('target'); });
document.getElementById('spa-replace').addEventListener('click', function () {
  var host = document.getElementById('spa-host');
  // 断开旧节点：旧 ref 必须因此失效，而不是被重新解析到新节点上。
  host.innerHTML = '';
  var btn = document.createElement('button');
  btn.id = 'spa-target-replaced';
  btn.type = 'button';
  btn.textContent = '替换后的按钮（计数到 replaced）';
  btn.addEventListener('click', function () { inc('replaced'); });
  host.appendChild(btn);
});
</script>
</body></html>`

const sameURLPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>同 URL 导航</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1>同 URL 导航</h1>
<p>URL 始终是 <code id="self-url">/same-url</code>，每次导航都是新文档。</p>
<button id="same-url-target" type="button">点我计数</button>
<a id="same-url-reload" href="/same-url">用同一个 URL 再导航一次</a>
<button id="same-url-js-reload" type="button">location.reload()</button>
<p>服务端计数：<strong id="same-url-count">0</strong></p>
<script>
document.getElementById('same-url-target').addEventListener('click', function () {
  fetch('/__same-url/action', { method: 'POST' }).then(function (r) { return r.json(); }).then(function (j) {
    document.getElementById('same-url-count').textContent = String(j.count);
  });
});
document.getElementById('same-url-js-reload').addEventListener('click', function () { location.reload(); });
</script>
</body></html>`

const crossOriginPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>cross-origin iframe</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}iframe{border:1px solid #888;width:420px;height:170px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1>cross-origin iframe</h1>
<p>外层 origin：<code id="outer-origin">{{ORIGIN}}</code></p>
<p>frame origin：<code id="frame-origin">{{OTHER}}</code></p>
<iframe id="xframe" src="{{OTHER}}/frame" title="cross-origin frame"></iframe>
<p>外层按钮计数：<strong id="outer-count">0</strong></p>
<button id="outer-action" type="button">外层计数一次</button>
<script>
document.getElementById('outer-action').addEventListener('click', function () {
  fetch('/__frame/action', { method: 'POST' }).then(function (r) { return r.json(); }).then(function (j) {
    document.getElementById('outer-count').textContent = String(j.count);
  });
});
</script>
</body></html>`

const framePage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>frame</title>
<style>body{font:13px/1.5 system-ui,sans-serif;margin:12px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1 id="frame-title">子 frame</h1>
<p>frame origin：<code id="frame-live-origin">{{ORIGIN}}</code></p>
<button id="frame-target" type="button">frame 内计数</button>
<p>frame 内计数：<strong id="frame-count">0</strong></p>
<script>
document.getElementById('frame-target').addEventListener('click', function () {
  fetch('/__frame/action', { method: 'POST' }).then(function (r) { return r.json(); }).then(function (j) {
    document.getElementById('frame-count').textContent = String(j.count);
  });
});
</script>
</body></html>`

const closedShadowPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>closed shadow root</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1>closed shadow root</h1>
<p>下面的按钮在 closed shadow root 里，顶层脚本拿不到它。</p>
<div id="shadow-host"></div>
<button id="shadow-light" type="button">光 DOM 计数（对照组）</button>
<p>closed=<strong id="shadow-closed-count">0</strong> light=<strong id="shadow-light-count">0</strong></p>
<script>
var host = document.getElementById('shadow-host');
var root = host.attachShadow({ mode: 'closed' });
root.innerHTML = '<button id="shadow-target" type="button">shadow 内按钮</button>';
root.getElementById('shadow-target').addEventListener('click', function () {
  fetch('/__shadow/action?kind=closed', { method: 'POST' });
});
document.getElementById('shadow-light').addEventListener('click', function () {
  fetch('/__shadow/action?kind=light', { method: 'POST' }).then(function (r) { return r.json(); }).then(function (j) {
    document.getElementById('shadow-light-count').textContent = String(j.count);
  });
});
</script>
</body></html>`

const trustedOnlyPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>trusted-only</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1>trusted-only button</h1>
<p>只接受真实用户点击（event.isTrusted）。脚本合成的事件连请求都不会发出。</p>
<button id="trusted-target" type="button">只认真实点击</button>
<p>最近一次事件：<code id="trusted-last">none</code></p>
<p>服务端：accepted=<strong id="trusted-accepted">0</strong> rejected=<strong id="trusted-rejected">0</strong></p>
<script>
document.getElementById('trusted-target').addEventListener('click', function (e) {
  var last = document.getElementById('trusted-last');
  if (!e.isTrusted) {
    // 页面侧闸门：非 trusted 直接返回，不产生任何网络副作用。
    last.textContent = 'untrusted（已忽略，未发请求）';
    return;
  }
  last.textContent = 'trusted';
  fetch('/__trusted/click', {
    method: 'POST',
    headers: { 'X-Event-Trusted': 'true' }
  }).then(function (r) { return r.json(); }).then(function (j) {
    document.getElementById('trusted-accepted').textContent = String(j.accepted);
    document.getElementById('trusted-rejected').textContent = String(j.rejected);
  });
});
</script>
</body></html>`

const loginPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>登录 cookie</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}label{display:block;margin:8px 0}</style>
</head><body data-origin="{{ORIGIN}}">
<h1 id="login-title">登录 cookie</h1>
<form id="login-form" method="post" action="/__login">
  <label>用户 <input id="login-user" name="user" type="text" value="fixture-user"></label>
  <label>密码 <input id="login-pass" name="pass" type="password" value="fixture-pass"></label>
  <button id="login-submit" type="submit">登录</button>
</form>
<p><a id="login-private" href="/private">查看受保护页面</a></p>
</body></html>`

const popupPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>window.open</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1>window.open 新窗口</h1>
<p>新窗口必须由宿主接管成同一个 session 的受控 tab（B06/BR16），
不允许产生宿主不知道的窗口。</p>
<button id="open-popup" type="button">window.open 打开新窗口</button>
<p>popup-target 加载次数：<strong id="popup-count">0</strong></p>
<script>
document.getElementById('open-popup').addEventListener('click', function () {
  window.open('/popup-target', '_blank');
});
fetch('/__counters').then(function (r) { return r.json(); }).then(function (m) {
  document.getElementById('popup-count').textContent = String(m['popup-target'] || 0);
});
</script>
</body></html>`

const downloadPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>下载</title>
<style>body{font:14px/1.6 system-ui,sans-serif;margin:24px}</style>
</head><body data-origin="{{ORIGIN}}">
<h1>下载</h1>
<p>下载必须先落到宿主管理的 DownloadJob，由用户确认保存位置（B06/BR17），
remote 页面不能自己选本机任意路径。</p>
<a id="download-link" href="/__download/report.csv" download="report.csv">下载 report.csv</a>
<p>服务端下载开始次数：<strong id="download-count">0</strong></p>
<script>
fetch('/__counters').then(function (r) { return r.json(); }).then(function (m) {
  document.getElementById('download-count').textContent = String(m['download'] || 0);
});
</script>
</body></html>`

// hugePageHTML 生成一个可配置规模的页面，用来压 BR09 的三条限额
// （元素 300 / 文本 40000 字符 / snapshot JSON 256KiB）。
//
// 为什么由服务端按参数生成而不是写死一个大模板：三条限额要分别被触发，
// 写死一页只能证明"这一页超限了"，无法证明"上限是 300 而不是正好这一页的元素数"。
//
//   - nodes      可交互元素（button）数量；
//   - labelLen   每个按钮文字的字符数（用中日韩字符，JSON 里 3 字节/字符）；
//   - textChars  正文段落的字符数（同样用中日韩字符，用来压文本与 JSON 上限）。
//
// 结构上仍然带 data-origin，所以 TestEveryPageIsServedWithItsOwnOrigin 也覆盖它。
func hugePageHTML(origin string, nodes, labelLen, textChars int) string {
	label := strings.Repeat("钮", labelLen)
	if labelLen <= 0 {
		label = ""
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><title>巨型页面</title>\n")
	b.WriteString("<style>body{font:13px/1.5 system-ui,sans-serif;margin:12px}</style>\n</head>\n")
	b.WriteString("<body data-origin=\"" + origin + "\">\n<h1 id=\"huge-title\">巨型页面</h1>\n")
	b.WriteString("<p id=\"huge-params\">nodes=" + strconv.Itoa(nodes) + " labelLen=" + strconv.Itoa(labelLen) +
		" textChars=" + strconv.Itoa(textChars) + "</p>\n<div id=\"huge-buttons\">\n")
	for i := 0; i < nodes; i++ {
		b.WriteString("<button type=\"button\" id=\"huge-" + strconv.Itoa(i) + "\">" + label + strconv.Itoa(i) +
			"</button>\n")
	}
	b.WriteString("</div>\n<p id=\"huge-text\">" + strings.Repeat("文", textChars) + "</p>\n")
	b.WriteString("</body></html>\n")
	return b.String()
}

// noisePage 把整个视口涂成随机像素，用来压 BR09 的"截图 ≤4MiB"。
//
// 为什么需要它：普通页面（大片留白）PNG 压得很小，永远碰不到 4MiB 上限，
// 于是"超限时受控缩小或明确报错"这条分支永远没有真实证据。随机像素不可压缩，
// 排版 2000×3000 时一张 PNG 就有十几 MB，正好把这条分支逼出来。
const noisePage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>噪声页面</title>
<style>html,body{margin:0;padding:0;overflow:hidden}canvas{display:block}</style>
</head><body data-origin="{{ORIGIN}}">
<canvas id="noise"></canvas>
<script>
var c = document.getElementById('noise');
var w = window.innerWidth, h = window.innerHeight;
c.width = w; c.height = h;
c.style.width = w + 'px'; c.style.height = h + 'px';
var ctx = c.getContext('2d');
var img = ctx.createImageData(w, h);
var data = img.data;
var chunk = new Uint8Array(65536);
for (var off = 0; off < data.length; off += chunk.length) {
  crypto.getRandomValues(chunk);
  var n = Math.min(chunk.length, data.length - off);
  for (var i = 0; i < n; i++) data[off + i] = chunk[i];
}
ctx.putImageData(img, 0, 0);
</script>
</body></html>`
