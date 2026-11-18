//! Catalog commands: discovery, sync and the explicit resource actions.
//!
//! The service is opened lazily on the first command so window creation never
//! waits for a scan, and the first request performs the startup scan. Every
//! command is `async` because scanning spawns processes (bounded by the
//! adapter timeouts) and must not block the UI thread.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, MutexGuard};

use anvil_catalog::model::{AdapterFailure, CatalogSnapshot};
use anvil_catalog::runtime::adapter::{McpProbe, ModelVerification};
use anvil_catalog::{CatalogService, Store};
use tauri::{AppHandle, Manager, State};

/// Catalog state shared by the commands.
pub struct CatalogState {
    database: PathBuf,
    home: PathBuf,
    service: Mutex<Option<CatalogService>>,
    scanned: AtomicBool,
    init_error: Mutex<Option<String>>,
}

impl CatalogState {
    /// Build the state; the database is opened on first use.
    pub fn new(database: PathBuf, home: PathBuf) -> Self {
        Self {
            database,
            home,
            service: Mutex::new(None),
            scanned: AtomicBool::new(false),
            init_error: Mutex::new(None),
        }
    }

    /// Path of the catalogue database (for the log line at startup).
    pub fn database(&self) -> &std::path::Path {
        &self.database
    }

    fn lock(&self) -> Result<MutexGuard<'_, Option<CatalogService>>, String> {
        self.service
            .lock()
            .map_err(|_| "catalog: state lock poisoned".to_string())
    }

    fn with_service<T>(
        &self,
        action: impl FnOnce(&mut CatalogService) -> Result<T, String>,
    ) -> Result<T, String> {
        if let Some(error) = self.init_error.lock().ok().and_then(|error| error.clone()) {
            return Err(error);
        }
        let mut guard = self.lock()?;
        if guard.is_none() {
            let store = match Store::open(&self.database) {
                Ok(store) => store,
                Err(error) => {
                    let message = format!("catalog: open {}: {}", self.database.display(), error);
                    if let Ok(mut slot) = self.init_error.lock() {
                        *slot = Some(message.clone());
                    }
                    return Err(message);
                }
            };
            let service = CatalogService::new(store, self.home.clone())
                .map_err(|error| format!("catalog: registry: {error}"))?;
            *guard = Some(service);
        }
        let service = guard.as_mut().expect("service was just created");
        action(service)
    }

    /// Scan and sync once per process, then answer.
    ///
    /// Discovery is best-effort. A runtime that fails to spawn, a gateway that
    /// times out or a transient store error must never hide the agents and
    /// models already committed to disk: the previous behaviour propagated the
    /// error out of `catalog_snapshot`, which put the whole UI into an error
    /// state that read as "nothing was remembered after a restart". Failures
    /// are attached to the snapshot instead, so the persisted rows stay on
    /// screen and the exact problem is still reported in `adapterFailures`.
    fn snapshot_after_startup(&self) -> Result<CatalogSnapshot, String> {
        self.with_service(|service| {
            if !self.scanned.swap(true, Ordering::SeqCst) {
                if let Err(error) = service.scan() {
                    let message = error.to_wire();
                    eprintln!("[catalog] startup scan failed: {message}");
                    service.record_failure(AdapterFailure {
                        runtime_id: "catalog".to_string(),
                        phase: "startup-scan".to_string(),
                        error: message,
                    });
                }
                // Only the model lists are needed to open the composer. Skills,
                // tools and MCP declarations are local files the UI re-reads
                // right after mount, so syncing them here only delayed the
                // first page: a model page used to wait for every family of
                // every runtime before it could render a single tab.
                for runtime_id in service.registry().ids() {
                    if let Err(error) = service.sync_models(&runtime_id) {
                        let message = error.to_wire();
                        eprintln!(
                            "[catalog] startup model sync failed for {runtime_id}: {message}"
                        );
                        service.record_failure(AdapterFailure {
                            runtime_id,
                            phase: "startup-sync-models".to_string(),
                            error: message,
                        });
                    }
                }
            }
            service.snapshot().map_err(|error| error.to_wire())
        })
    }
}

/// Every runtime, resource and sync state the UI renders.
#[tauri::command]
pub async fn catalog_snapshot(state: State<'_, CatalogState>) -> Result<CatalogSnapshot, String> {
    state.snapshot_after_startup()
}

/// Re-scan and re-sync everything.
#[tauri::command]
pub async fn catalog_refresh(state: State<'_, CatalogState>) -> Result<CatalogSnapshot, String> {
    let snapshot =
        state.with_service(|service| service.refresh().map_err(|error| error.to_wire()))?;
    state.scanned.store(true, Ordering::SeqCst);
    Ok(snapshot)
}

