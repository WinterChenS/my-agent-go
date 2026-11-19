//! The right rail: what a session actually did, derived from the ledger.
//!
//! Four panes, four independent sources of truth — none of them is invented:
//!
//! * `exec` — one row per tool call, paired with its result, keyed to the turn
//!   that produced it;
//! * `artifacts` — files a turn wrote, resolved on disk and **confined to the
//!   project**; a path outside it is counted and dropped, never displayed;
//! * `sources` — URLs the transcript cites, numbered by first appearance so the
//!   numbers in the pane are the numbers in the `[n]` markers;
//! * `browser` — the page this build can actually show (a local HTML artifact,
//!   sandboxed) or the honest reason there is none.
//!
//! Everything carries the `runId` and the 1-based turn index, because the panes
//! exist to send the reader back to the turn a row came from.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

use crate::context::is_within;
use crate::model::{EventKind, Message, Role, Run, RunEvent};

/// A local page larger than this is not previewed: the rail is a reader, not a
/// browser, and pasting a 20 MB file into the DOM would freeze the window.
const MAX_PAGE_BYTES: u64 = 256 * 1024;

/// Tools whose arguments name the file they write.
const WRITE_TOOLS: &[&str] = &[
    "write",
    "write_file",
    "writefile",
    "edit",
    "edit_file",
    "editfile",
    "multi_edit",
    "str_replace",
    "str_replace_editor",
    "apply_patch",
    "patch",
    "create",
    "create_file",
];

/// Tools whose arguments are a shell command line; the artifact is behind a
/// redirection, not in a named field.
const SHELL_TOOLS: &[&str] = &["command", "bash", "shell", "exec", "run", "terminal"];

/// One row of the execution pane: a tool call and what came back.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailExec {
    /// 1-based turn index; `0` when the call belongs to no run.
    pub turn: i64,
    pub run_id: String,
    pub tool: String,
    /// The thing the reader recognises: a command line, a file path, an args
    /// fragment. Trimmed and collapsed to one line.
    pub label: String,
    /// `done`, `failed`, `running` or `no_result`.
    pub status: String,
    /// Event sequence of the call, so a click can scroll to the exact row.
    pub seq: i64,
    pub at: String,
}

/// A file the session wrote, as the rail lists it.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailArtifact {
    /// Absolute path; always inside the project.
    pub path: String,
    /// Project-relative path, which is what the pane shows.
    pub relative: String,
    pub bytes: u64,
    /// Turn that first produced it — this is where the row goes back to.
    pub turn: i64,
    pub run_id: String,
    pub seq: i64,
    /// How many turns touched the same path.
    pub touches: usize,
    /// `.md`, `.html`, … (lowercase, without the dot).
    pub ext: String,
}

/// One cited URL.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailSource {
    /// The number that appears in the transcript as `[number]`.
    pub number: usize,
    pub url: String,
    pub host: String,
    pub turn: i64,
    pub run_id: String,
    /// `message` when the assistant wrote it, `tool` when a tool returned it.
    pub origin: String,
}

/// A file or folder the user attached to a turn.
///
/// This is the other half of "引用": the sources pane already lists the URLs an
/// answer cited, and these are the things the reader put in front of the model
/// — the composer chips, kept per turn so the pane can send the reader back.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailReference {
    /// Absolute path as the host resolved it (symlinks resolved).
    pub path: String,
    /// The name the chip showed; the file name when the turn carried none.
    pub label: String,
    /// `file`, `dir`, `image` or `skill`.
    pub kind: String,
    /// Turn that first attached it.
    pub turn: i64,
    /// Run of that turn, so a click can jump back to it.
    pub run_id: String,
    /// Bytes of the source, before any truncation.
    pub bytes: u64,
    /// The reference was larger than the inline budget and was cut.
    pub truncated: bool,
    /// It sits outside the project and was allowed by an approval.
    pub outside_root: bool,
    /// How many turns attached the same path.
    pub touches: usize,
}

/// A page the rail can render, with the permission it is rendered under.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailPage {
    pub relative: String,
    /// The sandbox the frame is created with. Empty string means "no
    /// permissions at all": no scripts, no same-origin, no navigation.
    pub sandbox: String,
    pub html: String,
    pub bytes: u64,
}

/// What the browser pane can honestly say.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailBrowser {
    /// `local` when a page is attached, `unavailable` when nothing can be shown.
    pub state: String,
    /// Why it is in that state, in the reader's words.
    pub reason: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page: Option<RailPage>,
}

/// Aggregated token accounting, from the runtime's own usage events.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailUsage {
    pub input: u64,
    pub output: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    /// Reported cost, when the runtime reports one. Never estimated here.
    pub cost: f64,
    /// How many usage events were folded (the latest one wins, because runtimes
    /// report a snapshot per message rather than a running total).
    pub events: usize,
}

/// One turn, as the execution pane summarises it.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailTurn {
    pub turn: i64,
    pub run_id: String,
    pub status: String,
    pub started_at: String,
    pub ended_at: String,
    /// Tool calls this turn made.
    pub calls: usize,
}

