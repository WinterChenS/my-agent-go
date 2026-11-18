//! Contract tests for the runtime extension path (G1).
//!
//! These tests use an injected adapter instead of a real CLI, which is exactly
//! what the extension contract asks for: an unknown runtime id must travel the
//! same discover → register → sync → snapshot path as the shipped adapters.
//!
//! Real-binary evidence is produced separately by the R1-0x acceptance cases.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use anvil_catalog::error::{Error, Result};
use anvil_catalog::model::{
    AgentDraft, Capabilities, CapabilityState, InventoryKind, McpRecord, ModelRecord,
    RuntimeCandidate, RuntimeHealth, RuntimeRegistration, RuntimeStatus, SkillRecord,
};
use anvil_catalog::runtime::adapter::{
    DiscoveryContext, Inventory, ModelVerification, ProbeContext, ProbeReport, RuntimeAdapter,
};
use anvil_catalog::runtime::registry::Registry;
use anvil_catalog::runtime::service::{CatalogService, Timeouts};
use anvil_catalog::store::Store;
use anvil_catalog::{clock, secrets};

/// What a scripted adapter should answer for one sync kind.
#[derive(Clone)]
enum SyncAnswer {
    Items(Vec<String>),
    Fail(String),
}

/// A fully scripted adapter.
struct ScriptedAdapter {
    id: String,
    display: String,
    capabilities: Capabilities,
    discovery_paths: Mutex<Vec<PathBuf>>,
    discover_error: Option<String>,
    probe_status: RuntimeStatus,
    probe_error: Option<String>,
    sync_answers: Mutex<BTreeMap<String, SyncAnswer>>,
    verify: Option<ModelVerification>,
    sync_calls: Arc<AtomicUsize>,
    verify_calls: Arc<AtomicUsize>,
}

impl ScriptedAdapter {
    fn new(id: &str) -> Self {
        Self {
            id: id.to_string(),
            display: format!("{id} runtime"),
            capabilities: Capabilities {
                models: CapabilityState::Supported,
                custom_models: CapabilityState::Supported,
                skills: CapabilityState::Supported,
                tools: CapabilityState::Unverified,
                mcp: CapabilityState::Supported,
                sessions: CapabilityState::Supported,
            },
            discovery_paths: Mutex::new(Vec::new()),
            discover_error: None,
            probe_status: RuntimeStatus::Available,
            probe_error: None,
            sync_answers: Mutex::new(BTreeMap::new()),
            verify: Some(ModelVerification {
                ok: true,
                detail: Some("HTTP 200".into()),
            }),
            sync_calls: Arc::new(AtomicUsize::new(0)),
            verify_calls: Arc::new(AtomicUsize::new(0)),
        }
    }

    fn discover_at(self, paths: Vec<PathBuf>) -> Self {
        *self.discovery_paths.lock().expect("paths") = paths;
        self
    }

    fn with_sync(self, kind: InventoryKind, answer: SyncAnswer) -> Self {
        self.set_answer(kind, answer);
        self
    }

    fn set_answer(&self, kind: InventoryKind, answer: SyncAnswer) {
        self.sync_answers
            .lock()
            .expect("answers")
            .insert(kind.as_str().to_string(), answer);
    }
}

impl RuntimeAdapter for ScriptedAdapter {
    fn id(&self) -> &'static str {
        // The registry keeps its own copy; this is only for display parity.
        Box::leak(self.id.clone().into_boxed_str())
    }

    fn display_name(&self) -> &'static str {
        Box::leak(self.display.clone().into_boxed_str())
    }

    fn capabilities(&self) -> Capabilities {
        self.capabilities.clone()
    }

    fn discover(&self, _context: &DiscoveryContext) -> Result<Vec<RuntimeCandidate>> {
        if let Some(error) = &self.discover_error {
            return Err(Error::Io(error.clone()));
        }
        Ok(self
            .discovery_paths
            .lock()
            .expect("paths")
            .iter()
            .map(|path| RuntimeCandidate {
                runtime_id: self.id.clone(),
                executable: path.clone(),
                source: anvil_catalog::model::InstallSource::Injected,
                instance: None,
            })
            .collect())
    }

    fn probe(&self, _context: &ProbeContext, _candidate: &RuntimeCandidate) -> Result<ProbeReport> {
        if let Some(error) = &self.probe_error {
            return Err(Error::Io(error.clone()));
        }
        Ok(ProbeReport {
            version: Some("9.9.9".into()),
            status: self.probe_status,
            config_path: None,
            detail: None,
            latency_ms: 3,
        })
    }

    fn sync(
        &self,
        _context: &ProbeContext,
        _registration: &RuntimeRegistration,
        kind: InventoryKind,
    ) -> Option<Result<Inventory>> {
        self.sync_calls.fetch_add(1, Ordering::SeqCst);
        let answer = self
            .sync_answers
            .lock()
            .expect("answers")
            .get(kind.as_str())?
            .clone();
        Some(match answer {
            SyncAnswer::Items(names) => Ok(match kind {
                InventoryKind::Models => Inventory::Models(
                    names
                        .into_iter()
                        .map(|name| model(&self.id, &name))
                        .collect(),
                ),
                InventoryKind::Skills => Inventory::Skills(
                    names
                        .into_iter()
                        .map(|name| skill(&self.id, &name, "valid"))
                        .collect(),
                ),
                InventoryKind::Mcp => Inventory::Mcp(
                    names
                        .into_iter()
                        .map(|name| mcp(&self.id, &name, "stdio", Some(r"C:\Tools\demo.exe")))
                        .collect(),
                ),
                InventoryKind::Tools => Inventory::Tools(Vec::new()),
            }),
            SyncAnswer::Fail(reason) => Err(Error::Io(reason)),
        })
    }

    fn verify_model(
        &self,
        _context: &ProbeContext,
        _registration: &RuntimeRegistration,
        _model: &ModelRecord,
        _credential: Option<&str>,
    ) -> Option<Result<ModelVerification>> {
        self.verify_calls.fetch_add(1, Ordering::SeqCst);
        self.verify.clone().map(Ok)
    }
}

