//! Host preflight: every writable location and the frontend bundle.
//!
//! Order and semantics follow the Go host (`cmd/anvil/main.go`): the data
//! directory is mandatory and must be a writable D: directory with no fallback,
//! the WebView2 data directory defaults to `<dataDir>/webview2`, the log
//! directory is `<dataDir>/logs`, and the built frontend is located before any
//! window exists. All path checks reuse `anvil-paths`, so reparse points,
//! cross-volume junctions and unwritable DACLs are handled by the shared
//! contract. Nothing here creates a window; `desktop::run` only builds the
//! Tauri app after this returns `Ok`.

use std::path::{Path, PathBuf};

use anvil_paths::validate_d_path;

use crate::log::HostLog;
use crate::options::{MIN_HEIGHT, MIN_WIDTH, WindowSize, initial_url, parse_window_size};
use crate::prefs::{PreferenceStore, resolve_theme};

/// Environment variable holding the mandatory D: data directory.
pub const DATA_DIR_ENV: &str = "ANVIL_DATA_DIR";
/// Environment variable for the WebView2 user data directory.
pub const WEBVIEW_DIR_ENV: &str = "ANVIL_WEBVIEW_DATA_DIR";
/// Environment variable for an explicit frontend bundle directory.
pub const FRONTEND_DIST_ENV: &str = "ANVIL_FRONTEND_DIST";
/// Environment variable for the initial route.
pub const INITIAL_URL_ENV: &str = "ANVIL_INITIAL_URL";
/// Environment variable overriding the window width.
pub const WINDOW_WIDTH_ENV: &str = "ANVIL_WINDOW_WIDTH";
/// Environment variable overriding the window height.
pub const WINDOW_HEIGHT_ENV: &str = "ANVIL_WINDOW_HEIGHT";

/// Startup failures reported before any window is created.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum HostError {
    /// `ANVIL_DATA_DIR` is missing, empty or not a writable D: directory.
    DataDirectory(String),
    /// The WebView2 user data directory is not a writable D: directory.
    WebviewDirectory(String),
    /// `<dataDir>/logs` is not a writable D: directory or the log file cannot be opened.
    LogDirectory(String),
    /// The built React bundle (`frontend/dist/index.html`) was not found.
    FrontendDist(String),
}

impl std::fmt::Display for HostError {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            HostError::DataDirectory(message)
            | HostError::WebviewDirectory(message)
            | HostError::LogDirectory(message)
            | HostError::FrontendDist(message) => write!(formatter, "{message}"),
        }
    }
}

impl std::error::Error for HostError {}

/// Fully resolved host configuration.
pub struct HostConfig {
    /// Real (reparse-resolved) data directory under D:.
    pub data_dir: PathBuf,
    /// Real WebView2 user data directory under D:.
    pub webview_dir: PathBuf,
    /// Real log directory under D:.
    pub log_dir: PathBuf,
    /// Directory containing the built frontend (`index.html`).
    pub dist_dir: PathBuf,
    /// Effective window size.
    pub size: WindowSize,
    /// `window-options:` warnings for rejected size overrides.
    pub size_warnings: Vec<String>,
    /// Effective initial route (always starts with `/`).
    pub initial_url: String,
    /// `window-options:` warning for a rejected `ANVIL_INITIAL_URL`.
    pub initial_url_warning: Option<String>,
    /// Host log writer.
    pub log: HostLog,
    /// Persisted preferences.
    pub preferences: PreferenceStore,
}

impl HostConfig {
    /// Reads the process environment.
    pub fn from_env() -> Result<Self, HostError> {
        Self::prepare(&|name| std::env::var(name).ok())
    }

