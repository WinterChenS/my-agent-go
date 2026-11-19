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

use crate::adapter::{AdapterEvent, Cancel, RunOutcome, SessionAdapter, Turn};
use crate::error::{Error, Result};
use crate::model::{
    EventKind, OpenedSession, RunStatus, SessionCapabilities, SessionContext, SessionSpec,
};
use crate::subagent::{SubAgentReport, SubAgentStatus};

/// Adapter id, matching the catalog.
pub const ID: &str = "pi";

/// Pick the directory that holds this conversation's pi transcript.
///
/// The default per-conversation directory (`<state_dir>/<session-id>`) is
/// already inside the workspace, so the transcript belongs right there and the
/// user sees one folder per conversation. A conversation explicitly bound to
/// one of the user's own projects must NOT have runtime files dropped into it;
/// for those we return `None` and the caller uses a dedicated
/// `<state_dir>/<native-id>` folder instead.
fn session_dir_for(context: &SessionContext, cwd: &str) -> Option<PathBuf> {
    if cwd.trim().is_empty() {
        return None;
    }
    let root = context.state_dir.to_string_lossy().to_lowercase();
    let candidate = cwd.to_lowercase();
    if candidate.starts_with(&root) {
        Some(PathBuf::from(cwd))
    } else {
        None
    }
}

/// pi's built-in tools. Naming all of them means "the defaults", not "narrow".
const BUILTIN_TOOLS: [&str; 7] = ["read", "write", "edit", "bash", "grep", "find", "ls"];

/// The tool a headless run must never offer: the model would ask a question and
/// wait for an answer nothing in this build can give.
const HEADLESS_EXCLUDE: &str = "ask_question";

