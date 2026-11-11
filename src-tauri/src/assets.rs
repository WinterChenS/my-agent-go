//! Runtime asset provider for the React bundle.
//!
//! The retired Go host served `frontend/dist` from disk
//! (`application.AssetFileServerFS`), deliberately not embedded, so a Rust-only
//! build works in a clean checkout before the frontend is built. R0-RS02 keeps
//! that property: `DiskAssets` snapshots `<frontend/dist>` during host preflight
//! and hands the bytes to Tauri's asset protocol. `tauri.conf.json` still names
//! `../frontend/dist` as the canonical location.
//!
//! SPA behaviour (direct route + reload) comes from Tauri's `get_asset`
//! fallback chain (`<path>`, `<path>.html`, `<path>/index.html`, `index.html`),
//! and `/index.html` is rewritten per request with the current theme/language
//! injection so reloads never flash the wrong theme.

use std::borrow::Cow;
use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use tauri::Runtime;
use tauri::utils::assets::{AssetKey, AssetsIter, CspHash};

use crate::inject::{self, InitialState};

/// In-memory snapshot of the built frontend plus the live injection state.
pub struct DiskAssets {
    dist: PathBuf,
    files: BTreeMap<String, Vec<u8>>,
    injection: Arc<Mutex<Option<InitialState>>>,
}

impl DiskAssets {
    /// Reads every file under `dist` into memory. Fails when `index.html` is
    /// missing: the host refuses to start without a built frontend, exactly like
    /// the Go host's `frontendDist()`.
    pub fn load(dist: &Path) -> io::Result<Self> {
        let dist = dist.canonicalize()?;
        let mut files = BTreeMap::new();
        collect(&dist, Path::new(""), &mut files)?;
        if !files.contains_key("/index.html") {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                format!("frontend dist {} has no index.html", dist.display()),
            ));
        }
        Ok(Self {
            dist,
            files,
            injection: Arc::new(Mutex::new(None)),
        })
    }

    /// Directory the assets were loaded from.
    #[must_use]
    pub fn dist(&self) -> &Path {
        &self.dist
    }

    /// Number of files served.
    #[must_use]
    pub fn asset_count(&self) -> usize {
        self.files.len()
    }

    /// Shared handle used to push theme/language changes into the asset layer.
    #[must_use]
    pub fn injection_handle(&self) -> Arc<Mutex<Option<InitialState>>> {
        Arc::clone(&self.injection)
    }

    /// Replaces the state injected into `/index.html`.
    pub fn set_injection(&self, state: InitialState) {
        if let Ok(mut guard) = self.injection.lock() {
            *guard = Some(state);
        }
    }
}

fn collect(root: &Path, relative: &Path, files: &mut BTreeMap<String, Vec<u8>>) -> io::Result<()> {
    for entry in std::fs::read_dir(root.join(relative))? {
        let entry = entry?;
        let file_type = entry.file_type()?;
        let child = relative.join(entry.file_name());
        if file_type.is_dir() {
            collect(root, &child, files)?;
        } else if file_type.is_file() {
            let key = format!("/{}", child.to_string_lossy().replace('\\', "/"));
            files.insert(key, std::fs::read(entry.path())?);
        }
    }
    Ok(())
}

impl<R: Runtime> tauri::Assets<R> for DiskAssets {
    fn get(&self, key: &AssetKey) -> Option<Cow<'_, [u8]>> {
        let path = key.as_ref();
        let raw = self.files.get(path)?;
        if path != "/index.html" {
            return Some(Cow::Borrowed(raw.as_slice()));
        }
        let state = self.injection.lock().ok().and_then(|guard| guard.clone());
        match state {
            Some(state) => std::str::from_utf8(raw)
                .ok()
                .map(|html| Cow::Owned(inject::render_index(html, &state).into_bytes())),
            None => Some(Cow::Borrowed(raw.as_slice())),
        }
    }

    fn iter(&self) -> Box<AssetsIter<'_>> {
        Box::new(
            self.files
                .iter()
                .map(|(key, bytes)| (Cow::Borrowed(key.as_str()), Cow::Borrowed(bytes.as_slice()))),
        )
    }

    fn csp_hashes(&self, _html_path: &AssetKey) -> Box<dyn Iterator<Item = CspHash<'_>> + '_> {
        // The host configures no CSP for the embedded app in R0-RS02.
        Box::new(std::iter::empty::<CspHash<'_>>())
    }
}
