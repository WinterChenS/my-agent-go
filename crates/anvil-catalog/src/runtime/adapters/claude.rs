//! Claude Code adapter.
//!
//! Installs ship as `~/.claude/local/claude.exe` (native installer) or an npm
//! shim; declarations live in `~/.claude.json`, credentials in
//! `~/.claude/.credentials.json`, skills under `~/.claude/skills`. Claude Code
//! publishes no model catalogue on disk, so models are declared unsupported
//! rather than guessed.

use std::path::{Path, PathBuf};

use crate::error::Result;
use crate::model::{
    Capabilities, CapabilityState, InventoryKind, McpRecord, RuntimeCandidate, RuntimeRegistration,
    RuntimeStatus,
};
use crate::runtime::adapter::{
    DiscoveryContext, Inventory, ProbeContext, ProbeReport, RuntimeAdapter,
};
use crate::runtime::adapters::common;
use crate::runtime::version::Version;
use crate::{mcp, skills};

const MIN_VERSION: Version = Version {
    major: 2,
    minor: 0,
    patch: 0,
};

pub struct ClaudeAdapter;

impl ClaudeAdapter {
    fn config_dir(home: &Path) -> PathBuf {
        home.join(".claude")
    }

    fn declarations_file(home: &Path) -> PathBuf {
        home.join(".claude.json")
    }

    fn credentials_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join(".credentials.json")
    }

    fn skills_root(home: &Path) -> PathBuf {
        Self::config_dir(home).join("skills")
    }

    fn install_candidates() -> Vec<PathBuf> {
        let mut paths = Vec::new();
        if let Some(home) = std::env::var_os("USERPROFILE").or_else(|| std::env::var_os("HOME")) {
            let home = PathBuf::from(home);
            paths.push(home.join(".claude").join("local").join(if cfg!(windows) {
                "claude.exe"
            } else {
                "claude"
            }));
            paths.push(home.join(".local").join("bin").join(if cfg!(windows) {
                "claude.exe"
            } else {
                "claude"
            }));
        }
        if let Some(roaming) = std::env::var_os("APPDATA") {
            paths.push(PathBuf::from(roaming).join("npm").join(if cfg!(windows) {
                "claude.cmd"
            } else {
                "claude"
            }));
        }
        paths
    }

    fn read_mcp(home: &Path) -> Result<Vec<McpRecord>> {
        let Some(text) = common::read_text(&Self::declarations_file(home), 16 << 20) else {
            return Ok(Vec::new());
        };
        mcp::from_claude_json("claude", &text, "~/.claude.json")
    }
}

impl RuntimeAdapter for ClaudeAdapter {
    fn id(&self) -> &'static str {
        "claude"
    }

    fn display_name(&self) -> &'static str {
        "Claude Code"
    }

    fn capabilities(&self) -> Capabilities {
        Capabilities {
            models: CapabilityState::Unsupported,
            custom_models: CapabilityState::Unsupported,
            skills: CapabilityState::Supported,
            tools: CapabilityState::Unverified,
            mcp: CapabilityState::Supported,
            sessions: CapabilityState::Supported,
        }
    }

    fn discover(&self, _context: &DiscoveryContext) -> Result<Vec<RuntimeCandidate>> {
        Ok(common::discover_installs(
            "claude",
            "claude",
            &Self::install_candidates(),
        ))
    }

    fn probe(&self, context: &ProbeContext, candidate: &RuntimeCandidate) -> Result<ProbeReport> {
        let probe = common::version_probe(&candidate.executable, context.timeout)?;
        let declarations = Self::declarations_file(&context.home);
        let config_path = if declarations.is_file() {
            Some(declarations)
        } else if Self::config_dir(&context.home).is_dir() {
            Some(Self::config_dir(&context.home))
        } else {
            None
        };
        let status = match probe.version {
            None => RuntimeStatus::ProbeFailed,
            Some(version) if !version.at_least(MIN_VERSION) => RuntimeStatus::VersionUnsupported,
            Some(_) if !common::has_credentials(&Self::credentials_file(&context.home)) => {
                RuntimeStatus::Unauthenticated
            }
            Some(_) => RuntimeStatus::Available,
        };
        let detail = match status {
            RuntimeStatus::ProbeFailed => {
                Some(probe.failure_note(&candidate.executable, context.timeout))
            }
            RuntimeStatus::VersionUnsupported => Some(format!(
                "version {} is below the supported {}.{}.{}",
                probe.version.expect("version"),
                MIN_VERSION.major,
                MIN_VERSION.minor,
                MIN_VERSION.patch
            )),
            RuntimeStatus::Unauthenticated => {
                Some("no credential in ~/.claude/.credentials.json".into())
            }
            _ => None,
        };
        Ok(ProbeReport {
            version: probe.version.map(|value| value.to_string()),
            status,
            config_path,
            detail,
            latency_ms: probe.elapsed_ms,
        })
    }

    fn sync(
        &self,
        context: &ProbeContext,
        registration: &RuntimeRegistration,
        kind: InventoryKind,
    ) -> Option<Result<Inventory>> {
        match kind {
            InventoryKind::Skills => {
                let roots = [skills::SkillRoot::new(
                    &registration.runtime_id,
                    "claude",
                    Self::skills_root(&context.home),
                )];
                Some(skills::scan(&roots, &crate::clock::now_rfc3339()).map(Inventory::Skills))
            }
            InventoryKind::Mcp => Some(Self::read_mcp(&context.home).map(Inventory::Mcp)),
            InventoryKind::Models | InventoryKind::Tools => None,
        }
    }
}

/// Skills root, exposed for tests.
pub fn skills_root(home: &Path) -> PathBuf {
    ClaudeAdapter::skills_root(home)
}
