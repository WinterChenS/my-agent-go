//! Pi adapter.
//!
//! Pi keeps its state under `~/.pi`: `agent/models.json` (providers and their
//! models), `agent/mcp.json`, `agent/auth.json` and a `skills/` root. The
//! CLI entry point is an npm shim in `%LOCALAPPDATA%\pi-node\current`, which is
//! also on `PATH`.

use std::path::{Path, PathBuf};

use crate::error::Result;
use crate::model::{
    Capabilities, CapabilityState, InstallSource, InventoryKind, McpRecord, ModelRecord,
    RuntimeCandidate, RuntimeRegistration, RuntimeStatus,
};
use crate::runtime::bundled::BundledPi;
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

/// Pi adapter.
///
/// Two shapes, and the difference matters (ADR 0009): a [`PiAdapter::system`]
/// discovers whatever `pi` the machine has, which is what tests and the
/// development host use; a [`PiAdapter::with_bundle`] drives the runtime that
/// ships inside the installer, and then there is exactly one install — the
/// bundled one — with its agent directory pinned to the Anvil data directory.
pub struct PiAdapter {
    bundled: Option<std::sync::Arc<BundledPi>>,
}

impl PiAdapter {
    /// Discover a pi installed on this machine (development and tests).
    #[must_use]
    pub fn system() -> Self {
        Self { bundled: None }
    }

    /// Drive the runtime that ships with the app.
    #[must_use]
    pub fn with_bundle(bundle: std::sync::Arc<BundledPi>) -> Self {
        Self {
            bundled: Some(bundle),
        }
    }

    fn bundle(&self) -> Option<&BundledPi> {
        self.bundled.as_deref()
    }

    /// Pi's agent directory for this adapter.
    ///
    /// Bundled: the Anvil data directory. System: `PI_CODING_AGENT_DIR` or
    /// `~/.pi/agent`, the same rule the providers module applies.
    fn agent_dir(&self, home: &Path) -> PathBuf {
        match self.bundle() {
            Some(bundle) => bundle.agent_dir.clone(),
            None => crate::providers::config_dir(home),
        }
    }

    fn skills_root_for(&self, home: &Path) -> PathBuf {
        match self.bundle() {
            Some(bundle) => bundle.skill_root.clone(),
            None => home.join(".pi").join("skills"),
        }
    }

    fn models_file(agent_dir: &Path) -> PathBuf {
        agent_dir.join("models.json")
    }

    fn mcp_file(agent_dir: &Path) -> PathBuf {
        agent_dir.join("mcp.json")
    }

    fn settings_file(agent_dir: &Path) -> PathBuf {
        agent_dir.join("settings.json")
    }

