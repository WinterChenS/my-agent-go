//! Wire types shared by the ledger, the host commands and the UI.
//!
//! Everything is `camelCase` on the wire: the React client consumes these
//! shapes directly, and the acceptance report asserts on the same field names.

use serde::{Deserialize, Serialize};

/// Lifecycle of a session.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SessionStatus {
    /// No run is in flight and the last run finished cleanly.
    #[default]
    Idle,
    /// A run is in flight.
    Running,
    /// The user stopped the last run.
    Aborted,
    /// The last run failed; `runs.error` says why.
    Failed,
    /// Hidden from the default list, kept for history.
    Archived,
}

impl SessionStatus {
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Idle => "idle",
            Self::Running => "running",
            Self::Aborted => "aborted",
            Self::Failed => "failed",
            Self::Archived => "archived",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "idle" => Some(Self::Idle),
            "running" => Some(Self::Running),
            "aborted" => Some(Self::Aborted),
            "failed" => Some(Self::Failed),
            "archived" => Some(Self::Archived),
            _ => None,
        }
    }
}

/// Who produced a message.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Role {
    System,
    User,
    Assistant,
    Tool,
}

impl Role {
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::System => "system",
            Self::User => "user",
            Self::Assistant => "assistant",
            Self::Tool => "tool",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "system" => Some(Self::System),
            "user" => Some(Self::User),
            "assistant" => Some(Self::Assistant),
            "tool" => Some(Self::Tool),
            _ => None,
        }
    }
}

/// Terminal state of one run.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RunStatus {
    Running,
    Completed,
    Aborted,
    Failed,
}

impl RunStatus {
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Running => "running",
            Self::Completed => "completed",
            Self::Aborted => "aborted",
            Self::Failed => "failed",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "running" => Some(Self::Running),
            "completed" => Some(Self::Completed),
            "aborted" => Some(Self::Aborted),
            "failed" => Some(Self::Failed),
            _ => None,
        }
    }

    /// Whether the run reached a terminal state.
    #[must_use]
    pub fn is_terminal(self) -> bool {
        self != Self::Running
    }
}

/// What a runtime can do beyond plain prompt/answer.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionCapabilities {
    pub streaming: bool,
    pub resume: bool,
    pub abort: bool,
    pub tools: bool,
    pub approvals: bool,
    pub compact: bool,
    pub fork: bool,
    pub usage: bool,
}

impl SessionCapabilities {
    /// Everything a fully featured runtime offers, used as the default for the
    /// shipped adapters and as the ceiling in tests.
    #[must_use]
    pub fn full() -> Self {
        Self {
            streaming: true,
            resume: true,
            abort: true,
            tools: true,
            approvals: true,
            compact: true,
            fork: true,
            usage: true,
        }
    }

    /// The minimum this build needs for a live conversation case.
    #[must_use]
    pub fn minimum() -> Self {
        Self {
            streaming: true,
            resume: false,
            abort: false,
            tools: false,
            approvals: false,
            compact: false,
            fork: false,
            usage: false,
        }
    }
}

/// What a run produced at one point in time.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum EventKind {
    SessionStarted,
    TurnStarted,
    TextDelta,
    /// Incremental reasoning text from the runtime. Kept in the ledger so a
    /// turn that thought before answering can actually show it.
    Thinking,
    MessageCompleted,
    ToolCall,
    ToolResult,
    ApprovalRequest,
    ApprovalResolved,
    Usage,
    Error,
    TurnCompleted,
    Aborted,
    /// The session's transcript was compacted. The runtime's own context is
    /// untouched: the payload says what this build did and what it did not.
    Compacted,
    /// Context references were resolved and folded into the turn's prompt.
    ContextAttached,
    /// A delegated child agent reported something. The runtime owns the
    /// delegation decision; this build only records what it reported, so a
    /// sub-agent that runs, streams, calls tools and finishes is visible
    /// instead of silently collapsing into one opaque tool card.
    SubAgent,
    /// The user injected a message into a run that was already streaming.
    ///
    /// This is not a new turn: the runtime keeps working and picks the message
    /// up at its next step. It is recorded as an event of the *current* run so
    /// the transcript can place it where it happened instead of appending it
    /// after the answer.
    UserSteer,
    /// The runtime asked the user something only the user can answer.
    ///
    /// The runtime blocks until the answer is sent back, so the request has to
    /// reach the page; dropping it leaves the turn waiting forever.
    ExtensionUi,
    /// The user answered (or dismissed) a runtime question.
    ExtensionUiResolved,
    /// A runtime event this build does not model yet; kept, never dropped.
    Unknown,
}

