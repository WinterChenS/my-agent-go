//! Schedules: cron-triggered sessions, and the runs they produced.
//!
//! The clock that fires a schedule lives in the host (one thread, one tick), but
//! every fact about a schedule is kept here, in the same SQLite file as the
//! conversations it starts. That matters for the page: "下次 明天 09:00" and
//! "昨天 09:00 成功 1m42s" have to come from something durable, and the run of a
//! schedule *is* a session — the occurrence row points at its session and run,
//! so the detail panel can open the exact conversation a fire produced.
//!
//! Cron is the Vixie five-field form (`minute hour day-of-month month
//! day-of-week`), with `*`, lists, ranges and steps. Day-of-month and
//! day-of-week follow the classic rule: when both are restricted, either one
//! matching is enough; when only one is restricted, that one decides.
//!
//! Time zones are stored as the UTC offset the user picked plus its label. The
//! ledger writes UTC, and an offset keeps "every day at 09:00 Asia/Shanghai"
//! exact without pulling a tz database into the binary; daylight-saving zones
//! are labelled with their offset at creation and can be edited.

use std::sync::atomic::{AtomicU64, Ordering};

use rusqlite::{Connection, OptionalExtension, params};
use serde::{Deserialize, Serialize};

use crate::clock;
use crate::error::{Error, Result};

/// Makes ids unique when two schedules are created in the same millisecond.
static SCHEDULE_COUNTER: AtomicU64 = AtomicU64::new(0);

const SCHEMA: &str = r#"
CREATE TABLE IF NOT EXISTS schedules (
    id                   TEXT PRIMARY KEY,
    name                 TEXT NOT NULL,
    cron                 TEXT NOT NULL,
    time_zone            TEXT NOT NULL DEFAULT '',
    utc_offset_minutes   INTEGER NOT NULL DEFAULT 0,
    agent_id             TEXT NOT NULL DEFAULT '',
    agent_name           TEXT NOT NULL DEFAULT '',
    runtime_id           TEXT NOT NULL DEFAULT '',
    model                TEXT NOT NULL DEFAULT '',
    prompt               TEXT NOT NULL,
    permission           TEXT NOT NULL DEFAULT 'medium',
    output_target        TEXT NOT NULL DEFAULT '',
    enabled              INTEGER NOT NULL DEFAULT 1,
    last_run_at          TEXT NOT NULL DEFAULT '',
    last_status          TEXT NOT NULL DEFAULT '',
    last_duration_ms     INTEGER NOT NULL DEFAULT 0,
    next_run_at          TEXT NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS schedule_occurrences (
    id           INTEGER PRIMARY KEY,
    schedule_id  TEXT NOT NULL,
    scheduled_at TEXT NOT NULL,
    state        TEXT NOT NULL,
    reason       TEXT NOT NULL DEFAULT '',
    session_id   TEXT NOT NULL DEFAULT '',
    run_id       TEXT NOT NULL DEFAULT '',
    started_at   TEXT NOT NULL DEFAULT '',
    finished_at  TEXT NOT NULL DEFAULT '',
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (schedule_id, scheduled_at)
);
CREATE INDEX IF NOT EXISTS schedule_occurrences_order
    ON schedule_occurrences (schedule_id, scheduled_at DESC);
"#;

/// Create the schedule tables on an existing ledger.
///
/// Adding tables to `store::prepare` would change the file for every read; this
/// keeps the migration scoped to the feature that needs it and safe to run on
/// every call.
pub fn ensure_schema(connection: &Connection) -> Result<()> {
    connection.execute_batch(SCHEMA)?;
    Ok(())
}

// ---- cron -----------------------------------------------------------------

/// A parsed five-field cron expression.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Cron {
    minutes: Vec<u32>,
    hours: Vec<u32>,
    days: Vec<u32>,
    months: Vec<u32>,
    weekdays: Vec<u32>,
    day_restricted: bool,
    weekday_restricted: bool,
}

impl Cron {
    /// Parse `minute hour dom month dow`; `7` is accepted as Sunday.
    pub fn parse(expression: &str) -> Result<Self> {
        let fields: Vec<&str> = expression.split_whitespace().collect();
        if fields.len() != 5 {
            return Err(Error::Rejected(format!(
                "cron: expected five fields, got {}",
                fields.len()
            )));
        }
        let (minutes, _) = parse_field(fields[0], 0, 59)?;
        let (hours, _) = parse_field(fields[1], 0, 23)?;
        let (days, day_restricted) = parse_field(fields[2], 1, 31)?;
        let (months, _) = parse_field(fields[3], 1, 12)?;
        let (mut weekdays, weekday_restricted) = parse_field(fields[4], 0, 7)?;
        // Cron accepts both 0 and 7 for Sunday; normalise to 0 so matching has
        // one spelling.
        for weekday in &mut weekdays {
            if *weekday == 7 {
                *weekday = 0;
            }
        }
        weekdays.sort_unstable();
        weekdays.dedup();
        Ok(Self {
            minutes,
            hours,
            days,
            months,
            weekdays,
            day_restricted,
            weekday_restricted,
        })
    }

    fn day_matches(&self, day: u32, month: u32, weekday: u32) -> bool {
        if !self.months.contains(&month) {
            return false;
        }
        let dom = self.days.contains(&day);
        let dow = self.weekdays.contains(&weekday);
        match (self.day_restricted, self.weekday_restricted) {
            (false, false) => true,
            (true, false) => dom,
            (false, true) => dow,
            (true, true) => dom || dow,
        }
    }

