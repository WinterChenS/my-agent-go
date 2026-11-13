//! Pi adapter.
//!
//! Pi keeps its state under `~/.pi`: `agent/models.json` (providers and their
//! models), `agent/mcp.json`, `agent/auth.json` and a `skills/` root. The
//! CLI entry point is an npm shim in `%LOCALAPPDATA%\pi-node\current`, which is
//! also on `PATH`.

use std::path::{Path, PathBuf};

use crate::error::Result;
use crate::model::{
    Capabilities, CapabilityState, InventoryKind, McpRecord, ModelRecord, RuntimeCandidate,
    RuntimeRegistration, RuntimeStatus,
};
use crate::runtime::adapter::{
    DiscoveryContext, Inventory, ModelVerification, ProbeContext, ProbeReport, RuntimeAdapter,
};
use crate::runtime::adapters::common;
use crate::runtime::verify;
use crate::runtime::version::Version;
use crate::{mcp, skills};

/// Minimum Pi version this build supports.
const MIN_VERSION: Version = Version {
    major: 1,
    minor: 0,
    patch: 0,
};

pub struct PiAdapter;

impl PiAdapter {
    fn config_dir(home: &Path) -> PathBuf {
        home.join(".pi").join("agent")
    }

    fn skills_root(home: &Path) -> PathBuf {
        home.join(".pi").join("skills")
    }

    fn models_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("models.json")
    }

    fn mcp_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("mcp.json")
    }

    fn auth_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("auth.json")
    }

    fn install_candidates() -> Vec<PathBuf> {
        let mut paths = Vec::new();
        if let Some(local) = std::env::var_os("LOCALAPPDATA") {
            paths.push(
                PathBuf::from(local)
                    .join("pi-node")
                    .join("current")
                    .join(if cfg!(windows) { "pi.cmd" } else { "pi" }),
            );
        }
        paths
    }

    fn read_models(home: &Path) -> Result<Vec<ModelRecord>> {
        let Some(value) = common::read_json(&Self::models_file(home), 8 << 20) else {
            return Ok(Vec::new());
        };
        let now = crate::clock::now_rfc3339();
        let mut models: Vec<ModelRecord> = Vec::new();
        let Some(providers) = value.get("providers").and_then(|value| value.as_object()) else {
            return Ok(Vec::new());
        };
        for (provider_name, provider) in providers {
            let provider_api = provider.get("api").and_then(|value| value.as_str());
            let Some(items) = provider.get("models").and_then(|value| value.as_array()) else {
                continue;
            };
            for item in items {
                let Some(model_id) = item.get("id").and_then(|value| value.as_str()) else {
                    continue;
                };
                if models.iter().any(|model| model.model_id == model_id) {
                    continue;
                }
                let display_name = item
                    .get("name")
                    .and_then(|value| value.as_str())
                    .unwrap_or(model_id);
                let protocol = item
                    .get("api")
                    .and_then(|value| value.as_str())
                    .or(provider_api)
                    .map(str::to_string);
                models.push(ModelRecord {
                    runtime_id: "pi".to_string(),
                    model_id: model_id.to_string(),
                    display_name: display_name.to_string(),
                    origin: "discovered".to_string(),
                    protocol,
                    endpoint: None,
                    verified: false,
                    verified_at: None,
                    is_default: false,
                    updated_at: now.clone(),
                    has_credential: false,
                });
            }
            let _ = provider_name;
        }
        Ok(models)
    }

    fn read_mcp(home: &Path) -> Result<Vec<McpRecord>> {
        let Some(text) = common::read_text(&Self::mcp_file(home), 4 << 20) else {
            return Ok(Vec::new());
        };
        mcp::from_pi_json("pi", &text, "~/.pi/agent/mcp.json")
    }
}

impl RuntimeAdapter for PiAdapter {
    fn id(&self) -> &'static str {
        "pi"
    }

    fn display_name(&self) -> &'static str {
        "Pi Agent"
    }

    fn capabilities(&self) -> Capabilities {
        Capabilities {
            models: CapabilityState::Supported,
            custom_models: CapabilityState::Supported,
            skills: CapabilityState::Supported,
            tools: CapabilityState::Unverified,
            mcp: CapabilityState::Supported,
            sessions: CapabilityState::Supported,
        }
    }

    fn discover(&self, _context: &DiscoveryContext) -> Result<Vec<RuntimeCandidate>> {
        Ok(common::discover_installs(
            "pi",
            "pi",
            &Self::install_candidates(),
        ))
    }

    fn probe(&self, context: &ProbeContext, candidate: &RuntimeCandidate) -> Result<ProbeReport> {
        let (version, first_line, latency_ms) =
            common::version_probe(&candidate.executable, context.timeout)?;
        let config_dir = Self::config_dir(&context.home);
        let config_path = if Self::models_file(&context.home).is_file() {
            Some(Self::models_file(&context.home))
        } else if config_dir.is_dir() {
            Some(config_dir)
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
            RuntimeStatus::VersionUnsupported => Some(format!(
                "version {} is below the supported {}.{}.{}",
                version.expect("version"),
                MIN_VERSION.major,
                MIN_VERSION.minor,
                MIN_VERSION.patch
            )),
            RuntimeStatus::Unauthenticated => Some("no credential in ~/.pi/agent/auth.json".into()),
            RuntimeStatus::ProbeFailed if first_line.is_empty() => {
                Some("--version printed nothing".into())
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
                    "pi",
                    Self::skills_root(&context.home),
                )];
                Some(skills::scan(&roots, &crate::clock::now_rfc3339()).map(Inventory::Skills))
            }
            InventoryKind::Mcp => Some(Self::read_mcp(&context.home).map(Inventory::Mcp)),
            InventoryKind::Tools => None,
        }
    }

    fn verify_model(
        &self,
        context: &ProbeContext,
        _registration: &RuntimeRegistration,
        model: &ModelRecord,
        credential: Option<&str>,
    ) -> Option<Result<ModelVerification>> {
        let endpoint = model.endpoint.clone()?;
        Some(verify::openai_chat(
            &endpoint,
            &model.model_id,
            credential,
            context.timeout,
        ))
    }
}

/// Skills root, exposed for tests.
pub fn skills_root(home: &Path) -> PathBuf {
    PiAdapter::skills_root(home)
}
