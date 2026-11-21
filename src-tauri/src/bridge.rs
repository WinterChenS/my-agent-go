//! The loopback bridge the runtime's browser tools talk to.
//!
//! Two kinds of caller use it:
//!
//! * the page in the right pane, posting the answer to a command it ran;
//! * a runtime's browser tool, asking for the page to be driven.
//!
//! It listens on `127.0.0.1` on a port the OS picks, never on a routable
//! address, and it speaks just enough HTTP for JSON in and JSON out. The port
//! is written into the pi extension this build installs, so the two find each
//! other without configuration.

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;

use serde_json::{json, Value};
use tauri::AppHandle;

use crate::browser::Browser;

/// One request, parsed far enough to route it.
struct Request {
    method: String,
    path: String,
    body: String,
}

pub fn start(app: AppHandle, browser: Arc<Browser>) -> Result<u16, String> {
    let listener =
        TcpListener::bind(("127.0.0.1", 0)).map_err(|error| format!("bridge: {error}"))?;
    let port = listener
        .local_addr()
        .map_err(|error| format!("bridge: {error}"))?
        .port();
    browser.set_port(port);
    write_render(&port)?;
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(stream) = stream else { continue };
            let app = app.clone();
            let browser = browser.clone();
            std::thread::spawn(move || {
                let _ = handle(stream, &app, &browser);
            });
        }
    });
    Ok(port)
}

fn handle(mut stream: TcpStream, app: &AppHandle, browser: &Arc<Browser>) -> std::io::Result<()> {
    stream.set_read_timeout(Some(std::time::Duration::from_secs(20)))?;
    let Some(request) = read_request(&mut stream)? else {
        return Ok(());
    };
    if request.method == "OPTIONS" {
        return respond(&mut stream, 204, "");
    }
    let body = match (request.method.as_str(), request.path.as_str()) {
        ("GET", "/health") => json!({
            "ok": true,
            "open": browser.is_open(),
            "port": browser.port(),
        })
        .to_string(),
        ("POST", "/result") => {
            settle_body(browser, &request.body);
            json!({ "ok": true }).to_string()
        }
        ("POST", "/rpc") => dispatch(app, browser, &request.body),
        _ => {
            return respond(&mut stream, 404, &json!({ "ok": false, "error": "no route" }).to_string());
        }
    };
    respond(&mut stream, 200, &body)
}

/// The page reporting the answer to a command it ran.
fn settle_body(browser: &Arc<Browser>, body: &str) {
    let Ok(value) = serde_json::from_str::<Value>(body) else {
        return;
    };
    let Some(id) = value.get("id").and_then(Value::as_u64) else {
        return;
    };
    let ok = value.get("ok").and_then(Value::as_bool).unwrap_or(false);
    let payload = value.get("value").cloned().unwrap_or(Value::Null);
    if ok {
        browser.settle(id, Ok(payload));
    } else {
        browser.settle(
            id,
            Err(payload.as_str().unwrap_or("the page failed").to_string()),
        );
    }
}

/// A runtime's tool call.
fn dispatch(app: &AppHandle, browser: &Arc<Browser>, body: &str) -> String {
    let parsed: Value = match serde_json::from_str(body) {
        Ok(value) => value,
        Err(error) => {
            return json!({ "ok": false, "error": format!("bad json: {error}") }).to_string();
        }
    };
    let method = parsed
        .get("method")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    let params = parsed.get("params").cloned().unwrap_or(Value::Null);
    let result = match method.as_str() {
        // Opening and moving the pane are the app's business, not the page's.
        "open" => {
            let url = params.get("url").and_then(Value::as_str).unwrap_or("about:blank");
            browser.open(app, url).map(|()| json!({ "url": url }))
        }
        "close" => browser.close().map(|()| json!(true)),
        "navigate" => {
            let url = params.get("url").and_then(Value::as_str).unwrap_or("about:blank");
            browser.open(app, url).map(|()| json!({ "url": url }))
        }
        // The picture of the page: what the DOM cannot say — that the layout
        // broke, that a banner covers the button, that the page is blank.
        "screenshot" => {
            let target = params.get("path").and_then(Value::as_str);
            browser.capture(target)
        }
        "back" | "forward" | "reload" => {
            let script = match method.as_str() {
                "back" => "history.back()",
                "forward" => "history.forward()",
                _ => "location.reload()",
            };
            browser.run_script(script).map(|()| json!({ "done": method }))
        }
        _ => browser.call(&method, &params),
    };
    match result {
        Ok(value) => json!({ "ok": true, "value": value }).to_string(),
        Err(error) => json!({ "ok": false, "error": error }).to_string(),
    }
}

