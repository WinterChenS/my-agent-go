//! Pi adapter (`pi --mode json`).
//!
//! Facts this implementation depends on, all measured on this machine:
//!
//! * `--mode json` prints one JSON object per line: `session`, `agent_start`,
//!   `turn_start`, `message_start`, `message_update`
//!   (`assistantMessageEvent`: `text_start` / `text_delta` / `text_end`),
//!   `message_end`, `turn_end`, `agent_end`, `agent_settled`;
//! * **stdin must be closed immediately**. With a pipe or a console attached,
//!   `pi -p` blocks forever, even for a model that does not exist. The child is
//!   spawned with `Stdio::null()` and a test guards it;
//! * `--session-id <id>` reuses the current directory's session with that id and
//!   creates it otherwise, so a follow-up turn needs no long-lived process;
//! * `--session-dir` keeps session files under the D:-only state directory.

use std::io::{BufRead, BufReader};
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

use serde_json::Value;

use crate::adapter::{AdapterEvent, Cancel, RunOutcome, SessionAdapter};
use crate::error::{Error, Result};
use crate::model::{
    EventKind, OpenedSession, RunStatus, SessionCapabilities, SessionContext, SessionSpec,
};

/// Adapter id, matching the catalog.
pub const ID: &str = "pi";

/// How long pi may stay silent before the turn is called dead.
///
/// Measured here: with a model whose provider refuses the call, pi opens the
/// turn, records a pending assistant message and then says nothing at all — no
/// retry event, no error, no exit — for minutes. A first token normally arrives
/// in a second or two, so 45 s is generous and still far below the turn budget.
const FIRST_TOKEN_TIMEOUT: Duration = Duration::from_secs(45);

/// Pi adapter.
pub struct PiAdapter {
    executable: Option<PathBuf>,
}

impl PiAdapter {
    #[must_use]
    pub fn new(executable: Option<PathBuf>) -> Self {
        Self { executable }
    }

    fn executable(&self) -> Result<&PathBuf> {
        self.executable.as_ref().ok_or_else(|| {
            Error::NotFound("pi executable not found on PATH (install pi or fix PATH)".into())
        })
    }

    fn command(
        &self,
        context: &SessionContext,
        opened: &OpenedSession,
        allow_tools: bool,
        tools: &[String],
        prompt: &str,
    ) -> Result<Command> {
        let executable = self.executable()?;
        let session_dir = context.state_dir.join(ID);
        std::fs::create_dir_all(&session_dir)?;
        // The child runs inside the project directory now, so a relative
        // `--session-dir` would land inside the user's project. Resolve it once.
        let session_dir = std::fs::canonicalize(&session_dir).unwrap_or(session_dir);

        let mut command = super::command_for(executable);
        command
            .arg("-p")
            .arg(prompt)
            .arg("--mode")
            .arg("json")
            .arg("--session-id")
            .arg(&opened.native_id)
            .arg("--session-dir")
            .arg(&session_dir)
            .arg("--model")
            .arg(&opened.model)
            // See the module docs: a live stdin makes pi wait forever.
            .current_dir(&opened.cwd)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if !allow_tools {
            command.arg("--no-tools");
        } else if !tools.is_empty() {
            // Pi's own allowlist: the run cannot use a tool that is not named,
            // so a case can prove "read only" rather than trusting the prompt.
            command.arg("--tools").arg(tools.join(","));
        }
        Ok(command)
    }
}

impl SessionAdapter for PiAdapter {
    fn id(&self) -> &str {
        ID
    }

    fn capabilities(&self) -> SessionCapabilities {
        SessionCapabilities {
            streaming: true,
            resume: true,
            abort: true,
            tools: true,
            // Pi asks for approval through extension UI requests, which this
            // build does not answer yet; R2-03 turns this on.
            approvals: false,
            compact: true,
            fork: true,
            usage: true,
        }
    }

