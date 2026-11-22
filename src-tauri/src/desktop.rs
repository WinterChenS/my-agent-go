//! Desktop host wiring: preflight → assets → Tauri builder → window.
//!
//! The order is significant and matches the Go host: nothing Tauri-related
//! happens until `HostConfig::prepare` validated `ANVIL_DATA_DIR`,
//! `ANVIL_WEBVIEW_DATA_DIR`, the log directory and the built frontend. A failed
//! preflight returns before any window (or Tauri event loop) exists.

use tauri::Manager;

use crate::assets::DiskAssets;
use crate::catalog::CatalogState;
use crate::host::HostConfig;
use crate::inject::InitialState;
use crate::session::SessionState;
use crate::state::{HostState, create_main_window, start_system_theme_poller};
use crate::system_theme;

/// Runs the desktop host. Returns an error when preflight fails.
pub fn run() -> Result<(), Box<dyn std::error::Error>> {
    let config = HostConfig::from_env()?;
    let system_dark = system_theme::system_dark();
    config.log_startup(system_dark);
    // First launch with the per-user data dir: bring the old fixed-location data
    // across so conversations and model/agent configuration are not lost.
    //
    // Only the default per-user directory is an upgrade target. An explicit
    // ANVIL_DATA_DIR is a deliberate choice (dev run, portable install, test
    // rig) and must never inherit another install's ledger behind the user's
    // back — that made a "clean" profile open with someone else's history.
    if config.data_dir == crate::host::default_data_dir() {
        crate::host::migrate_legacy_data(&config.data_dir, &config.log);
    }

    let assets = DiskAssets::load(&config.dist_dir)
        .map_err(|error| format!("frontend assets {}: {error}", config.dist_dir.display()))?;
    let initial = InitialState::new(&config.preferences.snapshot(), system_dark);
    assets.set_injection(initial);
    let injection = assets.injection_handle();

    // `assets = ...` keeps `frontend/dist` out of the compile-time dependency
    // set (see build.rs): the bundle is served from disk like the Go host.
    let context = tauri::generate_context!("tauri.conf.json", assets = assets);
    let catalog = CatalogState::new(
        config.data_dir.join("catalog.sqlite"),
        anvil_catalog::runtime::adapters::common::home(),
    );
    config.log.info(&format!(
        "catalog: database {}",
        catalog.database().display()
    ));
    let session = SessionState::new(
        config.data_dir.join("sessions.sqlite"),
        config.data_dir.join("workspace"),
        config.data_dir.join("catalog.sqlite"),
    );
    config
        .log
        .info(&format!("session: ledger {}", session.database().display()));
    let state = HostState::new(config, system_dark, injection);

    tauri::Builder::default()
        .manage(state)
        .manage(catalog)
        .manage(session)
        .invoke_handler(tauri::generate_handler![
            crate::commands::get_preferences,
            crate::commands::set_preferences,
            crate::commands::log_event,
            crate::catalog::catalog_snapshot,
            crate::catalog::catalog_refresh,
            crate::catalog::catalog_sync,
            crate::catalog::catalog_sync_inventory,
            crate::catalog::catalog_add_model,
            crate::catalog::catalog_verify_model,
            crate::catalog::catalog_set_default_model,
            crate::catalog::catalog_set_model_enabled,
            crate::catalog::catalog_mark_setup_done,
            crate::catalog::catalog_save_agent,
            crate::catalog::catalog_delete_agent,
            crate::catalog::catalog_probe_mcp,
            crate::catalog::catalog_add_mcp,
            crate::catalog::catalog_test_mcp,
            crate::catalog::catalog_remove_mcp,
            crate::catalog::runtime_install,
            crate::catalog::runtime_update_check,
            crate::session::session_list,
            crate::session::session_detail,
            crate::session::session_events,
            crate::session::session_open,
            crate::session::session_send,
            crate::session::session_abort,
            crate::session::session_steer,
            crate::session::session_context,
            crate::session::session_rename,
            crate::session::file_preview,
            crate::browser::browser_open,
            crate::browser::browser_bounds,
            crate::browser::browser_close,
            crate::browser::browser_status,
            crate::browser::browser_action,
            crate::session::session_pin,
            crate::session::session_archive,
            crate::session::session_delete,
            crate::session::session_projects,
            crate::session::session_fork,
            crate::session::session_compact,
            crate::session::session_approve,
            crate::session::session_extension_ui,
            crate::session::session_context_list,
            crate::session::session_rail,
            crate::session::session_export,
            crate::session::session_audit,
            crate::session::session_audit_export,
            crate::workbench::workbench_overview,
            crate::session::agent_runs,
            crate::workspace::workspace_defaults,
            crate::workspace::workspace_ensure,
            crate::workspace::workspace_create,
            crate::workspace::workspace_pick,
            crate::workspace::workspace_pick_file,
            crate::workspace::workspace_pick_files,
            crate::extensions::pi_subagent_status,
            crate::extensions::pi_subagent_install,
            crate::schedules::schedule_overview,
            crate::schedules::schedule_save,
            crate::schedules::schedule_delete,
            crate::schedules::schedule_toggle,
            crate::schedules::schedule_run_now,
            crate::library::library_overview,
            crate::library::library_import,
            crate::library::library_export,
            crate::library::library_preview,
            crate::library::library_reveal,
            crate::connectors::connectors_overview,
            crate::connectors::connector_save,
            crate::connectors::connector_delete,
            crate::connectors::connector_toggle,
            crate::connectors::connector_probe,
            crate::skills::skill_create,
            crate::skills::skill_import,
            crate::skills::skill_remove
        ])
        .setup(|app| {
            create_main_window(app)?;
            // The right pane's browser: one child webview, plus the loopback
            // bridge a runtime's shell commands use to drive it.
            let browser = std::sync::Arc::new(crate::browser::Browser::new());
            app.manage(crate::browser::BrowserHandle(browser.clone()));
            match crate::bridge::start(app.handle().clone(), browser) {
                Ok(port) => app
                    .state::<HostState>()
                    .config
                    .log
                    .info(&format!("browser bridge listening on 127.0.0.1:{port}")),
                Err(error) => app
                    .state::<HostState>()
                    .config
                    .log
                    .info(&format!("browser bridge unavailable: {error}")),
            }
            start_system_theme_poller(app.handle().clone());
            // The scheduler: due rows become real turns, one thread per fire.
            crate::schedules::spawn(app.handle().clone());
            Ok(())
        })
        .build(context)?
        .run(|handle, event| {
            if matches!(event, tauri::RunEvent::Exit) {
                handle.state::<HostState>().stop();
            }
        });

    Ok(())
}