    /// The model pi itself would use for a new session.
    ///
    /// `settings.json` carries `defaultProvider` + `defaultModel`; that is the
    /// only statement of intent the runtime makes. Discovery reports it so the
    /// app can adopt it instead of presenting an empty, all-disabled picker.
    fn configured_default(agent_dir: &Path) -> Option<(Option<String>, String)> {
        let settings = common::read_json(&Self::settings_file(agent_dir), 1 << 20)?;
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

    /// Whether any provider carries a usable credential.
    ///
    /// Two places can hold one: a provider login recorded in `auth.json`, or an
    /// `apiKey` written next to a custom provider in `models.json`. Checking
    /// only the first reported a working setup as "未认证" and hid it from the
    /// pickers. The providers module owns both rules, so the models page and
    /// this probe cannot disagree about what "configured" means.
    fn has_provider_credential(agent_dir: &Path) -> bool {
        !crate::providers::signed_in_at(agent_dir).is_empty()
    }

    fn install_candidates() -> Vec<PathBuf> {
        let mut paths = Vec::new();
        if let Some(prefix) = common::shared_prefix() {
            paths.push(common::shared_shim(&prefix, "pi"));
        }
        paths
    }

    fn read_models(agent_dir: &Path) -> Result<Vec<ModelRecord>> {
        let now = crate::clock::now_rfc3339();
        let mut models: Vec<ModelRecord> = Vec::new();
        // Providers with a credential, from both stores pi accepts: `auth.json`
        // (a key or a sign-in) and an `apiKey` beside a custom provider. The
        // models page shows a row per provider, so a model worth offering has to
        // say whether the account behind it exists.
        let signed = crate::providers::signed_in_at(agent_dir);
        // `models.json` only holds hand-written providers, and a pi signed in to
        // a built-in one may not have the file at all. Its absence is not "no
        // models": the runtime still knows which model it starts with, and that
        // answer is handled below.
        let configured = Self::configured_default(agent_dir);
        let providers = common::read_json(&Self::models_file(agent_dir), 8 << 20)
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
                    .is_some_and(|key| !key.trim().is_empty())
                    || signed.iter().any(|id| id == provider_name);
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
                    let has_credential = provider
                        .as_deref()
                        .is_some_and(|id| signed.iter().any(|signed| signed == id));
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
                        has_credential,
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

    fn read_mcp(agent_dir: &Path) -> Result<Vec<McpRecord>> {
        let Some(text) = common::read_text(&Self::mcp_file(agent_dir), 4 << 20) else {
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
        if let Some(bundle) = self.bundle() {
            // One install: the one this build carries. A machine that also has
            // pi on PATH gets no second row — this app has no way to run it.
            return Ok(vec![RuntimeCandidate {
                runtime_id: "pi".to_string(),
                executable: bundle.cli.clone(),
                source: InstallSource::Bundled,
                instance: None,
            }]);
        }
        Ok(common::discover_installs(
            "pi",
            "pi",
            &Self::install_candidates(),
        ))
    }

    fn probe(&self, context: &ProbeContext, candidate: &RuntimeCandidate) -> Result<ProbeReport> {
        // A bundled runtime is asked through the command a session would use:
        // `node <cli>` with the agent directory pinned. Probing the bundled CLI
        // by path would try to execute a JavaScript file.
        let (probe, label) = match self.bundle() {
            Some(bundle) => {
                let mut command = bundle.command();
                command.arg("--version");
                (
                    common::version_probe_prepared(command, context.timeout)?,
                    bundle.cli.clone(),
                )
            }
            None => (
                common::version_probe(&candidate.executable, context.timeout)?,
                candidate.executable.clone(),
            ),
        };
        let agent_dir = self.agent_dir(&context.home);
        let models_file = Self::models_file(&agent_dir);
        let config_path = if models_file.is_file() {
            Some(models_file)
        } else if agent_dir.is_dir() {
            Some(agent_dir.clone())
        } else {
            None
        };
        // Two ways a pi install is authenticated: a provider login recorded in
        // `auth.json`, or an `apiKey` written next to a custom provider in
        // `models.json`. Checking only the first reported a working setup as
        // "未认证" and hid it from the pickers.
        let has_credential = Self::has_provider_credential(&agent_dir);
        let status = match probe.version {
            None => RuntimeStatus::ProbeFailed,
            Some(version) if !version.at_least(MIN_VERSION) => RuntimeStatus::VersionUnsupported,
            Some(_) if !has_credential => RuntimeStatus::Unauthenticated,
            Some(_) => RuntimeStatus::Available,
        };
        let detail = match status {
            RuntimeStatus::VersionUnsupported => Some(format!(
                "version {} is below the supported {}.{}.{}",
                probe.version.expect("version"),
                MIN_VERSION.major,
                MIN_VERSION.minor,
                MIN_VERSION.patch
            )),
            RuntimeStatus::Unauthenticated => Some(format!(
                "no credential found yet: sign in from 设置 → 模型, or add a provider key in {}",
                Self::models_file(&agent_dir).display()
            )),
            // Name the file, its exit status and what it printed. The generic
            // "would not start" hint cannot tell a missing Node from a blocked
            // directory, which is the one thing the reader has to know.
            RuntimeStatus::ProbeFailed => {
                let mut note = probe.failure_note(&label, context.timeout);
                if self.bundle().is_some() {
                    note.push_str(
                        " — the bundled runtime did not start; reinstall Anvil if this repeats",
                    );
                }
                Some(note)
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
        let agent_dir = self.agent_dir(&context.home);
        match kind {
            InventoryKind::Models => Some(Self::read_models(&agent_dir).map(Inventory::Models)),
            InventoryKind::Skills => {
                let roots = [skills::SkillRoot::new(
                    &registration.runtime_id,
                    "pi",
                    self.skills_root_for(&context.home),
                )];
                Some(skills::scan(&roots, &crate::clock::now_rfc3339()).map(Inventory::Skills))
            }
            InventoryKind::Mcp => Some(Self::read_mcp(&agent_dir).map(Inventory::Mcp)),
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

/// Skills root, exposed for tests and for the host's skills page.
///
/// The bundled runtime keeps user skills under `<data_dir>/skill`; a system
/// install keeps pi's own default (`~/.pi/skills`). One function, so "where the
/// app writes a skill" and "where the catalogue scans" cannot drift.
pub fn skills_root(home: &Path) -> PathBuf {
    PiAdapter::system().skills_root_for(home)
}

#[cfg(test)]
mod tests {
    use super::PiAdapter;
    use crate::runtime::adapter::RuntimeAdapter;
    use std::fs;
    use std::path::PathBuf;

    /// The tests run the system adapter, whose agent directory is
    /// `<home>/.pi/agent`.
    fn agent(home: &std::path::Path) -> PathBuf {
        home.join(".pi").join("agent")
    }

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

        let rows = PiAdapter::read_models(&agent(&home)).expect("read models");
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

        let rows = PiAdapter::read_models(&agent(&home)).expect("read models");
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
        let rows = PiAdapter::read_models(&agent(&home)).expect("read models");
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

        let rows = PiAdapter::read_models(&agent(&home)).expect("read models");
        assert_eq!(rows.len(), 1, "the runtime's own default has to be offered");
        assert_eq!(rows[0].model_id, "deepseek-v4-pro");
        assert!(rows[0].is_default);
        assert!(rows[0].enabled, "a default nobody can pick is not a default");
        let _ = fs::remove_dir_all(&home);
    }

    /// A shim that cannot start must say why.
    ///
    /// "探测失败" with a generic hint told the reader nothing: a missing `node`
    /// and a blocked install directory look identical. The shim's own first line
    /// is the only fact that separates them, so the probe has to keep it.
    #[test]
    #[cfg(windows)]
    fn a_shim_that_will_not_start_reports_its_own_words() {
        let home = scratch("probe-failure");
        let shim = home.join("bin").join("pi.cmd");
        fs::create_dir_all(shim.parent().expect("parent")).expect("bin");
        fs::write(&shim, "@ECHO off\r\n>&2 ECHO 'node' is not recognized\r\nEXIT /b 1\r\n")
            .expect("write shim");

        let candidate = crate::model::RuntimeCandidate {
            runtime_id: "pi".to_string(),
            executable: shim.clone(),
            source: crate::model::InstallSource::KnownDir,
            instance: None,
        };
        let report = PiAdapter::system()
            .probe(
                &crate::runtime::adapter::ProbeContext {
                    home: home.clone(),
                    timeout: std::time::Duration::from_secs(5),
                },
                &candidate,
            )
            .expect("probe runs");

        assert_eq!(report.status, crate::model::RuntimeStatus::ProbeFailed);
        let detail = report.detail.expect("a failed probe explains itself");
        assert!(detail.contains("pi.cmd"), "names the file: {detail}");
        assert!(detail.contains("exited 1"), "names the exit status: {detail}");
        assert!(
            detail.contains("'node' is not recognized"),
            "keeps the shim's own message: {detail}"
        );

        let _ = fs::remove_dir_all(&home);
    }
}
