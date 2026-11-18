//! Delegated child agents: the one shape every adapter reports them in.
//!
//! The runtimes own the delegation decision — pi through its `subagent`
//! extension, codex through the collab tools. This build never invents a child
//! agent; it only records what the runtime said about the ones it started.
//!
//! One `SubAgent` event is one *observation*, not one agent. Status changes,
//! streamed text, tool calls and the final answer all arrive as separate rows
//! with the same `agentId`, so the ledger stays append-only and the UI folds
//! them into a single card in arrival order. Collapsing them into one mutable
//! row would lose the moments the user most wants to see: the moment a second
//! agent starts, the moment one of them errors while the other keeps going.

use serde::Serialize;
use serde_json::{Value, json};

/// What the runtime last said about an agent's lifecycle.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SubAgentStatus {
    /// Announced but not running yet (queued, or awaiting a slot).
    Pending,
    Running,
    /// Blocked on the parent or on another agent (codex `wait`).
    Waiting,
    Completed,
    Failed,
    Interrupted,
}

impl SubAgentStatus {
    #[must_use]
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Pending => "pending",
            Self::Running => "running",
            Self::Waiting => "waiting",
            Self::Completed => "completed",
            Self::Failed => "failed",
            Self::Interrupted => "interrupted",
        }
    }

    /// True once the agent will not report anything again.
    #[must_use]
    pub fn is_terminal(&self) -> bool {
        matches!(self, Self::Completed | Self::Failed | Self::Interrupted)
    }

    /// Map a codex `CollabAgentStatus` onto the shared vocabulary.
    ///
    /// `notFound`, `shutdown` and `errored` are failures from the reader's point
    /// of view: the agent the parent expected is not going to answer.
    #[must_use]
    pub fn from_codex(label: &str) -> Self {
        match label {
            "pendingInit" => Self::Pending,
            "running" => Self::Running,
            "interrupted" => Self::Interrupted,
            "completed" => Self::Completed,
            "errored" | "notFound" | "shutdown" => Self::Failed,
            _ => Self::Running,
        }
    }

    /// Map a codex `SubAgentActivityKind`.
    #[must_use]
    pub fn from_codex_activity(label: &str) -> Self {
        match label {
            "started" | "interacted" => Self::Running,
            "interrupted" => Self::Interrupted,
            "completed" => Self::Completed,
            _ => Self::Running,
        }
    }
}

/// What kind of observation this row carries.
///
/// The UI uses it to decide whether to append text, add a tool row, or seal the
/// card; `status` alone cannot express "same state, new output".
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SubAgentPhase {
    /// First sighting: names the agent and its task.
    Started,
    /// State changed without new content.
    Status,
    /// A chunk of the agent's own answer.
    Text,
    /// The agent called a tool.
    Tool,
    /// A tool call the agent made produced a result.
    ToolResult,
    /// The whole content stream as of now, replacing what the card holds.
    ///
    /// pi's `subagent` extension reports a cumulative snapshot on every update
    /// (it re-sends every message the child has produced so far), while codex
    /// streams child-thread events one at a time. Sending increments for the
    /// first would duplicate every line; a snapshot says "this is the whole
    /// story", so the reader replaces instead of appending and both runtimes
    /// land in the same card.
    Snapshot,
    /// Finished, with the final answer attached.
    Completed,
    /// Gave up, with the reason attached.
    Failed,
    /// Stopped on request.
    Interrupted,
}

impl SubAgentPhase {
    #[must_use]
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Started => "started",
            Self::Status => "status",
            Self::Text => "text",
            Self::Tool => "tool",
            Self::ToolResult => "tool_result",
            Self::Snapshot => "snapshot",
            Self::Completed => "completed",
            Self::Failed => "failed",
            Self::Interrupted => "interrupted",
        }
    }

    /// The phase implied by a terminal status, if any.
    #[must_use]
    pub fn for_status(status: SubAgentStatus) -> Self {
        match status {
            SubAgentStatus::Completed => Self::Completed,
            SubAgentStatus::Failed => Self::Failed,
            SubAgentStatus::Interrupted => Self::Interrupted,
            SubAgentStatus::Pending | SubAgentStatus::Running | SubAgentStatus::Waiting => {
                Self::Status
            }
        }
    }
}