    /// First minute strictly after `after_epoch`, as a UTC epoch second.
    ///
    /// `offset_minutes` is the schedule's own zone: the expression is read on
    /// that wall clock, then converted back to UTC. Returns `None` when nothing
    /// matches inside a year (e.g. `0 0 31 2 *`).
    #[must_use]
    pub fn next_after(&self, after_epoch: i64, offset_minutes: i64) -> Option<i64> {
        // Whole minutes only: firing mid-minute would make the recorded
        // `scheduled_at` depend on when the tick happened to run.
        let start = after_epoch.div_euclid(60) * 60 + 60;
        let shift = offset_minutes.saturating_mul(60);
        let horizon = 366 * 24 * 60;
        for step in 0..horizon {
            let stamp = start + step * 60;
            let local = stamp + shift;
            let days = local.div_euclid(86_400);
            let seconds = local.rem_euclid(86_400);
            let (year, month, day) = clock::civil_from_days(days);
            if year > 9999 {
                return None;
            }
            let hour = (seconds / 3_600) as u32;
            let minute = ((seconds % 3_600) / 60) as u32;
            let weekday = ((days + 4).rem_euclid(7)) as u32;
            if self.hours.contains(&hour)
                && self.minutes.contains(&minute)
                && self.day_matches(day, month, weekday)
            {
                return Some(stamp);
            }
        }
        None
    }
}

/// One field: `*`, `n`, `a-b`, a list of those, and an optional `/step`.
fn parse_field(text: &str, min: u32, max: u32) -> Result<(Vec<u32>, bool)> {
    let mut values = Vec::new();
    let mut restricted = false;
    for part in text.split(',') {
        let part = part.trim();
        if part.is_empty() {
            return Err(Error::Rejected(format!(
                "cron: empty field item in {text:?}"
            )));
        }
        let (range, step) = match part.split_once('/') {
            Some((range, step)) => {
                let step: u32 = step
                    .parse()
                    .map_err(|_| Error::Rejected(format!("cron: bad step in {part:?}")))?;
                if step == 0 {
                    return Err(Error::Rejected("cron: step must be at least 1".into()));
                }
                (range, step)
            }
            None => (part, 1),
        };
        let (low, high) = if range == "*" {
            (min, max)
        } else if let Some((low, high)) = range.split_once('-') {
            let low: u32 = low
                .parse()
                .map_err(|_| Error::Rejected(format!("cron: bad range in {part:?}")))?;
            let high: u32 = high
                .parse()
                .map_err(|_| Error::Rejected(format!("cron: bad range in {part:?}")))?;
            (low, high)
        } else {
            let single: u32 = range
                .parse()
                .map_err(|_| Error::Rejected(format!("cron: bad value in {part:?}")))?;
            // `5/15` means "from 5 to the end, every 15", the same as Vixie cron.
            let high = if step > 1 { max } else { single };
            (single, high)
        };
        if low < min || high > max || low > high {
            return Err(Error::Rejected(format!(
                "cron: {part:?} is outside {min}-{max}"
            )));
        }
        if range != "*" {
            restricted = true;
        }
        let mut value = low;
        while value <= high {
            values.push(value);
            value += step;
        }
    }
    values.sort_unstable();
    values.dedup();
    if values.is_empty() {
        return Err(Error::Rejected(format!("cron: {text:?} matches nothing")));
    }
    Ok((values, restricted))
}

// ---- records --------------------------------------------------------------

/// One saved schedule, as the page renders it.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScheduleRecord {
    pub id: String,
    pub name: String,
    pub cron: String,
    pub time_zone: String,
    pub utc_offset_minutes: i64,
    pub agent_id: String,
    pub agent_name: String,
    pub runtime_id: String,
    pub model: String,
    pub prompt: String,
    pub permission: String,
    pub output_target: String,
    pub enabled: bool,
    pub last_run_at: String,
    pub last_status: String,
    pub last_duration_ms: u64,
    pub next_run_at: String,
    pub consecutive_failures: u32,
    pub created_at: String,
    pub updated_at: String,
}

/// One fire of a schedule. `state` is `running`, `success`, `failed`,
/// `skipped` (the run was aborted) or `unknown` (the app died mid-run).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScheduleRun {
    pub schedule_id: String,
    pub scheduled_at: String,
    pub state: String,
    pub reason: String,
    pub session_id: String,
    pub run_id: String,
    pub started_at: String,
    pub finished_at: String,
    pub duration_ms: u64,
}

/// What the form sends. The agent's name, runtime and model are resolved by the
/// host from the catalog, so a renamed agent cannot leave a stale label here.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScheduleDraft {
    #[serde(default)]
    pub id: Option<String>,
    pub name: String,
    pub cron: String,
    #[serde(default)]
    pub time_zone: String,
    #[serde(default)]
    pub utc_offset_minutes: i64,
    #[serde(default)]
    pub agent_id: String,
    pub prompt: String,
    #[serde(default)]
    pub permission: String,
    #[serde(default)]
    pub output_target: String,
    #[serde(default = "default_enabled")]
    pub enabled: bool,
}

fn default_enabled() -> bool {
    true
}

/// The persona a draft runs as, read from the catalog by the caller.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ScheduleTarget {
    pub agent_id: String,
    pub agent_name: String,
    pub runtime_id: String,
    pub model: String,
}

