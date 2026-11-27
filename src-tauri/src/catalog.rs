//! Catalog commands: discovery, sync and the explicit resource actions.
//!
//! The service is opened lazily on the first command so window creation never
//! waits for a scan, and the first `catalog_snapshot` answers **from disk** and
//! starts discovery behind it. That split is what keeps 设置 → 运行时 (and the
//! runtime step of first-run setup) instant: a scan spawns every installed
//! runtime to read its version, and a seed probe can wait on a gateway for
//! twelve seconds — neither belongs on a call the UI is blocking on. When the
//! background pass commits its rows the host emits [`CATALOG_SCANNED_EVENT`]
//! and the pages re-read the snapshot.
//!
//! Every command is `async` because scanning spawns processes (bounded by the
//! adapter timeouts) and must not block the UI thread.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};

use anvil_catalog::model::{AdapterFailure, CatalogSnapshot};
use anvil_catalog::runtime::adapter::{McpProbe, ModelVerification};
use anvil_catalog::runtime::adapters;
use anvil_catalog::runtime::bundled::BundledPi;
use anvil_catalog::runtime::registry::Registry;
use anvil_catalog::{CatalogService, Store};
use tauri::{AppHandle, Emitter, Manager, State};

use crate::state::HostState;
use crate::subagents::{SubagentOverview, sync_agents};

/// Emitted once the background discovery pass has committed its rows.
pub const CATALOG_SCANNED_EVENT: &str = "anvil://catalog-scanned";

/// One npm install line, delivered while the install is still running.
pub const RUNTIME_INSTALL_LOG_EVENT: &str = "anvil://runtime-install-log";

/// Catalog state shared by the commands.
pub struct CatalogState {
    database: PathBuf,
    home: PathBuf,
    /// The Pi this install carries (ADR 0009). Set during desktop startup,
    /// before the first command, and read when the service is first created.
    pi: Mutex<Option<Arc<BundledPi>>>,
    /// Why that runtime cannot be used. Either slot being filled means this
    /// process ships its own pi: the registry then never looks at `PATH`.
    pi_error: Mutex<Option<String>>,
    service: Mutex<Option<CatalogService>>,
    /// Discovery already ran, or was claimed by a background thread.
    scanned: AtomicBool,
    /// The background discovery thread is still working. Handed to the service
    /// so every snapshot it builds carries the flag.
    scanning: Arc<AtomicBool>,
    /// Rows as of the last read, served while discovery owns the service.
    last: Mutex<Option<CatalogSnapshot>>,
    init_error: Mutex<Option<String>>,
}

impl CatalogState {
    /// Build the state; the database is opened on first use.
    pub fn new(database: PathBuf, home: PathBuf) -> Self {
        Self {
            database,
            home,
            pi: Mutex::new(None),
            pi_error: Mutex::new(None),
            service: Mutex::new(None),
            scanned: AtomicBool::new(false),
            scanning: Arc::new(AtomicBool::new(false)),
            last: Mutex::new(None),
            init_error: Mutex::new(None),
        }
    }

    /// Path of the catalogue database (for the log line at startup).
    pub fn database(&self) -> &std::path::Path {
        &self.database
    }

    /// Record what startup found: the runtime this install carries, or the
    /// reason it cannot be used.
    ///
    /// Both outcomes are recorded, because both mean "this build ships its own
    /// pi". Only [`CatalogState::new`] without a call to this keeps the
    /// development behaviour of discovering the machine's runtime.
    /// The registry is built on first use, so this has to run before any page
    /// asks for a snapshot — desktop startup does it inside `setup`.
    pub fn set_pi(&self, result: &Result<Arc<BundledPi>, String>) {
        match result {
            Ok(bundle) => {
                if let Ok(mut slot) = self.pi.lock() {
                    *slot = Some(Arc::clone(bundle));
                }
            }
            Err(error) => {
                if let Ok(mut slot) = self.pi_error.lock() {
                    *slot = Some(error.clone());
                }
            }
        }
    }

