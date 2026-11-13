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

/// The four panes in one read.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RailView {
    pub turns: Vec<RailTurn>,
    pub exec: Vec<RailExec>,
    pub artifacts: Vec<RailArtifact>,
    pub sources: Vec<RailSource>,
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
    let usage = collect_usage(events);
    let browser = browser_for(&artifacts);

    let empty = turns.is_empty() && exec.is_empty() && artifacts.is_empty() && sources.is_empty();

    RailView {
        turns,
        exec,
        artifacts,
        sources,
        usage,
        browser,
        outside_paths,
        empty,
    }
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

    #[test]
    fn a_written_file_is_listed_with_its_turn() {
        let root = project("written");
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