fn model(runtime_id: &str, id: &str) -> ModelRecord {
    ModelRecord {
        runtime_id: runtime_id.to_string(),
        model_id: id.to_string(),
        display_name: id.to_string(),
        origin: "discovered".into(),
        protocol: Some("openai".into()),
        endpoint: None,
        verified: false,
        verified_at: None,
        is_default: false,
        enabled: false,
        updated_at: clock::now_rfc3339(),
        has_credential: false,
    }
}

fn skill(runtime_id: &str, name: &str, state: &str) -> SkillRecord {
    SkillRecord {
        runtime_id: runtime_id.to_string(),
        name: name.to_string(),
        description: "scripted".into(),
        root: r"C:\Skills".into(),
        path: format!(r"C:\Skills\{name}"),
        state: state.to_string(),
        reason: None,
        updated_at: clock::now_rfc3339(),
    }
}

fn mcp(runtime_id: &str, name: &str, transport: &str, command: Option<&str>) -> McpRecord {
    McpRecord {
        runtime_id: runtime_id.to_string(),
        name: name.to_string(),
        transport: transport.to_string(),
        command: command.map(str::to_string),
        args: vec!["--serve".into()],
        url: None,
        env_keys: vec!["DEMO_TOKEN".into()],
        header_keys: Vec::new(),
        source: "scripted".into(),
        state: "declared".into(),
        probed_at: None,
    }
}

/// Temp home with unique directory and cleanup.
struct TempHome {
    path: PathBuf,
}

impl TempHome {
    fn new(label: &str) -> Self {
        let path = std::env::temp_dir().join(format!(
            "anvil-catalog-{label}-{}-{}",
            std::process::id(),
            clock::unix_now()
        ));
        std::fs::create_dir_all(&path).expect("temp home");
        Self { path }
    }

    fn path(&self) -> PathBuf {
        self.path.clone()
    }

    /// Create a file (used to give the fake adapter a real executable path).
    fn touch(&self, relative: &str) -> PathBuf {
        let path = self.path.join(relative);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).expect("parent");
        }
        std::fs::write(&path, b"fake").expect("touch");
        path
    }
}

impl Drop for TempHome {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.path);
    }
}

fn service(home: &TempHome, adapters: Vec<Arc<dyn RuntimeAdapter>>) -> CatalogService {
    let mut registry = Registry::new();
    for adapter in adapters {
        registry.register(adapter).expect("register");
    }
    let store = Store::open_in_memory().expect("store");
    CatalogService::with_registry(
        store,
        home.path(),
        registry,
        Timeouts {
            discovery: Duration::from_secs(2),
            probe: Duration::from_secs(2),
            sync: Duration::from_secs(2),
        },
    )
    .expect("service")
}

#[test]
fn scan_registers_once_per_normalised_path() {
    let home = TempHome::new("dedupe");
    let executable = home.touch("bin/demo.cmd");
    let adapter =
        ScriptedAdapter::new("demo").discover_at(vec![executable.clone(), executable.clone()]);
    let mut service = service(&home, vec![Arc::new(adapter)]);

    let summary = service.scan().expect("scan");
    assert_eq!(summary.registrations, 1);
    let snapshot = service.snapshot().expect("snapshot");
    assert_eq!(snapshot.runtimes.len(), 1);
    let runtime = &snapshot.runtimes[0];
    assert_eq!(runtime.runtime_id, "demo");
    assert_eq!(runtime.status, "available");
    assert_eq!(runtime.version.as_deref(), Some("9.9.9"));
    assert_eq!(runtime.snapshot_state, "never");
}

#[test]
fn rescan_is_idempotent_and_keeps_first_seen() {
    let home = TempHome::new("rescan");
    let executable = home.touch("bin/demo.cmd");
    let adapter = ScriptedAdapter::new("demo").discover_at(vec![executable]);
    let mut service = service(&home, vec![Arc::new(adapter)]);

    service.scan().expect("first");
    let first = service.snapshot().expect("snapshot");
    service.scan().expect("second");
    let second = service.snapshot().expect("snapshot");

    assert_eq!(first.runtimes.len(), second.runtimes.len());
    assert_eq!(first.runtimes[0].id, second.runtimes[0].id);
    assert_eq!(first.runtimes[0].first_seen, second.runtimes[0].first_seen);
}

#[test]
fn removed_binary_is_marked_not_installed() {
    let home = TempHome::new("removed");
    let executable = home.touch("bin/demo.cmd");
    let adapter = Arc::new(ScriptedAdapter::new("demo").discover_at(vec![executable]));
    let mut service = service(&home, vec![adapter.clone()]);

    service.scan().expect("first");
    assert_eq!(
        service.snapshot().expect("snapshot").runtimes[0].status,
        "available"
    );

    // The same adapter now finds nothing: the previous row must flip instead of
    // being silently deleted.
    *adapter.discovery_paths.lock().expect("paths") = Vec::new();
    service.scan().expect("second");
    let snapshot = service.snapshot().expect("snapshot");
    let statuses: Vec<&str> = snapshot
        .runtimes
        .iter()
        .map(|runtime| runtime.status.as_str())
        .collect();
    assert!(statuses.contains(&"not_installed"), "{statuses:?}");
}

#[test]
fn failing_adapter_does_not_hide_the_healthy_one() {
    let home = TempHome::new("isolation");
    let executable = home.touch("bin/good.cmd");
    let mut broken = ScriptedAdapter::new("broken");
    broken.discover_error = Some("simulated discovery failure".into());
    let healthy = ScriptedAdapter::new("healthy").discover_at(vec![executable]);
    let mut service = service(&home, vec![Arc::new(broken), Arc::new(healthy)]);

    let summary = service.scan().expect("scan");
    assert_eq!(summary.failures.len(), 1);
    assert_eq!(summary.failures[0].runtime_id, "broken");
    let snapshot = service.snapshot().expect("snapshot");
    assert!(
        snapshot
            .runtimes
            .iter()
            .any(|runtime| runtime.runtime_id == "healthy")
    );
    assert!(
        !snapshot
            .runtimes
            .iter()
            .any(|runtime| runtime.runtime_id == "broken")
    );
    assert_eq!(snapshot.adapter_failures.len(), 1);
}

