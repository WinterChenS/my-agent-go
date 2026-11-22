//! Turning saved sub-agents into something pi can actually be asked for.
//!
//! [`anvil_session::subagents`] owns the file format and the ownership rules;
//! this module is the host's mapping from catalog rows to those files, plus the
//! two answers the pages need: "did it land" and "what should the parent prompt
//! say".
//!
//! Only `pi` rows are written: the format *is* pi's extension contract, and a
//! codex sub agent dropped into `~/.pi/agent/agents` would be a file that pi
//! loads and the run it was meant for never reads.

use std::path::PathBuf;

use anvil_catalog::model::AgentRecord;
use anvil_session::subagents::{DeclaredAgent, SyncReport, scan, sync};
use serde::Serialize;

/// `<pi agent dir>/agents` — the directory pi's `subagent` extension scans by
/// default. `None` when pi's config directory cannot be resolved at all.
#[must_use]
pub fn agents_dir() -> Option<PathBuf> {
    anvil_session::extensions::agent_dir().map(|dir| dir.join("agents"))
}

/// The rows that should exist as pi agent files, in catalog order.
#[must_use]
pub fn declared(agents: &[AgentRecord]) -> Vec<DeclaredAgent> {
    agents
        .iter()
        .filter(|agent| agent.group_kind == "sub" && agent.enabled && agent.runtime_id == "pi")
        .map(|agent| DeclaredAgent {
            id: agent.id.clone(),
            slug: agent.slug.clone(),
            title: agent.name.clone(),
            description: agent.description.clone(),
            model: agent.model_id.clone(),
            tools: agent.effective_tools(),
            prompt: agent.system_prompt.clone(),
        })
        .collect()
}

/// Write the sub agents of `agents` into pi's directory.
///
/// Failing to resolve the directory is a report, not an error: a machine without
/// pi has nothing to sync, and the agents page still has to open.
#[must_use]
pub fn sync_agents(agents: &[AgentRecord]) -> SyncReport {
    let Some(dir) = agents_dir() else {
        let mut report = SyncReport::default();
        report
            .skipped
            .push("(pi): pi's agent directory cannot be resolved".to_string());
        return report;
    };
    match sync(&dir, &declared(agents)) {
        Ok(report) => report,
        Err(error) => {
            let mut report = SyncReport::default();
            report.skipped.push(format!("(pi): {error}"));
            report
        }
    }
}

/// The delegation block for a session's appended system prompt.
///
/// `never` yields nothing, and the caller also drops `subagent` from the tool
/// allowlist: a roster that describes a tool the model does not have invites
/// calls that cannot succeed.
#[must_use]
pub fn roster_for(policy: &str, concurrency: u32) -> String {
    let Some(dir) = agents_dir() else {
        return String::new();
    };
    anvil_session::subagents::roster(&dir, policy, concurrency)
}

/// One catalog row's standing in pi's directory.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SubagentRow {
    pub slug: String,
    /// `ready` | `conflict` | `no-description` | `disabled` | `other-runtime`.
    pub state: String,
    /// Why, in the user's terms; empty when ready.
    pub detail: String,
}

/// What the agents page shows under the sub-agent table.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SubagentOverview {
    /// `<agent-dir>/agents`; empty when pi is not configured on this machine.
    pub dir: String,
    /// Files pi will load right now (its own and this app's).
    pub files: Vec<String>,
    pub rows: Vec<SubagentRow>,
    /// Files in the directory this app does not own — context for a conflict.
    pub foreign: Vec<String>,
}

/// Read the directory and grade every sub-agent row against it.
///
/// Read-only: the page calls it to draw the state, and a page load must not
/// write files behind the user's back.
#[must_use]
pub fn overview(agents: &[AgentRecord]) -> SubagentOverview {
    let dir = agents_dir();
    let files = dir.as_deref().map(scan).unwrap_or_default();
    let mut rows = Vec::new();
    for agent in agents.iter().filter(|agent| agent.group_kind == "sub") {
        let state = if agent.runtime_id != "pi" {
            SubagentRow {
                slug: agent.slug.clone(),
                state: "other-runtime".to_string(),
                detail: "only pi has a sub-agent file format; this row is saved but not delegated".to_string(),
            }
        } else if !agent.enabled {
            SubagentRow {
                slug: agent.slug.clone(),
                state: "disabled".to_string(),
                detail: "disabled, so its file is removed".to_string(),
            }
        } else if agent.description.trim().is_empty() {
            SubagentRow {
                slug: agent.slug.clone(),
                state: "no-description".to_string(),
                detail: "pi skips a file without a description: the parent has nothing to choose it by".to_string(),
            }
        } else {
            match files.iter().find(|file| file.name == agent.slug) {
                Some(file) if file.owner.as_deref() == Some(agent.id.as_str()) => SubagentRow {
                    slug: agent.slug.clone(),
                    state: "ready".to_string(),
                    detail: String::new(),
                },
                Some(_) => SubagentRow {
                    slug: agent.slug.clone(),
                    state: "conflict".to_string(),
                    detail: "another file already uses this name; rename the agent".to_string(),
                },
                None => SubagentRow {
                    slug: agent.slug.clone(),
                    state: "pending".to_string(),
                    detail: "not written yet: save the agent to materialise it".to_string(),
                },
            }
        };
        rows.push(state);
    }
    SubagentOverview {
        dir: dir.as_ref().map(|path| path.display().to_string()).unwrap_or_default(),
        files: files.iter().map(|file| file.name.clone()).collect(),
        foreign: files
            .iter()
            .filter(|file| file.owner.is_none())
            .map(|file| file.name.clone())
            .collect(),
        rows,
    }
}

/// One line for the host log after a sync.
#[must_use]
pub fn summary(dir: Option<PathBuf>, report: &SyncReport) -> String {
    format!(
        "subagents: dir={} written={} removed={} conflicts={} skipped={}",
        dir.map_or_else(|| "(none)".to_string(), |path| path.display().to_string()),
        report.written,
        report.removed,
        report.conflicts.join("; "),
        report.skipped.join("; ")
    )
}