    fn open(&self, context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession> {
        // Resolve the executable now so a misconfigured host fails at open time,
        // not at the first message.
        self.executable()?;
        std::fs::create_dir_all(context.state_dir.join(ID))?;
        let native_id = spec
            .native_id
            .clone()
            .filter(|value| !value.trim().is_empty())
            .unwrap_or_else(|| format!("anvil-{}", crate::clock::unix_millis()));
        Ok(OpenedSession {
            runtime_id: ID.to_string(),
            native_id,
            model: spec.model.clone(),
            cwd: spec.cwd.clone(),
            allow_tools: spec.allow_tools,
            tools: spec.tools.clone(),
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
        let mut child = self
            .command(context, opened, opened.allow_tools, &opened.tools, prompt)?
            .spawn()
            .map_err(|error| Error::Failed(format!("spawn pi: {error}")))?;

        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| Error::Io("pi stdout was not piped".into()))?;
        let (sender, receiver) = mpsc::channel::<String>();
        std::thread::spawn(move || {
            for line in BufReader::new(stdout)
                .lines()
                .map_while(std::result::Result::ok)
            {
                if sender.send(line).is_err() {
                    break;
                }
            }
        });

        let deadline = Instant::now() + context.run_timeout;
        let mut deltas = String::new();
        let mut final_text: Option<String> = None;
        let mut usage: Option<Value> = None;
        let mut failure: Option<String> = None;
        // Pi retries a rejected provider call itself and then can sit on a
        // pending message forever: the child stays alive, no token arrives, and
        // the only record of the reason is `auto_retry_start`. Keep that message
        // for the failure, and hold the turn to one silence budget — a retry
        // must not extend it, or repeated retries postpone the failure forever.
        let mut retry_error: Option<String> = None;
        let first_token = Instant::now() + FIRST_TOKEN_TIMEOUT;
        let mut produced = false;

        loop {
            if cancel.is_cancelled() {
                super::kill_tree(&mut child);
                sink(AdapterEvent::new(
                    EventKind::Aborted,
                    serde_json::json!({ "reason": "user" }),
                ));
                return Ok(RunOutcome::aborted(&deltas));
            }
            if !produced && Instant::now() >= first_token {
                super::kill_tree(&mut child);
                return Ok(RunOutcome {
                    status: RunStatus::Failed,
                    text: String::new(),
                    error: Some(retry_error.clone().unwrap_or_else(|| {
                        format!(
                            "pi produced no output within {}s; model {:?} may be unavailable or not signed in",
                            FIRST_TOKEN_TIMEOUT.as_secs(),
                            opened.model
                        )
                    })),
                    usage: None,
                    native_id: None,
                });
            }
            if Instant::now() >= deadline {
                super::kill_tree(&mut child);
                return Err(Error::Timeout(format!(
                    "pi did not finish within {:?}",
                    context.run_timeout
                )));
            }
            match receiver.recv_timeout(Duration::from_millis(100)) {
                Ok(line) => {
                    let Some(value) = parse_line(&line) else {
                        continue;
                    };
                    // Pi echoes the session it opened. If it is not the one we
                    // asked for, the user's next turn would land somewhere else,
                    // so this is an error rather than a curiosity.
                    if value.get("type").and_then(Value::as_str) == Some("session") {
                        let reported = value.get("id").and_then(Value::as_str).unwrap_or("");
                        if !reported.is_empty() && reported != opened.native_id {
                            sink(AdapterEvent::new(
                                EventKind::Error,
                                serde_json::json!({
                                    "message": format!(
                                        "pi opened session {reported} instead of {}",
                                        opened.native_id
                                    )
                                }),
                            ));
                        }
                        continue;
                    }
                    // Recorded for the failure text; it does not move the
                    // silence deadline.
                    if value.get("type").and_then(Value::as_str) == Some("auto_retry_start")
                        && let Some(message) = value.get("errorMessage").and_then(Value::as_str)
                    {
                        retry_error = Some(message.to_string());
                    }
                    for event in translate(&value) {
                        if matches!(
                            event.kind,
                            EventKind::TextDelta
                                | EventKind::MessageCompleted
                                | EventKind::ToolCall
                        ) {
                            produced = true;
                        }
                        if event.kind == EventKind::TextDelta
                            && let Some(delta) = event.payload.get("delta").and_then(Value::as_str)
                        {
                            deltas.push_str(delta);
                        }
                        if event.kind == EventKind::Error {
                            failure = event
                                .payload
                                .get("message")
                                .and_then(Value::as_str)
                                .map(str::to_string)
                                .or_else(|| Some(event.payload.to_string()));
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
                }
                Err(mpsc::RecvTimeoutError::Timeout) => continue,
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
            }
        }

        let status = child
            .wait()
            .map_err(|error| Error::Io(format!("wait for pi: {error}")))?;
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
        if !status.success() {
            return Ok(RunOutcome {
                status: RunStatus::Failed,
                text,
                // The exit code alone says nothing actionable; the provider's own
                // message is what the page can show.
                error: Some(
                    retry_error
                        .clone()
                        .unwrap_or_else(|| format!("pi exited with {status}")),
                ),
                usage,
                native_id: None,
            });
        }
        Ok(RunOutcome {
            status: RunStatus::Completed,
            text,
            error: None,
            usage,
            native_id: None,
        })
    }
}

fn parse_line(line: &str) -> Option<Value> {
    let trimmed = line.trim();
    if !trimmed.starts_with('{') {
        return None;
    }
    serde_json::from_str(trimmed).ok()
}

/// Map one pi JSON event onto the ledger vocabulary.
///
/// Unknown shapes are kept as `Unknown` with their payload: the contract says an
/// event this build does not model must still be recorded, not dropped.
fn translate(value: &Value) -> Vec<AdapterEvent> {
    let mut events = Vec::new();
    let kind = value.get("type").and_then(Value::as_str).unwrap_or("");
    match kind {
        "message_update" => {
            let inner = value.get("assistantMessageEvent").unwrap_or(&Value::Null);
            match inner.get("type").and_then(Value::as_str).unwrap_or("") {
                "text_delta" => {
                    let delta = inner.get("delta").and_then(Value::as_str).unwrap_or("");
                    events.push(AdapterEvent::text_delta(delta));
                }
                // `turn_end` carries the final text; emitting it here too would
                // double every assistant message in the ledger.
                "text_end" | "text_start" => {}
                other => events.push(AdapterEvent::new(
                    EventKind::Unknown,
                    serde_json::json!({ "type": other }),
                )),
            }
        }
        "turn_end" => {
            let message = value.get("message").cloned().unwrap_or(Value::Null);
            let text = message
                .get("content")
                .and_then(Value::as_array)
                .map(|parts| {
                    parts
                        .iter()
                        .filter(|part| part.get("type").and_then(Value::as_str) == Some("text"))
                        .filter_map(|part| part.get("text").and_then(Value::as_str))
                        .collect::<String>()
                })
                .unwrap_or_default();
            let usage = message.get("usage").cloned().unwrap_or(Value::Null);
            events.push(AdapterEvent::new(
                EventKind::MessageCompleted,
                serde_json::json!({ "text": text, "finalText": text }),
            ));
            if !usage.is_null() {
                events.push(AdapterEvent::new(
                    EventKind::Usage,
                    serde_json::json!({ "usage": usage }),
                ));
            }
        }
        "tool_execution_start" => events.push(AdapterEvent::new(
            EventKind::ToolCall,
            serde_json::json!({
                "tool": value.get("toolName").cloned().unwrap_or(Value::Null),
                "args": value.get("args").cloned().unwrap_or(Value::Null),
            }),
        )),
        "tool_execution_end" => events.push(AdapterEvent::new(
            EventKind::ToolResult,
            serde_json::json!({
                "tool": value.get("toolName").cloned().unwrap_or(Value::Null),
                "ok": value.get("isError").map(|flag| !flag.as_bool().unwrap_or(false)),
            }),
        )),
        "agent_start" | "turn_start" | "message_start" | "message_end" | "agent_end"
        | "agent_settled" => {}
        // Pi retries a failed provider call on its own. The message is the only
        // place the upstream reason appears, so it is recorded rather than
        // collapsed into an opaque `Unknown`.
        "auto_retry_start" => events.push(AdapterEvent::new(
            EventKind::Unknown,
            serde_json::json!({
                "type": "auto_retry",
                "attempt": value.get("attempt").cloned().unwrap_or(Value::Null),
                "maxAttempts": value.get("maxAttempts").cloned().unwrap_or(Value::Null),
                "message": value.get("errorMessage").cloned().unwrap_or(Value::Null),
            }),
        )),
        "" => {}
        other => events.push(AdapterEvent::new(
            EventKind::Unknown,
            serde_json::json!({ "type": other }),
        )),
    }
    events
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn text_delta_is_translated() {
        let value = serde_json::json!({
            "type": "message_update",
            "assistantMessageEvent": { "type": "text_delta", "delta": "ok" }
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::TextDelta);
        assert_eq!(events[0].payload["delta"], "ok");
    }

    /// A rejected provider call only surfaces through pi's retry event. Dropping
    /// the message is what made a dead turn look like a slow one.
    #[test]
    fn a_provider_retry_keeps_the_reason_it_was_raised_for() {
        let value = serde_json::json!({
            "type": "auto_retry_start",
            "attempt": 3,
            "maxAttempts": 3,
            "errorMessage": "502: sign in with a browser instead"
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::Unknown);
        assert_eq!(events[0].payload["type"], "auto_retry");
        assert_eq!(
            events[0].payload["message"],
            "502: sign in with a browser instead"
        );
        assert_eq!(events[0].payload["maxAttempts"], 3);
    }

    #[test]
    fn turn_end_carries_final_text_and_usage() {
        let value = serde_json::json!({
            "type": "turn_end",
            "message": {
                "role": "assistant",
                "content": [{ "type": "text", "text": "ok" }],
                "usage": { "totalTokens": 7 }
            }
        });
        let events = translate(&value);
        assert!(
            events
                .iter()
                .any(|event| event.payload["finalText"] == "ok")
        );
        assert!(events.iter().any(|event| event.kind == EventKind::Usage));
    }

    #[test]
    fn unknown_events_are_kept_not_dropped() {
        let events = translate(&serde_json::json!({ "type": "brand_new_event" }));
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::Unknown);
        assert_eq!(events[0].payload["type"], "brand_new_event");
    }

    #[test]
    fn non_json_lines_are_ignored() {
        assert!(parse_line("Starting pi...").is_none());
        assert!(parse_line("{\"type\":\"agent_start\"}").is_some());
    }
}
