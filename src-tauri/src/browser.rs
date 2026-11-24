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
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, AtomicU16, AtomicU64, Ordering};
use std::sync::mpsc::{Sender, channel};
use std::time::Duration;

use serde_json::Value;
use tauri::webview::Webview;
use tauri::{AppHandle, LogicalPosition, LogicalSize, Manager};

/// Place the pane's child webview, on every platform, through Tauri's own
/// `set_bounds`. The page reports a DOM rect (top-left origin, y down) in the
/// main webview's viewport; wry's macOS backend feeds that same rect to its
/// `window_position`, which flips y against the superview's `isFlipped`. That
/// is the one conversion Tauri already gets right on all four platforms.
///
/// A previous attempt replaced this with an AppKit `convertRect:fromView:`
/// using the main webview as `fromView`. It was wrong twice over: a DOM rect is
/// not an NSView *bounds* rect (different origin and y direction), and every
/// placement needed three `with_webview` hops to the main thread with a 3s
/// timeout. Under the pane's high-frequency rect reports those hops timed out,
/// the errors were swallowed, and the view stopped following the pane entirely
/// — "it just sits there, resize and close do nothing".
fn place_view(app: &AppHandle, x: f64, y: f64, w: f64, h: f64) -> Result<(f64, f64, f64, f64), String> {
    let webview = app
        .get_webview("anvil-browser")
        .ok_or_else(|| "browser webview not found".to_string())?;
    let (px, py, pw, ph) = placement(x, y, w, h);
    webview
        .set_bounds(tauri::Rect {
            position: tauri::Position::Logical(LogicalPosition::new(px, py)),
            size: tauri::Size::Logical(LogicalSize::new(pw, ph)),
        })
        .map_err(|error| format!("browser bounds: {error}"))?;
    // macOS may refuse to move an off-screen/animating view; read back what the
    // platform actually took so the watchdog can compare like with like.
    let read = webview.bounds().ok().and_then(logical_of).unwrap_or((x, y, w, h));
    Ok(read)
}

/// Read the pane's frame back in the same coordinates `getBoundingClientRect`
/// reports, so a drift check compares like with like.
fn read_view(app: &AppHandle) -> Option<(f64, f64, f64, f64)> {
    let webview = app.get_webview("anvil-browser")?;
    webview.bounds().ok().and_then(logical_of)
}

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
    /// A URL asked for while the pane had no rect to place a view on. It waits
    /// here until the pane reports one, because a webview created at a
    /// degenerate rect is created off-screen — and a page that starts its life
    /// there may never commit its first navigation, which leaves every
    /// following command waiting for a script the page never ran.
    pending: Mutex<Option<String>>,
    /// Commands waiting for the page to post their answer.
    pending_replies: Mutex<HashMap<u64, Sender<Reply>>>,
    next_id: AtomicU64,
    /// The loopback port the page posts answers to.
    port: AtomicU16,
    /// Where the pane sits, in logical pixels.
    bounds: Mutex<(f64, f64, f64, f64)>,
    /// How many times the native view was actually moved. A number the pane can
    /// be asked for, because "it keeps flickering" needs a count and not an
    /// opinion.
    placements: AtomicU64,
}

