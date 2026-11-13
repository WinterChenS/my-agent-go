//! Exporting a conversation: three formats, one confined directory, no
//! half-written files.
//!
//! Rules that the acceptance case asserts by name:
//!
//! * **The text is the same in every format.** HTML is escaped, Markdown keeps
//!   the words raw, JSON carries the same strings; what changes is the envelope,
//!   never the content.
//! * **The path is chosen by the application, not the caller.** The file name is
//!   sanitised and written under one root; a name with a separator is refused
//!   rather than joined.
//! * **Overwriting needs an explicit yes.** An existing file is an error until
//!   the caller says so.
//! * **A failed export leaves nothing behind.** The bytes go to a `.part` file
//!   that is renamed into place, so a crash mid-write cannot leave a truncated
//!   document that looks complete.
//! * **Tool arguments are private by default.** They routinely hold paths,
//!   tokens and file bodies; the caller has to ask for them.

use std::fmt::Write as _;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::error::{Error, Result};
use crate::model::{EventKind, SessionSummary};
use crate::model::{Message, Run, RunEvent};
use crate::rail::label_for;

/// The formats the dialog offers.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ExportFormat {
    Html,
    Markdown,
    Json,
}

impl ExportFormat {
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Html => "html",
            Self::Markdown => "markdown",
            Self::Json => "json",
        }
    }

    #[must_use]
    pub fn extension(self) -> &'static str {
        match self {
            Self::Html => "html",
            Self::Markdown => "md",
            Self::Json => "json",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "html" => Some(Self::Html),
            "markdown" | "md" => Some(Self::Markdown),
            "json" => Some(Self::Json),
            _ => None,
        }
    }
}

/// What the caller decided about content and overwriting.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ExportOptions {
    pub format: ExportFormat,
    /// Tool arguments are private by default; this is the explicit opt-in.
    #[serde(default)]
    pub include_tools: bool,
    /// Overwriting an existing export is an explicit act.
    #[serde(default)]
    pub overwrite: bool,
}

/// What one export did.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ExportReport {
    /// Absolute path of the written file.
    pub path: String,
    /// Directory the path was confined to.
    pub root: String,
    pub file_name: String,
    pub bytes: u64,
    /// SHA-256 of the file's bytes, so two exports can be compared.
    pub sha256: String,
    pub format: String,
    pub messages: usize,
    pub runs: usize,
    /// Tool rows written; `0` when arguments were excluded.
    pub tool_rows: usize,
    pub tools_included: bool,
}

/// Counts of what the document contains.
#[derive(Debug, Clone, Default)]
pub struct RenderStats {
    pub messages: usize,
    pub runs: usize,
    pub tool_rows: usize,
}

/// One tool call as the export shows it.
struct ToolRow {
    tool: String,
    label: String,
    ok: Option<bool>,
}

/// Render one conversation. Returns the file name to use and the bytes.
#[must_use]
pub fn render(
    session: &SessionSummary,
    messages: &[Message],
    runs: &[Run],
    events: &[RunEvent],
    options: &ExportOptions,
) -> (String, String, RenderStats) {
    let tools = if options.include_tools {
        tool_rows(events)
    } else {
        Vec::new()
    };
    let stats = RenderStats {
        messages: messages.len(),
        runs: runs.len(),
        tool_rows: tools.len(),
    };
    let file_name = format!(
        "{}.{}",
        sanitize(&format!("{}-{}", session.title, &session.id)),
        options.format.extension()
    );
    let content = match options.format {
        ExportFormat::Html => html(session, messages, runs, &tools),
        ExportFormat::Markdown => markdown(session, messages, runs, &tools),
        ExportFormat::Json => json(session, messages, runs, &tools),
    };
    (file_name, content, stats)
}

/// Pair every tool call with its result, in ledger order.
fn tool_rows(events: &[RunEvent]) -> Vec<ToolRow> {
    let mut rows: Vec<ToolRow> = Vec::new();
    let mut waiting: std::collections::BTreeMap<&str, Vec<usize>> =
        std::collections::BTreeMap::new();
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
                rows.push(ToolRow {
                    label: label_for(&tool, &args),
                    tool,
                    ok: None,
                });
                waiting
                    .entry(event.run_id.as_str())
                    .or_default()
                    .push(rows.len() - 1);
            }
            EventKind::ToolResult => {
                if let Some(index) = waiting
                    .get_mut(event.run_id.as_str())
                    .and_then(|queue| queue.pop())
                {
                    rows[index].ok = event.payload.get("ok").and_then(|ok| ok.as_bool());
                }
            }
            _ => {}
        }
    }
    rows
}

