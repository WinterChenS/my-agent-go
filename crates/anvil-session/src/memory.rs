//! Memory: what the app is allowed to remember, and how it says so.
//!
//! Two layers, both visible in 设置 → 记忆:
//!
//! * **用户长期记忆** (`scope = "user"`) — facts and preferences the user chose to
//!   keep. Every turn's appended prompt carries them, so a preference stated once
//!   does not have to be repeated. Written by the user in the page, or by the
//!   agent through `anvil-memory.mjs` when the user says "记住…".
//! * **会话记忆** (`scope = "session"`) — one conclusion row per conversation,
//!   written by the host from the assistant's own answer. It is a record to
//!   review, not a hidden input: only 长期记忆 is injected, and promoting a
//!   session memory is an explicit copy the user approves.
//!
//! The design rule both layers follow: nothing is injected that the user cannot
//! see in the list. A "forget" that only hides a card while the text still
//! reaches the model is worse than no memory feature at all, so deletion here is
//! a real `DELETE` and injection reads the same rows the page renders.

use serde::{Deserialize, Serialize};

/// Kinds are the page's filters and the bracket in the prompt. A closed set
/// keeps the prompt readable and stops a typo from becoming a new category.
pub const KINDS: [&str; 4] = ["preference", "fact", "constraint", "workflow"];
pub const SCOPES: [&str; 2] = ["user", "session"];

/// How many characters of memory may join one turn.
///
/// A memory list is context, not the task: at some point more of it makes the
/// answer worse. The cut is reported (`还有 N 条未注入`) instead of silently
/// dropping the tail.
pub const PROMPT_BUDGET: usize = 2_400;

/// One memory row as the UI and the store exchange it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Memory {
    pub id: String,
    /// `user` | `session`.
    pub scope: String,
    /// The conversation a `session` row came from; empty for `user`.
    pub session_id: String,
    /// Title of that conversation, for provenance; not stored.
    pub session_title: String,
    /// Agent that was speaking when the row was written; empty when unknown.
    pub agent_id: String,
    pub kind: String,
    pub text: String,
    /// `manual` | `agent` | `session`.
    pub source: String,
    pub pinned: bool,
    pub enabled: bool,
    pub created_at: String,
    pub updated_at: String,
}

/// What the page (or the agent's CLI) sends to create a memory.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct MemoryDraft {
    pub scope: String,
    pub session_id: String,
    pub agent_id: String,
    pub kind: String,
    pub text: String,
    pub source: String,
    pub pinned: bool,
}

/// What an edit may change. `None` keeps the stored value, which is what an
/// inline "only the text changed" save needs.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct MemoryPatch {
    pub text: Option<String>,
    pub kind: Option<String>,
    pub pinned: Option<bool>,
    pub enabled: Option<bool>,
}

/// The two switches above the lists.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MemorySettings {
    /// Put 长期记忆 into every turn's prompt.
    pub inject: bool,
    /// Record one conclusion row per conversation as it finishes.
    pub record_sessions: bool,
}

impl Default for MemorySettings {
    fn default() -> Self {
        Self {
            inject: true,
            record_sessions: true,
        }
    }
}

/// Trim, collapse newlines and cap: a memory is one or two sentences, and a
/// 5 KB "memory" is a transcript that will crowd out the task.
#[must_use]
pub fn normalize_text(text: &str) -> String {
    let flat = text.replace(['\r', '\n'], " ");
    let squeezed = flat.split_whitespace().collect::<Vec<_>>().join(" ");
    squeezed.chars().take(500).collect()
}

/// A kind the store accepts; anything else becomes `fact`.
#[must_use]
pub fn normalize_kind(kind: &str) -> String {
    let value = kind.trim().to_ascii_lowercase();
    if KINDS.contains(&value.as_str()) {
        value
    } else {
        "fact".to_string()
    }
}

/// A scope the store accepts; anything else becomes `user`.
#[must_use]
pub fn normalize_scope(scope: &str) -> String {
    let value = scope.trim().to_ascii_lowercase();
    if SCOPES.contains(&value.as_str()) {
        value
    } else {
        "user".to_string()
    }
}

