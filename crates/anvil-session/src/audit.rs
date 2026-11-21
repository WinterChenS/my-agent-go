//! Observability and audit views over the ledger.
//!
//! Everything here is derived from rows the runtime actually wrote: `usage`
//! events for tokens and cost, `runs` for traces, `tool_call`/`tool_result`
//! events for the execution log, `sessions` for the model/agent labels. No
//! estimate, no seeded number. A missing counter is reported as zero and the
//! row is still shown with a `reported: false` marker so the UI can say "the
//! runtime did not report tokens" instead of printing a convincing 0.

use std::collections::{BTreeMap, HashMap};

use rusqlite::{Connection, params};
use serde::{Deserialize, Serialize};

use crate::error::{Error, Result};

/// How far back the view reaches. `Month` means the current calendar month.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Period {
    Days7,
    Days30,
    Month,
}

impl Period {
    pub fn from_label(label: &str) -> Option<Self> {
        match label.trim().to_ascii_lowercase().as_str() {
            "7" | "7d" | "days7" => Some(Self::Days7),
            "30" | "30d" | "days30" => Some(Self::Days30),
            "month" => Some(Self::Month),
            _ => None,
        }
    }

    /// Inclusive lower bound as `YYYY-MM-DD` (UTC). Computed from the epoch so
    /// month boundaries are correct without a calendar dependency.
    /// Inclusive lower bound as `YYYY-MM-DD` (UTC). Public so the boundary
    /// arithmetic can be asserted from the contract tests.
    #[must_use]
    pub fn since(self, today: &str) -> String {
        let date = parse_date(today);
        match self {
            Self::Days7 => shift_date(date, -6),
            Self::Days30 => shift_date(date, -29),
            Self::Month => format!("{:04}-{:02}-01", date.0, date.1),
        }
    }
}

/// `(year, month, day)`, tolerating a truncated stamp.
fn parse_date(stamp: &str) -> (i64, i64, i64) {
    let mut parts = stamp.split('-');
    let year = parts.next().and_then(|v| v.parse().ok()).unwrap_or(1970);
    let month = parts.next().and_then(|v| v.parse().ok()).unwrap_or(1);
    let day = parts
        .next()
        .and_then(|v| v.get(..2)?.parse().ok())
        .unwrap_or(1);
    (year, month, day)
}

fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    // Howard Hinnant's civil calendar algorithm, sufficient for epoch-day math.
    let y = year - if month <= 2 { 1 } else { 0 };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = month + if month > 2 { -3 } else { 9 };
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

fn civil_from_days(days: i64) -> (i64, i64, i64) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = mp + if mp < 10 { 3 } else { -9 };
    (y + if m <= 2 { 1 } else { 0 }, m, d)
}

fn shift_date(date: (i64, i64, i64), delta: i64) -> String {
    let (year, month, day) = civil_from_days(days_from_civil(date.0, date.1, date.2) + delta);
    format!("{year:04}-{month:02}-{day:02}")
}

/// One day's token totals.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UsageDay {
    pub date: String,
    pub input: u64,
    pub output: u64,
    pub cached: u64,
    pub cost: f64,
    pub calls: u64,
}

/// One model's or agent's share.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UsageShare {
    pub label: String,
    pub tokens: u64,
    pub calls: u64,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UsageTotals {
    pub tokens: u64,
    pub input: u64,
    pub output: u64,
    pub cached: u64,
    pub cost: f64,
    pub calls: u64,
    pub tools: u64,
    /// True when at least one `usage` event carried a token counter. When false
    /// the totals are about calls/tools only, and token numbers must be shown
    /// as "not reported" rather than as a measured zero.
    pub reported: bool,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UsageView {
    pub days: Vec<UsageDay>,
    pub by_model: Vec<UsageShare>,
    pub by_agent: Vec<UsageShare>,
    pub totals: UsageTotals,
}

/// One turn, as an audit trace.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TraceRow {
    pub id: String,
    pub session_id: String,
    pub session: String,
    pub model: String,
    pub agent: String,
    pub steps: u64,
    pub tokens: u64,
    pub started_at: String,
    pub duration_ms: u64,
    pub state: String,
}

