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
    // A sub agent saved by an older build has a row and no file, so pi has never
    // been able to delegate to it. Reconcile once at startup instead of waiting
    // for the user to press 保存 again.
    match catalog.with_service(|service| service.snapshot().map_err(|error| error.to_wire())) {
        Ok(snapshot) => {
            let report = crate::subagents::sync_agents(&snapshot.agents);
            if !report.is_noop() {
                config.log.info(&crate::subagents::summary(
                    crate::subagents::agents_dir(),
                    &report,
                ));
            }
        }
        Err(error) => config
            .log
            .info(&format!("subagents: not synced at startup: {error}")),
    }
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
        // Provider sign-ins outlive the command that starts them; the sessions
        // live here so a cancel (or a closed window) can stop the helper.
        .manage(crate::pi_auth::PiLoginState::default())
        // The native folder/file dialogs (`workspace_pick*`).
        .plugin(tauri_plugin_dialog::init())
        // Updates come from the release the CI publishes; the plugin owns the
        // signature check and the per-platform install, both of which are
        // easier to get wrong by hand than to configure.
        .plugin(tauri_plugin_updater::Builder::new().build())
        .invoke_handler(tauri::generate_handler![
            crate::commands::get_preferences,
            crate::commands::set_preferences,
            crate::commands::log_event,
            crate::commands::open_external,
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
            crate::catalog::subagent_overview,
            crate::catalog::catalog_probe_mcp,
            crate::catalog::catalog_add_mcp,
            crate::catalog::catalog_test_mcp,
            crate::catalog::catalog_remove_mcp,
            crate::catalog::runtime_install,
            crate::catalog::runtime_update_check,
            crate::pi_auth::pi_providers,
            crate::pi_auth::pi_provider_set_key,
            crate::pi_auth::pi_provider_logout,
            crate::pi_auth::pi_provider_custom_save,
            crate::pi_auth::pi_provider_custom_remove,
            crate::pi_auth::pi_provider_login,
            crate::pi_auth::pi_provider_login_submit,
            crate::pi_auth::pi_provider_login_cancel,
            crate::updater::app_update_check,
            crate::updater::app_update_install,
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
            crate::memory::memory_overview,
            crate::memory::memory_save,
            crate::memory::memory_update,
            crate::memory::memory_delete,
            crate::memory::memory_clear,
            crate::memory::memory_set_switches,
            crate::session::agent_runs,
            crate::workspace::workspace_defaults,
            crate::workspace::workspace_ensure,
            crate::workspace::workspace_create,
            crate::workspace::workspace_pick,
            crate::workspace::workspace_pick_file,
            crate::workspace::workspace_pick_files,
            crate::extensions::pi_subagent_status,
            crate::extensions::pi_extensions,
            crate::extensions::pi_subagent_install,
            crate::extensions::pi_subagent_update,
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
            // A provider sign-in waits on a browser, not on the window. Closing
            // the window must not leave its Node process (and a session with no
            // page to answer it) behind.
            if let Some(window) = app.get_window("main") {
                let handle = app.handle().clone();
                window.on_window_event(move |event| {
                    if matches!(
                        event,
                        tauri::WindowEvent::CloseRequested { .. } | tauri::WindowEvent::Destroyed
                    ) {
                        handle.state::<crate::pi_auth::PiLoginState>().cancel_all();
                    }
                });
            }
            // macOS moves a window's child views itself while the window
            // resizes, and it does that after the page has already reported the
            // pane's new rect — so the last word belongs to AppKit and the page
            // is left where it was before the window changed size. Every resize
            // re-arms a placement that runs once the window is quiet.
            #[cfg(target_os = "macos")]
            if let Some(window) = app.get_window("main") {
                let handle = app.handle().clone();
                window.on_window_event(move |event| {
                    if matches!(
                        event,
                        tauri::WindowEvent::Resized(_)
                            | tauri::WindowEvent::ScaleFactorChanged { .. }
                    ) {
                        crate::browser::settle_later(&handle);
                    }
                    // macOS hides its title bar in fullscreen, and with it the
                    // strip the page reserves for the traffic lights. Only tell
                    // the page when the answer actually changed: a live resize
                    // fires this dozens of times a second.
                    #[cfg(target_os = "macos")]
                    if matches!(event, tauri::WindowEvent::Resized(_)) {
                        use std::sync::atomic::{AtomicBool, Ordering};
                        static WAS_FULLSCREEN: AtomicBool = AtomicBool::new(false);
                        let Some(view) = handle.get_webview_window("main") else {
                            return;
                        };
                        let fullscreen = view.is_fullscreen().unwrap_or(false);
                        if WAS_FULLSCREEN.swap(fullscreen, Ordering::Relaxed) != fullscreen {
                            let inset = if fullscreen {
                                0
                            } else {
                                crate::inject::TITLEBAR_INSET
                            };
                            let _ = view.eval(&crate::inject::top_inset_script(inset));
                        }
                    }
                });
            }
            // The right pane's browser: one child webview, plus the loopback
            // bridge a runtime's shell commands use to drive it.
            let browser = std::sync::Arc::new(crate::browser::Browser::new());
            app.manage(crate::browser::BrowserHandle(browser.clone()));
            // A platform can move a child view without telling anyone; this
            // reads the frame back and corrects it.
            #[cfg(target_os = "macos")]
            crate::browser::spawn_watchdog(app.handle().clone());
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
