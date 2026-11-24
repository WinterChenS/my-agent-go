//! Host state, main-window creation and the system-theme push.
//!
//! The window is created **after** `HostConfig::prepare` succeeded, from
//! `Builder::setup`, because `tauri.conf.json` declares `app.windows = []`.
//! Creation explicitly passes the preflighted WebView2 `data_directory`, the
//! frozen geometry, the resolved theme background and the first-frame
//! `initialization_script`.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use serde::Serialize;
use tauri::window::Color;
use tauri::{AppHandle, Emitter, Manager, WebviewUrl, WebviewWindow, WebviewWindowBuilder};

use crate::host::HostConfig;
use crate::inject::{self, InitialState};
use crate::options::{MIN_HEIGHT, MIN_WIDTH};
use crate::prefs::{self, Preferences};
use crate::system_theme::{self, SYSTEM_THEME_EVENT};

/// Payload returned by `get_preferences`/`set_preferences`, mirroring the old
/// `GET/PUT /__anvil/preferences` JSON body.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct PreferencesResponse {
    /// Theme mode (`system`, `light`, `dark`).
    pub theme: String,
    /// Whether the OS currently reports a dark app theme.
    #[serde(rename = "systemDark")]
    pub system_dark: bool,
    /// Effective theme (`light`, `dark`).
    pub resolved: String,
    /// Language (`zh-CN`, `en-US`).
    pub language: String,
}

/// Payload of the `anvil://system-theme` event.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct SystemThemePayload {
    /// Whether the OS currently reports a dark app theme.
    #[serde(rename = "systemDark")]
    pub system_dark: bool,
    /// Effective theme for the current mode.
    pub resolved: String,
}

/// State shared by the commands, the window and the theme poller.
pub struct HostState {
    /// Resolved host configuration.
    pub config: HostConfig,
    /// Last observed OS app theme.
    pub system_dark: AtomicBool,
    /// Injection state used by the asset layer on every `index.html` request.
    pub injection: Arc<Mutex<Option<InitialState>>>,
    /// Main window handle, set once the window exists.
    pub window: Mutex<Option<WebviewWindow>>,
    /// Cleared on `RunEvent::Exit` so the poller stops.
    pub running: Arc<AtomicBool>,
    last_logged: Mutex<String>,
}

impl HostState {
    /// Builds the state from preflight results.
    #[must_use]
    pub fn new(
        config: HostConfig,
        system_dark: bool,
        injection: Arc<Mutex<Option<InitialState>>>,
    ) -> Self {
        Self {
            config,
            system_dark: AtomicBool::new(system_dark),
            injection,
            window: Mutex::new(None),
            running: Arc::new(AtomicBool::new(true)),
            last_logged: Mutex::new(String::new()),
        }
    }

    /// Current preferences plus resolved theme and system theme.
    #[must_use]
    pub fn snapshot(&self) -> PreferencesResponse {
        let preferences = self.config.preferences.snapshot();
        let system_dark = self.system_dark.load(Ordering::Relaxed);
        PreferencesResponse {
            resolved: prefs::resolve_theme(&preferences.theme, system_dark).to_string(),
            theme: preferences.theme,
            system_dark,
            language: preferences.language,
        }
    }

    /// Resolved theme for the current mode and system theme.
    #[must_use]
    pub fn resolved(&self) -> String {
        let preferences = self.config.preferences.snapshot();
        prefs::resolve_theme(&preferences.theme, self.system_dark.load(Ordering::Relaxed))
            .to_string()
    }

    /// Stores the main window handle.
    pub fn set_window(&self, window: WebviewWindow) {
        if let Ok(mut guard) = self.window.lock() {
            *guard = Some(window);
        }
    }

    /// Stops the system-theme poller.
    pub fn stop(&self) {
        self.running.store(false, Ordering::Relaxed);
    }

    /// Persists a theme and/or language patch, then re-syncs window + injection.
    pub fn set_preferences(
        &self,
        theme: Option<&str>,
        language: Option<&str>,
    ) -> Result<PreferencesResponse, String> {
        if theme.is_none() && language.is_none() {
            return Err("theme or language is required".to_string());
        }
        if let Some(theme) = theme {
            match self.config.preferences.set_theme(theme) {
                Ok(preferences) => {
                    let resolved = prefs::resolve_theme(
                        &preferences.theme,
                        self.system_dark.load(Ordering::Relaxed),
                    );
                    self.config.log.info(&format!(
                        "theme-options: user selected {} (resolved {})",
                        preferences.theme, resolved
                    ));
                }
                Err(error) => {
                    self.config
                        .log
                        .info(&format!("theme-options: reject {theme:?}: {error}"));
                    return Err(error);
                }
            }
        }
        if let Some(language) = language
            && let Err(error) = self
                .config
                .preferences
                .set_language(language, &self.config.log)
        {
            self.config
                .log
                .info(&format!("i18n-options: reject {language:?}: {error}"));
            return Err(error);
        }
        self.sync_background();
        Ok(self.snapshot())
    }

