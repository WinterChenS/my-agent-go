//! The right pane's browser: a real webview, and the way a runtime drives it.
//!
//! The pane is a Tauri child webview, not an iframe: a site that sends
//! `frame-ancestors 'none'` cannot be embedded in a frame, and the whole point
//! of the pane is to show the page the agent is working on. The webview is
//! positioned over the pane's content rect, which the page reports whenever it
//! moves or resizes.
//!
//! Driving it is a two-way bridge:
//!
//! * the page's script runtime runs a command (`click`, `fill`, `text`, …) and
//!   posts the answer back to the host's loopback server;
//! * the caller (a pi tool, through that same loopback server) is waiting on a
//!   channel for exactly that answer.
//!
//! `webview.eval` has no return value, which is why the answer travels back
//! through the bridge rather than as a function result.
//!
//! Reading the page is only half of looking at it: `capture` asks WebView2 to
//! rasterise what is on screen, because a broken layout and a hidden button are
//! invisible to a DOM query.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, AtomicU16, Ordering};
use std::sync::mpsc::{channel, Sender};
use std::sync::Mutex;
use std::time::Duration;

use serde_json::Value;
use tauri::webview::Webview;
use tauri::{AppHandle, LogicalPosition, LogicalSize, Manager};

/// How long a command may take before the caller is told nobody answered.
pub const COMMAND_TIMEOUT: Duration = Duration::from_secs(15);

/// Event telling the window that the pane should come forward on a URL.
///
/// A pane that is closed is a webview parked off-screen: a runtime navigating
/// in the background would put the page where nobody can look at it.
pub const SHOW_EVENT: &str = "anvil://browser-show";

/// One page command the runtime understands.
pub type Reply = Result<Value, String>;

pub struct Browser {
    /// The child webview, once the pane has been opened.
    webview: Mutex<Option<Webview<tauri::Wry>>>,
    /// Commands waiting for the page to post their answer.
    pending: Mutex<HashMap<u64, Sender<Reply>>>,
    next_id: AtomicU64,
    /// The loopback port the page posts answers to.
    port: AtomicU16,
    /// Where the pane sits, in logical pixels.
    bounds: Mutex<(f64, f64, f64, f64)>,
}

impl Browser {
    pub fn new() -> Self {
        Self {
            webview: Mutex::new(None),
            pending: Mutex::new(HashMap::new()),
            next_id: AtomicU64::new(1),
            port: AtomicU16::new(0),
            bounds: Mutex::new((0.0, 0.0, 0.0, 0.0)),
        }
    }

    pub fn set_port(&self, port: u16) {
        self.port.store(port, Ordering::Relaxed);
    }

    pub fn port(&self) -> u16 {
        self.port.load(Ordering::Relaxed)
    }

    /// Open the pane on `url`, or navigate the one that is already open.
    pub fn open(&self, app: &AppHandle, url: &str) -> Result<(), String> {
        let mut slot = self.webview.lock().map_err(|_| "browser lock".to_string())?;
        if let Some(webview) = slot.as_ref() {
            let parsed = parse_url(url)?;
            webview
                .navigate(parsed)
                .map_err(|error| format!("navigate: {error}"))?;
            return Ok(());
        }
        let (x, y, w, h) = *self.bounds.lock().map_err(|_| "browser bounds".to_string())?;
        let window = app
            .get_window("main")
            .ok_or_else(|| "main window is gone".to_string())?;
        let script = page_runtime(self.port());
        let webview = window
            .add_child(
                tauri::WebviewBuilder::new("anvil-browser", tauri::WebviewUrl::External(parse_url(url)?))
                    .initialization_script(&script),
                LogicalPosition::new(x, y),
                LogicalSize::new(w.max(1.0), h.max(1.0)),
            )
            .map_err(|error| format!("open browser: {error}"))?;
        *slot = Some(webview);
        Ok(())
    }

    /// Move the pane. A closed pane is parked off-screen rather than destroyed:
    /// coming back should not reload the page the agent was in the middle of.
    pub fn set_bounds(&self, x: f64, y: f64, w: f64, h: f64) -> Result<(), String> {
        *self.bounds.lock().map_err(|_| "browser bounds".to_string())? = (x, y, w, h);
        let slot = self.webview.lock().map_err(|_| "browser lock".to_string())?;
        let Some(webview) = slot.as_ref() else {
            return Ok(());
        };
        let visible = w > 1.0 && h > 1.0;
        let (px, py, pw, ph) = if visible {
            (x, y, w, h)
        } else {
            (-20_000.0, -20_000.0, 1.0, 1.0)
        };
        webview
            .set_position(LogicalPosition::new(px, py))
            .map_err(|error| format!("browser position: {error}"))?;
        webview
            .set_size(LogicalSize::new(pw, ph))
            .map_err(|error| format!("browser size: {error}"))?;
        Ok(())
    }

