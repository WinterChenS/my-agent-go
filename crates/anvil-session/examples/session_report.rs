//! Drive the session layer against a real runtime and check facts.
//!
//! ```text
//! cargo run -p anvil-session --example session_report -- \
//!     --data-dir .tmp/test/r2/pi --state-dir .tmp/test/r2/pi-state \
//!     --action live --runtime pi --model auto/gpt-5.6-sol \
//!     --prompt "Reply with exactly: ok" --timeout 120 \
//!     --expect action:status=completed --expect text-contains=ok
//! ```
//!
//! `--action history` runs the project/history semantics against the built-in
//! `echo` adapter (`--with-echo`), so paging, search, pin, rename, archive,
//! delete, branch, compaction and the project index are proven without spending
//! a single runtime token. `--action resume` re-opens the session a previous
//! process left behind and sends into it, which is the restart case.
//!
//! Exit codes follow the acceptance contract: 0 pass, 1 an expectation failed
//! (the report is still printed), 2 the input or the environment is incomplete.

use std::path::PathBuf;
use std::process::ExitCode;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use anvil_session::adapter::{AdapterEvent, Cancel, RunOutcome, SessionAdapter};
use anvil_session::{
    ApprovalDecision, ContextKind, ContextRef, EventKind, OpenedSession, Registry,
    Result as SessionResult, Run, RunStatus, SessionCapabilities, SessionContext, SessionInput,
    SessionQuery, SessionService, SessionSpec, SessionStatus, Store, default_registry,
};

struct Options {
    data_dir: PathBuf,
    state_dir: PathBuf,
    action: String,
    runtime: String,
    model: String,
    prompt: String,
    timeout: u64,
    turns: u32,
    abort_after_ms: u64,
    keep_last: usize,
    cwd: Option<String>,
    attach: Vec<(String, String)>,
    approve: Option<String>,
    allow_tools: bool,
    tools: Vec<String>,
    with_echo: bool,
    session_file: Option<PathBuf>,
    expect: Vec<String>,
    list_runtime: Option<String>,
}

fn main() -> ExitCode {
    match run() {
        Ok(code) => code,
        Err(message) => {
            eprintln!("session_report: {message}");
            ExitCode::from(2)
        }
    }
}

fn run() -> Result<ExitCode, String> {
    let options = parse()?;
    std::fs::create_dir_all(&options.data_dir).map_err(|error| format!("data dir: {error}"))?;
    std::fs::create_dir_all(&options.state_dir).map_err(|error| format!("state dir: {error}"))?;

    let database = options.data_dir.join("sessions.sqlite");
    let store = Store::open(&database).map_err(|error| error.to_string())?;
    let registry = registry_for(&options)?;
    let service_context = SessionContext {
        state_dir: options.state_dir.clone(),
        // `--timeout 0` runs without a deadline, like the desktop app.
        run_timeout: (options.timeout > 0).then(|| Duration::from_secs(options.timeout)),
    };
    let mut service = SessionService::new(store, registry, service_context);
    let recovered = service.recover().map_err(|error| error.to_string())?;

    let mut report = serde_json::json!({
        "database": database.display().to_string(),
        "stateDir": options.state_dir.display().to_string(),
        "recoveredRuns": recovered,
        "adapters": service
            .registry()
            .ids()
            .iter()
            .map(|id| {
                let caps = service.registry().capabilities(id).expect("registered");
                serde_json::json!({
                    "id": id,
                    "capabilities": caps,
                })
            })
            .collect::<Vec<_>>(),
    });

    let action_report = match options.action.as_str() {
        "ping" => serde_json::json!({ "ok": true, "adapters": service.registry().len() }),
        "live" => live(&mut service, &options)?,
        "abort" => abort(&mut service, &options)?,
        "list" => {
            let page = service
                .page(&SessionQuery {
                    include_archived: true,
                    limit: Some(50),
                    ..SessionQuery::default()
                })
                .map_err(|error| error.to_string())?;
            serde_json::json!({
                "ok": true,
                "total": page.total,
                "sessions": page.sessions.iter().map(|session| serde_json::json!({
                    "id": session.id,
                    "status": session.status.as_str(),
                    "messages": session.message_count,
                    "nativeId": session.native_id,
                })).collect::<Vec<_>>(),
            })
        }
        "history" => history(&options)?,
        "context" => context_action(&mut service, &options)?,
        "rail" => rail_action(&mut service, &options)?,
        "export" => export_action(&mut service, &options)?,
        "resume" => resume(&mut service, &options)?,
        "projects" => {
            let projects = service.projects().map_err(|error| error.to_string())?;
            serde_json::json!({
                "ok": true,
                "count": projects.len(),
                "projects": projects.iter().map(|project| serde_json::json!({
                    "path": project.path,
                    "name": project.name,
                    "sessions": project.session_count,
                })).collect::<Vec<_>>(),
            })
        }
        other => return Err(format!("unknown action {other:?}")),
    };
    report["action"] = action_report;

    let text = report
        .pointer("/action/text")
        .and_then(|value| value.as_str())
        .unwrap_or("")
        .to_string();
    let printed = serde_json::to_string_pretty(&report).unwrap_or_else(|_| "{}".into());
    println!("{printed}");

    let failures = check(&options.expect, &report, &options.state_dir, &text);
    if failures.is_empty() {
        Ok(ExitCode::SUCCESS)
    } else {
        for failure in failures {
            eprintln!("expectation failed: {failure}");
        }
        Ok(ExitCode::from(1))
    }
}

