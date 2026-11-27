//! Data the registry, the store and the UI exchange.
//!
//! Every type is serialisable because it crosses the Tauri boundary unchanged;
//! nothing here knows about a specific vendor.

use serde::{Deserialize, Serialize};

/// What an adapter can do, per optional capability.
///
/// `Unverified` is not the same as `Unsupported`: it means the adapter has the
/// capability but this machine has not proven it yet, so the UI must not show a
/// disabled control as if the runtime could never do it.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CapabilityState {
    Supported,
    Unsupported,
    #[default]
    Unverified,
}

/// Capability matrix of one adapter.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Capabilities {
    pub models: CapabilityState,
    pub custom_models: CapabilityState,
    pub skills: CapabilityState,
    pub tools: CapabilityState,
    pub mcp: CapabilityState,
    pub sessions: CapabilityState,
}

impl Capabilities {
    /// Everything unsupported; adapters opt in.
    pub fn none() -> Self {
        Self {
            models: CapabilityState::Unsupported,
            custom_models: CapabilityState::Unsupported,
            skills: CapabilityState::Unsupported,
            tools: CapabilityState::Unsupported,
            mcp: CapabilityState::Unsupported,
            sessions: CapabilityState::Unsupported,
        }
    }

    /// Capability state for one resource kind.
    pub fn state(&self, kind: InventoryKind) -> CapabilityState {
        match kind {
            InventoryKind::Models => self.models,
            InventoryKind::Skills => self.skills,
            InventoryKind::Tools => self.tools,
            InventoryKind::Mcp => self.mcp,
        }
    }
}

/// Lifecycle state of a discovered runtime.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RuntimeStatus {
    /// Nothing found on this machine.
    NotInstalled,
    /// Executable found, version could not be read yet.
    Detected,
    /// Found, but the version is below the adapter's minimum.
    VersionUnsupported,
    /// Found, but the runtime has no usable credential.
    Unauthenticated,
    /// Found and probed; usable.
    Available,
    /// Found, but the probe failed (timeout, crash, bad output).
    ProbeFailed,
    /// The adapter itself failed during discovery.
    AdapterFailed,
    /// A previous registration whose adapter is no longer registered.
    Orphaned,
}

impl RuntimeStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            RuntimeStatus::NotInstalled => "not_installed",
            RuntimeStatus::Detected => "detected",
            RuntimeStatus::VersionUnsupported => "version_unsupported",
            RuntimeStatus::Unauthenticated => "unauthenticated",
            RuntimeStatus::Available => "available",
            RuntimeStatus::ProbeFailed => "probe_failed",
            RuntimeStatus::AdapterFailed => "adapter_failed",
            RuntimeStatus::Orphaned => "orphaned",
        }
    }

    pub fn from_label(text: &str) -> Option<Self> {
        Some(match text {
            "not_installed" => RuntimeStatus::NotInstalled,
            "detected" => RuntimeStatus::Detected,
            "version_unsupported" => RuntimeStatus::VersionUnsupported,
            "unauthenticated" => RuntimeStatus::Unauthenticated,
            "available" => RuntimeStatus::Available,
            "probe_failed" => RuntimeStatus::ProbeFailed,
            "adapter_failed" => RuntimeStatus::AdapterFailed,
            "orphaned" => RuntimeStatus::Orphaned,
            _ => return None,
        })
    }
}

/// Where the executable was found.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum InstallSource {
    /// Found by walking `PATH`.
    Path,
    /// Found in a platform-known install directory.
    KnownDir,
    /// Found through an npm/PowerShell shim next to the real entry point.
    Shim,
    /// Injected by a contract test, not a real installation.
    Injected,
    /// Shipped inside this app's own install resources (ADR 0009).
    Bundled,
}