/// One tool call, as an execution-log row.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExecutionRow {
    pub at: String,
    pub action: String,
    pub subject: String,
    pub session: String,
    pub model: String,
    pub ok: Option<bool>,
    pub duration_ms: Option<u64>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct AuditReport {
    pub period: String,
    pub since: String,
    pub today: String,
    pub usage: UsageView,
    pub traces: Vec<TraceRow>,
    pub executions: Vec<ExecutionRow>,
}

/// Read the numeric field the runtime used, across the shapes pi, Codex and
/// OpenAI-compatible providers emit. Mirrors the front-end's `readUsage` so the
/// totals here and the per-turn line in chat agree.
fn number(raw: &serde_json::Value, paths: &[&str]) -> Option<f64> {
    for path in paths {
        let mut current = raw;
        for step in path.split('.') {
            current = match current.get(step) {
                Some(next) => next,
                None => break,
            };
        }
        if let Some(value) = current.as_f64() {
            return Some(value.max(0.0));
        }
        if let Some(text) = current.as_str() {
            if let Ok(value) = text.trim().parse::<f64>() {
                return Some(value.max(0.0));
            }
        }
    }
    None
}

pub(crate) struct UsageNumbers {
    pub(crate) input: u64,
    pub(crate) output: u64,
    pub(crate) cached: u64,
    pub(crate) cost: f64,
}

/// Shared with the workbench view so its token totals and the audit page agree
/// on the shapes pi, Codex and OpenAI-compatible providers emit.
pub(crate) fn usage_of(payload: &serde_json::Value) -> Option<UsageNumbers> {
    let raw = payload.get("usage").unwrap_or(payload);
    let base = number(
        raw,
        &[
            "inputTokens",
            "input_tokens",
            "promptTokens",
            "prompt_tokens",
            "input",
            "prompt",
        ],
    );
    let output = number(
        raw,
        &[
            "outputTokens",
            "output_tokens",
            "completionTokens",
            "completion_tokens",
            "output",
            "completion",
        ],
    );
    let cached = number(
        raw,
        &[
            "cacheRead",
            "cache_read",
            "cache_read_input_tokens",
            "cachedInputTokens",
            "prompt_tokens_details.cached_tokens",
            "input_tokens_details.cached_tokens",
        ],
    )
    .unwrap_or(0.0);
    let cache_write = number(
        raw,
        &[
            "cacheWrite",
            "cache_write",
            "cache_creation_input_tokens",
        ],
    )
    .unwrap_or(0.0);
    let total = number(raw, &["totalTokens", "total_tokens"]);
    if base.is_none() && output.is_none() && cached == 0.0 && cache_write == 0.0 && total.is_none() {
        return None;
    }
    let inclusive = ["inputTokens", "input_tokens", "promptTokens", "prompt_tokens"]
        .iter()
        .any(|key| raw.get(*key).is_some());
    let mut input = (base.unwrap_or(0.0) + if inclusive { 0.0 } else { cached + cache_write }) as u64;
    let out = output.unwrap_or(0.0) as u64;
    // A runtime that only reports a total has no split. Put it in input rather
    // than dropping it, and say so through `reported` (a split of 0/0 would be
    // a lie about the shape of the data).
    if base.is_none() && output.is_none() {
        if let Some(value) = total {
            input = value as u64;
        }
    }
    if input == 0 && out == 0 {
        if let Some(value) = total {
            input = value as u64;
        }
    }
    let cost = number(raw, &["cost.total", "cost"])
        .or_else(|| number(payload, &["cost.total", "cost"]))
        .unwrap_or(0.0);
    Some(UsageNumbers {
        input,
        output: out,
        cached: cached as u64,
        cost,
    })
}

/// Whether a usage event is a running total for a whole thread. Those must not
/// be summed with per-request reports.
pub(crate) fn is_thread_snapshot(payload: &serde_json::Value) -> bool {
    payload.get("scope").and_then(|v| v.as_str()) == Some("thread")
        || payload
            .get("usage")
            .and_then(|v| v.get("scope"))
            .and_then(|v| v.as_str())
            == Some("thread")
}

fn parse_stamp_ms(stamp: &str) -> Option<i64> {
    // RFC 3339 UTC produced by `clock::now_rfc3339`: YYYY-MM-DDTHH:MM:SSZ.
    // Parsing it through epoch-days keeps the crate free of a date dependency.
    if stamp.len() < 19 {
        return None;
    }
    let date = parse_date(stamp);
    let hh = stamp.get(11..13)?.parse::<i64>().ok()?;
    let mm = stamp.get(14..16)?.parse::<i64>().ok()?;
    let ss = stamp.get(17..19)?.parse::<i64>().ok()?;
    Some(days_from_civil(date.0, date.1, date.2) * 86_400 + hh * 3600 + mm * 60 + ss)
}