    pub fn close(&self) -> Result<(), String> {
        let mut slot = self.webview.lock().map_err(|_| "browser lock".to_string())?;
        if let Some(webview) = slot.take() {
            webview.close().map_err(|error| format!("close browser: {error}"))?;
        }
        Ok(())
    }

    pub fn is_open(&self) -> bool {
        self.webview
            .lock()
            .map(|slot| slot.is_some())
            .unwrap_or(false)
    }

    /// A PNG of what the page looks like right now.
    ///
    /// Reading the DOM tells a caller what the page *says*; it cannot tell it
    /// whether the page actually rendered, whether a dialog is covering the
    /// button, or whether the layout broke. This is the other half.
    ///
    /// The picture goes to a file because that is what a caller can pass on:
    /// a tool result, an attachment, a preview. `target` is honoured when given
    /// (a relative path resolves against the working directory); otherwise the
    /// shot lands in the system temp folder, which every reader can reach.
    pub fn capture(&self, target: Option<&str>) -> Result<Value, String> {
        let webview = {
            let slot = self.webview.lock().map_err(|_| "browser lock".to_string())?;
            slot.as_ref()
                .cloned()
                .ok_or_else(|| "the browser pane is not open".to_string())?
        };
        // Let the page finish before looking at it: a shot taken mid-load is a
        // half-painted picture, which is worse than no picture because it looks
        // like an answer. A page that cannot answer still gets captured.
        let _ = self.call("settle", &Value::Null);
        let bytes = platform::capture_png(&webview)?;
        let path = shot_path(target);
        if let Some(dir) = path.parent()
            && !dir.as_os_str().is_empty()
        {
            std::fs::create_dir_all(dir).map_err(|error| format!("screenshot: {error}"))?;
        }
        std::fs::write(&path, &bytes).map_err(|error| format!("screenshot: {error}"))?;
        let (width, height) = png_size(&bytes);
        Ok(serde_json::json!({
            "path": path.display().to_string(),
            "bytes": bytes.len(),
            "width": width,
            "height": height,
        }))
    }

    /// Run a script in the page without waiting for an answer.
    ///
    /// Navigation verbs are fire-and-forget: the page that would answer is
    /// gone by the time it could, so waiting would only time out.
    pub fn run_script(&self, script: &str) -> Result<(), String> {
        let slot = self.webview.lock().map_err(|_| "browser lock".to_string())?;
        let Some(webview) = slot.as_ref() else {
            return Err("the browser pane is not open".to_string());
        };
        webview.eval(script).map_err(|error| format!("eval: {error}"))
    }

    /// Run one command in the page and wait for the page's own answer.
    pub fn call(&self, method: &str, params: &Value) -> Reply {
        let slot = self.webview.lock().map_err(|_| "browser lock".to_string())?;
        let Some(webview) = slot.as_ref() else {
            return Err("the browser pane is not open".to_string());
        };
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = channel();
        self.pending
            .lock()
            .map_err(|_| "browser pending".to_string())?
            .insert(id, tx);
        let script = format!(
            "window.__anvilBrowser && window.__anvilBrowser.run({id}, {}, {});",
            serde_json::to_string(method).unwrap_or_else(|_| "\"\"".to_string()),
            params
        );
        if let Err(error) = webview.eval(&script) {
            self.pending
                .lock()
                .map(|mut pending| pending.remove(&id))
                .ok();
            return Err(format!("eval: {error}"));
        }
        match rx.recv_timeout(COMMAND_TIMEOUT) {
            Ok(reply) => reply,
            Err(_) => {
                self.pending
                    .lock()
                    .map(|mut pending| pending.remove(&id))
                    .ok();
                Err(format!("the page did not answer `{method}` in time"))
            }
        }
    }

    /// Complete one pending command with the page's answer.
    pub fn settle(&self, id: u64, reply: Reply) {
        if let Ok(mut pending) = self.pending.lock()
            && let Some(tx) = pending.remove(&id)
        {
            let _ = tx.send(reply);
        }
    }
}