impl EventKind {
    #[must_use]
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::SessionStarted => "session_started",
            Self::TurnStarted => "turn_started",
            Self::TextDelta => "text_delta",
            Self::Thinking => "thinking",
            Self::MessageCompleted => "message_completed",
            Self::ToolCall => "tool_call",
            Self::ToolResult => "tool_result",
            Self::ApprovalRequest => "approval_request",
            Self::ApprovalResolved => "approval_resolved",
            Self::Usage => "usage",
            Self::Error => "error",
            Self::TurnCompleted => "turn_completed",
            Self::Aborted => "aborted",
            Self::Compacted => "compacted",
            Self::ContextAttached => "context_attached",
            Self::SubAgent => "sub_agent",
            Self::UserSteer => "user_steer",
            Self::ExtensionUi => "extension_ui",
            Self::ExtensionUiResolved => "extension_ui_resolved",
            Self::Unknown => "unknown",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "session_started" => Some(Self::SessionStarted),
            "turn_started" => Some(Self::TurnStarted),
            "text_delta" => Some(Self::TextDelta),
            "thinking" => Some(Self::Thinking),
            "message_completed" => Some(Self::MessageCompleted),
            "tool_call" => Some(Self::ToolCall),
            "tool_result" => Some(Self::ToolResult),
            "approval_request" => Some(Self::ApprovalRequest),
            "approval_resolved" => Some(Self::ApprovalResolved),
            "usage" => Some(Self::Usage),
            "error" => Some(Self::Error),
            "turn_completed" => Some(Self::TurnCompleted),
            "aborted" => Some(Self::Aborted),
            "compacted" => Some(Self::Compacted),
            "context_attached" => Some(Self::ContextAttached),
            "sub_agent" => Some(Self::SubAgent),
            "user_steer" => Some(Self::UserSteer),
            "extension_ui" => Some(Self::ExtensionUi),
            "extension_ui_resolved" => Some(Self::ExtensionUiResolved),
            "unknown" => Some(Self::Unknown),
            _ => None,
        }
    }
}

/// One ledger row. `seq` is monotonic per session and never reused, so the UI
/// can merge streams from different processes without guessing.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RunEvent {
    pub session_id: String,
    pub run_id: String,
    pub seq: i64,
    pub kind: EventKind,
    pub at: String,
    pub payload: serde_json::Value,
}

impl RunEvent {
    #[must_use]
    pub fn new(
        session_id: &str,
        run_id: &str,
        seq: i64,
        kind: EventKind,
        payload: serde_json::Value,
    ) -> Self {
        Self {
            session_id: session_id.to_string(),
            run_id: run_id.to_string(),
            seq,
            kind,
            at: crate::clock::now_rfc3339(),
            payload,
        }
    }
}

/// A message row derived from the ledger.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Message {
    pub id: String,
    pub session_id: String,
    pub run_id: Option<String>,
    pub role: Role,
    pub text: String,
    /// Order inside the session, monotonic, shared with the event ledger.
    pub seq: i64,
    pub created_at: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub meta: Option<serde_json::Value>,
}

/// One run, for the "stop"/"retry" affordances and for the timeline.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Run {
    pub id: String,
    pub session_id: String,
    pub status: RunStatus,
    pub started_at: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ended_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    pub prompt: String,
}

/// A session as the list view needs it.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionSummary {
    pub id: String,
    pub runtime_id: String,
    pub title: String,
    pub status: SessionStatus,
    /// Identifier the runtime itself uses (`sessionId` for pi, `thread.id` for
    /// codex). Empty until the runtime confirmed the session.
    pub native_id: String,
    pub model: String,
    pub project_path: String,
    /// Session this one was branched from; empty for a session started from
    /// scratch.
    pub parent_id: String,
    /// Message `seq` in the parent the branch was taken at; `0` without a
    /// parent.
    pub branch_at: i64,
    /// Tool policy this session was opened with.
    pub allow_tools: bool,
    pub tools: Vec<String>,
    /// Agent whose persona this session runs with; empty when none was chosen.
    pub agent_id: String,
    /// Thinking level the session was opened with; empty for the runtime default.
    pub thinking: String,
    pub pinned: bool,
    pub archived: bool,
    pub message_count: i64,
    pub created_at: String,
    pub updated_at: String,
    /// Last assistant text, truncated, for the history list.
    pub preview: String,
}

