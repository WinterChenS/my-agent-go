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
            // Without the endpoint nothing can check whether a model answers,
            // and an unchecked catalog seeds whatever sorts first (`auto/
            // deepseek-v4.1-flash` here, whose upstream returns 502).
            let provider_endpoint = provider
                .get("baseUrl")
                .and_then(|value| value.as_str())
                .map(str::to_string);
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
                    enabled: false,
                    runtime_id: "pi".to_string(),
                    model_id: model_id.to_string(),
                    display_name: display_name.to_string(),
                    origin: "discovered".to_string(),
                    protocol,
                    endpoint: provider_endpoint.clone(),
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

#[cfg(test)]
mod tests {
    use super::PiAdapter;
    use std::fs;
    use std::path::PathBuf;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "anvil-pi-{name}-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(dir.join(".pi/agent")).expect("scratch dir");
        dir
    }

    /// The endpoint is what lets the catalog ask a model whether it answers.
    /// Discovered rows used to carry `None`, so the first-run seed could only
    /// guess by model id and picked the alias whose upstream was down.
    #[test]
    fn discovered_models_carry_the_provider_endpoint() {
        let home = scratch("endpoint");
        let models = serde_json::json!({
            "providers": {
                "magpie": {
                    "api": "openai-completions",
                    "baseUrl": "http://127.0.0.1:3425/v1",
                    "models": [
                        { "id": "auto/gpt-5.6-sol", "name": "GPT" },
                        { "id": "auto/deepseek-v4.1-flash", "name": "DeepSeek" }
                    ]
                }
            }
        });
        fs::write(
            home.join(".pi/agent/models.json"),
            serde_json::to_vec(&models).expect("json"),
        )
        .expect("write");

        let rows = PiAdapter::read_models(&home).expect("read models");
        assert_eq!(rows.len(), 2);
        for row in &rows {
            assert_eq!(
                row.endpoint.as_deref(),
                Some("http://127.0.0.1:3425/v1"),
                "{} lost its endpoint",
                row.model_id
            );
        }
        let _ = fs::remove_dir_all(&home);
    }
}