fn parse_url(url: &str) -> Result<tauri::Url, String> {
    let trimmed = url.trim();
    let candidate = if trimmed.contains("://") {
        trimmed.to_string()
    } else if trimmed.is_empty() {
        "about:blank".to_string()
    } else {
        format!("https://{trimmed}")
    };
    candidate
        .parse()
        .map_err(|error| format!("not a URL: {candidate} ({error})"))
}

/// The script every page in the pane carries.
///
/// It only knows how to look at and act on the page; the decisions are made by
/// whoever called the bridge.
fn page_runtime(port: u16) -> String {
    format!(
        r#"
(function () {{
  if (window.__anvilBrowser) return;
  var port = {port};
  function post(payload) {{
    try {{
      fetch('http://127.0.0.1:' + port + '/result', {{
        method: 'POST',
        headers: {{ 'content-type': 'application/json' }},
        body: JSON.stringify(payload)
      }});
    }} catch (error) {{ /* the host is gone; nothing to tell */ }}
  }}
  function describe(el) {{
    if (!el) return '';
    var tag = (el.tagName || '').toLowerCase();
    var text = (el.innerText || el.textContent || '').trim().replace(/\s+/g, ' ');
    return tag + (text ? ': ' + text.slice(0, 80) : '');
  }}
  function findAll(selector) {{
    try {{ return Array.from(document.querySelectorAll(selector)); }} catch (error) {{ return []; }}
  }}
  function byText(text) {{
    var wanted = String(text).trim();
    if (!wanted) return null;
    var nodes = Array.from(document.querySelectorAll('a,button,input,textarea,select,label,summary,[role="button"],[role="link"],[role="tab"],[contenteditable="true"]'));
    var hit = nodes.find(function (el) {{ return (el.innerText || el.textContent || '').trim() === wanted; }});
    if (hit) return hit;
    hit = nodes.find(function (el) {{ return (el.innerText || el.textContent || '').trim().indexOf(wanted) !== -1; }});
    if (hit) return hit;
    return Array.from(document.querySelectorAll('*')).find(function (el) {{
      return el.children.length === 0 && (el.innerText || el.textContent || '').trim().indexOf(wanted) !== -1;
    }}) || null;
  }}
  function pick(params) {{
    if (params && params.selector) {{
      var found = findAll(params.selector);
      var index = typeof params.index === 'number' ? params.index : 0;
      return found[index] || null;
    }}
    if (params && params.text) return byText(params.text);
    return null;
  }}
  function setValue(el, value) {{
    var proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype
      : el instanceof HTMLSelectElement ? HTMLSelectElement.prototype
      : HTMLInputElement.prototype;
    var setter = Object.getOwnPropertyDescriptor(proto, 'value');
    if (setter && setter.set) setter.set.call(el, value); else el.value = value;
    el.dispatchEvent(new Event('input', {{ bubbles: true }}));
    el.dispatchEvent(new Event('change', {{ bubbles: true }}));
  }}
  function snapshot(limit) {{
    var rows = [];
    var nodes = document.querySelectorAll('a,button,input,textarea,select,[role="button"],[role="link"]');
    for (var i = 0; i < nodes.length && rows.length < limit; i++) {{
      var el = nodes[i];
      var box = el.getBoundingClientRect();
      if (box.width < 1 || box.height < 1) continue;
      rows.push({{
        tag: (el.tagName || '').toLowerCase(),
        type: el.getAttribute('type') || '',
        text: (el.innerText || el.value || el.getAttribute('aria-label') || el.getAttribute('placeholder') || '').trim().slice(0, 100),
        href: el.getAttribute('href') || '',
        name: el.getAttribute('name') || ''
      }});
    }}
    return rows;
  }}
  window.__anvilBrowser = {{
    run: function (id, method, params) {{
      var out;
      try {{
        out = {{ ok: true, value: null }};
        params = params || {{}};
        if (method === 'text') {{
          out.value = (document.body ? document.body.innerText : '').slice(0, 30000);
        }} else if (method === 'title') {{
          out.value = document.title;
        }} else if (method === 'url') {{
          out.value = location.href;
        }} else if (method === 'html') {{
          var el = params.selector ? document.querySelector(params.selector) : document.documentElement;
          out.value = el ? el.outerHTML.slice(0, 60000) : '';
        }} else if (method === 'metrics') {{
          out.value = {{
            url: location.href,
            title: document.title,
            width: window.innerWidth,
            height: window.innerHeight,
            scrollY: Math.round(window.scrollY)
          }};
        }} else if (method === 'snapshot') {{
          out.value = snapshot(params.limit || 80);
        }} else if (method === 'click') {{
          var target = pick(params);
          if (!target) throw new Error('nothing matches ' + JSON.stringify(params));
          target.scrollIntoView({{ block: 'center' }});
          target.click();
          out.value = describe(target);
        }} else if (method === 'fill') {{
          var field = pick(params);
          if (!field) throw new Error('nothing matches ' + JSON.stringify(params));
          field.focus();
          setValue(field, params.value == null ? '' : String(params.value));
          out.value = describe(field);
        }} else if (method === 'press') {{
          var key = String(params.key || 'Enter');
          var box = pick(params) || document.activeElement || document.body;
          box.dispatchEvent(new KeyboardEvent('keydown', {{ key: key, bubbles: true }}));
          box.dispatchEvent(new KeyboardEvent('keyup', {{ key: key, bubbles: true }}));
          out.value = key;
        }} else if (method === 'scroll') {{
          var dy = Number(params.dy || 600);
          window.scrollBy(0, dy);
          out.value = Math.round(window.scrollY);
        }} else if (method === 'settle') {{
          // A capture returns the last frame the compositor finished, and a
          // page that is still loading has not finished one. Wait for the load,
          // then for the paint that follows it.
          var deadline = Date.now() + Math.min(Number(params.ms || 6000), 10000);
          var quiet = Number(params.quiet == null ? 260 : params.quiet);
          (function poll() {{
            if (document.readyState !== 'complete' && Date.now() < deadline) return setTimeout(poll, 80);
            setTimeout(function () {{
              post({{ id: id, ok: true, value: {{ readyState: document.readyState }} }});
            }}, quiet);
          }})();
          return;
        }} else if (method === 'wait') {{
          var ms = Math.min(Number(params.ms || 500), 5000);
          return setTimeout(function () {{ post({{ id: id, ok: true, value: ms }}); }}, ms);
        }} else {{
          throw new Error('unknown method: ' + method);
        }}
      }} catch (error) {{
        out = {{ ok: false, value: String((error && error.message) || error) }};
      }}
      post({{ id: id, ok: out.ok, value: out.value }});
    }}
  }};
}})();
"#
    )
}