/// A session with its messages and runs.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionDetail {
    pub session: SessionSummary,
    pub messages: Vec<Message>,
    pub runs: Vec<Run>,
}

/// Everything one read returns: list + paging facts.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionPage {
    pub sessions: Vec<SessionSummary>,
    /// Cursor for the next page; `None` when the list is exhausted.
    pub next_cursor: Option<String>,
    pub total: i64,
}

/// What the history panel asks the ledger for.
///
/// A struct rather than six positionals: `runtime_id`, `project_path` and
/// `search` are all optional strings, and a positional call that swaps two of
/// them would filter by the wrong thing while still compiling.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct SessionQuery {
    pub runtime_id: Option<String>,
    /// Only sessions that ran in this directory.
    pub project_path: Option<String>,
    pub include_archived: bool,
    /// Matches the title, the preview or any message text.
    pub search: Option<String>,
    pub limit: Option<usize>,
    pub cursor: Option<String>,
}

impl SessionQuery {
    /// Page size with the ledger's own bounds applied.
    #[must_use]
    pub fn page_size(&self) -> usize {
        self.limit.unwrap_or(50).clamp(1, 200)
    }

    /// Trim and drop empty filters, so `"  "` does not become a clause.
    #[must_use]
    pub fn normalized(&self) -> Self {
        let clean = |value: &Option<String>| {
            value
                .as_deref()
                .map(str::trim)
                .filter(|text| !text.is_empty())
                .map(str::to_string)
        };
        Self {
            runtime_id: clean(&self.runtime_id),
            project_path: clean(&self.project_path),
            include_archived: self.include_archived,
            search: clean(&self.search),
            limit: self.limit,
            cursor: self.cursor.clone(),
        }
    }
}

/// One project as the history panel groups sessions by.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectSummary {
    /// Absolute directory the sessions ran in; the grouping key.
    pub path: String,
    /// Last path segment, for display; falls back to the full path.
    pub name: String,
    /// Sessions in this project that are not archived.
    pub session_count: i64,
    /// Most recent `updated_at` among those sessions.
    pub last_updated: String,
}

/// One row of an agent's run history, read from this ledger rather than made
/// up. `tokens` is zero when the runtime never reported usage, not an estimate.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentRun {
    pub session_id: String,
    pub title: String,
    pub at: String,
    pub turns: i64,
    pub tool_calls: i64,
    pub tokens: i64,
    /// `idle` | `running` | `aborted` | `failed` | `archived`.
    pub status: String,
}

/// What one compaction did.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CompactReport {
    /// Messages folded into the summary.
    pub collapsed: usize,
    /// Messages kept verbatim after the summary.
    pub kept: usize,
    /// `seq` of the summary message, so the UI can scroll to it.
    pub summary_seq: i64,
}

/// What one turn consists of: the user's words plus the context they pointed at.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct SessionInput {
    pub prompt: String,
    /// Files, directories and skills the user attached with `@`.
    pub context: Vec<ContextRef>,
    /// How far the user trusts this workspace for this turn.
    ///
    /// `full` means "I trust this directory": a reference outside the project
    /// is read without raising an approval card. Any other value (including
    /// `None`) keeps the per-path question, because consent to read outside the
    /// project is the one thing that must never be assumed.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub permission: Option<String>,
    /// How hard the runtime should think on this turn.
    ///
    /// Travels with the send, like `permission`: the composer's picker is a
    /// decision about this question, not a permanent property of the
    /// conversation or of the agent's persona. `None` keeps whatever the
    /// session was opened with.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub thinking: Option<String>,
}

/// True when the user picked the "trust this workspace" mode for the turn.
#[must_use]
pub fn trusts_workspace(permission: Option<&str>) -> bool {
    matches!(permission, Some(value) if value.eq_ignore_ascii_case("full"))
}

/// Tools that can read but cannot change anything.
///
/// This is the runtime's own documented read-only mode (`pi --tools
/// read,grep,find,ls`), not an invention: the same four names the CLI prints as
/// its example for "no file modifications possible".
pub const READ_ONLY_TOOLS: [&str; 4] = ["read", "grep", "find", "ls"];