impl InstallSource {
    pub fn as_str(self) -> &'static str {
        match self {
            InstallSource::Path => "path",
            InstallSource::KnownDir => "known_dir",
            InstallSource::Shim => "shim",
            InstallSource::Injected => "injected",
            InstallSource::Bundled => "bundled",
        }
    }

    pub fn from_label(text: &str) -> Option<Self> {
        Some(match text {
            "path" => InstallSource::Path,
            "known_dir" => InstallSource::KnownDir,
            "shim" => InstallSource::Shim,
            "injected" => InstallSource::Injected,
            "bundled" => InstallSource::Bundled,
            _ => return None,
        })
    }
}

/// Result of one health probe.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RuntimeHealth {
    pub checked_at: String,
    pub latency_ms: u64,
    /// Short diagnostic (never a credential).
    pub detail: Option<String>,
}

/// One executable an adapter found.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RuntimeCandidate {
    pub runtime_id: String,
    pub executable: std::path::PathBuf,
    pub source: InstallSource,
    /// Config instance key when one runtime can have several profiles.
    pub instance: Option<String>,
}

/// A probed runtime, ready to be persisted.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RuntimeRegistration {
    pub id: String,
    pub runtime_id: String,
    pub display_name: String,
    pub version: Option<String>,
    pub executable: std::path::PathBuf,
    pub source: InstallSource,
    pub config_path: Option<std::path::PathBuf>,
    pub status: RuntimeStatus,
    pub capabilities: Capabilities,
    pub health: RuntimeHealth,
    pub last_seen: String,
}

/// What the store returns for one runtime row.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RuntimeRecord {
    pub id: String,
    pub runtime_id: String,
    pub display_name: String,
    pub version: Option<String>,
    pub install_path: String,
    pub install_source: String,
    pub config_path: Option<String>,
    pub status: String,
    pub capabilities: Capabilities,
    pub health: RuntimeHealth,
    pub last_seen: String,
    pub first_seen: String,
    /// `fresh` when the last sync succeeded, `stale` when it failed and an older
    /// snapshot is being served, `never` when nothing was synced yet.
    pub snapshot_state: String,
    pub adapter_registered: bool,
}

/// Resource families the catalog tracks.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum InventoryKind {
    Models,
    Skills,
    Tools,
    Mcp,
}

impl InventoryKind {
    pub fn as_str(self) -> &'static str {
        match self {
            InventoryKind::Models => "models",
            InventoryKind::Skills => "skills",
            InventoryKind::Tools => "tools",
            InventoryKind::Mcp => "mcp",
        }
    }

    pub fn from_label(text: &str) -> Option<Self> {
        Some(match text {
            "models" => InventoryKind::Models,
            "skills" => InventoryKind::Skills,
            "tools" => InventoryKind::Tools,
            "mcp" => InventoryKind::Mcp,
            _ => return None,
        })
    }
}

/// One model as reported by a runtime or added by the user.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ModelRecord {
    pub runtime_id: String,
    pub model_id: String,
    pub display_name: String,
    /// `discovered` (runtime reported) or `custom` (user added).
    pub origin: String,
    pub protocol: Option<String>,
    pub endpoint: Option<String>,
    /// Whether a real request succeeded; `false` also covers "detected only".
    pub verified: bool,
    pub verified_at: Option<String>,
    pub is_default: bool,
    /// Whether the model appears in the chat picker.
    ///
    /// Discovery finds every model an installed runtime can reach, which on a
    /// developer machine is dozens. Offering all of them made the picker a dump
    /// of the machine's config; only models the user turned on are offered.
    pub enabled: bool,
    pub updated_at: String,
    /// Never the credential itself, only whether one is stored.
    pub has_credential: bool,
}

/// One skill found in a runtime's skill roots.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SkillRecord {
    pub runtime_id: String,
    pub name: String,
    pub description: String,
    pub root: String,
    pub path: String,
    /// `valid`, `broken` (unreadable/malformed) or `linked` (junction/symlink).
    pub state: String,
    pub reason: Option<String>,
    pub updated_at: String,
}

/// One local tool declaration.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolRecord {
    pub runtime_id: String,
    pub name: String,
    /// `builtin`, `configured` or `command`.
    pub kind: String,
    pub executable: Option<String>,
    /// Permission claim from the declaration, not a granted right.
    pub permission: String,
    pub source: String,
}