/// The page's whole payload.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScheduleView {
    pub schedules: Vec<ScheduleRecord>,
    /// Five most recent runs per schedule, newest first.
    pub runs: Vec<ScheduleRun>,
    pub stats: ScheduleStats,
    /// Seven local days, oldest first.
    pub trend: Vec<ScheduleTrendPoint>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScheduleStats {
    pub total: u64,
    pub enabled: u64,
    pub running: u64,
    pub today_runs: u64,
    pub week_runs: u64,
    pub week_failed: u64,
    /// `None` when no run finished in the window: "—", not "0%".
    pub success_rate: Option<f64>,
    pub avg_duration_ms: Option<u64>,
    pub next_run_at: Option<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScheduleTrendPoint {
    pub date: String,
    pub runs: u64,
    pub failed: u64,
}

// ---- reads ----------------------------------------------------------------

const COLUMNS: &str = "id, name, cron, time_zone, utc_offset_minutes, agent_id, agent_name, \
     runtime_id, model, prompt, permission, output_target, enabled, last_run_at, last_status, \
     last_duration_ms, next_run_at, consecutive_failures, created_at, updated_at";

fn read_record(row: &rusqlite::Row<'_>) -> rusqlite::Result<ScheduleRecord> {
    Ok(ScheduleRecord {
        id: row.get(0)?,
        name: row.get(1)?,
        cron: row.get(2)?,
        time_zone: row.get(3)?,
        utc_offset_minutes: row.get(4)?,
        agent_id: row.get(5)?,
        agent_name: row.get(6)?,
        runtime_id: row.get(7)?,
        model: row.get(8)?,
        prompt: row.get(9)?,
        permission: row.get(10)?,
        output_target: row.get(11)?,
        enabled: row.get::<_, i64>(12)? != 0,
        last_run_at: row.get(13)?,
        last_status: row.get(14)?,
        last_duration_ms: row.get::<_, i64>(15)?.max(0) as u64,
        next_run_at: row.get(16)?,
        consecutive_failures: row.get::<_, i64>(17)?.max(0) as u32,
        created_at: row.get(18)?,
        updated_at: row.get(19)?,
    })
}

/// A filesystem-safe folder name for a schedule.
///
/// A scheduled task is one task run many times, so every fire shares one folder
/// and that folder is named after the task — "每日站会纪要" rather than a fresh
/// session id per run. Names are user text, so this only removes what a folder
/// name cannot hold (`\ / : * ? " < > |`, control characters, a trailing dot or
/// space) and keeps everything else, including CJK. Windows device names are
/// escaped, and a name that sanitises to nothing falls back to `schedule`.
#[must_use]
pub fn directory_name(name: &str) -> String {
    const MAX_CHARS: usize = 60;
    let mut cleaned = String::with_capacity(name.len());
    let mut last_dash = false;
    for ch in name.trim().chars() {
        let safe = !(ch.is_control()
            || matches!(ch, '\\' | '/' | ':' | '*' | '?' | '"' | '<' | '>' | '|'));
        if safe && !ch.is_whitespace() {
            cleaned.push(ch);
            last_dash = false;
            continue;
        }
        // Whitespace and rejected characters both become one dash, so
        // "站会 纪要" and "站会:纪要" do not produce two different folders.
        if !last_dash && !cleaned.is_empty() {
            cleaned.push('-');
            last_dash = true;
        }
    }
    let trimmed = cleaned.trim_matches(|c| c == '-' || c == '.' || c == ' ');
    let mut out: String = trimmed.chars().take(MAX_CHARS).collect();
    // A trailing dot makes a folder Windows cannot create.
    out = out.trim_end_matches(|c| c == '.' || c == ' ').to_string();
    if out.is_empty() {
        out = "schedule".to_string();
    }
    // `CON`, `NUL`, `COM1`… are device names, not folders. Deviating the name
    // keeps the schedule usable instead of failing every fire.
    let stem = out
        .split('.')
        .next()
        .unwrap_or_default()
        .to_ascii_uppercase();
    let reserved = matches!(stem.as_str(), "CON" | "PRN" | "AUX" | "NUL")
        || (stem.len() == 4
            && (stem.starts_with("COM") || stem.starts_with("LPT"))
            && stem.as_bytes()[3].is_ascii_digit());
    if reserved {
        out.push_str("-task");
    }
    out
}

/// The directory this schedule's most recent fire ran in.
///
/// Read from the ledger instead of derived from the name every time, so
/// renaming a schedule keeps its files where they are instead of scattering
/// them across two folders. `None` means "never ran" (or a ledger without the
/// session tables yet).
pub fn last_workspace(connection: &Connection, schedule_id: &str) -> Result<Option<String>> {
    ensure_schema(connection)?;
    let prepared = connection.prepare(
        "SELECT s.project_path FROM schedule_occurrences o
           JOIN sessions s ON s.id = o.session_id
          WHERE o.schedule_id = ?1 AND o.session_id <> '' AND s.project_path <> ''
          ORDER BY o.scheduled_at DESC LIMIT 1",
    );
    // A ledger created before any session existed has no `sessions` table; that
    // is "nothing to reuse", not a failure of the fire.
    let Ok(mut statement) = prepared else {
        return Ok(None);
    };
    let found = statement
        .query_row(params![schedule_id], |row| row.get::<_, String>(0))
        .optional()?;
    Ok(found.filter(|path| !path.trim().is_empty()))
}

/// Every schedule, enabled ones first and by the time they will next run.
pub fn list(connection: &Connection) -> Result<Vec<ScheduleRecord>> {
    ensure_schema(connection)?;
    let mut statement = connection.prepare(&format!(
        "SELECT {COLUMNS} FROM schedules
         ORDER BY enabled DESC, next_run_at = '', next_run_at, name"
    ))?;
    let rows = statement.query_map([], read_record)?;
    Ok(rows.collect::<rusqlite::Result<Vec<_>>>()?)
}

fn get(connection: &Connection, id: &str) -> Result<Option<ScheduleRecord>> {
    let mut statement =
        connection.prepare(&format!("SELECT {COLUMNS} FROM schedules WHERE id = ?1"))?;
    Ok(statement.query_row(params![id], read_record).optional()?)
}

/// Occurrences of one schedule, newest first.
pub fn runs_for(
    connection: &Connection,
    schedule_id: &str,
    limit: usize,
) -> Result<Vec<ScheduleRun>> {
    ensure_schema(connection)?;
    let mut statement = connection.prepare(
        "SELECT schedule_id, scheduled_at, state, reason, session_id, run_id,
                started_at, finished_at, duration_ms
         FROM schedule_occurrences WHERE schedule_id = ?1
         ORDER BY scheduled_at DESC LIMIT ?2",
    )?;
    let rows = statement.query_map(params![schedule_id, limit as i64], |row| {
        Ok(ScheduleRun {
            schedule_id: row.get(0)?,
            scheduled_at: row.get(1)?,
            state: row.get(2)?,
            reason: row.get(3)?,
            session_id: row.get(4)?,
            run_id: row.get(5)?,
            started_at: row.get(6)?,
            finished_at: row.get(7)?,
            duration_ms: row.get::<_, i64>(8)?.max(0) as u64,
        })
    })?;
    Ok(rows.collect::<rusqlite::Result<Vec<_>>>()?)
}

// ---- writes ---------------------------------------------------------------

fn validate(draft: &ScheduleDraft) -> Result<Cron> {
    if draft.name.trim().is_empty() {
        return Err(Error::Rejected("schedule: name is required".into()));
    }
    if draft.prompt.trim().is_empty() {
        return Err(Error::Rejected("schedule: prompt is required".into()));
    }
    if draft.agent_id.trim().is_empty() {
        return Err(Error::Rejected("schedule: an agent is required".into()));
    }
    let permission = draft.permission.trim().to_ascii_lowercase();
    if !matches!(
        permission.as_str(),
        "" | "readonly" | "medium" | "high" | "full"
    ) {
        return Err(Error::Rejected(format!(
            "schedule: unknown permission {permission:?}"
        )));
    }
    Cron::parse(&draft.cron)
}

fn next_stamp(cron: &Cron, offset_minutes: i64, enabled: bool) -> Option<String> {
    if !enabled {
        return None;
    }
    cron.next_after(clock::unix_now(), offset_minutes)
        .map(clock::rfc3339)
}

/// Insert or update one schedule, returning the stored row.
pub fn save(
    connection: &Connection,
    draft: &ScheduleDraft,
    target: &ScheduleTarget,
) -> Result<ScheduleRecord> {
    ensure_schema(connection)?;
    let cron = validate(draft)?;
    let offset = draft.utc_offset_minutes.clamp(-840, 840);
    let now = clock::now_rfc3339();
    let id = match draft.id.as_deref().map(str::trim).filter(|v| !v.is_empty()) {
        Some(id) => {
            get(connection, id)?.ok_or_else(|| Error::NotFound(format!("schedule {id}")))?;
            id.to_string()
        }
        None => {
            let counter = SCHEDULE_COUNTER.fetch_add(1, Ordering::Relaxed);
            format!(
                "sch-{}-{}-{counter}",
                clock::unix_millis(),
                std::process::id()
            )
        }
    };
    let enabled = draft.enabled;
    let next = next_stamp(&cron, offset, enabled).unwrap_or_default();
    let permission = {
        let value = draft.permission.trim().to_ascii_lowercase();
        if value.is_empty() {
            "medium".to_string()
        } else {
            value
        }
    };
    connection.execute(
        "INSERT INTO schedules (
            id, name, cron, time_zone, utc_offset_minutes, agent_id, agent_name,
            runtime_id, model, prompt, permission, output_target, enabled,
            next_run_at, created_at, updated_at
         ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?15)
         ON CONFLICT(id) DO UPDATE SET
            name = excluded.name,
            cron = excluded.cron,
            time_zone = excluded.time_zone,
            utc_offset_minutes = excluded.utc_offset_minutes,
            agent_id = excluded.agent_id,
            agent_name = excluded.agent_name,
            runtime_id = excluded.runtime_id,
            model = excluded.model,
            prompt = excluded.prompt,
            permission = excluded.permission,
            output_target = excluded.output_target,
            enabled = excluded.enabled,
            next_run_at = excluded.next_run_at,
            updated_at = excluded.updated_at",
        params![
            id,
            draft.name.trim(),
            draft.cron.trim(),
            draft.time_zone.trim(),
            offset,
            target.agent_id,
            target.agent_name,
            target.runtime_id,
            target.model,
            draft.prompt.trim(),
            permission,
            draft.output_target.trim(),
            i64::from(enabled),
            next,
            now,
        ],
    )?;
    get(connection, &id)?.ok_or_else(|| Error::Io("schedule: row vanished after write".into()))
}