impl Browser {
    pub fn new() -> Self {
        Self {
            webview: Mutex::new(None),
            pending: Mutex::new(None),
            pending_replies: Mutex::new(HashMap::new()),
            next_id: AtomicU64::new(1),
            port: AtomicU16::new(0),
            bounds: Mutex::new((0.0, 0.0, 0.0, 0.0)),
            placements: AtomicU64::new(0),
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
        let mut slot = self
            .webview
            .lock()
            .map_err(|_| "browser lock".to_string())?;
        if let Some(webview) = slot.as_ref() {
            let parsed = parse_url(url)?;
            webview
                .navigate(parsed)
                .map_err(|error| format!("navigate: {error}"))?;
            return Ok(());
        }
        let (_, _, w, h) = *self
            .bounds
            .lock()
            .map_err(|_| "browser bounds".to_string())?;
        if w <= 1.0 || h <= 1.0 {
            // No rect to place a view on yet. The pane is told to come forward
            // (`SHOW_EVENT`); when it does, its first rect report creates the
            // view on-screen. Creating it here would park it off-screen at
            // 1×1, and a page born there can stall before its first commit —
            // every command after that waits for a script that never ran,
            // which is exactly "the site stays blank and `text` times out".
            *self
                .pending
                .lock()
                .map_err(|_| "browser pending".to_string())? = Some(url.to_string());
            return Ok(());
        }
        self.spawn(app, url, &mut slot)
    }

    /// Create the webview at the rect the pane last reported. The caller holds
    /// the webview lock; `add_child` blocks on the main thread, and nothing on
    /// the main thread takes that lock, so the wait is safe.
    fn spawn(
        &self,
        app: &AppHandle,
        url: &str,
        slot: &mut Option<Webview<tauri::Wry>>,
    ) -> Result<(), String> {
        let (x, y, w, h) = *self
            .bounds
            .lock()
            .map_err(|_| "browser bounds".to_string())?;
        let window = app
            .get_window("main")
            .ok_or_else(|| "main window is gone".to_string())?;
        let script = page_runtime(self.port());
        let (px, py, pw, ph) = placement(x, y, w, h);
        let webview = window
            .add_child(
                tauri::WebviewBuilder::new(
                    "anvil-browser",
                    tauri::WebviewUrl::External(parse_url(url)?),
                )
                .initialization_script(&script),
                LogicalPosition::new(px, py),
                LogicalSize::new(pw, ph),
            )
            .map_err(|error| format!("open browser: {error}"))?;
        // wry creates a child view with `NSViewMinYMargin` ("keep the bottom
        // margin"). That mask is AppKit's licence to move the view by itself on
        // every window resize, and it acts *after* the page has reported the
        // pane's rect — so maximising the window slid the page up and left it
        // there. The rect we place is the only authority; drop the mask.
        #[cfg(target_os = "macos")]
        pin_view(&webview);
        *slot = Some(webview);
        // The view was created at whatever rect the pane had reported so far. A
        // report that arrives as the pane settles (the address bar appearing,
        // a tab change) updates that rect; this places the view there once the
        // layout is quiet, so a page is never created over the address bar.
        #[cfg(target_os = "macos")]
        settle_later(app);
        Ok(())
    }

    /// Move the pane. A closed pane is parked off-screen rather than destroyed:
    /// coming back should not reload the page the agent was in the middle of.
    pub fn set_bounds(&self, app: &AppHandle, x: f64, y: f64, w: f64, h: f64) -> Result<(), String> {
        let real = w > 1.0 && h > 1.0;
        let mut slot = self
            .webview
            .lock()
            .map_err(|_| "browser lock".to_string())?;
        if slot.is_some() {
            let same = *self
                .bounds
                .lock()
                .map_err(|_| "browser bounds".to_string())?
                == (x, y, w, h);
            if same {
                // Same place, same size. Placing a webview makes it lay itself out
                // again, and a repeated placement is a visible flicker with nothing
                // to show for it.
                return Ok(());
            }
            return match place_view(app, x, y, w, h) {
                Ok(_) => {
                    *self
                        .bounds
                        .lock()
                        .map_err(|_| "browser bounds".to_string())? = (x, y, w, h);
                    self.placements.fetch_add(1, Ordering::Relaxed);
                    Ok(())
                }
                Err(error) => {
                    // A failed placement must not be remembered. The pane keeps
                    // reporting the same rect, and the check above would swallow
                    // every retry — the native view would sit at its old place for
                    // good, which is exactly what "the page drifted and stayed
                    // there" looks like.
                    Err(format!("browser bounds: {error}"))
                }
            };
        }
        // There is no view yet. Remembering the rect — even a degenerate one,
        // which is a closed pane — is what makes the next `open` or the report
        // below create the view where the pane already is. A page the runtime
        // asked for while the pane had no rect has been waiting for exactly
        // this report: the pane's first real rect, which is where the view is
        // created — on-screen, where its first navigation can commit and its
        // script can answer.
        *self
            .bounds
            .lock()
            .map_err(|_| "browser bounds".to_string())? = (x, y, w, h);
        if real
            && let Some(url) = self
                .pending
                .lock()
                .map_err(|_| "browser pending".to_string())?
                .take()
        {
            return self.spawn(app, &url, &mut slot);
        }
        Ok(())
    }

    /// Place the view at the last reported rect again, ignoring the "same
    /// place" shortcut, and report what the native view says its frame is.
    ///
    /// The readback is the point: a placement the platform ignored looks
    /// exactly like a placement that arrived too early, and only the view's own
    /// frame tells them apart. It travels back to the host log, so "it still
    /// drifts" can be answered with a number instead of another guess.
    pub fn reapply(&self, app: &AppHandle) -> Result<String, String> {
        let (x, y, w, h) = *self
            .bounds
            .lock()
            .map_err(|_| "browser bounds".to_string())?;
        if !self.is_open() {
            return Ok("no webview".to_string());
        }
        let (px, py, pw, ph) = placement(x, y, w, h);
        // On macOS the placement also brings the view to the front, so a
        // theme switch cannot bury the page under the app's own webview.
        let read = place_view(app, x, y, w, h)?;
        self.placements.fetch_add(1, Ordering::Relaxed);
        let asked = format!("{px:.0},{py:.0} {pw:.0}x{ph:.0}");
        Ok(format!(
            "asked {asked}; view reports {:.0},{:.0} {:.0}x{:.0}",
            read.0, read.1, read.2, read.3
        ))
    }

    /// Puts the view back if it is not where the pane says it should be.
    ///
    /// The last resort against a platform that moves a child view behind the
    /// app's back: one readback every second and a half, and a placement only
    /// when the numbers disagree. A parked pane (no size) is left alone — it is
    /// off-screen on purpose.
    pub fn check(&self, app: &AppHandle) -> Result<Option<String>, String> {
        let want = *self
            .bounds
            .lock()
            .map_err(|_| "browser bounds".to_string())?;
        if want.2 <= 1.0 || want.3 <= 1.0 || !self.is_open() {
            return Ok(None);
        }
        let Some(actual) = read_view(app) else {
            return Ok(None);
        };
        let close_enough = (actual.0 - want.0).abs() <= 1.5
            && (actual.1 - want.1).abs() <= 1.5
            && (actual.2 - want.2).abs() <= 1.5
            && (actual.3 - want.3).abs() <= 1.5;
        if close_enough {
            return Ok(None);
        }
        let found = format!(
            "{:.0},{:.0} {:.0}x{:.0}",
            actual.0, actual.1, actual.2, actual.3
        );
        place_view(app, want.0, want.1, want.2, want.3)?;
        self.placements.fetch_add(1, Ordering::Relaxed);
        Ok(Some(format!(
            "was at {found}, put back on {:.0},{:.0} {:.0}x{:.0}",
            want.0, want.1, want.2, want.3
        )))
    }

    pub fn close(&self) -> Result<(), String> {
        let mut slot = self
            .webview
            .lock()
            .map_err(|_| "browser lock".to_string())?;
        if let Some(webview) = slot.take() {
            webview
                .close()
                .map_err(|error| format!("close browser: {error}"))?;
        }
        // A URL waiting for a rect belongs to the view that is now gone; the
        // next `open` decides again where its page starts life.
        *self
            .pending
            .lock()
            .map_err(|_| "browser pending".to_string())? = None;
        Ok(())
    }

    /// How many real placements the view has had since launch.
    pub fn placements(&self) -> u64 {
        self.placements.load(Ordering::Relaxed)
    }

    pub fn is_open(&self) -> bool {
        self.webview
            .lock()
            .map(|slot| slot.is_some())
            .unwrap_or(false)
    }

    /// The address the page is on, for the watchdog's log. A page that stays on
    /// `about:blank` after a navigation never committed it; that is the one
    /// fact that separates "the network never answered" from "the script did".
    pub fn page_url(&self) -> Option<String> {
        let slot = self.webview.lock().ok()?;
        let webview = slot.as_ref()?;
        webview.url().ok().map(|url| url.to_string())
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
            let slot = self
                .webview
                .lock()
                .map_err(|_| "browser lock".to_string())?;
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
        let slot = self
            .webview
            .lock()
            .map_err(|_| "browser lock".to_string())?;
        let Some(webview) = slot.as_ref() else {
            return Err("the browser pane is not open".to_string());
        };
        webview
            .eval(script)
            .map_err(|error| format!("eval: {error}"))
    }

    /// Run one command in the page and wait for the page's own answer.
    pub fn call(&self, method: &str, params: &Value) -> Reply {
        let slot = self
            .webview
            .lock()
            .map_err(|_| "browser lock".to_string())?;
        let Some(webview) = slot.as_ref() else {
            return Err("the browser pane is not open".to_string());
        };
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = channel();
        self.pending_replies
            .lock()
            .map_err(|_| "browser pending".to_string())?
            .insert(id, tx);
        let script = format!(
            "window.__anvilBrowser && window.__anvilBrowser.run({id}, {}, {});",
            serde_json::to_string(method).unwrap_or_else(|_| "\"\"".to_string()),
            params
        );
        if let Err(error) = webview.eval(&script) {
            self.pending_replies
                .lock()
                .map(|mut pending| pending.remove(&id))
                .ok();
            return Err(format!("eval: {error}"));
        }
        match rx.recv_timeout(COMMAND_TIMEOUT) {
            Ok(reply) => reply,
            Err(_) => {
                self.pending_replies
                    .lock()
                    .map(|mut pending| pending.remove(&id))
                    .ok();
                // The page's own address in the timeout turns the CLI output
                // into the diagnosis: a page that is still on `about:blank`
                // never committed its navigation, while a page on the asked-for
                // URL has the script and chose not to answer.
                let page = webview
                    .url()
                    .map(|url| url.to_string())
                    .unwrap_or_else(|_| "unreadable".to_string());
                Err(format!(
                    "the page did not answer `{method}` in time (page: {page})"
                ))
            }
        }
    }

    /// Complete one pending command with the page's answer.
    pub fn settle(&self, id: u64, reply: Reply) {
        if let Ok(mut pending) = self.pending_replies.lock()
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
        // `r##` because the page carries CSS colours like `fill="#ffffff"`, and
        // `"#` would end a single-hash raw string in the middle of the script.
        r##"
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
  var actionRoot = null, actionArrow = null, actionPath = null, actionBubble = null, actionTimer = null;
  function ensureActionUi() {{
    if (actionRoot && document.documentElement.contains(actionRoot)) return;
    actionRoot = document.createElement('div');
    actionRoot.id = '__anvil_action';
    actionRoot.setAttribute('data-anvil-ui', '1');
    actionRoot.style.cssText = 'all:initial;position:fixed;left:0;top:0;z-index:2147483647;pointer-events:none;opacity:0;transition:opacity .12s ease';
    actionArrow = document.createElement('div');
    actionArrow.style.cssText = 'all:initial;position:absolute;left:0;top:0;width:20px;height:20px;transition:transform .34s cubic-bezier(.22,.7,.2,1)';
    // A dark arrow with a light outline: it has to read on a white page and on
    // a black one, and the outline is what buys that.
    actionArrow.innerHTML = '<svg width="20" height="20" viewBox="0 0 20 20" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M3.4 2.2 L3.4 15.6 L7.6 11.5 L10.6 17.7 L12.9 16.6 L10 10.6 L15.6 10.6 Z" fill="#141416" stroke="#ffffff" stroke-width="1.7" stroke-linejoin="round"/></svg>';
    actionPath = actionArrow.querySelector('path');
    actionBubble = document.createElement('div');
    // A capsule, not a card: one short line, read at a glance, then gone.
    actionBubble.style.cssText = 'all:initial;position:absolute;left:0;top:0;max-width:300px;padding:5px 12px;border-radius:999px;background:rgba(22,22,24,.86);color:#ffffff;font:12.5px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;letter-spacing:.2px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;box-shadow:0 0 0 1px rgba(255,255,255,.14),0 3px 14px rgba(0,0,0,.45);transition:transform .34s cubic-bezier(.22,.7,.2,1)';
    actionRoot.appendChild(actionArrow);
    actionRoot.appendChild(actionBubble);
    (document.body || document.documentElement).appendChild(actionRoot);
  }}
  function clip(text, limit) {{
    var value = String(text == null ? '' : text).replace(/\s+/g, ' ').trim();
    return value.length > limit ? value.slice(0, limit) + '…' : value;
  }}
  function shortOf(el) {{
    var text = el.getAttribute('aria-label') || el.getAttribute('placeholder') || (el.tagName === 'INPUT' ? el.value : '') || el.innerText || '';
    var clean = String(text).replace(/\s+/g, ' ').trim();
    if (clean) return clip(clean, 16);
    var tag = (el.tagName || '').toLowerCase();
    if (tag === 'input' || tag === 'textarea') return '输入框';
    if (tag === 'a') return '链接';
    if (tag === 'button') return '按钮';
    return tag;
  }}
  // Is the surface under this point dark? The cursor has to read on both: a
  // black arrow with a white rim on a light page, the inverse on a dark one.
  function underIsDark(x, y) {{
    var node = document.elementFromPoint(x, y);
    while (node) {{
      var bg = '';
      try {{ bg = getComputedStyle(node).backgroundColor || ''; }} catch (error) {{ bg = ''; }}
      var parts = /rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)/.exec(bg);
      if (parts) {{
        var alpha = parts[4] === undefined ? 1 : Number(parts[4]);
        if (alpha > 0.5) {{
          var luminance = (0.2126 * Number(parts[1]) + 0.7152 * Number(parts[2]) + 0.0722 * Number(parts[3])) / 255;
          return luminance < 0.5;
        }}
      }}
      node = node.parentElement;
    }}
    return false;
  }}
  // Where the agent is about to act, said in one short line. It is drawn inside
  // the page because that is the only layer above the native view: the app's own
  // DOM sits underneath it.
  function showAction(x, y, label) {{
    ensureActionUi();
    actionBubble.textContent = label;
    var w = actionBubble.offsetWidth, h = actionBubble.offsetHeight;
    // Beside the pointer, its middle a little below the tip — the shape the
    // reference puts on screen.
    var bx = x + 14, by = y + 8;
    if (bx + w > window.innerWidth - 8) bx = x - w - 10;
    if (by + h > window.innerHeight - 8) by = y - h - 6;
    if (actionPath) {{
      var dark = underIsDark(x, y);
      actionPath.setAttribute('fill', dark ? '#ffffff' : '#141416');
      actionPath.setAttribute('stroke', dark ? '#141416' : '#ffffff');
    }}
    actionRoot.style.opacity = '1';
    actionArrow.style.transform = 'translate(' + Math.round(x) + 'px,' + Math.round(y) + 'px)';
    actionBubble.style.transform = 'translate(' + Math.round(bx) + 'px,' + Math.round(by) + 'px)';
    if (actionTimer) clearTimeout(actionTimer);
    actionTimer = setTimeout(function () {{ if (actionRoot) actionRoot.style.opacity = '0'; }}, 2400);
  }}
  // Let the pointer arrive before the page reacts: long enough to read as a
  // movement, short enough that a run of steps does not feel stuck.
  function actLater(id, act) {{
    return setTimeout(function () {{
      var out;
      try {{ out = {{ ok: true, value: act() }}; }}
      catch (error) {{ out = {{ ok: false, value: String((error && error.message) || error) }}; }}
      post({{ id: id, ok: out.ok, value: out.value }});
    }}, 200);
  }}

  // The pane's own context menu. WebView2's native menu would cover the page
  // with commands that act on a browser the reader cannot see, so every
  // contextmenu is taken here and only actions this pane can really do are
  // offered. The menu lives in the page because that is the only layer above
  // the native view.
  var contextRoot = null, contextDismiss = null;
  var CONTEXT_ICONS = {{
    back: '<path d="M14 5 7 12l7 7" />',
    forward: '<path d="m10 5 7 7-7 7" />',
    reload: '<path d="M18 12a6 6 0 1 1-2-4.5M18 4v4h-4" />',
    open: '<path d="M13 5h6v6M19 5l-9 9M18 13v6H5V6h6" />',
    link: '<path d="M10 14a4 4 0 0 0 6 .4l2-2a4 4 0 0 0-5.7-5.7L11 8M14 10a4 4 0 0 0-6-.4l-2 2A4 4 0 0 0 11.7 17L13 16" />',
    copy: '<rect x="8" y="8" width="12" height="12" rx="2" /><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" />',
    select: '<path d="M4 6h16M4 12h10M4 18h7" />',
  }};
  function contextIcon(name) {{
    return '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
      (CONTEXT_ICONS[name] || CONTEXT_ICONS.copy) + '</svg>';
  }}
  function legacyCopy(text) {{
    try {{
      var field = document.createElement('textarea');
      field.value = text;
      field.setAttribute('readonly', '');
      field.style.cssText = 'position:fixed;top:-1000px;opacity:0';
      document.body.appendChild(field);
      field.select();
      var ok = typeof document.execCommand === 'function' ? document.execCommand('copy') : false;
      field.remove();
      return ok;
    }} catch (error) {{ return false; }}
  }}
  function contextCopy(text, x, y) {{
    if (!text) return;
    try {{
      if (navigator.clipboard && navigator.clipboard.writeText) {{
        navigator.clipboard.writeText(text).catch(function () {{ legacyCopy(text); }});
      }} else legacyCopy(text);
    }} catch (error) {{ legacyCopy(text); }}
    if (typeof showAction === 'function') showAction(x, y, '已复制');
  }}
  function contextItems(event) {{
    var node = event.target && event.target.nodeType === 1 ? event.target : document.elementFromPoint(event.clientX, event.clientY);
    var link = node && node.closest ? node.closest('a[href]') : null;
    var image = node && node.closest ? node.closest('img[src]') : null;
    var editable = node && node.closest ? node.closest('input,textarea,[contenteditable="true"]') : null;
    var selected = String((window.getSelection() || {{}}).toString() || '').trim();
    var items = [
      {{ icon: 'back', label: '返回', action: 'back' }},
      {{ icon: 'forward', label: '前进', action: 'forward' }},
      {{ icon: 'reload', label: '重新加载', action: 'reload' }},
    ];
    if (link) {{
      items.push({{ divider: true }});
      items.push({{ icon: 'open', label: '在此面板打开链接', action: 'openLink', href: link.href }});
      items.push({{ icon: 'link', label: '复制链接地址', action: 'copy', value: link.href }});
    }}
    if (image) {{
      items.push({{ divider: true }});
      items.push({{ icon: 'open', label: '查看图片', action: 'openLink', href: image.src }});
      items.push({{ icon: 'link', label: '复制图片地址', action: 'copy', value: image.src }});
    }}
    if (selected) {{
      items.push({{ divider: true }});
      items.push({{ icon: 'copy', label: '复制选中内容', action: 'copy', value: selected }});
    }}
    if (editable) {{
      items.push({{ divider: true }});
      items.push({{ icon: 'select', label: '全选', action: 'selectAll', field: editable }});
    }}
    items.push({{ divider: true }});
    items.push({{ icon: 'link', label: '复制页面地址', action: 'copy', value: location.href }});
    return items;
  }}
  function closeContext() {{
    if (!contextRoot) return;
    var root = contextRoot;
    contextRoot = null;
    if (contextDismiss) {{ document.removeEventListener('pointerdown', contextDismiss, true); contextDismiss = null; }}
    root.style.opacity = '0';
    root.style.transform = 'scale(.97)';
    window.setTimeout(function () {{ root.remove(); }}, 120);
  }}
  function openContext(event) {{
    event.preventDefault();
    event.stopPropagation();
    closeContext();
    var items = contextItems(event);
    var root = document.createElement('div');
    root.id = '__anvil_context';
    root.setAttribute('data-anvil-ui', '1');
    root.setAttribute('role', 'menu');
    root.style.cssText = 'all:initial;position:fixed;left:0;top:0;z-index:2147483646;display:block;min-width:172px;max-width:min(280px,90vw);padding:5px;border-radius:12px;background:rgba(24,25,29,.94);border:1px solid rgba(255,255,255,.14);box-shadow:0 12px 34px rgba(0,0,0,.38);color:#f5f6f8;font:12.5px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;opacity:0;transform:scale(.97);transform-origin:top left;transition:opacity .12s ease,transform .12s ease;backdrop-filter:blur(10px)';
    items.forEach(function (item) {{
      if (item.divider) {{
        var line = document.createElement('div');
        line.style.cssText = 'height:1px;margin:4px 6px;background:rgba(255,255,255,.12)';
        root.appendChild(line);
        return;
      }}
      var button = document.createElement('button');
      button.type = 'button';
      button.setAttribute('role', 'menuitem');
      button.setAttribute('data-action', item.action);
      button.setAttribute('data-label', item.label);
      button.style.cssText = 'all:unset;display:flex;align-items:center;gap:9px;width:100%;box-sizing:border-box;padding:6px 9px;border-radius:8px;color:inherit;cursor:pointer;white-space:nowrap';
      button.innerHTML = contextIcon(item.icon) + '<span>' + item.label + '</span>';
      button.addEventListener('mouseenter', function () {{ button.style.background = 'rgba(255,255,255,.12)'; }});
      button.addEventListener('mouseleave', function () {{ button.style.background = 'transparent'; }});
      button.addEventListener('click', function (clickEvent) {{
        clickEvent.preventDefault();
        clickEvent.stopPropagation();
        var x = clickEvent.clientX || event.clientX, y = clickEvent.clientY || event.clientY;
        if (item.action === 'back') history.back();
        else if (item.action === 'forward') history.forward();
        else if (item.action === 'reload') location.reload();
        else if (item.action === 'openLink' && item.href) location.href = item.href;
        else if (item.action === 'copy') contextCopy(item.value, x, y);
        else if (item.action === 'selectAll') {{
          try {{ item.field.focus(); item.field.select(); }} catch (error) {{ /* detached */ }}
        }}
        closeContext();
      }});
      root.appendChild(button);
    }});
    (document.body || document.documentElement).appendChild(root);
    var box = root.getBoundingClientRect();
    var x = Math.max(6, Math.min(event.clientX, window.innerWidth - box.width - 6));
    var y = Math.max(6, Math.min(event.clientY, window.innerHeight - box.height - 6));
    root.style.transform = 'translate(' + Math.round(x) + 'px,' + Math.round(y) + 'px) scale(.97)';
    contextRoot = root;
    window.requestAnimationFrame(function () {{
      if (contextRoot === root) root.style.transform = 'translate(' + Math.round(x) + 'px,' + Math.round(y) + 'px) scale(1)';
    }});
    root.style.opacity = '1';
    contextDismiss = function (pointer) {{
      if (!root.contains(pointer.target)) closeContext();
    }};
    document.addEventListener('pointerdown', contextDismiss, true);
  }}
  document.addEventListener('contextmenu', openContext, true);
  document.addEventListener('keydown', function (event) {{
    if (event.key === 'Escape') closeContext();
  }}, true);
  window.addEventListener('scroll', closeContext, true);
  window.addEventListener('resize', closeContext);

  window.__anvilBrowser = {{
    run: function (id, method, params) {{
      var out;
      try {{
        out = {{ ok: true, value: null }};
        params = params || {{}};
        if (method === 'menu') {{
          var mx = Number(params.x || 120), my = Number(params.y || 120);
          if (params.selector) {{
            var anchor = document.querySelector(params.selector);
            if (anchor) {{
              var rect = anchor.getBoundingClientRect();
              mx = rect.left + rect.width / 2;
              my = rect.top + rect.height / 2;
            }}
          }}
          openContext({{
            clientX: mx, clientY: my,
            target: document.elementFromPoint(mx, my),
            preventDefault: function () {{}},
            stopPropagation: function () {{}}
          }});
          out.value = {{ items: document.querySelectorAll('#__anvil_context [role="menuitem"]').length }};
        }} else if (method === 'text') {{
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
          var box = target.getBoundingClientRect();
          showAction(box.left + box.width / 2, box.top + box.height / 2, '点击「' + shortOf(target) + '」');
          return actLater(id, function () {{ target.click(); return describe(target); }});
        }} else if (method === 'fill') {{
          var field = pick(params);
          if (!field) throw new Error('nothing matches ' + JSON.stringify(params));
          field.focus();
          var fieldBox = field.getBoundingClientRect();
          var text = params.value == null ? '' : String(params.value);
          showAction(fieldBox.left + fieldBox.width / 2, fieldBox.top + fieldBox.height / 2, '输入「' + clip(text, 20) + '」');
          return actLater(id, function () {{ setValue(field, text); return describe(field); }});
        }} else if (method === 'press') {{
          var key = String(params.key || 'Enter');
          var keyOn = pick(params) || document.activeElement || document.body;
          var keyBox = keyOn && keyOn.getBoundingClientRect ? keyOn.getBoundingClientRect() : null;
          showAction(
            keyBox ? keyBox.left + keyBox.width / 2 : window.innerWidth / 2,
            keyBox ? keyBox.top + keyBox.height / 2 : window.innerHeight / 2,
            '按下 ' + key
          );
          return actLater(id, function () {{
            keyOn.dispatchEvent(new KeyboardEvent('keydown', {{ key: key, bubbles: true }}));
            keyOn.dispatchEvent(new KeyboardEvent('keyup', {{ key: key, bubbles: true }}));
            return key;
          }});
        }} else if (method === 'scroll') {{
          var dy = Number(params.dy || 600);
          showAction(window.innerWidth / 2, window.innerHeight / 2, (dy < 0 ? '向上滚动 ' : '向下滚动 ') + Math.abs(dy) + 'px');
          return actLater(id, function () {{ window.scrollBy(0, dy); return Math.round(window.scrollY); }});
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
"##
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
    app: AppHandle,
    state: tauri::State<'_, BrowserHandle>,
    x: f64,
    y: f64,
    w: f64,
    h: f64,
) -> Result<(), String> {
    let browser = state.get();
    let placed = browser.set_bounds(&app, x, y, w, h);
    // The page swallows this command's error (a placement hint must not raise
    // into the chat UI), so a failing platform would otherwise be invisible —
    // exactly why three reports of "it just sits there" carried no cause. Log
    // the first failure with the rect, then stay quiet while it keeps failing.
    if placed.is_err() {
        use std::sync::atomic::{AtomicU8, Ordering};
        static LOGGED: AtomicU8 = AtomicU8::new(0);
        if LOGGED.fetch_add(1, Ordering::Relaxed) < 3 {
            if let Some(host) = app.try_state::<crate::state::HostState>() {
                host.config.log.info(&format!(
                    "browser: place {:.0},{:.0} {:.0}x{:.0} failed: {}",
                    x, y, w, h,
                    placed.as_ref().err().map(String::as_str).unwrap_or("")
                ));
            }
        }
    }
    // macOS repositions child views on its own while the window resizes, and it
    // does so after this call — see `settle_later`. The placement below is the
    // one that survives; this one keeps the pane following during a drag.
    #[cfg(target_os = "macos")]
    settle_later(&app);
    #[cfg(not(target_os = "macos"))]
    let _ = &app;
    placed
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

/// Clears a child webview's autoresizing mask (macOS) and puts it in front.
///
/// wry creates a child view with `NSViewMinYMargin` — "keep the distance from
/// the bottom of the window". AppKit honours that on every resize, and it does
/// so *after* the page has reported where the pane is, so growing the window
/// slid the page upwards and left it there. The rect we place is the only
/// authority over the frame; `0` (`NSViewNotSizable`) removes AppKit's.
///
/// The view is also re-added to its superview, which moves it to the front: a
/// theme switch or a wry relayout can push it beneath the app's own webview,
/// and a page that renders where nobody can see it reads as a blank pane.
#[cfg(target_os = "macos")]
fn pin_view(webview: &Webview<tauri::Wry>) {
    let _ = webview.with_webview(|platform| {
        let view = platform.inner();
        if view.is_null() {
            return;
        }
        // SAFETY: `inner` is wry's live `WKWebView`, an `NSView` subclass, and
        // `setAutoresizingMask:` takes an `NSUInteger` option set; `raise_raw`
        // re-adds that same live view to its own superview.
        unsafe {
            let _: () = objc2::msg_send![
                view as *mut objc2::runtime::AnyObject,
                setAutoresizingMask: 0usize
            ];
            raise_raw(view as *mut objc2::runtime::AnyObject);
        }
    });
}

/// Re-adding a subview already in the hierarchy moves it to the end — the
/// front — without reloading or resetting anything.
///
/// SAFETY: `view` is a live `NSView` and `superview`/`addSubview:` take and
/// return `NSView` pointers.
#[cfg(target_os = "macos")]
unsafe fn raise_raw(view: *mut objc2::runtime::AnyObject) {
    let superview: *mut objc2::runtime::AnyObject =
        unsafe { objc2::msg_send![view, superview] };
    if superview.is_null() {
        return;
    }
    unsafe {
        let _: () = objc2::msg_send![superview, addSubview: view];
    }
}

/// Where the native view goes for a reported rect.
///
/// A pane that is not on screen reports a zero size. The view is parked far
/// off-screen instead of being hidden: hiding and showing it again reloads
/// nothing but does drop the page's focus and any playing media, while a page
/// at (-20000, -20000) keeps running and comes back exactly as it was.
fn placement(x: f64, y: f64, w: f64, h: f64) -> (f64, f64, f64, f64) {
    if w > 1.0 && h > 1.0 {
        (x, y, w, h)
    } else {
        (-20_000.0, -20_000.0, 1.0, 1.0)
    }
}

/// The logical rect a platform reports, if it reported one.
///
/// The readback is asked for in logical pixels; a physical answer means the
/// platform handed back what the window manager thinks instead, and comparing
/// it with the requested rect would be comparing two different units.
fn logical_of(rect: tauri::Rect) -> Option<(f64, f64, f64, f64)> {
    match (rect.position, rect.size) {
        (tauri::Position::Logical(p), tauri::Size::Logical(s)) => {
            Some((p.x, p.y, s.width, s.height))
        }
        _ => None,
    }
}

/// Places the pane once more after the window (or the divider) stops moving.
///
/// Two things happen behind the pane's back while it is being moved:
///
/// * AppKit repositions child views itself when the window resizes — the child
///   carries a `ViewMinYMargin` autoresizing mask, so it stays put and keeps
///   its old size — and it does that *after* the page has reported the pane's
///   new rect. The last word is AppKit's, which is why maximising the window
///   leaves the page visibly behind.
/// * A pane placed in the same frame as a layout change can be placed from the
///   bounds that were current when the frame started.
///
/// Both are cured by the same thing: wait until the size stops changing, then
/// place the last reported rect again. One watcher runs at a time; further
/// resizes just extend its wait.
pub fn settle_later(app: &AppHandle) {
    static GENERATION: AtomicU64 = AtomicU64::new(0);
    static WATCHING: AtomicBool = AtomicBool::new(false);

    GENERATION.fetch_add(1, Ordering::Relaxed);
    if WATCHING.swap(true, Ordering::Relaxed) {
        return;
    }
    let app = app.clone();
    std::thread::spawn(move || {
        loop {
            let before = GENERATION.load(Ordering::Relaxed);
            std::thread::sleep(std::time::Duration::from_millis(160));
            if GENERATION.load(Ordering::Relaxed) == before {
                break;
            }
        }
        WATCHING.store(false, Ordering::Relaxed);
        place_last(&app, "settled");
    });
}

/// Applies the last reported rect and writes what the view answered about it.
fn place_last(app: &AppHandle, reason: &str) {
    use tauri::Manager;
    let Some(browser) = app.try_state::<BrowserHandle>() else {
        return;
    };
    let browser = browser.get();
    let message = match browser.reapply(app) {
        Ok(report) => format!("browser: {reason} {report}"),
        Err(error) => format!("browser: {reason} failed: {error}"),
    };
    if let Some(state) = app.try_state::<crate::state::HostState>() {
        state.config.log.info(&message);
    }
}

/// Keeps the page on its pane, once every beat.
///
/// Every other placement in this file reacts to something: a reported rect, a
/// window resize, a pane opening. This one reacts to nothing, which is the
/// point — a platform can move a child view without telling anyone (AppKit's
/// autoresizing mask does exactly that), and no event follows. One readback
/// every 1.5 s costs nothing visible and puts the view back when the numbers
/// disagree.
pub fn spawn_watchdog(app: AppHandle) {
    use tauri::Manager;
    std::thread::spawn(move || {
        let mut reported = 0u32;
        // The page's address, logged whenever it changes. The watchdog's other
        // corrections are silent unless they act; this one speaks, because a
        // blank page has no other symptom in the log.
        let mut last_url: Option<Option<String>> = None;
        loop {
            std::thread::sleep(std::time::Duration::from_millis(1500));
            let Some(host) = app.try_state::<crate::state::HostState>() else {
                return;
            };
            if !host.running.load(Ordering::Relaxed) {
                return;
            }
            let Some(browser) = app.try_state::<BrowserHandle>() else {
                continue;
            };
            let browser = browser.get();
            let url = browser.page_url();
            if last_url.as_ref() != Some(&url) {
                last_url = Some(url.clone());
                host.config.log.info(&format!(
                    "browser: page {}",
                    url.as_deref().unwrap_or("(no webview)")
                ));
            }
            match browser.check(&app) {
                Ok(None) => {}
                Ok(Some(note)) => {
                    // A pane that is being fixed every beat is a bug worth
                    // reading about; a pane that is fixed once is not.
                    if reported < 5 {
                        reported += 1;
                        host.config.log.info(&format!("browser: watchdog {note}"));
                    }
                }
                Err(error) => {
                    if reported < 5 {
                        reported += 1;
                        host.config
                            .log
                            .info(&format!("browser: watchdog failed: {error}"));
                    }
                }
            }
        }
    });
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
    let read =
        |at: usize| u32::from_be_bytes([bytes[at], bytes[at + 1], bytes[at + 2], bytes[at + 3]]);
    (read(16), read(20))
}

#[cfg(windows)]
mod platform {
    use std::sync::mpsc;
    use std::time::Duration;

    use tauri::webview::Webview;
    use webview2_com::CapturePreviewCompletedHandler;
    use webview2_com::Microsoft::Web::WebView2::Win32 as wv2;
    use windows::Win32::Foundation::HGLOBAL;
    use windows::Win32::System::Com::IStream;
    use windows::Win32::System::Com::StructuredStorage::{
        CreateStreamOnHGlobal, GetHGlobalFromStream,
    };
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

#[cfg(test)]
mod placement_tests {
    use super::*;

    #[test]
    fn a_visible_pane_is_placed_where_it_is() {
        assert_eq!(
            placement(540.0, 120.0, 420.0, 600.0),
            (540.0, 120.0, 420.0, 600.0)
        );
    }

    #[test]
    fn a_hidden_pane_is_parked_off_screen_rather_than_hidden() {
        // Hiding the view would drop focus and stop media; parking keeps the
        // page exactly as it was.
        assert_eq!(
            placement(0.0, 0.0, 0.0, 0.0),
            (-20_000.0, -20_000.0, 1.0, 1.0)
        );
        // A sliver (a pane mid-collapse) counts as hidden.
        assert_eq!(
            placement(10.0, 10.0, 1.0, 400.0),
            (-20_000.0, -20_000.0, 1.0, 1.0)
        );
    }

    #[test]
    fn a_rect_carries_the_same_numbers_in_one_call() {
        match rect_of(540.0, 120.0, 420.0, 600.0) {
            tauri::Rect {
                position: tauri::Position::Logical(position),
                size: tauri::Size::Logical(size),
            } => {
                assert_eq!((position.x, position.y), (540.0, 120.0));
                assert_eq!((size.width, size.height), (420.0, 600.0));
            }
            _ => panic!("the pane is placed in logical pixels"),
        }
    }
}
