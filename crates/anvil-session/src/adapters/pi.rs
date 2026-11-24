//! Pi adapter (`pi --mode rpc`).
//!
//! Facts this implementation depends on, all measured on this machine:
//!
//! * `--mode rpc` is long-lived: it reads one JSON command per line on stdin
//!   and writes responses plus session events on stdout. This is the only pi
//!   interface that can take a message *while* a turn is streaming, which is
//!   what the composer's "send now" needs;
//! * a prompt is a command (`{"type":"prompt","message":…}`), not an argument.
//!   `@path` expansion still happens inside the message, so attachments keep
//!   working and no image bytes are decoded here;
//! * `{"type":"steer","message":…}` queues text into the running turn; pi
//!   answers `queue_update` and then folds the text into its next step;
//! * the run is over at `agent_settled`, not at `agent_end`: retries, overflow
//!   recovery and steering can each start another low-level run after
//!   `agent_end`. Waiting for process exit while keeping stdin open would hang;
//! * closing stdin is the orderly shutdown. Give it a moment to exit, then kill
//!   whatever is left.

use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{ChildStdin, Command, Stdio};
use std::sync::{Mutex, mpsc};
use std::time::{Duration, Instant};

use serde_json::Value;

use crate::adapter::{
    AdapterEvent, Cancel, CompactOutcome, ContextUsage, RunOutcome, SessionAdapter, Turn,
};
use crate::error::{Error, Result};
use crate::model::{
    EventKind, OpenedSession, RunStatus, SessionCapabilities, SessionContext, SessionSpec,
    UiAnswer, UiRequest,
};
use crate::subagent::{SubAgentReport, SubAgentStatus};

/// Adapter id, matching the catalog.
pub const ID: &str = "pi";

/// One line of input for the run loop: runtime output, or a steer this build
/// injected and must therefore record itself.
enum Feed {
    Line(String),
    Steer(String),
}

/// A turn that is still streaming: the pipe into pi and the loop's feed.
struct LiveRun {
    stdin: ChildStdin,
    feed: mpsc::Sender<Feed>,
}

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
        vec!["--exclude-tools".to_string(), HEADLESS_EXCLUDE.to_string()]
    } else {
        vec!["--tools".to_string(), tools.join(",")]
    }
}

/// Pi adapter.
pub struct PiAdapter {
    executable: Option<PathBuf>,
    /// The pipe into each session's live RPC process, so a steer can reach the
    /// turn that is streaming right now. Keyed by the runtime's session id.
    live: Mutex<HashMap<String, LiveRun>>,
    /// Questions this runtime is waiting on, answered from the page.
    uis: crate::ui::ExtensionUis,
}

impl PiAdapter {
    #[must_use]
    pub fn new(executable: Option<PathBuf>) -> Self {
        Self {
            executable,
            live: Mutex::new(HashMap::new()),
            uis: crate::ui::extension_uis(),
        }
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
            .arg("--mode")
            .arg("rpc")
            .arg("--session-id")
            .arg(&opened.native_id)
            .arg("--session-dir")
            .arg(&session_dir)
            // An empty model means "whatever pi is configured with": passing
            // `--model ""` would fail the run before any work starts.
            .current_dir(&opened.cwd)
            // RPC mode reads commands from stdin until it is closed. Unlike
            // print mode, an open pipe here is the protocol, not a hang.
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if !opened.model.trim().is_empty() {
            command.arg("--model").arg(&opened.model);
        }
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
        Ok(command)
    }
}