/// One delegated child agent, folded from its `sub_agent` observations.
///
/// This is the pane's copy of what the transcript used to render inline: the
/// parent's window shows a single status row, and the child's own reasoning,
/// tool calls and answer live here until the reader asks for them.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailSubAgent {
    /// Runtime-level id (`call_1#0` for pi: one delegation can spawn several).
    pub id: String,
    pub name: String,
    pub task: String,
    pub model: String,
    /// `pending`, `running`, `waiting`, `completed`, `failed`, `interrupted`.
    pub status: String,
    /// Position inside a chain, when the runtime reports one.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub step: Option<i64>,
    /// 1-based turn that delegated it, and its run — what a click goes back to.
    pub turn: i64,
    pub run_id: String,
    /// Ordered content: `{kind:"text"|"tool"|"tool_result", …}`.
    pub steps: Vec<serde_json::Value>,
    pub result: String,
    pub error: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<serde_json::Value>,
}

/// The four panes in one read.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailView {
    pub turns: Vec<RailTurn>,
    pub exec: Vec<RailExec>,
    pub artifacts: Vec<RailArtifact>,
    pub sources: Vec<RailSource>,
    /// Files, folders and skills the user attached, per turn.
    pub references: Vec<RailReference>,
    /// Delegated child agents, one row per agent (not per event).
    pub subagents: Vec<RailSubAgent>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<RailUsage>,
    pub browser: RailBrowser,
    /// Paths a turn named outside the project. Counted, never listed.
    pub outside_paths: usize,
    /// True when nothing at all was produced: the UI shows empty states instead
    /// of placeholders that look like data.
    pub empty: bool,
}

impl Default for RailBrowser {
    fn default() -> Self {
        Self {
            state: "unavailable".into(),
            reason: String::new(),
            page: None,
        }
    }
}

/// Build the view from the ledger rows and the files on disk.
#[must_use]
pub fn build(
    project_path: &str,
    messages: &[Message],
    runs: &[Run],
    events: &[RunEvent],
) -> RailView {
    let mut turn_of: BTreeMap<&str, i64> = BTreeMap::new();
    for (index, run) in runs.iter().enumerate() {
        turn_of.insert(run.id.as_str(), index as i64 + 1);
    }
    let turn_for = |run_id: &str| turn_of.get(run_id).copied().unwrap_or(0);

    let mut calls_per_turn: BTreeMap<i64, usize> = BTreeMap::new();
    let (exec, artifacts, outside_paths) =
        walk_events(project_path, events, &turn_for, &mut calls_per_turn);

    let turns = runs
        .iter()
        .enumerate()
        .map(|(index, run)| RailTurn {
            turn: index as i64 + 1,
            run_id: run.id.clone(),
            status: run.status.as_str().to_string(),
            started_at: run.started_at.clone(),
            ended_at: run.ended_at.clone().unwrap_or_default(),
            calls: calls_per_turn
                .get(&(index as i64 + 1))
                .copied()
                .unwrap_or(0),
        })
        .collect::<Vec<_>>();

    let sources = collect_sources(messages, &turn_for);
    let references = collect_references(messages, &turn_for);
    let subagents = collect_subagents(events, &turn_for);
    let usage = collect_usage(events);
    let browser = browser_for(&artifacts);

    let empty = turns.is_empty()
        && exec.is_empty()
        && artifacts.is_empty()
        && sources.is_empty()
        && references.is_empty();

    RailView {
        turns,
        exec,
        artifacts,
        sources,
        references,
        subagents,
        usage,
        browser,
        outside_paths,
        empty,
    }
}

