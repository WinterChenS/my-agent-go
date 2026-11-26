//! Schedules: commands, the tick, and the bridge route an agent uses.
//!
//! The tab is only half of this feature. A schedule that can only be created by
//! filling a form is a setting; the point of a scheduled task is that the agent
//! can be *told* to make one ("每天 9 点帮我把站会纪要汇总一下"), so the loopback
//! bridge exposes the same create path to the runtime's shell, and every session
//! is told that command exists.
//!
//! The tick is one thread per app. It reads due rows, claims each scheduled
//! instant exactly once (the occurrence key is unique), opens a real session for
//! the schedule's agent, sends the prompt, and records the outcome — including
//! the session and run ids, so a fire is traceable back to its conversation.

use std::sync::Arc;
use std::time::{Duration, Instant};

use anvil_session::schedule::{
    self, OccurrenceOutcome, ScheduleDraft, ScheduleTarget, ScheduleView,
};
use serde::Serialize;
use serde_json::{Value, json};
use tauri::{AppHandle, Emitter, Manager, State};

use crate::session::SessionState;

/// Emitted after a fire so an open page can re-read the ledger.
pub const CHANGED_EVENT: &str = "anvil://schedules-changed";

/// How often the tick looks for work. Fifteen seconds keeps a 09:00 schedule
/// within one glance of 09:00 without a timer per row.
const TICK: Duration = Duration::from_secs(15);

#[tauri::command]
pub async fn schedule_overview(
    state: State<'_, SessionState>,
    utc_offset_minutes: Option<i64>,
) -> Result<ScheduleView, String> {
    let offset = utc_offset_minutes.unwrap_or(0).clamp(-840, 840);
    state.with_reader(|store| {
        schedule::view(store.raw(), offset).map_err(|error| error.to_wire().to_string())
    })
}

#[tauri::command]
pub async fn schedule_save(
    state: State<'_, SessionState>,
    draft: ScheduleDraft,
) -> Result<anvil_session::schedule::ScheduleRecord, String> {
    let target = resolve_target(&state, &draft.agent_id)?;
    state.with_reader(|store| {
        schedule::save(store.raw(), &draft, &target).map_err(|error| error.to_wire().to_string())
    })
}

#[tauri::command]
pub async fn schedule_delete(state: State<'_, SessionState>, id: String) -> Result<(), String> {
    state.with_reader(|store| {
        schedule::remove(store.raw(), &id).map_err(|error| error.to_wire().to_string())
    })
}

#[tauri::command]
pub async fn schedule_toggle(
    state: State<'_, SessionState>,
    id: String,
    enabled: bool,
) -> Result<anvil_session::schedule::ScheduleRecord, String> {
    state.with_reader(|store| {
        schedule::set_enabled(store.raw(), &id, enabled)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// What one manual fire produced.
#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RunNowReport {
    pub session_id: String,
    pub run_id: String,
    pub status: String,
    pub error: Option<String>,
}

/// Fire one schedule now, ignoring its clock.
///
/// This is the button a user presses to check a prompt before trusting it at
/// 09:00; it records an occurrence like any other fire, so the detail panel and
/// the success rate stay truthful.
#[tauri::command]
pub async fn schedule_run_now(app: AppHandle, id: String) -> Result<RunNowReport, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SessionState>();
        let record = state
            .with_reader(|store| {
                schedule::list(store.raw())
                    .map_err(|error| error.to_wire().to_string())
                    .map(|rows| rows.into_iter().find(|row| row.id == id))
            })?
            .ok_or_else(|| format!("schedule {id} does not exist"))?;
        let scheduled_at = anvil_session::clock::now_rfc3339();
        let claimed = state.with_reader(|store| {
            schedule::claim(store.raw(), &record, &scheduled_at)
                .map_err(|error| error.to_wire().to_string())
        })?;
        if !claimed {
            return Err("this schedule is already running".to_string());
        }
        let outcome = run_one(&state, &record);
        let report = RunNowReport {
            session_id: outcome.session_id.clone(),
            run_id: outcome.run_id.clone(),
            status: outcome.state.clone(),
            error: (!outcome.reason.is_empty()).then(|| outcome.reason.clone()),
        };
        let _ = state.with_reader(|store| {
            schedule::finish(store.raw(), &record.id, &scheduled_at, &outcome)
                .map_err(|error| error.to_wire().to_string())
        });
        let _ = app.emit(CHANGED_EVENT, json!({ "scheduleId": record.id }));
        Ok(report)
    })
    .await
    .map_err(|error| format!("schedule: the run task ended unexpectedly: {error}"))?
}