#[test]
fn a_recorded_startup_failure_keeps_persisted_rows_visible() {
    // The restart path must answer with what is already on disk even when a
    // startup scan/sync failed. `record_failure` is how the host attaches that
    // problem without throwing the whole snapshot away.
    let home = TempHome::new("startup-resilience");
    let mut service = service(&home, Vec::new());

    // A first snapshot persists the derived default agent, like a real boot.
    let before = service.snapshot().expect("first snapshot");
    assert_eq!(before.agents.len(), 1);

    service.record_failure(anvil_catalog::model::AdapterFailure {
        runtime_id: "catalog".to_string(),
        phase: "startup-scan".to_string(),
        error: "simulated startup failure".to_string(),
    });

    let after = service.snapshot().expect("snapshot after failure");
    assert_eq!(after.agents.len(), 1, "persisted agent must stay visible");
    assert_eq!(after.adapter_failures.len(), 1);
    assert_eq!(after.adapter_failures[0].phase, "startup-scan");
    assert_eq!(after.adapter_failures[0].error, "simulated startup failure");
}

#[test]
fn unknown_adapter_travels_the_whole_pipeline() {
    let home = TempHome::new("unknown");
    let executable = home.touch("bin/acme.cmd");
    let adapter = ScriptedAdapter::new("acme-cli")
        .discover_at(vec![executable])
        .with_sync(
            InventoryKind::Skills,
            SyncAnswer::Items(vec!["acme-skill".into()]),
        )
        .with_sync(
            InventoryKind::Mcp,
            SyncAnswer::Items(vec!["acme-mcp".into()]),
        );
    let mut service = service(&home, vec![Arc::new(adapter)]);

    service.scan().expect("scan");
    service.sync_runtime("acme-cli").expect("sync");
    let snapshot = service.snapshot().expect("snapshot");

    let runtime = snapshot
        .runtimes
        .iter()
        .find(|runtime| runtime.runtime_id == "acme-cli")
        .expect("runtime row");
    assert!(runtime.adapter_registered);
    assert_eq!(snapshot.skills.len(), 1);
    assert_eq!(snapshot.skills[0].runtime_id, "acme-cli");
    assert_eq!(snapshot.mcp.len(), 1);
    // Capabilities are declared by the adapter, not hardcoded by runtime name.
    assert_eq!(
        runtime.capabilities.custom_models,
        CapabilityState::Supported
    );
}

#[test]
fn the_startup_seed_never_asks_a_model_for_a_token() {
    // Startup is what the runtime page waits on, so it must not depend on a
    // gateway: the first pick comes from disk and nothing is verified.
    let home = TempHome::new("local-seed");
    let executable = home.touch("bin/scripted.exe");
    let adapter = ScriptedAdapter::new("scripted")
        .discover_at(vec![executable])
        .with_sync(
            InventoryKind::Models,
            SyncAnswer::Items(vec!["b-model".into(), "a-model".into()]),
        );
    let probes = Arc::clone(&adapter.verify_calls);
    let mut service = service(&home, vec![Arc::new(adapter)]);

    service.scan().expect("scan");
    service
        .sync_models_local("scripted")
        .expect("local model sync");
    let snapshot = service.snapshot().expect("snapshot");

    let enabled: Vec<&str> = snapshot
        .models
        .iter()
        .filter(|model| model.enabled)
        .map(|model| model.model_id.as_str())
        .collect();
    // Exactly one pick, and a deterministic one: the lowest id when nothing is
    // verified yet.
    assert_eq!(enabled, vec!["a-model"]);
    assert_eq!(
        probes.load(Ordering::SeqCst),
        0,
        "the startup seed must not probe a model"
    );
}

#[test]
fn an_explicit_sync_still_proves_the_seed() {
    // The refresh path keeps the old behaviour: ask, and mark what answered.
    let home = TempHome::new("probe-seed");
    let executable = home.touch("bin/scripted.exe");
    let adapter = ScriptedAdapter::new("scripted")
        .discover_at(vec![executable])
        .with_sync(
            InventoryKind::Models,
            SyncAnswer::Items(vec!["b-model".into(), "a-model".into()]),
        );
    let probes = Arc::clone(&adapter.verify_calls);
    let mut service = service(&home, vec![Arc::new(adapter)]);

    service.scan().expect("scan");
    service.sync_models("scripted").expect("model sync");
    let snapshot = service.snapshot().expect("snapshot");

    assert!(probes.load(Ordering::SeqCst) > 0);
    let seeded = snapshot
        .models
        .iter()
        .find(|model| model.enabled)
        .expect("one seeded model");
    assert!(seeded.verified, "a model that answered is recorded as verified");
}

#[test]
fn setup_done_is_remembered_in_the_store() {
    // The wizard's "already set up" check reads this, so it has to live with
    // the catalog and not in WebView storage.
    let store = Store::open_in_memory().expect("store");
    assert!(!store.setup_complete().expect("read"));
    store
        .mark_setup_complete(&clock::now_rfc3339())
        .expect("write");
    assert!(store.setup_complete().expect("read"));
}

#[test]
fn duplicate_registration_is_rejected() {
    let home = TempHome::new("duplicate");
    let mut registry = Registry::new();
    registry
        .register(Arc::new(ScriptedAdapter::new("same")))
        .expect("first");
    let error = registry
        .register(Arc::new(ScriptedAdapter::new("same")))
        .expect_err("second");
    assert_eq!(error.code(), "rejected");
    let _ = home;
}

#[test]
fn capability_without_sync_is_not_reported_as_an_empty_result() {
    let home = TempHome::new("capability");
    let executable = home.touch("bin/limited.cmd");
    let adapter = ScriptedAdapter::new("limited").discover_at(vec![executable]);
    let mut service = service(&home, vec![Arc::new(adapter)]);

    service.scan().expect("scan");
    let outcomes = service.sync_runtime("limited").expect("sync");
    assert!(
        outcomes.is_empty(),
        "unsupported families must not be synced"
    );

    let snapshot = service.snapshot().expect("snapshot");
    assert!(snapshot.syncs.is_empty(), "no sync row may pretend success");
    let runtime = &snapshot.runtimes[0];
    assert_eq!(runtime.capabilities.tools, CapabilityState::Unverified);
}

