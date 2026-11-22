//! Sub-agents as files pi already understands.
//!
//! pi has no sub-agents in its core: the official `subagent` extension scans
//! `<agent-dir>/agents/*.md` (and `<cwd>/.pi/agents/*.md` when the caller asks
//! for project scope) and turns every file with a `name` and a `description`
//! into one delegatable agent. The file *is* the interface:
//!
//! ```text
//! ---
//! name: reviewer
//! description: Reviews a diff for correctness and risk
//! tools: read, grep, find, ls
//! model: auto/gpt-6.1-sol        # optional; omitted means "inherit"
//! ---
//!
//! You are a reviewer. Read the diff, then ...
//! ```
//!
//! So the honest way to make an agent created in this app usable is to write
//! that file, not to invent a second registry the runtime has never heard of.
//! Three rules follow from it:
//!
//! 1. **Same directory as pi's own agents.** Anything else is invisible to the
//!    tool, and the model would be picking names that cannot resolve.
//! 2. **Only ever delete files this app wrote.** Each file carries `anvil: <id>`
//!    in its front matter; pi ignores the key. A file without it belongs to the
//!    user or to pi's example, and stays even when no agent row points at it.
//! 3. **The description is the selection input.** The tool's schema does not
//!    carry a roster, so the parent model only knows which agents exist if its
//!    prompt says so — [`roster`] builds that block from the same files, which
//!    keeps this app's agents and pi's own on one list.

use std::path::{Path, PathBuf};

use crate::error::Result;

/// Front-matter key that marks a file as written by this app.
pub const MARKER: &str = "anvil";

/// One agent row that should be reachable by the `subagent` tool.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct DeclaredAgent {
    /// Catalog id; the ownership key. Never shown to the runtime.
    pub id: String,
    /// pi agent name (the `agent:` argument), unique inside the directory.
    pub slug: String,
    /// Display name, used when the description is empty.
    pub title: String,
    /// One line the parent model reads when choosing. Required by pi.
    pub description: String,
    /// `provider/model`; empty means the child inherits the parent's model.
    pub model: String,
    /// Tool allowlist for the child; empty means pi's default set.
    pub tools: Vec<String>,
    /// The body of the file: the child's appended system prompt.
    pub prompt: String,
}

/// What one sync wrote, removed, refused and could not use.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SyncReport {
    /// Files created or replaced.
    pub written: usize,
    /// Files deleted because their agent is gone, disabled or renamed.
    pub removed: usize,
    /// `slug: reason` for names already taken by a file this app does not own.
    pub conflicts: Vec<String>,
    /// `slug: reason` for agents the runtime would refuse to load.
    pub skipped: Vec<String>,
}

impl SyncReport {
    /// True when the directory already matched the rows.
    #[must_use]
    pub fn is_noop(&self) -> bool {
        self.written == 0 && self.removed == 0 && self.conflicts.is_empty() && self.skipped.is_empty()
    }
}

/// A file found in the agents directory.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AgentFile {
    pub name: String,
    pub description: String,
    /// The `anvil:` marker, when this app wrote the file.
    pub owner: Option<String>,
    pub path: PathBuf,
}

/// Split front matter from body. Returns `(front, body)`; a file without front
/// matter yields an empty front and the whole text as body.
fn split_front(text: &str) -> (String, String) {
    let trimmed = text.strip_prefix('\u{feff}').unwrap_or(text);
    let Some(rest) = trimmed.strip_prefix("---") else {
        return (String::new(), trimmed.to_string());
    };
    let rest = rest.strip_prefix("\r\n").or_else(|| rest.strip_prefix('\n')).unwrap_or(rest);
    let mut front = String::new();
    let mut body_from = None;
    let mut offset = 0usize;
    for line in rest.split_inclusive('\n') {
        let bare = line.trim_end_matches(['\r', '\n']);
        if bare.trim() == "---" {
            body_from = Some(offset + line.len());
            break;
        }
        front.push_str(line);
        offset += line.len();
    }
    match body_from {
        Some(at) => (front, rest[at..].to_string()),
        None => (String::new(), trimmed.to_string()),
    }
}

/// Read one front-matter value, unquoting what [`quote`] wrote.
fn value_of<'a>(front: &'a str, key: &str) -> Option<&'a str> {
    let prefix = format!("{key}:");
    for line in front.lines() {
        let line = line.trim_start();
        let Some(rest) = line.strip_prefix(&prefix) else {
            continue;
        };
        return Some(rest.trim()).filter(|value| !value.is_empty());
    }
    None
}