fn html(session: &SessionSummary, messages: &[Message], runs: &[Run], tools: &[ToolRow]) -> String {
    let mut out = String::new();
    out.push_str("<!doctype html>\n<html lang=\"zh\">\n<head>\n<meta charset=\"utf-8\">\n");
    let _ = writeln!(out, "<title>{}</title>", escape_html(&session.title));
    out.push_str(
        "<style>body{font:14px/1.6 system-ui,sans-serif;max-width:820px;margin:2rem auto;padding:0 1rem;color:#111}\
h1{font-size:1.3rem}h2{font-size:.85rem;text-transform:uppercase;color:#666;margin:1.6rem 0 .4rem}\
pre{white-space:pre-wrap;word-break:break-word;background:#f6f6f6;padding:.7rem;border-radius:6px}\
.meta{color:#666;font-size:.85rem}</style>\n</head>\n<body>\n",
    );
    let _ = writeln!(out, "<h1>{}</h1>", escape_html(&session.title));
    let _ = writeln!(
        out,
        "<p class=\"meta\">{}</p>",
        escape_html(&format!(
            "{} · {} · {} turns · {} messages",
            session.runtime_id,
            session.model,
            runs.len(),
            messages.len()
        ))
    );
    for message in messages {
        let _ = writeln!(
            out,
            "<h2>{}</h2>\n<pre>{}</pre>",
            escape_html(message.role.as_str()),
            escape_html(&message.text)
        );
    }
    if !tools.is_empty() {
        out.push_str("<h2>tools</h2>\n<ul>\n");
        for row in tools {
            let state = match row.ok {
                Some(true) => "ok",
                Some(false) => "failed",
                None => "no result",
            };
            let _ = writeln!(
                out,
                "<li>{} · {} · {}</li>",
                escape_html(&row.tool),
                escape_html(state),
                escape_html(&row.label)
            );
        }
        out.push_str("</ul>\n");
    }
    out.push_str("</body>\n</html>\n");
    out
}

fn markdown(
    session: &SessionSummary,
    messages: &[Message],
    runs: &[Run],
    tools: &[ToolRow],
) -> String {
    let mut out = String::new();
    let _ = writeln!(out, "# {}\n", session.title);
    let _ = writeln!(
        out,
        "> {} · {} · {} turns · {} messages\n",
        session.runtime_id,
        session.model,
        runs.len(),
        messages.len()
    );
    for message in messages {
        let _ = writeln!(out, "## {}\n", message.role.as_str());
        // The body is quoted as a block so a message that itself starts with
        // `#` cannot restructure the document around it.
        for line in message.text.lines() {
            let _ = writeln!(out, "> {line}");
        }
        out.push('\n');
    }
    if !tools.is_empty() {
        out.push_str("## tools\n\n");
        for row in tools {
            let state = match row.ok {
                Some(true) => "ok",
                Some(false) => "failed",
                None => "no result",
            };
            let _ = writeln!(out, "- `{}` · {} · `{}`", row.tool, state, row.label);
        }
        out.push('\n');
    }
    out
}

fn json(session: &SessionSummary, messages: &[Message], runs: &[Run], tools: &[ToolRow]) -> String {
    let payload = serde_json::json!({
        "session": {
            "id": session.id,
            "title": session.title,
            "runtimeId": session.runtime_id,
            "model": session.model,
            "projectPath": session.project_path,
            "createdAt": session.created_at,
            "updatedAt": session.updated_at,
        },
        "messages": messages.iter().map(|message| serde_json::json!({
            "seq": message.seq,
            "role": message.role.as_str(),
            "text": message.text,
            "createdAt": message.created_at,
            "runId": message.run_id,
        })).collect::<Vec<_>>(),
        "runs": runs.iter().map(|run| serde_json::json!({
            "id": run.id,
            "status": run.status.as_str(),
            "startedAt": run.started_at,
            "endedAt": run.ended_at,
        })).collect::<Vec<_>>(),
        "tools": tools.iter().map(|row| serde_json::json!({
            "tool": row.tool,
            "label": row.label,
            "ok": row.ok,
        })).collect::<Vec<_>>(),
    });
    format!(
        "{}\n",
        serde_json::to_string_pretty(&payload).unwrap_or_else(|_| "{}".into())
    )
}

