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

use serde_json::{Value, json};
use tauri::{AppHandle, Emitter};

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
            // How many times the native view has really been placed: the pane
            // flickering is this number climbing.
            "placements": browser.placements(),
        })
        .to_string(),
        ("POST", "/result") => {
            settle_body(browser, &request.body);
            json!({ "ok": true }).to_string()
        }
        ("POST", "/rpc") => dispatch(app, browser, &request.body),
        // Schedules are the app's data, not the page's: the same route an agent
        // reaches through `anvil-schedule.mjs` creates a row the tab renders.
        ("GET", "/schedule") => crate::schedules::list_from_bridge(app).to_string(),
        ("POST", "/schedule") => match serde_json::from_str::<Value>(&request.body) {
            Ok(value) => crate::schedules::create_from_bridge(app, value).to_string(),
            Err(error) => json!({ "ok": false, "error": format!("bad json: {error}") }).to_string(),
        },
        ("POST", "/schedule/toggle") => bridge_mutation(app, "toggle", &request.body),
        ("POST", "/schedule/remove") => bridge_mutation(app, "remove", &request.body),
        // Connectors: what the agent may call, and the call itself. The host
        // owns the credentials, so the tool never leaves this process except as
        // an HTTPS/IMAP/SMTP request the connector makes.
        ("GET", "/connectors") => crate::connectors::list_for_agent(app).to_string(),
        ("POST", "/connector/call") => connector_call(app, &request.body).to_string(),
        _ => {
            return respond(
                &mut stream,
                404,
                &json!({ "ok": false, "error": "no route" }).to_string(),
            );
        }
    };
    respond(&mut stream, 200, &body)
}

/// `POST /connector/call` — `{ connector, tool, params }`.
fn connector_call(app: &AppHandle, body: &str) -> Value {
    let Ok(value) = serde_json::from_str::<Value>(body) else {
        return json!({ "ok": false, "error": "bad json" });
    };
    let connector = value
        .get("connector")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .trim()
        .to_string();
    let tool = value
        .get("tool")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .trim()
        .to_string();
    if connector.is_empty() || tool.is_empty() {
        return json!({ "ok": false, "error": "connector and tool are required" });
    }
    let params = value.get("params").cloned().unwrap_or(json!({}));
    crate::connectors::call_tool(app, &connector, &tool, &params)
}

/// `POST /schedule/toggle` and `/schedule/remove`.
fn bridge_mutation(app: &AppHandle, action: &str, body: &str) -> String {
    match serde_json::from_str::<Value>(body) {
        Ok(value) => crate::schedules::mutate_from_bridge(app, action, value).to_string(),
        Err(error) => json!({ "ok": false, "error": format!("bad json: {error}") }).to_string(),
    }
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
            let url = params
                .get("url")
                .and_then(Value::as_str)
                .unwrap_or("about:blank");
            browser.open(app, url).map(|()| {
                show_pane(app, url);
                json!({ "url": url })
            })
        }
        "close" => browser.close().map(|()| json!(true)),
        "navigate" => {
            let url = params
                .get("url")
                .and_then(Value::as_str)
                .unwrap_or("about:blank");
            browser.open(app, url).map(|()| {
                show_pane(app, url);
                json!({ "url": url })
            })
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
            browser
                .run_script(script)
                .map(|()| json!({ "done": method }))
        }
        _ => browser.call(&method, &params),
    };
    match result {
        Ok(value) => json!({ "ok": true, "value": value }).to_string(),
        Err(error) => json!({ "ok": false, "error": error }).to_string(),
    }
}