/// Turn a schedule off or on; enabling recomputes the next fire from now.
pub fn set_enabled(connection: &Connection, id: &str, enabled: bool) -> Result<ScheduleRecord> {
    ensure_schema(connection)?;
    let record = get(connection, id)?.ok_or_else(|| Error::NotFound(format!("schedule {id}")))?;
    let cron = Cron::parse(&record.cron)?;
    let next = next_stamp(&cron, record.utc_offset_minutes, enabled).unwrap_or_default();
    connection.execute(
        "UPDATE schedules SET enabled = ?2, next_run_at = ?3, updated_at = ?4 WHERE id = ?1",
        params![id, i64::from(enabled), next, clock::now_rfc3339()],
    )?;
    get(connection, id)?.ok_or_else(|| Error::Io("schedule: row vanished after write".into()))
}

/// Remove one schedule and its occurrences.
pub fn remove(connection: &Connection, id: &str) -> Result<()> {
    ensure_schema(connection)?;
    let removed = connection.execute("DELETE FROM schedules WHERE id = ?1", params![id])?;
    if removed == 0 {
        return Err(Error::NotFound(format!("schedule {id}")));
    }
    connection.execute(
        "DELETE FROM schedule_occurrences WHERE schedule_id = ?1",
        params![id],
    )?;
    Ok(())
}