/// Start the tick. Called once from the app's setup on a dedicated thread, so a
/// long turn in the scheduler cannot block a window command.
pub fn spawn(app: AppHandle) {
    let app = Arc::new(app);
    let _ = std::thread::Builder::new()
        .name("anvil-scheduler".into())
        .spawn(move || {
            // A run left `running` by the previous process has no outcome.
            if let Some(state) = app.try_state::<SessionState>() {
                let _ = state.with_reader(|store| {
                    schedule::reset_interrupted(store.raw())
                        .map_err(|error| error.to_wire().to_string())
                });
            }
            loop {
                std::thread::sleep(TICK);
                tick(&app);
            }
        });
}

fn tick(app: &AppHandle) {
    let Some(state) = app.try_state::<SessionState>() else {
        return;
    };
    let now = anvil_session::clock::now_rfc3339();
    let due = match state.with_reader(|store| {
        schedule::due(store.raw(), &now).map_err(|error| error.to_wire().to_string())
    }) {
        Ok(rows) => rows,
        Err(error) => {
            eprintln!("anvil: scheduler: {error}");
            return;
        }
    };
    for record in due {
        let scheduled_at = record.next_run_at.clone();
        let claimed = state
            .with_reader(|store| {
                schedule::claim(store.raw(), &record, &scheduled_at)
                    .map_err(|error| error.to_wire().to_string())
            })
            .unwrap_or(false);
        if !claimed {
            continue;
        }
        // One thread per fire: a turn can take minutes, and the next schedule
        // must not wait behind it for its claim.
        let app = app.clone();
        let _ = std::thread::Builder::new()
            .name(format!("anvil-schedule-{}", record.id))
            .spawn(move || {
                let state = app.state::<SessionState>();
                let outcome = run_one(&state, &record);
                let failed = outcome.state != "success";
                if let Err(error) = state.with_reader(|store| {
                    schedule::finish(store.raw(), &record.id, &scheduled_at, &outcome)
                        .map_err(|error| error.to_wire().to_string())
                }) {
                    eprintln!("anvil: scheduler: record {}: {error}", record.id);
                }
                if failed {
                    eprintln!(
                        "anvil: scheduler: {} ended {}: {}",
                        record.name, outcome.state, outcome.reason
                    );
                }
                let _ = app.emit(CHANGED_EVENT, json!({ "scheduleId": record.id }));
            });
    }
}

/// Open the schedule's agent and send its prompt, mapping the run's ending onto
/// the occurrence states the page knows.
fn run_one(state: &SessionState, record: &schedule::ScheduleRecord) -> OccurrenceOutcome {
    let started = Instant::now();
    let finish =
        |state: &str, reason: String, session_id: String, run_id: String| OccurrenceOutcome {
            state: state.to_string(),
            reason,
            session_id,
            run_id,
            duration_ms: u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX),
        };
    let session = match state.open_scheduled(&record.agent_id, &record.id, &record.name) {
        Ok(session) => session,
        Err(error) => return finish("failed", error, String::new(), String::new()),
    };
    match state.send(
        &session.id,
        &record.prompt,
        Vec::new(),
        Some(record.permission.clone()),
        None,
    ) {
        Ok(report) => {
            let run_status = report.status.as_str();
            let (state_name, reason) = match run_status {
                "completed" => ("success", String::new()),
                "aborted" => ("skipped", "the run was aborted".to_string()),
                _ => (
                    "failed",
                    report
                        .error
                        .clone()
                        .unwrap_or_else(|| run_status.to_string()),
                ),
            };
            finish(
                state_name,
                reason,
                session.id.clone(),
                report.run_id.clone(),
            )
        }
        Err(error) => finish("failed", error, session.id.clone(), String::new()),
    }
}

/// The persona a saved agent runs as; a schedule must point at something real.
fn resolve_target(state: &SessionState, agent_id: &str) -> Result<ScheduleTarget, String> {
    let (name, runtime_id, model) = state.agent_target(agent_id)?;
    Ok(ScheduleTarget {
        agent_id: agent_id.to_string(),
        agent_name: name,
        runtime_id,
        model,
    })
}

// ---- the bridge route -----------------------------------------------------