/// Managed handle for the commands.
pub struct BrowserHandle(pub std::sync::Arc<Browser>);

impl BrowserHandle {
    pub fn get(&self) -> std::sync::Arc<Browser> {
        self.0.clone()
    }
}

/// Open the pane (or navigate the page already in it).
///
/// Async on purpose: `add_child` hands the work to the main thread and waits
/// for it, so a command that already runs on the main thread would wait for
/// itself. Async commands run on the pool instead.
#[tauri::command]
pub async fn browser_open(
    app: AppHandle,
    state: tauri::State<'_, BrowserHandle>,
    url: String,
) -> Result<(), String> {
    state.get().open(&app, &url)
}

/// Place the native view over the pane's content rect. A zero size parks it
/// off-screen without losing the page.
#[tauri::command]
pub async fn browser_bounds(
    state: tauri::State<'_, BrowserHandle>,
    x: f64,
    y: f64,
    w: f64,
    h: f64,
) -> Result<(), String> {
    state.get().set_bounds(x, y, w, h)
}

#[tauri::command]
pub async fn browser_close(state: tauri::State<'_, BrowserHandle>) -> Result<(), String> {
    state.get().close()
}

/// Whether the pane currently holds a page, and the bridge port it answers on.
#[tauri::command]
pub fn browser_status(state: tauri::State<'_, BrowserHandle>) -> serde_json::Value {
    let browser = state.get();
    serde_json::json!({ "open": browser.is_open(), "port": browser.port() })
}

/// Navigation verbs the pane's own toolbar uses.
#[tauri::command]
pub async fn browser_action(
    state: tauri::State<'_, BrowserHandle>,
    method: String,
) -> Result<(), String> {
    let browser = state.get();
    let script = match method.as_str() {
        "back" => "history.back()",
        "forward" => "history.forward()",
        "reload" => "location.reload()",
        other => return Err(format!("unknown browser action: {other}")),
    };
    browser.run_script(script)
}