    /// Logs `theme-options:` changes, updates the window background and refreshes
    /// the asset-layer injection (the Go host's `syncBackground`).
    pub fn sync_background(&self) {
        let preferences = self.config.preferences.snapshot();
        let system_dark = self.system_dark.load(Ordering::Relaxed);
        let resolved = prefs::resolve_theme(&preferences.theme, system_dark).to_string();
        let signature = format!("{}|{}|{}", preferences.theme, system_dark, resolved);
        let changed = match self.last_logged.lock() {
            Ok(mut last) => {
                if *last == signature {
                    false
                } else {
                    *last = signature;
                    true
                }
            }
            Err(_) => true,
        };
        if changed {
            self.config.log.info(&format!(
                "theme-options: mode={} systemDark={} resolved={}",
                preferences.theme, system_dark, resolved
            ));
        }
        let initial = InitialState::new(&preferences, system_dark);
        if let Ok(mut guard) = self.injection.lock() {
            *guard = Some(initial);
        }
        let [red, green, blue, alpha] = HostConfig::window_background(&resolved);
        if let Ok(guard) = self.window.lock()
            && let Some(window) = guard.as_ref()
        {
            let _ = window.set_background_color(Some(Color(red, green, blue, alpha)));
            // The frame follows the *app's* theme, not the system's. Without
            // this macOS kept drawing a dark titlebar around a light page (the
            // window inherits the system appearance), which is exactly the
            // "only the border is black" report.
            let _ = window.set_theme(Some(theme_of(&resolved)));
        }
    }

    /// Appends a frontend diagnostic line with the Go host's prefix rules.
    pub fn log_event(&self, message: &str) {
        let sanitized = sanitize_probe(message);
        if sanitized.starts_with("theme:")
            || sanitized.starts_with("i18n:")
            || sanitized.starts_with("i18n-title:")
        {
            self.config.log.info(&sanitized);
        } else {
            self.config.log.info(&format!("layout-probe: {sanitized}"));
        }
    }
}

/// Collapses CR/LF, trims and caps a probe line at 512 characters (Go host).
#[must_use]
pub fn sanitize_probe(body: &str) -> String {
    let single = body.replace(['\r', '\n'], " ");
    single.trim().chars().take(512).collect()
}

/// The window chrome that matches a resolved app theme.
fn theme_of(resolved: &str) -> tauri::Theme {
    if resolved == "dark" {
        tauri::Theme::Dark
    } else {
        tauri::Theme::Light
    }
}

/// Creates the trusted main window after preflight.
pub fn create_main_window(app: &mut tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    let state = app.state::<HostState>();
    let preferences: Preferences = state.config.preferences.snapshot();
    let system_dark = state.system_dark.load(Ordering::Relaxed);
    let resolved = prefs::resolve_theme(&preferences.theme, system_dark);
    let script = inject::initialization_script(&InitialState::new(&preferences, system_dark));
    let [red, green, blue, alpha] = HostConfig::window_background(resolved);

    let mut builder = WebviewWindowBuilder::new(
        app,
        "main",
        WebviewUrl::App(state.config.initial_path().into()),
    )
    .title("Anvil")
    .inner_size(
        state.config.size.width as f64,
        state.config.size.height as f64,
    )
    .min_inner_size(MIN_WIDTH as f64, MIN_HEIGHT as f64)
    .background_color(Color(red, green, blue, alpha))
    .theme(Some(theme_of(&resolved)))
    .data_directory(state.config.webview_dir.clone())
    .initialization_script(script);

    // Debug hook: `ANVIL_WEBVIEW_ARGS` reaches WebView2 verbatim, so a test run
    // can open the CDP port (`--remote-debugging-port=9333`) and drive the real
    // WebView. Unset in normal use, so this adds nothing to the shipped app.
    if let Ok(extra) = std::env::var("ANVIL_WEBVIEW_ARGS") {
        builder = builder.additional_browser_args(&extra);
    }

    // macOS: AppKit paints the title bar in its own system material, a grey
    // that matches nothing in the app — that is the seam above the topbar. A
    // transparent title bar shows the window's background colour instead, which
    // is `window_background` below: the same token the shell paints its chrome
    // with, so the strip and the topbar are one colour, and it follows the
    // theme. `Transparent` (not `Overlay`) keeps the content view out of the
    // title bar: the page keeps its 792 pt viewport and the traffic lights stay
    // above the app's own header rather than on top of it. The title text is
    // hidden because the app draws its own.
    #[cfg(target_os = "macos")]
    {
        builder = builder
            .title_bar_style(tauri::TitleBarStyle::Transparent)
            .hidden_title(true);
    }

    let window = builder.build()?;

    state.set_window(window);
    state.config.log.info("window created; running");
    Ok(())
}

/// Pushes OS app-theme flips to the frontend and the window background.
pub fn start_system_theme_poller(handle: AppHandle) {
    let running = handle.state::<HostState>().running.clone();
    std::thread::spawn(move || {
        let mut last = system_theme::system_dark();
        while running.load(Ordering::Relaxed) {
            std::thread::sleep(system_theme::SYSTEM_THEME_POLL_INTERVAL);
            if !running.load(Ordering::Relaxed) {
                break;
            }
            let dark = system_theme::system_dark();
            if dark == last {
                continue;
            }
            last = dark;
            let state = handle.state::<HostState>();
            state.system_dark.store(dark, Ordering::Relaxed);
            state.sync_background();
            let payload = SystemThemePayload {
                system_dark: dark,
                resolved: state.resolved(),
            };
            let _ = handle.emit(SYSTEM_THEME_EVENT, payload);
        }
    });
}