#[test]
fn custom_model_cannot_be_duplicated_or_defaulted_before_verification() {
    let home = TempHome::new("models");
    let executable = home.touch("bin/demo.cmd");
    let adapter = ScriptedAdapter::new("demo")
        .discover_at(vec![executable])
        .with_sync(
            InventoryKind::Models,
            SyncAnswer::Items(vec!["builtin-1".into()]),
        );
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    service.sync_runtime("demo").expect("sync");

    service
        .add_custom_model(
            "demo",
            "custom-1",
            "Custom One",
            Some("openai"),
            Some("http://127.0.0.1:9/v1"),
            Some("sk-test-secret"),
        )
        .expect("add");

    let duplicate = service
        .add_custom_model(
            "demo",
            "custom-1",
            "Custom One",
            Some("openai"),
            Some("http://127.0.0.1:9/v1"),
            None,
        )
        .expect_err("duplicate");
    assert_eq!(duplicate.code(), "rejected");

    let default = service
        .set_default_model("demo", "custom-1")
        .expect_err("unverified");
    assert_eq!(default.code(), "rejected");

    // A model the adapter declares unsupported is refused outright.
    let unsupported = service
        .add_custom_model("missing-runtime", "x", "X", None, None, None)
        .expect_err("unknown adapter");
    assert_eq!(unsupported.code(), "not_found");

    let snapshot = service.snapshot().expect("snapshot");
    let custom = snapshot
        .models
        .iter()
        .find(|model| model.model_id == "custom-1")
        .expect("custom row");
    assert_eq!(custom.origin, "custom");
    assert!(custom.has_credential);
    let json = serde_json::to_string(&snapshot).expect("json");
    assert!(
        !json.contains("sk-test-secret"),
        "credential leaked into the snapshot"
    );
}

#[test]
fn verified_model_can_become_the_default_and_failure_clears_it() {
    let home = TempHome::new("verify");
    let executable = home.touch("bin/demo.cmd");
    let adapter = ScriptedAdapter::new("demo").discover_at(vec![executable]);
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");

    service
        .add_custom_model(
            "demo",
            "custom-2",
            "Custom Two",
            Some("openai"),
            Some("http://127.0.0.1:9/v1"),
            None,
        )
        .expect("add");
    let verification = service.verify_model("demo", "custom-2").expect("verify");
    assert!(verification.ok);
    service
        .set_default_model("demo", "custom-2")
        .expect("default");

    let snapshot = service.snapshot().expect("snapshot");
    let row = snapshot
        .models
        .iter()
        .find(|model| model.model_id == "custom-2")
        .expect("row");
    assert!(row.is_default);
    assert!(row.verified);
}

#[test]
fn failed_sync_keeps_the_previous_snapshot_and_marks_it_stale() {
    let mut store = Store::open_in_memory().expect("store");
    store
        .replace_mcp(
            "demo",
            &[mcp(
                "demo",
                "good-mcp",
                "stdio",
                Some(r"C://Tools//demo.exe"),
            )],
            "2026-01-01T00:00:00Z",
        )
        .expect("seed");
    store
        .mark_sync_failed(
            "demo",
            InventoryKind::Mcp,
            "config unreadable",
            "2026-01-01T00:01:00Z",
        )
        .expect("fail");

    let snapshot = store.snapshot(Vec::new(), None).expect("snapshot");
    assert_eq!(snapshot.mcp.len(), 1, "previous snapshot must survive");
    assert_eq!(snapshot.mcp[0].name, "good-mcp");
    let sync = snapshot
        .syncs
        .iter()
        .find(|sync| sync.kind == "mcp")
        .expect("sync row");
    assert!(!sync.ok);
    assert!(sync.stale);
    assert_eq!(sync.error.as_deref(), Some("config unreadable"));
}

#[test]
fn service_marks_a_failed_resync_stale_and_keeps_the_rows() {
    let home = TempHome::new("resync");
    let executable = home.touch("bin/demo.cmd");
    let adapter = Arc::new(
        ScriptedAdapter::new("demo")
            .discover_at(vec![executable])
            .with_sync(
                InventoryKind::Mcp,
                SyncAnswer::Items(vec!["kept-mcp".into()]),
            ),
    );
    let mut service = service(&home, vec![adapter.clone()]);
    service.scan().expect("scan");
    service.sync_runtime("demo").expect("first sync");
    assert_eq!(service.snapshot().expect("snapshot").mcp.len(), 1);

    adapter.set_answer(
        InventoryKind::Mcp,
        SyncAnswer::Fail("config unreadable".into()),
    );
    let outcomes = service.sync_runtime("demo").expect("second sync");
    let outcome = outcomes
        .iter()
        .find(|outcome| outcome.kind == "mcp")
        .expect("mcp outcome");
    assert!(!outcome.ok);
    assert!(outcome.stale);

    let snapshot = service.snapshot().expect("snapshot");
    assert_eq!(
        snapshot.mcp.len(),
        1,
        "the previous row must survive a failure"
    );
    assert_eq!(snapshot.mcp[0].name, "kept-mcp");
    assert_eq!(snapshot.runtimes[0].snapshot_state, "stale");
}

#[test]
fn credential_is_encrypted_at_rest() {
    let ciphertext = secrets::protect("sk-live-abcdef").expect("protect");
    let as_text = String::from_utf8_lossy(&ciphertext);
    assert!(!as_text.contains("sk-live"));
    assert_eq!(
        secrets::unprotect(&ciphertext).expect("unprotect"),
        "sk-live-abcdef"
    );
}

#[test]
fn snapshot_serialises_without_secret_material() {
    let home = TempHome::new("serialise");
    let executable = home.touch("bin/demo.cmd");
    let adapter = ScriptedAdapter::new("demo")
        .discover_at(vec![executable])
        .with_sync(
            InventoryKind::Mcp,
            SyncAnswer::Items(vec!["demo-mcp".into()]),
        );
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    service.sync_runtime("demo").expect("sync");
    let snapshot = service.snapshot().expect("snapshot");
    let json = serde_json::to_string(&snapshot).expect("json");
    assert!(json.contains("demo-mcp"));
    assert!(json.contains("DEMO_TOKEN"), "env names are kept");
    assert!(!json.to_lowercase().contains("password"));
}

