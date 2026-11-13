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
//! Exit codes follow the acceptance contract: 0 pass, 1 an expectation failed
//! (the report is still printed), 2 the input or the environment is incomplete.

use std::path::PathBuf;
use std::process::ExitCode;
use std::time::Duration;

use anvil_session::{
    EventKind, Registry, SessionContext, SessionService, SessionSpec, Store, default_registry,
};

struct Options {
    data_dir: PathBuf,
    state_dir: PathBuf,
    action: String,
    runtime: String,
    model: String,
    prompt: String,
    timeout: u64,
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
        run_timeout: Duration::from_secs(options.timeout),
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
        "list" => {
            let page = service
                .page(None, true, None, 50, None)
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
        allow_tools: false,
    };
    let session = service.open(&spec).map_err(|error| error.to_string())?;
    let outcome = service
        .send(&session.id, &options.prompt)
        .map_err(|error| error.to_string())?;

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

    let usage = events
        .iter()
        .find(|event| event.kind == EventKind::Usage)
        .map(|event| event.payload.clone())
        .unwrap_or(serde_json::Value::Null);

    Ok(serde_json::json!({
        "ok": outcome.run.status == anvil_session::RunStatus::Completed,
        "status": outcome.run.status.as_str(),
        "sessionId": session.id,
        "nativeId": session.native_id,
        "model": session.model,
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

fn registry_for(options: &Options) -> Result<Registry, String> {
    // `--list-runtime` narrows the registry to one adapter, so a case can prove
    // failure handling for a runtime that is absent on this machine.
    match options.list_runtime.as_deref() {
        None => Ok(default_registry()),
        Some("none") => Ok(Registry::new()),
        Some(_) => Ok(default_registry()),
    }
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
            "deltas-min" | "events-min" | "messages-min" => {
                let field = match selector {
                    "deltas-min" => "/action/deltas",
                    "events-min" => "/action/eventCount",
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
            "--expect" => options.expect.push(value()?),
            "--list-runtime" => options.list_runtime = Some(value()?),
            other => return Err(format!("unknown argument {other:?}")),
        }
    }
    Ok(options)
}
