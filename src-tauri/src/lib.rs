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
//!
//! The crate is Windows-first like the rest of this migration phase, but all
//! modules here are platform-neutral except `system_theme` (registry read).

pub mod assets;
pub mod catalog;
pub mod commands;
pub mod browser;
pub mod bridge;
pub mod desktop;
pub mod extensions;
pub mod host;
pub mod inject;
pub mod log;
pub mod options;
pub mod prefs;
pub mod runtimes;
pub mod session;
pub mod state;
pub mod system_theme;
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
    "catalog_probe_mcp",
    "catalog_add_mcp",
    "catalog_test_mcp",
    "runtime_install",
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
    "agent_runs",
    "workspace_defaults",
    "workspace_ensure",
    "workspace_create",
    "workspace_pick",
    "workspace_pick_file",
    "workspace_pick_files",
    "pi_subagent_status",
    "pi_subagent_install",
];
