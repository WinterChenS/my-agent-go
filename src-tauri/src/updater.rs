//! Checking for and installing a newer build of Anvil itself.
//!
//! The endpoint is the GitHub release the CI already publishes: `latest.json`
//! lists, per platform, the bundle to fetch and the minisign signature of it.
//! `tauri-plugin-updater` verifies that signature before anything is written,
//! which is why this feature needed a signing key before it could be more than
//! a button that downloads a file nobody can install.
//!
//! Two things differ per platform and are reported rather than hidden:
//!
//! * **Can this build replace itself?** Windows installs the NSIS bundle and
//!   restarts; macOS replaces the `.app`; Linux can only replace an AppImage it
//!   is running from. A `.deb` install has no such trick, so the page offers the
//!   download instead of a button that would fail.
//! * **Where does the restart come from?** On Windows the installer takes over
//!   and the process exits on its own; everywhere else the install returns and
//!   the app restarts itself.

use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use serde::Serialize;
use tauri::{AppHandle, Emitter};
use tauri_plugin_updater::UpdaterExt;

/// Streamed while an update downloads and installs.
pub const UPDATE_EVENT: &str = "anvil://app-update";

/// Where a build that cannot replace itself can send the reader.
pub const RELEASES_URL: &str = "https://github.com/WinterChenS/my-agent-go/releases/latest";

/// How often download progress reaches the page.
///
/// The updater calls back per network chunk; at a few MB/s that is hundreds of
/// events a second, and every one of them is an IPC round trip plus a render.
const PROGRESS_INTERVAL: Duration = Duration::from_millis(150);

/// A newer build than the one running.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateInfo {
    pub version: String,
    /// Release notes, as the release body wrote them.
    pub notes: Option<String>,
    /// Publish date, when the endpoint announced one.
    pub date: Option<String>,
    /// The platform key the endpoint matched, e.g. `windows-x86_64`.
    pub target: String,
    /// Size of the bundle, when the server announced a length.
    pub size: Option<u64>,
}

/// The answer to "is there something newer".
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateCheck {
    /// The version of the build answering the question.
    pub current: String,
    /// Null when this is already the newest build.
    pub available: Option<UpdateInfo>,
    /// Whether this build can download and install the update itself.
    pub installable: bool,
    /// Why it cannot, when it cannot: `package`, `development` or `platform`.
    pub reason: Option<String>,
    /// Every build can at least point at the release page.
    pub release_url: String,
}

/// One step of the download / install the page can render.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateProgress {
    /// `downloading`, `installing` or `done`.
    pub phase: &'static str,
    pub received: u64,
    pub total: Option<u64>,
    /// Bytes per second, once there is a second measurement to divide by.
    pub rate: Option<u64>,
}

/// Whether this build can replace itself, and why not when it cannot.
///
/// A `.deb` install is a real case, not a corner: on Linux the bundle the
/// updater can swap is the AppImage, and only a process started from one knows
/// where its own file is (`APPIMAGE`). Reporting this up front is what keeps
/// the page from offering a button that can only fail.
fn self_install() -> (bool, Option<String>) {
    if cfg!(debug_assertions) {
        // A `cargo build` binary is not a bundle, and the updater replaces
        // bundles. Say so instead of writing over the developer's target dir.
        return (false, Some("development".to_string()));
    }
    #[cfg(target_os = "linux")]
    {
        if std::env::var_os("APPIMAGE").is_some() {
            return (true, None);
        }
        return (false, Some("package".to_string()));
    }
    #[cfg(not(target_os = "linux"))]
    {
        (true, None)
    }
}

fn current_version(app: &AppHandle) -> String {
    app.package_info().version.to_string()
}