// ---- firing ---------------------------------------------------------------

/// Schedules whose next fire is due, oldest first.
pub fn due(connection: &Connection, now: &str) -> Result<Vec<ScheduleRecord>> {
    ensure_schema(connection)?;
    let mut statement = connection.prepare(&format!(
        "SELECT {COLUMNS} FROM schedules
         WHERE enabled = 1 AND next_run_at <> '' AND next_run_at <= ?1
         ORDER BY next_run_at LIMIT 8"
    ))?;
    let rows = statement.query_map(params![now], read_record)?;
    Ok(rows.collect::<rusqlite::Result<Vec<_>>>()?)
}

/// Claim one scheduled instant for this process.
///
/// The occurrence's `(schedule_id, scheduled_at)` is unique, so a second tick —
/// or a second window — cannot start the same fire twice. On success the
/// schedule's next fire is moved forward in the same transaction, which is what
/// keeps a crash from producing an infinite catch-up loop.
pub fn claim(connection: &Connection, record: &ScheduleRecord, scheduled_at: &str) -> Result<bool> {
    ensure_schema(connection)?;
    let now = clock::now_rfc3339();
    let inserted = connection.execute(
        "INSERT INTO schedule_occurrences (schedule_id, scheduled_at, state, started_at)
         VALUES (?1, ?2, 'running', ?3)
         ON CONFLICT (schedule_id, scheduled_at) DO NOTHING",
        params![record.id, scheduled_at, now],
    )?;
    if inserted == 0 {
        // Already claimed. Still push the next fire forward, otherwise every
        // tick would keep retrying the same instant.
        advance(connection, record, scheduled_at)?;
        return Ok(false);
    }
    advance(connection, record, scheduled_at)?;
    Ok(true)
}

fn advance(connection: &Connection, record: &ScheduleRecord, scheduled_at: &str) -> Result<()> {
    let cron = Cron::parse(&record.cron)?;
    // `scheduled_at` is the instant being fired; the next one is computed from
    // its end so a long run cannot produce a burst of catch-up fires.
    let from = parse_stamp(scheduled_at).unwrap_or_else(clock::unix_now);
    let next = cron
        .next_after(from.max(clock::unix_now()), record.utc_offset_minutes)
        .map(clock::rfc3339)
        .unwrap_or_default();
    connection.execute(
        "UPDATE schedules SET next_run_at = ?2, last_run_at = ?3, updated_at = ?4 WHERE id = ?1",
        params![record.id, next, scheduled_at, clock::now_rfc3339()],
    )?;
    Ok(())
}

/// Record the end of one fire.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct OccurrenceOutcome {
    pub state: String,
    pub reason: String,
    pub session_id: String,
    pub run_id: String,
    pub duration_ms: u64,
}

pub fn finish(
    connection: &Connection,
    schedule_id: &str,
    scheduled_at: &str,
    outcome: &OccurrenceOutcome,
) -> Result<()> {
    ensure_schema(connection)?;
    let now = clock::now_rfc3339();
    connection.execute(
        "UPDATE schedule_occurrences SET state = ?3, reason = ?4, session_id = ?5,
            run_id = ?6, finished_at = ?7, duration_ms = ?8
         WHERE schedule_id = ?1 AND scheduled_at = ?2",
        params![
            schedule_id,
            scheduled_at,
            outcome.state,
            outcome.reason,
            outcome.session_id,
            outcome.run_id,
            now,
            outcome.duration_ms as i64,
        ],
    )?;
    let failed = matches!(outcome.state.as_str(), "failed" | "unknown");
    connection.execute(
        "UPDATE schedules SET last_status = ?2, last_duration_ms = ?3,
            consecutive_failures = CASE WHEN ?4 THEN consecutive_failures + 1 ELSE 0 END,
            updated_at = ?5
         WHERE id = ?1",
        params![
            schedule_id,
            outcome.state,
            outcome.duration_ms as i64,
            i64::from(failed),
            now
        ],
    )?;
    Ok(())
}