/// Where a screenshot goes when the caller did not say.
fn shot_path(target: Option<&str>) -> std::path::PathBuf {
    match target.map(str::trim).filter(|value| !value.is_empty()) {
        Some(raw) => std::path::PathBuf::from(raw),
        None => {
            let stamp = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|gap| gap.as_millis())
                .unwrap_or(0);
            std::env::temp_dir()
                .join("anvil-shots")
                .join(format!("shot-{stamp}.png"))
        }
    }
}

/// The pixel size out of a PNG header, so the caller knows what it got.
fn png_size(bytes: &[u8]) -> (u32, u32) {
    if bytes.len() < 24 || &bytes[0..8] != b"\x89PNG\r\n\x1a\n" {
        return (0, 0);
    }
    let read = |at: usize| {
        u32::from_be_bytes([bytes[at], bytes[at + 1], bytes[at + 2], bytes[at + 3]])
    };
    (read(16), read(20))
}

#[cfg(windows)]
mod platform {
    use std::sync::mpsc;
    use std::time::Duration;

    use tauri::webview::Webview;
    use webview2_com::Microsoft::Web::WebView2::Win32 as wv2;
    use webview2_com::CapturePreviewCompletedHandler;
    use windows::Win32::Foundation::HGLOBAL;
    use windows::Win32::System::Com::StructuredStorage::{CreateStreamOnHGlobal, GetHGlobalFromStream};
    use windows::Win32::System::Com::IStream;
    use windows::Win32::System::Memory::{GlobalLock, GlobalSize, GlobalUnlock};

    /// Ask WebView2 to rasterise the page, and wait for the pixels.
    ///
    /// The wait happens here, on a worker thread, and not inside the UI
    /// closure: the completion handler is delivered by the UI message loop, so
    /// blocking that loop would be waiting for a callback that cannot run.
    pub fn capture_png(webview: &Webview<tauri::Wry>) -> Result<Vec<u8>, String> {
        let (tx, rx) = mpsc::channel::<Result<Vec<u8>, String>>();
        let reporter = tx.clone();
        webview
            .with_webview(move |platform| {
                if let Err(error) = begin(&platform.controller(), reporter.clone()) {
                    let _ = reporter.send(Err(error));
                }
            })
            .map_err(|error| format!("screenshot: {error}"))?;
        match rx.recv_timeout(Duration::from_secs(20)) {
            Ok(outcome) => outcome,
            Err(_) => Err("the page did not produce a screenshot in time".to_string()),
        }
    }

    fn begin(
        controller: &wv2::ICoreWebView2Controller,
        tx: mpsc::Sender<Result<Vec<u8>, String>>,
    ) -> Result<(), String> {
        // A memory-backed stream: no temp file, and the handler can read it the
        // moment the capture finishes.
        let (webview, stream): (wv2::ICoreWebView2, IStream) = unsafe {
            let webview = controller
                .CoreWebView2()
                .map_err(|error| format!("screenshot: {error}"))?;
            let stream = CreateStreamOnHGlobal(HGLOBAL(std::ptr::null_mut()), true)
                .map_err(|error| format!("screenshot: {error}"))?;
            (webview, stream)
        };
        let sink = stream.clone();
        let handler = CapturePreviewCompletedHandler::create(Box::new(move |result| {
            let outcome = match result {
                Ok(()) => read(&sink),
                Err(error) => Err(format!("screenshot: {error}")),
            };
            let _ = tx.send(outcome);
            Ok(())
        }));
        unsafe {
            webview
                .CapturePreview(
                    wv2::COREWEBVIEW2_CAPTURE_PREVIEW_IMAGE_FORMAT_PNG,
                    &stream,
                    &handler,
                )
                .map_err(|error| format!("screenshot: {error}"))
        }
    }

    fn read(stream: &IStream) -> Result<Vec<u8>, String> {
        unsafe {
            let memory =
                GetHGlobalFromStream(stream).map_err(|error| format!("screenshot: {error}"))?;
            let size = GlobalSize(memory);
            if size == 0 {
                return Err("the page produced an empty image".to_string());
            }
            let start = GlobalLock(memory) as *const u8;
            if start.is_null() {
                return Err("screenshot memory could not be read".to_string());
            }
            let bytes = std::slice::from_raw_parts(start, size).to_vec();
            let _ = GlobalUnlock(memory);
            Ok(bytes)
        }
    }
}

#[cfg(not(windows))]
mod platform {
    use tauri::webview::Webview;

    pub fn capture_png(_webview: &Webview<tauri::Wry>) -> Result<Vec<u8>, String> {
        Err("this build cannot capture the browser pane on this platform".to_string())
    }
}