/// Re-read skills, tools and MCP declarations from local files.
///
/// This deliberately skips model discovery and model probes: the UI calls it
/// on a timer and whenever the window regains focus, so it must stay cheap and
/// cannot leave a model picker waiting on a slow gateway.
#[tauri::command]
pub async fn catalog_sync_inventory(app: AppHandle) -> Result<CatalogSnapshot, String> {
    // The UI polls this in the background. Keep the file scan off the async
    // worker so a slow disk cannot stall the window that asked for it.
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<CatalogState>();
        state.with_service(|service| {
            service.sync_inventory().map_err(|error| error.to_wire())?;
            service.snapshot().map_err(|error| error.to_wire())
        })
    })
    .await
    .map_err(|error| format!("catalog: inventory sync task ended: {error}"))?
}

/// Sync one runtime's model list and return the refreshed snapshot.
///
/// The models page is split by runtime and syncs the tab the user is looking
/// at. Discovery (spawning every runtime to read its version) is not repeated
/// here: that is what made "sync" feel slow, and the rows already exist from
/// the startup scan.
#[tauri::command]
pub async fn catalog_sync(
    state: State<'_, CatalogState>,
    runtime_id: String,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .sync_models(&runtime_id)
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Add a custom model; the credential is encrypted before it is stored.
#[tauri::command]
pub async fn catalog_add_model(
    state: State<'_, CatalogState>,
    runtime_id: String,
    model_id: String,
    display_name: String,
    protocol: Option<String>,
    endpoint: Option<String>,
    credential: Option<String>,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .add_custom_model(
                &runtime_id,
                &model_id,
                &display_name,
                protocol.as_deref(),
                endpoint.as_deref(),
                credential.as_deref(),
            )
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Verify one model with a real request.
#[tauri::command]
pub async fn catalog_verify_model(
    state: State<'_, CatalogState>,
    runtime_id: String,
    model_id: String,
) -> Result<ModelVerification, String> {
    state.with_service(|service| {
        service
            .verify_model(&runtime_id, &model_id)
            .map_err(|error| error.to_wire())
    })
}

/// Promote a verified model to the default.
#[tauri::command]
pub async fn catalog_set_default_model(
    state: State<'_, CatalogState>,
    runtime_id: String,
    model_id: String,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .set_default_model(&runtime_id, &model_id)
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Turn one model on or off for the chat picker.
///
/// Discovery reports every model an installed runtime can reach; the picker
/// offers only the ones the user turned on here.
#[tauri::command]
pub async fn catalog_set_model_enabled(
    state: State<'_, CatalogState>,
    runtime_id: String,
    model_id: String,
    enabled: bool,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .set_model_enabled(&runtime_id, &model_id, enabled)
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Save one agent: name, binding and every persona field the page edits.
#[tauri::command]
pub async fn catalog_save_agent(
    state: State<'_, CatalogState>,
    agent: anvil_catalog::model::AgentDraft,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .save_agent(&agent)
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Remove one saved agent. The derived default agent cannot be removed.
#[tauri::command]
pub async fn catalog_delete_agent(
    state: State<'_, CatalogState>,
    id: String,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service.delete_agent(&id).map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Connect to one declared MCP server and enumerate its tools.
#[tauri::command]
pub async fn catalog_probe_mcp(
    state: State<'_, CatalogState>,
    runtime_id: String,
    name: String,
) -> Result<McpProbe, String> {
    state.with_service(|service| {
        service
            .probe_mcp(&runtime_id, &name)
            .map_err(|error| error.to_wire())
    })
}

/// What a one-click runtime install did, plus the catalog right after it.
#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RuntimeInstallResult {
    report: crate::runtimes::InstallReport,
    snapshot: CatalogSnapshot,
}

/// Install one runtime CLI (an npm package) into the shared prefix, then rescan.
///
/// The npm run happens outside the catalog lock: a cold download can take
/// minutes and must not freeze every other catalog command behind it. The rescan
/// afterwards is what turns "installed on disk" into "available in the picker" —
/// discovery reads the same prefix the installer writes to.
#[tauri::command]
pub async fn runtime_install(
    state: State<'_, CatalogState>,
    runtime_id: String,
) -> Result<RuntimeInstallResult, String> {
    let target = runtime_id.clone();
    let report = tauri::async_runtime::spawn_blocking(move || crate::runtimes::install(&target))
        .await
        .map_err(|error| format!("install task: {error}"))??;

    let snapshot = state.with_service(|service| {
        if report.ok {
            if let Err(error) = service.scan() {
                let message = error.to_wire();
                eprintln!("[catalog] rescan after install failed: {message}");
                service.record_failure(AdapterFailure {
                    runtime_id: "catalog".to_string(),
                    phase: "post-install-scan".to_string(),
                    error: message,
                });
            }
            if let Err(error) = service.sync_models(&runtime_id) {
                eprintln!(
                    "[catalog] post-install model sync failed: {}",
                    error.to_wire()
                );
            }
        }
        service.snapshot().map_err(|error| error.to_wire())
    })?;

    Ok(RuntimeInstallResult { report, snapshot })
}
