//! The workbench overview command.
//!
//! One call joins the three things the home page is made of: the session
//! ledger (counts, activity, conversations), the persona catalog (agents,
//! runtimes, MCP) and the files a session actually wrote inside its project.
//! Nothing is estimated. A family with no source in this build — the scheduler,
//! connectors, quota — is reported as unavailable so the UI can say so.

use std::collections::BTreeMap;
use std::path::Path;
use std::time::UNIX_EPOCH;

use anvil_session::workbench::{
    WorkbenchAgent, WorkbenchArtifact, WorkbenchInput, WorkbenchOverview, WorkbenchSchedule,
    WorkbenchSources, WorkbenchStatus,
};
use anvil_session::{EventKind, RunEvent};
use rusqlite::{Connection, params};
use tauri::State;

use crate::session::SessionState;

/// What the React page asks for.
///
/// `utcOffsetMinutes` is `-new Date().getTimezoneOffset()`: the ledger writes
/// UTC and the heatmap is a local calendar, so the host needs the viewer's
/// offset to put a UTC evening run on the right day.
#[tauri::command]
pub async fn workbench_overview(
    state: State<'_, SessionState>,
    weeks: Option<u32>,
    utc_offset_minutes: Option<i64>,
) -> Result<WorkbenchOverview, String> {
    let offset = utc_offset_minutes.unwrap_or(0).clamp(-840, 840);
    let today = anvil_session::clock::local_today(offset);
    let weeks = weeks.unwrap_or(53).clamp(1, 53);
    // Opening a missing path would create an empty catalog and make the page
    // claim it had read one. Only an existing database counts as a source.
    let catalog = state
        .catalog_database()
        .exists()
        .then(|| anvil_catalog::Store::open(state.catalog_database()).ok())
        .flatten();

    state.with_reader(|store| {
        let connection = store.raw();
        let (agents, mut status, catalog_ok) = catalog
            .as_ref()
            .map(|catalog| read_catalog(catalog, connection))
            .unwrap_or_else(|| (Vec::new(), WorkbenchStatus::default(), false));
        let (artifacts, filesystem_ok) = recent_artifacts(connection);
        // Schedules are rows in this same ledger, so the home page can show the
        // next fire instead of "no scheduler in this build".
        let schedule_view = anvil_session::schedule::view(connection, offset)
            .map_err(|error| error.to_wire().to_string())?;
        let schedules: Vec<WorkbenchSchedule> = schedule_view
            .schedules
            .iter()
            .map(|row| WorkbenchSchedule {
                id: row.id.clone(),
                name: row.name.clone(),
                agent: row.agent_name.clone(),
                next_run_at: row.next_run_at.clone(),
                last_run_at: row.last_run_at.clone(),
                last_status: row.last_status.clone(),
                enabled: row.enabled,
            })
            .collect();
        status.schedules_enabled = schedule_view.stats.enabled;
        status.schedules_today = schedule_view.stats.today_runs;
        status.connectors_available = catalog_ok;
        let input = WorkbenchInput {
            today,
            utc_offset_minutes: offset,
            weeks,
            agents,
            artifacts,
            schedules,
            status,
            sources: WorkbenchSources {
                ledger: true,
                catalog: catalog_ok,
                filesystem: filesystem_ok,
                schedules: true,
                // Connectors are the catalog's MCP rows; without a catalog there
                // is nothing to count.
                connectors: catalog_ok,
                // No quota service exists in this build.
                quota: false,
            },
        };
        anvil_session::workbench::report(connection, &input)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Agents and the machine's runtime/MCP state.
///
/// A broken catalog does not hide the conversations: the caller degrades to
/// `catalog_ok = false` and the page marks those rows as unavailable.
fn read_catalog(
    catalog: &anvil_catalog::Store,
    connection: &Connection,
) -> (Vec<WorkbenchAgent>, WorkbenchStatus, bool) {
    let agents = match catalog.agents() {
        Ok(agents) => agents,
        Err(error) => {
            eprintln!("anvil: workbench: catalog agents: {error}");
            return (Vec::new(), WorkbenchStatus::default(), false);
        }
    };
    let mut rows: Vec<WorkbenchAgent> = agents
        .iter()
        .map(|agent| WorkbenchAgent {
            id: agent.id.clone(),
            name: agent.name.clone(),
            initial: agent
                .name
                .chars()
                .next()
                .map(String::from)
                .unwrap_or_default(),
            model: agent.model_id.clone(),
            runtime_id: agent.runtime_id.clone(),
            skills: agent.skills.len() as u64,
            runs: connection
                .query_row(
                    "SELECT COUNT(*) FROM sessions WHERE agent_id = ?1",
                    params![agent.id],
                    |row| row.get::<_, i64>(0),
                )
                .unwrap_or(0)
                .max(0) as u64,
            enabled: agent.enabled,
            is_default: agent.is_default,
        })
        .collect();
    rows.sort_by(|left, right| {
        right
            .is_default
            .cmp(&left.is_default)
            .then_with(|| right.runs.cmp(&left.runs))
            .then_with(|| left.name.cmp(&right.name))
    });
    rows.truncate(4);

    let runtimes = catalog.runtimes().unwrap_or_default();
    let mcp = catalog.mcp().unwrap_or_default();
    let runtime_ready = runtimes
        .iter()
        .filter(|runtime| runtime.status == "available")
        .count() as u64;
    let runtime_label = runtimes
        .iter()
        .find(|runtime| runtime.status == "available")
        .map(|runtime| {
            let version = runtime.version.as_deref().unwrap_or("").trim();
            if version.is_empty() {
                runtime.display_name.clone()
            } else {
                format!("{} {version}", runtime.display_name)
            }
        })
        .unwrap_or_default();
    let status = WorkbenchStatus {
        runtime_ready,
        runtime_total: runtimes.len() as u64,
        runtime_label,
        mcp_connected: mcp.iter().filter(|row| row.state == "connected").count() as u64,
        mcp_total: mcp.len() as u64,
        connectors_available: false,
        quota_available: false,
        ..WorkbenchStatus::default()
    };
    (rows, status, true)
}

/// Files recent sessions wrote, using the right rail's exact rules.
///
/// Only `tool_call` events of the last few sessions are examined, every path is
/// canonicalised and confined to that session's project, and scratch folders
/// are excluded — the same `rail::artifact_files` the chat pane uses. A file
/// touched by two sessions appears once, with its newest modification time.
fn recent_artifacts(connection: &Connection) -> (Vec<WorkbenchArtifact>, bool) {
    let mut found: BTreeMap<String, WorkbenchArtifact> = BTreeMap::new();
    let sessions: Vec<(String, String, String)> = match connection
        .prepare(
            "SELECT id, title, project_path FROM sessions
             WHERE archived = 0 ORDER BY updated_at DESC LIMIT 12",
        )
        .and_then(|mut statement| {
            let rows = statement.query_map([], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                ))
            })?;
            rows.collect::<std::result::Result<Vec<_>, _>>()
        }) {
        Ok(rows) => rows,
        Err(error) => {
            eprintln!("anvil: workbench: recent sessions: {error}");
            return (Vec::new(), false);
        }
    };

    for (session_id, title, project_path) in sessions {
        let events: Vec<RunEvent> = match connection
            .prepare(
                "SELECT run_id, seq, at, payload FROM events
                 WHERE session_id = ?1 AND kind = 'tool_call'
                 ORDER BY seq DESC LIMIT 200",
            )
            .and_then(|mut statement| {
                let rows = statement.query_map(params![session_id], |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, i64>(1)?,
                        row.get::<_, String>(2)?,
                        row.get::<_, String>(3)?,
                    ))
                })?;
                let collected: rusqlite::Result<Vec<(String, i64, String, String)>> =
                    rows.collect();
                collected
            }) {
            Ok(rows) => rows
                .into_iter()
                .map(|(run_id, seq, at, payload)| RunEvent {
                    session_id: session_id.clone(),
                    run_id,
                    seq,
                    kind: EventKind::ToolCall,
                    at,
                    payload: serde_json::from_str(&payload).unwrap_or_default(),
                })
                .collect(),
            Err(error) => {
                eprintln!("anvil: workbench: tool calls: {error}");
                continue;
            }
        };
        for artifact in anvil_session::rail::artifact_files(&project_path, &events) {
            let path = Path::new(&artifact.path);
            let at = file_stamp(path);
            let name = path
                .file_name()
                .map(|name| name.to_string_lossy().to_string())
                .unwrap_or_else(|| artifact.relative.clone());
            let row = WorkbenchArtifact {
                name,
                relative: artifact.relative.clone(),
                path: artifact.path.clone(),
                ext: artifact.ext.clone(),
                bytes: artifact.bytes,
                session: title.clone(),
                session_id: session_id.clone(),
                at,
            };
            let key = artifact.path.to_lowercase();
            match found.get(&key) {
                Some(previous) if previous.at >= row.at => {}
                _ => {
                    found.insert(key, row);
                }
            }
        }
    }
    let mut rows: Vec<WorkbenchArtifact> = found.into_values().collect();
    rows.sort_by(|left, right| {
        right
            .at
            .cmp(&left.at)
            .then_with(|| left.name.cmp(&right.name))
    });
    rows.truncate(5);
    (rows, true)
}

fn file_stamp(path: &Path) -> String {
    std::fs::metadata(path)
        .ok()
        .and_then(|meta| meta.modified().ok())
        .and_then(|time| time.duration_since(UNIX_EPOCH).ok())
        .map(|duration| {
            anvil_session::clock::rfc3339(i64::try_from(duration.as_secs()).unwrap_or(i64::MAX))
        })
        .unwrap_or_default()
}
