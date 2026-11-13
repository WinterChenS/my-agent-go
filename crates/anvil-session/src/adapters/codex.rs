//! Codex adapter (`codex app-server`, JSON-RPC 2.0 over stdio).
//!
//! Facts this implementation depends on, measured on this machine (codex
//! 0.153.0):
//!
//! * the CLI entry point is a Node script reached through `codex.cmd`; a `.cmd`
//!   cannot be spawned directly on Windows, so the adapter wraps it in
//!   `cmd /c`;
//! * `initialize` → `initialized` → `thread/start {model}` gives a thread id;
//! * `turn/start {threadId, input:[{type:"text",text}]}` answers immediately and
//!   the answer itself arrives as notifications: `item/agentMessage/delta`,
//!   `item/completed`, `error`, then `turn/completed`;
//! * the default model (`gpt-6.1-sol`) is **not** usable with the signed-in
//!   account, so the model is always passed explicitly and a `model_not_found`
//!   error is surfaced verbatim instead of retried forever;
//! * a killed writer leaves "thread already has an active writer" behind, so the
//!   child is shut down by closing stdin and waiting, never by `kill`.

use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

use serde_json::{Value, json};

use crate::adapter::{AdapterEvent, Cancel, RunOutcome, SessionAdapter};
use crate::error::{Error, Result};
use crate::model::{
    EventKind, OpenedSession, RunStatus, SessionCapabilities, SessionContext, SessionSpec,
};

/// Adapter id, matching the catalog.
pub const ID: &str = "codex";

/// Codex adapter.
pub struct CodexAdapter {
    executable: Option<PathBuf>,
}

impl CodexAdapter {
    #[must_use]
    pub fn new(executable: Option<PathBuf>) -> Self {
        Self { executable }
    }

    fn executable(&self) -> Result<&PathBuf> {
        self.executable.as_ref().ok_or_else(|| {
            Error::NotFound("codex executable not found on PATH (install codex or fix PATH)".into())
        })
    }

    fn spawn(&self) -> Result<Client> {
        let executable = self.executable()?;
        let child = super::command_for(executable)
            .arg("app-server")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|error| Error::Failed(format!("spawn codex app-server: {error}")))?;
        Client::new(child)
    }
}

impl SessionAdapter for CodexAdapter {
    fn id(&self) -> &str {
        ID
    }

    fn capabilities(&self) -> SessionCapabilities {
        SessionCapabilities {
            streaming: true,
            resume: true,
            abort: true,
            tools: true,
            // Codex asks for approval through its own guard; this build has no
            // channel to answer it yet.
            approvals: false,
            compact: true,
            fork: true,
            usage: true,
        }
    }

    fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession> {
        // Codex writes a thread's rollout only after its first turn, and a
        // thread without a rollout cannot be resumed. Creating one here would
        // therefore produce an identifier that `send` could never reopen, so the
        // identifier is minted by the first run instead (see `RunOutcome`).
        self.executable()?;
        Ok(OpenedSession {
            runtime_id: ID.to_string(),
            native_id: spec.native_id.clone().unwrap_or_default(),
            model: spec.model.clone(),
        })
    }