fn duration(start: &str, end: &str) -> u64 {
    match (parse_stamp_ms(start), parse_stamp_ms(end)) {
        (Some(a), Some(b)) => (b - a).max(0) as u64 * 1000,
        _ => 0,
    }
}

fn accumulate(target: &mut UsageDay, numbers: &UsageNumbers) {
    target.input += numbers.input;
    target.output += numbers.output;
    target.cached += numbers.cached;
    target.cost += numbers.cost;
}

/// Build the whole observability view from the ledger.
pub fn report(connection: &Connection, period: Period, today: &str) -> Result<AuditReport> {
    let since = period.since(today);

    // Session labels used to attribute an event to a model or a saved agent.
    let mut labels: HashMap<String, (String, String, String)> = HashMap::new();
    {
        let mut statement = connection.prepare(
            "SELECT id, title, model, agent_id FROM sessions WHERE archived = 0",
        )?;
        let rows = statement.query_map([], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, String>(3)?,
            ))
        })?;
        for row in rows {
            let (id, title, model, agent) = row?;
            labels.insert(id, (title, model, agent));
        }
    }

    let label = |id: &str| -> (String, String, String) {
        labels
            .get(id)
            .cloned()
            .unwrap_or_else(|| (String::new(), String::new(), String::new()))
    };

    // ---- usage -----------------------------------------------------------
    let mut days: BTreeMap<String, UsageDay> = BTreeMap::new();
    let mut by_model: BTreeMap<String, UsageShare> = BTreeMap::new();
    let mut by_agent: BTreeMap<String, UsageShare> = BTreeMap::new();
    let mut snapshots: HashMap<String, (i64, UsageNumbers)> = HashMap::new();
    let mut totals = UsageTotals::default();
    {
        let mut statement = connection.prepare(
            "SELECT e.session_id, e.run_id, e.seq, e.at, e.payload
             FROM events e WHERE e.kind = 'usage' AND substr(e.at, 1, 10) >= ?1
             ORDER BY e.seq",
        )?;
        let rows = statement.query_map(params![since], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, i64>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, String>(4)?,
            ))
        })?;
        for row in rows {
            let (session_id, run_id, seq, at, payload) = row?;
            let value: serde_json::Value = serde_json::from_str(&payload).unwrap_or_default();
            let Some(numbers) = usage_of(&value) else { continue };
            let date = at.get(..10).unwrap_or("").to_string();
            let (_, model, agent) = label(&session_id);
            let key = format!("{session_id}:{run_id}");
            if is_thread_snapshot(&value) {
                // Latest snapshot of a run wins; summing them double counts.
                // `or_insert` would move `numbers` even when the entry exists,
                // so check first with the entry API's occupied branch.
                match snapshots.entry(key) {
                    std::collections::hash_map::Entry::Occupied(mut entry) => {
                        if seq > entry.get().0 {
                            entry.insert((seq, numbers));
                        }
                    }
                    std::collections::hash_map::Entry::Vacant(entry) => {
                        entry.insert((seq, numbers));
                    }
                }
                continue;
            }
            let bucket = days.entry(date.clone()).or_insert_with(|| UsageDay {
                date,
                ..UsageDay::default()
            });
            accumulate(bucket, &numbers);
            bucket.calls += 1;
            let tokens = numbers.input + numbers.output;
            by_model
                .entry(model.clone())
                .or_default()
                .add(model.clone(), tokens);
            by_agent
                .entry(agent.clone())
                .or_default()
                .add(agent.clone(), tokens);
            totals.input += numbers.input;
            totals.output += numbers.output;
            totals.cached += numbers.cached;
            totals.cost += numbers.cost;
            totals.calls += 1;
            totals.reported = true;
        }
    }
    for (_, (_, numbers)) in snapshots {
        // Thread snapshots have no reliable date split beyond their run; count
        // them into totals only, never into a day the event did not name.
        totals.input += numbers.input;
        totals.output += numbers.output;
        totals.cached += numbers.cached;
        totals.cost += numbers.cost;
        totals.calls += 1;
        totals.reported = true;
    }
    totals.tokens = totals.input + totals.output;

    // Tool calls in the same window, for the "tools" total and the exec log.
    let mut executions: Vec<ExecutionRow> = Vec::new();
    {
        let mut statement = connection.prepare(
            "SELECT e.session_id, e.run_id, e.seq, e.at, e.kind, e.payload
             FROM events e
             WHERE e.kind IN ('tool_call', 'tool_result') AND substr(e.at, 1, 10) >= ?1
             ORDER BY e.seq",
        )?;
        let rows = statement.query_map(params![since], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, i64>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, String>(4)?,
                row.get::<_, String>(5)?,
            ))
        })?;
        struct Open {
            at: String,
            action: String,
            subject: String,
            session: String,
            model: String,
            start_ms: Option<i64>,
        }
        let mut open: HashMap<String, Open> = HashMap::new();
        for row in rows {
            let (session_id, run_id, _seq, at, kind, payload) = row?;
            let value: serde_json::Value = serde_json::from_str(&payload).unwrap_or_default();
            let (title, model, _) = label(&session_id);
            if kind == "tool_call" {
                totals.tools += 1;
                let action = value
                    .get("tool")
                    .or_else(|| value.get("name"))
                    .and_then(|v| v.as_str())
                    .unwrap_or("tool")
                    .to_string();
                let subject = value
                    .get("args")
                    .or_else(|| value.get("command"))
                    .map(|v| match v {
                        serde_json::Value::String(text) => text.clone(),
                        other => other.to_string(),
                    })
                    .unwrap_or_default();
                open.insert(
                    format!("{session_id}:{run_id}"),
                    Open {
                        at: at.clone(),
                        action,
                        subject: truncate(&subject, 160),
                        session: title,
                        model,
                        start_ms: parse_stamp_ms(&at),
                    },
                );
                continue;
            }
            // tool_result closes the most recent open call of the same run.
            let key = format!("{session_id}:{run_id}");
            if let Some(call) = open.remove(&key) {
                let end = parse_stamp_ms(&at);
                let duration_ms = match (call.start_ms, end) {
                    (Some(a), Some(b)) => Some((b - a).max(0) as u64 * 1000),
                    _ => None,
                };
                executions.push(ExecutionRow {
                    at: call.at,
                    action: call.action,
                    subject: call.subject,
                    session: call.session,
                    model: call.model,
                    ok: value.get("ok").and_then(|v| v.as_bool()),
                    duration_ms,
                });
            }
        }
        for (_, call) in open {
            executions.push(ExecutionRow {
                at: call.at,
                action: call.action,
                subject: call.subject,
                session: call.session,
                model: call.model,
                ok: None,
                duration_ms: None,
            });
        }
    }
    executions.sort_by(|a, b| b.at.cmp(&a.at));

    // ---- traces ----------------------------------------------------------
    let mut traces: Vec<TraceRow> = Vec::new();
    {
        let mut statement = connection.prepare(
            "SELECT r.id, r.session_id, r.status, r.started_at, r.ended_at, r.error,
                    (SELECT COUNT(*) FROM events e
                       WHERE e.run_id = r.id AND e.kind = 'tool_call'),
                    COALESCE((
                       SELECT SUM(CAST(json_extract(e.payload, '$.usage.totalTokens') AS INTEGER))
                       FROM events e WHERE e.run_id = r.id AND e.kind = 'usage'
                    ), 0)
             FROM runs r WHERE substr(r.started_at, 1, 10) >= ?1
             ORDER BY r.started_at DESC LIMIT 40",
        )?;
        let rows = statement.query_map(params![since], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, Option<String>>(4)?,
                row.get::<_, Option<String>>(5)?,
                row.get::<_, u64>(6)?,
                row.get::<_, u64>(7)?,
            ))
        })?;
        for row in rows {
            let (id, session_id, status, started, ended, error, steps, tokens) = row?;
            let (title, model, agent) = label(&session_id);
            let state = if status == "running" {
                "warn"
            } else if status == "failed" || error.is_some() {
                "failed"
            } else {
                "success"
            };
            traces.push(TraceRow {
                id,
                session_id,
                session: title,
                model,
                agent,
                steps,
                tokens,
                started_at: started.clone(),
                duration_ms: ended.as_deref().map(|end| duration(&started, end)).unwrap_or(0),
                state: state.to_string(),
            });
        }
    }

    let sorted = |map: BTreeMap<String, UsageShare>| {
        let mut rows: Vec<UsageShare> = map.into_values().collect();
        rows.sort_by(|a, b| b.tokens.cmp(&a.tokens).then_with(|| a.label.cmp(&b.label)));
        rows.truncate(8);
        rows
    };
    Ok(AuditReport {
        period: match period {
            Period::Days7 => "7".into(),
            Period::Days30 => "30".into(),
            Period::Month => "month".into(),
        },
        since,
        today: today.to_string(),
        usage: UsageView {
            days: days.into_values().collect(),
            by_model: sorted(by_model),
            by_agent: sorted(by_agent),
            totals,
        },
        traces,
        executions,
    })
}

