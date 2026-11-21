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
    /// How many times the native view was actually moved. A number the pane can
    /// be asked for, because "it keeps flickering" needs a count and not an
    /// opinion.
    placements: AtomicU64,
}

impl Browser {
    pub fn new() -> Self {
        Self {
            webview: Mutex::new(None),
            pending: Mutex::new(HashMap::new()),
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
        {
            let mut bounds = self.bounds.lock().map_err(|_| "browser bounds".to_string())?;
            if *bounds == (x, y, w, h) {
                // Same place, same size. Placing a webview makes it lay itself
                // out again, and a repeated placement is a visible flicker with
                // nothing to show for it.
                return Ok(());
            }
            *bounds = (x, y, w, h);
        }
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
        // One call, not a move followed by a resize: the intermediate frame is
        // a page drawn at the old size in the new place.
        let moved = webview.set_bounds(tauri::Rect {
            position: tauri::Position::Logical(LogicalPosition::new(px, py)),
            size: tauri::Size::Logical(LogicalSize::new(pw, ph)),
        });
        if moved.is_ok() {
            self.placements.fetch_add(1, Ordering::Relaxed);
        }
        moved.map_err(|error| format!("browser bounds: {error}"))
    }

    pub fn close(&self) -> Result<(), String> {
        let mut slot = self.webview.lock().map_err(|_| "browser lock".to_string())?;
        if let Some(webview) = slot.take() {
            webview.close().map_err(|error| format!("close browser: {error}"))?;
        }
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