#[test]
fn untrusted_mcp_command_is_refused_without_spawning() {
    let home = TempHome::new("mcp-trust");
    let executable = home.touch("bin/demo.cmd");
    let shared = Arc::new(Mutex::new(Vec::<McpRecord>::new()));
    *shared.lock().expect("shared") = vec![mcp(
        "demo",
        "evil",
        "stdio",
        Some(r"\\server\share\evil.exe"),
    )];

    let adapter = ScriptedAdapter::new("demo").discover_at(vec![executable]);
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    service
        .store_mut()
        .replace_mcp("demo", &shared.lock().expect("shared").clone(), "t0")
        .expect("seed");

    let probe = service
        .probe_mcp("demo", "evil")
        .expect("probe returns a result");
    assert!(!probe.ok);
    let detail = probe.detail.unwrap_or_default();
    assert!(detail.contains("not trusted"), "{detail}");
}

#[test]
fn probe_mcp_reports_a_missing_server() {
    let home = TempHome::new("mcp-missing");
    let executable = home.touch("bin/demo.cmd");
    let adapter = ScriptedAdapter::new("demo").discover_at(vec![executable]);
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    let error = service.probe_mcp("demo", "nope").expect_err("missing");
    assert_eq!(error.code(), "not_found");
}

#[test]
fn registration_row_carries_health_and_capabilities() {
    let home = TempHome::new("row");
    let executable = home.touch("bin/demo.cmd");
    let mut adapter = ScriptedAdapter::new("demo").discover_at(vec![executable]);
    adapter.probe_status = RuntimeStatus::Unauthenticated;
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    let snapshot = service.snapshot().expect("snapshot");
    let runtime = &snapshot.runtimes[0];
    assert_eq!(runtime.status, "unauthenticated");
    assert_eq!(runtime.install_source, "injected");
    let health: RuntimeHealth = runtime.health.clone();
    assert!(!health.checked_at.is_empty());
    assert!(runtime.capabilities.models == CapabilityState::Supported);
}

#[test]
fn probe_failure_is_recorded_not_thrown() {
    let home = TempHome::new("probe-fail");
    let executable = home.touch("bin/demo.cmd");
    let mut adapter = ScriptedAdapter::new("demo").discover_at(vec![executable]);
    adapter.probe_error = Some("handshake timed out".into());
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    let snapshot = service.snapshot().expect("snapshot");
    assert_eq!(snapshot.runtimes[0].status, "probe_failed");
    assert!(
        snapshot.runtimes[0]
            .health
            .detail
            .as_deref()
            .unwrap_or_default()
            .contains("handshake timed out")
    );
}

#[test]
fn skill_records_preserve_source_root_for_duplicates() {
    let home = TempHome::new("skill-dup");
    let executable = home.touch("bin/demo.cmd");
    let adapter = ScriptedAdapter::new("demo")
        .discover_at(vec![executable])
        .with_sync(
            InventoryKind::Skills,
            SyncAnswer::Items(vec!["shared".into()]),
        );
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    service.sync_runtime("demo").expect("sync");
    let snapshot = service.snapshot().expect("snapshot");
    assert_eq!(snapshot.skills.len(), 1);
    assert_eq!(snapshot.skills[0].root, r"C:\Skills");
    let path = Path::new(&snapshot.skills[0].path);
    assert!(path.is_absolute());
}

#[test]
fn a_first_install_retires_the_not_installed_placeholder() {
    let home = TempHome::new("install-retires-placeholder");
    let adapter = Arc::new(ScriptedAdapter::new("demo"));
    let mut service = service(&home, vec![adapter.clone()]);
    service.scan().expect("scan before install");
    let before = service.snapshot().expect("snapshot");
    assert_eq!(before.runtimes.len(), 1);
    assert_eq!(before.runtimes[0].status, "not_installed");

    // The one-click installer dropped a shim: the next scan finds a real binary.
    let executable = home.touch("bin/demo.cmd");
    *adapter.discovery_paths.lock().expect("paths") = vec![executable];
    service.scan().expect("scan after install");

    let after = service.snapshot().expect("snapshot");
    assert_eq!(
        after.runtimes.len(),
        1,
        "the not-installed placeholder must be retired, not kept beside the install"
    );
    assert_eq!(after.runtimes[0].status, "available");
    assert!(!after.runtimes[0].install_path.is_empty());
}

#[test]
fn snapshot_lists_every_declared_runtime_even_when_nothing_is_installed() {
    let home = TempHome::new("nothing");
    let adapter = ScriptedAdapter::new("demo");
    let mut service = service(&home, vec![Arc::new(adapter)]);
    service.scan().expect("scan");
    let snapshot = service.snapshot().expect("snapshot");
    assert_eq!(snapshot.runtimes.len(), 1);
    assert_eq!(snapshot.runtimes[0].status, "not_installed");
    assert!(snapshot.runtimes[0].install_path.is_empty());
}

/// Discovery reports what the machine has; the picker offers what the user
/// turned on. Without this the chat composer listed every model of every
/// installed runtime, which is a dump of the machine's config rather than a
/// choice.
#[test]
fn a_discovered_model_stays_out_of_the_picker_until_it_is_enabled() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    store
        .replace_models("pi", &[model("pi", "alpha"), model("pi", "beta")], &at)
        .expect("discover");

    let discovered = store.models().expect("models");
    assert_eq!(discovered.len(), 2);
    assert!(
        discovered.iter().all(|row| !row.enabled),
        "discovery must not turn a model on"
    );

    store
        .set_model_enabled("pi", "alpha", true)
        .expect("enable");
    let after = store.models().expect("models");
    let alpha = after
        .iter()
        .find(|row| row.model_id == "alpha")
        .expect("alpha");
    let beta = after
        .iter()
        .find(|row| row.model_id == "beta")
        .expect("beta");
    assert!(alpha.enabled);
    assert!(!beta.enabled);

    let unknown = store
        .set_model_enabled("pi", "missing", true)
        .expect_err("unknown model");
    assert_eq!(unknown.code(), "not_found");
}

