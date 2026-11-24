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

use anvil_paths::{validate_writable_path};

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

/// The app's per-user data directory: `%USERPROFILE%\.anvil`.
///
/// A normal desktop program keeps its data with the user. A fixed `D:` path
/// tied the app to one machine layout and broke on profiles without a D:
/// drive. The caller may still override it with [`DATA_DIR_ENV`].
#[must_use]
pub fn default_data_dir() -> PathBuf {
    for key in ["USERPROFILE", "HOME"] {
        if let Some(value) = std::env::var_os(key) {
            let path = PathBuf::from(value);
            if !path.as_os_str().is_empty() {
                return path.join(".anvil");
            }
        }
    }
    PathBuf::from(".anvil")
}

/// Import the old fixed-location data into the per-user directory, once.
///
/// Earlier builds kept everything under `D:\Anvil-Experience\data`. On the first
/// launch with the new `%USERPROFILE%\.anvil` default, copy the ledger, catalog,
/// preferences and runtime session files so history, models and agent
/// configuration survive the move. This never overwrites: if the destination
/// already has a ledger, it is a no-op.
pub fn migrate_legacy_data(data_dir: &Path, log: &HostLog) {
    const LEGACY: &str = r"D:\Anvil-Experience\data";
    let legacy = Path::new(LEGACY);
    if legacy == data_dir || !legacy.is_dir() {
        return;
    }
    if data_dir.join("sessions.sqlite").exists() || !legacy.join("sessions.sqlite").exists() {
        return;
    }
    log.info(&format!(
        "migrate: importing legacy data {} -> {}",
        legacy.display(),
        data_dir.display()
    ));
    for name in ["catalog.sqlite", "sessions.sqlite", "preferences.json"] {
        let source = legacy.join(name);
        if !source.is_file() {
            continue;
        }
        let target = data_dir.join(name);
        let result = if name.ends_with(".sqlite") {
            copy_sqlite_with_wal(&source, &target)
        } else {
            std::fs::copy(&source, &target).map(|_| ())
        };
        match result {
            Ok(()) => log.info(&format!("migrate: {name}")),
            Err(error) => log.info(&format!("migrate: {name} failed: {error}")),
        }
    }
    // Runtime session files (the workspace root pi and codex write into).
    let from = legacy.join("sessions");
    let to = data_dir.join("workspace");
    if from.is_dir() && !to.exists() {
        match copy_dir_all(&from, &to) {
            Ok(()) => log.info("migrate: runtime session files copied"),
            Err(error) => log.info(&format!("migrate: runtime session files failed: {error}")),
        }
    }
    log.info("migrate: done");
}

/// Copy a SQLite database together with its `-wal` sidecar.
///
/// The databases run in WAL mode, so the most recent commits live in
/// `<db>-wal`, not in the main file. Copying only the `.sqlite` file would
/// silently drop exactly the messages written just before the upgrade. The
/// `-shm` index is deliberately not copied: SQLite rebuilds it on first open.
fn copy_sqlite_with_wal(from: &Path, to: &Path) -> std::io::Result<()> {
    std::fs::copy(from, to)?;
    let wal = std::path::PathBuf::from(format!("{}-wal", from.display()));
    if wal.is_file() {
        let target = std::path::PathBuf::from(format!("{}-wal", to.display()));
        std::fs::copy(&wal, &target)?;
    }
    Ok(())
}