/// Fold every `sub_agent` row into one entry per agent.
///
/// The rows arrive as observations, not as state: a status change, a text
/// delta, a tool call and a snapshot are separate events with the same
/// `agentId`. The pane wants the agent, so this replays the same merge the live
/// card does — text appends within a channel, a snapshot replaces, a tool result
/// closes the most recent call that has none.
fn collect_subagents(events: &[RunEvent], turn_for: &dyn Fn(&str) -> i64) -> Vec<RailSubAgent> {
    let mut out: Vec<RailSubAgent> = Vec::new();
    let mut index: BTreeMap<String, usize> = BTreeMap::new();

    for event in events {
        if event.kind != EventKind::SubAgent {
            continue;
        }
        let payload = &event.payload;
        let Some(agent_id) = payload
            .get("agentId")
            .and_then(serde_json::Value::as_str)
            .filter(|id| !id.is_empty())
        else {
            continue;
        };
        let key = format!("{}|{agent_id}", event.run_id);
        let position = match index.get(&key) {
            Some(position) => *position,
            None => {
                out.push(RailSubAgent {
                    id: agent_id.to_string(),
                    turn: turn_for(&event.run_id),
                    run_id: event.run_id.clone(),
                    ..RailSubAgent::default()
                });
                index.insert(key, out.len() - 1);
                out.len() - 1
            }
        };
        let agent = &mut out[position];

        let read = |name: &str| {
            payload
                .get(name)
                .and_then(serde_json::Value::as_str)
                .unwrap_or("")
                .to_string()
        };
        for (field, slot) in [
            ("name", &mut agent.name),
            ("task", &mut agent.task),
            ("model", &mut agent.model),
            ("result", &mut agent.result),
            ("error", &mut agent.error),
            ("status", &mut agent.status),
        ] {
            let value = read(field);
            if !value.is_empty() {
                *slot = value;
            }
        }
        if let Some(step) = payload.get("step").and_then(serde_json::Value::as_i64) {
            agent.step = Some(step);
        }
        if let Some(usage) = payload.get("usage").filter(|value| !value.is_null()) {
            agent.usage = Some(usage.clone());
        }

        let channel = if read("channel") == "thinking" {
            "thinking"
        } else {
            "answer"
        };
        match read("phase").as_str() {
            "text" => {
                let text = read("text");
                if text.is_empty() {
                    continue;
                }
                // `final` is the runtime re-sending the finished message after
                // its deltas: replace that channel's tail instead of printing
                // the answer twice.
                if payload.get("final") == Some(&serde_json::Value::Bool(true)) {
                    let same_channel = |step: &serde_json::Value| {
                        step.get("kind").and_then(serde_json::Value::as_str) == Some("text")
                            && step
                                .get("channel")
                                .and_then(serde_json::Value::as_str)
                                .unwrap_or("answer")
                                == channel
                    };
                    if let Some(step) = agent.steps.iter_mut().rev().find(|step| same_channel(step))
                    {
                        step["text"] = serde_json::Value::String(text);
                    }
                    continue;
                }
                let append = agent.steps.last_mut().filter(|step| {
                    step.get("kind").and_then(serde_json::Value::as_str) == Some("text")
                        && step
                            .get("channel")
                            .and_then(serde_json::Value::as_str)
                            .unwrap_or("answer")
                            == channel
                });
                match append {
                    Some(step) => {
                        let joined = format!(
                            "{}{}",
                            step.get("text")
                                .and_then(serde_json::Value::as_str)
                                .unwrap_or(""),
                            text
                        );
                        step["text"] = serde_json::Value::String(joined);
                    }
                    None => agent.steps.push(
                        serde_json::json!({ "kind": "text", "text": text, "channel": channel }),
                    ),
                }
            }
            "tool" => {
                let tool = payload
                    .get("tool")
                    .cloned()
                    .unwrap_or(serde_json::Value::Null);
                agent.steps.push(serde_json::json!({
                    "kind": "tool",
                    "name": tool.get("name").and_then(serde_json::Value::as_str).unwrap_or("tool"),
                    "args": tool.get("args").cloned().unwrap_or(serde_json::Value::Null),
                    "ok": serde_json::Value::Null,
                    "output": "",
                }));
            }
            "tool_result" => {
                let tool = payload
                    .get("tool")
                    .cloned()
                    .unwrap_or(serde_json::Value::Null);
                let name = tool
                    .get("name")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or("tool");
                let open = agent.steps.iter_mut().rev().find(|step| {
                    step.get("kind").and_then(serde_json::Value::as_str) == Some("tool")
                        && step.get("ok").is_some_and(serde_json::Value::is_null)
                        && step.get("name").and_then(serde_json::Value::as_str) == Some(name)
                });
                if let Some(step) = open {
                    step["ok"] = tool
                        .get("ok")
                        .cloned()
                        .unwrap_or(serde_json::Value::Bool(false));
                    step["output"] = tool
                        .get("output")
                        .cloned()
                        .unwrap_or_else(|| serde_json::Value::String(String::new()));
                }
            }
            "snapshot" => {
                if let Some(steps) = payload.get("steps").and_then(serde_json::Value::as_array) {
                    agent.steps = steps.clone();
                }
            }
            _ => {}
        }
    }
    out
}

/// The attachments of every user turn, first attachment first.
///
/// The record is the message metadata the send path already writes
/// (`meta.context`), not a second list kept in the UI: a conversation reopened
/// tomorrow shows the same references, and editing the composer cannot change
/// what a past turn is said to have carried.
fn collect_references(
    messages: &[Message],
    turn_for: &dyn Fn(&str) -> i64,
) -> Vec<RailReference> {
    let mut out: Vec<RailReference> = Vec::new();
    let mut index: BTreeMap<String, usize> = BTreeMap::new();

    for message in messages {
        if message.role != Role::User {
            continue;
        }
        let Some(entries) = message
            .meta
            .as_ref()
            .and_then(|meta| meta.get("context"))
            .and_then(serde_json::Value::as_array)
        else {
            continue;
        };
        let run_id = message.run_id.clone().unwrap_or_default();
        let turn = turn_for(&run_id);
        for entry in entries {
            let Some(path) = entry.get("path").and_then(serde_json::Value::as_str) else {
                continue;
            };
            if path.is_empty() {
                continue;
            }
            // Windows paths are case-insensitive: `C:\a\B.rs` and `c:\a\b.rs`
            // are one file, and two rows for it would be a lie about how much
            // the model was given.
            let key = path.to_lowercase();
            if let Some(&existing) = index.get(&key) {
                out[existing].touches += 1;
                continue;
            }
            index.insert(key, out.len());
            out.push(RailReference {
                path: path.to_string(),
                run_id: run_id.clone(),
                label: display_name(entry, path),
                kind: entry
                    .get("kind")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or("file")
                    .to_string(),
                turn,
                bytes: entry
                    .get("bytes")
                    .and_then(serde_json::Value::as_u64)
                    .unwrap_or(0),
                truncated: entry
                    .get("truncated")
                    .and_then(serde_json::Value::as_bool)
                    .unwrap_or(false),
                outside_root: entry
                    .get("outsideRoot")
                    .and_then(serde_json::Value::as_bool)
                    .unwrap_or(false),
                touches: 1,
            });
        }
    }
    out
}

