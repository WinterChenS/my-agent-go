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