fn draft(name: Option<&str>, runtime_id: &str, model_id: &str) -> AgentDraft {
    AgentDraft {
        name: name.unwrap_or("").to_string(),
        // Empty means "use the generated id", so each new agent is unique.
        slug: String::new(),
        runtime_id: runtime_id.to_string(),
        model_id: model_id.to_string(),
        thinking: "medium".to_string(),
        context_window: "400K".to_string(),
        auto_compact: true,
        network: true,
        confirm_dangerous: true,
        group_kind: "main".to_string(),
        delegation_policy: "auto".to_string(),
        concurrency: 4,
        enabled: true,
        permission: "medium".to_string(),
        ..Default::default()
    }
}

/// An agent is a saved runtime+model pair, and the built-in one is a real row:
/// the user has to be able to point it at a runtime, which a row derived from
/// the default model never allowed.
#[test]
fn an_agent_binds_a_runtime_and_a_model_and_the_default_one_is_editable() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    store
        .replace_models("pi", &[model("pi", "alpha"), model("pi", "beta")], &at)
        .expect("discover");

    store.ensure_default_agent(&at).expect("default agent");
    let built_in = store.agents().expect("agents");
    assert_eq!(built_in.len(), 1);
    assert_eq!(built_in[0].id, "default");
    assert!(built_in[0].is_default);

    let saved = store
        .save_agent(&draft(Some("  代码助手 "), "pi", "alpha"), &at)
        .expect("save");
    assert!(saved.id.starts_with("agent-"));
    assert_eq!(saved.name, "代码助手");
    assert_eq!(saved.runtime_id, "pi");
    assert_eq!(saved.model_id, "alpha");
    assert!(!saved.is_default);

    let listed = store.agents().expect("agents");
    assert_eq!(listed.len(), 2);
    assert_eq!(listed[0].id, "default", "the built-in agent leads the list");

    // Binding the built-in agent is a save like any other; it also promotes
    // that model, because a default the picker hides is one the user cannot
    // see they have.
    let bound = store
        .save_agent(
            &AgentDraft {
                id: Some("default".into()),
                ..draft(Some("Default Agent"), "pi", "beta")
            },
            &at,
        )
        .expect("bind the built-in agent");
    assert!(bound.is_default);
    assert_eq!(bound.model_id, "beta");
    let beta = store
        .models()
        .expect("models")
        .into_iter()
        .find(|row| row.model_id == "beta")
        .expect("beta");
    assert!(beta.is_default, "binding the agent promotes its model");
    assert!(beta.enabled, "a promoted model must be pickable");

    let unnamed = store
        .save_agent(&draft(Some("  "), "pi", "alpha"), &at)
        .expect_err("no name");
    assert_eq!(unnamed.code(), "rejected");

    // A binding to something the machine does not have would look saved and
    // fail at the first turn, so it is refused here.
    let unknown = store
        .save_agent(&draft(Some("Ghost"), "pi", "missing"), &at)
        .expect_err("unknown model");
    assert_eq!(unknown.code(), "rejected");

    store.delete_agent(&saved.id).expect("delete");
    let remaining = store.agents().expect("agents");
    assert_eq!(remaining.len(), 1);
    assert_eq!(remaining[0].id, "default");
    let default_removal = store.delete_agent("default").expect_err("built-in");
    assert_eq!(default_removal.code(), "rejected");
}

/// A fresh install has no default model yet; the built-in agent still has to
/// name something the machine can run, or "the system has one agent" would be
/// an empty row in the picker.
#[test]
fn the_default_agent_adopts_the_enabled_model_before_a_default_exists() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    store
        .replace_models("pi", &[model("pi", "alpha"), model("pi", "beta")], &at)
        .expect("discover");
    store.set_model_enabled("pi", "beta", true).expect("enable");

    store.ensure_default_agent(&at).expect("default agent");
    let agents = store.agents().expect("agents");
    assert_eq!(agents.len(), 1);
    assert_eq!(agents[0].runtime_id, "pi");
    assert_eq!(agents[0].model_id, "beta");
}

/// The endpoint is what a model check posts to. It used to be written as NULL
/// for every discovered row, so "verify" and the first-run seed probe both had
/// nothing to call and silently gave up.
#[test]
fn discovered_models_keep_their_endpoint() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    let mut alpha = model("pi", "alpha");
    alpha.endpoint = Some("http://127.0.0.1:3425/v1".to_string());
    store.replace_models("pi", &[alpha], &at).expect("discover");

    let row = store.models().expect("models");
    assert_eq!(row.len(), 1);
    assert_eq!(
        row[0].endpoint.as_deref(),
        Some("http://127.0.0.1:3425/v1"),
        "a discovered model lost the endpoint the probe needs"
    );

    // A rescan that reports a new endpoint must update the row in place.
    let mut moved = model("pi", "alpha");
    moved.endpoint = Some("http://127.0.0.1:9999/v1".to_string());
    store
        .replace_models("pi", &[moved], &at)
        .expect("rediscover");
    assert_eq!(
        store.models().expect("models")[0].endpoint.as_deref(),
        Some("http://127.0.0.1:9999/v1")
    );
}

/// A rescan reports the same models again. It must not take the user's
/// switches with it, and it must still drop models the runtime stopped
/// reporting.
#[test]
fn a_rescan_keeps_the_models_the_user_switched_on() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    store
        .replace_models("pi", &[model("pi", "alpha"), model("pi", "beta")], &at)
        .expect("discover");
    store.set_model_enabled("pi", "beta", true).expect("enable");

    store
        .replace_models(
            "pi",
            &[
                model("pi", "alpha"),
                model("pi", "beta"),
                model("pi", "gamma"),
            ],
            &at,
        )
        .expect("rediscover");
    let rows = store.models().expect("models");
    let beta = rows
        .iter()
        .find(|row| row.model_id == "beta")
        .expect("beta");
    let gamma = rows
        .iter()
        .find(|row| row.model_id == "gamma")
        .expect("gamma");
    assert!(beta.enabled, "a rescan disabled a model the user turned on");
    assert!(!gamma.enabled, "a new model must not switch itself on");

    store
        .replace_models("pi", &[model("pi", "alpha")], &at)
        .expect("shrink");
    let kept = store.models().expect("models");
    assert_eq!(kept.len(), 1, "models the runtime dropped must disappear");
    assert_eq!(kept[0].model_id, "alpha");
}

