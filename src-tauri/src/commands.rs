//! Typed Tauri commands replacing the Go host's `/__anvil/*` HTTP endpoints.
//!
//! Only the main-window capability lists the matching permissions
//! (`capabilities/main.json` + `permissions/anvil.toml`).

use tauri::State;

use crate::state::{HostState, PreferencesResponse};

/// `GET /__anvil/preferences` equivalent.
#[tauri::command]
pub fn get_preferences(state: State<'_, HostState>) -> PreferencesResponse {
    state.sync_background();
    state.snapshot()
}

/// `PUT /__anvil/preferences` equivalent. `None` leaves a field untouched;
/// at least one field is required, and an unknown value is rejected with the
/// same message the Go host returned as HTTP 400.
#[tauri::command]
pub fn set_preferences(
    state: State<'_, HostState>,
    theme: Option<String>,
    language: Option<String>,
) -> Result<PreferencesResponse, String> {
    state.set_preferences(theme.as_deref(), language.as_deref())
}

/// `POST /__anvil/log` equivalent (theme/i18n/layout probe lines).
#[tauri::command]
pub fn log_event(state: State<'_, HostState>, message: String) {
    state.log_event(&message);
}