/// Strip the quoting a YAML writer added around a scalar.
fn unquote(raw: &str) -> String {
    let text = raw.trim();
    if text.len() >= 2 && text.starts_with('"') && text.ends_with('"') {
        let inner = &text[1..text.len() - 1];
        let mut out = String::with_capacity(inner.len());
        let mut chars = inner.chars();
        while let Some(ch) = chars.next() {
            if ch != '\\' {
                out.push(ch);
                continue;
            }
            match chars.next() {
                Some('n') => out.push('\n'),
                Some('t') => out.push('\t'),
                Some(other) => out.push(other),
                None => out.push('\\'),
            }
        }
        return out;
    }
    if text.len() >= 2 && text.starts_with('\'') && text.ends_with('\'') {
        return text[1..text.len() - 1].replace("''", "'");
    }
    text.to_string()
}

/// A scalar that a real YAML parser will read back unchanged.
///
/// Descriptions are prose: they contain colons, `#`, quotes and commas. An
/// unquoted `Design: notes` would parse as a mapping and pi would drop the file
/// for having no string description, so anything not obviously plain is quoted.
fn quote(text: &str) -> String {
    let plain = !text.is_empty()
        && !text.starts_with(['-', '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>', '@', '`', '"', '\'', '%'])
        && !text.contains([':', '#', '"', '\'', '\n', '\r', '\t'])
        && !text.ends_with(' ')
        && !text.starts_with(' ');
    if plain {
        text.to_string()
    } else {
        format!(
            "\"{}\"",
            text.replace('\\', "\\\\").replace('"', "\\\"").replace('\n', "\\n").replace('\t', "\\t")
        )
    }
}

/// Parse one file the way the extension will: name and description decide
/// whether it exists at all, so a file missing either is not an agent.
fn read_file(path: &Path) -> Option<AgentFile> {
    let text = std::fs::read_to_string(path).ok()?;
    let (front, _) = split_front(&text);
    if front.trim().is_empty() {
        return None;
    }
    let name = value_of(&front, "name").map(unquote)?;
    let description = value_of(&front, "description").map(unquote)?;
    let owner = value_of(&front, MARKER).map(unquote);
    Some(AgentFile {
        name,
        description,
        owner,
        path: path.to_path_buf(),
    })
}

/// Every agent file in `dir`, sorted by name.
#[must_use]
pub fn scan(dir: &Path) -> Vec<AgentFile> {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return Vec::new();
    };
    let mut files: Vec<AgentFile> = entries
        .filter_map(std::result::Result::ok)
        .map(|entry| entry.path())
        .filter(|path| path.extension().is_some_and(|ext| ext.eq_ignore_ascii_case("md")))
        .filter(|path| path.is_file())
        .filter_map(|path| read_file(&path))
        .collect();
    files.sort_by(|a, b| a.name.cmp(&b.name));
    files
}

/// The file body for one agent.
fn render(agent: &DeclaredAgent) -> String {
    let mut out = String::from("---\n");
    out.push_str(&format!("name: {}\n", agent.slug));
    let description = agent.description.trim();
    let description = if description.is_empty() {
        agent.title.trim()
    } else {
        description
    };
    out.push_str(&format!("description: {}\n", quote(description)));
    if !agent.tools.is_empty() {
        out.push_str(&format!("tools: {}\n", agent.tools.join(", ")));
    }
    // A bare id can resolve ambiguously across providers; pi then fails the
    // child with "Model ... is ambiguous". Inheriting is the safer default.
    if agent.model.contains('/') {
        out.push_str(&format!("model: {}\n", quote(&agent.model)));
    }
    out.push_str(&format!("{MARKER}: {}\n", quote(&agent.id)));
    out.push_str("---\n\n");
    let prompt = agent.prompt.trim();
    if prompt.is_empty() {
        out.push_str(&format!("You are {}. ", description));
        out.push_str("Do the task you are given, then answer with what you found — nothing else.\n");
    } else {
        out.push_str(prompt);
        out.push('\n');
    }
    out
}

