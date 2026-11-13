//! The contract every runtime adapter implements.
//!
//! Shared code never branches on a runtime name: it asks the registry for an
//! adapter, calls the contract and persists what comes back. A new runtime is a
//! new module plus one `register` call.

use std::path::PathBuf;
use std::time::Duration;

use crate::error::Result;
use crate::model::{
    Capabilities, InventoryKind, McpRecord, ModelRecord, RuntimeCandidate, RuntimeRegistration,
    RuntimeStatus, SkillRecord, ToolRecord,
};

/// Inputs available while discovering installs.
pub struct DiscoveryContext {
    pub home: PathBuf,
    pub timeout: Duration,
}

/// Inputs available while probing one candidate.
pub struct ProbeContext {
    pub home: PathBuf,
    pub timeout: Duration,
}

/// Outcome of probing one candidate.
#[derive(Clone, Debug)]
pub struct ProbeReport {
    pub version: Option<String>,
    pub status: RuntimeStatus,
    pub config_path: Option<PathBuf>,
    pub detail: Option<String>,
    pub latency_ms: u64,
}

/// One resource family returned by a sync.
#[derive(Clone, Debug)]
pub enum Inventory {
    Models(Vec<ModelRecord>),
    Skills(Vec<SkillRecord>),
    Tools(Vec<ToolRecord>),
    Mcp(Vec<McpRecord>),
}

impl Inventory {
    pub fn kind(&self) -> InventoryKind {
        match self {
            Inventory::Models(_) => InventoryKind::Models,
            Inventory::Skills(_) => InventoryKind::Skills,
            Inventory::Tools(_) => InventoryKind::Tools,
            Inventory::Mcp(_) => InventoryKind::Mcp,
        }
    }

    pub fn len(&self) -> usize {
        match self {
            Inventory::Models(items) => items.len(),
            Inventory::Skills(items) => items.len(),
            Inventory::Tools(items) => items.len(),
            Inventory::Mcp(items) => items.len(),
        }
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

/// Result of a real model verification request.
#[derive(Clone, Debug)]
pub struct ModelVerification {
    pub ok: bool,
    /// Short diagnostic; never contains the credential.
    pub detail: Option<String>,
}

/// Result of an explicit MCP connection attempt.
#[derive(Clone, Debug)]
pub struct McpProbe {
    pub ok: bool,
    pub tools: Vec<String>,
    pub detail: Option<String>,
}

/// A runtime family (Pi, Codex, …).
///
/// Discovery and probing are mandatory; resource sync and the two explicit
/// actions are optional so an adapter can declare a capability it does not
/// implement yet instead of faking an empty result.
pub trait RuntimeAdapter: Send + Sync {
    /// Stable, extensible id (`pi`, `codex`, `acme-cli`).
    fn id(&self) -> &'static str;

    /// Human display name; the UI translates nothing here.
    fn display_name(&self) -> &'static str;

    /// What this adapter supports on any machine.
    fn capabilities(&self) -> Capabilities;

    /// Every install on this machine. Must not install, log in or spawn MCP
    /// servers; executable calls are limited to bounded `--version` probes.
    fn discover(&self, context: &DiscoveryContext) -> Result<Vec<RuntimeCandidate>>;

    /// Version, auth and health for one candidate.
    fn probe(&self, context: &ProbeContext, candidate: &RuntimeCandidate) -> Result<ProbeReport>;

    /// Sync one resource family. `None` means the capability is not implemented.
    fn sync(
        &self,
        _context: &ProbeContext,
        _registration: &RuntimeRegistration,
        _kind: InventoryKind,
    ) -> Option<Result<Inventory>> {
        None
    }

    /// Verify a model through a real request. `None` means unsupported.
    fn verify_model(
        &self,
        _context: &ProbeContext,
        _registration: &RuntimeRegistration,
        _model: &ModelRecord,
        _credential: Option<&str>,
    ) -> Option<Result<ModelVerification>> {
        None
    }

    /// Connect to one declared MCP server and enumerate its tools.
    fn probe_mcp(
        &self,
        _context: &ProbeContext,
        _registration: &RuntimeRegistration,
        _server: &McpRecord,
    ) -> Option<Result<McpProbe>> {
        None
    }
}