/// One observation about one delegated agent.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SubAgentReport {
    pub agent_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub parent_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    /// The task the parent handed over, when the runtime reports it.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task: Option<String>,
    pub status: SubAgentStatus,
    pub phase: SubAgentPhase,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,
    /// Which of the agent's own streams the text belongs to: `answer` or
    /// `thinking`. Without it the card cannot tell a child's reasoning from what
    /// it actually said.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub channel: Option<String>,
    /// The text is authoritative and replaces the same channel's tail.
    ///
    /// Codex streams a child's answer twice — deltas, then the finished message
    /// — so the completed copy has to replace what the deltas built instead of
    /// appending a duplicate.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub final_text: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool: Option<Value>,
    /// Ordered content stream for a `snapshot` row: `{kind:"text"|"tool"|"tool_result", ...}`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub steps: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<Value>,
    /// Position in a sequential chain (pi runs `chain` steps in order).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub step: Option<i64>,
}

impl SubAgentReport {
    /// First sighting of an agent. `status` is `running` because a runtime that
    /// announces an agent has already started it; a queued agent is announced
    /// later, when its first observation arrives.
    #[must_use]
    pub fn started(agent_id: &str, name: &str) -> Self {
        Self {
            agent_id: agent_id.to_string(),
            name: Some(name.to_string()),
            parent_id: None,
            model: None,
            task: None,
            status: SubAgentStatus::Running,
            phase: SubAgentPhase::Started,
            text: None,
            channel: None,
            final_text: None,
            tool: None,
            steps: None,
            result: None,
            error: None,
            usage: None,
            step: None,
        }
    }

    /// A row for a status change that carries no content.
    #[must_use]
    pub fn status(agent_id: &str, status: SubAgentStatus) -> Self {
        Self {
            phase: SubAgentPhase::for_status(status),
            status,
            ..Self::started(agent_id, "")
        }
    }

    /// Override the status while keeping the phase. A spawn that is still
    /// `pendingInit` is announced, not running, and saying "running" would make
    /// the card look stuck rather than queued.
    #[must_use]
    pub fn with_status(mut self, status: SubAgentStatus) -> Self {
        self.status = status;
        self
    }

    #[must_use]
    pub fn with_name(mut self, name: &str) -> Self {
        if !name.trim().is_empty() {
            self.name = Some(name.to_string());
        }
        self
    }

    #[must_use]
    pub fn with_model(mut self, model: &str) -> Self {
        if !model.trim().is_empty() {
            self.model = Some(model.to_string());
        }
        self
    }

    #[must_use]
    pub fn with_parent(mut self, parent: &str) -> Self {
        if !parent.trim().is_empty() {
            self.parent_id = Some(parent.to_string());
        }
        self
    }

    #[must_use]
    pub fn with_task(mut self, task: &str) -> Self {
        if !task.trim().is_empty() {
            self.task = Some(task.to_string());
        }
        self
    }

    #[must_use]
    pub fn with_text(mut self, text: &str) -> Self {
        if !text.is_empty() {
            self.text = Some(text.to_string());
            self.phase = SubAgentPhase::Text;
        }
        self
    }

    #[must_use]
    pub fn with_tool(mut self, tool: Value) -> Self {
        self.tool = Some(tool);
        self.phase = SubAgentPhase::Tool;
        self
    }

    #[must_use]
    pub fn with_tool_result(mut self, tool: Value) -> Self {
        self.tool = Some(tool);
        self.phase = SubAgentPhase::ToolResult;
        self
    }

    /// Replace the card's content stream with a complete snapshot.
    /// Tag the text as the child's answer or its reasoning.
    #[must_use]
    pub fn with_channel(mut self, channel: &str) -> Self {
        if !channel.trim().is_empty() {
            self.channel = Some(channel.to_string());
        }
        self
    }

    /// This text replaces the same channel's tail rather than extending it.
    #[must_use]
    pub fn as_final(mut self) -> Self {
        self.final_text = Some(true);
        self
    }

    #[must_use]
    pub fn with_steps(mut self, steps: Value) -> Self {
        self.steps = Some(steps);
        self.phase = SubAgentPhase::Snapshot;
        self
    }

    #[must_use]
    pub fn with_result(mut self, result: &str) -> Self {
        if !result.is_empty() {
            self.result = Some(result.to_string());
        }
        self
    }

