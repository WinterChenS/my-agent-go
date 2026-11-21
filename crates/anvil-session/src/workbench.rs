//! The workbench overview: every number on the home page, derived from the
//! ledger.
//!
//! The prototype seeds its heatmap from a hash of the date. This module is the
//! opposite: a day is busy only if a run actually finished or a tool actually
//! returned that day, and a token count exists only if a runtime reported one.
//! Dates are the viewer's local dates — the ledger writes UTC, so the host
//! passes `Date#getTimezoneOffset` negated and all bucketing happens here.
//!
//! The catalog and the filesystem are read by the host and passed in as seeds:
//! this crate must stay usable without a runtime inventory or a workspace.

use std::collections::{BTreeMap, HashMap};

use rusqlite::{Connection, params};
use serde::{Deserialize, Serialize};

use crate::audit::{is_thread_snapshot, usage_of};
use crate::clock::{days_from_civil, local_date, parse_ymd, shift_date, weekday};
use crate::error::Result;

/// What the host read from the catalog, the project folders and the ledger's
/// session index. Keeping these as plain seeds makes `report` testable with an
/// in-memory database alone.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchInput {
    pub today: String,
    pub utc_offset_minutes: i64,
    pub weeks: u32,
    pub agents: Vec<WorkbenchAgent>,
    pub artifacts: Vec<WorkbenchArtifact>,
    /// Schedules the host read from the ledger; empty when none exist.
    pub schedules: Vec<WorkbenchSchedule>,
    pub status: WorkbenchStatus,
    pub sources: WorkbenchSources,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchOverview {
    pub today: String,
    pub weeks: u32,
    /// First local date covered by the heatmap grid.
    pub grid_start: String,
    pub grid_end: String,
    pub stats: WorkbenchStats,
    pub activity: WorkbenchActivity,
    pub chats: Vec<WorkbenchChat>,
    pub artifacts: Vec<WorkbenchArtifact>,
    pub agents: Vec<WorkbenchAgent>,
    pub schedules: Vec<WorkbenchSchedule>,
    pub status: WorkbenchStatus,
    /// Which parts of this page have a real source. The UI says "not connected"
    /// rather than printing a convincing zero for the ones that do not.
    pub sources: WorkbenchSources,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchStats {
    pub sessions_this_week: u64,
    /// This week minus the same number of days last week.
    pub sessions_delta: i64,
    pub active_sessions: u64,
    pub tool_calls: u64,
    pub tool_results: u64,
    pub tool_failures: u64,
    /// `null` when no tool returned an outcome; never a guessed 100%.
    pub tool_success_rate: Option<f64>,
    pub tokens: u64,
    pub tokens_input: u64,
    pub tokens_output: u64,
    /// False when the runtimes reported no token counter at all.
    pub tokens_reported: bool,
    pub pending_approvals: u64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ActivityCell {
    pub date: String,
    pub count: u64,
    pub runs: u64,
    pub tools: u64,
    pub future: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchActivity {
    pub cells: Vec<ActivityCell>,
    pub total: u64,
    pub active_days: u64,
    pub longest_streak: u64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchChat {
    pub id: String,
    pub title: String,
    pub agent: String,
    pub model: String,
    pub updated_at: String,
    pub turns: u64,
    pub messages: u64,
    pub pending: u64,
    pub status: String,
    pub preview: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchArtifact {
    pub name: String,
    pub relative: String,
    pub path: String,
    pub ext: String,
    pub bytes: u64,
    pub session: String,
    pub session_id: String,
    /// File mtime, not the event time: a file edited later is a later output.
    pub at: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchAgent {
    pub id: String,
    pub name: String,
    pub initial: String,
    pub model: String,
    pub runtime_id: String,
    pub skills: u64,
    pub runs: u64,
    pub enabled: bool,
    pub is_default: bool,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchSchedule {
    pub id: String,
    pub name: String,
    pub agent: String,
    pub next_run_at: String,
    pub last_run_at: String,
    pub last_status: String,
    pub enabled: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchStatus {
    pub runtime_ready: u64,
    pub runtime_total: u64,
    pub runtime_label: String,
    pub mcp_connected: u64,
    pub mcp_total: u64,
    pub connectors_available: bool,
    pub quota_available: bool,
    /// Enabled schedules and how many fired today, when a scheduler exists.
    pub schedules_enabled: u64,
    pub schedules_today: u64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkbenchSources {
    pub ledger: bool,
    pub catalog: bool,
    pub filesystem: bool,
    /// There is no scheduler, no connector registry and no quota service in
    /// this build. Those cards must say so instead of showing a fixture.
    pub schedules: bool,
    pub connectors: bool,
    pub quota: bool,
}

#[derive(Default)]
struct DayBucket {
    runs: u64,
    tools: u64,
    tool_calls: u64,
    tool_failures: u64,
    tokens_input: u64,
    tokens_output: u64,
}

impl DayBucket {
    fn completed(&self) -> u64 {
        self.runs + self.tools
    }
}

struct SessionRow {
    id: String,
    title: String,
    status: String,
    model: String,
    agent_id: String,
    created_at: String,
    updated_at: String,
    preview: String,
    messages: u64,
    turns: u64,
}

/// Build the whole overview from committed ledger rows.
pub fn report(connection: &Connection, input: &WorkbenchInput) -> Result<WorkbenchOverview> {
    let today = input.today.clone();
    let offset = input.utc_offset_minutes;
    let weeks = input.weeks.clamp(1, 53);
    let weekday = weekday(&today).unwrap_or(0);
    // The prototype's grid ends on Saturday of the current week. The backend
    // owns the grid so React and Rust cannot disagree about which day a cell is.
    let grid_end = shift_date(&today, 6 - weekday).unwrap_or_else(|| today.clone());
    let grid_start =
        shift_date(&grid_end, -(weeks as i64 * 7 - 1)).unwrap_or_else(|| today.clone());
    // Monday-based week, matching a Chinese work week.
    let week_start =
        shift_date(&today, -(weekday + 6).rem_euclid(7)).unwrap_or_else(|| today.clone());
    let day_index = |date: &str| -> i64 {
        parse_ymd(date)
            .map(|(year, month, day)| days_from_civil(year, month, day))
            .unwrap_or(0)
    };
    let elapsed = (day_index(&today) - day_index(&week_start) + 1).max(1);
    let prev_start = shift_date(&week_start, -7).unwrap_or_else(|| week_start.clone());
    let prev_end = shift_date(&prev_start, elapsed - 1).unwrap_or_else(|| prev_start.clone());
    let approval_since = shift_date(&today, -29).unwrap_or_else(|| today.clone());
    let in_week = |date: &str| date >= week_start.as_str() && date <= today.as_str();

    // ---- sessions --------------------------------------------------------
    let mut sessions: Vec<SessionRow> = Vec::new();
    {
        let mut statement = connection.prepare(
            "SELECT s.id, s.title, s.status, s.model, s.agent_id, s.created_at,
                    s.updated_at, s.preview,
                    (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id),
                    (SELECT COUNT(*) FROM runs r WHERE r.session_id = s.id)
             FROM sessions s WHERE s.archived = 0
             ORDER BY s.pinned DESC, s.updated_at DESC",
        )?;
        let rows = statement.query_map([], |row| {
            Ok(SessionRow {
                id: row.get(0)?,
                title: row.get(1)?,
                status: row.get(2)?,
                model: row.get(3)?,
                agent_id: row.get(4)?,
                created_at: row.get(5)?,
                updated_at: row.get(6)?,
                preview: row.get(7)?,
                messages: row.get::<_, i64>(8)?.max(0) as u64,
                turns: row.get::<_, i64>(9)?.max(0) as u64,
            })
        })?;
        for row in rows {
            sessions.push(row?);
        }
    }
    let sessions_this_week = sessions
        .iter()
        .filter(|row| {
            local_date(&row.created_at, offset)
                .map(|date| date >= week_start && date <= today)
                .unwrap_or(false)
        })
        .count() as u64;
    let sessions_last_week = sessions
        .iter()
        .filter(|row| {
            local_date(&row.created_at, offset)
                .map(|date| date >= prev_start && date <= prev_end)
                .unwrap_or(false)
        })
        .count() as u64;
    let active_sessions = sessions
        .iter()
        .filter(|row| row.status == "running")
        .count() as u64;

    // ---- events ----------------------------------------------------------
    // UTC bounds are deliberately two days wider than the local grid; the exact
    // local date is decided per row with `local_date`.
    let lower_utc = shift_date(&grid_start, -2).unwrap_or_else(|| grid_start.clone());
    let upper_utc = shift_date(&today, 2).unwrap_or_else(|| today.clone());
    let mut days: BTreeMap<String, DayBucket> = BTreeMap::new();
    let mut approvals: HashMap<String, (u64, u64)> = HashMap::new();
    // A thread-level usage snapshot is a running total, not a new measurement.
    let mut snapshots: HashMap<String, (i64, u64, u64, String)> = HashMap::new();
    let mut tokens_reported = false;
    {
        let mut statement = connection.prepare(
            "SELECT e.session_id, e.run_id, e.seq, e.kind, e.at, e.payload
             FROM events e
             WHERE e.kind IN ('usage', 'tool_call', 'tool_result',
                              'approval_request', 'approval_resolved')
               AND substr(e.at, 1, 10) BETWEEN ?1 AND ?2
             ORDER BY e.seq",
        )?;
        let rows = statement.query_map(params![lower_utc, upper_utc], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, i64>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, String>(4)?,
                row.get::<_, String>(5)?,
            ))
        })?;
        for row in rows {
            let (session_id, run_id, seq, kind, at, payload) = row?;
            let Some(date) = local_date(&at, offset) else {
                continue;
            };
            if date < grid_start || date > grid_end {
                continue;
            }
            let value: serde_json::Value = serde_json::from_str(&payload).unwrap_or_default();
            if kind == "approval_request" || kind == "approval_resolved" {
                if date >= approval_since && date <= today {
                    let entry = approvals.entry(session_id).or_insert((0, 0));
                    if kind == "approval_request" {
                        entry.0 += 1;
                    } else {
                        entry.1 += 1;
                    }
                }
                continue;
            }
            let bucket = days.entry(date.clone()).or_default();
            match kind.as_str() {
                "tool_call" => bucket.tool_calls += 1,
                "tool_result" => {
                    // A result without an outcome still proves the call
                    // returned; only an explicit false is a failure.
                    if value.get("ok").and_then(serde_json::Value::as_bool) == Some(false) {
                        bucket.tool_failures += 1;
                    } else {
                        bucket.tools += 1;
                    }
                }
                "usage" => {
                    let Some(numbers) = usage_of(&value) else {
                        continue;
                    };
                    tokens_reported = true;
                    let key = format!("{session_id}:{run_id}");
                    if is_thread_snapshot(&value) {
                        match snapshots.entry(key) {
                            std::collections::hash_map::Entry::Occupied(mut entry) => {
                                if seq > entry.get().0 {
                                    entry.insert((
                                        seq,
                                        numbers.input,
                                        numbers.output,
                                        date.clone(),
                                    ));
                                }
                            }
                            std::collections::hash_map::Entry::Vacant(entry) => {
                                entry.insert((seq, numbers.input, numbers.output, date.clone()));
                            }
                        }
                        continue;
                    }
                    bucket.tokens_input += numbers.input;
                    bucket.tokens_output += numbers.output;
                }
                _ => {}
            }
        }
    }
    // Thread snapshots have no reliable day split, so they only contribute to
    // the week's totals, and only when their last snapshot arrived this week.
    let mut snapshot_input = 0u64;
    let mut snapshot_output = 0u64;
    for (_, (_, input_tokens, output_tokens, date)) in snapshots {
        if in_week(&date) {
            snapshot_input += input_tokens;
            snapshot_output += output_tokens;
        }
    }

    // ---- completed runs --------------------------------------------------
    {
        let mut statement = connection.prepare(
            "SELECT started_at, ended_at, status FROM runs
             WHERE substr(started_at, 1, 10) BETWEEN ?1 AND ?2",
        )?;
        let rows = statement.query_map(params![lower_utc, upper_utc], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, Option<String>>(1)?,
                row.get::<_, String>(2)?,
            ))
        })?;
        for row in rows {
            let (started, ended, status) = row?;
            if status != "completed" {
                continue;
            }
            let stamp = ended.as_deref().unwrap_or(&started);
            let Some(date) = local_date(stamp, offset) else {
                continue;
            };
            if date < grid_start || date > grid_end {
                continue;
            }
            days.entry(date).or_default().runs += 1;
        }
    }

    // ---- stats -----------------------------------------------------------
    let (mut tool_calls, mut tool_results, mut tool_failures) = (0u64, 0u64, 0u64);
    let (mut tokens_input, mut tokens_output) = (0u64, 0u64);
    for (date, bucket) in &days {
        if !in_week(date) {
            continue;
        }
        tool_calls += bucket.tool_calls;
        tool_results += bucket.tools;
        tool_failures += bucket.tool_failures;
        tokens_input += bucket.tokens_input;
        tokens_output += bucket.tokens_output;
    }
    tokens_input += snapshot_input;
    tokens_output += snapshot_output;
    let decided = tool_results + tool_failures;
    let tool_success_rate = (decided > 0).then(|| tool_results as f64 * 100.0 / decided as f64);
    let pending_approvals = approvals
        .values()
        .map(|(requests, resolved)| requests.saturating_sub(*resolved))
        .sum::<u64>();

    let stats = WorkbenchStats {
        sessions_this_week,
        sessions_delta: sessions_this_week as i64 - sessions_last_week as i64,
        active_sessions,
        tool_calls,
        tool_results,
        tool_failures,
        tool_success_rate,
        tokens: tokens_input + tokens_output,
        tokens_input,
        tokens_output,
        tokens_reported,
        pending_approvals,
    };

    // ---- heatmap grid ----------------------------------------------------
    let mut cells = Vec::with_capacity(weeks as usize * 7);
    let mut total = 0u64;
    let mut active_days = 0u64;
    let mut streak = 0u64;
    let mut longest_streak = 0u64;
    let mut cursor = grid_start.clone();
    for _ in 0..(weeks as i64 * 7) {
        let future = cursor > today;
        let bucket = days.get(&cursor);
        let count = if future {
            0
        } else {
            bucket.map_or(0, DayBucket::completed)
        };
        let runs = if future {
            0
        } else {
            bucket.map_or(0, |day| day.runs)
        };
        let tools = if future {
            0
        } else {
            bucket.map_or(0, |day| day.tools)
        };
        if !future {
            total += count;
            if count > 0 {
                active_days += 1;
                streak += 1;
                longest_streak = longest_streak.max(streak);
            } else {
                streak = 0;
            }
        }
        cells.push(ActivityCell {
            date: cursor.clone(),
            count,
            runs,
            tools,
            future,
        });
        cursor = shift_date(&cursor, 1).unwrap_or_else(|| today.clone());
    }
    let activity = WorkbenchActivity {
        cells,
        total,
        active_days,
        longest_streak,
    };

    // ---- recent conversations -------------------------------------------
    let agent_names: HashMap<&str, &str> = input
        .agents
        .iter()
        .map(|agent| (agent.id.as_str(), agent.name.as_str()))
        .collect();
    let chats = sessions
        .iter()
        .take(5)
        .map(|row| WorkbenchChat {
            id: row.id.clone(),
            title: if row.title.trim().is_empty() {
                row.preview.trim().chars().take(48).collect()
            } else {
                row.title.clone()
            },
            agent: agent_names
                .get(row.agent_id.as_str())
                .copied()
                .unwrap_or("")
                .to_string(),
            model: row.model.clone(),
            updated_at: row.updated_at.clone(),
            turns: row.turns,
            messages: row.messages,
            pending: approvals
                .get(&row.id)
                .map(|(requests, resolved)| requests.saturating_sub(*resolved))
                .unwrap_or(0),
            status: row.status.clone(),
            preview: row.preview.clone(),
        })
        .collect();

    Ok(WorkbenchOverview {
        today,
        weeks,
        grid_start,
        grid_end,
        stats,
        activity,
        chats,
        artifacts: input.artifacts.clone(),
        agents: input.agents.clone(),
        schedules: input.schedules.clone(),
        status: input.status.clone(),
        sources: input.sources.clone(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Store;

    fn seed(connection: &Connection, session: &str, created: &str, status: &str) {
        connection
            .execute(
                "INSERT INTO sessions (id, runtime_id, title, status, native_id, model,
                    project_path, parent_id, branch_at, allow_tools, tools, agent_id,
                    thinking, system_prompt, append_prompt, skills, pinned, archived,
                    created_at, updated_at, preview)
                 VALUES (?1, 'pi', 'Session', ?2, '', 'auto/test', '', '', 0, 1, '[]',
                    'agent-a', '', '', '', '[]', 0, 0, ?3, ?3, 'preview')",
                params![session, status, created],
            )
            .unwrap();
    }

    fn event(
        connection: &Connection,
        session: &str,
        run: &str,
        seq: i64,
        kind: &str,
        at: &str,
        payload: &str,
    ) {
        connection
            .execute(
                "INSERT INTO events (session_id, run_id, seq, kind, at, payload)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
                params![session, run, seq, kind, at, payload],
            )
            .unwrap();
    }

    fn input(today: &str, offset: i64) -> WorkbenchInput {
        WorkbenchInput {
            today: today.to_string(),
            utc_offset_minutes: offset,
            weeks: 12,
            sources: WorkbenchSources {
                ledger: true,
                ..Default::default()
            },
            ..Default::default()
        }
    }

    #[test]
    fn an_empty_ledger_is_an_empty_overview_not_a_fixture() {
        let store = Store::open_in_memory().unwrap();
        let view = report(store.raw(), &input("2026-11-21", 480)).unwrap();
        assert_eq!(view.stats.sessions_this_week, 0);
        assert_eq!(view.stats.tokens, 0);
        assert!(!view.stats.tokens_reported);
        assert_eq!(view.stats.tool_success_rate, None);
        assert_eq!(view.activity.total, 0);
        assert_eq!(view.activity.cells.len(), 84);
        assert!(view.chats.is_empty());
    }

    #[test]
    fn counts_real_runs_tools_tokens_and_approvals() {
        let store = Store::open_in_memory().unwrap();
        let connection = store.raw();
        // Saturday 2026-11-21, local UTC+8. Monday of that week is 11-16.
        seed(connection, "s1", "2026-11-20T02:00:00Z", "idle");
        seed(connection, "s2", "2026-11-10T02:00:00Z", "running");
        connection
            .execute(
                "INSERT INTO runs (id, session_id, status, started_at, ended_at, prompt)
                 VALUES ('r1', 's1', 'completed', '2026-11-20T03:00:00Z', '2026-11-20T03:01:00Z', '')",
                [],
            )
            .unwrap();
        event(
            connection,
            "s1",
            "r1",
            1,
            "tool_call",
            "2026-11-20T03:00:10Z",
            r#"{"tool":"write","args":{"path":"out.md"}}"#,
        );
        event(
            connection,
            "s1",
            "r1",
            2,
            "tool_result",
            "2026-11-20T03:00:11Z",
            r#"{"ok":true}"#,
        );
        event(
            connection,
            "s1",
            "r1",
            3,
            "tool_call",
            "2026-11-20T03:00:12Z",
            r#"{"tool":"bash"}"#,
        );
        event(
            connection,
            "s1",
            "r1",
            4,
            "tool_result",
            "2026-11-20T03:00:13Z",
            r#"{"ok":false}"#,
        );
        event(
            connection,
            "s1",
            "r1",
            5,
            "usage",
            "2026-11-20T03:00:14Z",
            r#"{"usage":{"inputTokens":1200,"outputTokens":300}}"#,
        );
        // A thread snapshot supersedes itself; summing both would double count.
        event(
            connection,
            "s1",
            "r1",
            6,
            "usage",
            "2026-11-20T03:00:15Z",
            r#"{"scope":"thread","usage":{"inputTokens":100,"outputTokens":10}}"#,
        );
        event(
            connection,
            "s1",
            "r1",
            7,
            "usage",
            "2026-11-20T03:00:16Z",
            r#"{"scope":"thread","usage":{"inputTokens":200,"outputTokens":20}}"#,
        );
        event(
            connection,
            "s2",
            "r2",
            1,
            "approval_request",
            "2026-11-20T04:00:00Z",
            "{}",
        );
        event(
            connection,
            "s2",
            "r2",
            2,
            "approval_request",
            "2026-11-20T04:00:01Z",
            "{}",
        );
        event(
            connection,
            "s2",
            "r2",
            3,
            "approval_resolved",
            "2026-11-20T04:00:02Z",
            "{}",
        );

        let view = report(connection, &input("2026-11-21", 480)).unwrap();
        assert_eq!(view.stats.sessions_this_week, 1);
        assert_eq!(view.stats.active_sessions, 1);
        assert_eq!(view.stats.tool_calls, 2);
        assert_eq!(view.stats.tool_results, 1);
        assert_eq!(view.stats.tool_failures, 1);
        assert_eq!(view.stats.tool_success_rate, Some(50.0));
        assert_eq!(view.stats.tokens_input, 1400);
        assert_eq!(view.stats.tokens_output, 320);
        assert_eq!(view.stats.tokens, 1720);
        assert!(view.stats.tokens_reported);
        assert_eq!(view.stats.pending_approvals, 1);
        // 1 completed run + 1 successful tool = 2 pieces of work that day.
        assert_eq!(view.activity.total, 2);
        assert_eq!(view.activity.active_days, 1);
        assert_eq!(view.activity.longest_streak, 1);
        let day = view
            .activity
            .cells
            .iter()
            .find(|cell| cell.date == "2026-11-20")
            .unwrap();
        assert_eq!(day.count, 2);
        assert_eq!(day.runs, 1);
        assert_eq!(day.tools, 1);
        assert_eq!(view.chats.len(), 2);
        assert_eq!(
            view.chats
                .iter()
                .find(|chat| chat.id == "s2")
                .unwrap()
                .pending,
            1
        );
        assert_eq!(
            view.chats
                .iter()
                .find(|chat| chat.id == "s1")
                .unwrap()
                .pending,
            0
        );
    }

    #[test]
    fn a_utc_evening_run_belongs_to_the_next_local_day() {
        let store = Store::open_in_memory().unwrap();
        let connection = store.raw();
        seed(connection, "s1", "2026-11-21T17:00:00Z", "idle");
        connection
            .execute(
                "INSERT INTO runs (id, session_id, status, started_at, ended_at, prompt)
                 VALUES ('r1', 's1', 'completed', '2026-11-21T17:00:00Z', '2026-11-21T17:00:30Z', '')",
                [],
            )
            .unwrap();
        let view = report(connection, &input("2026-11-22", 480)).unwrap();
        let day = view
            .activity
            .cells
            .iter()
            .find(|cell| cell.date == "2026-11-22")
            .unwrap();
        assert_eq!(day.runs, 1);
        assert_eq!(view.stats.sessions_this_week, 1);
    }

    #[test]
    fn the_grid_ends_on_saturday_and_marks_future_days() {
        let store = Store::open_in_memory().unwrap();
        let view = report(store.raw(), &input("2026-11-21", 480)).unwrap();
        // 2026-11-21 is a Saturday, so the grid ends on that very day.
        assert_eq!(view.grid_end, "2026-11-21");
        assert_eq!(view.activity.cells.first().unwrap().date, "2026-08-30");
        assert_eq!(view.activity.cells.last().unwrap().date, "2026-11-21");
        assert!(view.activity.cells.iter().all(|cell| !cell.future));
    }

    #[test]
    fn seeds_from_the_host_are_passed_through_unchanged() {
        let store = Store::open_in_memory().unwrap();
        let mut input = input("2026-11-21", 480);
        input.agents.push(WorkbenchAgent {
            id: "agent-a".into(),
            name: "迁移工程师".into(),
            ..Default::default()
        });
        input.artifacts.push(WorkbenchArtifact {
            name: "out.md".into(),
            ..Default::default()
        });
        input.status.runtime_total = 2;
        input.sources.filesystem = true;
        let view = report(store.raw(), &input).unwrap();
        assert_eq!(view.agents.len(), 1);
        assert_eq!(view.artifacts.len(), 1);
        assert_eq!(view.status.runtime_total, 2);
        assert!(view.sources.filesystem);
        assert!(!view.sources.schedules);
        assert!(view.schedules.is_empty());
    }
}