/// A fresh catalog must not open chat with a dead composer, and must not offer
/// every model the machine happens to have. One per runtime, then stop.
#[test]
fn the_picker_is_seeded_with_one_model_per_runtime_exactly_once() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    store
        .replace_models("pi", &[model("pi", "alpha"), model("pi", "beta")], &at)
        .expect("discover pi");
    store
        .replace_models("codex", &[model("codex", "omega")], &at)
        .expect("discover codex");

    assert!(store.seed_picker_once("pi").expect("seed pi"));
    assert!(store.seed_picker_once("codex").expect("seed codex"));
    let enabled: Vec<String> = store
        .models()
        .expect("models")
        .into_iter()
        .filter(|row| row.enabled)
        .map(|row| format!("{}/{}", row.runtime_id, row.model_id))
        .collect();
    assert_eq!(enabled, vec!["codex/omega", "pi/alpha"]);

    // A later rescan that finds more models must not silently enable them.
    store
        .replace_models(
            "pi",
            &[
                model("pi", "alpha"),
                model("pi", "beta"),
                model("pi", "gamma"),
            ],
            &at,
        )
        .expect("rediscover");
    assert!(!store.seed_picker_once("pi").expect("second seed"));
    let still_on: Vec<String> = store
        .models()
        .expect("models")
        .into_iter()
        .filter(|row| row.enabled)
        .map(|row| row.model_id)
        .collect();
    assert!(!still_on.contains(&"gamma".to_string()));
    // The model the user had switched on must survive the rescan; a blanket
    // delete used to empty the picker on every sync.
    assert!(still_on.contains(&"alpha".to_string()));
}

/// Every field the agent page edits survives a save and a re-read. A persona
/// that silently dropped its prompt would look configured and run as the
/// runtime's default coding assistant.
#[test]
fn an_agent_s_persona_round_trips_through_the_store() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    store
        .replace_models("pi", &[model("pi", "alpha")], &at)
        .expect("discover");

    let mut input = draft(Some("迁移工程师"), "pi", "alpha");
    input.slug = "migration-engineer".into();
    input.description = "负责代码库迁移与兼容性评估".into();
    input.thinking = "high".into();
    input.context_window = "1M".into();
    input.memory = true;
    input.auto_compact = false;
    input.system_prompt = "你是迁移工程师，仓库在 {{repo_path}}".into();
    input.tools = vec!["read".into(), "grep".into()];
    input.skills = vec!["openspec-verify-change".into()];
    input.permission = "high".into();
    input.writable_dirs = "docs/**".into();
    input.command_allowlist = "pnpm test".into();
    input.network = false;
    input.confirm_dangerous = false;
    input.group_kind = "sub".into();
    input.delegated_from = "default".into();
    input.delegation_policy = "explicit".into();
    input.concurrency = 8;
    input.enabled = false;

    let saved = store.save_agent(&input, &at).expect("save");
    let loaded = store.agent(&saved.id).expect("read").expect("row");
    assert_eq!(loaded.slug, "migration-engineer");
    assert_eq!(loaded.description, input.description);
    assert_eq!(loaded.thinking, "high");
    assert_eq!(loaded.context_window, "1M");
    assert!(loaded.memory);
    assert!(!loaded.auto_compact);
    assert_eq!(loaded.system_prompt, input.system_prompt);
    assert_eq!(loaded.tools, input.tools);
    assert_eq!(loaded.skills, input.skills);
    assert_eq!(loaded.permission, "high");
    assert_eq!(loaded.writable_dirs, "docs/**");
    assert_eq!(loaded.command_allowlist, "pnpm test");
    assert!(!loaded.network);
    assert!(!loaded.confirm_dangerous);
    assert_eq!(loaded.group_kind, "sub");
    assert_eq!(loaded.delegated_from, "default");
    assert_eq!(loaded.delegation_policy, "explicit");
    assert_eq!(loaded.concurrency, 8);
    assert!(!loaded.enabled);

    // Updating one field must not wipe the others: the page saves the whole
    // draft, but an older caller (or a future partial patch) must be safe.
    input.system_prompt = "你是发布检查员".into();
    input.id = Some(saved.id.clone());
    let updated = store.save_agent(&input, &at).expect("update");
    assert_eq!(updated.thinking, "high");
    assert_eq!(updated.slug, "migration-engineer");
    assert_eq!(store.agents().expect("agents").len(), 1, "no duplicate row");

    // Turning every tool off is a real state, not "fall back to the defaults".
    input.tools.clear();
    let no_tools = store.save_agent(&input, &at).expect("save with no tools");
    assert!(no_tools.tools.is_empty());
    assert!(!no_tools.allows_tools());
    assert_eq!(
        no_tools.effective_tools(),
        Vec::<String>::new(),
        "the runtime must receive --no-tools"
    );

    let readonly = AgentDraft {
        permission: "readonly".into(),
        ..input
    };
    let narrowed = store.save_agent(&readonly, &at).expect("readonly");
    assert_eq!(
        narrowed.effective_tools(),
        vec!["read", "grep", "find", "ls"]
    );
}

