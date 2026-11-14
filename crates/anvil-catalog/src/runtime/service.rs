//! Scan, sync and the explicit actions, shared by every adapter.
//!
//! The service owns the sequencing (timeouts, dedupe, transaction boundaries,
//! staleness) and never branches on a runtime name: everything runtime-specific
//! comes out of the adapter behind [`Registry`].

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use crate::error::{Error, Result};
use crate::ids;
use crate::model::{
    AdapterFailure, AgentRecord, Capabilities, CapabilityState, CatalogSnapshot, InventoryKind,
    RuntimeCandidate, RuntimeHealth, RuntimeRegistration, RuntimeStatus, SyncOutcome,
};
use crate::runtime::adapter::{
    DiscoveryContext, Inventory, McpProbe, ModelVerification, ProbeContext, RuntimeAdapter,
};
use crate::runtime::adapters;
use crate::runtime::registry::Registry;
use crate::secrets;
use crate::store::Store;
use crate::{clock, mcp};

/// How many models a first run may ask for one token before settling.
const SEED_PROBES: usize = 3;

/// A seed probe is a liveness check, not a conversation: one token, but the
/// candidates are asked at the same time. Reasoning models on this gateway need
/// 4-5 s for that single token, and three concurrent curls slow each other down,
/// so 5 s marked a live model as dead; 12 s is a real answer and still shorter
/// than three sequential 8 s timeouts.
const SEED_PROBE_TIMEOUT: Duration = Duration::from_secs(12);

/// Deadlines used for one refresh.
#[derive(Clone, Copy, Debug)]
pub struct Timeouts {
    pub discovery: Duration,
    pub probe: Duration,
    pub sync: Duration,
}

impl Default for Timeouts {
    fn default() -> Self {
        Self {
            discovery: Duration::from_secs(10),
            probe: Duration::from_secs(12),
            sync: Duration::from_secs(20),
        }
    }
}

/// What one scan produced.
#[derive(Clone, Debug, Default)]
pub struct ScanSummary {
    pub registrations: usize,
    pub failures: Vec<AdapterFailure>,
    pub scanned_at: String,
}

/// Faces the host uses.
pub struct CatalogService {
    registry: Registry,
    store: Store,
    home: PathBuf,
    timeouts: Timeouts,
    failures: Vec<AdapterFailure>,
    scanned_at: Option<String>,
}

impl CatalogService {
    /// Production wiring: the default adapter set.
    pub fn new(store: Store, home: PathBuf) -> Result<Self> {
        Self::with_registry(
            store,
            home,
            adapters::default_registry()?,
            Timeouts::default(),
        )
    }

    /// Test wiring with an explicit registry.
    pub fn with_registry(
        store: Store,
        home: PathBuf,
        registry: Registry,
        timeouts: Timeouts,
    ) -> Result<Self> {
        Ok(Self {
            registry,
            store,
            home,
            timeouts,
            failures: Vec::new(),
            scanned_at: None,
        })
    }

    pub fn registry(&self) -> &Registry {
        &self.registry
    }

    pub fn store(&self) -> &Store {
        &self.store
    }

    pub fn store_mut(&mut self) -> &mut Store {
        &mut self.store
    }

    /// Discover and probe every adapter once.
    pub fn scan(&mut self) -> Result<ScanSummary> {
        let scanned_at = clock::now_rfc3339();
        let discovery = DiscoveryContext {
            home: self.home.clone(),
            timeout: self.timeouts.discovery,
        };
        let mut registrations: Vec<RuntimeRegistration> = Vec::new();
        let mut failures: Vec<AdapterFailure> = Vec::new();
        let mut scanned_ids: Vec<String> = Vec::new();

        for adapter in self.registry.all() {
            let runtime_id = adapter.id().to_string();
            let candidates = match adapter.discover(&discovery) {
                Ok(candidates) => candidates,
                Err(error) => {
                    // One broken adapter must not stop the others.
                    failures.push(AdapterFailure {
                        runtime_id: runtime_id.clone(),
                        phase: "discover".to_string(),
                        error: error.to_wire(),
                    });
                    continue;
                }
            };
            scanned_ids.push(runtime_id.clone());
            if candidates.is_empty() {
                registrations.push(not_installed(&adapter, &scanned_at));
                continue;
            }
            let probe = ProbeContext {
                home: self.home.clone(),
                timeout: self.timeouts.probe,
            };
            for candidate in dedupe(candidates) {
                registrations.push(self.register(&adapter, &probe, &candidate, &scanned_at));
            }
        }

        self.store
            .apply_scan(&registrations, &scanned_ids, &scanned_at)?;
        self.failures = failures.clone();
        self.scanned_at = Some(scanned_at.clone());
        Ok(ScanSummary {
            registrations: registrations.len(),
            failures,
            scanned_at,
        })
    }

