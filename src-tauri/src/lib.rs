//! Anvil desktop host (Tauri v2) for the existing React frontend.
//!
//! R0-RS02 migrates the retired Go/Wails host's behaviour, not its stack:
//!
//! * `host` — D:-only preflight of the data directory, the WebView2 user data
//!   directory, the log directory and the built frontend, before any window.
//! * `prefs` — `<dataDir>/preferences.json` (`theme`, `language`, 2-space JSON,
//!   trailing newline, tmp+rename) with the Go host's defaults/normalization.
//! * `log` — `<dataDir>/logs/anvil.log`, UTC, same prefixes the native runner
//!   reads: `theme-options:`, `i18n-options:`, `window-options:`, `starting:`.
//! * `inject` — first-frame theme/language injection (`__ANVIL_INITIAL_*`,
//!   `<html lang>`, `dataset.theme`, `colorScheme`).
//! * `assets` — runtime asset provider for `frontend/dist` (disk, not embedded).
//! * `state`/`commands` — window creation after preflight, typed preferences and
//!   log commands, and the system-theme event push;
//! * `catalog` — runtime discovery, resource sync and the phase-1 actions
//!   (custom model, verification, default model, MCP probe).
//! * `session` — the phase-2 chat path: open a session on a real runtime, send a
//!   turn, stream it by polling the ledger, stop it, and manage history.
//! * `extensions` — runtime extensions the chat path depends on: pi's
//!   `subagent` extension status and one-click install.
//! * `bundled` / `pi_import` — the Pi runtime this install carries (ADR 0009):
//!   what it is, whether it loaded, and the one-time import of an old `~/.pi`.
//!
//! The crate is Windows-first like the rest of this migration phase, but all
//! modules here are platform-neutral except `system_theme` (registry read).

pub mod assets;
pub mod catalog;
pub mod commands;
pub mod browser;
pub mod bridge;
pub mod bundled;
pub mod connectors;
pub mod desktop;
pub mod extensions;
pub mod host;
pub mod inject;
pub mod library;
pub mod log;
pub mod memory;
pub mod options;
pub mod pi_auth;
pub mod pi_import;
pub mod prefs;
pub mod runtimes;
pub mod session;
pub mod state;
pub mod schedules;
pub mod updater;
pub mod skills;
pub mod subagents;
pub mod system_theme;
pub mod workbench;
pub mod workspace;

/// Every app command the host registers, in ACL order.
///
/// `permissions/anvil.toml` generates one `allow-<slug>` permission per entry and
/// `capabilities/main.json` grants them to the trusted `main` window only.
/// `tests/host_contract.rs` asserts the three lists stay in sync.
pub const APP_COMMANDS: &[&str] = &[
    "get_preferences",
    "set_preferences",
    "log_event",
    "open_external",
    "catalog_snapshot",
    "catalog_refresh",
    "catalog_sync",
    "catalog_sync_inventory",
    "catalog_add_model",
    "catalog_verify_model",
    "catalog_set_default_model",
    "catalog_set_model_enabled",
    "catalog_mark_setup_done",
    "catalog_save_agent",
    "catalog_delete_agent",
    "subagent_overview",
    "catalog_probe_mcp",
    "catalog_add_mcp",
    "catalog_test_mcp",
    "catalog_remove_mcp",
    "runtime_install",
    "runtime_update_check",
    "pi_runtime_status",
    "pi_import_plan",
    "pi_import_run",
    "pi_providers",
    "pi_provider_set_key",
    "pi_provider_logout",
    "pi_provider_custom_save",
    "pi_provider_custom_remove",
    "pi_provider_login",
    "pi_provider_login_submit",
    "pi_provider_login_cancel",
    "app_update_check",
    "app_update_install",
    "session_list",
    "session_detail",
    "session_events",
    "session_open",
    "session_send",
    "session_abort",
    "session_steer",
    "session_context",
    "session_rename",
    "file_preview",
    "browser_open",
    "browser_bounds",
    "browser_close",
    "browser_status",
    "browser_action",
    "session_pin",
    "session_archive",
    "session_delete",
    "session_projects",
    "session_fork",
    "session_compact",
    "session_approve",
    "session_extension_ui",
    "session_context_list",
    "session_rail",
    "session_export",
    "session_audit",
    "session_audit_export",
    "workbench_overview",
    "memory_overview",
    "memory_save",
    "memory_update",
    "memory_delete",
    "memory_clear",
    "memory_set_switches",
    "agent_runs",
    "workspace_defaults",
    "workspace_ensure",
    "workspace_create",
    "workspace_pick",
    "workspace_pick_file",
    "workspace_pick_files",
    "pi_subagent_status",
    "pi_extensions",
    "pi_subagent_install",
    "pi_subagent_update",
    "schedule_overview",
    "schedule_save",
    "schedule_delete",
    "schedule_toggle",
    "schedule_run_now",
    "library_overview",
    "library_import",
    "library_export",
    "library_preview",
    "library_reveal",
    "connectors_overview",
    "connector_save",
    "connector_delete",
    "connector_toggle",
    "connector_probe",
    "skill_create",
    "skill_import",
    "skill_remove",
];