/// The chip's name for an attachment, else the last path segment.
fn display_name(entry: &serde_json::Value, path: &str) -> String {
    if let Some(label) = entry.get("label").and_then(serde_json::Value::as_str)
        && !label.trim().is_empty()
    {
        return label.to_string();
    }
    let trimmed = path.trim_end_matches(['/', '\\']);
    trimmed
        .rsplit(['/', '\\'])
        .next()
        .filter(|name| !name.is_empty())
        .unwrap_or(trimmed)
        .to_string()
}

/// Walk the event stream once, pairing calls with results and collecting the
/// files the writing calls name.
fn walk_events(
    project_path: &str,
    events: &[RunEvent],
    turn_for: &dyn Fn(&str) -> i64,
    calls_per_turn: &mut BTreeMap<i64, usize>,
) -> (Vec<RailExec>, Vec<RailArtifact>, usize) {
    let root = Path::new(project_path);
    // A result belongs to the oldest call in the same run that has none yet:
    // adapters emit them in order and without a shared id.
    let mut waiting: BTreeMap<String, Vec<usize>> = BTreeMap::new();
    let mut exec: Vec<RailExec> = Vec::new();
    let mut artifacts: Vec<RailArtifact> = Vec::new();
    let mut seen: BTreeMap<String, usize> = BTreeMap::new();
    let mut outside = 0usize;
    let mut running: BTreeMap<String, bool> = BTreeMap::new();

    for event in events {
        match event.kind {
            EventKind::ToolCall => {
                let tool = event
                    .payload
                    .get("tool")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or("tool")
                    .to_string();
                let args = event.payload.get("args").cloned().unwrap_or_default();
                let turn = turn_for(&event.run_id);
                *calls_per_turn.entry(turn).or_insert(0) += 1;
                exec.push(RailExec {
                    turn,
                    run_id: event.run_id.clone(),
                    tool: tool.clone(),
                    label: label_for(&tool, &args),
                    status: "running".into(),
                    seq: event.seq,
                    at: event.at.clone(),
                });
                waiting
                    .entry(event.run_id.clone())
                    .or_default()
                    .push(exec.len() - 1);

                for candidate in artifact_candidates(&tool, &args) {
                    let Some(path) = resolve_existing(root, &candidate) else {
                        continue;
                    };
                    if !is_within(root, &path) {
                        outside += 1;
                        continue;
                    }
                    let key = path.to_string_lossy().to_lowercase();
                    if let Some(&index) = seen.get(&key) {
                        artifacts[index].touches += 1;
                        continue;
                    }
                    let bytes = std::fs::metadata(&path).map(|meta| meta.len()).unwrap_or(0);
                    seen.insert(key, artifacts.len());
                    artifacts.push(RailArtifact {
                        path: path.to_string_lossy().to_string(),
                        relative: relative_to(root, &path),
                        bytes,
                        turn,
                        run_id: event.run_id.clone(),
                        seq: event.seq,
                        touches: 1,
                        ext: path
                            .extension()
                            .map(|ext| ext.to_string_lossy().to_lowercase())
                            .unwrap_or_default(),
                    });
                }
            }
            EventKind::ToolResult => {
                if let Some(index) = waiting.get_mut(&event.run_id).and_then(|queue| queue.pop()) {
                    exec[index].status = match event.payload.get("ok").and_then(|ok| ok.as_bool()) {
                        Some(true) => "done".into(),
                        Some(false) => "failed".into(),
                        // A result without an outcome still proves the call
                        // returned; guessing "failed" would be a lie.
                        None => "done".into(),
                    };
                }
            }
            EventKind::TurnStarted => {
                running.insert(event.run_id.clone(), true);
            }
            EventKind::TurnCompleted | EventKind::Aborted => {
                running.insert(event.run_id.clone(), false);
            }
            _ => {}
        }
    }

    // A call whose run already ended will never get a result. Saying "running"
    // there would leave a spinner on screen forever.
    for row in &mut exec {
        if row.status == "running" && running.get(&row.run_id) != Some(&true) {
            row.status = "no_result".into();
        }
    }

    (exec, artifacts, outside)
}

/// The text the reader sees for one call: the command line, the path, or the
/// first scalar argument.
pub(crate) fn label_for(tool: &str, args: &serde_json::Value) -> String {
    let mut text = match args {
        serde_json::Value::String(raw) => raw.clone(),
        serde_json::Value::Object(map) => {
            for key in [
                "command",
                "cmd",
                "path",
                "file",
                "file_path",
                "query",
                "url",
            ] {
                if let Some(value) = map.get(key).and_then(serde_json::Value::as_str) {
                    return collapse(value);
                }
            }
            map.values()
                .find_map(serde_json::Value::as_str)
                .map(str::to_string)
                .unwrap_or_default()
        }
        other => other.to_string(),
    };
    if text.trim().is_empty() {
        text = tool.to_string();
    }
    collapse(&text)
}

fn collapse(text: &str) -> String {
    let joined = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if joined.chars().count() <= 160 {
        return joined;
    }
    let cut = joined.chars().take(157).collect::<String>();
    format!("{cut}...")
}