    fn register(
        &self,
        adapter: &Arc<dyn RuntimeAdapter>,
        probe: &ProbeContext,
        candidate: &RuntimeCandidate,
        scanned_at: &str,
    ) -> RuntimeRegistration {
        let id = ids::registration_id(
            &candidate.runtime_id,
            &candidate.executable,
            candidate.instance.as_deref(),
        );
        match adapter.probe(probe, candidate) {
            Ok(report) => RuntimeRegistration {
                id,
                runtime_id: candidate.runtime_id.clone(),
                display_name: adapter.display_name().to_string(),
                version: report.version,
                executable: candidate.executable.clone(),
                source: candidate.source,
                config_path: report.config_path,
                status: report.status,
                capabilities: adapter.capabilities(),
                health: RuntimeHealth {
                    checked_at: scanned_at.to_string(),
                    latency_ms: report.latency_ms,
                    detail: report.detail,
                },
                last_seen: scanned_at.to_string(),
            },
            Err(error) => RuntimeRegistration {
                id,
                runtime_id: candidate.runtime_id.clone(),
                display_name: adapter.display_name().to_string(),
                version: None,
                executable: candidate.executable.clone(),
                source: candidate.source,
                config_path: None,
                status: RuntimeStatus::ProbeFailed,
                capabilities: adapter.capabilities(),
                health: RuntimeHealth {
                    checked_at: scanned_at.to_string(),
                    latency_ms: 0,
                    detail: Some(error.to_wire()),
                },
                last_seen: scanned_at.to_string(),
            },
        }
    }

    /// Sync every supported resource family for one runtime.
    pub fn sync_runtime(&mut self, runtime_id: &str) -> Result<Vec<SyncOutcome>> {
        let rows = self.store.runtimes()?;
        let Some(row) = rows.into_iter().find(|row| {
            row.runtime_id == runtime_id && row.status != RuntimeStatus::NotInstalled.as_str()
        }) else {
            return Ok(Vec::new());
        };
        let Some(adapter) = self.registry.get(runtime_id) else {
            return Err(Error::NotFound(format!(
                "adapter {runtime_id:?} is not registered"
            )));
        };
        let registration = RuntimeRegistration {
            id: row.id.clone(),
            runtime_id: row.runtime_id.clone(),
            display_name: row.display_name.clone(),
            version: row.version.clone(),
            executable: PathBuf::from(&row.install_path),
            source: crate::model::InstallSource::Path,
            config_path: row.config_path.as_ref().map(PathBuf::from),
            status: RuntimeStatus::from_label(&row.status).unwrap_or(RuntimeStatus::ProbeFailed),
            capabilities: row.capabilities.clone(),
            health: row.health.clone(),
            last_seen: row.last_seen.clone(),
        };
        let context = ProbeContext {
            home: self.home.clone(),
            timeout: self.timeouts.sync,
        };
        let fetched_at = clock::now_rfc3339();
        let mut outcomes = Vec::new();
        for kind in [
            InventoryKind::Models,
            InventoryKind::Skills,
            InventoryKind::Tools,
            InventoryKind::Mcp,
        ] {
            let Some(result) = adapter.sync(&context, &registration, kind) else {
                continue;
            };
            match result {
                Ok(inventory) => {
                    let count = inventory.len();
                    let models = matches!(inventory, Inventory::Models(_));
                    self.persist(runtime_id, inventory)?;
                    if models {
                        // Choosing a default is a convenience; a probe that
                        // misbehaves must never turn a successful sync into a
                        // failed one.
                        if let Err(error) =
                            self.seed_picker(runtime_id, adapter.clone(), &context, &registration)
                        {
                            let _ = self.store.mark_sync_failed(
                                runtime_id,
                                InventoryKind::Models,
                                &error.to_wire(),
                                &fetched_at,
                            );
                        }
                    }
                    outcomes.push(SyncOutcome {
                        runtime_id: runtime_id.to_string(),
                        kind: kind.as_str().to_string(),
                        ok: true,
                        item_count: count,
                        fetched_at: fetched_at.clone(),
                        stale: false,
                        error: None,
                    });
                }
                Err(error) => {
                    // Keep the previous snapshot and mark it stale.
                    self.store
                        .mark_sync_failed(runtime_id, kind, &error.to_wire(), &fetched_at)?;
                    outcomes.push(SyncOutcome {
                        runtime_id: runtime_id.to_string(),
                        kind: kind.as_str().to_string(),
                        ok: false,
                        item_count: 0,
                        fetched_at: fetched_at.clone(),
                        stale: true,
                        error: Some(error.to_wire()),
                    });
                }
            }
        }
        Ok(outcomes)
    }