/// Thinking levels the runtime accepts, lowest first.
pub const THINKING_LEVELS: [&str; 4] = ["off", "low", "medium", "high"];

/// Permission modes the agent page offers, narrowest first.
pub const PERMISSION_MODES: [&str; 4] = ["readonly", "medium", "high", "full"];

/// Context-window presets, in the order the page offers them.
pub const CONTEXT_WINDOWS: [&str; 3] = ["200K", "400K", "1M"];

/// How a main agent may hand work to its sub agents.
pub const DELEGATION_POLICIES: [&str; 3] = ["auto", "explicit", "never"];

/// Which side of the agent list a row belongs to.
pub const AGENT_GROUPS: [&str; 2] = ["main", "sub"];

/// Tools the read-only permission mode keeps.
///
/// These are pi's own tool names, and they are what the adapter passes to
/// `--tools`; a mode that named tools the runtime does not have would look
/// enforced and change nothing.
pub const READ_ONLY_TOOLS: [&str; 4] = ["read", "grep", "find", "ls"];

/// Built-in tool set a runtime exposes when the user has not narrowed it.
///
/// Names come from `pi --help`, which documents them in its own examples
/// (`--tools read,grep,find,ls`, `--exclude-tools ask_question`).
pub const BUILTIN_TOOLS: [&str; 7] = ["read", "write", "edit", "bash", "grep", "find", "ls"];

/// Tools a runtime can expose *besides* its built-ins.
///
/// `subagent` is what pi's official `subagent` extension registers, and what
/// lets the runtime delegate to child agents. It has to be named in the tool
/// list, because `--tools` is a whole allowlist: leaving it out hid the
/// extension from the model, so a machine with the extension installed still
/// could not delegate.
///
/// Naming a tool the runtime does not have is harmless — pi ignores unknown
/// names rather than failing — so this belongs in the default set even when the
/// extension is not installed.
pub const DELEGATION_TOOLS: [&str; 1] = ["subagent"];

/// The one tool name that delegates; used where the policy has to remove it.
pub const SUBAGENT_TOOL: &str = DELEGATION_TOOLS[0];

/// The tool list a new agent gets: the built-ins plus delegation.
///
/// Read-only mode deliberately excludes delegation: a child agent runs its own
/// tools, so allowing it would let a "read only" turn write through one.
pub const DEFAULT_TOOLS: [&str; 8] = [
    "read", "write", "edit", "bash", "grep", "find", "ls", "subagent",
];

/// One agent: a name on a runtime+model pair plus the persona it runs with.
///
/// The composer offers agents and models; an agent is a saved pair with the
/// settings a conversation inherits. Picking one is the same code path as
/// picking a model with the persona decisions already made. The built-in
/// `default` agent is a stored row like any other (it is created on the first
/// snapshot), so the user can bind it to whatever runtime and model they run;
/// only its removal is refused.
///
/// Only the persona fields are executed by this build: `thinking`,
/// `system_prompt`, `tools` and `skills` reach the runtime as command-line
/// arguments. The rest are stored so the page keeps what the user configured,
/// and every surface that cannot execute a field says so instead of pretending.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentRecord {
    pub id: String,
    pub name: String,
    /// Stable identifier the user types; unique across agents.
    pub slug: String,
    pub description: String,
    pub runtime_id: String,
    pub model_id: String,
    /// True for the built-in `default` agent, which cannot be removed.
    pub is_default: bool,
    /// `off` | `low` | `medium` | `high`; reaches the runtime as `--thinking`.
    pub thinking: String,
    /// Preset label only; the runtime caps its own window.
    pub context_window: String,
    /// Bring a summary of this agent's earlier conclusions into a new session.
    pub memory: bool,
    /// Fold early turns once the window is nearly full.
    pub auto_compact: bool,
    /// Sent as `--system-prompt`; `{{variables}}` are rendered per session.
    pub system_prompt: String,
    /// Tool names the runtime may use. Empty means the runtime's own default
    /// set; a named list is a real allowlist passed as `--tools`.
    pub tools: Vec<String>,
    /// Skill names mounted for this agent, resolved to paths when a session
    /// opens and passed as `--skill`.
    pub skills: Vec<String>,
    pub permission: String,
    pub writable_dirs: String,
    pub command_allowlist: String,
    pub network: bool,
    pub confirm_dangerous: bool,
    /// `main` or `sub`.
    pub group_kind: String,
    /// Main agent a sub agent was delegated from; empty for `main`.
    pub delegated_from: String,
    pub delegation_policy: String,
    /// Global ceiling on concurrent delegated runs.
    pub concurrency: u32,
    pub enabled: bool,
    pub updated_at: String,
}