/// Paths a call names, when the call is one that writes.
fn artifact_candidates(tool: &str, args: &serde_json::Value) -> Vec<String> {
    let lowered = tool.to_ascii_lowercase();
    if WRITE_TOOLS.contains(&lowered.as_str()) {
        let mut found = Vec::new();
        for key in [
            "path",
            "file",
            "file_path",
            "filePath",
            "target",
            "filename",
            "name",
        ] {
            if let Some(value) = args.get(key).and_then(serde_json::Value::as_str)
                && looks_like_path(value)
            {
                found.push(clean(value));
            }
        }
        if found.is_empty() {
            // Some runtimes send a patch body instead of a path.
            collect_paths(args, &mut found);
        }
        return found;
    }
    if SHELL_TOOLS.contains(&lowered.as_str()) {
        // `> out.md` / `>> out.md`: the artifact is the redirection target, not
        // every word of the command.
        let command = args
            .as_str()
            .map(str::to_string)
            .or_else(|| {
                args.get("command")
                    .and_then(serde_json::Value::as_str)
                    .map(str::to_string)
            })
            .unwrap_or_default();
        return redirects(&command);
    }
    Vec::new()
}

fn collect_paths(value: &serde_json::Value, out: &mut Vec<String>) {
    match value {
        serde_json::Value::String(text) if looks_like_path(text) => out.push(clean(text)),
        serde_json::Value::Array(items) => {
            for item in items {
                collect_paths(item, out);
            }
        }
        serde_json::Value::Object(map) => {
            for item in map.values() {
                collect_paths(item, out);
            }
        }
        _ => {}
    }
}

fn looks_like_path(text: &str) -> bool {
    let trimmed = text.trim();
    !trimmed.is_empty()
        && trimmed.len() <= 400
        && !trimmed.contains(char::is_whitespace)
        && !trimmed.to_ascii_lowercase().starts_with("http")
        && (trimmed.contains('/') || trimmed.contains('\\'))
}

fn clean(text: &str) -> String {
    text.trim()
        .trim_matches(|c| c == '"' || c == '\'' || c == '`')
        .to_string()
}

/// Redirection targets of one shell command line.
fn redirects(command: &str) -> Vec<String> {
    let mut out = Vec::new();
    let tokens: Vec<&str> = command.split_whitespace().collect();
    for (index, token) in tokens.iter().enumerate() {
        if *token == ">" || *token == ">>" {
            if let Some(next) = tokens.get(index + 1) {
                out.push(clean(next));
            }
            continue;
        }
        if let Some(rest) = token.strip_prefix(">>").or_else(|| token.strip_prefix('>'))
            && !rest.is_empty()
        {
            out.push(clean(rest));
        }
    }
    out.retain(|candidate| looks_like_path(candidate) || candidate.contains('.'));
    out
}

fn resolve_existing(root: &Path, candidate: &str) -> Option<PathBuf> {
    let path = Path::new(candidate);
    let joined = if path.is_absolute() {
        path.to_path_buf()
    } else {
        root.join(path)
    };
    let canonical = std::fs::canonicalize(&joined).ok()?;
    canonical.is_file().then_some(canonical)
}

fn relative_to(root: &Path, path: &Path) -> String {
    match std::fs::canonicalize(root) {
        Ok(root) => path
            .strip_prefix(&root)
            .map(|rest| rest.to_string_lossy().replace('\\', "/"))
            .unwrap_or_else(|_| path.to_string_lossy().to_string()),
        Err(_) => path.to_string_lossy().to_string(),
    }
}

/// URLs the transcript cites, numbered by first appearance.
fn collect_sources(messages: &[Message], turn_for: &dyn Fn(&str) -> i64) -> Vec<RailSource> {
    let mut sources: Vec<RailSource> = Vec::new();
    let mut index: BTreeMap<String, usize> = BTreeMap::new();

    for message in messages {
        if message.role != Role::Assistant {
            continue;
        }
        let run_id = message.run_id.clone().unwrap_or_default();
        for url in urls_in(&message.text) {
            if index.contains_key(&url) {
                continue;
            }
            let number = sources.len() + 1;
            index.insert(url.clone(), number);
            sources.push(RailSource {
                number,
                host: host_of(&url),
                url,
                turn: turn_for(&run_id),
                run_id: run_id.clone(),
                origin: "message".into(),
            });
        }
    }
    sources
}

/// Every `http(s)` URL outside code fences, in reading order.
fn urls_in(text: &str) -> Vec<String> {
    let mut found = Vec::new();
    let mut fenced = false;
    for line in text.lines() {
        if line.trim_start().starts_with("```") {
            fenced = !fenced;
            continue;
        }
        if fenced {
            continue;
        }
        found.extend(scan_urls(line));
    }
    found
}