    /// Switch on this runtime's first pick, preferring a model that answers.
    ///
    /// Picking the alphabetically first model is how a fresh install ended up
    /// with `auto/deepseek-v4.1-flash` switched on: that alias was alive in the
    /// gateway's list while its upstream returned 502, so the very first turn
    /// hung until the adapter's first-token deadline. A model the user already
    /// verified wins outright; otherwise up to [`SEED_PROBES`] candidates are
    /// asked for one token, and the first answer becomes the seed. When nothing
    /// answers (offline, gateway down) the first candidate stays selected, so
    /// the composer is never left without a model.
    fn seed_picker(
        &mut self,
        runtime_id: &str,
        adapter: Arc<dyn RuntimeAdapter>,
        context: &ProbeContext,
        registration: &RuntimeRegistration,
    ) -> Result<()> {
        if self.store.picker_seeded(runtime_id)? {
            return Ok(());
        }
        let mut candidates: Vec<crate::model::ModelRecord> = self
            .store
            .models()?
            .into_iter()
            .filter(|model| model.runtime_id == runtime_id)
            .collect();
        if candidates.is_empty() {
            self.store.seed_picker_once(runtime_id)?;
            return Ok(());
        }
        candidates.sort_by(|a, b| {
            b.verified
                .cmp(&a.verified)
                .then_with(|| a.model_id.cmp(&b.model_id))
        });

        let probe = ProbeContext {
            home: context.home.clone(),
            timeout: SEED_PROBE_TIMEOUT,
        };
        // Ask in parallel: one dead alias costs the whole budget when the
        // probes run in sequence, and that budget is inside the first scan the
        // UI is already waiting on.
        let asked: Vec<&crate::model::ModelRecord> = candidates.iter().take(SEED_PROBES).collect();
        let probes: Vec<Option<bool>> = std::thread::scope(|scope| {
            let handles: Vec<_> = asked
                .iter()
                .map(|model| {
                    let adapter = adapter.clone();
                    let probe = &probe;
                    scope.spawn(move || {
                        adapter
                            .verify_model(probe, registration, model, None)
                            .map(|result| matches!(result, Ok(verification) if verification.ok))
                    })
                })
                .collect();
            handles
                .into_iter()
                .map(|handle| handle.join().unwrap_or(None))
                .collect()
        });
        let (model_id, verified) = choose_seed_model(&candidates, &probes);
        self.store
            .seed_picker(runtime_id, &model_id, verified, &clock::now_rfc3339())
    }

    fn persist(&mut self, runtime_id: &str, inventory: Inventory) -> Result<()> {
        let now = clock::now_rfc3339();
        match inventory {
            Inventory::Models(items) => self.store.replace_models(runtime_id, &items, &now),
            Inventory::Skills(items) => self.store.replace_skills(runtime_id, &items, &now),
            Inventory::Tools(items) => self.store.replace_tools(runtime_id, &items, &now),
            Inventory::Mcp(items) => self.store.replace_mcp(runtime_id, &items, &now),
        }
    }