/// What one permission mode lets a turn do, in terms the adapters can honour.
///
/// The picker offers four levels, and three of them have to mean something a
/// user could observe — otherwise the control is decoration:
///
/// | mode       | tools                    | project-local `.pi` | outside reads |
/// |------------|--------------------------|---------------------|---------------|
/// | `readonly` | read,grep,find,ls        | skipped             | asks          |
/// | `medium`   | the session's own list   | skipped             | asks          |
/// | `high`     | the session's own list   | loaded              | asks          |
/// | `full`     | the session's own list   | loaded              | no question   |
///
/// `high` is the one level that widens what the runtime may load rather than
/// narrowing it: a working folder's `.pi/extensions`, `.pi/skills`,
/// `.pi/settings.json` and `.pi/mcp.json` are executable configuration, and a
/// non-interactive run refuses them unless it is told to trust the folder.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct PermissionPlan {
    /// Replace the turn's tool list with [`READ_ONLY_TOOLS`].
    pub read_only: bool,
    /// `--approve` / `--no-approve`; `None` leaves the runtime's default.
    pub project_trust: Option<bool>,
    /// Skip the approval card for a path outside the project.
    pub trusts_workspace: bool,
    /// The level the plan was made from: `readonly`, `medium`, `high` or
    /// `full`.
    ///
    /// The runtime needs the level itself, not only what it implies: a
    /// question the runtime asks on its own is answered without the user only
    /// in `full`, where the user already said they trust this workspace.
    pub level: &'static str,
}

impl Default for PermissionPlan {
    fn default() -> Self {
        // The safe answer for an absent or unrecognised value: keep the
        // session's tools, do not load a stranger's `.pi`, keep asking.
        Self {
            read_only: false,
            project_trust: Some(false),
            trusts_workspace: false,
            level: "medium",
        }
    }
}

/// Map the composer's picker onto flags.
#[must_use]
pub fn permission_plan(permission: Option<&str>) -> PermissionPlan {
    let asked = permission.map(str::trim).filter(|value| !value.is_empty());
    match asked.map(str::to_ascii_lowercase).as_deref() {
        Some("readonly") => PermissionPlan {
            read_only: true,
            level: "readonly",
            ..PermissionPlan::default()
        },
        Some("high") => PermissionPlan {
            read_only: false,
            project_trust: Some(true),
            trusts_workspace: false,
            level: "high",
        },
        Some("full") => PermissionPlan {
            read_only: false,
            project_trust: Some(true),
            trusts_workspace: true,
            level: "full",
        },
        // `medium` and anything this build does not know.
        _ => PermissionPlan::default(),
    }
}

/// Levels pi accepts for `--thinking`, weakest first.
///
/// Kept here rather than in the adapter so the service can reject a value the
/// UI invented before it reaches a child process; the adapter passes the string
/// through unchanged.
pub const THINKING_LEVELS: [&str; 7] = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];

/// The level to run with, or `None` when the value is not a known level.
///
/// Unknown values are ignored instead of forwarded: `pi` exits with a usage
/// error on a typo, and a broken picker must not take a whole turn down.
#[must_use]
pub fn thinking_level(value: Option<&str>) -> Option<&'static str> {
    let wanted = value?.trim();
    THINKING_LEVELS
        .iter()
        .copied()
        .find(|level| level.eq_ignore_ascii_case(wanted))
}

/// One `@` reference, as the composer sends it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextRef {
    pub kind: ContextKind,
    /// Project-relative or absolute. Resolved and confined by the service.
    pub path: String,
    /// Display label (a skill's name, usually). Never used for resolution.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub label: Option<String>,
}

/// What kind of context a reference points at.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ContextKind {
    File,
    Dir,
    Skill,
}

impl ContextKind {
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::File => "file",
            Self::Dir => "dir",
            Self::Skill => "skill",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "file" => Some(Self::File),
            "dir" => Some(Self::Dir),
            "skill" => Some(Self::Skill),
            _ => None,
        }
    }
}

/// A reference after resolution: the absolute path plus what was actually inlined.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ResolvedRef {
    pub kind: ContextKind,
    /// Absolute, symlink-resolved path.
    pub path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub label: Option<String>,
    /// Bytes of the source file, before any truncation.
    pub bytes: u64,
    /// Characters inlined into the prompt; `0` when the reference only produced
    /// a listing or an omission note.
    pub chars: usize,
    /// The reference was larger than the budget and was cut.
    pub truncated: bool,
    /// The reference came from outside the session's project and was allowed by
    /// an explicit approval.
    pub outside_root: bool,
}