/// A run that was `running` when the app last stopped has no known outcome.
pub fn reset_interrupted(connection: &Connection) -> Result<usize> {
    ensure_schema(connection)?;
    let changed = connection.execute(
        "UPDATE schedule_occurrences SET state = 'unknown', reason = 'the app stopped during this run',
            finished_at = ?1 WHERE state = 'running'",
        params![clock::now_rfc3339()],
    )?;
    Ok(changed)
}

/// Whether a fire of this schedule is still marked running.
pub fn is_running(connection: &Connection, schedule_id: &str) -> Result<bool> {
    ensure_schema(connection)?;
    let count: i64 = connection.query_row(
        "SELECT COUNT(*) FROM schedule_occurrences WHERE schedule_id = ?1 AND state = 'running'",
        params![schedule_id],
        |row| row.get(0),
    )?;
    Ok(count > 0)
}

fn parse_stamp(stamp: &str) -> Option<i64> {
    let (year, month, day) = clock::parse_ymd(stamp)?;
    let hour: i64 = stamp.get(11..13)?.parse().ok()?;
    let minute: i64 = stamp.get(14..16)?.parse().ok()?;
    let second: i64 = stamp.get(17..19)?.parse().ok()?;
    Some(clock::days_from_civil(year, month, day) * 86_400 + hour * 3_600 + minute * 60 + second)
}

// ---- the page's payload ---------------------------------------------------

