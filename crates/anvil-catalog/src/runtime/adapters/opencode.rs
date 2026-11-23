//! OpenCode adapter.
//!
//! OpenCode reads `~/.config/opencode/opencode.json` (providers and MCP
//! declarations), keeps credentials in `~/.local/share/opencode/auth.json` and
//! supports a `skills/` root next to its config.

use std::path::{Path, PathBuf};

use crate::error::Result;
use crate::model::{
    Capabilities, CapabilityState, InventoryKind, McpRecord, ModelRecord, RuntimeCandidate,
    RuntimeRegistration, RuntimeStatus,
};
use crate::runtime::adapter::{
    DiscoveryContext, Inventory, ProbeContext, ProbeReport, RuntimeAdapter,
};
use crate::runtime::adapters::common;
use crate::runtime::version::Version;
use crate::{mcp, skills};

const MIN_VERSION: Version = Version {
    major: 1,
    minor: 0,
    patch: 0,
};

pub struct OpenCodeAdapter;

impl OpenCodeAdapter {
    fn config_dir(home: &Path) -> PathBuf {
        home.join(".config").join("opencode")
    }

    fn config_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("opencode.json")
    }

    fn auth_file(home: &Path) -> PathBuf {
        home.join(".local")
            .join("share")
            .join("opencode")
            .join("auth.json")
    }

    fn skills_root(home: &Path) -> PathBuf {
        Self::config_dir(home).join("skills")
    }

    fn install_candidates() -> Vec<PathBuf> {
        let mut paths = Vec::new();
        if let Some(prefix) = common::shared_prefix() {
            paths.push(common::shared_shim(&prefix, "opencode"));
        }
        if let Some(home) = std::env::var_os("USERPROFILE").or_else(|| std::env::var_os("HOME")) {
            paths.push(
                PathBuf::from(home)
                    .join(".opencode")
                    .join("bin")
                    .join(if cfg!(windows) {
                        "opencode.exe"
                    } else {
                        "opencode"
                    }),
            );
        }
        paths
    }

    /// Providers declared in `opencode.json`; an empty object is a real empty
    /// catalogue, not a failed sync.
    fn read_models(home: &Path) -> Result<Vec<ModelRecord>> {
        let Some(value) = common::read_json(&Self::config_file(home), 8 << 20) else {
            return Ok(Vec::new());
        };
        let now = crate::clock::now_rfc3339();
        let mut models = Vec::new();
        let Some(providers) = value.get("provider").and_then(|value| value.as_object()) else {
            return Ok(Vec::new());
        };
        for (provider_name, provider) in providers {
            let Some(items) = provider.get("models").and_then(|value| value.as_object()) else {
                continue;
            };
            for (model_id, model) in items {
                if models
                    .iter()
                    .any(|entry: &ModelRecord| entry.model_id == *model_id)
                {
                    continue;
                }
                let display_name = model
                    .get("name")
                    .and_then(|value| value.as_str())
                    .unwrap_or(model_id);
                models.push(ModelRecord {
                    enabled: false,
                    runtime_id: "opencode".to_string(),
                    model_id: model_id.clone(),
                    display_name: display_name.to_string(),
                    origin: "discovered".to_string(),
                    protocol: Some(provider_name.clone()),
                    endpoint: None,
                    verified: false,
                    verified_at: None,
                    is_default: false,
                    updated_at: now.clone(),
                    has_credential: false,
                });
            }
        }
        Ok(models)
    }

    fn read_mcp(home: &Path) -> Result<Vec<McpRecord>> {
        let Some(text) = common::read_text(&Self::config_file(home), 8 << 20) else {
            return Ok(Vec::new());
        };
        mcp::from_opencode_json("opencode", &text, "~/.config/opencode/opencode.json")
    }
}

impl RuntimeAdapter for OpenCodeAdapter {
    fn id(&self) -> &'static str {
        "opencode"
    }

    fn display_name(&self) -> &'static str {
        "OpenCode"
    }

    fn capabilities(&self) -> Capabilities {
        Capabilities {
            models: CapabilityState::Supported,
            custom_models: CapabilityState::Unsupported,
            skills: CapabilityState::Supported,
            tools: CapabilityState::Unverified,
            mcp: CapabilityState::Supported,
            sessions: CapabilityState::Supported,
        }
    }

    fn discover(&self, _context: &DiscoveryContext) -> Result<Vec<RuntimeCandidate>> {
        Ok(common::discover_installs(
            "opencode",
            "opencode",
            &Self::install_candidates(),
        ))
    }

    fn probe(&self, context: &ProbeContext, candidate: &RuntimeCandidate) -> Result<ProbeReport> {
        let (version, first_line, latency_ms) =
            common::version_probe(&candidate.executable, context.timeout)?;
        let config_file = Self::config_file(&context.home);
        let config_path = if config_file.is_file() {
            Some(config_file)
        } else if Self::config_dir(&context.home).is_dir() {
            Some(Self::config_dir(&context.home))
        } else {
            None
        };
        let status = match version {
            None => RuntimeStatus::ProbeFailed,
            Some(version) if !version.at_least(MIN_VERSION) => RuntimeStatus::VersionUnsupported,
            Some(_) if !common::has_credentials(&Self::auth_file(&context.home)) => {
                RuntimeStatus::Unauthenticated
            }
            Some(_) => RuntimeStatus::Available,
        };
        let detail = match status {
            RuntimeStatus::ProbeFailed if first_line.is_empty() => {
                Some("--version printed nothing".into())
            }
            RuntimeStatus::VersionUnsupported => Some(format!(
                "version {} is below the supported {}.{}.{}",
                version.expect("version"),
                MIN_VERSION.major,
                MIN_VERSION.minor,
                MIN_VERSION.patch
            )),
            RuntimeStatus::Unauthenticated => {
                Some("no credential in ~/.local/share/opencode/auth.json".into())
            }
            _ => None,
        };
        Ok(ProbeReport {
            version: version.map(|value| value.to_string()),
            status,
            config_path,
            detail,
            latency_ms,
        })
    }

    fn sync(
        &self,
        context: &ProbeContext,
        registration: &RuntimeRegistration,
        kind: InventoryKind,
    ) -> Option<Result<Inventory>> {
        match kind {
            InventoryKind::Models => Some(Self::read_models(&context.home).map(Inventory::Models)),
            InventoryKind::Skills => {
                let roots = [skills::SkillRoot::new(
                    &registration.runtime_id,
                    "opencode",
                    Self::skills_root(&context.home),
                )];
                Some(skills::scan(&roots, &crate::clock::now_rfc3339()).map(Inventory::Skills))
            }
            InventoryKind::Mcp => Some(Self::read_mcp(&context.home).map(Inventory::Mcp)),
            InventoryKind::Tools => None,
        }
    }
}

/// Skills root, exposed for tests.
pub fn skills_root(home: &Path) -> PathBuf {
    OpenCodeAdapter::skills_root(home)
}