fn live(service: &mut SessionService, options: &Options) -> Result<serde_json::Value, String> {
    let spec = SessionSpec {
        runtime_id: options.runtime.clone(),
        model: options.model.clone(),
        cwd: std::env::current_dir()
            .map_err(|error| format!("cwd: {error}"))?
            .display()
            .to_string(),
        title: Some("acceptance".into()),
        native_id: None,
        allow_tools: options.allow_tools,
        tools: options.tools.clone(),
        ..Default::default()
    };
    let session = service.open(&spec).map_err(|error| error.to_string())?;
    let mut outcome = service
        .send(&session.id, &options.prompt)
        .map_err(|error| error.to_string())?;
    // Extra turns prove the runtime resumes what the first turn created; a
    // fresh thread per turn would pass a single-turn check just as well.
    let mut turns = 1;
    while turns < options.turns.max(1) {
        turns += 1;
        let prompt = format!("{} ({turns})", options.prompt);
        outcome = service
            .send(&session.id, &prompt)
            .map_err(|error| error.to_string())?;
    }

    let detail = service
        .detail(&session.id)
        .map_err(|error| error.to_string())?;
    let events = service
        .events(&session.id)
        .map_err(|error| error.to_string())?;
    let mut kinds = std::collections::BTreeMap::<String, usize>::new();
    for event in &events {
        *kinds.entry(event.kind.as_str().to_string()).or_default() += 1;
    }

    // Keep the raw stream beside the database: an auditor must be able to read
    // what the runtime actually said, not only our summary of it.
    let stream_path = options
        .data_dir
        .join(format!("{}.events.jsonl", session.id));
    let mut lines = String::new();
    for event in &events {
        lines.push_str(&serde_json::to_string(event).unwrap_or_default());
        lines.push('\n');
    }
    let _ = std::fs::write(&stream_path, lines);
    // The next process needs to know which session to resume; this file is the
    // hand-off between the two runs of the restart case.
    let _ = std::fs::write(session_file(options), &session.id);

    let usage = events
        .iter()
        .find(|event| event.kind == EventKind::Usage)
        .map(|event| event.payload.clone())
        .unwrap_or(serde_json::Value::Null);

    Ok(serde_json::json!({
        "ok": outcome.run.status == anvil_session::RunStatus::Completed,
        "status": outcome.run.status.as_str(),
        "sessionId": session.id,
        "nativeId": detail.session.native_id,
        "model": detail.session.model,
        "turns": turns,
        "text": outcome.message.as_ref().map(|message| message.text.clone()).unwrap_or_default(),
        "runId": outcome.run.id,
        "error": outcome.run.error,
        "eventCount": events.len(),
        "eventKinds": kinds,
        "deltas": events.iter().filter(|event| event.kind == EventKind::TextDelta).count(),
        "messages": detail.messages.len(),
        "roles": detail.messages.iter().map(|message| message.role.as_str()).collect::<Vec<_>>(),
        "usage": usage,
        "stream": stream_path.display().to_string(),
    }))
}

/// Send one turn and stop it from another thread, the way the UI does.
fn abort(service: &mut SessionService, options: &Options) -> Result<serde_json::Value, String> {
    let spec = SessionSpec {
        runtime_id: options.runtime.clone(),
        model: options.model.clone(),
        cwd: std::env::current_dir()
            .map_err(|error| format!("cwd: {error}"))?
            .display()
            .to_string(),
        title: Some("abort".into()),
        native_id: None,
        allow_tools: options.allow_tools,
        tools: options.tools.clone(),
        ..Default::default()
    };
    let session = service.open(&spec).map_err(|error| error.to_string())?;
    let cancels = service.cancels();
    let id = session.id.clone();
    let prompt = options.prompt.clone();
    let wait = Duration::from_millis(options.abort_after_ms);

    // `send` holds `&mut`, so the stop arrives through the cloned registry —
    // exactly the path the host uses when the UI's stop button fires.
    let (outcome, stopped) = std::thread::scope(|scope| {
        let handle = scope.spawn(|| service.send(&id, &prompt));
        std::thread::sleep(wait);
        let stopped = cancels.cancel(&id);
        (handle.join().expect("join").expect("send"), stopped)
    });

    let detail = service
        .detail(&session.id)
        .map_err(|error| error.to_string())?;
    let events = service
        .events(&session.id)
        .map_err(|error| error.to_string())?;
    Ok(serde_json::json!({
        "ok": outcome.run.status == anvil_session::RunStatus::Aborted,
        "status": outcome.run.status.as_str(),
        "stopped": stopped,
        "sessionId": session.id,
        "nativeId": detail.session.native_id,
        "runId": outcome.run.id,
        "endedAt": outcome.run.ended_at,
        "error": outcome.run.error,
        "messages": detail.messages.len(),
        "eventCount": events.len(),
        "lastEvent": events.last().map(|event| event.kind.as_str()),
        "text": outcome.message.as_ref().map(|message| message.text.clone()).unwrap_or_default(),
    }))
}

/// An adapter that answers locally, used by the history case.
///
/// The history semantics — projects, paging, search, branch, compaction — are
/// about the ledger, not about a model. Proving them against a fake adapter
/// keeps the case deterministic and free; the live cases prove the runtimes.
/// Export one session in all three formats and check what the files contain.
///
/// The interesting assertions are the ones a happy path would skip: an existing
/// file is refused until the caller confirms, a failed write leaves no `.part`
/// behind, and tool arguments (which hold tokens and file bodies) stay out
/// unless they were explicitly asked for.
fn export_action(
    service: &mut SessionService,
    options: &Options,
) -> Result<serde_json::Value, String> {
    use anvil_session::export::{ExportFormat, ExportOptions};

    let project = options.data_dir.join("project");
    std::fs::create_dir_all(&project).map_err(|error| format!("project dir: {error}"))?;
    let spec = SessionSpec {
        runtime_id: "export".into(),
        model: options.model.clone(),
        cwd: project.display().to_string(),
        title: Some("Weekly <script>alert(1)</script> report".into()),
        native_id: None,
        allow_tools: true,
        tools: vec!["write".into()],
        ..Default::default()
    };
    let session = service.open(&spec).map_err(|error| error.to_string())?;
    service
        .send(&session.id, "export the weekly report")
        .map_err(|error| error.to_string())?;

    let root = options.data_dir.join("exports");
    let quiet = |format: ExportFormat| ExportOptions {
        format,
        include_tools: false,
        overwrite: false,
    };

    let mut written = Vec::new();
    for format in [
        ExportFormat::Html,
        ExportFormat::Markdown,
        ExportFormat::Json,
    ] {
        let report = service
            .export(&session.id, &root, &quiet(format))
            .map_err(|error| format!("export {}: {error}", format.as_str()))?;
        let body = std::fs::read_to_string(&report.path)
            .map_err(|error| format!("read {}: {error}", report.path))?;
        written.push((report, body));
    }

    let needle = "ZEBRA-EXPORT";
    let same_text = written.iter().all(|(_, body)| body.contains(needle));
    let html = &written[0].1;
    let escaped = html.contains("&lt;script&gt;") && !html.contains("<script>");
    let hash_len = written[0].0.sha256.len();
    let canonical_root = std::fs::canonicalize(&root).unwrap_or(root.clone());
    let path_inside = written
        .iter()
        .all(|(report, _)| PathBuf::from(&report.path).starts_with(&canonical_root));
    let secret_absent = written
        .iter()
        .all(|(_, body)| !body.contains("sk-export-SECRET"));

    // Same content, same hash: an export is reproducible, which is what makes
    // the recorded digest useful to an auditor.
    let first = &written[0].0;
    let again = service
        .export(
            &session.id,
            &root,
            &ExportOptions {
                format: ExportFormat::Html,
                include_tools: false,
                overwrite: true,
            },
        )
        .map_err(|error| error.to_string())?;
    let hash_stable = again.sha256 == first.sha256;

    // Without the confirm, an existing file is refused and left untouched.
    let overwrite_refused = matches!(
        service.export(&session.id, &root, &quiet(ExportFormat::Html)),
        Err(error) if error.code() == "rejected"
    );

    // A target occupied by a directory cannot be replaced; the attempt must not
    // leave a `.part` file that looks like a finished export.
    let blocked_name = again.file_name.clone();
    std::fs::remove_file(root.join(&blocked_name)).map_err(|error| error.to_string())?;
    std::fs::create_dir_all(root.join(&blocked_name)).map_err(|error| error.to_string())?;
    let failed = service.export(
        &session.id,
        &root,
        &ExportOptions {
            format: ExportFormat::Html,
            include_tools: false,
            overwrite: true,
        },
    );
    let failure_no_partial =
        failed.is_err() && !root.join(format!(".{blocked_name}.part")).exists();

    // The opt-in changes what is written, and still not the private argument.
    let with_tools = service
        .export(
            &session.id,
            &root,
            &ExportOptions {
                format: ExportFormat::Json,
                include_tools: true,
                overwrite: true,
            },
        )
        .map_err(|error| error.to_string())?;
    let shown_body =
        std::fs::read_to_string(&with_tools.path).map_err(|error| error.to_string())?;

    let formats: Vec<&str> = written
        .iter()
        .map(|(report, _)| report.format.as_str())
        .collect();
    Ok(serde_json::json!({
        "ok": same_text
            && escaped
            && path_inside
            && hash_len == 64
            && hash_stable
            && overwrite_refused
            && failure_no_partial
            && secret_absent
            && shown_body.contains("out/export.md"),
        "formats": formats,
        "sameText": same_text,
        "escaped": escaped,
        "hashLen": hash_len,
        "hashStable": hash_stable,
        "pathInside": path_inside,
        "overwriteRefused": overwrite_refused,
        "failureNoPartial": failure_no_partial,
        "secretAbsent": secret_absent,
        "toolRowsHidden": written[0].0.tool_rows,
        "toolRowsShown": with_tools.tool_rows,
        "toolsIncluded": with_tools.tools_included,
        "root": root.display().to_string(),
        "fileNames": written.iter().map(|(report, _)| report.file_name.clone()).collect::<Vec<_>>(),
        // Recorded, not faked: external sharing needs a service this build has
        // none of, so the capability is absent rather than "passing".
        "share": "unavailable: no external sharing service is configured",
    }))
}