impl AgentRecord {
    /// The settings a new agent starts from.
    #[must_use]
    pub fn default_persona() -> Self {
        Self {
            id: String::new(),
            name: String::new(),
            slug: String::new(),
            description: String::new(),
            runtime_id: String::new(),
            model_id: String::new(),
            is_default: false,
            thinking: "medium".to_string(),
            context_window: "400K".to_string(),
            memory: false,
            auto_compact: true,
            system_prompt: String::new(),
            tools: Vec::new(),
            skills: Vec::new(),
            permission: "medium".to_string(),
            writable_dirs: String::new(),
            command_allowlist: String::new(),
            network: true,
            confirm_dangerous: true,
            group_kind: "main".to_string(),
            delegated_from: String::new(),
            delegation_policy: "auto".to_string(),
            concurrency: 4,
            enabled: true,
            updated_at: String::new(),
        }
    }

    /// Narrow the tool allowlist to what the permission mode permits.
    ///
    /// `readonly` is the only mode this build can turn into a runtime fact: it
    /// becomes pi's `--tools read,grep,find,ls`. The wider modes keep the
    /// configured list, because pi has no approval channel to consult.
    #[must_use]
    pub fn effective_tools(&self) -> Vec<String> {
        if self.permission == "readonly" {
            return READ_ONLY_TOOLS
                .iter()
                .map(|name| (*name).to_string())
                .collect();
        }
        self.tools.clone()
    }

    /// Whether the runtime may use any tool at all.
    ///
    /// Turning every tool off is a real choice, so it maps to `--no-tools`
    /// rather than to "the runtime's default set".
    #[must_use]
    pub fn allows_tools(&self) -> bool {
        !self.effective_tools().is_empty()
    }

    /// The allowlist the runtime should receive, after the delegation policy.
    ///
    /// `never` is the only policy this build can turn into a runtime fact: it
    /// drops `subagent` from the allowlist, so the model is not offered a tool
    /// whose use would contradict the setting. An empty configured list means
    /// "the runtime's default set" — which includes `subagent` — so it becomes
    /// the built-ins minus `subagent` rather than "no tools": refusing to
    /// delegate must not leave the agent unable to work.
    #[must_use]
    pub fn runtime_tools(&self) -> Vec<String> {
        let mut tools = self.effective_tools();
        if self.delegation_policy != "never" {
            return tools;
        }
        tools.retain(|name| name != SUBAGENT_TOOL);
        if tools.is_empty() {
            tools = DEFAULT_TOOLS
                .iter()
                .filter(|name| **name != SUBAGENT_TOOL)
                .map(|name| (*name).to_string())
                .collect();
        }
        tools
    }
}

/// What the agent page sends when it saves.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct AgentDraft {
    pub id: Option<String>,
    pub name: String,
    pub slug: String,
    pub description: String,
    pub runtime_id: String,
    pub model_id: String,
    pub thinking: String,
    pub context_window: String,
    pub memory: bool,
    pub auto_compact: bool,
    pub system_prompt: String,
    pub tools: Vec<String>,
    pub skills: Vec<String>,
    pub permission: String,
    pub writable_dirs: String,
    pub command_allowlist: String,
    pub network: bool,
    pub confirm_dangerous: bool,
    pub group_kind: String,
    pub delegated_from: String,
    pub delegation_policy: String,
    pub concurrency: u32,
    pub enabled: bool,
}