fn scan_urls(line: &str) -> Vec<String> {
    let bytes = line.as_bytes();
    let mut out = Vec::new();
    let mut cursor = 0usize;
    while cursor < bytes.len() {
        let rest = &line[cursor..];
        let start = if rest.starts_with("https://") {
            Some(8)
        } else if rest.starts_with("http://") {
            Some(7)
        } else {
            None
        };
        let Some(_) = start else {
            // Advance one character, not one byte: the line may be Chinese.
            cursor += rest.chars().next().map(char::len_utf8).unwrap_or(1);
            continue;
        };
        let charset: &[char] = &[
            ' ', '\t', '\n', ')', ']', '}', '>', '"', '\'', '`', '<', '，', '。', '；', '）', '】',
        ];
        let tail: String = rest
            .chars()
            .take_while(|c| !charset.contains(c))
            .collect::<String>();
        let trimmed = tail.trim_end_matches(['.', ',', ';', ':', '!', '?', '/']);
        if trimmed.len() > 10 {
            out.push(trimmed.to_string());
        }
        cursor += tail.chars().map(char::len_utf8).sum::<usize>().max(1);
    }
    out
}

fn host_of(url: &str) -> String {
    url.split("://")
        .nth(1)
        .and_then(|rest| rest.split('/').next())
        .unwrap_or(url)
        .to_string()
}

/// Latest usage event wins: runtimes report a snapshot per message, so summing
/// them would multiply the bill.
fn collect_usage(events: &[RunEvent]) -> Option<RailUsage> {
    let mut usage: Option<RailUsage> = None;
    for event in events {
        if event.kind != EventKind::Usage {
            continue;
        }
        let raw = event.payload.get("usage").unwrap_or(&event.payload);
        let number = |key: &str| {
            raw.get(key)
                .and_then(serde_json::Value::as_f64)
                .map(|value| value.max(0.0) as u64)
                .unwrap_or(0)
        };
        let cost = raw
            .get("cost")
            .and_then(|cost| {
                cost.get("total")
                    .and_then(serde_json::Value::as_f64)
                    .or_else(|| cost.as_f64())
            })
            .unwrap_or(0.0);
        let folded = usage.get_or_insert_with(RailUsage::default);
        folded.input = number("input");
        folded.output = number("output");
        folded.cache_read = number("cacheRead").max(number("cache_read"));
        folded.cache_write = number("cacheWrite").max(number("cache_write"));
        folded.cost = cost;
        folded.events += 1;
    }
    usage
}