/// A runtime whose turn cites text with HTML in it and calls one writing tool,
/// so the export has something worth escaping and something worth withholding.
struct ExportAdapter;

impl SessionAdapter for ExportAdapter {
    fn id(&self) -> &str {
        "export"
    }

    fn capabilities(&self) -> SessionCapabilities {
        SessionCapabilities::full()
    }

    fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> SessionResult<OpenedSession> {
        Ok(OpenedSession {
            runtime_id: "export".into(),
            native_id: "export-native".into(),
            model: spec.model.clone(),
            cwd: spec.cwd.clone(),
            allow_tools: spec.allow_tools,
            tools: spec.tools.clone(),
            ..Default::default()
        })
    }

    fn send(
        &self,
        _context: &SessionContext,
        _opened: &OpenedSession,
        _prompt: &str,
        _cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> SessionResult<RunOutcome> {
        sink(AdapterEvent::new(
            EventKind::ToolCall,
            serde_json::json!({
                "tool": "write",
                "args": { "path": "out/export.md", "secret": "sk-export-SECRET" }
            }),
        ));
        sink(AdapterEvent::new(
            EventKind::ToolResult,
            serde_json::json!({ "tool": "write", "ok": true }),
        ));
        Ok(RunOutcome::completed(
            "export needle ZEBRA-EXPORT and a hostile <script>alert(1)</script> tag",
            None,
        ))
    }
}

/// Aggregate the right rail from a two-turn session and check the facts the
/// panes claim.
///
/// Every number here comes from the ledger or from the filesystem; the case
/// asserts them by name (`action:sources=2`), so a pane that invents a row or
/// loses the turn a row came from fails instead of looking plausible.
fn rail_action(
    service: &mut SessionService,
    options: &Options,
) -> Result<serde_json::Value, String> {
    let project = match &options.cwd {
        Some(dir) => PathBuf::from(dir),
        None => options.data_dir.join("project"),
    };
    std::fs::create_dir_all(&project).map_err(|error| format!("project dir: {error}"))?;
    let spec = SessionSpec {
        runtime_id: "rail".into(),
        model: options.model.clone(),
        cwd: project.display().to_string(),
        title: Some("rail".into()),
        native_id: None,
        allow_tools: true,
        tools: vec!["write".into(), "read".into(), "command".into()],
        ..Default::default()
    };

    let session = service.open(&spec).map_err(|error| error.to_string())?;
    service
        .send(&session.id, "write the report")
        .map_err(|error| error.to_string())?;
    service
        .send(&session.id, "same sources again")
        .map_err(|error| error.to_string())?;
    let view = service
        .rail(&session.id)
        .map_err(|error| error.to_string())?;

    // An empty session must produce an empty rail: no rows, no page, and the
    // flag that tells the UI to show its empty states.
    let idle = service.open(&spec).map_err(|error| error.to_string())?;
    let idle_view = service.rail(&idle.id).map_err(|error| error.to_string())?;

    let detail = service
        .detail(&session.id)
        .map_err(|error| error.to_string())?;
    let mut markers = 0usize;
    let mut resolved = 0usize;
    for message in &detail.messages {
        if message.role != anvil_session::Role::Assistant {
            continue;
        }
        for number in cite_numbers(&message.text) {
            markers += 1;
            if number >= 1 && number as usize <= view.sources.len() {
                resolved += 1;
            }
        }
    }

    let canonical = std::fs::canonicalize(&project).unwrap_or(project.clone());
    let artifacts_inside = view
        .artifacts
        .iter()
        .all(|artifact| PathBuf::from(&artifact.path).starts_with(&canonical));
    // "A row goes back to its turn" is only true when the turn index and the
    // run id agree with the ledger, in both directions.
    let turns_linked = view.artifacts.iter().all(|artifact| {
        view.turns
            .iter()
            .any(|turn| turn.run_id == artifact.run_id && turn.turn == artifact.turn)
    }) && view.exec.iter().all(|row| {
        view.turns
            .iter()
            .any(|turn| turn.run_id == row.run_id && turn.turn == row.turn)
    });

    let artifacts: Vec<&str> = view
        .artifacts
        .iter()
        .map(|artifact| artifact.relative.as_str())
        .collect();
    let calls: Vec<usize> = view.turns.iter().map(|turn| turn.calls).collect();
    let all_done = view.exec.iter().all(|row| row.status == "done");
    let page = view.browser.page.as_ref();

    Ok(serde_json::json!({
        "ok": view.artifacts.len() == 2
            && view.sources.len() == 2
            && view.exec.len() == 5
            && artifacts_inside
            && turns_linked
            && all_done,
        "sessionId": session.id,
        "project": project.display().to_string(),
        "artifacts": view.artifacts.len(),
        "artifactNames": artifacts,
        "artifactTouches": view.artifacts.iter().map(|artifact| artifact.touches).collect::<Vec<_>>(),
        "artifactsInside": artifacts_inside,
        "outsidePaths": view.outside_paths,
        "sources": view.sources.len(),
        "sourceNumbers": view.sources.iter().map(|source| source.number).collect::<Vec<_>>(),
        "sourceTurn": view.sources.first().map(|source| source.turn),
        "exec": view.exec.len(),
        "execStatuses": view.exec.iter().map(|row| row.status.clone()).collect::<Vec<_>>(),
        "turns": view.turns.len(),
        "turnCalls": calls,
        "turnsLinked": turns_linked,
        "cites": markers,
        "citesResolved": resolved,
        "usageInput": view.usage.as_ref().map(|usage| usage.input),
        "usageCost": view.usage.as_ref().map(|usage| usage.cost),
        "browserState": view.browser.state,
        "browserSandbox": page.map(|page| page.sandbox.clone()),
        "browserMarker": page.map(|page| page.html.contains("RAIL-PAGE-OK")),
        "emptyRailEmpty": idle_view.empty,
        "emptyRailArtifacts": idle_view.artifacts.len(),
        "emptyRailSources": idle_view.sources.len(),
        "emptyRailBrowser": idle_view.browser.state,
    }))
}

/// The `[n]` markers in one message, as numbers.
fn cite_numbers(text: &str) -> Vec<u32> {
    let bytes: Vec<char> = text.chars().collect();
    let mut numbers = Vec::new();
    let mut index = 0usize;
    while index < bytes.len() {
        if bytes[index] != '[' {
            index += 1;
            continue;
        }
        let mut cursor = index + 1;
        let mut digits = String::new();
        while cursor < bytes.len() && bytes[cursor].is_ascii_digit() && digits.len() < 3 {
            digits.push(bytes[cursor]);
            cursor += 1;
        }
        if !digits.is_empty() && cursor < bytes.len() && bytes[cursor] == ']' {
            if let Ok(number) = digits.parse::<u32>() {
                numbers.push(number);
            }
            index = cursor + 1;
            continue;
        }
        index += 1;
    }
    numbers
}

/// A runtime that writes real files and reports real tool calls, locally.
///
/// The rail reads four things: tool calls, produced files, citations and one
/// previewable page. This double produces exactly those, on disk, so the panes
/// are proven against files and a ledger rather than against a fixture object.
struct RailAdapter {
    turns: AtomicUsize,
}

impl RailAdapter {
    fn new() -> Self {
        Self {
            turns: AtomicUsize::new(0),
        }
    }
}

impl SessionAdapter for RailAdapter {
    fn id(&self) -> &str {
        "rail"
    }

