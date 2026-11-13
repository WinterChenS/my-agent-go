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
}

impl InstallSource {
    pub fn as_str(self) -> &'static str {
        match self {
            InstallSource::Path => "path",
            InstallSource::KnownDir => "known_dir",
            InstallSource::Shim => "shim",
            InstallSource::Injected => "injected",
        }
    }

    pub fn from_label(text: &str) -> Option<Self> {
        Some(match text {
            "path" => InstallSource::Path,
            "known_dir" => InstallSource::KnownDir,
            "shim" => InstallSource::Shim,
            "injected" => InstallSource::Injected,
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
    pub skills: Vec<SkillRecord>,
    pub tools: Vec<ToolRecord>,
    pub mcp: Vec<McpRecord>,
    pub syncs: Vec<SyncOutcome>,
    pub adapter_failures: Vec<AdapterFailure>,
    pub scanned_at: Option<String>,
}
