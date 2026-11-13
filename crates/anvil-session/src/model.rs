//! Wire types shared by the ledger, the host commands and the UI.
//!
//! Everything is `camelCase` on the wire: the React client consumes these
//! shapes directly, and the acceptance report asserts on the same field names.

use serde::{Deserialize, Serialize};

/// Lifecycle of a session.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SessionStatus {
    /// No run is in flight and the last run finished cleanly.
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
    MessageCompleted,
    ToolCall,
    ToolResult,
    ApprovalRequest,
    ApprovalResolved,
    Usage,
    Error,
    TurnCompleted,
    Aborted,
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
            Self::MessageCompleted => "message_completed",
            Self::ToolCall => "tool_call",
            Self::ToolResult => "tool_result",
            Self::ApprovalRequest => "approval_request",
            Self::ApprovalResolved => "approval_resolved",
            Self::Usage => "usage",
            Self::Error => "error",
            Self::TurnCompleted => "turn_completed",
            Self::Aborted => "aborted",
            Self::Unknown => "unknown",
        }
    }

    #[must_use]
    pub fn from_label(label: &str) -> Option<Self> {
        match label {
            "session_started" => Some(Self::SessionStarted),
            "turn_started" => Some(Self::TurnStarted),
            "text_delta" => Some(Self::TextDelta),
            "message_completed" => Some(Self::MessageCompleted),
            "tool_call" => Some(Self::ToolCall),
            "tool_result" => Some(Self::ToolResult),
            "approval_request" => Some(Self::ApprovalRequest),
            "approval_resolved" => Some(Self::ApprovalResolved),
            "usage" => Some(Self::Usage),
            "error" => Some(Self::Error),
            "turn_completed" => Some(Self::TurnCompleted),
            "aborted" => Some(Self::Aborted),
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
#[derive(Debug, Clone, Serialize, Deserialize)]
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

/// What the caller asks the adapter to open.
#[derive(Debug, Clone, Serialize, Deserialize)]
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
}

/// What the adapter returns once the runtime confirmed a session.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct OpenedSession {
    pub runtime_id: String,
    pub native_id: String,
    pub model: String,
}

/// Where an adapter may write and how long it may take.
#[derive(Debug, Clone)]
pub struct SessionContext {
    /// D:-only directory for runtime session files.
    pub state_dir: std::path::PathBuf,
    /// Wall clock budget for one run.
    pub run_timeout: std::time::Duration,
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
            pinned: false,
            archived: false,
            message_count: 2,
            created_at: "2026-11-13T00:00:00Z".into(),
            updated_at: "2026-11-13T00:00:00Z".into(),
            preview: "hi".into(),
        };
        let json = serde_json::to_value(&summary).expect("serialises");
        assert_eq!(json["runtimeId"], "pi");
        assert_eq!(json["nativeId"], "n1");
        assert_eq!(json["messageCount"], 2);
        assert_eq!(json["status"], "idle");
    }

    #[test]
    fn running_is_the_only_non_terminal_status() {
        assert!(!RunStatus::Running.is_terminal());
        assert!(RunStatus::Completed.is_terminal());
        assert!(RunStatus::Aborted.is_terminal());
        assert!(RunStatus::Failed.is_terminal());
    }
}