    fn capabilities(&self) -> SessionCapabilities {
        SessionCapabilities::full()
    }

    fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> SessionResult<OpenedSession> {
        Ok(OpenedSession {
            runtime_id: "rail".into(),
            native_id: "rail-native".into(),
            model: spec.model.clone(),
            cwd: spec.cwd.clone(),
            allow_tools: spec.allow_tools,
            tools: spec.tools.clone(),
            ..Default::default()
        })
    }

    fn send(
        &self,
        _context: &SessionContext,
        opened: &OpenedSession,
        _prompt: &str,
        _cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> SessionResult<RunOutcome> {
        let turn = self.turns.fetch_add(1, Ordering::SeqCst) + 1;
        let root = PathBuf::from(&opened.cwd);
        let write = |relative: &str, body: &str| -> bool {
            let path = root.join(relative);
            if let Some(parent) = path.parent() {
                let _ = std::fs::create_dir_all(parent);
            }
            std::fs::write(&path, body).is_ok()
        };

        let mut call = |tool: &str, args: serde_json::Value, ok: bool| {
            sink(AdapterEvent::new(
                EventKind::ToolCall,
                serde_json::json!({ "tool": tool, "args": args }),
            ));
            sink(AdapterEvent::new(
                EventKind::ToolResult,
                serde_json::json!({ "tool": tool, "ok": ok }),
            ));
        };

        let text = if turn == 1 {
            let wrote = write("out/report.md", "RAIL-REPORT-BODY\n");
            call(
                "write",
                serde_json::json!({ "path": "out/report.md" }),
                wrote,
            );
            let page = write(
                "out/page.html",
                "<!doctype html><h1>RAIL-PAGE-OK</h1><p>local preview</p>",
            );
            call(
                "write",
                serde_json::json!({ "path": "out/page.html" }),
                page,
            );
            // A path outside the project: it exists, so only the confinement
            // check keeps it out of the pane.
            let _ = std::fs::write(root.join("../outside-rail.txt"), "OUTSIDE-RAIL\n");
            call(
                "command",
                serde_json::json!("echo x > ../outside-rail.txt"),
                true,
            );
            call("read", serde_json::json!({ "path": "out/report.md" }), true);
            // The numbers the model writes must line up with the URLs below:
            // the rail numbers sources by first appearance, so `[1]` is the
            // first URL and `[2]` the second.
            "Wrote the report [1] and the page [2]. Sources: https://docs.example.com/a , https://news.example.org/b"
        } else {
            // Same file again: the pane lists it once and counts the touches.
            let wrote = write("out/report.md", "RAIL-REPORT-BODY v2\n");
            call(
                "write",
                serde_json::json!({ "path": "out/report.md" }),
                wrote,
            );
            // `[9]` has no source behind it, and must stay plain text in the UI.
            "Same sources [1] [2], and a marker with no source [9]."
        };

        sink(AdapterEvent::new(
            EventKind::Usage,
            serde_json::json!({
                "usage": { "input": 240, "output": 9, "cacheRead": 6912, "cost": { "total": 0.5 } }
            }),
        ));
        Ok(RunOutcome::completed(text, None))
    }
}

struct EchoAdapter {
    opens: AtomicUsize,
}

impl EchoAdapter {
    fn new() -> Self {
        Self {
            opens: AtomicUsize::new(0),
        }
    }
}

impl SessionAdapter for EchoAdapter {
    fn id(&self) -> &str {
        "echo"
    }