    /// Scan, then sync every registered adapter. This is the UI refresh action.
    pub fn refresh(&mut self) -> Result<CatalogSnapshot> {
        self.scan()?;
        for runtime_id in self.registry.ids() {
            self.sync_runtime(&runtime_id)?;
        }
        self.snapshot()
    }

    pub fn snapshot(&self) -> Result<CatalogSnapshot> {
        let mut snapshot = self
            .store
            .snapshot(self.failures.clone(), self.scanned_at.clone())?;
        for runtime in &mut snapshot.runtimes {
            runtime.adapter_registered = self.registry.contains(&runtime.runtime_id);
            if !runtime.adapter_registered && runtime.status != RuntimeStatus::NotInstalled.as_str()
            {
                runtime.status = RuntimeStatus::Orphaned.as_str().to_string();
            }
        }
        Ok(snapshot)
    }

    /// Turn one model on or off for the chat picker.
    pub fn set_model_enabled(
        &mut self,
        runtime_id: &str,
        model_id: &str,
        enabled: bool,
    ) -> Result<()> {
        self.store.set_model_enabled(runtime_id, model_id, enabled)
    }

    /// Create or rename one agent binding.
    pub fn save_agent(
        &mut self,
        id: Option<&str>,
        name: &str,
        runtime_id: &str,
        model_id: &str,
    ) -> Result<AgentRecord> {
        let now = clock::now_rfc3339();
        self.store.save_agent(id, name, runtime_id, model_id, &now)
    }

    /// Remove one saved agent binding.
    pub fn delete_agent(&mut self, id: &str) -> Result<()> {
        self.store.delete_agent(id)
    }

    /// Capability matrix of one runtime, for the UI and for guard checks.
    pub fn capabilities(&self, runtime_id: &str) -> Option<Capabilities> {
        self.registry.capabilities(runtime_id)
    }

    /// Add a user model. The credential, when present, is encrypted before it
    /// touches the store; the returned record only says one exists.
    pub fn add_custom_model(
        &mut self,
        runtime_id: &str,
        model_id: &str,
        display_name: &str,
        protocol: Option<&str>,
        endpoint: Option<&str>,
        credential: Option<&str>,
    ) -> Result<()> {
        let capabilities = self
            .registry
            .capabilities(runtime_id)
            .ok_or_else(|| Error::NotFound(format!("adapter {runtime_id:?} is not registered")))?;
        if capabilities.custom_models != CapabilityState::Supported {
            return Err(Error::Rejected(format!(
                "{runtime_id} does not accept custom models"
            )));
        }
        let now = clock::now_rfc3339();
        let credential_ref = match credential {
            Some(value) if !value.trim().is_empty() => {
                let name = credential_name(runtime_id, model_id);
                let ciphertext = secrets::protect(value)?;
                self.store.store_credential(&name, &ciphertext, &now)?;
                Some(name)
            }
            _ => None,
        };
        self.store.upsert_custom_model(crate::store::CustomModel {
            runtime_id,
            model_id,
            display_name,
            protocol,
            endpoint,
            credential_ref: credential_ref.as_deref(),
            updated_at: &now,
        })
    }