/// One MCP server declaration, with every secret removed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct McpRecord {
    pub runtime_id: String,
    pub name: String,
    /// `stdio` or `http`/`sse`.
    pub transport: String,
    pub command: Option<String>,
    pub args: Vec<String>,
    pub url: Option<String>,
    /// Names only; values are never imported.
    pub env_keys: Vec<String>,
    pub header_keys: Vec<String>,
    pub source: String,
    /// `declared` until an explicit probe succeeds.
    pub state: String,
    pub probed_at: Option<String>,
    /// Tool names the last successful probe listed; empty until then.
    #[serde(default)]
    pub tools: Vec<String>,
}

/// Result of one sync, kept so a failure can be told apart from an empty list.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncOutcome {
    pub runtime_id: String,
    pub kind: String,
    pub ok: bool,
    pub item_count: usize,
    pub fetched_at: String,
    pub stale: bool,
    pub error: Option<String>,
}

/// One adapter that failed during a scan.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AdapterFailure {
    pub runtime_id: String,
    pub phase: String,
    pub error: String,
}

/// Everything the UI needs in one payload.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CatalogSnapshot {
    pub runtimes: Vec<RuntimeRecord>,
    pub models: Vec<ModelRecord>,
    /// Saved runtime+model bindings. The default agent is prepended by the
    /// service, so the list always has exactly one `default`.
    pub agents: Vec<AgentRecord>,
    pub skills: Vec<SkillRecord>,
    pub tools: Vec<ToolRecord>,
    pub mcp: Vec<McpRecord>,
    pub syncs: Vec<SyncOutcome>,
    pub adapter_failures: Vec<AdapterFailure>,
    pub scanned_at: Option<String>,
    /// A discovery pass is still running, so the rows above are what the host
    /// last committed rather than a fresh probe.
    ///
    /// The first `catalog_snapshot` of a process answers from disk and starts
    /// discovery behind it. Without this flag an empty profile looks exactly
    /// like a machine with nothing installed, and the UI would say "没有可用的
    /// 运行时" while it is still reading them.
    #[serde(default)]
    pub scanning: bool,
    /// The user finished first-run setup.
    ///
    /// Stored in the data directory, not in the WebView: the wizard has to stay
    /// closed even when localStorage was lost (a forced quit, a storage reset),
    /// and it must not be inferred from model state — discovery enables models
    /// by itself on a brand-new profile.
    #[serde(default)]
    pub setup_done: bool,
}

#[cfg(test)]
mod delegation_tests {
    use super::*;

    /// "Never delegate" has to remove the tool as well as the roster: a UI
    /// switch that leaves the model holding `subagent` is a switch that does
    /// nothing.
    #[test]
    fn never_delegating_removes_the_subagent_tool() {
        let mut agent = AgentRecord::default_persona();
        agent.delegation_policy = "never".to_string();
        agent.tools = vec!["read".to_string(), "subagent".to_string()];
        assert_eq!(agent.runtime_tools(), vec!["read"]);

        // An empty list means "the runtime default", which contains subagent.
        // It must become the built-ins without it, never an empty allowlist.
        agent.tools = Vec::new();
        let tools = agent.runtime_tools();
        assert!(tools.iter().any(|name| name == "bash"));
        assert!(!tools.iter().any(|name| name == SUBAGENT_TOOL));
    }

    /// Every other policy leaves the allowlist exactly as configured.
    #[test]
    fn auto_and_explicit_keep_the_delegation_tool() {
        for policy in ["auto", "explicit"] {
            let mut agent = AgentRecord::default_persona();
            agent.delegation_policy = policy.to_string();
            agent.tools = vec!["read".to_string(), "subagent".to_string()];
            assert_eq!(agent.runtime_tools(), vec!["read", "subagent"], "{policy}");
        }
    }
}
