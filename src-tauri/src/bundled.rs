//! The Pi runtime that ships with Anvil (ADR 0009).
//!
//! Startup is the one place that turns the installer's resource directory into
//! the runtime every other module uses. Doing it here — and publishing the
//! result with [`anvil_catalog::runtime::bundled::set_active`] — is what makes
//! "the app runs the Pi it carries" true everywhere: the catalogue's probe, the
//! session adapter, the OAuth helper, the skills page and the sub-agent files
//! all read the same paths.
//!
//! A bundle that cannot be loaded is **not** a reason to fail startup: the
//! window has to open so it can say what is wrong and offer a reinstall. The
//! error is kept next to the state and reported by the surfaces that need pi.

use std::path::Path;
use std::sync::Arc;

use anvil_catalog::runtime::bundled::BundledPi;
use serde::Serialize;
use tauri::Manager;

/// Resource directory of this install, resolved by Tauri.
fn resource_dir(app: &tauri::AppHandle) -> Result<std::path::PathBuf, String> {
    app.path()
        .resource_dir()
        .map_err(|error| format!("cannot locate Anvil's install resources: {error}"))
}

/// Load and publish the bundled runtime for this process.
///
/// Returns the error instead of propagating it: a broken install still opens.
pub fn load(app: &tauri::AppHandle, data_dir: &Path) -> Result<Arc<BundledPi>, String> {
    let resources = resource_dir(app)?;
    let bundle = BundledPi::load(&resources, data_dir)?;
    let bundle = Arc::new(bundle);
    anvil_catalog::runtime::bundled::set_active(Arc::clone(&bundle));
    Ok(bundle)
}

/// Where the runtime lives, for a log line that has to work even when the load
/// failed (the manifest may be missing, the directory may not be).
#[must_use]
pub fn describe_location(app: &tauri::AppHandle) -> String {
    let override_dir = std::env::var_os(anvil_catalog::runtime::bundled::DIR_ENV);
    if let Some(dir) = override_dir.filter(|value| !value.is_empty()) {
        return format!(
            "{} (from {})",
            dir.to_string_lossy(),
            anvil_catalog::runtime::bundled::DIR_ENV
        );
    }
    match resource_dir(app) {
        Ok(dir) => dir
            .join(anvil_catalog::runtime::bundled::RESOURCE_SUBDIR)
            .display()
            .to_string(),
        Err(error) => error,
    }
}

/// What the settings page and the first-run wizard render about the runtime this
/// install carries.
///
/// Two states, and the second one is the whole point of the type: a tree that
/// cannot be loaded is **damaged**, not missing. "Not installed" would offer to
/// install `pi` — via npm, from the internet — which is exactly the path ADR
/// 0009 removed. The `error` string is the loader's own text, unedited, so the
/// page can show the file that is at fault and what fixes it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BundledPiStatus {
    /// `bundled` when the runtime is usable, `damaged` when it is not.
    pub state: &'static str,
    /// Pi's own version, from the manifest. `None` when it could not be read.
    pub version: Option<String>,
    pub node_version: Option<String>,
    /// Directory the installer put the runtime in.
    pub install_root: String,
    /// Private Node interpreter, and the CLI it runs.
    pub node_path: String,
    pub cli_path: String,
    /// `PI_CODING_AGENT_DIR`: the Anvil data directory.
    pub agent_dir: String,
    /// User-level skill root, `<data_dir>/skill`.
    pub skill_root: String,
    /// The Anvil data directory this runtime reads and writes.
    pub data_dir: String,
    /// Why the runtime is unusable; `None` when it is fine.
    pub error: Option<String>,
}

impl BundledPiStatus {
    /// A runtime that is present and usable.
    #[must_use]
    pub fn ready(bundle: &BundledPi, data_dir: &Path) -> Self {
        Self {
            state: "bundled",
            version: Some(bundle.pi_version.clone()),
            node_version: Some(bundle.node_version.clone()),
            install_root: bundle.root.display().to_string(),
            node_path: bundle.node.display().to_string(),
            cli_path: bundle.cli.display().to_string(),
            agent_dir: bundle.agent_dir.display().to_string(),
            skill_root: bundle.skill_root.display().to_string(),
            data_dir: data_dir.display().to_string(),
            error: None,
        }
    }

    /// A runtime that should be there and is not.
    ///
    /// The paths are the ones the app would have used, and the version fields are
    /// empty: there is no runtime to ask, and reporting the version the manifest
    /// promised would be a claim about a file that is gone.
    #[must_use]
    pub fn damaged(error: &str, install_root: &str, data_dir: &Path) -> Self {
        Self {
            state: "damaged",
            version: None,
            node_version: None,
            install_root: install_root.to_string(),
            node_path: String::new(),
            cli_path: String::new(),
            agent_dir: data_dir.display().to_string(),
            skill_root: data_dir.join("skill").display().to_string(),
            data_dir: data_dir.display().to_string(),
            error: Some(error.to_string()),
        }
    }
}

/// The runtime this install carries, as the settings page needs it.
///
/// Answers in both states, and never with "pi is not installed": the only two
/// answers are "here is the one that ships with Anvil" and "the one that ships
/// with Anvil is broken, and here is why".
#[tauri::command]
pub fn pi_runtime_status(
    app: tauri::AppHandle,
    host: tauri::State<'_, crate::state::HostState>,
) -> BundledPiStatus {
    let data_dir = host.config.data_dir.clone();
    match host.loaded_pi() {
        Some(bundle) => BundledPiStatus::ready(&bundle, &data_dir),
        None => BundledPiStatus::damaged(
            &host
                .pi_error()
                .unwrap_or_else(|| "the bundled Pi runtime was not loaded at startup".to_string()),
            &describe_location(&app),
            &data_dir,
        ),
    }
}