/// Bring the pane forward on `url`.
///
/// The click the user asked for has to end somewhere they can see: a runtime
/// that navigates while the pane is closed still gets its page loaded, so the
/// window is told to open the pane and show it.
fn show_pane(app: &AppHandle, url: &str) {
    let _ = app.emit(crate::browser::SHOW_EVENT, json!({ "url": url }));
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
    std::fs::write(root.join("browser.json"), format!("{{\"port\":{port}}}"))
        .map_err(|error| format!("browser bridge: {error}"))?;
    std::fs::write(root.join("bridge.json"), format!("{{\"port\":{port}}}"))
        .map_err(|error| format!("browser bridge: {error}"))?;
    std::fs::write(bin.join("anvil-browser.mjs"), RENDER_CLI)
        .map_err(|error| format!("browser bridge: {error}"))?;
    std::fs::write(bin.join("anvil-schedule.mjs"), SCHEDULE_CLI)
        .map_err(|error| format!("schedule bridge: {error}"))?;
    std::fs::write(bin.join("anvil-connector.mjs"), CONNECTOR_CLI)
        .map_err(|error| format!("connector bridge: {error}"))?;
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

/// The command an agent runs to make a schedule from a sentence.
///
/// Cron and the clock are the app's business, so the model only supplies what it
/// understood: a name, when, and what to do. The caller's own time zone offset is
/// attached here, because the machine running this script is the machine the
/// schedule fires on.
const SCHEDULE_CLI: &str = r##"#!/usr/bin/env node
// Create and inspect Anvil schedules.
//
//   node anvil-schedule.mjs create '{"name":"每日站会纪要","cron":"0 9 * * 1-5","prompt":"汇总今天的会议记录","agent":"文档助手"}'
//   node anvil-schedule.mjs list
//   node anvil-schedule.mjs pause  '{"id":"sch-..."}'
//   node anvil-schedule.mjs resume '{"id":"sch-..."}'
//   node anvil-schedule.mjs remove '{"id":"sch-..."}'
//
// `cron` is the five-field form: minute hour day-of-month month day-of-week.
// `agent` may be a saved agent's name; omit it to use the default agent.
import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

function readPort() {
  for (const name of ['bridge.json', 'browser.json']) {
    try {
      return JSON.parse(readFileSync(join(homedir(), '.anvil', name), 'utf8')).port
    } catch {}
  }
  return null
}

const port = readPort()
if (!port) {
  console.error('the Anvil bridge is not running (no ~/.anvil/bridge.json)')
  process.exit(1)
}

const [action, ...rest] = process.argv.slice(2)
if (!action) {
  console.error('usage: anvil-schedule <create|list|pause|resume|remove> [json]')
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

const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
const body =
  action === 'create'
    ? {
        ...params,
        utcOffsetMinutes: params.utcOffsetMinutes ?? -new Date().getTimezoneOffset(),
        timeZone: params.timeZone ?? zone,
      }
    : params

const routes = {
  create: ['POST', '/schedule', body],
  list: ['GET', '/schedule', null],
  pause: ['POST', '/schedule/toggle', { id: params.id, enabled: false }],
  resume: ['POST', '/schedule/toggle', { id: params.id, enabled: true }],
  remove: ['POST', '/schedule/remove', { id: params.id }],
}
const route = routes[action]
if (!route) {
  console.error('unknown action: ' + action)
  process.exit(2)
}
try {
  const response = await fetch(`http://127.0.0.1:${port}${route[1]}`, {
    method: route[0],
    headers: route[0] === 'GET' ? undefined : { 'content-type': 'application/json' },
    body: route[0] === 'GET' ? undefined : JSON.stringify(route[2]),
  })
  const data = await response.json()
  console.log(JSON.stringify(data, null, 2))
  process.exit(data.ok ? 0 : 1)
} catch (error) {
  console.error('the Anvil bridge did not answer: ' + error.message)
  process.exit(1)
}
"##;

/// The command an agent runs to use a connector.
///
/// A connector is a service with credentials the app holds, so the agent can
/// only reach it through the loopback bridge: `list` shows what is connected,
/// `call` runs one tool. No secret is ever printed.
const CONNECTOR_CLI: &str = r##"#!/usr/bin/env node
// Use an Anvil connector (mail / Notion / DingTalk docs).
//
//   node anvil-connector.mjs list
//   node anvil-connector.mjs call '{"connector":"cn-1763...","tool":"mail_list","params":{"count":5}}'
//
// The connector id comes from `list`. Tool names and their parameters are
// printed by `list` as well.
import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

function readPort() {
  for (const name of ['bridge.json', 'browser.json']) {
    try {
      return JSON.parse(readFileSync(join(homedir(), '.anvil', name), 'utf8')).port
    } catch {}
  }
  return null
}

const port = readPort()
if (!port) {
  console.error('the Anvil bridge is not running (no ~/.anvil/bridge.json)')
  process.exit(1)
}

const [action, ...rest] = process.argv.slice(2)
if (!action || !['list', 'call'].includes(action)) {
  console.error('usage: anvil-connector <list|call> [json]')
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

const url =
  action === 'list'
    ? `http://127.0.0.1:${port}/connectors`
    : `http://127.0.0.1:${port}/connector/call`
try {
  const response = await fetch(url, {
    method: action === 'list' ? 'GET' : 'POST',
    headers: action === 'list' ? undefined : { 'content-type': 'application/json' },
    body: action === 'list' ? undefined : JSON.stringify(params),
  })
  const data = await response.json()
  console.log(JSON.stringify(data, null, 2))
  process.exit(data.ok ? 0 : 1)
} catch (error) {
  console.error('the Anvil bridge did not answer: ' + error.message)
  process.exit(1)
}
"##;