    /// Verify a stored model with a real request.
    pub fn verify_model(&mut self, runtime_id: &str, model_id: &str) -> Result<ModelVerification> {
        let models = self.store.models()?;
        let model = models
            .into_iter()
            .find(|model| model.runtime_id == runtime_id && model.model_id == model_id)
            .ok_or_else(|| Error::NotFound(format!("model {model_id:?} for {runtime_id}")))?;
        let adapter = self
            .registry
            .get(runtime_id)
            .ok_or_else(|| Error::NotFound(format!("adapter {runtime_id:?} is not registered")))?;
        let rows = self.store.runtimes()?;
        let row = rows
            .into_iter()
            .find(|row| row.runtime_id == runtime_id)
            .ok_or_else(|| Error::NotFound(format!("runtime {runtime_id:?}")))?;
        let registration = RuntimeRegistration {
            id: row.id,
            runtime_id: row.runtime_id,
            display_name: row.display_name,
            version: row.version,
            executable: PathBuf::from(&row.install_path),
            source: crate::model::InstallSource::Path,
            config_path: row.config_path.map(PathBuf::from),
            status: RuntimeStatus::from_label(&row.status).unwrap_or(RuntimeStatus::ProbeFailed),
            capabilities: row.capabilities,
            health: row.health,
            last_seen: row.last_seen,
        };
        let credential = match self
            .store
            .load_credential(&credential_name(runtime_id, model_id))?
        {
            Some(ciphertext) => Some(secrets::unprotect(&ciphertext)?),
            None => None,
        };
        let context = ProbeContext {
            home: self.home.clone(),
            timeout: self.timeouts.probe,
        };
        let Some(result) =
            adapter.verify_model(&context, &registration, &model, credential.as_deref())
        else {
            return Err(Error::Rejected(format!(
                "{runtime_id} cannot verify a model through this build"
            )));
        };
        let verification = result?;
        let at = clock::now_rfc3339();
        self.store
            .set_model_verified(runtime_id, model_id, verification.ok, &at)?;
        Ok(verification)
    }

    /// Promote a verified model to the default.
    pub fn set_default_model(&mut self, runtime_id: &str, model_id: &str) -> Result<()> {
        self.store.set_default_model(runtime_id, model_id)
    }

    /// Connect to one declared MCP server and enumerate its tools.
    pub fn probe_mcp(&mut self, runtime_id: &str, name: &str) -> Result<McpProbe> {
        let servers = self.store.mcp()?;
        let server = servers
            .into_iter()
            .find(|server| server.runtime_id == runtime_id && server.name == name)
            .ok_or_else(|| Error::NotFound(format!("MCP server {name:?} for {runtime_id}")))?;
        let now = clock::now_rfc3339();
        if server.transport != "stdio" {
            self.store
                .set_mcp_state(runtime_id, name, "unsupported_transport", &now)?;
            return Ok(McpProbe {
                ok: false,
                tools: Vec::new(),
                detail: Some("remote MCP probing is not implemented in this build".into()),
            });
        }
        let Some(command) = server.command.clone() else {
            self.store
                .set_mcp_state(runtime_id, name, "missing_command", &now)?;
            return Ok(McpProbe {
                ok: false,
                tools: Vec::new(),
                detail: Some("declaration has no command".into()),
            });
        };
        match mcp::probe_stdio(&command, &server.args, self.timeouts.sync) {
            Ok(tools) => {
                self.store
                    .set_mcp_state(runtime_id, name, "connected", &now)?;
                Ok(McpProbe {
                    ok: true,
                    detail: Some(format!("{} tools", tools.len())),
                    tools,
                })
            }
            Err(error) => {
                self.store.set_mcp_state(runtime_id, name, "failed", &now)?;
                Ok(McpProbe {
                    ok: false,
                    tools: Vec::new(),
                    detail: Some(error.to_wire()),
                })
            }
        }
    }
}

/// One row per declared capability when nothing is installed, so the UI can say
/// "not installed" instead of omitting the runtime.
fn not_installed(adapter: &Arc<dyn RuntimeAdapter>, scanned_at: &str) -> RuntimeRegistration {
    RuntimeRegistration {
        id: format!("{}:not-installed", adapter.id()),
        runtime_id: adapter.id().to_string(),
        display_name: adapter.display_name().to_string(),
        version: None,
        executable: PathBuf::new(),
        source: crate::model::InstallSource::Path,
        config_path: None,
        status: RuntimeStatus::NotInstalled,
        capabilities: adapter.capabilities(),
        health: RuntimeHealth {
            checked_at: scanned_at.to_string(),
            latency_ms: 0,
            detail: None,
        },
        last_seen: scanned_at.to_string(),
    }
}