/// Write `declared` into `dir`, removing files this app wrote for agents that
/// are no longer declared. Files it does not own are never touched.
///
/// Returns what happened instead of failing on one bad row: a name clash on one
/// agent must not stop the other nine from being saved.
pub fn sync(dir: &Path, declared: &[DeclaredAgent]) -> Result<SyncReport> {
    std::fs::create_dir_all(dir)?;
    let mut report = SyncReport::default();
    let existing = scan(dir);

    for agent in declared {
        if agent.slug.trim().is_empty() {
            report.skipped.push("(no name): the agent has no name".to_string());
            continue;
        }
        if agent.description.trim().is_empty() && agent.title.trim().is_empty() {
            // pi drops a file without a description, so writing one would only
            // pretend the agent works.
            report
                .skipped
                .push(format!("{}: no description, so the runtime would ignore it", agent.slug));
            continue;
        }
        let target = dir.join(format!("{}.md", agent.slug));
        if let Some(other) = existing.iter().find(|file| file.path == target) {
            let mine = other.owner.as_deref() == Some(agent.id.as_str());
            if !mine {
                report.conflicts.push(format!(
                    "{}: a file this app did not write already uses that name",
                    agent.slug
                ));
                continue;
            }
        }
        let body = render(agent);
        // Rewriting identical bytes on every save would touch mtimes for no
        // reason; the extension re-reads on every call either way.
        let same = std::fs::read_to_string(&target).is_ok_and(|old| old == body);
        if !same {
            std::fs::write(&target, body)?;
            report.written += 1;
        }
    }

    // Files this app wrote whose agent is gone, disabled, renamed, or no longer
    // a sub agent at all. Ownership is the marker, never the file name: the user
    // may have edited the body, and a rename leaves the old file behind.
    let live: Vec<(&str, &str)> = declared
        .iter()
        .map(|agent| (agent.id.as_str(), agent.slug.as_str()))
        .collect();
    for file in &existing {
        let Some(owner) = file.owner.as_deref() else {
            continue;
        };
        let keep = live
            .iter()
            .any(|(id, slug)| *id == owner && *slug == file.name);
        if keep {
            continue;
        }
        if std::fs::remove_file(&file.path).is_ok() {
            report.removed += 1;
        }
    }

    Ok(report)
}