impl UsageShare {
    fn add(&mut self, label: String, tokens: u64) {
        if self.label.is_empty() {
            self.label = label;
        }
        self.tokens += tokens;
        self.calls += 1;
    }
}

fn truncate(text: &str, limit: usize) -> String {
    let mut out: String = text.chars().take(limit).collect();
    if text.chars().count() > limit {
        out.push('…');
    }
    out
}

/// Replace anything that looks like a secret in a tool argument before it is
/// written to an audit export. The ledger keeps the real text (that is the
/// point of an audit trail); only the exported copy is masked.
#[must_use]
pub fn mask_text(text: &str) -> String {
    let mut out = text.to_string();
    for key in [
        "api_key", "api-key", "apikey", "token", "password", "secret",
        "authorization", "bearer", "connection_string", "access_key",
    ] {
        // Mask `key=value`, `key: value` and `"key":"value"` shapes without a
        // regex dependency; the value runs to the next separator or quote.
        let lower = out.to_lowercase();
        let mut from = 0usize;
        while let Some(hit) = lower[from..].find(key) {
            let start = from + hit;
            let after = start + key.len();
            let tail = &out[after..];
            let sep = tail.find(|ch| ch == '=' || ch == ':' || ch == '"');
            let Some(sep) = sep else { break };
            if tail[..sep].chars().any(|ch| ch == ' ' || ch == '\n') {
                from = after;
                continue;
            }
            let mut value_at = after + sep + 1;
            // Skip the quote pair of `"key": "value"`.
            while value_at < out.len() && out.as_bytes()[value_at].is_ascii_whitespace() {
                value_at += 1;
            }
            if value_at < out.len() && out.as_bytes()[value_at] == b'"' {
                value_at += 1;
            }
            let end = out[value_at..]
                .find(|ch| ch == ',' || ch == '&' || ch == ' ' || ch == '\n' || ch == '"' || ch == '\r')
                .map(|offset| value_at + offset)
                .unwrap_or(out.len());
            if end > value_at {
                out.replace_range(value_at..end, "***");
            }
            from = value_at + 3;
            if from >= out.len() {
                break;
            }
        }
    }
    out
}