    fn send(
        &self,
        context: &SessionContext,
        opened: &OpenedSession,
        prompt: &str,
        cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> Result<RunOutcome> {
        let mut client = self.spawn()?;
        client.handshake(context.run_timeout)?;

        // First turn creates the thread; later turns must resume it. A resume
        // failure is fatal on purpose: silently starting a new thread would lose
        // the user's history while looking like success.
        let thread_id = if opened.native_id.trim().is_empty() {
            let started = client.call(
                "thread/start",
                json!({ "model": opened.model }),
                context.run_timeout,
                &mut |_| {},
            )?;
            started
                .pointer("/result/thread/id")
                .and_then(Value::as_str)
                .map(str::to_string)
                .ok_or_else(|| {
                    Error::Failed(format!(
                        "codex did not return a thread id: {}",
                        truncate(&started.to_string())
                    ))
                })?
        } else {
            let resumed = client.call(
                "thread/resume",
                json!({ "threadId": opened.native_id }),
                context.run_timeout,
                &mut |_| {},
            )?;
            if let Some(error) = resumed.get("error") {
                client.shutdown();
                return Err(Error::Failed(format!(
                    "cannot resume thread {}: {error}",
                    opened.native_id
                )));
            }
            opened.native_id.clone()
        };

        let turn = client.call(
            "turn/start",
            json!({
                "threadId": thread_id,
                "input": [{ "type": "text", "text": prompt }],
            }),
            context.run_timeout,
            &mut |_| {},
        )?;
        if let Some(error) = turn.get("error") {
            client.shutdown();
            return Err(Error::Failed(format!("turn/start failed: {error}")));
        }

        let deadline = Instant::now() + context.run_timeout;
        let mut deltas = String::new();
        let mut final_text: Option<String> = None;
        let mut usage: Option<Value> = None;
        let mut failure: Option<String> = None;
        let completed: Option<String>;

        loop {
            if cancel.is_cancelled() {
                let _ = client.call(
                    "turn/interrupt",
                    json!({ "threadId": thread_id }),
                    Duration::from_secs(5),
                    &mut |_| {},
                );
                client.shutdown();
                sink(AdapterEvent::new(
                    EventKind::Aborted,
                    json!({ "reason": "user" }),
                ));
                return Ok(RunOutcome::aborted(&deltas));
            }
            if Instant::now() >= deadline {
                client.shutdown();
                return Err(Error::Timeout(format!(
                    "codex did not finish within {:?}",
                    context.run_timeout
                )));
            }
            match client.next_message(Duration::from_millis(100)) {
                Some(message) => {
                    let method = message
                        .get("method")
                        .and_then(Value::as_str)
                        .unwrap_or("")
                        .to_string();
                    let params = message.get("params").cloned().unwrap_or(Value::Null);
                    if method == "item/agentMessage/delta"
                        && let Some(delta) = params.get("delta").and_then(Value::as_str)
                    {
                        deltas.push_str(delta);
                    }
                    for event in translate(&method, &params) {
                        if event.kind == EventKind::Error {
                            failure = event
                                .payload
                                .get("message")
                                .and_then(Value::as_str)
                                .map(str::to_string);
                        }
                        if let Some(found) = event.payload.get("finalText").and_then(Value::as_str)
                        {
                            final_text = Some(found.to_string());
                        }
                        if let Some(found) = event.payload.get("usage")
                            && !found.is_null()
                        {
                            usage = Some(found.clone());
                        }
                        sink(event);
                    }
                    if method == "turn/completed" {
                        completed = params
                            .pointer("/turn/status")
                            .and_then(Value::as_str)
                            .map(str::to_string);
                        break;
                    }
                }
                None => continue,
            }
        }
        client.shutdown();

        let text = final_text.unwrap_or_else(|| deltas.clone());
        if let Some(failure) = failure {
            return Ok(RunOutcome {
                status: RunStatus::Failed,
                text,
                error: Some(failure),
                usage,
                native_id: None,
            });
        }
        let base = match completed.as_deref() {
            Some("completed") => RunOutcome {
                status: RunStatus::Completed,
                text,
                error: None,
                usage,
                native_id: None,
            },
            Some(other) => RunOutcome {
                status: RunStatus::Failed,
                text,
                error: Some(format!("turn ended as {other}")),
                usage,
                native_id: None,
            },
            None => RunOutcome {
                status: RunStatus::Failed,
                text,
                error: Some("the turn ended without a terminal notification".into()),
                usage,
                native_id: None,
            },
        };
        Ok(base.with_native_id(&thread_id))
    }
}

/// Map one notification onto the ledger vocabulary.
fn translate(method: &str, params: &Value) -> Vec<AdapterEvent> {
    match method {
        "item/agentMessage/delta" => {
            let delta = params.get("delta").and_then(Value::as_str).unwrap_or("");
            vec![AdapterEvent::text_delta(delta)]
        }
        "item/completed" => {
            let item = params.get("item").cloned().unwrap_or(Value::Null);
            match item.get("type").and_then(Value::as_str).unwrap_or("") {
                "agentMessage" => {
                    let text = item
                        .get("content")
                        .and_then(Value::as_array)
                        .map(|parts| {
                            parts
                                .iter()
                                .filter_map(|part| part.get("text").and_then(Value::as_str))
                                .collect::<String>()
                        })
                        .unwrap_or_default();
                    vec![AdapterEvent::new(
                        EventKind::MessageCompleted,
                        json!({ "text": text, "finalText": text }),
                    )]
                }
                "commandExecution" => vec![AdapterEvent::new(
                    EventKind::ToolResult,
                    json!({
                        "tool": "command",
                        "ok": item.get("status").and_then(Value::as_str) == Some("completed"),
                        "exitCode": item.get("exitCode").cloned().unwrap_or(Value::Null),
                    }),
                )],
                other => vec![AdapterEvent::new(
                    EventKind::Unknown,
                    json!({ "type": other }),
                )],
            }
        }
        "item/started" => {
            let item = params.get("item").cloned().unwrap_or(Value::Null);
            match item.get("type").and_then(Value::as_str).unwrap_or("") {
                "commandExecution" => vec![AdapterEvent::new(
                    EventKind::ToolCall,
                    json!({
                        "tool": "command",
                        "args": item.get("command").cloned().unwrap_or(Value::Null),
                    }),
                )],
                other => vec![AdapterEvent::new(
                    EventKind::Unknown,
                    json!({ "type": other }),
                )],
            }
        }
        "error" => {
            let message = params
                .pointer("/error/message")
                .and_then(Value::as_str)
                .unwrap_or("codex reported an error");
            vec![AdapterEvent::new(
                EventKind::Error,
                json!({ "message": message }),
            )]
        }
        "turn/completed" => {
            let status = params
                .pointer("/turn/status")
                .and_then(Value::as_str)
                .unwrap_or("unknown");
            // The service writes the terminal `turn_completed` itself; emitting
            // one here as well put two in the ledger per turn. Only the usage is
            // carried across.
            let usage = params
                .pointer("/turn/usage")
                .cloned()
                .unwrap_or(Value::Null);
            let mut events = Vec::new();
            if !usage.is_null() {
                events.push(AdapterEvent::new(
                    EventKind::Usage,
                    json!({ "usage": usage }),
                ));
            }
            let _ = status;
            events
        }
        "thread/status/changed" | "mcpServer/startupStatus/updated" | "turn/started" | "" => {
            Vec::new()
        }
        other => vec![AdapterEvent::new(
            EventKind::Unknown,
            json!({ "type": other }),
        )],
    }
}

/// A JSON-RPC client over one app-server process.
struct Client {
    child: Child,
    stdin: Option<ChildStdin>,
    messages: mpsc::Receiver<Value>,
    next_id: i64,
}

impl Client {
    fn new(mut child: Child) -> Result<Self> {
        let stdin = child
            .stdin
            .take()
            .ok_or_else(|| Error::Io("codex stdin was not piped".into()))?;
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| Error::Io("codex stdout was not piped".into()))?;
        let (sender, receiver) = mpsc::channel::<Value>();
        std::thread::spawn(move || {
            for line in BufReader::new(stdout)
                .lines()
                .map_while(std::result::Result::ok)
            {
                let trimmed = line.trim();
                if !trimmed.starts_with('{') {
                    continue;
                }
                if let Ok(value) = serde_json::from_str::<Value>(trimmed)
                    && sender.send(value).is_err()
                {
                    break;
                }
            }
        });
        Ok(Self {
            child,
            stdin: Some(stdin),
            messages: receiver,
            next_id: 0,
        })
    }