/// Write inside `root`, atomically, and only over an existing file when the
/// caller said so.
pub fn write(root: &Path, file_name: &str, content: &str, overwrite: bool) -> Result<PathBuf> {
    if file_name.trim().is_empty()
        || file_name.contains('/')
        || file_name.contains('\\')
        || file_name.contains("..")
        || file_name.starts_with('.')
    {
        return Err(Error::Rejected(format!(
            "export name {file_name:?} is not a plain file name"
        )));
    }
    std::fs::create_dir_all(root)?;
    let root = std::fs::canonicalize(root)?;
    let target = root.join(file_name);
    if target.exists() && !overwrite {
        return Err(Error::Rejected(format!(
            "{file_name} already exists; confirm the overwrite"
        )));
    }

    // The bytes land beside the target first, so an interrupted write leaves a
    // `.part` file rather than a document that looks finished.
    let temp = root.join(format!(".{file_name}.part"));
    let _ = std::fs::remove_file(&temp);
    if let Err(error) = std::fs::write(&temp, content) {
        let _ = std::fs::remove_file(&temp);
        return Err(error.into());
    }
    // Every step after the temporary write can fail (a target that is a
    // directory, a locked file, a full disk), and every one of them must take
    // the temporary file with it: a `.part` left behind is exactly the
    // half-written export this function exists to prevent.
    let renamed = (|| -> std::io::Result<()> {
        if target.exists() {
            // Windows refuses to rename onto an existing file; the overwrite was
            // already confirmed above, so removing first is the explicit act.
            std::fs::remove_file(&target)?;
        }
        std::fs::rename(&temp, &target)
    })();
    if let Err(error) = renamed {
        let _ = std::fs::remove_file(&temp);
        return Err(error.into());
    }
    Ok(target)
}