/// One decision the user can make about an approval request.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ApprovalDecision {
    Allow,
    Deny,
    /// The user closed the card without deciding; the run is stopped, not failed.
    Cancel,
}

impl ApprovalDecision {
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Allow => "allow",
            Self::Deny => "deny",
            Self::Cancel => "cancel",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "allow" => Some(Self::Allow),
            "deny" => Some(Self::Deny),
            "cancel" => Some(Self::Cancel),
            _ => None,
        }
    }
}

/// What the user is being asked to approve.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalRequest {
    pub id: String,
    pub session_id: String,
    /// Empty when the request was raised before a run row existed.
    pub run_id: String,
    /// `read_outside_project`, `command` or `tool`.
    pub kind: String,
    pub title: String,
    pub detail: String,
    pub created_at: String,
}

/// A question the runtime cannot answer on its own.
///
/// Runtimes that run headless still have to ask the human something: pi's
/// extensions reach for `ctx.ui`, which in RPC mode arrives here as an
/// `extension_ui_request`. The runtime blocks until the answer comes back, so
/// a request that is dropped leaves the turn waiting forever.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UiRequest {
    /// The runtime's own id for this question; the answer must carry it back.
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub run_id: String,
    /// `select`, `confirm`, `input`, `editor`, or one of the fire-and-forget
    /// methods (`notify`, `setStatus`, `setWidget`, `setTitle`).
    #[serde(default)]
    pub method: String,
    #[serde(default)]
    pub title: String,
    /// The body of a `confirm` or `notify`.
    #[serde(default)]
    pub message: String,
    /// The choices of a `select`.
    #[serde(default)]
    pub options: Vec<String>,
    /// The hint of an `input`.
    #[serde(default)]
    pub placeholder: String,
    /// The starting text of an `editor`.
    #[serde(default)]
    pub prefill: String,
    /// `info`, `warning` or `error` for a `notify`.
    #[serde(default)]
    pub notify_type: String,
    /// How long the runtime waits before it moves on without an answer.
    #[serde(default)]
    pub timeout_ms: Option<u64>,
    #[serde(default)]
    pub created_at: String,
}

impl UiRequest {
    /// Whether the runtime waits for an answer before it can continue.
    ///
    /// The fire-and-forget methods only want to be shown; answering them would
    /// be noise.
    #[must_use]
    pub fn expects_answer(&self) -> bool {
        matches!(
            self.method.as_str(),
            "select" | "confirm" | "input" | "editor"
        )
    }
}

/// The user's answer to a [`UiRequest`].
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UiAnswer {
    /// The chosen option or the typed text.
    #[serde(default)]
    pub value: Option<String>,
    /// The yes/no answer of a `confirm`.
    #[serde(default)]
    pub confirmed: Option<bool>,
    /// The question was dismissed without an answer.
    ///
    /// Defaulted, because the page sends only the part the question asked
    /// for: a `confirm` carries a yes/no, and a `select` carries a value.
    #[serde(default)]
    pub cancelled: bool,
}

impl UiAnswer {
    /// The answer a dismissed question gets: the runtime treats it as "no".
    #[must_use]
    pub fn cancelled() -> Self {
        Self {
            value: None,
            confirmed: Some(false),
            cancelled: true,
        }
    }
}

/// One entry in the composer's `@` list.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextEntry {
    /// Absolute path.
    pub path: String,
    /// Project-relative path, which is what the user sees.
    pub relative: String,
    pub kind: ContextKind,
    pub bytes: u64,
}