/// The `prompt` command, with the turn's attachments.
///
/// pi expands `@path` inside the message exactly as it did on the command line
/// (`@file` *arguments* are rejected in RPC mode, which is why they travel
/// here). A path with spaces is quoted; without the quotes pi splits it and
/// reads a file that does not exist.
fn prompt_command(id: &str, prompt: &str, images: &[PathBuf]) -> Value {
    let mut message = prompt.to_string();
    for image in images {
        let path = image.display().to_string();
        if path.contains(' ') {
            message.push_str(&format!(" @\"{path}\""));
        } else {
            message.push_str(&format!(" @{path}"));
        }
    }
    serde_json::json!({ "id": id, "type": "prompt", "message": message })
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
            permission: String::new(),
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
            .command(context, opened, opened.allow_tools, &opened.tools)?
            .spawn()
            .map_err(|error| Error::Failed(format!("spawn pi: {error}")))?;

        let mut stdin = child
            .stdin
            .take()
            .ok_or_else(|| Error::Io("pi stdin was not piped".into()))?;
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| Error::Io("pi stdout was not piped".into()))?;
        // stderr is diagnostics, never protocol data. Draining it keeps a
        // chatty runtime from filling the pipe and blocking mid-turn; the tail
        // is the only place an early exit explains itself ("not signed in").
        let diagnostics = std::sync::Arc::new(Mutex::new(String::new()));
        if let Some(stderr) = child.stderr.take() {
            let diagnostics = diagnostics.clone();
            std::thread::spawn(move || {
                for line in BufReader::new(stderr)
                    .lines()
                    .map_while(std::result::Result::ok)
                {
                    if let Ok(mut text) = diagnostics.lock() {
                        text.push_str(&line);
                        text.push('\n');
                        // Only the tail is worth keeping; a long debug stream
                        // must not grow without bound.
                        if text.len() > 8_192 {
                            let cut = text.len() - 8_192;
                            let keep = text.split_off(cut);
                            *text = keep;
                        }
                    }
                }
            });
        }

        let (sender, receiver) = mpsc::channel::<Feed>();
        let lines = sender.clone();
        std::thread::spawn(move || {
            for line in BufReader::new(stdout)
                .lines()
                .map_while(std::result::Result::ok)
            {
                if lines.send(Feed::Line(line)).is_err() {
                    break;
                }
            }
        });

        // The prompt is a command now. It goes out before the first read so a
        // fast completion cannot land before the loop is listening.
        let prompt = prompt_command("turn-1", turn.prompt, turn.images);
        if let Err(error) = write_command(&mut stdin, &prompt) {
            super::kill_tree(&mut child);
            return Err(Error::Failed(format!("pi refused the prompt: {error}")));
        }
        // Register the pipe *after* the prompt: a steer that arrives while the
        // process is starting finds the run as soon as it is listening.
        self.register_live(&opened.native_id, stdin, sender);

        // No deadline by default. A turn ends when pi finishes, fails, or the
        // user stops it; `run_timeout` is only set by tests and the report
        // example, which need a broken adapter to fail instead of hang. Cutting
        // real turns at a round number threw away replies that were still being
        // written — a run that reads files and reasons between them can take
        // far longer than any fixed budget, and that is work, not a hang.
        let deadline = context.run_timeout.map(|budget| Instant::now() + budget);
        let mut deltas = String::new();
        // One entry per `turn_end`. A steered run produces several: the work
        // before the steer, then the work after it. Every message counts as the
        // run's answer, so they are joined rather than overwritten.
        let mut turns_text: Vec<String> = Vec::new();
        // A steered run speaks in several turns: the answer before the steer and
        // the one after it. Only then is the run's text their sum; an ordinary
        // multi-step run ends with the one message that is its answer.
        let mut steered = false;
        let mut usage: Option<Value> = None;
        let mut failure: Option<String> = None;
        // Pi retries a rejected provider call itself: the child stays alive, no
        // token arrives, and the only record of the reason is `auto_retry_start`.
        // Keep that message for the failure text when the run ends.
        let mut retry_error: Option<String> = None;
        let mut settled = false;

        while !settled {
            if cancel.is_cancelled() {
                self.forget_live(&opened.native_id);
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
                self.forget_live(&opened.native_id);
                super::kill_tree(&mut child);
                return Err(Error::Timeout(format!(
                    "pi did not finish within {:?}",
                    context.run_timeout.unwrap_or_default()
                )));
            }
            match receiver.recv_timeout(Duration::from_millis(100)) {
                Ok(Feed::Steer(text)) => {
                    steered = true;
                    // pi keeps the text in its own queue; recording it here is
                    // what puts the row in this run's ledger at the moment it
                    // was injected instead of after the answer.
                    sink(AdapterEvent::new(
                        EventKind::UserSteer,
                        serde_json::json!({ "text": text }),
                    ));
                }
                Ok(Feed::Line(line)) => {
                    let Some(value) = parse_line(&line) else {
                        continue;
                    };
                    if value.get("type").and_then(Value::as_str) == Some("agent_settled") {
                        settled = true;
                    }
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
                    // An extension asked the user something. pi blocks until the
                    // answer is written back, so the request has to reach the
                    // page and the answer has to come back the same way.
                    if value.get("type").and_then(Value::as_str) == Some("extension_ui_request") {
                        let request = ui_request(&value);
                        let request_id = request.id.clone();
                        let expects_answer = request.expects_answer();
                        let timeout = request
                            .timeout_ms
                            .map(Duration::from_millis)
                            .map(|budget| budget + Duration::from_secs(2));
                        sink(AdapterEvent::new(
                            EventKind::ExtensionUi,
                            serde_json::to_value(&request).unwrap_or(serde_json::Value::Null),
                        ));
                        if expects_answer {
                            // "Full control" means the user already said they
                            // trust this workspace, so a question the runtime
                            // asks on its own is answered for them: the turn
                            // must not stop on a card the user did not expect.
                            let automatic = automatic_answer(&request, &opened.permission);
                            let (answer, auto) = match automatic {
                                Some(answer) => (answer, true),
                                None => (self.uis.ask(request, timeout, cancel), false),
                            };
                            let _ = self.answer_ui(&opened.native_id, &request_id, &answer);
                            sink(AdapterEvent::new(
                                EventKind::ExtensionUiResolved,
                                serde_json::json!({
                                    "id": request_id,
                                    "cancelled": answer.cancelled,
                                    "confirmed": answer.confirmed,
                                    "value": answer.value,
                                    "auto": auto,
                                }),
                            ));
                        }
                        continue;
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
                            && !found.trim().is_empty()
                        {
                            turns_text.push(found.to_string());
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

        // Orderly shutdown: closing the pipe and giving pi a moment to flush its
        // session file. `forget_live` drops the only handle to stdin, which is
        // the EOF pi treats as "shut down"; a runtime that ignores it is killed
        // rather than blocking the turn forever.
        self.forget_live(&opened.native_id);
        let status = wait_briefly(&mut child);
        let text = if turns_text.is_empty() {
            deltas.clone()
        } else if steered {
            turns_text.join("\n\n")
        } else {
            turns_text.last().cloned().unwrap_or_else(|| deltas.clone())
        };
        let stderr_tail = diagnostics
            .lock()
            .map(|text| text.trim().to_string())
            .unwrap_or_default();

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
                error: Some(retry_error.clone().unwrap_or_else(|| {
                    if stderr_tail.is_empty() {
                        format!("pi exited with {status}")
                    } else {
                        last_line(&stderr_tail)
                    }
                })),
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

    /// Compact the conversation stored inside pi, not just our ledger.
    ///
    /// `{"type":"compact"}` makes pi summarize older entries and persist a real
    /// compaction entry in its session file, so the next turn genuinely sees a
    /// smaller context. The short-lived child mirrors `context_usage`: open the
    /// same session, send one command, read the response, exit.
    fn compact(
        &self,
        context: &SessionContext,
        opened: &OpenedSession,
        instructions: Option<&str>,
        _sink: &mut dyn FnMut(AdapterEvent),
    ) -> Result<Option<CompactOutcome>> {
        if opened.native_id.trim().is_empty() {
            return Ok(None);
        }
        let mut child = self
            .command(context, opened, opened.allow_tools, &opened.tools)?
            .spawn()
            .map_err(|error| Error::Failed(format!("spawn pi for compact: {error}")))?;
        let mut stdin = child
            .stdin
            .take()
            .ok_or_else(|| Error::Io("pi compact stdin was not piped".into()))?;
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| Error::Io("pi compact stdout was not piped".into()))?;
        // pi says on stderr when it cannot find the session it was pointed at
        // and opens a fresh one instead. A compaction of that empty session is
        // refused as "too small", which would read as a no-op even though the
        // real transcript was never seen — so keep the warning.
        let stderr_lines = std::sync::Arc::new(std::sync::Mutex::new(Vec::<String>::new()));
        if let Some(stderr) = child.stderr.take() {
            let sink = stderr_lines.clone();
            // Drain it so a chatty summarizer cannot block on a full pipe.
            std::thread::spawn(move || {
                for line in BufReader::new(stderr)
                    .lines()
                    .map_while(std::result::Result::ok)
                {
                    if let Ok(mut kept) = sink.lock() {
                        if kept.len() < 40 {
                            kept.push(line);
                        }
                    }
                }
            });
        }
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
        let mut request = serde_json::json!({ "id": "compact-1", "type": "compact" });
        if let Some(instructions) = instructions.filter(|value| !value.trim().is_empty()) {
            request["customInstructions"] = Value::String(instructions.trim().to_string());
        }
        write_command(&mut stdin, &request)
            .map_err(|error| Error::Failed(format!("ask pi to compact: {error}")))?;
        drop(stdin);

        // A summary is an LLM call; a cold provider can take minutes. The page
        // shows its own "compacting" card, so waiting here is visible.
        let deadline = Instant::now() + Duration::from_secs(600);
        let mut outcome = CompactOutcome::default();
        let mut failure: Option<String> = None;
        let mut answered = false;
        while Instant::now() < deadline {
            match receiver.recv_timeout(Duration::from_millis(100)) {
                Ok(line) => {
                    let Some(value) = parse_line(&line) else {
                        continue;
                    };
                    match value.get("type").and_then(Value::as_str) {
                        Some("compaction_end") => {
                            if value.get("aborted").and_then(Value::as_bool) == Some(true) {
                                failure = Some("pi aborted the context compaction".into());
                            } else if let Some(message) =
                                value.get("errorMessage").and_then(Value::as_str)
                            {
                                // "Nothing to compact" is pi's normal answer for a
                                // session under its retention budget, not a fault.
                                if let Some(reason) = nothing_to_compact(message) {
                                    outcome.skipped = Some(reason);
                                } else {
                                    failure = Some(message.to_string());
                                }
                            } else if let Some(result) = value.get("result") {
                                outcome = compact_outcome(result);
                            }
                        }
                        Some("response")
                            if value.get("command").and_then(Value::as_str) == Some("compact") =>
                        {
                            if value.get("success").and_then(Value::as_bool) == Some(true) {
                                if let Some(data) = value.get("data") {
                                    let from_response = compact_outcome(data);
                                    if !from_response.summary.is_empty() {
                                        outcome.summary = from_response.summary;
                                    }
                                    if from_response.tokens_before.is_some() {
                                        outcome.tokens_before = from_response.tokens_before;
                                    }
                                    if from_response.estimated_tokens_after.is_some() {
                                        outcome.estimated_tokens_after =
                                            from_response.estimated_tokens_after;
                                    }
                                    if from_response.first_kept_entry_id.is_some() {
                                        outcome.first_kept_entry_id =
                                            from_response.first_kept_entry_id;
                                    }
                                    if from_response.usage.is_some() {
                                        outcome.usage = from_response.usage;
                                    }
                                }
                            } else {
                                let message = value
                                    .pointer("/error/message")
                                    .and_then(Value::as_str)
                                    .unwrap_or("pi refused the context compaction");
                                match nothing_to_compact(message) {
                                    Some(reason) => outcome.skipped = Some(reason),
                                    None => failure = Some(message.to_string()),
                                }
                            }
                            answered = true;
                            break;
                        }
                        _ => {}
                    }
                }
                Err(mpsc::RecvTimeoutError::Timeout) => continue,
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
            }
        }
        let _ = wait_briefly(&mut child);
        if let Some(failure) = failure {
            return Err(Error::Failed(failure));
        }
        if !answered {
            return Err(Error::Timeout(
                "pi did not answer the compact command".into(),
            ));
        }
        if outcome.skipped.is_some() {
            // The refusal only means "nothing to compact" if pi actually opened
            // the conversation. When it could not find the file it silently
            // starts an empty one, and saying "nothing to compact" would hide a
            // session that was never read.
            let missed = stderr_lines
                .lock()
                .map(|lines| {
                    lines
                        .iter()
                        .any(|line| line.contains("No project session found"))
                })
                .unwrap_or(false);
            if missed {
                return Err(Error::Failed(
                    "pi could not open this session's own transcript, so nothing was compacted"
                        .into(),
                ));
            }
            return Ok(Some(outcome));
        }
        if outcome.summary.trim().is_empty() {
            return Err(Error::Failed("pi returned no compaction summary".into()));
        }
        Ok(Some(outcome))
    }

    /// Ask pi for the session's own context estimate.
    ///
    /// `get_session_stats` is a read-only RPC command: a short-lived child opens
    /// the same session file, answers once and exits on stdin EOF. That keeps
    /// the number real (the runtime's own compaction estimate) without keeping
    /// another long-running process around just to draw a ring.
    fn context_usage(
        &self,
        context: &SessionContext,
        opened: &OpenedSession,
    ) -> Result<Option<ContextUsage>> {
        if opened.native_id.trim().is_empty() {
            return Ok(None);
        }
        let mut child = self
            .command(context, opened, opened.allow_tools, &opened.tools)?
            .spawn()
            .map_err(|error| Error::Failed(format!("spawn pi for stats: {error}")))?;
        let mut stdin = child
            .stdin
            .take()
            .ok_or_else(|| Error::Io("pi stats stdin was not piped".into()))?;
        let stdout = child
            .stdout
            .take()
            .ok_or_else(|| Error::Io("pi stats stdout was not piped".into()))?;
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
        write_command(
            &mut stdin,
            &serde_json::json!({ "id": "ctx-1", "type": "get_session_stats" }),
        )
        .map_err(|error| Error::Failed(format!("ask pi for stats: {error}")))?;
        drop(stdin);

        let deadline = Instant::now() + Duration::from_secs(12);
        let mut found: Option<ContextUsage> = None;
        while Instant::now() < deadline {
            match receiver.recv_timeout(Duration::from_millis(100)) {
                Ok(line) => {
                    let Some(value) = parse_line(&line) else {
                        continue;
                    };
                    if value.get("type").and_then(Value::as_str) != Some("response")
                        || value.get("command").and_then(Value::as_str) != Some("get_session_stats")
                    {
                        continue;
                    }
                    if value.get("success").and_then(Value::as_bool) != Some(true) {
                        break;
                    }
                    let Some(usage) = value.pointer("/data/contextUsage") else {
                        break;
                    };
                    let window = usage
                        .get("contextWindow")
                        .and_then(Value::as_u64)
                        .unwrap_or(0);
                    if window == 0 {
                        break;
                    }
                    found = Some(ContextUsage {
                        tokens: usage.get("tokens").and_then(Value::as_u64),
                        context_window: window,
                        percent: usage.get("percent").and_then(Value::as_f64).or_else(|| {
                            usage
                                .get("tokens")
                                .and_then(Value::as_u64)
                                .map(|tokens| tokens as f64 * 100.0 / window as f64)
                        }),
                    });
                    break;
                }
                Err(mpsc::RecvTimeoutError::Timeout) => continue,
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
            }
        }
        if found.is_none() {
            super::kill_tree(&mut child);
        }
        let _ = wait_briefly(&mut child);
        Ok(found)
    }

    fn extension_uis(&self) -> Option<crate::ui::ExtensionUis> {
        Some(self.uis.clone())
    }

    /// Answer the question pi is blocked on.
    ///
    /// pi's extension UI is a request/response sub-protocol: the runtime waits
    /// for an `extension_ui_response` on stdin before it can go on. The answer
    /// is written to the same live pipe a steer uses.
    fn answer_ui(&self, native_id: &str, request_id: &str, answer: &UiAnswer) -> Result<()> {
        let mut payload = serde_json::json!({
            "type": "extension_ui_response",
            "id": request_id,
        });
        if answer.cancelled {
            payload["cancelled"] = Value::Bool(true);
        } else if let Some(confirmed) = answer.confirmed {
            payload["confirmed"] = Value::Bool(confirmed);
        } else if let Some(value) = &answer.value {
            payload["value"] = Value::String(value.clone());
        }
        let mut live = match self.live.lock() {
            Ok(live) => live,
            Err(poisoned) => poisoned.into_inner(),
        };
        let Some(run) = live.get_mut(native_id) else {
            return Err(Error::Failed(format!(
                "no live pi session for {native_id} to answer"
            )));
        };
        write_command(&mut run.stdin, &payload)
            .map_err(|error| Error::Failed(format!("answer pi: {error}")))
    }

    fn steer(&self, opened: &OpenedSession, text: &str) -> Result<bool> {
        let text = text.trim();
        if text.is_empty() {
            return Ok(false);
        }
        let mut live = match self.live.lock() {
            Ok(live) => live,
            // A poisoned map still holds the pipes that are open. Refusing to
            // steer because some other thread panicked helps nobody.
            Err(poisoned) => poisoned.into_inner(),
        };
        let Some(run) = live.get_mut(&opened.native_id) else {
            return Ok(false);
        };
        let command = serde_json::json!({
            "id": format!("steer-{}", crate::clock::unix_millis()),
            "type": "steer",
            "message": text,
        });
        write_command(&mut run.stdin, &command)
            .map_err(|error| Error::Failed(format!("steer pi: {error}")))?;
        // The runtime's own stream will show this text on its next step; the
        // feed carries it now so the ledger can place it in time.
        let _ = run.feed.send(Feed::Steer(text.to_string()));
        Ok(true)
    }
}

impl PiAdapter {
    fn register_live(&self, native_id: &str, stdin: ChildStdin, feed: mpsc::Sender<Feed>) {
        let mut live = match self.live.lock() {
            Ok(live) => live,
            Err(poisoned) => poisoned.into_inner(),
        };
        live.insert(native_id.to_string(), LiveRun { stdin, feed });
    }

    fn forget_live(&self, native_id: &str) {
        let mut live = match self.live.lock() {
            Ok(live) => live,
            Err(poisoned) => poisoned.into_inner(),
        };
        live.remove(native_id);
    }
}

/// One JSON command, one line, flushed.
fn write_command(stdin: &mut ChildStdin, command: &Value) -> std::io::Result<()> {
    stdin.write_all(command.to_string().as_bytes())?;
    stdin.write_all(b"\n")?;
    stdin.flush()
}

/// Wait for an orderly exit, then insist.
///
/// pi disposes its runtime and writes the session file after stdin closes; that
/// is quick but not instant. A process that is still alive after five seconds
/// is not shutting down, and blocking the turn on it would look like a hang.
fn wait_briefly(child: &mut std::process::Child) -> std::process::ExitStatus {
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return status,
            Ok(None) if Instant::now() < deadline => {
                std::thread::sleep(Duration::from_millis(25));
            }
            Ok(None) => {
                super::kill_tree(child);
                return child.wait().unwrap_or_else(|_| synthetic_exit());
            }
            Err(_) => return synthetic_exit(),
        }
    }
}

/// An exit status for the case where the process API itself failed.
fn synthetic_exit() -> std::process::ExitStatus {
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        std::process::ExitStatus::from_raw(1)
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::ExitStatusExt;
        std::process::ExitStatus::from_raw(1)
    }
}