/// The browser pane: a local page when a turn produced one, otherwise an
/// explanation. It never renders a page it did not read from this project.
fn browser_for(artifacts: &[RailArtifact]) -> RailBrowser {
    let Some(page) = artifacts
        .iter()
        .rev()
        .find(|artifact| artifact.ext == "html" || artifact.ext == "htm")
    else {
        return RailBrowser {
            state: "unavailable".into(),
            reason: "no local page in this session".into(),
            page: None,
        };
    };
    if page.bytes > MAX_PAGE_BYTES {
        return RailBrowser {
            state: "unavailable".into(),
            reason: format!(
                "{} is {} KiB, larger than the preview limit",
                page.relative,
                page.bytes / 1024
            ),
            page: None,
        };
    }
    match std::fs::read_to_string(&page.path) {
        Ok(html) => RailBrowser {
            state: "local".into(),
            reason: String::new(),
            page: Some(RailPage {
                relative: page.relative.clone(),
                // Empty sandbox: scripts, forms, same-origin and navigation are
                // all off. The page is data, not an application.
                sandbox: String::new(),
                html,
                bytes: page.bytes,
            }),
        },
        Err(error) => RailBrowser {
            state: "unavailable".into(),
            reason: format!("{}: {error}", page.relative),
            page: None,
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::RunStatus;

    fn project(name: &str) -> PathBuf {
        let root = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("rail")
            .join(name);
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(root.join("out")).expect("create project");
        root
    }

    fn run(id: &str, status: RunStatus) -> Run {
        Run {
            id: id.to_string(),
            session_id: "s".into(),
            status,
            started_at: "2026-11-13T00:00:00Z".into(),
            ended_at: Some("2026-11-13T00:00:01Z".into()),
            error: None,
            prompt: "go".into(),
        }
    }

    fn message(seq: i64, role: Role, run_id: &str, text: &str) -> Message {
        Message {
            id: format!("m{seq}"),
            session_id: "s".into(),
            run_id: Some(run_id.to_string()),
            role,
            text: text.to_string(),
            seq,
            created_at: "2026-11-13T00:00:00Z".into(),
            meta: None,
        }
    }

    fn event(run_id: &str, seq: i64, kind: EventKind, payload: serde_json::Value) -> RunEvent {
        RunEvent::new("s", run_id, seq, kind, payload)
    }

    /// The pane lists agents, not observations: a child that starts, streams a
    /// snapshot and finishes is one row, and a second child is a second row.
    #[test]
    fn delegated_agents_are_folded_into_one_row_each() {
        let root = project("subagents");
        let runs = vec![run("s:r1", RunStatus::Completed)];
        let events = vec![
            event(
                "s:r1",
                1,
                EventKind::SubAgent,
                serde_json::json!({
                    "agentId": "call_1#0", "name": "scout", "task": "count files",
                    "model": "auto/fast", "status": "running", "phase": "started"
                }),
            ),
            event(
                "s:r1",
                2,
                EventKind::SubAgent,
                serde_json::json!({
                    "agentId": "call_1#1", "name": "reviewer", "task": "read the diff",
                    "status": "running", "phase": "started"
                }),
            ),
            event(
                "s:r1",
                3,
                EventKind::SubAgent,
                serde_json::json!({
                    "agentId": "call_1#0", "status": "running", "phase": "snapshot",
                    "steps": [
                        { "kind": "text", "text": "counting" },
                        { "kind": "tool", "name": "bash", "args": { "command": "ls" } },
                        { "kind": "tool_result", "name": "bash", "ok": true, "output": "42" }
                    ]
                }),
            ),
            event(
                "s:r1",
                4,
                EventKind::SubAgent,
                serde_json::json!({
                    "agentId": "call_1#0", "status": "completed", "phase": "completed",
                    "result": "42 files"
                }),
            ),
            event(
                "s:r1",
                5,
                EventKind::SubAgent,
                serde_json::json!({
                    "agentId": "call_1#1", "status": "failed", "phase": "failed",
                    "error": "model quota exhausted"
                }),
            ),
        ];
        let view = build(&root.to_string_lossy(), &[], &runs, &events);
        assert_eq!(view.subagents.len(), 2, "{:?}", view.subagents);
        let scout = &view.subagents[0];
        assert_eq!(scout.id, "call_1#0");
        assert_eq!(scout.name, "scout");
        assert_eq!(scout.model, "auto/fast");
        assert_eq!(scout.status, "completed");
        assert_eq!(scout.result, "42 files");
        assert_eq!(scout.turn, 1);
        assert_eq!(scout.run_id, "s:r1");
        assert_eq!(scout.steps.len(), 3, "{:?}", scout.steps);
        assert_eq!(scout.steps[1]["name"], "bash");
        assert_eq!(scout.steps[2]["output"], "42");
        let reviewer = &view.subagents[1];
        assert_eq!(reviewer.status, "failed");
        assert_eq!(reviewer.error, "model quota exhausted");
    }

    #[test]
    fn a_written_file_is_listed_with_its_turn() {        let root = project("written");
        std::fs::write(root.join("out/report.md"), "hello").expect("write");
        let runs = vec![run("s:r1", RunStatus::Completed)];
        let events = vec![
            event("s:r1", 1, EventKind::TurnStarted, serde_json::json!({})),
            event(
                "s:r1",
                2,
                EventKind::ToolCall,
                serde_json::json!({ "tool": "write", "args": { "path": "out/report.md" } }),
            ),
            event(
                "s:r1",
                3,
                EventKind::ToolResult,
                serde_json::json!({ "tool": "write", "ok": true }),
            ),
        ];
        let view = build(&root.to_string_lossy(), &[], &runs, &events);
        assert_eq!(view.artifacts.len(), 1);
        assert_eq!(view.artifacts[0].relative, "out/report.md");
        assert_eq!(view.artifacts[0].turn, 1);
        assert_eq!(view.artifacts[0].run_id, "s:r1");
        assert_eq!(view.artifacts[0].bytes, 5);
        assert_eq!(view.exec[0].status, "done");
        assert_eq!(view.exec[0].label, "out/report.md");
    }

    #[test]
    fn a_path_outside_the_project_is_counted_and_never_listed() {
        let root = project("outside");
        let outside = root.parent().expect("parent").join("outside-secret.md");
        std::fs::write(&outside, "secret").expect("write outside");
        let runs = vec![run("s:r1", RunStatus::Completed)];
        let events = vec![
            event(
                "s:r1",
                1,
                EventKind::ToolCall,
                serde_json::json!({ "tool": "write", "args": { "path": outside } }),
            ),
            event(
                "s:r1",
                2,
                EventKind::ToolCall,
                serde_json::json!({ "tool": "write", "args": { "path": "../outside-secret.md" } }),
            ),
        ];
        let view = build(&root.to_string_lossy(), &[], &runs, &events);
        assert!(view.artifacts.is_empty(), "{:?}", view.artifacts);
        assert_eq!(view.outside_paths, 2);
    }

    #[test]
    fn a_shell_redirection_names_the_artifact() {
        let root = project("redirect");
        std::fs::write(root.join("out/summary.txt"), "x").expect("write");
        let runs = vec![run("s:r1", RunStatus::Completed)];
        let events = vec![event(
            "s:r1",
            1,
            EventKind::ToolCall,
            serde_json::json!({ "tool": "command", "args": "echo x >> out/summary.txt" }),
        )];
        let view = build(&root.to_string_lossy(), &[], &runs, &events);
        assert_eq!(view.artifacts.len(), 1);
        assert_eq!(view.artifacts[0].relative, "out/summary.txt");
    }

    #[test]
    fn a_read_only_tool_produces_no_artifact() {
        let root = project("read");
        std::fs::write(root.join("out/report.md"), "hello").expect("write");
        let runs = vec![run("s:r1", RunStatus::Completed)];
        let events = vec![event(
            "s:r1",
            1,
            EventKind::ToolCall,
            serde_json::json!({ "tool": "read", "args": { "path": "out/report.md" } }),
        )];
        let view = build(&root.to_string_lossy(), &[], &runs, &events);
        assert!(view.artifacts.is_empty());
        assert_eq!(view.exec.len(), 1);
    }

    #[test]
    fn sources_are_numbered_by_first_appearance_and_carry_their_turn() {
        let runs = vec![
            run("s:r1", RunStatus::Completed),
            run("s:r2", RunStatus::Completed),
        ];
        let messages = vec![
            message(1, Role::User, "s:r1", "see https://user.example/x"),
            message(
                2,
                Role::Assistant,
                "s:r1",
                "first [1] and second [2]\n```\nhttps://fenced.example/ignore\n```",
            ),
            message(3, Role::User, "s:r2", "again"),
            message(
                4,
                Role::Assistant,
                "s:r2",
                "same https://docs.example.com/a/b and new https://news.example.org/x.",
            ),
        ];
        let view = build("D:/none", &messages, &runs, &[]);
        let urls: Vec<(&str, usize, i64)> = view
            .sources
            .iter()
            .map(|source| (source.url.as_str(), source.number, source.turn))
            .collect();
        assert_eq!(
            urls,
            vec![
                ("https://docs.example.com/a/b", 1, 2),
                ("https://news.example.org/x", 2, 2),
            ]
        );
        assert_eq!(view.sources[0].host, "docs.example.com");
    }

    #[test]
    fn a_call_without_a_result_is_not_left_running_after_its_run_ended() {
        let runs = vec![run("s:r1", RunStatus::Aborted)];
        let events = vec![
            event(
                "s:r1",
                1,
                EventKind::ToolCall,
                serde_json::json!({ "tool": "read", "args": { "path": "a.md" } }),
            ),
            event("s:r1", 2, EventKind::Aborted, serde_json::json!({})),
        ];
        let view = build("D:/none", &[], &runs, &events);
        assert_eq!(view.exec[0].status, "no_result");
        assert_eq!(view.turns[0].calls, 1);
        assert!(!view.empty, "a turn with a call is not an empty session");
    }

    #[test]
    fn usage_takes_the_latest_snapshot_instead_of_summing_messages() {
        let events = vec![
            event(
                "s:r1",
                1,
                EventKind::Usage,
                serde_json::json!({ "usage": { "input": 10, "output": 2 } }),
            ),
            event(
                "s:r1",
                2,
                EventKind::Usage,
                serde_json::json!({
                    "usage": { "input": 240, "output": 9, "cacheRead": 6912, "cost": { "total": 0.5 } }
                }),
            ),
        ];
        let view = build("D:/none", &[], &[], &events);
        let usage = view.usage.expect("usage");
        assert_eq!(usage.input, 240);
        assert_eq!(usage.cache_read, 6912);
        assert!((usage.cost - 0.5).abs() < f64::EPSILON);
        assert_eq!(usage.events, 2);
    }

    #[test]
    fn the_attachments_of_each_turn_appear_once_with_their_turn() {
        // Two turns attach the same file: the pane lists it once, says which
        // turn introduced it, and reports that both turns used it.
        let runs = vec![
            run("s:r1", RunStatus::Completed),
            run("s:r2", RunStatus::Completed),
        ];
        let mut first = message(1, Role::User, "s:r1", "read this");
        first.meta = Some(serde_json::json!({
            "context": [
                {
                    "kind": "file",
                    "path": "D:/project/src/main.rs",
                    "bytes": 1200,
                    "chars": 1200,
                    "truncated": false,
                    "outsideRoot": false
                },
                {
                    "kind": "dir",
                    "path": "D:/project/docs",
                    "label": "需求文档",
                    "bytes": 0,
                    "chars": 40,
                    "truncated": true,
                    "outsideRoot": true
                },
            ],
        }));
        let mut second = message(3, Role::User, "s:r2", "and again");
        second.meta = Some(serde_json::json!({
            "context": [
                {
                    "kind": "file",
                    "path": "d:/PROJECT/src/main.rs",
                    "bytes": 1200,
                    "chars": 1200,
                    "truncated": false,
                    "outsideRoot": false
                }
            ],
        }));

        let view = build("D:/project", &[first, second], &runs, &[]);
        assert_eq!(view.references.len(), 2, "one row per distinct path");
        let file = &view.references[0];
        assert_eq!(file.label, "main.rs", "falls back to the file name");
        assert_eq!(file.turn, 1);
        assert_eq!(file.run_id, "s:r1");
        assert_eq!(file.touches, 2, "case-insensitive on Windows");
        let dir = &view.references[1];
        assert_eq!(dir.label, "需求文档");
        assert_eq!(dir.kind, "dir");
        assert!(dir.truncated && dir.outside_root);
        assert!(!view.empty, "attachments alone are data worth showing");
    }

    #[test]
    fn a_report_with_nothing_in_it_is_empty_not_fabricated() {
        let view = build("D:/none", &[], &[], &[]);
        assert!(view.empty);
        assert_eq!(view.browser.state, "unavailable");
        assert!(view.browser.page.is_none());
    }

    #[test]
    fn an_html_artifact_becomes_a_sandboxed_page() {
        let root = project("page");
        std::fs::write(root.join("out/page.html"), "<h1>hi</h1>").expect("write");
        let runs = vec![run("s:r1", RunStatus::Completed)];
        let events = vec![event(
            "s:r1",
            1,
            EventKind::ToolCall,
            serde_json::json!({ "tool": "write", "args": { "path": "out/page.html" } }),
        )];
        let view = build(&root.to_string_lossy(), &[], &runs, &events);
        assert_eq!(view.browser.state, "local");
        let page = view.browser.page.expect("page");
        assert_eq!(page.relative, "out/page.html");
        assert_eq!(
            page.sandbox, "",
            "no scripts, no same-origin, no navigation"
        );
        assert!(page.html.contains("hi"));
    }
}