/// What the caller asks the adapter to open.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionSpec {
    pub runtime_id: String,
    /// Model id the runtime understands. Never left empty: a runtime default
    /// that the account cannot use is a real failure on this machine.
    pub model: String,
    pub cwd: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// Resume an existing native session instead of starting a new one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub native_id: Option<String>,
    /// Whether the runtime may use tools. Off by default: a headless run that
    /// stops to ask for approval would hang, so a case opts in explicitly.
    #[serde(default)]
    pub allow_tools: bool,
    /// Tools the runtime is allowed to use when `allow_tools` is on. Empty means
    /// "the runtime's own default set"; a named list is a real restriction, not
    /// a hint.
    #[serde(default)]
    pub tools: Vec<String>,
    /// Agent the session runs as; empty for a session opened without one.
    ///
    /// Kept on the session row so the ledger can answer "what did this agent
    /// run" and so a resumed turn uses the same persona, not whatever the page
    /// happens to hold now.
    #[serde(default)]
    pub agent_id: String,
    /// Thinking level the runtime should use (`off`…`high`); empty leaves the
    /// runtime's own default in place.
    #[serde(default)]
    pub thinking: String,
    /// Persona prompt, sent as the replacement system prompt.
    #[serde(default)]
    pub system_prompt: String,
    /// Extra system text, appended after the persona prompt. Cross-session
    /// memory is delivered through this rather than by rewriting the persona.
    #[serde(default)]
    pub append_system_prompt: String,
    /// Skill files or directories the run may load.
    #[serde(default)]
    pub skills: Vec<String>,
}

/// What the adapter returns once the runtime confirmed a session.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OpenedSession {
    pub runtime_id: String,
    pub native_id: String,
    pub model: String,
    /// Directory the runtime must run in. A tool that resolves a relative path
    /// must resolve it against the project the user chose, not against whatever
    /// directory the host happened to start in.
    #[serde(default)]
    pub cwd: String,
    /// Carried from the session row, because `send` receives no `SessionSpec`.
    #[serde(default)]
    pub allow_tools: bool,
    #[serde(default)]
    pub tools: Vec<String>,
    #[serde(default)]
    pub agent_id: String,
    #[serde(default)]
    pub thinking: String,
    #[serde(default)]
    pub system_prompt: String,
    #[serde(default)]
    pub append_system_prompt: String,
    #[serde(default)]
    pub skills: Vec<String>,
    /// Whether this run may load the working folder's own `.pi` resources.
    ///
    /// `Some(true)` maps to the runtime's `--approve`, `Some(false)` to
    /// `--no-approve`, `None` leaves the runtime's default — which for a
    /// non-interactive run is "skip", because there is nobody to ask.
    #[serde(default)]
    pub project_trust: Option<bool>,
    /// The permission level this turn runs under: `readonly`, `medium`,
    /// `high` or `full`.
    ///
    /// A runtime that can ask the user something needs the level, not only the
    /// flags it implies: in `full` the user already said they trust this
    /// workspace, so such a question is answered for them.
    #[serde(default)]
    pub permission: String,
}

/// Where an adapter may write, and whether a run has a deadline.
#[derive(Debug, Clone)]
pub struct SessionContext {
    /// D:-only directory for runtime session files.
    pub state_dir: std::path::PathBuf,
    /// Optional wall clock budget for one run.
    ///
    /// `None` means the run is not limited by time: it ends when the runtime
    /// finishes, fails, or the user stops it. The desktop app passes `None` —
    /// a long tool-using turn is real work, not a hang, and cutting it at some
    /// round number threw away replies that were still being written. Tests and
    /// the report example still use a deadline so a broken adapter cannot make
    /// them wait forever.
    pub run_timeout: Option<std::time::Duration>,
}

/// Adapter failure, kept separate from `Error` so a runtime that is simply not
/// installed is not confused with a bug in this crate.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AdapterFailure {
    pub runtime_id: String,
    pub code: String,
    pub detail: String,
}