/// CSV of the execution log, for the audit export. Escaped per RFC 4180.
/// `mask` redacts secret-looking arguments in the exported copy only.
pub fn executions_csv(rows: &[ExecutionRow], mask: bool) -> String {
    let mut out = String::from("time,action,subject,session,model,result,duration_ms\n");
    for row in rows {
        let fields = [
            row.at.clone(),
            row.action.clone(),
            if mask {
                mask_text(&row.subject)
            } else {
                row.subject.clone()
            },
            row.session.clone(),
            row.model.clone(),
            match row.ok {
                Some(true) => "success".into(),
                Some(false) => "failed".into(),
                None => "running".into(),
            },
            row.duration_ms.map(|v| v.to_string()).unwrap_or_default(),
        ];
        let line: Vec<String> = fields
            .iter()
            .map(|field| {
                if field.contains([',', '"', '\n', '\r']) {
                    format!("\"{}\"", field.replace('"', "\"\""))
                } else {
                    field.clone()
                }
            })
            .collect();
        out.push_str(&line.join(","));
        out.push('\n');
    }
    out
}

/// Resolve a period label, or explain what was wrong with it.
pub fn parse_period(label: &str) -> Result<Period> {
    Period::from_label(label).ok_or_else(|| Error::Rejected(format!("unknown audit period {label:?}")))
}