    /// Testable preflight: `env` returns raw environment values.
    pub fn prepare(env: &dyn Fn(&str) -> Option<String>) -> Result<Self, HostError> {
        let raw_data_dir = env(DATA_DIR_ENV).unwrap_or_default();
        let raw_data_dir = raw_data_dir.trim();
        if raw_data_dir.is_empty() {
            return Err(HostError::DataDirectory(format!(
                "{DATA_DIR_ENV} is required and must be a writable D: directory (no fallback to another drive)"
            )));
        }
        let data = validate_d_path(raw_data_dir, true)
            .map_err(|error| HostError::DataDirectory(format!("{DATA_DIR_ENV}: {error}")))?;

        let raw_webview_dir = env(WEBVIEW_DIR_ENV).unwrap_or_default();
        let raw_webview_dir = raw_webview_dir.trim();
        let webview_requested = if raw_webview_dir.is_empty() {
            data.resolved.join("webview2")
        } else {
            PathBuf::from(raw_webview_dir)
        };
        let webview = validate_d_path(&webview_requested, true).map_err(|error| {
            HostError::WebviewDirectory(format!("WebView2 data directory: {error}"))
        })?;

        let log_dir = data.resolved.join("logs");
        validate_d_path(&log_dir, true)
            .map_err(|error| HostError::LogDirectory(format!("log directory: {error}")))?;
        let log = HostLog::open(&log_dir).map_err(|error| {
            HostError::LogDirectory(format!(
                "open {}: {error}",
                log_dir.join("anvil.log").display()
            ))
        })?;

        let cwd = std::env::current_dir().ok();
        let exe_dir = std::env::current_exe()
            .ok()
            .and_then(|exe| exe.parent().map(std::path::Path::to_path_buf));
        let dist_dir = resolve_frontend_dist(env, cwd.as_deref(), exe_dir.as_deref())?;
        let (size, size_warnings) = parse_window_size(
            &env(WINDOW_WIDTH_ENV).unwrap_or_default(),
            &env(WINDOW_HEIGHT_ENV).unwrap_or_default(),
        );
        let (initial_url, initial_url_warning) =
            initial_url(&env(INITIAL_URL_ENV).unwrap_or_default());
        let preferences = PreferenceStore::load(&data.resolved, &log);

        Ok(Self {
            data_dir: data.resolved,
            webview_dir: webview.resolved,
            log_dir,
            dist_dir,
            size,
            size_warnings,
            initial_url,
            initial_url_warning,
            log,
            preferences,
        })
    }

    /// Writes the startup signal lines the native runner depends on.
    pub fn log_startup(&self, system_dark: bool) {
        let preferences = self.preferences.snapshot();
        let resolved = resolve_theme(&preferences.theme, system_dark);
        self.log.info(&format!(
            "theme-options: mode={} resolved={}",
            preferences.theme, resolved
        ));
        self.log
            .info(&format!("i18n-options: language={}", preferences.language));
        for warning in &self.size_warnings {
            self.log.info(&format!("window-options: {warning}"));
        }
        self.log.info(&format!(
            "starting: dist={} data={} webview={}",
            self.dist_dir.display(),
            self.data_dir.display(),
            self.webview_dir.display()
        ));
        self.log.info(&format!(
            "window-options: requested={}x{} min={}x{}",
            self.size.width, self.size.height, MIN_WIDTH, MIN_HEIGHT
        ));
        if let Some(warning) = &self.initial_url_warning {
            self.log.info(warning);
        }
        self.log
            .info(&format!("window-options: initialURL={}", self.initial_url));
    }

    /// Path Tauri should load into the main window (`index.html` for `/`).
    #[must_use]
    pub fn initial_path(&self) -> String {
        if self.initial_url == "/" {
            "index.html".to_string()
        } else {
            self.initial_url.trim_start_matches('/').to_string()
        }
    }

    /// Background colour for a resolved theme, matching the Go host.
    #[must_use]
    pub fn window_background(resolved: &str) -> [u8; 4] {
        if resolved == "dark" {
            [20, 22, 28, 255]
        } else {
            [245, 246, 248, 255]
        }
    }
}

/// Resolves the built frontend. `cwd`/`exe_dir` are parameters so tests never
/// depend on the process working directory.
pub fn resolve_frontend_dist(
    env: &dyn Fn(&str) -> Option<String>,
    cwd: Option<&Path>,
    exe_dir: Option<&Path>,
) -> Result<PathBuf, HostError> {
    let mut candidates: Vec<PathBuf> = Vec::new();
    if let Some(override_dir) = env(FRONTEND_DIST_ENV) {
        let override_dir = override_dir.trim();
        if !override_dir.is_empty() {
            candidates.push(PathBuf::from(override_dir));
        }
    }
    if let Some(cwd) = cwd {
        candidates.push(cwd.join("frontend").join("dist"));
    }
    if let Some(exe_dir) = exe_dir {
        candidates.push(exe_dir.join("frontend").join("dist"));
        candidates.push(exe_dir.join("dist"));
    }
    for candidate in &candidates {
        if candidate.join("index.html").is_file() {
            return candidate.canonicalize().map_err(|error| {
                HostError::FrontendDist(format!("frontend dist {}: {error}", candidate.display()))
            });
        }
    }
    let looked_at = candidates
        .iter()
        .map(|candidate| candidate.display().to_string())
        .collect::<Vec<_>>()
        .join(", ");
    Err(HostError::FrontendDist(format!(
        "frontend dist not found (looked for index.html in {looked_at}); run npm --prefix frontend run build or set {FRONTEND_DIST_ENV}"
    )))
}

/// Convenience for tests and diagnostics: `<dataDir>/preferences.json` path.
#[must_use]
pub fn preferences_path(data_dir: &Path) -> PathBuf {
    data_dir.join(crate::prefs::PREFERENCES_FILE)
}