/// Everything `/schedules` draws: rows, recent runs, stats and the 7-day trend.
pub fn view(connection: &Connection, utc_offset_minutes: i64) -> Result<ScheduleView> {
    let offset = utc_offset_minutes.clamp(-840, 840);
    let schedules = list(connection)?;
    let mut runs = Vec::new();
    for record in &schedules {
        runs.extend(runs_for(connection, &record.id, 5)?);
    }
    let today = clock::local_today(offset);
    let week_start = clock::shift_date(&today, -6).unwrap_or_else(|| today.clone());

    // Only the window is read; a year of history would be paged, not charted.
    let (start_year, start_month, start_day) =
        clock::parse_ymd(&week_start).unwrap_or((1970, 1, 1));
    let cutoff = clock::days_from_civil(start_year, start_month, start_day) * 86_400 - offset * 60;
    let mut statement = connection.prepare(
        "SELECT scheduled_at, state, started_at, duration_ms FROM schedule_occurrences
         WHERE started_at >= ?1",
    )?;
    let rows = statement.query_map(params![clock::rfc3339(cutoff)], |row| {
        Ok((
            row.get::<_, String>(0)?,
            row.get::<_, String>(1)?,
            row.get::<_, String>(2)?,
            row.get::<_, i64>(3)?,
        ))
    })?;

    let mut trend: Vec<ScheduleTrendPoint> = (0..7)
        .map(|index| ScheduleTrendPoint {
            date: clock::shift_date(&week_start, index).unwrap_or_default(),
            runs: 0,
            failed: 0,
        })
        .collect();
    let mut week_runs = 0u64;
    let mut week_failed = 0u64;
    let mut finished = 0u64;
    let mut completed = 0u64;
    let mut duration_total = 0u64;
    let mut duration_count = 0u64;
    for row in rows {
        let (scheduled_at, state, started_at, duration_ms) = row?;
        let date = clock::local_date(&started_at, offset)
            .or_else(|| clock::local_date(&scheduled_at, offset))
            .unwrap_or_default();
        if let Some(point) = trend.iter_mut().find(|point| point.date == date) {
            point.runs += 1;
            if matches!(state.as_str(), "failed" | "unknown") {
                point.failed += 1;
            }
        }
        week_runs += 1;
        if matches!(state.as_str(), "failed" | "unknown") {
            week_failed += 1;
        }
        if matches!(state.as_str(), "success" | "failed" | "unknown" | "skipped") {
            finished += 1;
        }
        if state == "success" {
            completed += 1;
            if duration_ms > 0 {
                duration_total += duration_ms.max(0) as u64;
                duration_count += 1;
            }
        }
    }

    let today_runs = trend
        .iter()
        .find(|point| point.date == today)
        .map(|point| point.runs)
        .unwrap_or(0);
    let stats = ScheduleStats {
        total: schedules.len() as u64,
        enabled: schedules.iter().filter(|row| row.enabled).count() as u64,
        running: schedules
            .iter()
            .filter(|row| row.last_status == "running")
            .count() as u64,
        today_runs,
        week_runs,
        week_failed,
        success_rate: (finished > 0)
            .then(|| (completed as f64) * 1000.0 / (finished as f64) / 10.0),
        avg_duration_ms: (duration_count > 0).then(|| duration_total / duration_count),
        next_run_at: schedules
            .iter()
            .filter(|row| row.enabled && !row.next_run_at.is_empty())
            .map(|row| row.next_run_at.clone())
            .min(),
    };
    Ok(ScheduleView {
        schedules,
        runs,
        stats,
        trend,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ledger() -> Connection {
        let connection = Connection::open_in_memory().expect("open");
        ensure_schema(&connection).expect("schema");
        connection
    }

    fn target() -> ScheduleTarget {
        ScheduleTarget {
            agent_id: "agent-1".into(),
            agent_name: "文档助手".into(),
            runtime_id: "pi".into(),
            model: "claude-haiku-4.5".into(),
        }
    }

    fn draft(id: Option<&str>, cron: &str) -> ScheduleDraft {
        ScheduleDraft {
            id: id.map(str::to_string),
            name: "每日站会纪要".into(),
            cron: cron.into(),
            time_zone: "Asia/Shanghai".into(),
            utc_offset_minutes: 480,
            agent_id: "agent-1".into(),
            prompt: "汇总今天的会议记录".into(),
            permission: "medium".into(),
            output_target: "library".into(),
            enabled: true,
        }
    }

    #[test]
    fn parses_lists_ranges_and_steps() {
        let cron = Cron::parse("0,30 9-18/3 * * 1-5").expect("parse");
        assert_eq!(cron.minutes, vec![0, 30]);
        assert_eq!(cron.hours, vec![9, 12, 15, 18]);
        assert_eq!(cron.weekdays, vec![1, 2, 3, 4, 5]);
        assert!(cron.day_restricted == false);
        assert!(cron.weekday_restricted);
    }

    #[test]
    fn rejects_expressions_it_cannot_honour() {
        assert!(Cron::parse("0 9 * *").is_err());
        assert!(Cron::parse("60 9 * * *").is_err());
        assert!(Cron::parse("0 9 * * 9").is_err());
        assert!(Cron::parse("*/0 9 * * *").is_err());
    }

    #[test]
    fn next_fire_lands_on_the_local_wall_clock() {
        // Friday 2026-11-20 00:30 UTC = 08:30 in +08:00. `0 9 * * 1-5` is the
        // next weekday at 09:00 local = 2026-11-20T01:00:00Z.
        let cron = Cron::parse("0 9 * * 1-5").expect("parse");
        let after = parse_stamp("2026-11-20T00:30:00Z").expect("stamp");
        let next = cron.next_after(after, 480).expect("next");
        assert_eq!(clock::rfc3339(next), "2026-11-20T01:00:00Z");
    }

    #[test]
    fn weekend_expression_skips_to_monday() {
        // Saturday 2026-11-21 10:00 local; weekdays only, so Monday 09:00.
        let cron = Cron::parse("0 9 * * 1-5").expect("parse");
        let after = parse_stamp("2026-11-21T02:00:00Z").expect("stamp");
        let next = cron.next_after(after, 480).expect("next");
        assert_eq!(clock::rfc3339(next), "2026-11-23T01:00:00Z");
    }

    #[test]
    fn sunday_may_be_written_as_seven() {
        let five = Cron::parse("0 10 * * 0").expect("parse");
        let seven = Cron::parse("0 10 * * 7").expect("parse");
        assert_eq!(five.weekdays, seven.weekdays);
    }

    #[test]
    fn day_and_weekday_are_either_when_both_restricted() {
        // `0 0 1 * 1` = the 1st of the month OR any Monday.
        let cron = Cron::parse("0 0 1 * 1").expect("parse");
        let monday = parse_stamp("2026-11-23T00:00:00Z").expect("stamp"); // Monday
        assert!(cron.next_after(monday, 0).is_some());
    }

    #[test]
    fn saves_updates_and_toggles() {
        let connection = ledger();
        let saved = save(&connection, &draft(None, "0 9 * * *"), &target()).expect("save");
        assert!(saved.id.starts_with("sch-"));
        assert_eq!(saved.agent_name, "文档助手");
        assert!(!saved.next_run_at.is_empty());

        let mut update = draft(Some(&saved.id), "30 18 * * 1-5");
        update.name = "竞品巡检".into();
        let updated = save(&connection, &update, &target()).expect("update");
        assert_eq!(updated.name, "竞品巡检");
        assert_eq!(updated.cron, "30 18 * * 1-5");
        assert_eq!(list(&connection).expect("list").len(), 1);

        let paused = set_enabled(&connection, &saved.id, false).expect("pause");
        assert!(!paused.enabled);
        assert!(paused.next_run_at.is_empty());

        remove(&connection, &saved.id).expect("remove");
        assert!(list(&connection).expect("list").is_empty());
    }

    #[test]
    fn a_folder_name_keeps_the_task_name_and_drops_what_a_path_cannot_hold() {
        // The task's own name is the folder, CJK included.
        assert_eq!(directory_name("每日站会纪要"), "每日站会纪要");
        assert_eq!(directory_name("站会 纪要"), "站会-纪要");
        assert_eq!(directory_name("notes/report: v2?"), "notes-report-v2");
        // Nothing usable left: still a folder name, never an empty path.
        assert_eq!(directory_name("  "), "schedule");
        assert_eq!(directory_name("***"), "schedule");
        // Windows device names are not folders.
        assert_eq!(directory_name("CON"), "CON-task");
        assert_eq!(directory_name("com1"), "com1-task");
        assert_eq!(directory_name("console"), "console");
        // A trailing dot or space makes a folder Windows refuses to create.
        assert_eq!(directory_name("trailing."), "trailing");
        assert_eq!(directory_name(&"长".repeat(80)).chars().count(), 60);
    }

    #[test]
    fn the_folder_of_a_schedule_is_the_folder_its_last_fire_used() {
        let connection = ledger();
        let saved = save(&connection, &draft(None, "0 9 * * *"), &target()).expect("save");
        // Never fired, and the session tables are not part of this fixture yet:
        // "nothing to reuse" rather than a failed fire.
        assert_eq!(last_workspace(&connection, &saved.id).expect("lookup"), None);

        connection
            .execute_batch(
                "CREATE TABLE sessions (id TEXT PRIMARY KEY, project_path TEXT NOT NULL DEFAULT '')",
            )
            .expect("sessions table");
        connection
            .execute(
                "INSERT INTO sessions (id, project_path) VALUES ('sess-1', 'D:/workspace/schedules/每日站会纪要')",
                [],
            )
            .expect("session row");
        let at = saved.next_run_at.clone();
        assert!(claim(&connection, &saved, &at).expect("claim"));
        finish(
            &connection,
            &saved.id,
            &at,
            &OccurrenceOutcome {
                state: "success".into(),
                reason: String::new(),
                session_id: "sess-1".into(),
                run_id: "run-1".into(),
                duration_ms: 10,
            },
        )
        .expect("finish");
        // The next fire reuses this, so a renamed schedule keeps its history.
        assert_eq!(
            last_workspace(&connection, &saved.id).expect("lookup").as_deref(),
            Some("D:/workspace/schedules/每日站会纪要")
        );
    }

    #[test]
    fn rejects_an_empty_name_or_prompt() {
        let connection = ledger();
        let mut bad = draft(None, "0 9 * * *");
        bad.name = "  ".into();
        assert!(save(&connection, &bad, &target()).is_err());
        let mut bad = draft(None, "0 9 * * *");
        bad.prompt = String::new();
        assert!(save(&connection, &bad, &target()).is_err());
        assert!(list(&connection).expect("list").is_empty());
    }

    #[test]
    fn claim_is_idempotent_and_advances_the_next_fire() {
        let connection = ledger();
        let saved = save(&connection, &draft(None, "0 9 * * *"), &target()).expect("save");
        let at = saved.next_run_at.clone();
        assert!(claim(&connection, &saved, &at).expect("claim"));
        assert!(!claim(&connection, &saved, &at).expect("second claim"));
        let after = get(&connection, &saved.id).expect("get").expect("row");
        assert!(after.next_run_at > at);
        assert_eq!(after.last_run_at, at);
        assert_eq!(runs_for(&connection, &saved.id, 5).expect("runs").len(), 1);
    }

    #[test]
    fn finishing_records_the_run_and_the_failure_streak() {
        let connection = ledger();
        let saved = save(&connection, &draft(None, "0 9 * * *"), &target()).expect("save");
        let at = saved.next_run_at.clone();
        assert!(claim(&connection, &saved, &at).expect("claim"));
        finish(
            &connection,
            &saved.id,
            &at,
            &OccurrenceOutcome {
                state: "failed".into(),
                reason: "model refused".into(),
                session_id: "sess-1".into(),
                run_id: "run-1".into(),
                duration_ms: 4_200,
            },
        )
        .expect("finish");
        let after = get(&connection, &saved.id).expect("get").expect("row");
        assert_eq!(after.last_status, "failed");
        assert_eq!(after.last_duration_ms, 4_200);
        assert_eq!(after.consecutive_failures, 1);
        let runs = runs_for(&connection, &saved.id, 5).expect("runs");
        assert_eq!(runs[0].session_id, "sess-1");
        assert_eq!(runs[0].state, "failed");
        assert!(!is_running(&connection, &saved.id).expect("running"));
    }

    #[test]
    fn interrupted_runs_become_unknown_instead_of_running_forever() {
        let connection = ledger();
        let saved = save(&connection, &draft(None, "0 9 * * *"), &target()).expect("save");
        let at = saved.next_run_at.clone();
        assert!(claim(&connection, &saved, &at).expect("claim"));
        assert_eq!(reset_interrupted(&connection).expect("reset"), 1);
        let runs = runs_for(&connection, &saved.id, 5).expect("runs");
        assert_eq!(runs[0].state, "unknown");
    }

    #[test]
    fn view_counts_today_and_keeps_the_success_rate_honest() {
        let connection = ledger();
        let saved = save(&connection, &draft(None, "0 9 * * *"), &target()).expect("save");
        let today = clock::local_today(480);
        let stamp = format!("{today}T01:00:00Z");
        connection
            .execute(
                "INSERT INTO schedule_occurrences
                 (schedule_id, scheduled_at, state, started_at, finished_at, duration_ms)
                 VALUES (?1, ?2, 'success', ?2, ?2, 90000)",
                params![saved.id, stamp],
            )
            .expect("insert");
        let view = view(&connection, 480).expect("view");
        assert_eq!(view.schedules.len(), 1);
        assert_eq!(view.stats.today_runs, 1);
        assert_eq!(view.stats.success_rate, Some(100.0));
        assert_eq!(view.stats.avg_duration_ms, Some(90_000));
        assert_eq!(view.trend.len(), 7);
        assert_eq!(view.trend.last().expect("today").runs, 1);
        assert!(view.stats.next_run_at.is_some());
    }

    #[test]
    fn view_reports_no_rate_when_nothing_finished() {
        let connection = ledger();
        save(&connection, &draft(None, "0 9 * * *"), &target()).expect("save");
        let view = view(&connection, 480).expect("view");
        assert_eq!(view.schedules.len(), 1);
        assert_eq!(view.stats.success_rate, None);
        assert_eq!(view.stats.avg_duration_ms, None);
        assert_eq!(view.stats.today_runs, 0);
    }
}
