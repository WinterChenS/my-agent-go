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

use crate::adapter::{AdapterEvent, Cancel, RunOutcome, SessionAdapter, Turn};
use crate::error::{Error, Result};
use crate::model::{
    EventKind, OpenedSession, RunStatus, SessionCapabilities, SessionContext, SessionSpec,
};
use crate::subagent::{SubAgentReport, SubAgentStatus};

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

    fn spawn(&self, cwd: &str) -> Result<Client> {
        let executable = self.executable()?;
        let mut command = super::command_for(executable);
        command.arg("app-server");
        if !cwd.trim().is_empty() {
            command.current_dir(cwd);
        }
        let child = command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .map_err(|error| Error::Failed(format!("spawn codex app-server: {error}")))?;
        Client::new(child)
    }
}

/// Build the text sent to codex: the saved persona first, then the user's words.
///
/// Rendering uses the same variables as the pi adapter, so `{{repo_path}}` and
/// friends mean the same thing whichever runtime an agent is bound to.
fn effective_prompt(opened: &OpenedSession, prompt: &str) -> String {
    let vars = crate::persona::PersonaVars {
        repo_path: crate::persona::display_path(&opened.cwd),
        model: &opened.model,
        agent: &opened.agent_id,
        date: &crate::clock::today(),
    };
    let mut instructions = Vec::new();
    if !opened.system_prompt.trim().is_empty() {
        instructions.push(crate::persona::render(&opened.system_prompt, &vars));
    }
    if !opened.append_system_prompt.trim().is_empty() {
        instructions.push(crate::persona::render(&opened.append_system_prompt, &vars));
    }
    if instructions.is_empty() {
        return prompt.to_string();
    }
    format!(
        "[Agent instructions — follow for this conversation]\n{}\n\n[User message]\n{prompt}",
        instructions.join("\n\n")
    )
}

/// The `input` array of `turn/start`.
///
/// Codex reads a local file itself: `localImage` takes a path, so an image is
/// never base64 we would have to size-limit. The text item stays first so the
/// instruction block still frames the turn.
fn turn_input(text: &str, images: &[PathBuf]) -> Vec<Value> {
    let mut items = vec![json!({ "type": "text", "text": text })];
    for image in images {
        items.push(json!({
            "type": "localImage",
            "path": image.to_string_lossy(),
        }));
    }
    items
}

impl SessionAdapter for CodexAdapter {
    fn id(&self) -> &str {
        ID
    }

    /// Codex's protocol takes `localImage` items; see [`turn_input`].
    fn supports_images(&self) -> bool {
        true
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
            cwd: spec.cwd.clone(),
            allow_tools: spec.allow_tools,
            tools: spec.tools.clone(),
            agent_id: spec.agent_id.clone(),
            thinking: spec.thinking.clone(),
            system_prompt: spec.system_prompt.clone(),
            append_system_prompt: spec.append_system_prompt.clone(),
            // Trust is a per-turn decision (see `permission_plan`); opening a
            // session never loads a folder's `.pi` on its own.
            project_trust: None,
            skills: spec.skills.clone(),
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
        self.send_turn(context, opened, &Turn::text(prompt), cancel, sink)
    }