impl AdapterFailure {
    #[must_use]
    pub fn new(runtime_id: &str, code: &str, detail: &str) -> Self {
        Self {
            runtime_id: runtime_id.to_string(),
            code: code.to_string(),
            detail: detail.to_string(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Every level in the picker has to differ from its neighbours in something
    /// a user could observe, or the control is decoration.
    #[test]
    fn each_permission_level_maps_to_distinct_flags() {
        let readonly = permission_plan(Some("readonly"));
        let medium = permission_plan(Some("medium"));
        let high = permission_plan(Some("high"));
        let full = permission_plan(Some("full"));

        assert!(readonly.read_only, "read-only must narrow the tool list");
        assert!(!medium.read_only, "medium keeps the session's tools");
        assert!(!high.read_only, "high keeps the session's tools");

        assert_eq!(medium.project_trust, Some(false), "medium skips a folder's .pi");
        assert_eq!(high.project_trust, Some(true), "high loads it");
        assert_eq!(full.project_trust, Some(true));

        assert!(!readonly.trusts_workspace);
        assert!(!medium.trusts_workspace);
        assert!(!high.trusts_workspace, "only full stops asking about outside paths");
        assert!(full.trusts_workspace);

        // All four are distinct, which is the whole point.
        let all = [readonly, medium, high, full];
        for (index, left) in all.iter().enumerate() {
            for right in &all[index + 1..] {
                assert_ne!(left, right, "{left:?} and {right:?} behave the same");
            }
        }
    }

    #[test]
    fn an_unknown_permission_falls_back_to_asking() {
        for value in [None, Some(""), Some("  "), Some("superuser")] {
            assert_eq!(permission_plan(value), PermissionPlan::default(), "{value:?}");
            assert!(!permission_plan(value).trusts_workspace);
            assert!(!permission_plan(value).read_only);
        }
        // Case and stray spaces are the picker's problem, not the runtime's.
        assert_eq!(permission_plan(Some(" FULL ")), permission_plan(Some("full")));
    }

    #[test]
    fn statuses_round_trip_through_labels() {
        for status in [
            SessionStatus::Idle,
            SessionStatus::Running,
            SessionStatus::Aborted,
            SessionStatus::Failed,
            SessionStatus::Archived,
        ] {
            assert_eq!(SessionStatus::from_label(status.as_str()), Some(status));
        }
        for status in [
            RunStatus::Running,
            RunStatus::Completed,
            RunStatus::Aborted,
            RunStatus::Failed,
        ] {
            assert_eq!(RunStatus::from_label(status.as_str()), Some(status));
        }
    }

    #[test]
    fn event_kinds_round_trip_through_labels() {
        for kind in [
            EventKind::SessionStarted,
            EventKind::TurnStarted,
            EventKind::TextDelta,
            EventKind::MessageCompleted,
            EventKind::ToolCall,
            EventKind::ToolResult,
            EventKind::ApprovalRequest,
            EventKind::ApprovalResolved,
            EventKind::Usage,
            EventKind::Error,
            EventKind::TurnCompleted,
            EventKind::Aborted,
            EventKind::Compacted,
            EventKind::ContextAttached,
            EventKind::Unknown,
        ] {
            assert_eq!(EventKind::from_label(kind.as_str()), Some(kind));
        }
    }

    #[test]
    fn wire_shape_is_camel_case() {
        let summary = SessionSummary {
            id: "s1".into(),
            runtime_id: "pi".into(),
            title: "t".into(),
            status: SessionStatus::Idle,
            native_id: "n1".into(),
            model: "m".into(),
            project_path: "D:\\p".into(),
            parent_id: String::new(),
            branch_at: 0,
            allow_tools: false,
            tools: Vec::new(),
            pinned: false,
            archived: false,
            message_count: 2,
            created_at: "2026-11-13T00:00:00Z".into(),
            updated_at: "2026-11-13T00:00:00Z".into(),
            preview: "hi".into(),
            ..Default::default()
        };
        let json = serde_json::to_value(&summary).expect("serialises");
        assert_eq!(json["runtimeId"], "pi");
        assert_eq!(json["nativeId"], "n1");
        assert_eq!(json["messageCount"], 2);
        assert_eq!(json["projectPath"], "D:\\p");
        assert_eq!(json["parentId"], "");
        assert_eq!(json["status"], "idle");
    }

    #[test]
    fn an_empty_query_falls_back_to_the_default_page_size() {
        let query = SessionQuery::default();
        assert_eq!(query.page_size(), 50);
        assert_eq!(
            SessionQuery {
                limit: Some(0),
                ..SessionQuery::default()
            }
            .page_size(),
            1
        );
        assert_eq!(
            SessionQuery {
                limit: Some(9_999),
                ..SessionQuery::default()
            }
            .page_size(),
            200
        );
        let normalized = SessionQuery {
            runtime_id: Some("  pi ".into()),
            search: Some("   ".into()),
            ..SessionQuery::default()
        }
        .normalized();
        assert_eq!(normalized.runtime_id.as_deref(), Some("pi"));
        assert!(normalized.search.is_none());
    }

    #[test]
    fn running_is_the_only_non_terminal_status() {
        assert!(!RunStatus::Running.is_terminal());
        assert!(RunStatus::Completed.is_terminal());
        assert!(RunStatus::Aborted.is_terminal());
        assert!(RunStatus::Failed.is_terminal());
    }
}