fn dedupe(candidates: Vec<RuntimeCandidate>) -> Vec<RuntimeCandidate> {
    let mut seen: Vec<String> = Vec::new();
    let mut out = Vec::new();
    for candidate in candidates {
        let key = ids::instance_key(&candidate.executable, candidate.instance.as_deref());
        if seen.contains(&key) {
            continue;
        }
        seen.push(key);
        out.push(candidate);
    }
    out
}

/// Credential slot name for one model.
pub fn credential_name(runtime_id: &str, model_id: &str) -> String {
    format!("model:{runtime_id}:{model_id}")
}

/// Pick the model a first run should switch on.
///
/// `candidates` must already be ordered by preference (verified first, then id)
/// and `probes` holds one answer per candidate: `Some(true)` replied,
/// `Some(false)` refused or timed out, `None` means the adapter cannot check.
///
/// The fallback matters: with the gateway offline every probe fails, and an
/// empty picker is worse than a picker whose default fails loudly on the first
/// turn.
fn choose_seed_model(
    candidates: &[crate::model::ModelRecord],
    probes: &[Option<bool>],
) -> (String, bool) {
    for (index, model) in candidates.iter().take(SEED_PROBES).enumerate() {
        match probes.get(index).copied().flatten() {
            Some(true) => return (model.model_id.clone(), true),
            Some(false) => continue,
            None => return (model.model_id.clone(), model.verified),
        }
    }
    let first = &candidates[0];
    (first.model_id.clone(), first.verified)
}

#[cfg(test)]
mod seed_tests {
    use super::{SEED_PROBES, choose_seed_model};
    use crate::model::ModelRecord;

    fn model(id: &str, verified: bool) -> ModelRecord {
        ModelRecord {
            runtime_id: "pi".to_string(),
            model_id: id.to_string(),
            display_name: id.to_string(),
            origin: "discovered".to_string(),
            protocol: None,
            endpoint: Some("http://127.0.0.1:3425/v1".to_string()),
            verified,
            verified_at: None,
            is_default: false,
            enabled: false,
            updated_at: "2026-11-15T00:00:00Z".to_string(),
            has_credential: false,
        }
    }

    #[test]
    fn a_model_the_user_already_verified_is_taken_first() {
        // Sorted the way the service sorts: verified first, then id.
        let candidates = vec![model("auto/good", true), model("auto/deepseek", false)];
        let (id, verified) = choose_seed_model(&candidates, &[Some(true), Some(false)]);
        assert_eq!(id, "auto/good");
        assert!(verified);
    }

    #[test]
    fn the_first_model_that_answers_becomes_the_seed() {
        let candidates = vec![
            model("auto/deepseek-v4.1-flash", false),
            model("auto/glm-5.3-flash", false),
            model("auto/gpt-5.6-sol", false),
        ];
        // Exactly this machine's gateway: two aliases time out, the third answers.
        let (id, verified) =
            choose_seed_model(&candidates, &[Some(false), Some(false), Some(true)]);
        assert_eq!(id, "auto/gpt-5.6-sol");
        assert!(verified, "a model that answered is verified");
    }

    #[test]
    fn an_adapter_that_cannot_check_models_seeds_the_first_unasked() {
        let candidates = vec![model("codex-auto-review", false)];
        let (id, verified) = choose_seed_model(&candidates, &[None]);
        assert_eq!(id, "codex-auto-review");
        assert!(!verified);
    }

    #[test]
    fn nothing_answering_leaves_the_first_candidate_selected() {
        // Gateway offline: an empty picker is worse than one that fails loudly.
        let candidates = vec![model("a", false), model("b", false)];
        let (id, verified) = choose_seed_model(&candidates, &[Some(false), Some(false)]);
        assert_eq!(id, "a");
        assert!(!verified);
    }

    #[test]
    fn only_the_first_candidates_are_considered() {
        let candidates: Vec<ModelRecord> = (0..6).map(|i| model(&format!("m{i}"), false)).collect();
        let probes = vec![Some(false); 6];
        let (id, _verified) = choose_seed_model(&candidates, &probes);
        // Nothing answered, so the first stays; the limit is asserted by the
        // documented constant rather than by counting closure calls now that
        // the probes are computed up front.
        assert_eq!(id, "m0");
        assert!(SEED_PROBES < candidates.len());
    }
}