    fn send_turn(
        &self,
        context: &SessionContext,
        opened: &OpenedSession,
        turn: &Turn<'_>,
        cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> Result<RunOutcome> {
        let mut client = self.spawn(&opened.cwd)?;
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

        // Every notification carries a thread id; this is what tells the
        // parent's stream apart from a delegated agent's.
        let threads = Threads::new(&thread_id);

        // Codex app-server has no `--system-prompt` equivalent in the protocol
        // this build speaks. Deliver the agent's persona as an explicit
        // instruction block in the turn's first text item instead: the model
        // still obeys it, and the ledger keeps the user's own words verbatim.
        let effective = effective_prompt(opened, turn.prompt);
        let started = client.call(
            "turn/start",
            json!({
                "threadId": thread_id,
                "input": turn_input(&effective, turn.images),
            }),
            context.run_timeout,
            &mut |_| {},
        )?;
        if let Some(error) = started.get("error") {
            client.shutdown();
            return Err(Error::Failed(format!("turn/start failed: {error}")));
        }

        let deadline = context.run_timeout.map(|budget| Instant::now() + budget);
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
                    Some(Duration::from_secs(5)),
                    &mut |_| {},
                );
                client.shutdown();
                sink(AdapterEvent::new(
                    EventKind::Aborted,
                    json!({ "reason": "user" }),
                ));
                return Ok(RunOutcome::aborted(&deltas));
            }
            if let Some(deadline) = deadline
                && Instant::now() >= deadline
            {
                client.shutdown();
                return Err(Error::Timeout(format!(
                    "codex did not finish within {:?}",
                    context.run_timeout.unwrap_or_default()
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
                    // A delegated agent runs as its own thread; its deltas must
                    // not become the parent's answer and its `turn/completed`
                    // must not end the parent's turn.
                    let thread = params.get("threadId").and_then(Value::as_str).unwrap_or("");
                    let from_main = !threads.is_child(thread);
                    if method == "item/agentMessage/delta"
                        && from_main
                        && let Some(delta) = params.get("delta").and_then(Value::as_str)
                    {
                        deltas.push_str(delta);
                    }
                    for event in translate_in(&threads, &method, &params) {
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
                        // A child's tokens are reported under `usage` too, but
                        // they are the child's bill, not the turn's.
                        if event.kind == EventKind::Usage
                            && let Some(found) = event.payload.get("usage")
                            && !found.is_null()
                        {
                            usage = Some(found.clone());
                        }
                        sink(event);
                    }
                    if method == "turn/completed" && from_main {
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
/// Text of one finished message item, whatever shape codex used.
///
/// The runtime has shipped `content: [{type, text}]`, a bare `text` and, in
/// 0.153, an item whose content array carries `output_text` parts; all three are
/// accepted rather than guessing one.
fn item_text(item: &Value) -> String {
    let from_parts = item
        .get("content")
        .and_then(Value::as_array)
        .map(|parts| {
            parts
                .iter()
                .filter_map(|part| part.get("text").and_then(Value::as_str))
                .collect::<String>()
        })
        .unwrap_or_default();
    if !from_parts.is_empty() {
        return from_parts;
    }
    item.get("text")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

/// Which thread a notification belongs to.
///
/// Codex runs a delegated agent as its own thread on the same app-server
/// connection, so a child's streamed answer arrives through exactly the same
/// notifications as the parent's. Without this distinction a child's text was
/// appended to the parent's answer and the child's `turn/completed` ended the
/// parent's turn early.
#[derive(Debug)]
struct Threads {
    main: String,
}

impl Threads {
    fn new(main: &str) -> Self {
        Self {
            main: main.to_string(),
        }
    }

    fn is_child(&self, thread: &str) -> bool {
        !thread.is_empty() && !self.main.is_empty() && thread != self.main
    }
}

/// Translate one notification without any knowledge of other threads.
///
/// Used by tests: the event's own thread is treated as the main one, so a
/// single-notification case behaves exactly as it did before delegation was
/// modelled.
#[cfg(test)]
fn translate(method: &str, params: &Value) -> Vec<AdapterEvent> {
    let thread = params.get("threadId").and_then(Value::as_str).unwrap_or("");
    translate_in(&Threads::new(thread), method, params)
}

fn translate_in(threads: &Threads, method: &str, params: &Value) -> Vec<AdapterEvent> {
    let thread = params.get("threadId").and_then(Value::as_str).unwrap_or("");
    let child = threads.is_child(thread);
    match method {
        // Codex reports reasoning on its own notification channels. Keep the
        // incremental text the same way pi's thinking deltas are kept.
        channel if channel.contains("reasoning") => {
            let delta = params
                .get("delta")
                .or_else(|| params.get("text"))
                .and_then(Value::as_str)
                .unwrap_or("");
            if delta.is_empty() {
                Vec::new()
            } else if child {
                vec![child_text(thread, delta, "thinking", false)]
            } else {
                vec![AdapterEvent::new(
                    EventKind::Thinking,
                    json!({ "delta": delta }),
                )]
            }
        }
        "item/agentMessage/delta" => {
            let delta = params.get("delta").and_then(Value::as_str).unwrap_or("");
            if delta.is_empty() {
                Vec::new()
            } else if child {
                vec![child_text(thread, delta, "answer", false)]
            } else {
                vec![AdapterEvent::text_delta(delta)]
            }
        }
        "item/completed" => item_event(thread, params, child, true),
        "item/started" => item_event(thread, params, child, false),
        // Token accounting is per thread and arrives as its own notification.
        // It is a running total for the whole thread, not this turn's bill, so
        // it is tagged: the UI treats `scope: "thread"` as a snapshot to read
        // last rather than a report to add up.
        "thread/tokenUsage/updated" => {
            let usage = params
                .get("usage")
                .cloned()
                .unwrap_or_else(|| params.clone());
            // A child's tokens are the child's; adding them to the parent's
            // bill would double-charge a delegated turn.
            if child {
                return vec![AdapterEvent::new(
                    EventKind::SubAgent,
                    SubAgentReport::status(thread, SubAgentStatus::Running)
                        .with_usage(usage)
                        .to_payload(),
                )];
            }
            vec![AdapterEvent::new(
                EventKind::Usage,
                json!({ "usage": usage, "scope": "thread" }),
            )]
        }
        "error" => {
            // A child failing is reported on the child's card; the parent turn
            // only fails when codex itself reports it on the parent thread.
            if child {
                return vec![AdapterEvent::new(
                    EventKind::SubAgent,
                    SubAgentReport::status(thread, SubAgentStatus::Failed)
                        .with_error(
                            params
                                .pointer("/error/message")
                                .and_then(Value::as_str)
                                .unwrap_or("the agent reported an error"),
                        )
                        .to_payload(),
                )];
            }
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
                    // This one is the turn's own total, so it is added up with
                    // the turn's other reports.
                    json!({ "usage": usage, "scope": "turn" }),
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

/// One `item/started` or `item/completed` payload.
///
/// The same switch serves both the parent and a delegated child: the only
/// difference is where the resulting event goes, never what it means.
fn item_event(thread: &str, params: &Value, child: bool, done: bool) -> Vec<AdapterEvent> {
    let item = params.get("item").cloned().unwrap_or(Value::Null);
    let kind = item.get("type").and_then(Value::as_str).unwrap_or("");
    match kind {
        // The delegation tool call itself. It names the children, so it is the
        // one place a child agent's card can be created.
        "collabAgentToolCall" => collab_items(&item),
        "subAgentActivity" => activity_items(&item),
        "agentMessage" => {
            // Codex reports the finished message twice: a
            // `message/completed`-style item with content, and the deltas that
            // built it. Only a non-empty item is an event of its own; an empty
            // one would put a blank assistant message in the ledger right before
            // the real text.
            let text = item_text(&item);
            if text.is_empty() {
                Vec::new()
            } else if child {
                // `final` makes the card replace the deltas' tail instead of
                // repeating the whole answer after them.
                vec![child_text(thread, &text, "answer", true)]
            } else {
                vec![AdapterEvent::new(
                    EventKind::MessageCompleted,
                    json!({ "text": text }),
                )]
            }
        }
        // The prompt is already in the ledger from `send`; echoing it back as an
        // event only adds noise.
        "userMessage" | "reasoning" => Vec::new(),
        "commandExecution" | "mcpToolCall" | "dynamicToolCall" | "fileChange" | "webSearch"
        | "imageView" => {
            if child {
                return child_tool(thread, &item, done).into_iter().collect();
            }
            match kind {
                "commandExecution" => {
                    if done {
                        vec![AdapterEvent::new(
                            EventKind::ToolResult,
                            json!({
                                "tool": "command",
                                "ok": item.get("status").and_then(Value::as_str) == Some("completed"),
                                "exitCode": item.get("exitCode").cloned().unwrap_or(Value::Null),
                            }),
                        )]
                    } else {
                        vec![AdapterEvent::new(
                            EventKind::ToolCall,
                            json!({
                                "tool": "command",
                                "args": item.get("command").cloned().unwrap_or(Value::Null),
                            }),
                        )]
                    }
                }
                other => vec![AdapterEvent::new(
                    EventKind::Unknown,
                    json!({ "type": other }),
                )],
            }
        }
        other => vec![AdapterEvent::new(
            EventKind::Unknown,
            json!({ "type": other }),
        )],
    }
}

/// A child's raw tool item, shaped like the parent's tool cards.
fn child_tool(thread: &str, item: &Value, done: bool) -> Option<AdapterEvent> {
    let kind = item.get("type").and_then(Value::as_str).unwrap_or("");
    let completed = item.get("status").and_then(Value::as_str) == Some("completed");
    let (name, args, output, ok) = match kind {
        "commandExecution" => (
            "command",
            json!({ "command": item.get("command").cloned().unwrap_or(Value::Null) }),
            item.get("aggregatedOutput").cloned().unwrap_or(Value::Null),
            completed,
        ),
        "mcpToolCall" => (
            "mcp",
            json!({
                "tool": item.get("tool").cloned().unwrap_or(Value::Null),
                "arguments": item.get("arguments").cloned().unwrap_or(Value::Null),
            }),
            item.get("error")
                .cloned()
                .or_else(|| item.get("result").cloned())
                .unwrap_or(Value::Null),
            completed,
        ),
        "dynamicToolCall" => (
            "tool",
            json!({
                "tool": item.get("tool").cloned().unwrap_or(Value::Null),
                "arguments": item.get("arguments").cloned().unwrap_or(Value::Null),
            }),
            item.get("contentItems").cloned().unwrap_or(Value::Null),
            item.get("success")
                .and_then(Value::as_bool)
                .unwrap_or(false),
        ),
        "fileChange" => (
            "file",
            json!({ "changes": item.get("changes").cloned().unwrap_or(Value::Null) }),
            Value::Null,
            completed,
        ),
        "webSearch" => (
            "web_search",
            json!({ "query": item.get("query").cloned().unwrap_or(Value::Null) }),
            item.get("results").cloned().unwrap_or(Value::Null),
            true,
        ),
        "imageView" => (
            "image",
            json!({ "path": item.get("path").cloned().unwrap_or(Value::Null) }),
            Value::Null,
            true,
        ),
        _ => return None,
    };
    let report = SubAgentReport::status(thread, SubAgentStatus::Running);
    let report = if done {
        report.with_tool_result(json!({
            "name": name, "args": args, "output": output, "ok": ok
        }))
    } else {
        report.with_tool(json!({ "name": name, "args": args }))
    };
    Some(AdapterEvent::new(EventKind::SubAgent, report.to_payload()))
}

/// The collab tool call: `spawnAgent`, `sendInput`, `wait`, `closeAgent`, …
///
/// `agentsStates` is the authoritative per-child status map; the call's own
/// status only describes the tool call. Spawning a child is what creates its
/// card, so only `spawnAgent` produces a `started` row.
fn collab_items(item: &Value) -> Vec<AdapterEvent> {
    let tool = item.get("tool").and_then(Value::as_str).unwrap_or("collab");
    let call_status = item
        .get("status")
        .and_then(Value::as_str)
        .unwrap_or("inProgress");
    let prompt = item.get("prompt").and_then(Value::as_str).unwrap_or("");
    let model = item.get("model").and_then(Value::as_str).unwrap_or("");
    let sender = item
        .get("senderThreadId")
        .and_then(Value::as_str)
        .unwrap_or("");
    let receivers = item
        .get("receiverThreadIds")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let states = item
        .get("agentsStates")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();

    receivers
        .iter()
        .filter_map(|receiver| {
            let thread = receiver.as_str().unwrap_or("");
            if thread.is_empty() {
                return None;
            }
            let state = states.get(thread);
            let status = state
                .and_then(|value| value.get("status"))
                .and_then(Value::as_str)
                .map(SubAgentStatus::from_codex)
                .unwrap_or_else(|| match call_status {
                    "completed" => SubAgentStatus::Completed,
                    "failed" => SubAgentStatus::Failed,
                    "interrupted" => SubAgentStatus::Interrupted,
                    _ => SubAgentStatus::Running,
                });
            let label = agent_label(thread);
            let report = if tool == "spawnAgent" && !status.is_terminal() {
                SubAgentReport::started(thread, &label)
                    .with_status(status)
                    .with_parent(sender)
                    .with_task(prompt)
                    .with_model(model)
            } else {
                SubAgentReport::status(thread, status)
                    .with_name(&label)
                    .with_parent(sender)
            };
            let report = match state
                .and_then(|value| value.get("message"))
                .and_then(Value::as_str)
            {
                Some(message) if status == SubAgentStatus::Failed && !message.is_empty() => {
                    report.with_error(message)
                }
                Some(message) if !message.trim().is_empty() => report.with_result(message),
                _ => report,
            };
            Some(AdapterEvent::new(EventKind::SubAgent, report.to_payload()))
        })
        .collect()
}

/// `subAgentActivity`: the lifecycle rows codex emits about a child thread.
fn activity_items(item: &Value) -> Vec<AdapterEvent> {
    let thread = item
        .get("agentThreadId")
        .and_then(Value::as_str)
        .unwrap_or("");
    if thread.is_empty() {
        return Vec::new();
    }
    let kind = item.get("kind").and_then(Value::as_str).unwrap_or("");
    let path = item.get("agentPath").and_then(Value::as_str).unwrap_or("");
    let status = SubAgentStatus::from_codex_activity(kind);
    let mut report = SubAgentReport::status(thread, status);
    if !path.trim().is_empty() {
        report = report.with_name(path);
    }
    vec![AdapterEvent::new(EventKind::SubAgent, report.to_payload())]
}

/// A child's own words. `final` marks the authoritative copy codex sends after
/// the deltas, so the card replaces rather than repeats.
fn child_text(thread: &str, text: &str, channel: &str, final_text: bool) -> AdapterEvent {
    let report = SubAgentReport::status(thread, SubAgentStatus::Running)
        .with_channel(channel)
        .with_text(text);
    let report = if final_text {
        report.as_final()
    } else {
        report
    };
    AdapterEvent::new(EventKind::SubAgent, report.to_payload())
}

/// Codex does not name a child, so the thread id is the only stable label.
fn agent_label(thread: &str) -> String {
    let short: String = thread.chars().take(8).collect();
    if short.is_empty() {
        "agent".to_string()
    } else {
        format!("agent {short}")
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

    fn handshake(&mut self, timeout: Option<Duration>) -> Result<()> {
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
        timeout: Option<Duration>,
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
        let deadline = timeout.map(|budget| Instant::now() + budget);
        loop {
            if let Some(deadline) = deadline
                && Instant::now() >= deadline
            {
                return Err(Error::Timeout(format!(
                    "codex did not answer {method} in time"
                )));
            }
            match self.next_message(Duration::from_millis(100)) {
                Some(message) => {
                    if message.get("id").and_then(Value::as_i64) == Some(id) {
                        return Ok(message);
                    }
                    if message.get("method").is_some() {
                        on_notification(message);
                    }
                }
                None => {
                    // No clock here: a request waits until the runtime answers.
                    // A runtime that already exited will never answer, so its
                    // exit is the only event worth failing on.
                    if let Ok(Some(status)) = self.child.try_wait() {
                        return Err(Error::Failed(format!(
                            "codex exited ({status}) before answering {method}"
                        )));
                    }
                }
            }
        }
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

    /// A spawn names its children; each becomes its own card with the task and
    /// model the parent handed over.
    #[test]
    fn a_spawned_agent_becomes_its_own_card() {
        let events = translate(
            "item/started",
            &json!({
                "threadId": "main", "turnId": "t1",
                "item": {
                    "type": "collabAgentToolCall",
                    "id": "c1",
                    "tool": "spawnAgent",
                    "status": "inProgress",
                    "prompt": "count the rust files",
                    "model": "gpt-6-astra",
                    "senderThreadId": "main",
                    "receiverThreadIds": ["child-1", "child-2"],
                    "agentsStates": {
                        "child-1": { "status": "running" },
                        "child-2": { "status": "pendingInit" }
                    }
                }
            }),
        );
        assert_eq!(events.len(), 2);
        assert!(events.iter().all(|event| event.kind == EventKind::SubAgent));
        let first = &events[0].payload;
        assert_eq!(first["agentId"], "child-1");
        assert_eq!(first["phase"], "started");
        assert_eq!(first["status"], "running");
        assert_eq!(first["task"], "count the rust files");
        assert_eq!(first["model"], "gpt-6-astra");
        assert_eq!(first["parentId"], "main");
        // A queued child says so; calling it "running" makes the card look stuck.
        assert_eq!(events[1].payload["status"], "pending");
    }

    /// `errored` and `notFound` must not read as finished-with-no-answer.
    #[test]
    fn a_child_that_errors_is_reported_on_its_own_card() {
        let events = translate(
            "item/completed",
            &json!({
                "threadId": "main", "turnId": "t1",
                "item": {
                    "type": "collabAgentToolCall",
                    "id": "c1",
                    "tool": "wait",
                    "status": "completed",
                    "senderThreadId": "main",
                    "receiverThreadIds": ["child-1"],
                    "agentsStates": {
                        "child-1": { "status": "errored", "message": "model quota exhausted" }
                    }
                }
            }),
        );
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].payload["phase"], "failed");
        assert_eq!(events[0].payload["status"], "failed");
        assert_eq!(events[0].payload["error"], "model quota exhausted");
    }

    /// `subAgentActivity` is the lifecycle channel; the path it carries is the
    /// only human-readable name codex gives a child.
    #[test]
    fn sub_agent_activity_moves_the_card() {
        let events = translate(
            "item/completed",
            &json!({
                "threadId": "main", "turnId": "t1",
                "item": {
                    "type": "subAgentActivity",
                    "id": "a1",
                    "agentThreadId": "child-1",
                    "agentPath": "scout",
                    "kind": "completed"
                }
            }),
        );
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].payload["agentId"], "child-1");
        assert_eq!(events[0].payload["name"], "scout");
        assert_eq!(events[0].payload["status"], "completed");
        assert_eq!(events[0].payload["phase"], "completed");
    }

    /// The bug this guard exists for: a child's text used to be appended to the
    /// parent's answer because notifications on every thread look alike.
    #[test]
    fn a_childs_answer_does_not_leak_into_the_parent() {
        let threads = Threads::new("main");
        let child = translate_in(
            &threads,
            "item/agentMessage/delta",
            &json!({ "threadId": "child-1", "delta": "child text" }),
        );
        assert_eq!(child.len(), 1);
        assert_eq!(child[0].kind, EventKind::SubAgent);
        assert_eq!(child[0].payload["text"], "child text");
        assert_eq!(child[0].payload["channel"], "answer");

        let parent = translate_in(
            &threads,
            "item/agentMessage/delta",
            &json!({ "threadId": "main", "delta": "parent text" }),
        );
        assert_eq!(parent.len(), 1);
        assert_eq!(parent[0].kind, EventKind::TextDelta);
    }

    /// Codex sends the finished message after the deltas, so that copy is
    /// marked final and the card replaces the tail instead of repeating it.
    #[test]
    fn a_finished_child_message_replaces_its_deltas() {
        let threads = Threads::new("main");
        let events = translate_in(
            &threads,
            "item/completed",
            &json!({
                "threadId": "child-1", "turnId": "t2",
                "item": {
                    "type": "agentMessage",
                    "id": "m1",
                    "text": "42 files under src"
                }
            }),
        );
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].payload["final"], true);
        assert_eq!(events[0].payload["text"], "42 files under src");
    }

    #[test]
    fn a_childs_command_is_a_tool_on_its_card() {
        let threads = Threads::new("main");
        let started = translate_in(
            &threads,
            "item/started",
            &json!({
                "threadId": "child-1", "turnId": "t2",
                "item": { "type": "commandExecution", "id": "x1", "command": "ls" }
            }),
        );
        assert_eq!(started[0].kind, EventKind::SubAgent);
        assert_eq!(started[0].payload["phase"], "tool");
        assert_eq!(started[0].payload["tool"]["name"], "command");

        let done = translate_in(
            &threads,
            "item/completed",
            &json!({
                "threadId": "child-1", "turnId": "t2",
                "item": {
                    "type": "commandExecution", "id": "x1",
                    "command": "ls", "status": "completed",
                    "exitCode": 0, "aggregatedOutput": "42 files"
                }
            }),
        );
        assert_eq!(done[0].payload["phase"], "tool_result");
        assert_eq!(done[0].payload["tool"]["output"], "42 files");
        assert_eq!(done[0].payload["tool"]["ok"], true);
    }

    /// The parent's own tool cards keep their existing shape: only delegated
    /// threads are re-routed onto a sub-agent card.
    #[test]
    fn the_parents_command_card_is_unchanged() {
        let events = translate(
            "item/started",
            &json!({
                "threadId": "main", "turnId": "t1",
                "item": { "type": "commandExecution", "id": "x1", "command": "ls" }
            }),
        );
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::ToolCall);
        assert_eq!(events[0].payload["tool"], "command");
    }

    /// A child's tokens are its own bill; adding them to the turn's usage would
    /// double-charge a delegated turn.
    #[test]
    fn a_childs_usage_is_not_the_turns_bill() {
        let threads = Threads::new("main");
        let child = translate_in(
            &threads,
            "thread/tokenUsage/updated",
            &json!({ "threadId": "child-1", "usage": { "totalTokens": 99 } }),
        );
        assert_eq!(child.len(), 1);
        assert_eq!(child[0].kind, EventKind::SubAgent);
        assert_eq!(child[0].payload["usage"]["totalTokens"], 99);

        let parent = translate_in(
            &threads,
            "thread/tokenUsage/updated",
            &json!({ "threadId": "main", "usage": { "totalTokens": 5 } }),
        );
        assert_eq!(parent[0].kind, EventKind::Usage);
    }

    /// The parent's turn ends on the parent's `turn/completed` only.
    #[test]
    fn only_the_parent_thread_counts_as_main() {
        let threads = Threads::new("main");
        assert!(!threads.is_child("main"));
        assert!(threads.is_child("child-1"));
        assert!(!threads.is_child(""));
    }

    /// Codex reads the file itself (`localImage` takes a path), so an image is
    /// one extra input item — not base64 pasted into the text item.
    #[test]
    fn an_image_becomes_a_local_image_input_item() {
        let images = vec![
            PathBuf::from(r"D:\Downloads\陈思成.png"),
            PathBuf::from(r"D:\Downloads\second shot.jpg"),
        ];
        let items = turn_input("看看这两张图", &images);
        assert_eq!(items.len(), 3);
        assert_eq!(items[0]["type"], "text");
        assert_eq!(items[0]["text"], "看看这两张图");
        assert_eq!(items[1]["type"], "localImage");
        assert_eq!(items[1]["path"], r"D:\Downloads\陈思成.png");
        assert_eq!(items[2]["path"], r"D:\Downloads\second shot.jpg");
    }

    #[test]
    fn a_text_only_turn_sends_exactly_one_text_item() {
        let items = turn_input("hello", &[]);
        assert_eq!(items, vec![json!({ "type": "text", "text": "hello" })]);
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
    fn reasoning_notifications_become_thinking_events() {
        let events = translate("item/reasoning/partial", &json!({ "delta": "检查文件" }));
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::Thinking);
        assert_eq!(events[0].payload["delta"], "检查文件");
        assert!(translate("item/reasoning/partial", &json!({})).is_empty());
    }

    #[test]
    fn message_items_do_not_double_report_a_turn() {
        // Observed on 0.153: `item/started` and `item/completed` for both the
        // user message and the agent message. Only a completed message with
        // text is an event; the prompt is already in the ledger.
        assert!(
            translate(
                "item/started",
                &json!({ "item": { "type": "userMessage" } })
            )
            .is_empty()
        );
        assert!(
            translate(
                "item/completed",
                &json!({ "item": { "type": "userMessage" } })
            )
            .is_empty()
        );
        assert!(
            translate(
                "item/started",
                &json!({ "item": { "type": "agentMessage" } })
            )
            .is_empty()
        );
        // An empty completion would be a blank assistant message.
        assert!(
            translate(
                "item/completed",
                &json!({ "item": { "type": "agentMessage" } })
            )
            .is_empty()
        );

        let full = translate(
            "item/completed",
            &json!({ "item": { "type": "agentMessage", "content": [{ "type": "output_text", "text": "ok" }] } }),
        );
        assert_eq!(full.len(), 1);
        assert_eq!(full[0].kind, EventKind::MessageCompleted);
        assert_eq!(full[0].payload["text"], "ok");
    }

    #[test]
    fn thread_token_usage_becomes_a_usage_event() {
        let events = translate(
            "thread/tokenUsage/updated",
            &json!({ "threadId": "t", "usage": { "totalTokens": 42 } }),
        );
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::Usage);
        assert_eq!(events[0].payload["usage"]["totalTokens"], 42);
        // A thread total, not this turn's bill: the UI must not add two of
        // these together, so the scope travels with the event.
        assert_eq!(events[0].payload["scope"], "thread");
    }

    #[test]
    fn turn_usage_is_tagged_as_the_turns_own_total() {
        let events = translate(
            "turn/completed",
            &json!({ "turn": { "status": "completed", "usage": { "inputTokens": 10, "outputTokens": 3 } } }),
        );
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::Usage);
        assert_eq!(events[0].payload["scope"], "turn");
        assert_eq!(events[0].payload["usage"]["outputTokens"], 3);
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

#[cfg(test)]
mod persona_tests {
    use super::*;

    #[test]
    fn a_persona_is_injected_ahead_of_the_user_message() {
        let opened = OpenedSession {
            cwd: "D:/work/api".into(),
            model: "codex-auto-review".into(),
            agent_id: "migration-engineer".into(),
            system_prompt: "你是 {{agent}}，仓库 {{repo_path}}".into(),
            append_system_prompt: "历史结论".into(),
            ..Default::default()
        };
        let text = effective_prompt(&opened, "你叫什么名字？");
        assert!(text.starts_with("[Agent instructions"));
        assert!(text.contains("你是 migration-engineer，仓库 D:/work/api"));
        assert!(text.contains("历史结论"));
        assert!(text.ends_with("[User message]\n你叫什么名字？"));
    }

    #[test]
    fn without_a_persona_the_prompt_is_untouched() {
        let text = effective_prompt(&OpenedSession::default(), "hello");
        assert_eq!(text, "hello");
    }
}