fn read_request(stream: &mut TcpStream) -> std::io::Result<Option<Request>> {
    let mut buffer = Vec::new();
    let mut chunk = [0u8; 4096];
    let mut header_end = None;
    while header_end.is_none() {
        let read = stream.read(&mut chunk)?;
        if read == 0 {
            return Ok(None);
        }
        buffer.extend_from_slice(&chunk[..read]);
        header_end = find_header_end(&buffer);
        if buffer.len() > 96 * 1024 {
            return Ok(None);
        }
    }
    let header_end = header_end.unwrap_or(0);
    let head = String::from_utf8_lossy(&buffer[..header_end]).to_string();
    let mut lines = head.split("\r\n");
    let request_line = lines.next().unwrap_or_default();
    let mut parts = request_line.split_whitespace();
    let method = parts.next().unwrap_or_default().to_string();
    let path = parts.next().unwrap_or_default().to_string();
    let mut length = 0usize;
    for line in lines {
        let lower = line.to_lowercase();
        if let Some(value) = lower.strip_prefix("content-length:") {
            length = value.trim().parse().unwrap_or(0);
        }
    }
    let mut body = buffer[header_end + 4..].to_vec();
    while body.len() < length {
        let read = stream.read(&mut chunk)?;
        if read == 0 {
            break;
        }
        body.extend_from_slice(&chunk[..read]);
    }
    Ok(Some(Request {
        method,
        path,
        body: String::from_utf8_lossy(&body).to_string(),
    }))
}

fn find_header_end(buffer: &[u8]) -> Option<usize> {
    buffer.windows(4).position(|window| window == b"\r\n\r\n")
}

fn respond(stream: &mut TcpStream, status: u16, body: &str) -> std::io::Result<()> {
    let reason = match status {
        200 => "OK",
        204 => "No Content",
        _ => "Not Found",
    };
    let head = format!(
        "HTTP/1.1 {status} {reason}\r\n\
         content-type: application/json\r\n\
         content-length: {}\r\n\
         access-control-allow-origin: *\r\n\
         access-control-allow-headers: content-type\r\n\
         access-control-allow-methods: GET, POST, OPTIONS\r\n\
         connection: close\r\n\r\n",
        body.len()
    );
    stream.write_all(head.as_bytes())?;
    stream.write_all(body.as_bytes())?;
    stream.flush()
}

/// Tell the rest of the machine where the bridge is, and leave the command the
/// runtime can call behind it.
///
/// The CLI is how an agent drives the pane: pi already has a shell, so a tool
/// that can be called from the shell needs no extension, no registration and
/// no reload — `node ~/.anvil/bin/anvil-browser.mjs text` just works.
fn write_render(port: &u16) -> Result<(), String> {
    let home = std::env::var("USERPROFILE")
        .or_else(|_| std::env::var("HOME"))
        .map_err(|_| "no home directory".to_string())?;
    let root = std::path::Path::new(&home).join(".anvil");
    let bin = root.join("bin");
    std::fs::create_dir_all(&bin).map_err(|error| format!("browser bridge: {error}"))?;
    std::fs::write(
        root.join("browser.json"),
        format!("{{\"port\":{port}}}"),
    )
    .map_err(|error| format!("browser bridge: {error}"))?;
    std::fs::write(bin.join("anvil-browser.mjs"), RENDER_CLI)
        .map_err(|error| format!("browser bridge: {error}"))?;
    Ok(())
}

/// The command an agent runs. Kept deliberately small: one JSON reply, one
/// exit code, no dependencies beyond Node itself.
const RENDER_CLI: &str = r##"#!/usr/bin/env node
// Drive Anvil's browser pane.
//
//   node anvil-browser.mjs navigate '{"url":"https://example.com"}'
//   node anvil-browser.mjs text
//   node anvil-browser.mjs click '{"text":"Sign in"}'
//   node anvil-browser.mjs fill '{"selector":"#q","value":"anvil"}'
//   node anvil-browser.mjs snapshot
//   node anvil-browser.mjs screenshot '{"path":"shot.png"}'
//   node anvil-browser.mjs back            (also: forward, reload, scroll, wait)
import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

const config = JSON.parse(readFileSync(join(homedir(), '.anvil', 'browser.json'), 'utf8'))
const [method, ...rest] = process.argv.slice(2)
if (!method) {
  console.error('usage: anvil-browser <method> [json-params]')
  process.exit(2)
}
let params = {}
if (rest.length > 0) {
  try {
    params = JSON.parse(rest.join(' '))
  } catch (error) {
    console.error('params must be JSON: ' + error.message)
    process.exit(2)
  }
}
try {
  const response = await fetch(`http://127.0.0.1:${config.port}/rpc`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ method, params }),
  })
  const data = await response.json()
  console.log(JSON.stringify(data, null, 2))
  process.exit(data.ok ? 0 : 1)
} catch (error) {
  console.error('the browser bridge is not running: ' + error.message)
  process.exit(1)
}
"##;