/// SHA-256 of the bytes, lowercase hex.
#[must_use]
pub fn digest(content: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(content.as_bytes());
    let bytes = hasher.finalize();
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

/// HTML escaping. Every character that can end a text node or an attribute is
/// replaced; the export never contains an executable `<script>`.
#[must_use]
pub fn escape_html(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for ch in text.chars() {
        match ch {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&#39;"),
            _ => out.push(ch),
        }
    }
    out
}

/// A file name that survives every filesystem the app ships on.
fn sanitize(raw: &str) -> String {
    let mut out = String::with_capacity(raw.len());
    for ch in raw.chars() {
        if ch.is_alphanumeric() || ch == '-' || ch == '_' || ch == '.' {
            out.push(ch);
        } else if ch.is_whitespace() {
            out.push('-');
        }
    }
    let trimmed = out.trim_matches(['-', '.']).to_string();
    if trimmed.is_empty() {
        "session".into()
    } else if trimmed.chars().count() > 80 {
        trimmed.chars().take(80).collect()
    } else {
        trimmed
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{Role, RunStatus};

    fn root(name: &str) -> PathBuf {
        let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("export")
            .join(name);
        let _ = std::fs::remove_dir_all(&dir);
        dir
    }

    fn session() -> SessionSummary {
        SessionSummary {
            id: "pi-1".into(),
            runtime_id: "pi".into(),
            title: "Weekly <script>alert(1)</script> report".into(),
            status: crate::model::SessionStatus::Idle,
            native_id: "n1".into(),
            model: "auto/gpt-5.6-sol".into(),
            project_path: "D:\\work".into(),
            parent_id: String::new(),
            branch_at: 0,
            allow_tools: true,
            tools: vec!["write".into()],
            pinned: false,
            archived: false,
            message_count: 2,
            created_at: "2026-11-13T00:00:00Z".into(),
            updated_at: "2026-11-13T00:00:00Z".into(),
            preview: String::new(),
        }
    }

    fn messages() -> Vec<Message> {
        vec![
            Message {
                id: "m1".into(),
                session_id: "pi-1".into(),
                run_id: Some("pi-1:r1".into()),
                role: Role::User,
                text: "check <b>this</b> & that".into(),
                seq: 1,
                created_at: "2026-11-13T00:00:00Z".into(),
                meta: None,
            },
            Message {
                id: "m2".into(),
                session_id: "pi-1".into(),
                run_id: Some("pi-1:r1".into()),
                role: Role::Assistant,
                text: "done: <script>alert(1)</script>".into(),
                seq: 2,
                created_at: "2026-11-13T00:00:01Z".into(),
                meta: None,
            },
        ]
    }

    fn runs() -> Vec<Run> {
        vec![Run {
            id: "pi-1:r1".into(),
            session_id: "pi-1".into(),
            status: RunStatus::Completed,
            started_at: "2026-11-13T00:00:00Z".into(),
            ended_at: Some("2026-11-13T00:00:01Z".into()),
            error: None,
            prompt: "check".into(),
        }]
    }

    fn events() -> Vec<RunEvent> {
        vec![
            RunEvent::new(
                "pi-1",
                "pi-1:r1",
                1,
                EventKind::ToolCall,
                serde_json::json!({
                    "tool": "write",
                    "args": { "path": "out/report.md", "secret": "sk-live-DEADBEEF" }
                }),
            ),
            RunEvent::new(
                "pi-1",
                "pi-1:r1",
                2,
                EventKind::ToolResult,
                serde_json::json!({ "tool": "write", "ok": true }),
            ),
        ]
    }

    fn options(format: ExportFormat) -> ExportOptions {
        ExportOptions {
            format,
            include_tools: false,
            overwrite: false,
        }
    }

    #[test]
    fn html_escapes_every_dangerous_character() {
        let (_, content, _) = render(
            &session(),
            &messages(),
            &runs(),
            &events(),
            &options(ExportFormat::Html),
        );
        assert!(content.contains("&lt;script&gt;alert(1)&lt;/script&gt;"));
        assert!(!content.contains("<script>"), "{content}");
        assert!(content.contains("&amp; that"));
    }

    #[test]
    fn markdown_and_json_keep_the_words_untouched() {
        let (_, md, _) = render(
            &session(),
            &messages(),
            &runs(),
            &events(),
            &options(ExportFormat::Markdown),
        );
        assert!(md.contains("done: <script>alert(1)</script>"));
        let (_, json, stats) = render(
            &session(),
            &messages(),
            &runs(),
            &events(),
            &options(ExportFormat::Json),
        );
        let parsed: serde_json::Value = serde_json::from_str(&json).expect("json");
        assert_eq!(
            parsed["messages"][1]["text"],
            "done: <script>alert(1)</script>"
        );
        assert_eq!(stats.messages, 2);
        assert_eq!(stats.runs, 1);
    }

    #[test]
    fn tool_arguments_stay_private_unless_asked_for() {
        let (_, hidden, stats) = render(
            &session(),
            &messages(),
            &runs(),
            &events(),
            &options(ExportFormat::Json),
        );
        assert_eq!(stats.tool_rows, 0);
        assert!(!hidden.contains("DEADBEEF"), "{hidden}");
        let asked = ExportOptions {
            include_tools: true,
            ..options(ExportFormat::Json)
        };
        let (_, shown, stats) = render(&session(), &messages(), &runs(), &events(), &asked);
        assert_eq!(stats.tool_rows, 1);
        // The label is the path; the free-form args (which may hold a token) are
        // still not copied wholesale.
        assert!(shown.contains("out/report.md"));
        assert!(!shown.contains("DEADBEEF"));
    }

    #[test]
    fn the_file_name_is_plain_and_the_extension_follows_the_format() {
        let (name, _, _) = render(
            &session(),
            &messages(),
            &runs(),
            &events(),
            &options(ExportFormat::Markdown),
        );
        assert!(name.ends_with(".md"), "{name}");
        assert!(!name.contains('<'));
        assert!(!name.contains(' '));
    }

    #[test]
    fn an_existing_file_needs_an_explicit_overwrite() {
        let dir = root("overwrite");
        std::fs::create_dir_all(&dir).expect("dir");
        std::fs::write(dir.join("a.md"), "old").expect("seed");
        let error = write(&dir, "a.md", "new", false).expect_err("refused");
        assert_eq!(error.code(), "rejected");
        assert_eq!(
            std::fs::read_to_string(dir.join("a.md")).expect("read"),
            "old"
        );
        let path = write(&dir, "a.md", "new", true).expect("overwrite");
        assert_eq!(std::fs::read_to_string(path).expect("read"), "new");
    }

    #[test]
    fn a_name_with_a_separator_is_refused_instead_of_joined() {
        let dir = root("escape");
        for name in ["../outside.md", "sub/a.md", "a\\b.md", ".hidden"] {
            let error = write(&dir, name, "x", true).expect_err("refused");
            assert_eq!(error.code(), "rejected", "{name}");
        }
        assert!(!dir.parent().expect("parent").join("outside.md").exists());
    }

    #[test]
    fn a_failed_write_leaves_no_partial_file_behind() {
        let dir = root("partial");
        std::fs::create_dir_all(dir.parent().expect("parent")).expect("parent");
        // Making the root a file is enough to fail every write inside it.
        std::fs::write(&dir, "not a directory").expect("seed");
        assert!(write(&dir, "a.md", "x", true).is_err());
        assert!(!dir.join(".a.md.part").exists());
        std::fs::remove_file(&dir).expect("cleanup");
    }

    #[test]
    fn the_same_content_hashes_the_same_and_different_content_does_not() {
        assert_eq!(digest("abc"), digest("abc"));
        assert_ne!(digest("abc"), digest("abd"));
        assert_eq!(digest("abc").len(), 64);
    }
}