    fn capabilities(&self) -> SessionCapabilities {
        SessionCapabilities::full()
    }

    fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> SessionResult<OpenedSession> {
        let index = self.opens.fetch_add(1, Ordering::SeqCst);
        Ok(OpenedSession {
            runtime_id: "echo".into(),
            // Every open is a distinct native conversation, so a branch can be
            // told apart from the session it came from.
            native_id: format!("echo-native-{index}"),
            model: spec.model.clone(),
            cwd: spec.cwd.clone(),
            allow_tools: spec.allow_tools,
            tools: spec.tools.clone(),
            ..Default::default()
        })
    }

    fn send(
        &self,
        _context: &SessionContext,
        opened: &OpenedSession,
        prompt: &str,
        _cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> SessionResult<RunOutcome> {
        sink(AdapterEvent::text_delta("echo:"));
        let text = format!("echo:{prompt}");
        Ok(RunOutcome::completed(
            &text,
            Some(serde_json::json!({ "totalTokens": text.len() })),
        )
        .with_native_id(&opened.native_id))
    }
}

fn session_file(options: &Options) -> PathBuf {
    options
        .session_file
        .clone()
        .unwrap_or_else(|| options.data_dir.join("last-session.txt"))
}

fn build_service(options: &Options) -> Result<SessionService, String> {
    let database = options.data_dir.join("sessions.sqlite");
    let store = Store::open(&database).map_err(|error| error.to_string())?;
    Ok(SessionService::new(
        store,
        registry_for(options)?,
        SessionContext {
            state_dir: options.state_dir.clone(),
            run_timeout: (options.timeout > 0).then(|| Duration::from_secs(options.timeout)),
        },
    ))
}

/// Project and history semantics on one real ledger, across a restart.
///
/// The service is dropped and rebuilt in the middle on purpose: that is what the
/// next process sees, and it is the only way to catch an index that lives in
/// memory instead of in SQLite.
fn history(options: &Options) -> Result<serde_json::Value, String> {
    if !options.with_echo {
        return Err("--action history needs --with-echo (the echo adapter)".into());
    }
    let alpha = "D:\\work\\alpha";
    let beta = "D:\\work\\beta";
    let spec = |cwd: &str| SessionSpec {
        runtime_id: "echo".into(),
        model: "echo-1".into(),
        cwd: cwd.to_string(),
        title: None,
        native_id: None,
        allow_tools: false,
        tools: Vec::new(),
        ..Default::default()
    };

    let mut expected_alpha = Vec::new();
    let mut expected_beta = Vec::new();
    let (pinned, renamed, archived, survived) = {
        let mut service = build_service(options)?;
        for index in 0..3 {
            let session = service
                .open(&spec(alpha))
                .map_err(|error| error.to_string())?;
            service
                .send(&session.id, &format!("alpha turn {index}"))
                .map_err(|error| error.to_string())?;
            expected_alpha.push(session.id);
        }
        for index in 0..2 {
            let session = service
                .open(&spec(beta))
                .map_err(|error| error.to_string())?;
            service
                .send(&session.id, &format!("beta turn {index}"))
                .map_err(|error| error.to_string())?;
            expected_beta.push(session.id);
        }
        service
            .set_pinned(&expected_alpha[0], true)
            .map_err(|error| error.to_string())?;
        service
            .rename(&expected_alpha[1], "Renamed alpha")
            .map_err(|error| error.to_string())?;
        service
            .set_archived(&expected_alpha[2], true)
            .map_err(|error| error.to_string())?;

        // A process that dies mid-run leaves a run and a session marked
        // running. Reopening must repair both, or the session can never send
        // again.
        let stuck = Run {
            id: format!("{}:stuck", expected_beta[1]),
            session_id: expected_beta[1].clone(),
            status: RunStatus::Running,
            started_at: anvil_session::clock::now_rfc3339(),
            ended_at: None,
            error: None,
            prompt: "never answered".into(),
        };
        let store = service.store_mut();
        store.start_run(&stuck).map_err(|error| error.to_string())?;
        store
            .set_status(&expected_beta[1], SessionStatus::Running, &stuck.started_at)
            .map_err(|error| error.to_string())?;
        (
            expected_alpha[0].clone(),
            expected_alpha[1].clone(),
            expected_alpha[2].clone(),
            expected_beta[1].clone(),
        )
    };

    // ---- a new process opens the same file ------------------------------
    let mut service = build_service(options)?;
    let recovered = service.recover().map_err(|error| error.to_string())?;

    // Paging: walk the whole list two rows at a time and check that the union is
    // exactly the live set, with no row visited twice.
    let live_before: Vec<String> = expected_alpha[..2]
        .iter()
        .chain(expected_beta.iter())
        .cloned()
        .collect();
    let mut walked: Vec<String> = Vec::new();
    let mut cursor: Option<String> = None;
    let mut pages = 0;
    loop {
        let page = service
            .page(&SessionQuery {
                limit: Some(2),
                cursor: cursor.clone(),
                ..SessionQuery::default()
            })
            .map_err(|error| error.to_string())?;
        pages += 1;
        walked.extend(page.sessions.iter().map(|session| session.id.clone()));
        match page.next_cursor {
            Some(next) => cursor = Some(next),
            None => break,
        }
    }
    let duplicates = walked.len()
        - walked
            .iter()
            .collect::<std::collections::BTreeSet<_>>()
            .len();
    let missing = live_before.iter().filter(|id| !walked.contains(id)).count();
    let unexpected = walked.iter().filter(|id| !live_before.contains(id)).count();

    let projects = service.projects().map_err(|error| error.to_string())?;
    let alpha_project = projects.iter().find(|project| project.path == alpha);
    let beta_project = projects.iter().find(|project| project.path == beta);

    let search_hits = service
        .page(&SessionQuery {
            search: Some("alpha turn 1".into()),
            ..SessionQuery::default()
        })
        .map_err(|error| error.to_string())?
        .total;
    let first_page = service
        .page(&SessionQuery {
            limit: Some(2),
            ..SessionQuery::default()
        })
        .map_err(|error| error.to_string())?;
    let pin_first = first_page
        .sessions
        .first()
        .map(|session| session.id.clone());
    let rename_visible = service
        .page(&SessionQuery {
            search: Some("Renamed alpha".into()),
            ..SessionQuery::default()
        })
        .map_err(|error| error.to_string())?
        .sessions
        .iter()
        .any(|session| session.id == renamed);
    let archive_hidden = service
        .page(&SessionQuery::default())
        .map_err(|error| error.to_string())?
        .sessions
        .iter()
        .all(|session| session.id != archived);
    let archive_visible = service
        .page(&SessionQuery {
            include_archived: true,
            ..SessionQuery::default()
        })
        .map_err(|error| error.to_string())?
        .sessions
        .iter()
        .any(|session| session.id == archived);

    // The session left "running" by the previous process accepts a turn again.
    let recovered_send = service
        .send(&survived, "are you back?")
        .map_err(|error| error.to_string())?;
    let resumed_after_recovery = recovered_send.run.status == RunStatus::Completed;

    // Delete removes the row and its messages, and the project goes with its
    // last session.
    let total_before_delete = service
        .page(&SessionQuery::default())
        .map_err(|error| error.to_string())?
        .total;
    service
        .delete(&expected_beta[1])
        .map_err(|error| error.to_string())?;
    let total_after_delete = service
        .page(&SessionQuery::default())
        .map_err(|error| error.to_string())?
        .total;
    let deleted_gone = service.detail(&expected_beta[1]).is_err();

    // ---- branch ---------------------------------------------------------
    // The parent already ran a turn, so the seed is not empty: the branch's
    // reply proves the transcript travelled with it.
    let branch = service
        .fork(&pinned, None)
        .map_err(|error| error.to_string())?;
    let branch_reply = service
        .send(&branch.id, "what did I say first?")
        .map_err(|error| error.to_string())?;
    let branch_carried = branch_reply
        .message
        .as_ref()
        .map(|message| message.text.contains("alpha turn 0"))
        .unwrap_or(false);
    let branch_seeded_event = service
        .events(&branch.id)
        .map_err(|error| error.to_string())?
        .iter()
        .any(|event| {
            event.kind == EventKind::TurnStarted
                && event.payload["seeded"] == serde_json::Value::Bool(true)
        });

    // ---- compaction -----------------------------------------------------
    service
        .send(&renamed, "third alpha turn")
        .map_err(|error| error.to_string())?;
    let before_compact = service
        .detail(&renamed)
        .map_err(|error| error.to_string())?
        .messages
        .len();
    let report = service
        .compact(&renamed, options.keep_last, None)
        .map_err(|error| error.to_string())?;
    let compacted = service
        .detail(&renamed)
        .map_err(|error| error.to_string())?;
    let compact_event = service
        .events(&renamed)
        .map_err(|error| error.to_string())?
        .into_iter()
        .find(|event| event.kind == EventKind::Compacted);
    let native_untouched = compact_event
        .as_ref()
        .map(|event| event.payload["nativeContext"] == "untouched")
        .unwrap_or(false);

    // ---- project isolation after all of the above ------------------------
    let alpha_page = service
        .page(&SessionQuery {
            project_path: Some(alpha.into()),
            ..SessionQuery::default()
        })
        .map_err(|error| error.to_string())?;
    let cross_project = alpha_page
        .sessions
        .iter()
        .filter(|session| session.project_path != alpha)
        .count();

    let facts = serde_json::json!({
        "ok": duplicates == 0
            && missing == 0
            && unexpected == 0
            && cross_project == 0
            && pin_first.as_deref() == Some(pinned.as_str())
            && rename_visible
            && archive_hidden
            && archive_visible
            && resumed_after_recovery
            && deleted_gone
            && total_after_delete + 1 == total_before_delete
            && branch_carried
            && branch_seeded_event
            && native_untouched
            && compacted.messages.first().map(|message| message.role) == Some(anvil_session::Role::System),
        "recoveredRuns": recovered,
        "resumedAfterRecovery": resumed_after_recovery,
        "pages": pages,
        "walked": walked.len(),
        "duplicates": duplicates,
        "missing": missing,
        "unexpected": unexpected,
        "totalAfterRestart": live_before.len(),
        "projects": projects.iter().map(|project| serde_json::json!({
            "path": project.path,
            "name": project.name,
            "sessions": project.session_count,
        })).collect::<Vec<_>>(),
        "projectCount": projects.len(),
        "alphaSessions": alpha_project.map(|project| project.session_count),
        "betaSessions": beta_project.map(|project| project.session_count),
        "searchHits": search_hits,
        "pinFirst": pin_first.as_deref() == Some(pinned.as_str()),
        "renameVisible": rename_visible,
        "archiveHidden": archive_hidden,
        "archiveVisible": archive_visible,
        "totalBeforeDelete": total_before_delete,
        "totalAfterDelete": total_after_delete,
        "deletedGone": deleted_gone,
        "crossProject": cross_project,
        "branch": {
            "id": branch.id,
            "parentId": branch.parent_id,
            "branchAt": branch.branch_at,
            "messages": branch.message_count,
            "carriedParentText": branch_carried,
            "seededEvent": branch_seeded_event,
            "nativeId": branch.native_id,
        },
        "compact": {
            "before": before_compact,
            "collapsed": report.collapsed,
            "kept": report.kept,
            "after": compacted.messages.len(),
            "firstRole": compacted.messages.first().map(|message| message.role.as_str()),
            "nativeUntouched": native_untouched,
        },
    });
    Ok(facts)
}

/// One turn with attached context, optionally answering the approval it raises.
///
/// This is the only live path that proves two things at once: the reference
/// content actually reached the runtime (the answer quotes it), and a restricted
/// tool really ran (the ledger has `tool_call`/`tool_result` rows).
fn context_action(
    service: &mut SessionService,
    options: &Options,
) -> Result<serde_json::Value, String> {
    let cwd = options.cwd.clone().unwrap_or_else(|| {
        std::env::current_dir()
            .map(|path| path.display().to_string())
            .unwrap_or_default()
    });
    let spec = SessionSpec {
        runtime_id: options.runtime.clone(),
        model: options.model.clone(),
        cwd,
        title: Some("context".into()),
        native_id: None,
        allow_tools: options.allow_tools,
        tools: options.tools.clone(),
        ..Default::default()
    };
    let session = service.open(&spec).map_err(|error| error.to_string())?;

    // A headless run cannot wait for a person, so the case names the answer up
    // front. The request itself is still real: it goes through the same registry
    // the UI uses, and the decision is recorded in the ledger.
    if let Some(mode) = options.approve.clone() {
        let approvals = service.approvals();
        let owner = session.id.clone();
        std::thread::spawn(move || {
            let decision = match mode.as_str() {
                "allow" => ApprovalDecision::Allow,
                "cancel" => ApprovalDecision::Cancel,
                _ => ApprovalDecision::Deny,
            };
            for _ in 0..600 {
                if let Some(id) = approvals.pending().into_iter().next() {
                    let _ = approvals.resolve(&owner, &id, decision);
                    return;
                }
                std::thread::sleep(Duration::from_millis(50));
            }
        });
    }

    let context: Vec<ContextRef> = options
        .attach
        .iter()
        .map(|(kind, path)| ContextRef {
            kind: ContextKind::from_label(kind).unwrap_or(ContextKind::File),
            path: path.clone(),
            label: None,
        })
        .collect();

    let outcome = service.send_with(
        &session.id,
        &SessionInput {
            prompt: options.prompt.clone(),
            context,

            ..Default::default()
        },
    );

    let detail = service
        .detail(&session.id)
        .map_err(|error| error.to_string())?;
    let events = service
        .events(&session.id)
        .map_err(|error| error.to_string())?;
    let count = |kind: EventKind| events.iter().filter(|event| event.kind == kind).count();
    let attached_chars: usize = events
        .iter()
        .filter(|event| event.kind == EventKind::ContextAttached)
        .filter_map(|event| {
            event
                .payload
                .get("totalChars")
                .and_then(serde_json::Value::as_u64)
        })
        .map(|value| value as usize)
        .sum();
    let refs: Vec<serde_json::Value> = events
        .iter()
        .filter(|event| event.kind == EventKind::ContextAttached)
        .filter_map(|event| {
            event
                .payload
                .get("refs")
                .and_then(serde_json::Value::as_array)
                .cloned()
        })
        .flatten()
        .collect();
    let last_decision = events
        .iter()
        .rev()
        .find(|event| event.kind == EventKind::ApprovalResolved)
        .and_then(|event| {
            event
                .payload
                .get("decision")
                .and_then(serde_json::Value::as_str)
        })
        .map(str::to_string);

    let (ok, status, text, error, code, run_id) = match &outcome {
        Ok(report) => (
            report.run.status == RunStatus::Completed,
            report.run.status.as_str().to_string(),
            report
                .message
                .as_ref()
                .map(|message| message.text.clone())
                .unwrap_or_default(),
            report.run.error.clone(),
            String::new(),
            report.run.id.clone(),
        ),
        Err(failure) => (
            false,
            "rejected".to_string(),
            String::new(),
            Some(failure.to_string()),
            failure.code().to_string(),
            String::new(),
        ),
    };

    Ok(serde_json::json!({
        "ok": ok,
        "status": status,
        "code": code,
        "text": text,
        "error": error,
        "runId": run_id,
        "sessionId": session.id,
        "nativeId": detail.session.native_id,
        "runs": detail.runs.len(),
        "messages": detail.messages.len(),
        "toolCalls": count(EventKind::ToolCall),
        "toolResults": count(EventKind::ToolResult),
        "contextRefs": refs.len(),
        "contextChars": attached_chars,
        "context": refs,
        "approvals": count(EventKind::ApprovalRequest),
        "lastApprovalDecision": last_decision,
    }))
}

/// Send one more turn into the session a previous process created.
fn resume(service: &mut SessionService, options: &Options) -> Result<serde_json::Value, String> {
    let file = session_file(options);
    let id = std::fs::read_to_string(&file)
        .map_err(|error| format!("read {}: {error}", file.display()))?
        .trim()
        .to_string();
    if id.is_empty() {
        return Err(format!("{} is empty", file.display()));
    }
    let before = service.detail(&id).map_err(|error| error.to_string())?;
    let native_before = before.session.native_id.clone();
    let outcome = service
        .send(&id, &options.prompt)
        .map_err(|error| error.to_string())?;
    let after = service.detail(&id).map_err(|error| error.to_string())?;
    Ok(serde_json::json!({
        "ok": outcome.run.status == anvil_session::RunStatus::Completed,
        "status": outcome.run.status.as_str(),
        "sessionId": id,
        "nativeId": after.session.native_id,
        "nativeIdSame": after.session.native_id == native_before,
        "nativeIdPresent": !after.session.native_id.trim().is_empty(),
        "messagesBefore": before.messages.len(),
        "messages": after.messages.len(),
        "runId": outcome.run.id,
        "error": outcome.run.error,
        "text": outcome.message.as_ref().map(|message| message.text.clone()).unwrap_or_default(),
    }))
}

fn registry_for(options: &Options) -> Result<Registry, String> {
    if options.action == "export" {
        let mut registry = Registry::new();
        registry
            .register(std::sync::Arc::new(ExportAdapter))
            .map_err(|error| error.to_string())?;
        return Ok(registry);
    }
    if options.action == "rail" {
        // The rail is a read of the ledger; the runtime only has to produce a
        // believable ledger, so a local double keeps the case token-free while
        // every row the panes render is still real.
        let mut registry = Registry::new();
        registry
            .register(std::sync::Arc::new(RailAdapter::new()))
            .map_err(|error| error.to_string())?;
        return Ok(registry);
    }
    // `--list-runtime` narrows the registry to one adapter, so a case can prove
    // failure handling for a runtime that is absent on this machine.
    let mut registry = match options.list_runtime.as_deref() {
        Some("none") => Registry::new(),
        _ => default_registry(),
    };
    if options.with_echo {
        registry
            .register(std::sync::Arc::new(EchoAdapter::new()))
            .map_err(|error| error.to_string())?;
    }
    Ok(registry)
}

fn check(
    expectations: &[String],
    report: &serde_json::Value,
    state_dir: &std::path::Path,
    text: &str,
) -> Vec<String> {
    let mut failures = Vec::new();
    for expectation in expectations {
        let Some((selector, want)) = expectation.split_once('=') else {
            failures.push(format!("{expectation:?} is not selector=value"));
            continue;
        };
        match selector {
            "action:status" => {
                let actual = report
                    .pointer("/action/status")
                    .and_then(|value| value.as_str())
                    .unwrap_or("");
                if actual != want {
                    failures.push(format!("action status = {actual:?}, want {want:?}"));
                }
            }
            "action:ok" => {
                let actual = report
                    .pointer("/action/ok")
                    .and_then(serde_json::Value::as_bool)
                    .unwrap_or(false);
                if actual.to_string() != want {
                    failures.push(format!("action ok = {actual}, want {want}"));
                }
            }
            "action:error-contains" => {
                let actual = report
                    .pointer("/action/error")
                    .and_then(|value| value.as_str())
                    .unwrap_or("");
                if !actual.contains(want) {
                    failures.push(format!(
                        "action error = {actual:?}, want it to contain {want:?}"
                    ));
                }
            }
            "text-contains" => {
                if !text.contains(want) {
                    failures.push(format!("text = {text:?}, want it to contain {want:?}"));
                }
            }
            "deltas-min" | "events-min" | "messages-min" | "tools-min" | "context-min" => {
                let field = match selector {
                    "deltas-min" => "/action/deltas",
                    "events-min" => "/action/eventCount",
                    "tools-min" => "/action/toolCalls",
                    "context-min" => "/action/contextRefs",
                    _ => "/action/messages",
                };
                let actual = report.pointer(field).and_then(serde_json::Value::as_i64);
                let want: i64 = match want.parse() {
                    Ok(number) => number,
                    Err(_) => {
                        failures.push(format!("{selector} needs a number, not {want:?}"));
                        continue;
                    }
                };
                match actual {
                    Some(value) if value >= want => {}
                    Some(value) => {
                        failures.push(format!("{selector} = {value}, want at least {want}"))
                    }
                    None => failures.push(format!("{selector} is missing")),
                }
            }
            "usage-present" => {
                let present = report
                    .pointer("/action/usage")
                    .map(|value| !value.is_null())
                    .unwrap_or(false);
                if present.to_string() != want {
                    failures.push(format!("usage present = {present}, want {want}"));
                }
            }
            "session-files-min" => {
                let count = count_files(state_dir);
                let want: usize = match want.parse() {
                    Ok(number) => number,
                    Err(_) => {
                        failures.push(format!("{selector} needs a number, not {want:?}"));
                        continue;
                    }
                };
                if count < want {
                    failures.push(format!(
                        "session files in {} = {count}, want at least {want}",
                        state_dir.display()
                    ));
                }
            }
            "native-id-present" => {
                let present = report
                    .pointer("/action/nativeId")
                    .and_then(|value| value.as_str())
                    .map(|value| !value.is_empty())
                    .unwrap_or(false);
                if present.to_string() != want {
                    failures.push(format!("native id present = {present}, want {want}"));
                }
            }
            "adapter-count" => {
                let actual = report["adapters"].as_array().map(Vec::len).unwrap_or(0);
                if actual.to_string() != want {
                    failures.push(format!("adapter count = {actual}, want {want}"));
                }
            }
            "adapter:capability" => {
                // adapter:capability=pi:streaming:true
                let mut parts = want.split(':');
                let (Some(runtime), Some(name), Some(value)) =
                    (parts.next(), parts.next(), parts.next())
                else {
                    failures.push(format!(
                        "adapter:capability needs <id>:<name>:<value>, got {want:?}"
                    ));
                    continue;
                };
                let actual = report["adapters"]
                    .as_array()
                    .and_then(|adapters| adapters.iter().find(|adapter| adapter["id"] == runtime))
                    .and_then(|adapter| adapter.pointer("/capabilities").cloned())
                    .and_then(|capabilities| capabilities.get(name).cloned())
                    .map(|value| value.to_string())
                    .unwrap_or_else(|| "missing".into());
                if actual != value {
                    failures.push(format!(
                        "capability {runtime}.{name} = {actual}, want {value}"
                    ));
                }
            }
            // `action:<field>=<value>` reads any report field directly, so a
            // new fact does not need a new selector. Dots address nested
            // fields (`action:branch.branchAt`), because a flat namespace
            // would force every report to invent unique top-level names.
            other if other.starts_with("action:") => {
                let field = &other["action:".len()..];
                let path = field.replace('.', "/");
                let actual = report
                    .pointer(&format!("/action/{path}"))
                    .map(|value| match value {
                        serde_json::Value::String(text) => text.clone(),
                        other => other.to_string(),
                    })
                    .unwrap_or_else(|| "missing".into());
                if actual != want {
                    failures.push(format!("action {field} = {actual:?}, want {want:?}"));
                }
            }
            other => failures.push(format!("unknown selector {other:?}")),
        }
    }
    failures
}

fn count_files(directory: &std::path::Path) -> usize {
    let mut count = 0;
    let mut stack = vec![directory.to_path_buf()];
    while let Some(current) = stack.pop() {
        let Ok(entries) = std::fs::read_dir(&current) else {
            continue;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                stack.push(path);
            } else {
                count += 1;
            }
        }
    }
    count
}

fn parse() -> Result<Options, String> {
    let mut args = std::env::args().skip(1);
    let mut options = Options {
        data_dir: PathBuf::from(".tmp/test/r2/default"),
        state_dir: PathBuf::from(".tmp/test/r2/default-state"),
        action: "ping".into(),
        runtime: "pi".into(),
        model: "auto/gpt-5.6-sol".into(),
        prompt: "Reply with exactly: ok".into(),
        timeout: 120,
        turns: 1,
        abort_after_ms: 300,
        keep_last: 2,
        cwd: None,
        attach: Vec::new(),
        approve: None,
        allow_tools: false,
        tools: Vec::new(),
        with_echo: false,
        session_file: None,
        expect: Vec::new(),
        list_runtime: None,
    };
    while let Some(argument) = args.next() {
        let mut value = || {
            args.next()
                .ok_or_else(|| format!("{argument} needs a value"))
        };
        match argument.as_str() {
            "--data-dir" => options.data_dir = PathBuf::from(value()?),
            "--state-dir" => options.state_dir = PathBuf::from(value()?),
            "--action" => options.action = value()?,
            "--runtime" => options.runtime = value()?,
            "--model" => options.model = value()?,
            "--prompt" => options.prompt = value()?,
            "--timeout" => {
                options.timeout = value()?
                    .parse()
                    .map_err(|_| "--timeout needs seconds".to_string())?
            }
            "--turns" => {
                options.turns = value()?
                    .parse()
                    .map_err(|_| "--turns needs a count".to_string())?
            }
            "--abort-after-ms" => {
                options.abort_after_ms = value()?
                    .parse()
                    .map_err(|_| "--abort-after-ms needs milliseconds".to_string())?
            }
            "--keep-last" => {
                options.keep_last = value()?
                    .parse()
                    .map_err(|_| "--keep-last needs a message count".to_string())?
            }
            "--cwd" => options.cwd = Some(value()?),
            "--attach" => {
                // `kind:path`, e.g. `file:docs/notes.md` or `dir:src`.
                let raw = value()?;
                let (kind, path) = raw
                    .split_once(':')
                    .ok_or_else(|| format!("--attach needs <kind>:<path>, got {raw:?}"))?;
                options.attach.push((kind.to_string(), path.to_string()));
            }
            "--approve" => options.approve = Some(value()?),
            "--allow-tools" => options.allow_tools = true,
            "--tools" => {
                options.tools = value()?
                    .split(',')
                    .map(str::trim)
                    .filter(|name| !name.is_empty())
                    .map(str::to_string)
                    .collect();
            }
            "--with-echo" => options.with_echo = true,
            "--session-file" => options.session_file = Some(PathBuf::from(value()?)),
            "--expect" => options.expect.push(value()?),
            "--list-runtime" => options.list_runtime = Some(value()?),
            other => return Err(format!("unknown argument {other:?}")),
        }
    }
    Ok(options)
}