/// The block appended to a session's system prompt.
///
/// Pinned rows lead, then newest first (the store already orders them). The
/// closing line matters as much as the list: memories are background, and a
/// request that contradicts one has to win without the model arguing with a
/// stale note.
#[must_use]
pub fn prompt_block(user: &[Memory], budget: usize) -> String {
    // Only `user` rows are injected, even if a caller passes the whole table:
    // a session conclusion is a record to review, not a hidden input.
    let live: Vec<&Memory> = user
        .iter()
        .filter(|row| row.enabled && row.scope == "user")
        .collect();
    if live.is_empty() {
        return String::new();
    }
    let mut body = String::new();
    let mut used = 0usize;
    let mut included = 0usize;
    for row in &live {
        let kind = label(&row.kind);
        let line = format!("- [{kind}] {}\n", row.text.trim());
        let cost = line.chars().count();
        if used + cost > budget {
            break;
        }
        used += cost;
        included += 1;
        body.push_str(&line);
    }
    if included == 0 {
        return String::new();
    }
    let mut out = String::from(
        "## 记忆（用户明确让我长期记住的）\n\n下面这些是背景，不是这次的任务。和用户这一轮说的冲突时，以这一轮为准；不要主动复述、也不要因为它们改变任务范围。\n\n",
    );
    out.push_str(&body);
    let rest = live.len() - included;
    if rest > 0 {
        out.push_str(&format!("（还有 {rest} 条记忆因长度未注入，需要时用 anvil-memory.mjs list 查看。）\n"));
    }
    out
}

/// The bracket label inside a prompt line.
fn label(kind: &str) -> &'static str {
    match kind {
        "preference" => "偏好",
        "constraint" => "约束",
        "workflow" => "习惯",
        _ => "事实",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn row(id: &str, kind: &str, text: &str) -> Memory {
        Memory {
            id: id.to_string(),
            scope: "user".to_string(),
            session_id: String::new(),
            session_title: String::new(),
            agent_id: String::new(),
            kind: kind.to_string(),
            text: text.to_string(),
            source: "manual".to_string(),
            pinned: false,
            enabled: true,
            created_at: "2026-11-22T00:00:00Z".to_string(),
            updated_at: "2026-11-22T00:00:00Z".to_string(),
        }
    }

    #[test]
    fn an_empty_or_disabled_list_adds_nothing_to_the_prompt() {
        assert!(prompt_block(&[], PROMPT_BUDGET).is_empty());
        let mut off = row("mem-1", "preference", "喜欢简洁");
        off.enabled = false;
        assert!(prompt_block(&[off], PROMPT_BUDGET).is_empty());
    }

    #[test]
    fn the_block_says_it_is_background_not_the_task() {
        let text = prompt_block(&[row("mem-1", "preference", "回答先给结论")], PROMPT_BUDGET);
        assert!(text.contains("- [偏好] 回答先给结论"));
        assert!(text.contains("以这一轮为准"));
        assert!(!text.contains("未注入"));
    }

    #[test]
    fn a_session_conclusion_is_never_injected_by_accident() {
        let mut session = row("session-memory:s1", "fact", "上次会话的结论");
        session.scope = "session".to_string();
        assert!(prompt_block(&[session], PROMPT_BUDGET).is_empty());
    }

    #[test]
    fn a_full_budget_is_reported_rather_than_silently_cut() {
        let rows: Vec<Memory> = (0..6)
            .map(|index| row(&format!("mem-{index}"), "fact", &"很长的记忆".repeat(6)))
            .collect();
        let text = prompt_block(&rows, 40);
        assert!(text.contains("条记忆因长度未注入"), "{text}");
        // The cut is by whole lines: no half sentence ends up in the prompt.
        assert!(!text.trim_end().ends_with("很长"));
    }

    #[test]
    fn text_is_flattened_and_capped() {
        let flat = normalize_text("  第一行\n\n第二行\t\t尾巴  ");
        assert_eq!(flat, "第一行 第二行 尾巴");
        let long = normalize_text(&"字".repeat(900));
        assert_eq!(long.chars().count(), 500);
    }

    #[test]
    fn unknown_kinds_and_scopes_fall_back_instead_of_erroring() {
        assert_eq!(normalize_kind("Preference"), "preference");
        assert_eq!(normalize_kind("opinion"), "fact");
        assert_eq!(normalize_scope("SESSION"), "session");
        assert_eq!(normalize_scope("global"), "user");
    }
}