fn copy_dir_all(from: &Path, to: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(to)?;
    for entry in std::fs::read_dir(from)? {
        let entry = entry?;
        let target = to.join(entry.file_name());
        let kind = entry.file_type()?;
        if kind.is_dir() {
            copy_dir_all(&entry.path(), &target)?;
        } else if kind.is_file() {
            std::fs::copy(entry.path(), &target)?;
        }
    }
    Ok(())
}

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
    ///
    /// A plain double-click of `anvil.exe` (no launcher) has no `ANVIL_*` vars
    /// set and used to exit immediately with no log — indistinguishable from
    /// "won't start". Fill in the package's conventional data location so a
    /// direct launch works too. `prepare` stays strict: its contract tests and
    /// any explicit (even wrong) value are untouched — we only supply a value
    /// when the variable is genuinely absent/blank.
    pub fn from_env() -> Result<Self, HostError> {
        Self::prepare(&|name| {
            let value = std::env::var(name).ok();
            if name == DATA_DIR_ENV && value.as_deref().unwrap_or("").trim().is_empty() {
                return Some(default_data_dir().to_string_lossy().into_owned());
            }
            value
        })
    }

    /// Testable preflight: `env` returns raw environment values.
    pub fn prepare(env: &dyn Fn(&str) -> Option<String>) -> Result<Self, HostError> {
        let raw_data_dir = env(DATA_DIR_ENV).unwrap_or_default();
        let raw_data_dir = raw_data_dir.trim();
        if raw_data_dir.is_empty() {
            return Err(HostError::DataDirectory(format!(
                "{DATA_DIR_ENV} is required and must be a writable directory"
            )));
        }
        let data = validate_writable_path(raw_data_dir, true)
            .map_err(|error| HostError::DataDirectory(format!("{DATA_DIR_ENV}: {error}")))?;

        let raw_webview_dir = env(WEBVIEW_DIR_ENV).unwrap_or_default();
        let raw_webview_dir = raw_webview_dir.trim();
        let webview_requested = if raw_webview_dir.is_empty() {
            data.resolved.join("webview2")
        } else {
            PathBuf::from(raw_webview_dir)
        };
        let webview = validate_writable_path(&webview_requested, true).map_err(|error| {
            HostError::WebviewDirectory(format!("WebView2 data directory: {error}"))
        })?;

        let log_dir = data.resolved.join("logs");
        validate_writable_path(&log_dir, true)
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
    ///
    /// It is the shell's `--surface-2` token in sRGB — the colour of the sidebar
    /// and of the topbar: `oklch(23% 0.014 255)` dark, `oklch(97.4% 0.004 250)`
    /// light. On macOS this is what shows through the transparent title bar, the
    /// strip right above the topbar, so it has to be the same colour as the
    /// chrome or the top of the window reads as a seam. It is also the shade the
    /// window shows before the first paint, so a launch does not flash a
    /// different colour than the page it is about to draw.
    #[must_use]
    pub fn window_background(resolved: &str) -> [u8; 4] {
        if resolved == "dark" {
            [25, 29, 36, 255]
        } else {
            [244, 247, 249, 255]
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
        // A bundle unpacked without its `dist` level. It is only accepted when
        // `index.html` really sits there, so a stray folder cannot shadow a
        // real bundle — and a mispackaged zip stops being a silent no-window.
        candidates.push(cwd.join("frontend"));
    }
    if let Some(exe_dir) = exe_dir {
        candidates.push(exe_dir.join("frontend").join("dist"));
        candidates.push(exe_dir.join("frontend"));
        candidates.push(exe_dir.join("dist"));
        // Installed bundles do not keep the resources beside the executable:
        //   macOS  <app>/Contents/MacOS/anvil  ->  <app>/Contents/Resources
        //   Linux  /usr/bin/anvil              ->  /usr/lib/Anvil
        // The same relative rule covers an AppImage's $APPDIR/usr/bin.
        if let Some(parent) = exe_dir.parent() {
            candidates.push(parent.join("Resources").join("frontend").join("dist"));
            candidates.push(parent.join("Resources").join("frontend"));
            candidates.push(
                parent
                    .join("lib")
                    .join("Anvil")
                    .join("frontend")
                    .join("dist"),
            );
            candidates.push(parent.join("lib").join("Anvil").join("frontend"));
        }
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

#[cfg(test)]
mod tests {
    use super::*;

    fn bundle(dir: &Path) {
        std::fs::create_dir_all(dir).expect("dir");
        std::fs::write(dir.join("index.html"), "<!doctype html>").expect("index");
    }

    /// A zip unpacked one level too shallow (`frontend/index.html`, no `dist`)
    /// used to abort preflight before any window, which the user sees as
    /// "it will not start" with nothing on screen. The bundle is accepted from
    /// either layout as long as `index.html` is really there.
    #[test]
    fn a_bundle_without_the_dist_level_still_starts() {
        let root = std::env::temp_dir().join(format!("anvil-host-shallow-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        bundle(&root.join("frontend"));
        let env = |_: &str| None;

        let resolved =
            resolve_frontend_dist(&env, Some(root.as_path()), None).expect("shallow bundle");
        assert_eq!(
            resolved,
            root.join("frontend").canonicalize().expect("canonical")
        );

        let exe = resolve_frontend_dist(&env, None, Some(root.as_path())).expect("exe dir");
        assert_eq!(
            exe,
            root.join("frontend").canonicalize().expect("canonical")
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    /// Installers place the bundled frontend in the platform resource folder,
    /// not beside the binary. Both layouts must resolve without an env var.
    #[test]
    fn installed_macos_and_linux_layouts_are_found() {
        let env = |_: &str| None::<String>;

        let mac = std::env::temp_dir().join(format!("anvil-host-mac-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&mac);
        bundle(&mac.join("Contents/Resources/frontend/dist"));
        let found = resolve_frontend_dist(
            &env,
            None,
            Some(mac.join("Contents/MacOS").as_path()),
        )
        .expect("macOS Resources");
        assert_eq!(
            found,
            mac.join("Contents/Resources/frontend/dist")
                .canonicalize()
                .expect("canonical")
        );
        let _ = std::fs::remove_dir_all(&mac);

        let linux = std::env::temp_dir().join(format!("anvil-host-linux-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&linux);
        bundle(&linux.join("usr/lib/Anvil/frontend/dist"));
        let found =
            resolve_frontend_dist(&env, None, Some(linux.join("usr/bin").as_path()))
                .expect("Linux /usr/lib");
        assert_eq!(
            found,
            linux.join("usr/lib/Anvil/frontend/dist")
                .canonicalize()
                .expect("canonical")
        );
        let _ = std::fs::remove_dir_all(&linux);
    }
}