/// `--tools` / `--exclude-tools` for one run.
///
/// A full allowlist (`--tools read,write,edit,bash,grep,find,ls,subagent`) is the
/// obvious encoding of "exactly these tools" and it is wrong for a *resumed*
/// session: measured on pi 1.5.0, resuming a session whose transcript recorded a
/// loadout without `subagent` restores that old loadout and drops the extension
/// tool, even though the extension is installed and the name is in the
/// allowlist. The session then answers "I don't have a subagent tool" and the
/// model improvises by spawning `pi -p` through bash, which is the loop that
/// left a turn hanging for eight minutes.
///
/// Expressing the same intent as an exclusion list keeps the tool: with no
/// allowlist, pi activates extension tools that register after the transcript is
/// restored (`_restoreToolsFromTranscript` → `_buildRuntime`). Measured on the
/// same resumed session: allowlist → 7 tools, no subagent; exclusion → 9 tools,
/// including subagent.
///
/// A narrowed list (a subset of the built-ins) keeps the allowlist: the point
/// there is to remove tools, and exclusions cannot express "only read and grep".
fn tool_flags(tools: &[String]) -> Vec<String> {
    let extras: Vec<&String> = tools
        .iter()
        .filter(|tool| !BUILTIN_TOOLS.contains(&tool.as_str()))
        .collect();
    // An explicit `ask_question` is the caller's decision, not silence to
    // override: only a widened list that does *not* name it becomes exclusions.
    let widened = !extras.is_empty()
        && BUILTIN_TOOLS
            .iter()
            .all(|builtin| tools.iter().any(|tool| tool == builtin))
        && !extras.iter().any(|tool| tool.as_str() == HEADLESS_EXCLUDE);
    if widened {
        vec![
            "--exclude-tools".to_string(),
            HEADLESS_EXCLUDE.to_string(),
        ]
    } else {
        vec!["--tools".to_string(), tools.join(",")]
    }
}

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
        images: &[PathBuf],
    ) -> Result<Command> {
        let executable = self.executable()?;
        // One directory per conversation. When the conversation is not bound to
        // a user project, its cwd already is `<state_dir>/<session-id>`, so the
        // pi transcript lives right there. A conversation bound to a real
        // project keeps its transcript under `<state_dir>/<native-id>` instead,
        // so the user's project tree is never polluted with runtime files.
        let session_dir = session_dir_for(context, &opened.cwd)
            .unwrap_or_else(|| context.state_dir.join(&opened.native_id));
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
            for flag in tool_flags(tools) {
                command.arg(flag);
            }
        }
        // Project-local trust. A print-mode run cannot show the trust prompt, so
        // without an override the folder's `.pi/extensions`, `.pi/skills`,
        // `.pi/settings.json` and `.pi/mcp.json` are skipped. The permission
        // picker decides per turn whether to load them.
        match opened.project_trust {
            Some(true) => {
                command.arg("--approve");
            }
            Some(false) => {
                command.arg("--no-approve");
            }
            None => {}
        }
        // Persona. `--system-prompt` replaces the coding-assistant prompt, which
        // is what an agent means: the user wrote who this agent is. Variables are
        // rendered here because the values are per session, not per save.
        if !opened.system_prompt.trim().is_empty() {
            let rendered = crate::persona::render(
                &opened.system_prompt,
                &crate::persona::PersonaVars {
                    repo_path: crate::persona::display_path(&opened.cwd),
                    model: &opened.model,
                    agent: &opened.agent_id,
                    date: &crate::clock::today(),
                },
            );
            command.arg("--system-prompt").arg(rendered);
        }
        if !opened.append_system_prompt.trim().is_empty() {
            command
                .arg("--append-system-prompt")
                .arg(&opened.append_system_prompt);
        }
        if !opened.thinking.trim().is_empty() {
            command.arg("--thinking").arg(opened.thinking.trim());
        }
        for skill in &opened.skills {
            if !skill.trim().is_empty() {
                command.arg("--skill").arg(skill);
            }
        }
        // Attachments. `pi [options] [--] [@files...] [messages...]` turns each
        // `@path` into prompt content by itself: an image becomes an image
        // content block the model can look at, a text file is inlined. Reading
        // everything here and pasting it into the prompt is what made a PNG
        // unreachable. `--` ends option parsing, so a path is never read as one.
        if !images.is_empty() {
            command.arg("--");
            for image in images {
                command.arg(format!("@{}", image.display()));
            }
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

    /// Pi reads `@path` itself; images become image content blocks.
    fn supports_images(&self) -> bool {
        true
    }

    fn open(&self, context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession> {
        // Resolve the executable now so a misconfigured host fails at open time,
        // not at the first message.
        self.executable()?;
        let native_id = spec
            .native_id
            .clone()
            .filter(|value| !value.trim().is_empty())
            .unwrap_or_else(|| format!("anvil-{}", crate::clock::unix_millis()));
        // Per-conversation directory (see `session_dir_for` / `command`).
        let session_dir = session_dir_for(context, &spec.cwd)
            .unwrap_or_else(|| context.state_dir.join(&native_id));
        std::fs::create_dir_all(&session_dir)?;
        // Conversations created before per-session directories kept their
        // transcript in the shared `<state_dir>/pi/` folder. Copy the matching
        // file across so resuming an old conversation still finds it. pi names
        // files `<timestamp>_<native-id>.jsonl`.
        let legacy_dir = context.state_dir.join(ID);
        if legacy_dir.is_dir() {
            let suffix = format!("_{native_id}.jsonl");
            if let Ok(entries) = std::fs::read_dir(&legacy_dir) {
                for entry in entries.flatten() {
                    let name = entry.file_name();
                    let name = name.to_string_lossy();
                    if name.ends_with(&suffix) {
                        let target = session_dir.join(name.as_ref());
                        if !target.exists() {
                            let _ = std::fs::copy(entry.path(), &target);
                        }
                    }
                }
            }
        }
        Ok(OpenedSession {
            runtime_id: ID.to_string(),
            native_id,
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
        let mut child = self
            .command(
                context,
                opened,
                opened.allow_tools,
                &opened.tools,
                turn.prompt,
                turn.images,
            )?
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

        // No deadline by default. A turn ends when pi finishes, fails, or the
        // user stops it; `run_timeout` is only set by tests and the report
        // example, which need a broken adapter to fail instead of hang. Cutting
        // real turns at a round number threw away replies that were still being
        // written — a run that reads files and reasons between them can take
        // far longer than any fixed budget, and that is work, not a hang.
        let deadline = context.run_timeout.map(|budget| Instant::now() + budget);
        let mut deltas = String::new();
        let mut final_text: Option<String> = None;
        let mut usage: Option<Value> = None;
        let mut failure: Option<String> = None;
        // Pi retries a rejected provider call itself: the child stays alive, no
        // token arrives, and the only record of the reason is `auto_retry_start`.
        // Keep that message for the failure text when the run ends.
        let mut retry_error: Option<String> = None;

        loop {
            if cancel.is_cancelled() {
                super::kill_tree(&mut child);
                sink(AdapterEvent::new(
                    EventKind::Aborted,
                    serde_json::json!({ "reason": "user" }),
                ));
                return Ok(RunOutcome::aborted(&deltas));
            }
            if let Some(deadline) = deadline
                && Instant::now() >= deadline
            {
                super::kill_tree(&mut child);
                return Err(Error::Timeout(format!(
                    "pi did not finish within {:?}",
                    context.run_timeout.unwrap_or_default()
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
                "text_end" | "text_start" | "thinking_start" | "thinking_end" => {}
                // Pi streams reasoning as `thinking_delta` inside the same
                // `message_update` wrapper as text. Keep the text: collapsing
                // it into `Unknown` made every turn look like it never thought.
                "thinking_delta" => {
                    let delta = inner.get("delta").and_then(Value::as_str).unwrap_or("");
                    if !delta.is_empty() {
                        events.push(AdapterEvent::new(
                            EventKind::Thinking,
                            serde_json::json!({ "delta": delta }),
                        ));
                    }
                }
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
        "tool_execution_start" => {
            let tool = value.get("toolName").and_then(Value::as_str).unwrap_or("");
            if is_subagent_tool(tool) {
                events.extend(subagent_started(value));
            } else {
                events.push(AdapterEvent::new(
                    EventKind::ToolCall,
                    serde_json::json!({
                        "tool": value.get("toolName").cloned().unwrap_or(Value::Null),
                        "args": value.get("args").cloned().unwrap_or(Value::Null),
                    }),
                ));
            }
        }
        // The `subagent` extension reports progress through the tool's own
        // update channel: every child's whole transcript, re-sent. Recorded as
        // a snapshot so the card replaces rather than appends.
        "tool_execution_update" => {
            let tool = value.get("toolName").and_then(Value::as_str).unwrap_or("");
            if is_subagent_tool(tool) {
                events.extend(subagent_snapshot(value));
            }
        }
        "tool_execution_end" => {
            let tool = value.get("toolName").and_then(Value::as_str).unwrap_or("");
            if is_subagent_tool(tool) {
                events.extend(subagent_finished(value));
            } else {
                events.push(AdapterEvent::new(
                    EventKind::ToolResult,
                    serde_json::json!({
                        "tool": value.get("toolName").cloned().unwrap_or(Value::Null),
                        "ok": value.get("isError").map(|flag| !flag.as_bool().unwrap_or(false)),
                    }),
                ));
            }
        }
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

// ---------------------------------------------------------------------------
// Delegated agents (`subagent` extension)
// ---------------------------------------------------------------------------

/// One step's text, and the whole transcript's budget.
///
/// A sub-agent can print a whole file. The card is a progress view, not an
/// archive, so the head is kept and the cut is stated rather than silently
/// dropping the tail.
const STEP_TEXT_CHARS: usize = 8_000;
const STEPS_TOTAL_CHARS: usize = 60_000;
const MAX_STEPS: usize = 80;

/// pi itself has no sub-agents; its official `subagent` extension registers a
/// tool that spawns one child process per agent. The tool name is the only
/// stable thing on the wire, so the match is deliberately narrow: guessing on
/// names like `agent` would swallow an unrelated tool's output.
fn is_subagent_tool(name: &str) -> bool {
    let name = name.to_ascii_lowercase();
    name == "subagent" || name.starts_with("subagent:") || name == "spawn_subagent"
}

fn tool_call_id(value: &Value) -> String {
    value
        .get("toolCallId")
        .and_then(Value::as_str)
        .map(str::to_string)
        .unwrap_or_else(|| "subagent".to_string())
}

/// Stable per-child key: one delegation call can spawn several children.
fn agent_key(call: &str, index: usize) -> String {
    format!("{call}#{index}")
}

/// `{index, name, task, step}` for every child a call is about to run.
///
/// The extension accepts exactly one of three shapes: `{agent,task}`,
/// `{tasks:[…]}` (parallel) and `{chain:[…]}` (sequential). Reading the same
/// shapes here is what lets a parallel delegation show up as N cards instead of
/// one.
fn subagent_dispatch(args: &Value) -> Vec<(usize, String, String, Option<i64>)> {
    let read = |item: &Value, index: usize, step: Option<i64>| {
        let name = item
            .get("agent")
            .and_then(Value::as_str)
            .unwrap_or("agent")
            .to_string();
        let task = item
            .get("task")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string();
        (index, name, task, step)
    };

    if let Some(tasks) = args.get("tasks").and_then(Value::as_array) {
        return tasks
            .iter()
            .enumerate()
            .map(|(index, item)| read(item, index, None))
            .collect();
    }
    if let Some(chain) = args.get("chain").and_then(Value::as_array) {
        return chain
            .iter()
            .enumerate()
            .map(|(index, item)| read(item, index, Some(index as i64 + 1)))
            .collect();
    }
    if args.get("agent").is_some() || args.get("task").is_some() {
        return vec![read(args, 0, None)];
    }
    Vec::new()
}

fn subagent_started(value: &Value) -> Vec<AdapterEvent> {
    let call = tool_call_id(value);
    let args = value.get("args").cloned().unwrap_or(Value::Null);
    let model = args.get("model").and_then(Value::as_str).unwrap_or("");

    subagent_dispatch(&args)
        .into_iter()
        .map(|(index, name, task, step)| {
            let mut report = SubAgentReport::started(&agent_key(&call, index), &name)
                .with_task(&task)
                .with_model(model);
            if let Some(step) = step {
                report = report.with_step(step);
            }
            AdapterEvent::new(EventKind::SubAgent, report.to_payload())
        })
        .collect()
}

/// Both update and end carry `details.results`, under different parents.
fn results_of(value: &Value) -> &[Value] {
    value
        .pointer("/partialResult/details/results")
        .or_else(|| value.pointer("/result/details/results"))
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .unwrap_or(&[])
}

fn subagent_snapshot(value: &Value) -> Vec<AdapterEvent> {
    let call = tool_call_id(value);
    let mut events = Vec::new();
    for (index, result) in results_of(value).iter().enumerate() {
        let name = result.get("agent").and_then(Value::as_str).unwrap_or("");
        let steps = messages_to_steps(result.get("messages"));
        let empty = steps.as_array().is_none_or(Vec::is_empty);
        let mut report = SubAgentReport::status(&agent_key(&call, index), SubAgentStatus::Running)
            .with_name(name);
        if let Some(step) = result.get("step").and_then(Value::as_i64) {
            report = report.with_step(step);
        }
        if !empty {
            report = report.with_steps(steps);
        }
        events.push(AdapterEvent::new(EventKind::SubAgent, report.to_payload()));
    }
    events
}

fn subagent_finished(value: &Value) -> Vec<AdapterEvent> {
    let call = tool_call_id(value);
    let mut events = Vec::new();
    for (index, result) in results_of(value).iter().enumerate() {
        let name = result.get("agent").and_then(Value::as_str).unwrap_or("");
        let exit = result.get("exitCode").and_then(Value::as_i64).unwrap_or(1);
        let message = result
            .get("errorMessage")
            .and_then(Value::as_str)
            .unwrap_or("");
        let stderr = result.get("stderr").and_then(Value::as_str).unwrap_or("");
        let ok = exit == 0 && message.is_empty();
        let status = if ok {
            SubAgentStatus::Completed
        } else {
            SubAgentStatus::Failed
        };
        let mut report = SubAgentReport::status(&agent_key(&call, index), status)
            .with_name(name)
            .with_result(&last_assistant_text(result.get("messages")))
            .with_usage(result.get("usage").cloned().unwrap_or(Value::Null));
        if let Some(step) = result.get("step").and_then(Value::as_i64) {
            report = report.with_step(step);
        }
        if !ok {
            report = report.with_error(if message.is_empty() { stderr } else { message });
        }
        events.push(AdapterEvent::new(EventKind::SubAgent, report.to_payload()));
    }
    events
}

/// Turn one child's message list into the card's ordered content.
///
/// Assistant text is folded between tool calls so a card reads "said this, ran
/// that, said this" instead of one wall of text with every call at the end.
fn messages_to_steps(messages: Option<&Value>) -> Value {
    let mut steps: Vec<Value> = Vec::new();
    let Some(messages) = messages.and_then(Value::as_array) else {
        return Value::Array(steps);
    };
    let mut budget = STEPS_TOTAL_CHARS;

    for message in messages {
        if steps.len() >= MAX_STEPS || budget == 0 {
            steps.push(serde_json::json!({
                "kind": "text",
                "text": "(transcript truncated: the agent produced more than this card shows)",
            }));
            break;
        }
        let role = message.get("role").and_then(Value::as_str).unwrap_or("");
        if role == "toolResult" || role == "tool" {
            let name = message
                .get("toolName")
                .and_then(Value::as_str)
                .unwrap_or("tool");
            let ok = !message
                .get("isError")
                .and_then(Value::as_bool)
                .unwrap_or(false);
            let output = clip(
                &content_text(message.get("content")),
                budget.min(STEP_TEXT_CHARS),
            );
            budget = budget.saturating_sub(output.chars().count());
            steps.push(serde_json::json!({
                "kind": "tool_result",
                "name": name,
                "ok": ok,
                "output": output,
            }));
            continue;
        }
        if role != "assistant" {
            continue;
        }
        let Some(parts) = message.get("content").and_then(Value::as_array) else {
            continue;
        };
        let mut pending = String::new();
        for part in parts {
            match part.get("type").and_then(Value::as_str).unwrap_or("") {
                "text" => {
                    if let Some(text) = part.get("text").and_then(Value::as_str) {
                        pending.push_str(text);
                    }
                }
                "toolCall" => {
                    if !pending.trim().is_empty() {
                        let text = clip(&pending, budget.min(STEP_TEXT_CHARS));
                        budget = budget.saturating_sub(text.chars().count());
                        steps.push(serde_json::json!({ "kind": "text", "text": text }));
                        pending.clear();
                    }
                    steps.push(serde_json::json!({
                        "kind": "tool",
                        "name": part.get("name").cloned().unwrap_or(Value::Null),
                        "args": part.get("arguments").cloned().unwrap_or(Value::Null),
                    }));
                }
                _ => {}
            }
        }
        if !pending.trim().is_empty() {
            let text = clip(&pending, budget.min(STEP_TEXT_CHARS));
            budget = budget.saturating_sub(text.chars().count());
            steps.push(serde_json::json!({ "kind": "text", "text": text }));
        }
    }
    Value::Array(steps)
}

fn content_text(content: Option<&Value>) -> String {
    let Some(parts) = content.and_then(Value::as_array) else {
        return String::new();
    };
    let mut text = String::new();
    for part in parts {
        if part.get("type").and_then(Value::as_str) == Some("text")
            && let Some(found) = part.get("text").and_then(Value::as_str)
        {
            text.push_str(found);
        }
    }
    text
}

fn last_assistant_text(messages: Option<&Value>) -> String {
    let Some(messages) = messages.and_then(Value::as_array) else {
        return String::new();
    };
    for message in messages.iter().rev() {
        if message.get("role").and_then(Value::as_str) != Some("assistant") {
            continue;
        }
        let text = content_text(message.get("content"));
        if !text.trim().is_empty() {
            return text;
        }
    }
    String::new()
}

fn clip(text: &str, limit: usize) -> String {
    if text.chars().count() <= limit {
        return text.to_string();
    }
    let mut out: String = text.chars().take(limit).collect();
    out.push_str(" …(truncated)");
    out
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

    /// A parallel delegation has to become N cards, not one: the extension
    /// takes `{tasks:[…]}` and runs every entry in its own process.
    #[test]
    fn a_parallel_delegation_announces_every_child() {
        let value = serde_json::json!({
            "type": "tool_execution_start",
            "toolCallId": "call_1",
            "toolName": "subagent",
            "args": {
                "tasks": [
                    { "agent": "scout", "task": "count files" },
                    { "agent": "reviewer", "task": "read the diff" }
                ]
            }
        });
        let events = translate(&value);
        assert_eq!(events.len(), 2);
        assert!(events.iter().all(|event| event.kind == EventKind::SubAgent));
        assert_eq!(events[0].payload["agentId"], "call_1#0");
        assert_eq!(events[0].payload["name"], "scout");
        assert_eq!(events[0].payload["task"], "count files");
        assert_eq!(events[0].payload["status"], "running");
        assert_eq!(events[0].payload["phase"], "started");
        assert_eq!(events[1].payload["agentId"], "call_1#1");
        assert_eq!(events[1].payload["name"], "reviewer");
    }

    /// A chain reports its position, so the card can say "step 2 of 3".
    #[test]
    fn a_chain_step_carries_its_position() {
        let value = serde_json::json!({
            "type": "tool_execution_start",
            "toolCallId": "call_2",
            "toolName": "subagent",
            "args": { "chain": [
                { "agent": "scout", "task": "look" },
                { "agent": "planner", "task": "plan" }
            ] }
        });
        let events = translate(&value);
        assert_eq!(events[0].payload["step"], 1);
        assert_eq!(events[1].payload["step"], 2);
    }

    /// The extension re-sends the whole transcript on every update, so the row
    /// is a snapshot: the card replaces its content instead of appending and
    /// every line does not appear three times.
    #[test]
    fn progress_arrives_as_one_snapshot_with_text_and_tools_in_order() {
        let value = serde_json::json!({
            "type": "tool_execution_update",
            "toolCallId": "call_1",
            "toolName": "subagent",
            "partialResult": {
                "details": {
                    "results": [{
                        "agent": "scout",
                        "task": "count files",
                        "exitCode": -1,
                        "messages": [
                            { "role": "user", "content": [{ "type": "text", "text": "count files" }] },
                            { "role": "assistant", "content": [
                                { "type": "text", "text": "counting" },
                                { "type": "toolCall", "id": "t1", "name": "bash",
                                  "arguments": { "command": "ls | wc -l" } }
                            ] },
                            { "role": "toolResult", "toolName": "bash", "isError": false,
                              "content": [{ "type": "text", "text": "42" }] },
                            { "role": "assistant", "content": [{ "type": "text", "text": "42 files" }] }
                        ],
                        "usage": { "turns": 2, "input": 10, "output": 5 }
                    }]
                }
            }
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        let payload = &events[0].payload;
        assert_eq!(payload["phase"], "snapshot");
        assert_eq!(payload["status"], "running");
        let steps = payload["steps"].as_array().expect("steps");
        let kinds: Vec<&str> = steps
            .iter()
            .map(|step| step["kind"].as_str().unwrap_or(""))
            .collect();
        assert_eq!(kinds, vec!["text", "tool", "tool_result", "text"]);
        assert_eq!(steps[0]["text"], "counting");
        assert_eq!(steps[1]["name"], "bash");
        assert_eq!(steps[2]["output"], "42");
        assert_eq!(steps[3]["text"], "42 files");
        // The parent's own prompt is not the child's work.
        assert!(steps.iter().all(|step| step["text"] != "count files"));
    }

    #[test]
    fn a_finished_child_carries_its_answer_and_usage() {
        let value = serde_json::json!({
            "type": "tool_execution_end",
            "toolCallId": "call_1",
            "toolName": "subagent",
            "result": {
                "content": [{ "type": "text", "text": "done" }],
                "details": { "results": [{
                    "agent": "scout",
                    "task": "count files",
                    "exitCode": 0,
                    "messages": [{ "role": "assistant", "content": [
                        { "type": "text", "text": "42 files under src" }
                    ] }],
                    "usage": { "turns": 1, "input": 3, "output": 7 }
                }] }
            }
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].payload["phase"], "completed");
        assert_eq!(events[0].payload["status"], "completed");
        assert_eq!(events[0].payload["result"], "42 files under src");
        assert_eq!(events[0].payload["usage"]["turns"], 1);
        assert!(events[0].payload.get("error").is_none());
    }

    /// A child that dies must not read as finished-with-an-empty-answer.
    #[test]
    fn a_child_that_exits_nonzero_reports_the_reason() {
        let value = serde_json::json!({
            "type": "tool_execution_end",
            "toolCallId": "call_1",
            "toolName": "subagent",
            "result": { "details": { "results": [{
                "agent": "worker",
                "exitCode": 2,
                "messages": [],
                "errorMessage": "model quota exhausted"
            }] } }
        });
        let events = translate(&value);
        assert_eq!(events[0].payload["phase"], "failed");
        assert_eq!(events[0].payload["status"], "failed");
        assert_eq!(events[0].payload["error"], "model quota exhausted");
    }

    /// Other tools keep the plain tool card: only the delegation tool is
    /// expanded into child agents.
    #[test]
    fn a_regular_tool_is_still_one_tool_call() {
        let value = serde_json::json!({
            "type": "tool_execution_start",
            "toolCallId": "call_9",
            "toolName": "bash",
            "args": { "command": "ls" }
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::ToolCall);
        assert_eq!(events[0].payload["tool"], "bash");
    }

    #[test]
    fn thinking_deltas_keep_their_text() {
        let value = serde_json::json!({
            "type": "message_update",
            "assistantMessageEvent": { "type": "thinking_delta", "delta": "先读文件" }
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::Thinking);
        assert_eq!(events[0].payload["delta"], "先读文件");
        // Boundaries carry no text and should not become ledger noise.
        assert!(
            translate(&serde_json::json!({
                "type": "message_update",
                "assistantMessageEvent": { "type": "thinking_end", "content": "done" }
            }))
            .is_empty()
        );
    }

    #[test]
    fn non_json_lines_are_ignored() {
        assert!(parse_line("Starting pi...").is_none());
        assert!(parse_line("{\"type\":\"agent_start\"}").is_some());
    }

    /// The persona a user saved has to become arguments pi actually reads. A
    /// unit test over `Command`'s argv is the narrowest proof that it is not
    /// lost between the catalog and the child process.
    fn argv(persona: OpenedSession, allow_tools: bool, tools: &[String]) -> Vec<String> {
        argv_with_images(persona, allow_tools, tools, &[])
    }

    fn argv_with_images(
        persona: OpenedSession,
        allow_tools: bool,
        tools: &[String],
        images: &[PathBuf],
    ) -> Vec<String> {
        let dir = std::env::temp_dir().join(format!("anvil-pi-argv-{}", std::process::id()));
        let adapter = PiAdapter::new(Some(PathBuf::from("D:/tools/pi/pi.exe")));
        let context = SessionContext {
            state_dir: dir.clone(),
            run_timeout: Some(Duration::from_secs(60)),
        };
        let command = adapter
            .command(&context, &persona, allow_tools, tools, "hello", images)
            .expect("build command");
        let args = command
            .get_args()
            .map(|value| value.to_string_lossy().into_owned())
            .collect();
        let _ = std::fs::remove_dir_all(&dir);
        args
    }

    #[test]
    fn persona_flags_reach_the_runtime() {
        let opened = OpenedSession {
            runtime_id: ID.to_string(),
            native_id: "s1".into(),
            model: "auto/fast".into(),
            cwd: "D:/work/api".into(),
            allow_tools: true,
            tools: vec!["read".into(), "grep".into()],
            agent_id: "migration-engineer".into(),
            thinking: "high".into(),
            system_prompt: "你是 {{agent}}，仓库 {{repo_path}}，模型 {{model}}，{{target_version}}"
                .into(),
            append_system_prompt: "历史结论摘要".into(),
            skills: vec!["D:/skills/review.md".into(), "D:/skills/docs".into()],
            project_trust: None,
        };
        let args = argv(opened, true, &["read".into(), "grep".into()]);
        let flag = |name: &str| -> Option<String> {
            args.iter()
                .position(|arg| arg == name)
                .and_then(|index| args.get(index + 1))
                .cloned()
        };
        assert_eq!(
            flag("--system-prompt").as_deref(),
            Some("你是 migration-engineer，仓库 D:/work/api，模型 auto/fast，{{target_version}}"),
            "known variables render, unknown ones stay verbatim"
        );
        assert_eq!(
            flag("--append-system-prompt").as_deref(),
            Some("历史结论摘要")
        );
        assert_eq!(flag("--thinking").as_deref(), Some("high"));
        assert_eq!(flag("--tools").as_deref(), Some("read,grep"));
        let skill_args: Vec<&str> = args
            .iter()
            .enumerate()
            .filter(|(_, arg)| arg.as_str() == "--skill")
            .map(|(index, _)| args[index + 1].as_str())
            .collect();
        assert_eq!(skill_args, vec!["D:/skills/review.md", "D:/skills/docs"]);
    }

    /// Measured on pi 1.5.0: resuming a session whose transcript recorded a
    /// loadout without `subagent` drops the extension tool when the run is
    /// started with an allowlist, but keeps it with an exclusion list. That is
    /// the difference between "the model delegates" and "the model says it has
    /// no subagent tool and spawns `pi -p` through bash".
    #[test]
    fn a_widened_tool_list_becomes_exclusions_so_a_resumed_session_keeps_extensions() {
        let tools: Vec<String> = [
            "read", "write", "edit", "bash", "grep", "find", "ls", "subagent",
        ]
        .iter()
        .map(|name| (*name).to_string())
        .collect();
        let args = argv(OpenedSession::default(), true, &tools);
        assert!(
            args.iter().any(|arg| arg == "--exclude-tools"),
            "a full default set plus an extension tool is an exclusion, not an allowlist: {args:?}"
        );
        assert!(!args.iter().any(|arg| arg == "--tools"), "{args:?}");
        let excluded = args
            .iter()
            .position(|arg| arg == "--exclude-tools")
            .and_then(|index| args.get(index + 1))
            .cloned();
        assert_eq!(excluded.as_deref(), Some("ask_question"));
    }

    #[test]
    fn a_narrowed_tool_list_keeps_the_allowlist() {
        let tools = vec!["read".to_string(), "grep".to_string()];
        let args = argv(OpenedSession::default(), true, &tools);
        let allowed = args
            .iter()
            .position(|arg| arg == "--tools")
            .and_then(|index| args.get(index + 1))
            .cloned();
        assert_eq!(allowed.as_deref(), Some("read,grep"));
        assert!(!args.iter().any(|arg| arg == "--exclude-tools"), "{args:?}");
    }

    #[test]
    fn exactly_the_builtin_list_stays_an_allowlist() {
        // An Agent saved before delegation existed must not silently gain the
        // extension tool: its list names only the built-ins on purpose.
        let tools: Vec<String> = BUILTIN_TOOLS
            .iter()
            .map(|name| (*name).to_string())
            .collect();
        let args = argv(OpenedSession::default(), true, &tools);
        assert!(args.iter().any(|arg| arg == "--tools"), "{args:?}");
        assert!(!args.iter().any(|arg| arg == "--exclude-tools"), "{args:?}");
    }

    #[test]
    fn an_explicit_ask_question_is_not_silently_excluded() {
        let tools: Vec<String> = [
            "read", "write", "edit", "bash", "grep", "find", "ls", "ask_question",
        ]
        .iter()
        .map(|name| (*name).to_string())
        .collect();
        let args = argv(OpenedSession::default(), true, &tools);
        let allowed = args
            .iter()
            .position(|arg| arg == "--tools")
            .and_then(|index| args.get(index + 1))
            .cloned();
        assert_eq!(
            allowed.as_deref(),
            Some("read,write,edit,bash,grep,find,ls,ask_question"),
            "an explicit choice is respected rather than overridden by our default"
        );
    }

    /// The permission picker reaches the CLI as flags, not as a label.
    #[test]
    fn the_permission_plan_becomes_a_tool_allowlist_and_a_trust_flag() {
        // read-only: pi's own documented read-only mode.
        let read_only = argv(
            OpenedSession {
                project_trust: Some(false),
                ..OpenedSession::default()
            },
            true,
            &crate::model::READ_ONLY_TOOLS
                .iter()
                .map(|name| (*name).to_string())
                .collect::<Vec<_>>(),
        );
        let allowed = read_only
            .iter()
            .position(|arg| arg == "--tools")
            .and_then(|index| read_only.get(index + 1))
            .cloned();
        assert_eq!(allowed.as_deref(), Some("read,grep,find,ls"), "{read_only:?}");
        assert!(read_only.iter().any(|arg| arg == "--no-approve"), "{read_only:?}");

        let trusted = argv(
            OpenedSession {
                project_trust: Some(true),
                ..OpenedSession::default()
            },
            true,
            &[],
        );
        assert!(trusted.iter().any(|arg| arg == "--approve"), "{trusted:?}");
        assert!(!trusted.iter().any(|arg| arg == "--no-approve"), "{trusted:?}");

        // No decision means no flag: the runtime keeps its own default.
        let untouched = argv(OpenedSession::default(), true, &[]);
        assert!(
            !untouched.iter().any(|arg| arg == "--approve" || arg == "--no-approve"),
            "{untouched:?}"
        );
    }

    #[test]
    fn an_agent_without_persona_does_not_change_the_runtime_defaults() {
        let args = argv(OpenedSession::default(), false, &[]);
        for absent in [
            "--system-prompt",
            "--append-system-prompt",
            "--thinking",
            "--skill",
            "--tools",
        ] {
            assert!(!args.iter().any(|arg| arg == absent), "{absent} leaked in");
        }
        assert!(args.iter().any(|arg| arg == "--no-tools"));
    }

    /// A picture only reaches the model through pi's own `@path` channel: pi
    /// turns it into an image content block, which pasting UTF-8 text never can.
    #[test]
    fn images_travel_as_at_path_arguments_after_the_option_separator() {
        let images = vec![
            PathBuf::from(r"D:\Downloads\陈思成.png"),
            PathBuf::from(r"D:\Downloads\second shot.jpg"),
        ];
        let args = argv_with_images(OpenedSession::default(), true, &[], &images);
        let separator = args
            .iter()
            .position(|arg| arg == "--")
            .expect("attachments need `--` so a path is never read as a flag");
        assert_eq!(
            args[separator + 1..].to_vec(),
            vec![
                r"@D:\Downloads\陈思成.png".to_string(),
                r"@D:\Downloads\second shot.jpg".to_string(),
            ],
            "every image becomes one `@path` positional, in the order it was added"
        );
        // `--` must be the last option: only files follow it.
        assert_eq!(separator, args.len() - 3);
    }

    #[test]
    fn a_text_only_turn_gets_no_attachment_arguments() {
        let args = argv(OpenedSession::default(), true, &[]);
        assert!(!args.iter().any(|arg| arg == "--"), "{args:?}");
        assert!(!args.iter().any(|arg| arg.starts_with('@')), "{args:?}");
    }
}