    /// The bundled runtime, when startup loaded one.
    pub fn bundled_pi(&self) -> Option<Arc<BundledPi>> {
        self.pi.lock().ok().and_then(|slot| slot.clone())
    }

    /// Why the bundled runtime is unusable, when startup tried and failed.
    pub fn pi_failure(&self) -> Option<String> {
        self.pi_error.lock().ok().and_then(|slot| slot.clone())
    }

    /// Registry for this process.
    ///
    /// A loaded bundle is the only pi that may run; a failed load offers none (it
    /// must not become an invitation to use the machine's copy); and a host that
    /// never tried — development, tests — keeps discovering, which is the only
    /// case where an external `pi` is still reachable.
    fn registry(&self) -> Result<Registry, String> {
        adapters::registry_for_pi(self.bundled_pi(), self.pi_failure().as_deref())
            .map_err(|error| format!("catalog: registry: {error}"))
    }

    /// The home directory every adapter reads its runtime config from.
    pub fn home(&self) -> &std::path::Path {
        &self.home
    }

    fn lock(&self) -> Result<MutexGuard<'_, Option<CatalogService>>, String> {
        self.service
            .lock()
            .map_err(|_| "catalog: state lock poisoned".to_string())
    }

    pub(crate) fn with_service<T>(
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
            let mut service = CatalogService::with_registry(
                store,
                self.home.clone(),
                self.registry()?,
                anvil_catalog::runtime::service::Timeouts::default(),
            )
            .map_err(|error| format!("catalog: registry: {error}"))?;
            service.set_scanning_flag(Arc::clone(&self.scanning));
            *guard = Some(service);
        }
        let service = guard.as_mut().expect("service was just created");
        action(service)
    }

    /// Stamp a snapshot with the live "still discovering" flag.
    ///
    /// Applied on the way out *and* on the way out of the cache: the flag moves
    /// while a pass runs, and a cached snapshot that keeps its old value would
    /// tell the UI discovery is finished when it is not.
    fn annotate(&self, mut snapshot: CatalogSnapshot) -> CatalogSnapshot {
        snapshot.scanning = self.scanning.load(Ordering::SeqCst);
        snapshot
    }

    /// The snapshot as it stands right now.
    ///
    /// This is a database read (a few hundred microseconds), not a probe: the
    /// rows are the ones the host last committed.
    ///
    /// While discovery owns the service it holds its lock, so a second page
    /// opening at that moment gets the rows from the previous read instead of
    /// queueing behind a three-second process probe. Those rows are identical —
    /// only discovery can change them, and it has not committed yet.
    fn snapshot_now(&self) -> Result<CatalogSnapshot, String> {
        if self.scanning.load(Ordering::SeqCst) {
            if let Some(cached) = self.last.lock().ok().and_then(|slot| slot.clone()) {
                return Ok(self.annotate(cached));
            }
        }
        let snapshot =
            self.with_service(|service| service.snapshot().map_err(|error| error.to_wire()))?;
        let snapshot = self.annotate(snapshot);
        if let Ok(mut slot) = self.last.lock() {
            *slot = Some(snapshot.clone());
        }
        Ok(snapshot)
    }

    /// Start the one discovery pass this process gets.
    ///
    /// Discovery is best-effort. A runtime that fails to spawn, a gateway that
    /// times out or a transient store error must never hide the agents and
    /// models already committed to disk: failures are attached to the snapshot,
    /// so the persisted rows stay on screen and the exact problem is still
    /// reported in `adapterFailures`.
    ///
    /// Runs on its own thread rather than a runtime worker: it spawns processes
    /// with multi-second deadlines, and the window must stay responsive while
    /// it does.
    fn discover_in_background(&self, app: &AppHandle) -> bool {
        // Whoever gets here first owns the pass. A second window opening at the
        // same moment rides on `scanning` instead of starting a parallel probe.
        if self.scanned.swap(true, Ordering::SeqCst) {
            return false;
        }
        self.scanning.store(true, Ordering::SeqCst);
        // The cached rows are still the right answer, but they are now "from
        // before the pass" — a reader landing mid-scan must not be told the
        // catalog is settled.
        if let Ok(mut slot) = self.last.lock() {
            if let Some(snapshot) = slot.as_mut() {
                snapshot.scanning = true;
            }
        }
        let app = app.clone();
        std::thread::spawn(move || {
            let ran = {
                let state = app.state::<CatalogState>();
                let ran =
                    std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| state.discover()));
                // Cleared even when the pass panicked: a stuck `scanning` flag
                // would leave an empty profile on the skeleton forever.
                state.scanning.store(false, Ordering::SeqCst);
                // The next read must come from the database, not from the rows
                // this pass has just replaced.
                if let Ok(mut slot) = state.last.lock() {
                    *slot = None;
                }
                ran
            };
            if ran.is_err() {
                eprintln!("[catalog] discovery panicked; rows on screen were kept");
            }
            // Record why a found runtime is unusable in the host log as well as
            // on screen. A report that says only "探测失败" costs a round trip;
            // the log line carries the path, the exit status and the shim's own
            // error, which is what actually names the cause.
            let state = app.state::<CatalogState>();
            let host = app.state::<HostState>();
            #[cfg(windows)]
            for note in state.repair_launchers() {
                host.config.log.info(&format!("catalog: repaired launcher — {note}"));
            }
            if let Ok(snapshot) = state.snapshot_now() {
                for runtime in &snapshot.runtimes {
                    if runtime.status == "available" || runtime.install_path.is_empty() {
                        continue;
                    }
                    host.config.log.info(&format!(
                        "catalog: {} is {} at {}{}",
                        runtime.runtime_id,
                        runtime.status,
                        runtime.install_path,
                        runtime
                            .health
                            .detail
                            .as_deref()
                            .map(|detail| format!(" — {detail}"))
                            .unwrap_or_default()
                    ));
                }
            }
            // The rows changed under a page that is already on screen.
            let _ = app.emit(CATALOG_SCANNED_EVENT, ());
        });
        true
    }

    /// Scan and read model lists, without asking any model for a token.
    fn discover(&self) {
        let outcome = self.with_service(|service| {
            if let Err(error) = service.scan() {
                let message = error.to_wire();
                eprintln!("[catalog] startup scan failed: {message}");
                service.record_failure(AdapterFailure {
                    runtime_id: "catalog".to_string(),
                    phase: "startup-scan".to_string(),
                    error: message,
                });
            }
            // Only the model lists are needed to open the composer, and only
            // from disk: skills, tools and MCP declarations are local files the
            // UI re-reads right after mount, and seeding a picker by probing a
            // model is the request that used to hold the runtime page for ten
            // seconds on a fresh profile.
            for runtime_id in service.registry().ids() {
                if let Err(error) = service.sync_models_local(&runtime_id) {
                    let message = error.to_wire();
                    eprintln!("[catalog] startup model sync failed for {runtime_id}: {message}");
                    service.record_failure(AdapterFailure {
                        runtime_id,
                        phase: "startup-sync-models".to_string(),
                        error: message,
                    });
                }
            }
            Ok(())
        });
        if let Err(error) = outcome {
            eprintln!("[catalog] discovery could not open the catalog: {error}");
        }
    }

    /// Rewrite launchers this app wrote that the scan found unusable.
    ///
    /// A launcher pinned to a Node that has since moved keeps the runtime
    /// "found but would not start" for as long as nobody rewrites the file, and
    /// that is the shape of failure a user cannot fix by themselves. Returns one
    /// note per launcher repaired, for the log; the caller re-reads the snapshot
    /// afterwards so a repaired runtime is not reported as broken.
    #[cfg(windows)]
    fn repair_launchers(&self) -> Vec<String> {
        // A bundled runtime has no launcher to repair: `node <cli>` is built
        // from the manifest, and rewriting a file inside the install directory
        // would fight the installer (ADR 0009). A damaged bundle has nothing to
        // repair either — there is no launcher, and the machine's own shims are
        // not ours to touch.
        if self.bundled_pi().is_some() || self.pi_failure().is_some() {
            return Vec::new();
        }
        let broken: Vec<(String, String)> = match self.snapshot_now() {
            Ok(snapshot) => snapshot
                .runtimes
                .iter()
                .filter(|runtime| {
                    runtime.status == "probe_failed" && !runtime.install_path.is_empty()
                })
                .map(|runtime| (runtime.runtime_id.clone(), runtime.install_path.clone()))
                .collect(),
            Err(_) => return Vec::new(),
        };
        let mut notes = Vec::new();
        for (runtime_id, install_path) in broken {
            match crate::runtimes::repair_launcher(&runtime_id, &install_path) {
                Ok(Some(note)) => {
                    // The launcher changed, so the status that was just written is
                    // already out of date: probe again before the UI reads the row.
                    let _ = self.with_service(|service| {
                        service
                            .probe_runtime(&runtime_id)
                            .map_err(|error| error.to_wire())
                    });
                    notes.push(note);
                }
                Ok(None) => {}
                Err(reason) => notes.push(format!(
                    "{runtime_id} 的启动器无法自动修复（{install_path}）：{reason}"
                )),
            }
        }
        notes
    }
}