/// An old catalog (agents table without the persona columns) still opens, and
/// its stored agent gets the safe defaults the page would have shown.
#[test]
fn an_existing_agent_keeps_a_usable_persona_after_migration() {
    let dir = std::env::temp_dir().join(format!(
        "anvil-agent-migration-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    std::fs::create_dir_all(&dir).expect("scratch dir");
    let path = dir.join("catalog.sqlite");
    {
        // Exactly the table shape the pre-persona build wrote.
        let connection = rusqlite::Connection::open(&path).expect("old database");
        connection
            .execute_batch(
                "CREATE TABLE agents (
                    id TEXT PRIMARY KEY, name TEXT NOT NULL, runtime_id TEXT NOT NULL,
                    model_id TEXT NOT NULL, updated_at TEXT NOT NULL
                 );
                 CREATE TABLE models (
                    runtime_id TEXT NOT NULL, model_id TEXT NOT NULL,
                    display_name TEXT NOT NULL, origin TEXT NOT NULL,
                    protocol TEXT, endpoint TEXT, credential_ref TEXT,
                    verified INTEGER NOT NULL DEFAULT 0, verified_at TEXT,
                    is_default INTEGER NOT NULL DEFAULT 0,
                    updated_at TEXT NOT NULL, PRIMARY KEY (runtime_id, model_id)
                 );
                 INSERT INTO agents VALUES ('legacy', 'Legacy', 'pi', 'alpha', '2026-11-01T00:00:00Z');",
            )
            .expect("write the old shape");
    }

    let store = Store::open(&path).expect("migrate an old database");
    let legacy = store.agent("legacy").expect("read").expect("legacy row");
    assert_eq!(legacy.slug, "legacy", "a blank slug falls back to the id");
    assert_eq!(legacy.thinking, "medium");
    assert_eq!(legacy.context_window, "400K");
    assert_eq!(
        legacy.tools.len(),
        anvil_catalog::model::DEFAULT_TOOLS.len(),
        "a row saved before tools existed keeps the runtime's default set"
    );
    assert_eq!(legacy.permission, "medium");
    assert!(legacy.auto_compact);
    assert!(legacy.network);
    assert!(legacy.enabled);
    let _ = std::fs::remove_dir_all(&dir);
}

/// The background poll must never re-probe models. A slow gateway turns a
/// 1-token probe into a 12-second stall; skills and MCP declarations are local
/// files and can be re-read as often as the window likes.
#[test]
fn inventory_sync_reads_local_families_without_touching_models() {
    let home = TempHome::new("inventory-sync");
    let adapter = Arc::new(
        ScriptedAdapter::new("fake")
            .discover_at(vec![home.touch("bin/fake")])
            .with_sync(
                InventoryKind::Models,
                SyncAnswer::Items(vec!["model-a".into()]),
            )
            .with_sync(
                InventoryKind::Skills,
                SyncAnswer::Items(vec!["skill-a".into()]),
            )
            .with_sync(InventoryKind::Mcp, SyncAnswer::Items(vec!["mcp-a".into()])),
    );
    let calls = adapter.sync_calls.clone();
    let mut service = service(&home, vec![adapter]);
    service.scan().expect("scan");
    calls.store(0, Ordering::SeqCst);

    let outcomes = service.sync_inventory().expect("inventory sync");
    assert_eq!(
        calls.load(Ordering::SeqCst),
        3,
        "skills, tools and mcp were each offered to the adapter"
    );
    assert!(
        outcomes.iter().all(|outcome| outcome.kind != "models"),
        "a model probe leaked into the cheap sync: {outcomes:?}"
    );
    let snapshot = service.snapshot().expect("snapshot");
    assert!(snapshot.models.is_empty(), "models stayed untouched");
    assert_eq!(snapshot.skills.len(), 1);
    assert_eq!(snapshot.mcp.len(), 1);

    // The full rescan is the one that still offers models.
    calls.store(0, Ordering::SeqCst);
    service.sync_runtime("fake").expect("full sync");
    assert_eq!(calls.load(Ordering::SeqCst), 4);
    assert_eq!(service.snapshot().expect("snapshot").models.len(), 1);
}

/// Delegation has to be in the default tool list, and never in the read-only one.
///
/// `--tools` is a whole allowlist, so a tool left out of the list is a tool the
/// model cannot see. The default set therefore names `subagent` — pi ignores the
/// name when its extension is absent — while `readonly` must not: a child agent
/// runs its own tools, so delegation would let a read-only turn write through one.
#[test]
fn the_default_tool_list_can_delegate_and_read_only_cannot() {
    assert!(
        anvil_catalog::model::DEFAULT_TOOLS.contains(&"subagent"),
        "the default set must be able to delegate"
    );
    assert!(
        !anvil_catalog::model::READ_ONLY_TOOLS.contains(&"subagent"),
        "read only must not be able to delegate"
    );
    // Narrowing may only remove: every read-only tool is a default tool too.
    for name in anvil_catalog::model::READ_ONLY_TOOLS {
        assert!(
            anvil_catalog::model::DEFAULT_TOOLS.contains(&name),
            "{name} is missing from the default set"
        );
    }
}

/// An agent saved before delegation existed gains the tool once, and an agent
/// the user narrowed keeps its exact list.
///
/// The distinction matters because both look like "a saved list": one is the old
/// default that never mentioned delegation, the other is a decision. Widening the
/// second would silently undo a read-only setup.
#[test]
fn only_an_untouched_tool_list_gains_delegation() {
    let mut store = Store::open_in_memory().expect("store");
    let at = clock::now_rfc3339();
    store
        .replace_models("pi", &[model("pi", "alpha")], &at)
        .expect("discover");

    let legacy = AgentDraft {
        // Explicit ids: two saves in the same second would otherwise share the
        // timestamp-derived id and the second would overwrite the first.
        id: Some("legacy".to_string()),
        tools: anvil_catalog::model::BUILTIN_TOOLS
            .iter()
            .map(|name| (*name).to_string())
            .collect(),
        ..draft(Some("legacy"), "pi", "alpha")
    };
    store.save_agent(&legacy, &at).expect("save legacy");

    let narrowed = AgentDraft {
        id: Some("narrow".to_string()),
        tools: vec!["read".to_string()],
        ..draft(Some("narrow"), "pi", "alpha")
    };
    store.save_agent(&narrowed, &at).expect("save narrowed");

    store.ensure_default_agent(&at).expect("migrate");
    let legacy = store.agent("legacy").expect("read").expect("legacy row");
    assert!(
        legacy.tools.contains(&"subagent".to_string()),
        "the old default list gained delegation: {:?}",
        legacy.tools
    );
    let narrow = store.agent("narrow").expect("read").expect("narrow row");
    assert_eq!(
        narrow.tools,
        vec!["read".to_string()],
        "an edited list is a user decision"
    );
}