    fn write(&mut self, value: &Value) -> Result<()> {
        let stdin = self
            .stdin
            .as_mut()
            .ok_or_else(|| Error::Io("codex stdin is closed".into()))?;
        let mut line = serde_json::to_string(value)
            .map_err(|error| Error::Io(format!("encode request: {error}")))?;
        line.push('\n');
        stdin
            .write_all(line.as_bytes())
            .and_then(|()| stdin.flush())
            .map_err(|error| Error::Io(format!("write to codex: {error}")))
    }

    fn next_message(&self, wait: Duration) -> Option<Value> {
        self.messages.recv_timeout(wait).ok()
    }

    fn handshake(&mut self, timeout: Duration) -> Result<()> {
        let reply = self.call(
            "initialize",
            json!({ "clientInfo": { "name": "anvil", "version": env!("CARGO_PKG_VERSION") } }),
            timeout,
            &mut |_| {},
        )?;
        if let Some(error) = reply.get("error") {
            return Err(Error::Failed(format!("codex initialize failed: {error}")));
        }
        self.write(&json!({ "jsonrpc": "2.0", "method": "initialized", "params": {} }))
    }

    /// Send a request and wait for the matching response, forwarding any
    /// notification that arrives meanwhile.
    fn call(
        &mut self,
        method: &str,
        params: Value,
        timeout: Duration,
        on_notification: &mut dyn FnMut(Value),
    ) -> Result<Value> {
        self.next_id += 1;
        let id = self.next_id;
        self.write(&json!({
            "jsonrpc": "2.0",
            "id": id,
            "method": method,
            "params": params,
        }))?;
        let deadline = Instant::now() + timeout;
        while Instant::now() < deadline {
            match self.next_message(Duration::from_millis(100)) {
                Some(message) => {
                    if message.get("id").and_then(Value::as_i64) == Some(id) {
                        return Ok(message);
                    }
                    if message.get("method").is_some() {
                        on_notification(message);
                    }
                }
                None => continue,
            }
        }
        Err(Error::Timeout(format!(
            "codex did not answer {method} in time"
        )))
    }