/// Body of `POST /schedule`.
///
/// Everything is optional except `name`, `cron` and `prompt`, because the caller
/// is a model writing JSON from a sentence: a missing agent falls back to the
/// machine's default, and a missing offset falls back to the caller's own clock
/// (the CLI always sends it).
pub fn create_from_bridge(app: &AppHandle, body: Value) -> Value {
    let Some(state) = app.try_state::<SessionState>() else {
        return json!({ "ok": false, "error": "the session service is not ready" });
    };
    let text = |key: &str| {
        body.get(key)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .trim()
            .to_string()
    };
    let agent_id = match resolve_agent(&state, &text("agentId"), &text("agent")) {
        Ok(id) => id,
        Err(error) => return json!({ "ok": false, "error": error }),
    };
    let draft = ScheduleDraft {
        id: None,
        name: text("name"),
        cron: text("cron"),
        time_zone: text("timeZone"),
        utc_offset_minutes: body
            .get("utcOffsetMinutes")
            .and_then(Value::as_i64)
            .unwrap_or(0)
            .clamp(-840, 840),
        agent_id,
        prompt: text("prompt"),
        permission: {
            let value = text("permission");
            if value.is_empty() {
                "medium".to_string()
            } else {
                value
            }
        },
        output_target: text("output"),
        enabled: body.get("enabled").and_then(Value::as_bool).unwrap_or(true),
    };
    let target = match resolve_target(&state, &draft.agent_id) {
        Ok(target) => target,
        Err(error) => return json!({ "ok": false, "error": error }),
    };
    match state.with_reader(|store| {
        schedule::save(store.raw(), &draft, &target).map_err(|error| error.to_wire().to_string())
    }) {
        Ok(record) => {
            let _ = app.emit(CHANGED_EVENT, json!({ "scheduleId": record.id }));
            json!({ "ok": true, "schedule": record })
        }
        Err(error) => json!({ "ok": false, "error": error }),
    }
}

/// Body of `GET /schedule`: the rows, so an agent can check before adding one.
pub fn list_from_bridge(app: &AppHandle) -> Value {
    let Some(state) = app.try_state::<SessionState>() else {
        return json!({ "ok": false, "error": "the session service is not ready" });
    };
    match state.with_reader(|store| {
        schedule::list(store.raw()).map_err(|error| error.to_wire().to_string())
    }) {
        Ok(rows) => json!({ "ok": true, "schedules": rows }),
        Err(error) => json!({ "ok": false, "error": error }),
    }
}

/// Body of `POST /schedule/toggle` and `/schedule/remove`.
pub fn mutate_from_bridge(app: &AppHandle, action: &str, body: Value) -> Value {
    let Some(state) = app.try_state::<SessionState>() else {
        return json!({ "ok": false, "error": "the session service is not ready" });
    };
    let id = body
        .get("id")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    if id.trim().is_empty() {
        return json!({ "ok": false, "error": "id is required" });
    }
    let result = state.with_reader(|store| {
        match action {
            "toggle" => {
                let enabled = body.get("enabled").and_then(Value::as_bool).unwrap_or(true);
                schedule::set_enabled(store.raw(), &id, enabled)
                    .map(|record| json!({ "ok": true, "schedule": record }))
            }
            _ => schedule::remove(store.raw(), &id).map(|()| json!({ "ok": true })),
        }
        .map_err(|error| error.to_wire().to_string())
    });
    match result {
        Ok(value) => {
            let _ = app.emit(CHANGED_EVENT, json!({ "scheduleId": id }));
            value
        }
        Err(error) => json!({ "ok": false, "error": error }),
    }
}

/// Resolve the caller's agent: explicit id, then name, then the default.
fn resolve_agent(state: &SessionState, agent_id: &str, name: &str) -> Result<String, String> {
    let path = state.catalog_database();
    if !path.exists() {
        return Err("the catalog is empty: no agent to run this schedule".to_string());
    }
    let store = anvil_catalog::Store::open(path).map_err(|error| format!("catalog: {error}"))?;
    let agents = store
        .agents()
        .map_err(|error| format!("catalog: {error}"))?;
    if !agent_id.trim().is_empty() {
        if agents.iter().any(|agent| agent.id == agent_id) {
            return Ok(agent_id.to_string());
        }
        return Err(format!("agent {agent_id} is not in the catalog"));
    }
    if !name.trim().is_empty() {
        if let Some(agent) = agents
            .iter()
            .find(|agent| agent.name.eq_ignore_ascii_case(name.trim()))
        {
            return Ok(agent.id.clone());
        }
    }
    agents
        .iter()
        .find(|agent| agent.is_default && agent.enabled)
        .or_else(|| agents.iter().find(|agent| agent.enabled))
        .map(|agent| agent.id.clone())
        .ok_or_else(|| "no enabled agent to run this schedule".to_string())
}