    #[must_use]
    pub fn with_error(mut self, error: &str) -> Self {
        if !error.is_empty() {
            self.error = Some(error.to_string());
        }
        self
    }

    #[must_use]
    pub fn with_usage(mut self, usage: Value) -> Self {
        if !usage.is_null() {
            self.usage = Some(usage);
        }
        self
    }

    #[must_use]
    pub fn with_step(mut self, step: i64) -> Self {
        self.step = Some(step);
        self
    }

    /// The payload stored on a `RunEvent`.
    ///
    /// `kind` is duplicated inside the payload so a reader that only has the
    /// JSON (an exported transcript, a future adapter) still knows what it is.
    #[must_use]
    pub fn to_payload(&self) -> Value {
        let mut value = json!({
            "agentId": self.agent_id,
            "status": self.status.as_str(),
            "phase": self.phase.as_str(),
        });
        let map = value.as_object_mut().expect("object");
        if let Some(name) = &self.name
            && !name.is_empty()
        {
            map.insert("name".to_string(), json!(name));
        }
        if let Some(parent) = &self.parent_id {
            map.insert("parentId".to_string(), json!(parent));
        }
        if let Some(model) = &self.model {
            map.insert("model".to_string(), json!(model));
        }
        if let Some(task) = &self.task {
            map.insert("task".to_string(), json!(task));
        }
        if let Some(text) = &self.text {
            map.insert("text".to_string(), json!(text));
        }
        if let Some(tool) = &self.tool {
            map.insert("tool".to_string(), tool.clone());
        }
        if let Some(channel) = &self.channel {
            map.insert("channel".to_string(), json!(channel));
        }
        if self.final_text == Some(true) {
            map.insert("final".to_string(), json!(true));
        }
        if let Some(steps) = &self.steps {
            map.insert("steps".to_string(), steps.clone());
        }
        if let Some(result) = &self.result {
            map.insert("result".to_string(), json!(result));
        }
        if let Some(error) = &self.error {
            map.insert("error".to_string(), json!(error));
        }
        if let Some(usage) = &self.usage {
            map.insert("usage".to_string(), usage.clone());
        }
        if let Some(step) = self.step {
            map.insert("step".to_string(), json!(step));
        }
        value
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_started_report_names_the_agent_and_carries_no_empty_fields() {
        let report = SubAgentReport::started("t1", "scout");
        let payload = report.to_payload();
        assert_eq!(payload["agentId"], "t1");
        assert_eq!(payload["name"], "scout");
        assert_eq!(payload["status"], "running");
        assert_eq!(payload["phase"], "started");
        // Empty optional fields are omitted rather than serialized as null: the
        // UI merges rows, and a null must not overwrite a value seen earlier.
        assert!(payload.get("task").is_none());
        assert!(payload.get("error").is_none());
    }

    #[test]
    fn a_text_row_becomes_a_text_phase() {
        let payload = SubAgentReport::status("t1", SubAgentStatus::Running)
            .with_name("scout")
            .with_text("counting files")
            .to_payload();
        assert_eq!(payload["phase"], "text");
        assert_eq!(payload["text"], "counting files");
        assert_eq!(payload["status"], "running");
    }

    #[test]
    fn terminal_statuses_pick_the_matching_phase() {
        assert_eq!(
            SubAgentPhase::for_status(SubAgentStatus::Completed),
            SubAgentPhase::Completed
        );
        assert_eq!(
            SubAgentPhase::for_status(SubAgentStatus::Failed),
            SubAgentPhase::Failed
        );
        assert_eq!(
            SubAgentPhase::for_status(SubAgentStatus::Waiting),
            SubAgentPhase::Status
        );
        assert!(SubAgentStatus::Interrupted.is_terminal());
        assert!(!SubAgentStatus::Waiting.is_terminal());
    }

    #[test]
    fn codex_statuses_that_mean_failure_are_failures() {
        // `notFound` and `shutdown` are how codex says the agent the parent
        // expects will never answer; showing them as "running" would hang a
        // card forever.
        for label in ["errored", "notFound", "shutdown"] {
            assert_eq!(SubAgentStatus::from_codex(label), SubAgentStatus::Failed);
        }
        assert_eq!(
            SubAgentStatus::from_codex("pendingInit"),
            SubAgentStatus::Pending
        );
        assert_eq!(
            SubAgentStatus::from_codex_activity("interacted"),
            SubAgentStatus::Running
        );
    }
}