/// The block appended to a session's system prompt: which agents exist, and when
/// not to use them.
///
/// Empty when there is nothing to delegate to, or when the agent's policy is
/// `never` — in that case the caller also drops `subagent` from the allowlist, so
/// a roster would describe a tool the model does not have.
#[must_use]
pub fn roster(dir: &Path, policy: &str, concurrency: u32) -> String {
    if policy == "never" {
        return String::new();
    }
    let files = scan(dir);
    if files.is_empty() {
        return String::new();
    }
    let mut out = String::from("## 子 agent（委派）\n\n");
    out.push_str(
        "下面这些子 agent 可以直接用 `subagent` 工具调用：`agent` 填左边的名字，`task` 写要它做的事。每个子 agent 是独立进程、独立上下文，适合边界清楚、能独立完成、结论能压缩成一段话的活。\n\n",
    );
    for file in &files {
        out.push_str(&format!("- `{}` — {}\n", file.name, file.description));
    }
    out.push_str("\n规则：\n");
    out.push_str("- 先判断值不值得委派：一句话能做完、或者需要你手上已有上下文的事，自己做更快。\n");
    out.push_str(
        "- 只有名字和任务真的对得上才用。上面没有合适的就自己做，不要硬套一个名字，也不要编名字——编出来的名字会直接失败。\n",
    );
    if policy == "explicit" {
        out.push_str("- 用户没有点名某个子 agent 时不要委派：自己完成。\n");
    }
    if concurrency > 0 {
        out.push_str(&format!("- 同时最多并行 {concurrency} 个子任务。\n"));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "anvil-subagents-{name}-{}",
            std::process::id()
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("scratch dir");
        dir
    }

    fn agent(id: &str, slug: &str, description: &str) -> DeclaredAgent {
        DeclaredAgent {
            id: id.to_string(),
            slug: slug.to_string(),
            title: slug.to_string(),
            description: description.to_string(),
            model: String::new(),
            tools: vec!["read".into(), "grep".into()],
            prompt: "You review diffs.".to_string(),
        }
    }

    #[test]
    fn writes_a_file_pi_would_load() {
        let dir = scratch("write");
        let report = sync(&dir, &[agent("ag-1", "reviewer", "Reviews a diff: correctness, risk")])
            .expect("sync");
        assert_eq!(report.written, 1);
        let text = std::fs::read_to_string(dir.join("reviewer.md")).expect("file");
        assert!(text.starts_with("---\nname: reviewer\n"));
        // A colon in prose must not turn the description into a mapping.
        assert!(text.contains("description: \"Reviews a diff: correctness, risk\""));
        assert!(text.contains("tools: read, grep"));
        assert!(text.contains("anvil: ag-1"));
        assert!(text.contains("You review diffs."));

        let scanned = scan(&dir);
        assert_eq!(scanned.len(), 1);
        assert_eq!(scanned[0].name, "reviewer");
        assert_eq!(scanned[0].description, "Reviews a diff: correctness, risk");
        assert_eq!(scanned[0].owner.as_deref(), Some("ag-1"));
    }

    #[test]
    fn rewrites_only_when_the_text_changed() {
        let dir = scratch("idempotent");
        let mut row = agent("ag-1", "reviewer", "Reviews a diff");
        sync(&dir, &[row.clone()]).expect("first");
        let again = sync(&dir, &[row.clone()]).expect("second");
        assert_eq!(again.written, 0, "identical bytes must not be rewritten");

        row.description = "Reviews a diff and a plan".to_string();
        let changed = sync(&dir, &[row]).expect("third");
        assert_eq!(changed.written, 1);
    }

    #[test]
    fn never_deletes_a_file_it_did_not_write() {
        let dir = scratch("foreign");
        std::fs::write(
            dir.join("planner.md"),
            "---\nname: planner\ndescription: from pi\n---\n\nPlan.\n",
        )
        .expect("foreign file");

        // Same name, not ours: refused, and the file survives untouched.
        let report = sync(&dir, &[agent("ag-1", "planner", "mine")]).expect("sync");
        assert_eq!(report.written, 0);
        assert_eq!(report.conflicts.len(), 1);
        assert!(dir.join("planner.md").is_file());

        // Rows with no file of their own: the foreign file still survives.
        let empty = sync(&dir, &[]).expect("empty sync");
        assert_eq!(empty.removed, 0);
        assert!(dir.join("planner.md").is_file());
    }

    #[test]
    fn removal_follows_the_marker_not_the_name() {
        let dir = scratch("remove");
        sync(&dir, &[agent("ag-1", "old-name", "Reviews")]).expect("first");
        assert!(dir.join("old-name.md").is_file());

        // Renamed: the new file appears, the old one goes.
        let report = sync(&dir, &[agent("ag-1", "new-name", "Reviews")]).expect("rename");
        assert_eq!(report.written, 1);
        assert_eq!(report.removed, 1);
        assert!(dir.join("new-name.md").is_file());
        assert!(!dir.join("old-name.md").exists());

        // Deleted: nothing left behind.
        let gone = sync(&dir, &[]).expect("delete");
        assert_eq!(gone.removed, 1);
        assert!(scan(&dir).is_empty());
    }

    #[test]
    fn an_agent_without_a_description_is_reported_not_written() {
        let dir = scratch("nodesc");
        let mut row = agent("ag-1", "reviewer", "");
        row.title = String::new();
        let report = sync(&dir, &[row]).expect("sync");
        assert_eq!(report.written, 0);
        assert_eq!(report.skipped.len(), 1);
        assert!(scan(&dir).is_empty());
    }

    #[test]
    fn a_model_without_a_provider_is_not_pinned() {
        let dir = scratch("model");
        let mut row = agent("ag-1", "reviewer", "Reviews");
        row.model = "gpt-6.1-sol".to_string();
        sync(&dir, &[row.clone()]).expect("bare");
        let bare = std::fs::read_to_string(dir.join("reviewer.md")).expect("file");
        assert!(!bare.contains("model:"), "a bare id must not become a pin");

        row.model = "auto/gpt-6.1-sol".to_string();
        sync(&dir, &[row]).expect("qualified");
        let pinned = std::fs::read_to_string(dir.join("reviewer.md")).expect("file");
        assert!(pinned.contains("model: auto/gpt-6.1-sol"));
    }

    #[test]
    fn roster_lists_every_agent_and_says_what_to_do_without_one() {
        let dir = scratch("roster");
        std::fs::write(
            dir.join("scout.md"),
            "---\nname: scout\ndescription: Fast recon\n---\n\nRecon.\n",
        )
        .expect("pi agent");
        sync(&dir, &[agent("ag-1", "reviewer", "Reviews a diff")]).expect("sync");

        let text = roster(&dir, "auto", 4);
        assert!(text.contains("`scout` — Fast recon"));
        assert!(text.contains("`reviewer` — Reviews a diff"));
        assert!(text.contains("上面没有合适的就自己做"));
        assert!(text.contains("同时最多并行 4 个子任务"));

        let explicit = roster(&dir, "explicit", 4);
        assert!(explicit.contains("用户没有点名某个子 agent 时不要委派"));
        assert!(roster(&dir, "never", 4).is_empty());
        assert!(roster(&scratch("roster-empty"), "auto", 4).is_empty());
    }

    #[test]
    fn front_matter_without_name_or_description_is_not_an_agent() {
        let dir = scratch("broken");
        std::fs::write(dir.join("a.md"), "---\nname: only\n---\n\nbody\n").expect("a");
        std::fs::write(dir.join("b.md"), "no front matter\n").expect("b");
        assert!(scan(&dir).is_empty());
    }
}
