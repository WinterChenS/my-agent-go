//! Catalog commands: discovery, sync and the explicit resource actions.
//!
//! The service is opened lazily on the first command so window creation never
//! waits for a scan, and the first request performs the startup scan. Every
//! command is `async` because scanning spawns processes (bounded by the
//! adapter timeouts) and must not block the UI thread.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, MutexGuard};

use anvil_catalog::model::{CatalogSnapshot, SyncOutcome};
use anvil_catalog::runtime::adapter::{McpProbe, ModelVerification};
use anvil_catalog::{CatalogService, Store};
use tauri::State;

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
    fn snapshot_after_startup(&self) -> Result<CatalogSnapshot, String> {
        self.with_service(|service| {
            if !self.scanned.swap(true, Ordering::SeqCst) {
                service.scan().map_err(|error| error.to_wire())?;
                for runtime_id in service.registry().ids() {
                    service
                        .sync_runtime(&runtime_id)
                        .map_err(|error| error.to_wire())?;
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
    let snapshot = state.with_service(|service| {
        service.refresh().map_err(|error| error.to_wire())
    })?;
    state.scanned.store(true, Ordering::SeqCst);
    Ok(snapshot)
}

/// Sync one runtime's resources.
#[tauri::command]
pub async fn catalog_sync(
    state: State<'_, CatalogState>,
    runtime_id: String,
) -> Result<Vec<SyncOutcome>, String> {
    state.with_service(|service| {
        service
            .sync_runtime(&runtime_id)
            .map_err(|error| error.to_wire())
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
