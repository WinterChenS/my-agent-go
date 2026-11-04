package browserfixture

// 页面模板。{{ORIGIN}} / {{OTHER}} 由 Origin.page 换成真实 origin，
// 因此响应体本身就带"这个页面属于哪个 origin"的证据。
//
// 约定：页面里的数字来自服务端（fetch 返回的 count），只是给人看的；
// 自动化断言一律读 Counter()，不读这些数字，也不读任何 toast。

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