/// The last non-empty line of a diagnostics tail: that is the message that
/// explains the exit, not the stack above it.
fn last_line(text: &str) -> String {
    text.lines()
        .rev()
        .find(|line| !line.trim().is_empty())
        .map(|line| line.trim().to_string())
        .unwrap_or_else(|| "pi exited without a message".to_string())
}

fn parse_line(line: &str) -> Option<Value> {
    let trimmed = line.trim();
    if !trimmed.starts_with('{') {
        return None;
    }
    serde_json::from_str(trimmed).ok()
}

/// Read the fields pi uses for a compaction result (`data` or `result`).
/// pi's "nothing to do" compaction answers.
///
/// A manual compaction is refused when the session is still under pi's
/// `keepRecentTokens` budget ("Nothing to compact (session too small)") and when
/// a compaction is already the newest entry ("Already compacted"). Neither is a
/// failure the reader should be asked to fix; the caller reports them as a
/// no-op. The returned code is what the page localises.
fn nothing_to_compact(message: &str) -> Option<String> {
    let lower = message.to_ascii_lowercase();
    if lower.contains("nothing to compact") {
        return Some("too_small".to_string());
    }
    if lower.contains("already compacted") {
        return Some("already_compacted".to_string());
    }
    None
}

/// Translate pi's compaction result into the runtime-neutral outcome.
fn compact_outcome(value: &Value) -> CompactOutcome {
    CompactOutcome {
        summary: value
            .get("summary")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string(),
        first_kept_entry_id: value
            .get("firstKeptEntryId")
            .and_then(Value::as_str)
            .map(str::to_string),
        tokens_before: value.get("tokensBefore").and_then(Value::as_u64),
        estimated_tokens_after: value.get("estimatedTokensAfter").and_then(Value::as_u64),
        usage: value.get("usage").cloned(),
        skipped: None,
    }
}