/// Look for a newer build without downloading it.
#[tauri::command]
pub async fn app_update_check(app: AppHandle) -> Result<UpdateCheck, String> {
    let (installable, reason) = self_install();
    let current = current_version(&app);
    let check = async {
        let updater = app
            .updater()
            .map_err(|error| format!("updater is not configured: {error}"))?;
        updater
            .check()
            .await
            .map_err(|error| format!("could not read the update endpoint: {error}"))
    };
    let available = match check.await {
        Ok(Some(update)) => Some(UpdateInfo {
            version: update.version.clone(),
            notes: update.body.clone(),
            date: update.date.map(|date| date.to_string()),
            target: update.target.clone(),
            size: None,
        }),
        Ok(None) => None,
        // No network is not the same as "up to date", and a page that says
        // "newest version" when the check failed is a page that lies.
        Err(error) => return Err(error),
    };
    Ok(UpdateCheck {
        current,
        available,
        installable,
        reason,
        release_url: RELEASES_URL.to_string(),
    })
}

/// Download the newer build, verify it, install it, and restart into it.
///
/// The version is checked again here rather than reused from
/// [`app_update_check`]: the download is the step that must be right, and a
/// minute-old answer is a minute of releases that may have moved on. The cost is
/// one request.
#[tauri::command]
pub async fn app_update_install(app: AppHandle) -> Result<(), String> {
    let (installable, reason) = self_install();
    if !installable {
        return Err(match reason.as_deref() {
            Some("package") => {
                "this build was installed as a system package; download the new release instead"
                    .to_string()
            }
            Some("development") => "a development build cannot update itself".to_string(),
            _ => "this build cannot update itself".to_string(),
        });
    }
    let updater = app
        .updater()
        .map_err(|error| format!("updater is not configured: {error}"))?;
    let update = updater
        .check()
        .await
        .map_err(|error| format!("could not read the update endpoint: {error}"))?
        .ok_or_else(|| "this is already the newest build".to_string())?;

    let started = Instant::now();
    let received = Arc::new(AtomicU64::new(0));
    let last_emit = Arc::new(AtomicU64::new(0));
    let handle = app.clone();
    let counter = received.clone();
    let throttle = last_emit.clone();
    let total_seen = Arc::new(AtomicU64::new(0));
    let total_for_cb = total_seen.clone();

    let bytes = update
        .download(
            move |chunk, total| {
                let so_far = counter.fetch_add(chunk as u64, Ordering::Relaxed) + chunk as u64;
                if let Some(total) = total {
                    total_for_cb.store(total, Ordering::Relaxed);
                }
                // Progress is for the reader, not an audit trail: one update per
                // `PROGRESS_INTERVAL` is enough to animate a bar.
                let now = started.elapsed().as_millis() as u64;
                let previous = throttle.load(Ordering::Relaxed);
                if now.saturating_sub(previous) < PROGRESS_INTERVAL.as_millis() as u64 {
                    return;
                }
                throttle.store(now, Ordering::Relaxed);
                let seconds = started.elapsed().as_secs_f64().max(0.001);
                let total = total_for_cb.load(Ordering::Relaxed);
                let _ = handle.emit(
                    UPDATE_EVENT,
                    UpdateProgress {
                        phase: "downloading",
                        received: so_far,
                        total: (total > 0).then_some(total),
                        rate: Some((so_far as f64 / seconds) as u64),
                    },
                );
            },
            || {},
        )
        .await
        .map_err(|error| format!("could not download the update: {error}"))?;

    let size = bytes.len() as u64;
    let _ = app.emit(
        UPDATE_EVENT,
        UpdateProgress {
            phase: "installing",
            received: size,
            total: Some(size),
            rate: None,
        },
    );

    // On Windows this hands the bundle to the installer and exits; the installer
    // starts the new build. On macOS and Linux it returns, and the restart is
    // ours to do.
    update
        .install(bytes)
        .map_err(|error| format!("could not install the update: {error}"))?;

    let _ = app.emit(
        UPDATE_EVENT,
        UpdateProgress {
            phase: "done",
            received: size,
            total: Some(size),
            rate: None,
        },
    );
    app.restart();
}