    /// Close stdin and let the runtime release its writer lock. A hard kill
    /// leaves "thread already has an active writer" behind, so this waits.
    fn shutdown(&mut self) {
        self.stdin.take();
        let deadline = Instant::now() + Duration::from_secs(5);
        while Instant::now() < deadline {
            match self.child.try_wait() {
                Ok(Some(_)) => return,
                Ok(None) => std::thread::sleep(Duration::from_millis(50)),
                Err(_) => break,
            }
        }
        super::kill_tree(&mut self.child);
    }
}

fn truncate(text: &str) -> String {
    text.chars().take(240).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deltas_are_translated() {
        let events = translate(
            "item/agentMessage/delta",
            &json!({ "delta": "ok", "threadId": "t", "turnId": "u" }),
        );
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::TextDelta);
    }

    #[test]
    fn turn_completed_only_carries_usage() {
        // The terminal event is the service's job; a second one here would put
        // two `turn_completed` rows in the ledger for a single turn.
        let without_usage = translate(
            "turn/completed",
            &json!({ "turn": { "status": "completed" } }),
        );
        assert!(without_usage.is_empty());
        let with_usage = translate(
            "turn/completed",
            &json!({ "turn": { "status": "completed", "usage": { "totalTokens": 9 } } }),
        );
        assert_eq!(with_usage.len(), 1);
        assert_eq!(with_usage[0].kind, EventKind::Usage);
    }

    #[test]
    fn error_notifications_keep_the_runtime_message() {
        let events = translate(
            "error",
            &json!({ "error": { "message": "model_not_found" } }),
        );
        assert_eq!(events[0].kind, EventKind::Error);
        assert_eq!(events[0].payload["message"], "model_not_found");
    }

    #[test]
    fn unknown_notifications_are_kept() {
        let events = translate("brand/new", &json!({}));
        assert_eq!(events[0].kind, EventKind::Unknown);
    }

    #[test]
    fn command_execution_maps_to_tool_events() {
        let started = translate(
            "item/started",
            &json!({ "item": { "type": "commandExecution", "command": "dir" } }),
        );
        assert_eq!(started[0].kind, EventKind::ToolCall);
        let done = translate(
            "item/completed",
            &json!({ "item": { "type": "commandExecution", "status": "completed" } }),
        );
        assert_eq!(done[0].payload["ok"], true);
    }
}