/// The answer a fully trusted workspace gives for itself.
///
/// "Full control" means the user already said they trust this folder, so a
/// question the runtime asks on its own must not stop the turn on a card the
/// user did not expect: a `confirm` is yes, a `select` takes its first
/// choice, and a free-text question takes what it was given.
fn automatic_answer(request: &UiRequest, permission: &str) -> Option<UiAnswer> {
    if !permission.trim().eq_ignore_ascii_case("full") {
        return None;
    }
    match request.method.as_str() {
        "confirm" => Some(UiAnswer {
            confirmed: Some(true),
            ..UiAnswer::default()
        }),
        "select" => request.options.first().map(|choice| UiAnswer {
            value: Some(choice.clone()),
            ..UiAnswer::default()
        }),
        "input" | "editor" => Some(UiAnswer {
            value: Some(request.prefill.clone()),
            ..UiAnswer::default()
        }),
        _ => None,
    }
}

/// Read an extension's question out of pi's `extension_ui_request`.
///
/// The runtime names the method and carries the pieces that method needs:
/// `select` brings options, `confirm` brings a message, `input` brings a
/// placeholder, `editor` brings a prefill.
///
/// The fire-and-forget methods each name their words differently — a status is
/// `statusText`, a widget is `widgetLines`, editor text is `text` — and they all
/// arrive with `message` unset. The page prints one line of words, so they are
/// folded into `message` here; reading only `message` left every one of them
/// with nothing to say, which the transcript then rendered as a bare
/// `扩展面板:` line. A widget with no `widgetLines` is an extension *clearing*
/// its panel: that carries no words at all, and the page drops it.
fn ui_request(value: &Value) -> UiRequest {
    let text = |key: &str| {
        value
            .get(key)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    let options = value
        .get("options")
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .filter_map(|item| item.as_str().map(str::to_string))
                .collect::<Vec<String>>()
        })
        .unwrap_or_default();
    let method = text("method");
    let message = match method.as_str() {
        "setStatus" => text("statusText"),
        "setWidget" => value
            .get("widgetLines")
            .and_then(Value::as_array)
            .map(|lines| {
                lines
                    .iter()
                    .filter_map(Value::as_str)
                    .collect::<Vec<&str>>()
                    .join("\n")
            })
            .unwrap_or_default(),
        "set_editor_text" => text("text"),
        _ => text("message"),
    };
    UiRequest {
        id: text("id"),
        session_id: String::new(),
        run_id: String::new(),
        method,
        title: text("title"),
        message,
        options,
        placeholder: text("placeholder"),
        prefill: text("prefill"),
        notify_type: text("notifyType"),
        timeout_ms: value.get("timeout").and_then(Value::as_u64),
        created_at: crate::clock::now_rfc3339(),
    }
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
                // Keep the tool's output and pictures on the *result* event:
                // that is where the turn produced them, so the page can draw
                // an image between the two messages that surround the call.
                let (text, images) =
                    content_parts(value.get("result").and_then(|r| r.get("content")));
                let mut payload = serde_json::json!({
                    "tool": value.get("toolName").cloned().unwrap_or(Value::Null),
                    "ok": value.get("isError").map(|flag| !flag.as_bool().unwrap_or(false)),
                });
                if !text.is_empty() {
                    payload["output"] = Value::String(text);
                }
                if !images.is_empty() {
                    payload["images"] = Value::Array(images);
                }
                events.push(AdapterEvent::new(EventKind::ToolResult, payload));
            }
        }
        "agent_start" | "turn_start" | "message_start" | "message_end" | "agent_end"
        | "agent_settled" => {}
        // The receipt of an RPC command. It is protocol bookkeeping, not
        // something the reader said or did, so it stays out of the transcript.
        // A failed command is the exception: its reason is the only place the
        // problem shows up.
        "response" => {
            if value.get("success").and_then(Value::as_bool) == Some(false) {
                events.push(AdapterEvent::new(
                    EventKind::Error,
                    serde_json::json!({
                        "message": format!(
                            "pi refused the {} command: {}",
                            value.get("command").and_then(Value::as_str).unwrap_or("unknown"),
                            value.get("error").and_then(Value::as_str).unwrap_or("no reason given"),
                        ),
                    }),
                ));
            }
        }
        // An extension asked the user something. The run is blocked on the
        // answer, so the question is a first-class event, not an odd shape.
        "extension_ui_request" => events.push(AdapterEvent::new(
            EventKind::ExtensionUi,
            serde_json::to_value(&ui_request(value)).unwrap_or(Value::Null),
        )),
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
            let (raw, images) = content_parts(message.get("content"));
            let output = clip(&raw, budget.min(STEP_TEXT_CHARS));
            budget = budget.saturating_sub(output.chars().count());
            steps.push(serde_json::json!({
                "kind": "tool_result",
                "name": name,
                "ok": ok,
                "output": output,
            }));
            // A child's picture belongs in its own transcript at the moment
            // the tool produced it, just like the parent's timeline.
            for image in images {
                steps.push(serde_json::json!({ "kind": "image", "image": image }));
            }
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
                // Reasoning is part of what the child did, not an internal
                // detail of the parent's: the panel shows it above the answer
                // the same way the main transcript does. pi writes it as
                // `{type:"thinking", thinking:"…"}`, and there is no `text`
                // field to fall back on.
                "thinking" | "reasoning" => {
                    let thinking = part
                        .get("thinking")
                        .or_else(|| part.get("reasoning"))
                        .or_else(|| part.get("text"))
                        .and_then(Value::as_str)
                        .unwrap_or("");
                    if thinking.trim().is_empty() {
                        continue;
                    }
                    if !pending.trim().is_empty() {
                        let text = clip(&pending, budget.min(STEP_TEXT_CHARS));
                        budget = budget.saturating_sub(text.chars().count());
                        steps.push(serde_json::json!({ "kind": "text", "text": text }));
                        pending.clear();
                    }
                    let text = clip(thinking, budget.min(STEP_TEXT_CHARS));
                    budget = budget.saturating_sub(text.chars().count());
                    steps.push(serde_json::json!({
                        "kind": "text",
                        "text": text,
                        "channel": "thinking",
                    }));
                }
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

