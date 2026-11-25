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
        // pi honours this variable for its whole agent directory; a user who
        // isolated their config would otherwise get an empty catalog.
        if let Some(dir) = std::env::var_os("PI_CODING_AGENT_DIR") {
            if !dir.is_empty() {
                return PathBuf::from(dir);
            }
        }
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

    fn settings_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("settings.json")
    }

    /// The model pi itself would use for a new session.
    ///
    /// `settings.json` carries `defaultProvider` + `defaultModel`; that is the
    /// only statement of intent the runtime makes. Discovery reports it so the
    /// app can adopt it instead of presenting an empty, all-disabled picker.
    fn configured_default(home: &Path) -> Option<(Option<String>, String)> {
        let settings = common::read_json(&Self::settings_file(home), 1 << 20)?;
        let model = settings
            .get("defaultModel")
            .and_then(|value| value.as_str())
            .map(str::trim)
            .filter(|value| !value.is_empty())?;
        let provider = settings
            .get("defaultProvider")
            .and_then(|value| value.as_str())
            .map(str::to_string);
        Some((provider, model.to_string()))
    }

    /// Whether any provider in `models.json` carries its own key.
    ///
    /// A custom provider is authenticated by the `apiKey` written next to it
    /// (`~/.pi/agent/models.json`), not by `auth.json` — pi never writes the
    /// latter for a hand-configured provider. Checking only `auth.json` made a
    /// working runtime read as "未认证".
    fn models_have_api_key(home: &Path) -> bool {
        let Some(value) = common::read_json(&Self::models_file(home), 8 << 20) else {
            return false;
        };
        value
            .get("providers")
            .and_then(|value| value.as_object())
            .is_some_and(|providers| {
                providers.values().any(|provider| {
                    provider
                        .get("apiKey")
                        .or_else(|| provider.get("api_key"))
                        .and_then(|value| value.as_str())
                        .is_some_and(|key| !key.trim().is_empty())
                })
            })
    }

    fn install_candidates() -> Vec<PathBuf> {
        let mut paths = Vec::new();
        if let Some(prefix) = common::shared_prefix() {
            paths.push(common::shared_shim(&prefix, "pi"));
        }
        paths
    }

    fn read_models(home: &Path) -> Result<Vec<ModelRecord>> {
        let now = crate::clock::now_rfc3339();
        let mut models: Vec<ModelRecord> = Vec::new();
        // `models.json` only holds hand-written providers, and a pi signed in to
        // a built-in one may not have the file at all. Its absence is not "no
        // models": the runtime still knows which model it starts with, and that
        // answer is handled below.
        let configured = Self::configured_default(home);
        let providers = common::read_json(&Self::models_file(home), 8 << 20)
            .and_then(|value| {
                value
                    .get("providers")
                    .and_then(|value| value.as_object())
                    .cloned()
            })
            .unwrap_or_default();
        for (provider_name, provider) in &providers {
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
                let provider_has_key = provider
                    .get("apiKey")
                    .or_else(|| provider.get("api_key"))
                    .and_then(|value| value.as_str())
                    .is_some_and(|key| !key.trim().is_empty());
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
                    has_credential: provider_has_key,
                });
            }
            let _ = provider_name;
        }
        // The runtime names the model it would start with. Marking it is what
        // lets the catalog adopt it, so a configured pi never lands the reader
        // on "未启用模型（设置 → 模型 里启用）". When `settings.json` names
        // something outside `models.json` (a built-in provider), the first
        // discovered model stands in — an empty picker helps nobody.
        if let Some((provider, default_model)) = configured {
            let exact = models
                .iter()
                .position(|model| model.model_id == default_model)
                .or_else(|| {
                    models.iter().position(|model| {
                        model.model_id.eq_ignore_ascii_case(&default_model)
                            || default_model.ends_with(&format!("/{}", model.model_id))
                    })
                });
            match exact {
                Some(index) => {
                    models[index].is_default = true;
                    models[index].enabled = true;
                }
                None => {
                    // A built-in provider: `models.json` holds only hand-written
                    // providers, so its model list is not where pi's own models
                    // live. The runtime still said which model it starts with,
                    // and that one row is what opens a working picker — without
                    // it a signed-in pi with no custom provider showed zero
                    // models and 「未启用模型」, which reads as "installed but
                    // unusable".
                    let _ = provider;
                    models.push(ModelRecord {
                        enabled: true,
                        runtime_id: "pi".to_string(),
                        model_id: default_model.clone(),
                        display_name: default_model,
                        origin: "runtime".to_string(),
                        protocol: None,
                        endpoint: None,
                        verified: false,
                        verified_at: None,
                        is_default: true,
                        updated_at: now.clone(),
                        has_credential: false,
                    });
                }
            }
        }
        if !models.iter().any(|model| model.is_default) {
            if let Some(first) = models.first_mut() {
                first.is_default = true;
                first.enabled = true;
            }
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
        // Two ways a pi install is authenticated: a provider login recorded in
        // `auth.json`, or an `apiKey` written next to a custom provider in
        // `models.json`. Checking only the first reported a working setup as
        // "未认证" and hid it from the pickers.
        let has_login = common::has_credentials(&Self::auth_file(&context.home));
        let has_provider_key = Self::models_have_api_key(&context.home);
        let status = match version {
            None => RuntimeStatus::ProbeFailed,
            Some(version) if !version.at_least(MIN_VERSION) => RuntimeStatus::VersionUnsupported,
            Some(_) if !has_login && !has_provider_key => RuntimeStatus::Unauthenticated,
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
            RuntimeStatus::Unauthenticated => Some(
                "no credential found: configure a provider key in ~/.pi/agent/models.json or run \
                 `pi login`"
                    .into(),
            ),
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

    /// A configured pi must not land the reader on "未启用模型" with a full
    /// model list underneath: the runtime's own default is what gets adopted.
    #[test]
    fn the_configured_default_model_is_marked() {
        let home = scratch("default-model");
        let models = serde_json::json!({
            "providers": {
                "magpie": {
                    "api": "openai-completions",
                    "apiKey": "sk-test",
                    "baseUrl": "http://127.0.0.1:3425/v1",
                    "models": [
                        { "id": "auto/gpt-6.1-sol" },
                        { "id": "remote-magpie/auto/deepseek-v4.1-flash" }
                    ]
                }
            }
        });
        fs::write(
            home.join(".pi/agent/models.json"),
            serde_json::to_vec(&models).expect("json"),
        )
        .expect("write models");
        fs::write(
            home.join(".pi/agent/settings.json"),
            br#"{"defaultProvider":"magpie","defaultModel":"remote-magpie/auto/deepseek-v4.1-flash"}"#,
        )
        .expect("write settings");

        let rows = PiAdapter::read_models(&home).expect("read models");
        let default = rows
            .iter()
            .find(|row| row.is_default)
            .expect("one model must be the default");
        assert_eq!(default.model_id, "remote-magpie/auto/deepseek-v4.1-flash");
        assert!(default.enabled, "the default has to be usable");
        assert!(default.has_credential, "the provider carries an apiKey");
        assert_eq!(rows.iter().filter(|row| row.is_default).count(), 1);

        let _ = fs::remove_dir_all(&home);
    }

    /// Without a configured default the first discovered model stands in —
    /// an empty picker helps nobody.
    #[test]
    fn a_runtime_without_a_configured_default_still_offers_one() {
        let home = scratch("default-fallback");
        let models = serde_json::json!({
            "providers": {
                "magpie": { "api": "openai-completions", "models": [{ "id": "auto/a" }, { "id": "auto/b" }] }
            }
        });
        fs::write(
            home.join(".pi/agent/models.json"),
            serde_json::to_vec(&models).expect("json"),
        )
        .expect("write");
        let rows = PiAdapter::read_models(&home).expect("read models");
        assert_eq!(rows.iter().filter(|row| row.is_default).count(), 1);
        assert!(rows.iter().any(|row| row.enabled));
        let _ = fs::remove_dir_all(&home);
    }

    /// A signed-in pi that never wrote a custom provider keeps its models in its
    /// own bundle, and `models.json` is either absent or empty. The model pi
    /// says it starts with is then the only row the app can name — and without
    /// it a working install showed 「未启用模型」 with nothing to pick.
    #[test]
    fn the_builtin_default_model_is_offered_when_models_json_is_empty() {
        let home = scratch("builtin-default");
        fs::write(
            home.join(".pi/agent/settings.json"),
            br#"{"defaultProvider":"deepseek","defaultModel":"deepseek-v4-pro"}"#,
        )
        .expect("write settings");

        let rows = PiAdapter::read_models(&home).expect("read models");
        assert_eq!(rows.len(), 1, "the runtime's own default has to be offered");
        assert_eq!(rows[0].model_id, "deepseek-v4-pro");
        assert!(rows[0].is_default);
        assert!(rows[0].enabled, "a default nobody can pick is not a default");
        let _ = fs::remove_dir_all(&home);
    }
}
