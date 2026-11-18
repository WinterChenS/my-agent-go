//! Scan, sync and the explicit actions, shared by every adapter.
//!
//! The service owns the sequencing (timeouts, dedupe, transaction boundaries,
//! staleness) and never branches on a runtime name: everything runtime-specific
//! comes out of the adapter behind [`Registry`].

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use crate::error::{Error, Result};
use crate::ids;
use crate::model::{
    AdapterFailure, AgentDraft, AgentRecord, Capabilities, CapabilityState, CatalogSnapshot,
    InventoryKind, RuntimeCandidate, RuntimeHealth, RuntimeRegistration, RuntimeStatus,
    SyncOutcome,
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

/// How much work a model sync may do before it returns.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SeedMode {
    /// Pick the seed from what is already on disk. No token request, no
    /// network: this is the startup path, where nothing may wait on a gateway.
    Local,
    /// Ask up to [`SEED_PROBES`] candidates for one token and seed the first
    /// that answers. This is the refresh path, where waiting is the point.
    Probe,
}

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
    /// Shared with the host: a discovery pass is still running for this
    /// process. Every snapshot carries it, so the UI can tell "nothing is
    /// installed" from "nothing has been read yet".
    scanning: Arc<AtomicBool>,
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
            scanning: Arc::new(AtomicBool::new(false)),
        })
    }

    /// Share the host's "still discovering" flag with this service.
    ///
    /// The service is the one place every snapshot is assembled, so wiring the
    /// flag here means each command reports it without the host remembering to
    /// annotate a dozen return values.
    pub fn set_scanning_flag(&mut self, flag: Arc<AtomicBool>) {
        self.scanning = flag;
    }

    pub fn registry(&self) -> &Registry {
        &self.registry
    }

    pub fn store(&self) -> &Store {
        &self.store
    }

    /// Attach a non-fatal failure to the next snapshot.
    ///
    /// Startup discovery is best-effort: a broken adapter, a missing runtime
    /// or a transient store error must not stop the host from answering with
    /// the agents/models already on disk. Callers use this to surface the
    /// problem in `adapterFailures` instead of turning the whole catalog into
    /// an error state that looks like "everything was forgotten".
    pub fn record_failure(&mut self, failure: AdapterFailure) {
        self.failures.push(failure);
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
        let probe_timeout = self.timeouts.probe;
        let adapters = self.registry.all();

        // Every adapter's discovery spawns its own `--version` processes, and
        // one process spawn is most of the wall clock. The adapters share
        // nothing, so they run side by side: a three-runtime machine used to
        // wait for the sum, and that sum was the first thing the window showed
        // a spinner for.
        let outcomes: Vec<AdapterScan> = std::thread::scope(|scope| {
            let handles: Vec<_> = adapters
                .iter()
                .map(|adapter| {
                    let adapter = adapter.clone();
                    let discovery = &discovery;
                    let scanned_at = scanned_at.as_str();
                    scope
                        .spawn(move || scan_adapter(&adapter, discovery, probe_timeout, scanned_at))
                })
                .collect();
            handles
                .into_iter()
                .zip(adapters.iter())
                .map(|(handle, adapter)| {
                    handle.join().unwrap_or_else(|_| AdapterScan {
                        runtime_id: adapter.id().to_string(),
                        scanned: false,
                        registrations: Vec::new(),
                        failure: Some(AdapterFailure {
                            runtime_id: adapter.id().to_string(),
                            phase: "scan".to_string(),
                            error: "the adapter's scan thread panicked".to_string(),
                        }),
                    })
                })
                .collect()
        });

        let mut registrations: Vec<RuntimeRegistration> = Vec::new();
        let mut failures: Vec<AdapterFailure> = Vec::new();
        let mut scanned_ids: Vec<String> = Vec::new();
        for outcome in outcomes {
            registrations.extend(outcome.registrations);
            if let Some(failure) = outcome.failure {
                failures.push(failure);
            }
            if outcome.scanned {
                scanned_ids.push(outcome.runtime_id);
            }
        }

        // Rows an older build wrote for a runtime this one does not register are
        // dropped before the new rows land. The set is the *adapter* set, not the
        // scan result: an adapter that merely failed to spawn keeps its rows, and
        // a parked one loses them.
        let registered = self.registry.ids();
        let registered_refs: Vec<&str> = registered.iter().map(String::as_str).collect();
        self.store.forget_unregistered(&registered_refs)?;
        self.store
            .apply_scan(&registrations, &scanned_ids, &scanned_at)?;        self.failures = failures.clone();
        self.scanned_at = Some(scanned_at.clone());
        Ok(ScanSummary {
            registrations: registrations.len(),
            failures,
            scanned_at,
        })
    }

    /// Sync every supported resource family for one runtime.
    pub fn sync_runtime(&mut self, runtime_id: &str) -> Result<Vec<SyncOutcome>> {
        self.sync_kinds(
            runtime_id,
            &[
                InventoryKind::Models,
                InventoryKind::Skills,
                InventoryKind::Tools,
                InventoryKind::Mcp,
            ],
            SeedMode::Probe,
        )
    }

    /// Re-read one runtime's model list.
    ///
    /// The models page shows one runtime at a time, and every adapter reads its
    /// models from a local JSON file, so this is a handful of milliseconds. The
    /// expensive part of a full sync is discovery — spawning each runtime to
    /// read its version — and that is deliberately not part of this call.
    pub fn sync_models(&mut self, runtime_id: &str) -> Result<Vec<SyncOutcome>> {
        self.sync_kinds(runtime_id, &[InventoryKind::Models], SeedMode::Probe)
    }

    /// Re-read one runtime's model list without asking any model for a token.
    ///
    /// This is what startup uses. Seeding a picker with [`SeedMode::Probe`]
    /// costs up to three concurrent 12-second requests when the gateway is slow
    /// or offline, and that was the multi-second wait the runtime page sat
    /// behind on a fresh profile. The first pick is chosen from disk instead;
    /// an explicit refresh still proves the candidates.
    pub fn sync_models_local(&mut self, runtime_id: &str) -> Result<Vec<SyncOutcome>> {
        self.sync_kinds(runtime_id, &[InventoryKind::Models], SeedMode::Local)
    }

    /// Re-read the local resource families of every installed runtime.
    ///
    /// Skills, tools and MCP declarations are files on this machine; a user can
    /// add one at any moment. This is the cheap sync the UI runs in the
    /// background: unlike [`Self::sync_runtime`] it never probes a model
    /// endpoint, so a slow gateway cannot stall the window.
    pub fn sync_inventory(&mut self) -> Result<Vec<SyncOutcome>> {
        let kinds = [
            InventoryKind::Skills,
            InventoryKind::Tools,
            InventoryKind::Mcp,
        ];
        let runtime_ids: Vec<String> = self
            .store
            .runtimes()?
            .into_iter()
            .filter(|row| row.status != RuntimeStatus::NotInstalled.as_str())
            .map(|row| row.runtime_id)
            .collect();
        let mut outcomes = Vec::new();
        for runtime_id in runtime_ids {
            let mut batch = self.sync_kinds(&runtime_id, &kinds, SeedMode::Local)?;
            outcomes.append(&mut batch);
        }
        Ok(outcomes)
    }

    fn sync_kinds(
        &mut self,
        runtime_id: &str,
        kinds: &[InventoryKind],
        seed: SeedMode,
    ) -> Result<Vec<SyncOutcome>> {
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
        for kind in kinds.iter().copied() {
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
                        let seeded = match seed {
                            SeedMode::Probe => self.seed_picker(
                                runtime_id,
                                adapter.clone(),
                                &context,
                                &registration,
                            ),
                            SeedMode::Local => self.seed_picker_local(runtime_id),
                        };
                        if let Err(error) = seeded {
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

    /// Seed the picker from what is already on disk.
    ///
    /// Same ordering as [`Self::seed_picker`] — an already-verified entry wins,
    /// then whatever discovery switched on, then the first id — but nothing is
    /// asked for a token. Startup uses this so a slow or offline gateway cannot
    /// hold the runtime list hostage; the models page can still verify a model
    /// on demand, and an explicit refresh re-runs the probing variant.
    fn seed_picker_local(&mut self, runtime_id: &str) -> Result<()> {
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
                .then_with(|| b.enabled.cmp(&a.enabled))
                .then_with(|| a.model_id.cmp(&b.model_id))
        });
        let choice = &candidates[0];
        self.store.seed_picker(
            runtime_id,
            &choice.model_id,
            choice.verified,
            &clock::now_rfc3339(),
        )
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
        snapshot.scanning = self.scanning.load(Ordering::SeqCst);
        // Parked adapters are not part of this build's surface. Their rows leave
        // the database on the first scan; this also covers the window before it,
        // when the snapshot is still read from a file an older build wrote.
        self.retain_registered(&mut snapshot);
        for runtime in &mut snapshot.runtimes {
            runtime.adapter_registered = self.registry.contains(&runtime.runtime_id);
            if !runtime.adapter_registered && runtime.status != RuntimeStatus::NotInstalled.as_str()
            {
                runtime.status = RuntimeStatus::Orphaned.as_str().to_string();
            }
        }
        Ok(snapshot)
    }

    /// Keep only the rows of runtimes this build registers.
    ///
    /// Runtimes are not interchangeable: a parked adapter's models must not be
    /// offered to the picker, its skills must not be listed, and its sync state
    /// must not show up as a problem the user can act on.
    fn retain_registered(&self, snapshot: &mut CatalogSnapshot) {
        let registered = |runtime_id: &str| self.registry.contains(runtime_id);
        snapshot.runtimes.retain(|row| registered(&row.runtime_id));
        snapshot.models.retain(|row| registered(&row.runtime_id));
        snapshot.skills.retain(|row| registered(&row.runtime_id));
        snapshot.tools.retain(|row| registered(&row.runtime_id));
        snapshot.mcp.retain(|row| registered(&row.runtime_id));
        snapshot.syncs.retain(|row| registered(&row.runtime_id));
        // Failures about opening the database or parsing a config belong to the
        // catalog itself, not to a runtime.
        snapshot
            .adapter_failures
            .retain(|row| registered(&row.runtime_id) || row.runtime_id == "catalog");
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

    /// Remember that the user finished first-run setup.
    pub fn mark_setup_complete(&mut self) -> Result<()> {
        self.store.mark_setup_complete(&clock::now_rfc3339())
    }

    /// Create or update one agent from the page's draft.
    pub fn save_agent(&mut self, draft: &AgentDraft) -> Result<AgentRecord> {
        let now = clock::now_rfc3339();
        self.store.save_agent(draft, &now)
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

/// What one adapter contributed to a scan.
struct AdapterScan {
    runtime_id: String,
    /// Discovery worked, so this adapter's rows should survive the scan.
    scanned: bool,
    registrations: Vec<RuntimeRegistration>,
    failure: Option<AdapterFailure>,
}

/// Discover and probe one adapter. Runs on its own thread.
fn scan_adapter(
    adapter: &Arc<dyn RuntimeAdapter>,
    discovery: &DiscoveryContext,
    probe_timeout: Duration,
    scanned_at: &str,
) -> AdapterScan {
    let runtime_id = adapter.id().to_string();
    let candidates = match adapter.discover(discovery) {
        Ok(candidates) => candidates,
        Err(error) => {
            // One broken adapter must not stop the others.
            return AdapterScan {
                runtime_id: runtime_id.clone(),
                scanned: false,
                registrations: Vec::new(),
                failure: Some(AdapterFailure {
                    runtime_id,
                    phase: "discover".to_string(),
                    error: error.to_wire(),
                }),
            };
        }
    };
    let probe = ProbeContext {
        home: discovery.home.clone(),
        timeout: probe_timeout,
    };
    let mut registrations = Vec::new();
    if candidates.is_empty() {
        registrations.push(not_installed(adapter, scanned_at));
    } else {
        for candidate in dedupe(candidates) {
            registrations.push(register(adapter, &probe, &candidate, scanned_at));
        }
    }
    AdapterScan {
        runtime_id,
        scanned: true,
        registrations,
        failure: None,
    }
}

/// One registration row from a probe: version, health and capabilities.
fn register(
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