/// Split a pi message's content into text and pictures.
///
/// An image from a tool result reaches us base64-encoded in the message itself
/// (there is no file to link to). It stays on the event that carried it so the
/// reader sees it where the tool ran, not as a detached attachment. A very
/// large payload is stated instead of silently pushing megabytes into the
/// ledger and every CDP frame.
const MAX_IMAGE_BYTES: usize = 2 * 1024 * 1024;

fn content_parts(content: Option<&Value>) -> (String, Vec<Value>) {
    let Some(parts) = content.and_then(Value::as_array) else {
        return (String::new(), Vec::new());
    };
    let mut text = String::new();
    let mut images: Vec<Value> = Vec::new();
    for part in parts {
        match part.get("type").and_then(Value::as_str).unwrap_or("") {
            "text" => {
                if let Some(found) = part.get("text").and_then(Value::as_str) {
                    text.push_str(found);
                }
            }
            "image" => {
                let data = part.get("data").and_then(Value::as_str).unwrap_or("");
                if data.is_empty() {
                    continue;
                }
                if data.len() > MAX_IMAGE_BYTES {
                    if !text.is_empty() {
                        text.push('\n');
                    }
                    text.push_str("(图片过大，未写入对话记录)");
                    continue;
                }
                images.push(serde_json::json!({
                    "data": data,
                    "mimeType": part.get("mimeType").and_then(Value::as_str).unwrap_or("image/png"),
                }));
            }
            _ => {}
        }
    }
    (text, images)
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

    /// A question the runtime asked has to reach the page: the run is blocked
    /// on the answer, so dropping it would hang the turn forever.
    #[test]
    fn an_extension_question_becomes_its_own_event() {
        let value = serde_json::json!({
            "type": "extension_ui_request",
            "id": "uuid-1",
            "method": "confirm",
            "title": "Clear session?",
            "message": "All messages will be lost.",
            "timeout": 5000,
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::ExtensionUi);
        assert_eq!(events[0].payload["id"], "uuid-1");
        assert_eq!(events[0].payload["method"], "confirm");
        assert_eq!(events[0].payload["title"], "Clear session?");
        assert_eq!(events[0].payload["message"], "All messages will be lost.");
        assert_eq!(events[0].payload["timeoutMs"], 5000);
    }

    /// The fire-and-forget methods each name their words differently. Reading
    /// only `message` left them all silent, and the page printed the bare label
    /// instead of the text — that is the "扩展面板:" line over the first answer.
    #[test]
    fn a_widget_brings_its_lines_and_a_clear_brings_nothing() {
        let set = serde_json::json!({
            "type": "extension_ui_request",
            "id": "uuid-3",
            "method": "setWidget",
            "widgetKey": "subagents",
            "widgetLines": ["子代理 2 个运行中", "最长 3m12s"],
        });
        let events = translate(&set);
        assert_eq!(events[0].kind, EventKind::ExtensionUi);
        assert_eq!(
            events[0].payload["message"],
            "子代理 2 个运行中\n最长 3m12s"
        );

        // pi's extensions clear their panel with `setWidget(key, undefined)` on
        // session start: no lines, no words, nothing to print.
        let clear = serde_json::json!({
            "type": "extension_ui_request",
            "id": "uuid-4",
            "method": "setWidget",
            "widgetKey": "subagents",
        });
        let events = translate(&clear);
        assert_eq!(events[0].kind, EventKind::ExtensionUi);
        assert_eq!(events[0].payload["message"], "");
    }

    #[test]
    fn a_status_and_editor_text_bring_their_own_field() {
        let status = translate(&serde_json::json!({
            "type": "extension_ui_request",
            "id": "uuid-5",
            "method": "setStatus",
            "statusKey": "mcp",
            "statusText": "MCP: 2 个服务就绪",
        }));
        assert_eq!(status[0].payload["message"], "MCP: 2 个服务就绪");

        let editor = translate(&serde_json::json!({
            "type": "extension_ui_request",
            "id": "uuid-6",
            "method": "set_editor_text",
            "text": "git status",
        }));
        assert_eq!(editor[0].payload["message"], "git status");
    }

    /// A `select` carries its options; they are the whole question.
    #[test]
    fn a_select_carries_its_options() {
        let value = serde_json::json!({
            "type": "extension_ui_request",
            "id": "uuid-2",
            "method": "select",
            "title": "Allow dangerous command?",
            "options": ["Allow", "Block"],
        });
        let events = translate(&value);
        assert_eq!(events[0].kind, EventKind::ExtensionUi);
        assert_eq!(events[0].payload["options"][0], "Allow");
        assert_eq!(events[0].payload["options"][1], "Block");
    }

    /// A fully trusted workspace answers for itself: the turn must not stop
    /// on a card the user did not expect.
    #[test]
    fn a_trusted_workspace_answers_the_question_by_itself() {
        let confirm = UiRequest {
            id: "q1".to_string(),
            method: "confirm".to_string(),
            ..UiRequest::default()
        };
        let answer = automatic_answer(&confirm, "full").expect("an answer");
        assert_eq!(answer.confirmed, Some(true));
        assert!(!answer.cancelled);

        let select = UiRequest {
            id: "q2".to_string(),
            method: "select".to_string(),
            options: vec!["允许".to_string(), "拒绝".to_string()],
            ..UiRequest::default()
        };
        assert_eq!(
            automatic_answer(&select, "full").and_then(|a| a.value),
            Some("允许".to_string())
        );

        let editor = UiRequest {
            id: "q3".to_string(),
            method: "editor".to_string(),
            prefill: "原来的文字".to_string(),
            ..UiRequest::default()
        };
        assert_eq!(
            automatic_answer(&editor, "full").and_then(|a| a.value),
            Some("原来的文字".to_string())
        );
    }

    /// Every other level keeps asking: the user has not said they trust this
    /// workspace.
    #[test]
    fn an_untrusted_workspace_still_asks() {
        let confirm = UiRequest {
            id: "q1".to_string(),
            method: "confirm".to_string(),
            ..UiRequest::default()
        };
        for level in ["readonly", "medium", "high", ""] {
            assert!(
                automatic_answer(&confirm, level).is_none(),
                "{level} must not answer for the user"
            );
        }
    }

    /// The receipt of an RPC command is protocol bookkeeping. It must not
    /// show up in the transcript, but a refused command is worth seeing.
    #[test]
    fn a_command_receipt_is_not_a_transcript_event() {
        let ok = translate(&serde_json::json!({
            "type": "response",
            "id": "ctx-1",
            "command": "get_session_stats",
            "success": true,
            "data": { "contextUsage": { "tokens": 10, "contextWindow": 100 } },
        }));
        assert!(ok.is_empty(), "a receipt must not become a ledger event");

        let refused = translate(&serde_json::json!({
            "type": "response",
            "command": "prompt",
            "success": false,
            "error": "model is not enabled",
        }));
        assert_eq!(refused.len(), 1);
        assert_eq!(refused[0].kind, EventKind::Error);
        assert!(
            refused[0].payload["message"]
                .as_str()
                .unwrap_or_default()
                .contains("model is not enabled")
        );
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
                                { "type": "thinking", "thinking": "I should count with ls" },
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
        assert_eq!(kinds, vec!["text", "text", "tool", "tool_result", "text"]);
        // Reasoning first, marked as such: the panel renders it above the
        // answer instead of blending the two into one paragraph.
        assert_eq!(steps[0]["channel"], "thinking");
        assert_eq!(steps[0]["text"], "I should count with ls");
        assert_eq!(steps[1]["text"], "counting");
        assert_eq!(steps[2]["name"], "bash");
        assert_eq!(steps[3]["output"], "42");
        assert_eq!(steps[4]["text"], "42 files");
        // The parent's own prompt is not the child's work.
        assert!(steps.iter().all(|step| step["text"] != "count files"));
    }

    /// A picture from a tool result stays on the result event, with the text
    /// around it, so the page can draw it where the tool ran.
    #[test]
    fn a_tool_result_keeps_its_output_and_images() {
        let value = serde_json::json!({
            "type": "tool_execution_end",
            "toolCallId": "call_9",
            "toolName": "bash",
            "isError": false,
            "result": { "content": [
                { "type": "text", "text": "wrote chart.png" },
                { "type": "image", "data": "aGVsbG8=", "mimeType": "image/png" }
            ] }
        });
        let events = translate(&value);
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::ToolResult);
        assert_eq!(events[0].payload["output"], "wrote chart.png");
        let images = events[0].payload["images"].as_array().expect("images");
        assert_eq!(images[0]["mimeType"], "image/png");
        assert_eq!(images[0]["data"], "aGVsbG8=");
    }

    /// Oversized pictures are reported, not written into every event row.
    #[test]
    fn an_oversized_image_is_reported_instead_of_stored() {
        let huge = "x".repeat(MAX_IMAGE_BYTES + 1);
        let (text, images) = content_parts(Some(&serde_json::json!([
            { "type": "image", "data": huge, "mimeType": "image/png" }
        ])));
        assert!(images.is_empty());
        assert!(text.contains("图片过大"));
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
        let dir = std::env::temp_dir().join(format!("anvil-pi-argv-{}", std::process::id()));
        let adapter = PiAdapter::new(Some(PathBuf::from("D:/tools/pi/pi.exe")));
        let context = SessionContext {
            state_dir: dir.clone(),
            run_timeout: Some(Duration::from_secs(60)),
        };
        let command = adapter
            .command(&context, &persona, allow_tools, tools)
            .expect("build command");
        let args = command
            .get_args()
            .map(|value| value.to_string_lossy().into_owned())
            .collect();
        let _ = std::fs::remove_dir_all(&dir);
        args
    }

    /// The interface this adapter speaks: a live stdin, not a one-shot prompt.
    #[test]
    fn the_command_is_the_long_lived_rpc_interface_not_print_mode() {
        let args = argv(OpenedSession::default(), true, &[]);
        let mode = args
            .iter()
            .position(|arg| arg == "--mode")
            .and_then(|index| args.get(index + 1))
            .cloned();
        assert_eq!(mode.as_deref(), Some("rpc"), "{args:?}");
        assert!(
            !args.iter().any(|arg| arg == "-p" || arg == "--print"),
            "RPC mode must not also be a one-shot run: {args:?}"
        );
    }

    #[test]
    fn persona_flags_reach_the_runtime() {
        let opened = OpenedSession {
            permission: String::new(),
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
            "read",
            "write",
            "edit",
            "bash",
            "grep",
            "find",
            "ls",
            "ask_question",
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
        assert_eq!(
            allowed.as_deref(),
            Some("read,grep,find,ls"),
            "{read_only:?}"
        );
        assert!(
            read_only.iter().any(|arg| arg == "--no-approve"),
            "{read_only:?}"
        );

        let trusted = argv(
            OpenedSession {
                project_trust: Some(true),
                ..OpenedSession::default()
            },
            true,
            &[],
        );
        assert!(trusted.iter().any(|arg| arg == "--approve"), "{trusted:?}");
        assert!(
            !trusted.iter().any(|arg| arg == "--no-approve"),
            "{trusted:?}"
        );

        // No decision means no flag: the runtime keeps its own default.
        let untouched = argv(OpenedSession::default(), true, &[]);
        assert!(
            !untouched
                .iter()
                .any(|arg| arg == "--approve" || arg == "--no-approve"),
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
    /// RPC mode rejects `@file` *arguments*, so the path travels inside the
    /// prompt command, and a path with a space is quoted or pi splits it.
    #[test]
    fn images_travel_inside_the_prompt_command() {
        let images = vec![
            PathBuf::from(r"D:\Downloads\陈思成.png"),
            PathBuf::from(r"D:\Downloads\second shot.jpg"),
        ];
        let command = prompt_command("turn-1", "look at these", &images);
        assert_eq!(command["type"], "prompt");
        let message = command["message"].as_str().unwrap_or("");
        assert!(
            message.starts_with("look at these @D:\\Downloads\\陈思成.png "),
            "{message}"
        );
        assert!(
            message.ends_with("@\"D:\\Downloads\\second shot.jpg\""),
            "a path with a space must be quoted or pi splits it: {message}"
        );
    }

    #[test]
    fn a_text_only_turn_carries_no_attachment() {
        let command = prompt_command("turn-1", "hello", &[]);
        assert_eq!(command["message"], "hello");
    }

    /// The compact response and the compaction event carry the same shape; the
    /// ledger must keep the runtime's own numbers, not a local estimate.
    #[test]
    fn reads_pi_compaction_result() {
        let value = serde_json::json!({
            "summary": "Earlier work summarized.",
            "firstKeptEntryId": "entry-42",
            "tokensBefore": 150000,
            "estimatedTokensAfter": 32000,
            "usage": { "input": 32000, "output": 1200 }
        });
        let outcome = compact_outcome(&value);
        assert_eq!(outcome.summary, "Earlier work summarized.");
        assert_eq!(outcome.first_kept_entry_id.as_deref(), Some("entry-42"));
        assert_eq!(outcome.tokens_before, Some(150000));
        assert_eq!(outcome.estimated_tokens_after, Some(32000));
        assert!(outcome.usage.is_some());
    }
}
