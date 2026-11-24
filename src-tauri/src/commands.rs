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

/// Open a link from an assistant answer in the reader's own browser.
///
/// A Tauri webview treats `target="_blank"` as a navigation it either refuses
/// or keeps inside the pane, so an answer's link looked dead. The host asks the
/// OS instead. Only absolute http(s) URLs are accepted: a link is data from the
/// model, and passing anything else to a platform opener would be unsafe. The
/// URL is one argument to a fixed program — never a shell string — so no part
/// of it can grow into a second command.
#[tauri::command]
pub fn open_external(url: String) -> Result<(), String> {
    let trimmed = url.trim();
    let valid = (trimmed.starts_with("http://") || trimmed.starts_with("https://"))
        && !trimmed.chars().any(|c| c.is_whitespace() || c.is_control());
    if !valid {
        return Err("only http and https links can be opened".to_string());
    }
    #[cfg(target_os = "windows")]
    let launched = {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        let mut command = std::process::Command::new("rundll32.exe");
        command
            .args(["url.dll,FileProtocolHandler", trimmed])
            .creation_flags(CREATE_NO_WINDOW)
            .spawn()
    };
    #[cfg(target_os = "macos")]
    let launched = std::process::Command::new("open").arg(trimmed).spawn();
    #[cfg(not(any(target_os = "windows", target_os = "macos")))]
    let launched = std::process::Command::new("xdg-open").arg(trimmed).spawn();

    launched
        .map(|_| ())
        .map_err(|error| format!("open link: {error}"))
}