/// Every runtime, resource and sync state the UI renders.
///
/// Answers immediately from disk and starts discovery in the background; the
/// returned snapshot says `scanning` while that pass is running.
#[tauri::command]
pub async fn catalog_snapshot(app: AppHandle) -> Result<CatalogSnapshot, String> {
    let snapshot = {
        let state = app.state::<CatalogState>();
        // Read first, then start the pass: the other order lets the scan take
        // the service lock before this read, and the first paint of a fresh
        // profile would wait for the very probe this is meant to skip.
        let mut snapshot = state.snapshot_now()?;
        if state.discover_in_background(&app) {
            // Say "still reading" on the way out. Without this the caller sees
            // an empty list with `scanning: false` and renders "没有可用的
            // 运行时" while discovery is only just starting.
            snapshot = state.annotate(snapshot);
        }
        snapshot
    };
    Ok(snapshot)
}

/// Record that the user finished first-run setup.
///
/// Written to the data directory so the wizard stays closed after a WebView
/// storage reset, instead of depending on a localStorage flag (or on "some
/// model is default", which discovery alone can also produce).
#[tauri::command]
pub async fn catalog_mark_setup_done(state: State<'_, CatalogState>) -> Result<(), String> {
    state.with_service(|service| {
        service
            .mark_setup_complete()
            .map_err(|error| error.to_wire())
    })
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
/// at. A *user-initiated* sync also re-probes that one runtime: the status is
/// what the reader is trying to fix when they press this — a runtime installed
/// while the app was open, or `pi login` run in another window, leaves the row
/// saying "未认证" until something re-reads it. One process spawn for the
/// runtime on screen is the right price; the startup path keeps the file-only
/// sync so nothing waits on a probe.
#[tauri::command]
pub async fn catalog_sync(
    state: State<'_, CatalogState>,
    runtime_id: String,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .probe_runtime(&runtime_id)
            .map_err(|error| error.to_wire())?;
        service
            .sync_models(&runtime_id)
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Add a custom model; the credential is encrypted before it is stored.
///
/// A credential changes the runtime's own status — it is the difference between
/// "未认证" and usable — so the runtime is re-probed in the same call. The reader
/// just typed a key; making them press refresh to see it take effect is the gap
/// that kept "I added a key and nothing changed" alive.
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
        if credential.is_some_and(|value| !value.trim().is_empty()) {
            // Best effort: a probe that fails must not undo a model that was
            // stored successfully — the row is still there, and the status is
            // corrected by the next scan.
            let _ = service.probe_runtime(&runtime_id);
        }
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
    app: AppHandle,
    state: State<'_, CatalogState>,
    agent: anvil_catalog::model::AgentDraft,
) -> Result<CatalogSnapshot, String> {
    let snapshot = state.with_service(|service| {
        service
            .save_agent(&agent)
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })?;
    // A sub agent is only real once pi can read it, so the row and the file are
    // written in the same call: a saved agent that the runtime has never heard of
    // is exactly the state this page used to be stuck in.
    log_sync(&app, &snapshot);
    Ok(snapshot)
}

/// Remove one saved agent. The derived default agent cannot be removed.
#[tauri::command]
pub async fn catalog_delete_agent(
    app: AppHandle,
    state: State<'_, CatalogState>,
    id: String,
) -> Result<CatalogSnapshot, String> {
    let snapshot = state.with_service(|service| {
        service.delete_agent(&id).map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })?;
    // Deleting the row removes the file this app wrote for it, and nothing else.
    log_sync(&app, &snapshot);
    Ok(snapshot)
}

/// Write every sub-agent row to pi's agent directory and log what happened.
fn log_sync(app: &AppHandle, snapshot: &CatalogSnapshot) {
    let report = sync_agents(&snapshot.agents);
    if report.is_noop() {
        return;
    }
    let dir = crate::subagents::agents_dir();
    let state = app.state::<HostState>();
    state
        .config
        .log
        .info(&crate::subagents::summary(dir, &report));
}

/// Which sub agents pi can actually be asked for right now.
///
/// Read-only on purpose: the page draws per-row state from this, and a page load
/// must not rewrite files. The snapshot save is what writes them.
#[tauri::command]
pub async fn subagent_overview(state: State<'_, CatalogState>) -> Result<SubagentOverview, String> {
    let agents = state.with_service(|service| {
        service
            .snapshot()
            .map(|snapshot| snapshot.agents)
            .map_err(|error| error.to_wire())
    })?;
    Ok(crate::subagents::overview(&agents))
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

/// Save one MCP declaration into the chosen runtime's config and rescan it.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
pub async fn catalog_add_mcp(
    state: State<'_, CatalogState>,
    runtime_id: String,
    name: String,
    transport: String,
    command: Option<String>,
    args: Vec<String>,
    url: Option<String>,
    env: Vec<(String, String)>,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .add_mcp_server(
                &runtime_id,
                &name,
                &transport,
                command.as_deref(),
                &args,
                url.as_deref(),
                &env,
            )
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Delete a declared MCP server from the runtime's own config, then rescan.
#[tauri::command]
pub async fn catalog_remove_mcp(
    state: State<'_, CatalogState>,
    runtime_id: String,
    name: String,
) -> Result<CatalogSnapshot, String> {
    state.with_service(|service| {
        service
            .remove_mcp_server(&runtime_id, &name)
            .map_err(|error| error.to_wire())?;
        service.snapshot().map_err(|error| error.to_wire())
    })
}

/// Try a declaration the reader typed, without saving it.
#[tauri::command]
pub async fn catalog_test_mcp(
    state: State<'_, CatalogState>,
    transport: String,
    command: Option<String>,
    args: Vec<String>,
    url: Option<String>,
) -> Result<McpProbe, String> {
    state.with_service(|service| {
        service
            .test_mcp_server(&transport, command.as_deref(), &args, url.as_deref())
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

/// Ask npm for the latest version of a runtime's package.
///
/// Read-only: nothing is installed here, and the UI only offers the update
/// button when the registry really has something newer.
///
/// Pi is the exception, and the reason is ADR 0009: it arrives with the Anvil
/// installer, so its "latest version" is the newest Anvil release. Asking npm
/// about `@earendil-works/pi-coding-agent` would name a version this build can
/// never install over the one it ships.
#[tauri::command]
pub async fn runtime_update_check(
    app: AppHandle,
    state: State<'_, CatalogState>,
    runtime_id: String,
) -> Result<crate::runtimes::UpdateCheck, String> {
    let installed = match state.bundled_pi() {
        Some(bundle) => Some(bundle.pi_version.clone()),
        None => stored_version(&state, &runtime_id)?,
    };
    if runtime_id == "pi" {
        return pi_update_check(app, installed).await;
    }
    let target = runtime_id.clone();
    tauri::async_runtime::spawn_blocking(move || {
        crate::runtimes::check_update(&target, installed.as_deref())
    })
    .await
    .map_err(|error| format!("update check task: {error}"))?
}

/// The version the last scan committed for one runtime.
fn stored_version(state: &CatalogState, runtime_id: &str) -> Result<Option<String>, String> {
    state.with_service(|service| {
        let rows = service
            .store()
            .runtimes()
            .map_err(|error| error.to_wire())?;
        Ok(rows
            .into_iter()
            .find(|row| row.runtime_id == runtime_id)
            .and_then(|row| row.version))
    })
}

/// The Pi update is an Anvil update.
///
/// A failure to reach the release endpoint is reported as an error, not as "up to
/// date": a page that claims the newest version while the check failed is a page
/// that lies. The bundled version still travels with the answer, so a damaged
/// bundle shows "no installed version" instead of a made-up one.
async fn pi_update_check(
    app: AppHandle,
    installed: Option<String>,
) -> Result<crate::runtimes::UpdateCheck, String> {
    let report = crate::updater::app_update_check(app).await?;
    let latest = report.available.as_ref().map(|info| info.version.clone());
    Ok(crate::runtimes::bundled_update_check(
        "pi",
        installed.as_deref(),
        latest.as_deref(),
        report.available.is_some(),
    ))
}

/// Why this build refuses to install pi through npm, if it does.
///
/// `bundle` is the runtime this install carries; `failure` is why it could not
/// be loaded. Both mean the same thing for this question — pi is not something
/// the user installs — and the two messages differ only in what the reader can
/// do next. `(None, None)` is a host that never loaded an installer resource
/// (development, tests), which keeps the old discover-and-install path.
#[must_use]
pub fn bundled_pi_install_refusal(
    bundle: Option<&BundledPi>,
    failure: Option<&str>,
) -> Option<String> {
    if let Some(bundle) = bundle {
        return Some(format!(
            "Pi 已内置在 Anvil 中（版本 {}），无需安装；如果包内运行时损坏，请重新安装 Anvil。",
            bundle.pi_version
        ));
    }
    failure.map(|reason| {
        format!("包内 Pi 运行时不可用（{reason}），请重新安装 Anvil；不会通过 npm 安装 Pi。")
    })
}

/// Install one runtime CLI (an npm package) into the shared prefix, then rescan.
///
/// The npm run happens outside the catalog lock: a cold download can take
/// minutes and must not freeze every other catalog command behind it. The rescan
/// afterwards is what turns "installed on disk" into "available in the picker" —
/// discovery reads the same prefix the installer writes to.
///
/// Pi never reaches npm here (ADR 0009). It ships inside Anvil, so an old page's
/// 一键安装 means one of two things: the runtime is already there, or the copy the
/// installer dropped is damaged and only reinstalling Anvil fixes it. Either way
/// the answer is an error, and it is decided *before* anything is spawned.
#[tauri::command]
pub async fn runtime_install(
    app: AppHandle,
    state: State<'_, CatalogState>,
    runtime_id: String,
) -> Result<RuntimeInstallResult, String> {
    if runtime_id == "pi"
        && let Some(refusal) = bundled_pi_install_refusal(
            state.bundled_pi().as_deref(),
            state.pi_failure().as_deref(),
        )
    {
        return Err(refusal);
    }
    // Only a runtime this build registers is installable from here. A parked
    // adapter's CLI would download, never appear in the catalog, and turn the
    // button into a lie.
    state.with_service(|service| {
        if service.registry().contains(&runtime_id) {
            Ok(())
        } else {
            Err(format!("{runtime_id} 未接入当前版本（本版本只接入 pi）"))
        }
    })?;

    let target = runtime_id.clone();
    let report = tauri::async_runtime::spawn_blocking(move || {
        crate::runtimes::install(&target, |line| {
            let _ = app.emit(
                RUNTIME_INSTALL_LOG_EVENT,
                serde_json::json!({ "runtimeId": target, "line": line }),
            );
        })
    })
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
            if let Err(error) = service.sync_models_local(&runtime_id) {
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
