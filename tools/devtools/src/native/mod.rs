//! Native desktop cases: drive the real Tauri client, observe it through
//! agent-desktop, capture screenshots and prove the process is gone.
//!
//! Ported from `internal/acceptance/runner_*_native.go`. The client is the
//! Tauri host (`cargo build -p anvil`), not the retired Wails binary, so the
//! build step and the executable path differ; every assertion is kept.
//!
//! Failure classification matters as much as the assertions: a missing
//! accessibility permission, an untrusted DACL or a broken CLI is an
//! environment problem (BLOCKED, exit 2), while "no window", "wrong title" or a
//! screenshot that is too small is a product failure (FAIL, exit 1).

mod case_agents;
mod case_catalog;
mod case_chat;
mod case_chat_live;
mod case_connectors;
mod case_i18n;
mod case_library;
mod case_mcp;
mod case_rail_live;
mod case_schedules;
mod case_settings;
mod case_shell;
mod case_skills;
mod case_theme;
mod case_window;
mod case_workbench;
mod pages;
pub(crate) mod parse;
pub(crate) mod session;

use std::path::{Path, PathBuf};

use anvil_paths::SystemResolver;
use serde::Deserialize;
use serde::Serialize;

use crate::evidence::{Artifact, Command};
use crate::hash::hash_artifact;
use crate::plans::{NativeKind, StepSpec};
use crate::preflight::check_path;
use crate::runner::AutomationRunner;

/// Result of one native case.
#[derive(Debug, Default)]
pub(crate) struct NativeOutcome {
    pub status: String,
    pub commands: Vec<Command>,
    pub artifacts: Vec<Artifact>,
    pub limitations: Vec<String>,
}

/// Dispatch a native case by kind.
pub(crate) fn run(
    kind: NativeKind,
    runner: &AutomationRunner,
    sha: &str,
    target: &str,
) -> NativeOutcome {
    match kind {
        NativeKind::Window => case_window::run(runner, sha, target),
        NativeKind::Shell => case_shell::run(runner, sha, target),
        NativeKind::Theme => case_theme::run(runner, sha, target),
        NativeKind::I18n => case_i18n::run(runner, sha, target),
        NativeKind::Workbench => case_workbench::run(runner, sha, target),
        NativeKind::Chat => case_chat::run(runner, sha, target),
        NativeKind::ChatLive => case_chat_live::run(runner, sha, target),
        NativeKind::RailLive => case_rail_live::run(runner, sha, target),
        NativeKind::Agents => case_agents::run(runner, sha, target),
        NativeKind::Skills => case_skills::run(runner, sha, target),
        NativeKind::Mcp => case_mcp::run(runner, sha, target),
        NativeKind::Connectors => case_connectors::run(runner, sha, target),
        NativeKind::Schedules => case_schedules::run(runner, sha, target),
        NativeKind::Library => case_library::run(runner, sha, target),
        NativeKind::Settings => case_settings::run(runner, sha, target),
        NativeKind::Catalog => case_catalog::run(runner, sha, target),
    }
}

/// The short SHA used in the per-case data directory names.
pub(crate) fn short_sha(sha: &str) -> String {
    sha.chars().take(12).collect()
}

/// Reset a directory after proving the path resolves to D:.
pub(crate) fn reset_directory(path: &Path) -> Result<(), String> {
    check_path(&SystemResolver, path)?;
    match std::fs::metadata(path) {
        Ok(_) => std::fs::remove_dir_all(path).map_err(|err| err.to_string()),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(err) => Err(err.to_string()),
    }
}

/// Write a pretty-printed JSON artifact and hash it.
pub(crate) fn write_json_artifact<T: Serialize>(
    case_dir: &Path,
    relative: &str,
    value: &T,
) -> Result<Artifact, String> {
    let text = serde_json::to_string_pretty(value).map_err(|err| err.to_string())?;
    let path = case_dir.join(crate::hash::from_slash(relative));
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|err| err.to_string())?;
    }
    std::fs::write(&path, text).map_err(|err| err.to_string())?;
    hash_artifact(case_dir, relative)
}

pub(crate) fn copy_artifact(
    case_dir: &Path,
    source_rel: &str,
    dest_rel: &str,
) -> Result<Artifact, String> {
    let source = std::fs::read(case_dir.join(crate::hash::from_slash(source_rel)))
        .map_err(|err| err.to_string())?;
    let dest = case_dir.join(crate::hash::from_slash(dest_rel));
    if let Some(parent) = dest.parent() {
        std::fs::create_dir_all(parent).map_err(|err| err.to_string())?;
    }
    std::fs::write(&dest, source).map_err(|err| err.to_string())?;
    hash_artifact(case_dir, dest_rel)
}

/// Copy `<data>/logs/anvil.log` into the evidence directory.
pub(crate) fn copy_app_log(case_dir: &Path, data_dir: &Path) -> Option<Artifact> {
    let source = data_dir.join("logs").join("anvil.log");
    let body = std::fs::read(&source).ok()?;
    if body.is_empty() {
        return None;
    }
    let dest = case_dir.join("native").join("anvil.log");
    std::fs::create_dir_all(dest.parent()?).ok()?;
    std::fs::write(&dest, body).ok()?;
    hash_artifact(case_dir, "native/anvil.log").ok()
}

/// Path of the agent-desktop state directory.
pub(crate) fn agent_desktop_home(repo: &Path) -> PathBuf {
    repo.join(".tmp").join("tooling").join("agent-desktop-home")
}

/// Path validation plus the DACL normalisation of the state directory.
pub(crate) fn agent_desktop_home_steps(home: &Path) -> Vec<StepSpec> {
    let mut steps = vec![StepSpec::new(
        "path-agent-desktop-home",
        "self",
        &[
            "preflight",
            &format!("--path={}", home.display()),
            "--create",
        ],
    )];
    match agent_desktop_acl_args(home) {
        Ok(args) => {
            let refs: Vec<&str> = args.iter().map(String::as_str).collect();
            steps.push(StepSpec::new("agent-desktop-home-acl", "icacls", &refs).timeout(120));
        }
        Err(err) => steps.push(
            StepSpec::new("agent-desktop-home-acl", "icacls", &["--unavailable"])
                .timeout(1)
                .expect_exit(0)
                .expect_contains(&err),
        ),
    }
    steps
}

/// icacls arguments that strip inherited ACEs and grant the state directory only
/// to the current user, SYSTEM and Administrators. SIDs keep it locale
/// independent; `/T` is deliberately absent so container-only ACEs are not
/// applied to existing files.
pub(crate) fn agent_desktop_acl_args(home: &Path) -> Result<Vec<String>, String> {
    let user_sid = current_user_sid()?;
    Ok(vec![
        home.display().to_string(),
        "/inheritance:r".to_string(),
        "/grant:r".to_string(),
        format!("*{user_sid}:(OI)(CI)F"),
        "*S-1-5-18:(OI)(CI)F".to_string(),
        "*S-1-5-32-544:(OI)(CI)F".to_string(),
        "/Q".to_string(),
    ])
}

/// Read the current user's SID from `whoami /user /fo csv /nh`.
///
/// The SID column is language independent even when the column header is not,
/// so parsing the last quoted field avoids both FFI and localized output.
fn current_user_sid() -> Result<String, String> {
    let output = std::process::Command::new("whoami")
        .args(["/user", "/fo", "csv", "/nh"])
        .output()
        .map_err(|err| format!("whoami failed: {err}"))?;
    if !output.status.success() {
        return Err("whoami /user failed".to_string());
    }
    let text = String::from_utf8_lossy(&output.stdout);
    let sid = text
        .split("\",\"")
        .last()
        .map(|part| part.trim().trim_matches('"').trim())
        .filter(|part| part.starts_with("S-1-5-"))
        .ok_or_else(|| format!("could not read the current user SID from {text:?}"))?;
    Ok(sid.to_string())
}

#[derive(Debug, Deserialize)]
struct StatusEnvelope {
    ok: bool,
    data: StatusData,
}

#[derive(Debug, Deserialize)]
struct StatusData {
    platform: String,
    permissions: Permissions,
}

#[derive(Debug, Deserialize)]
struct Permissions {
    accessibility: Accessibility,
}

#[derive(Debug, Deserialize)]
struct Accessibility {
    state: String,
}

/// Require a usable accessibility surface before anything is launched.
pub(crate) fn check_agent_desktop_status(body: &[u8]) -> Result<(), String> {
    let envelope: StatusEnvelope = parse::parse_json(body)
        .ok_or_else(|| "parse agent-desktop status: invalid JSON".to_string())?;
    if !envelope.ok {
        return Err("agent-desktop status did not report ok".to_string());
    }
    if envelope.data.platform != "windows" {
        return Err(format!(
            "agent-desktop platform = {:?}, want windows",
            envelope.data.platform
        ));
    }
    if envelope.data.permissions.accessibility.state != "granted" {
        return Err(format!(
            "agent-desktop accessibility permission = {:?}, want granted",
            envelope.data.permissions.accessibility.state
        ));
    }
    Ok(())
}

#[derive(Debug, Deserialize)]
struct SnapshotEnvelope {
    data: SnapshotData,
}

#[derive(Debug, Deserialize)]
struct SnapshotData {
    #[serde(default)]
    complete: bool,
}

/// Whether the accessibility snapshot reported `data.complete`.
pub(crate) fn snapshot_complete(body: &[u8]) -> bool {
    parse::parse_json::<SnapshotEnvelope>(body)
        .map(|envelope| envelope.data.complete)
        .unwrap_or(false)
}

#[derive(Debug, Deserialize)]
pub(crate) struct ErrorEnvelope {
    #[serde(default)]
    error: Option<DesktopError>,
}

#[derive(Debug, Deserialize)]
struct DesktopError {
    #[serde(default)]
    code: String,
    #[serde(default)]
    message: String,
}

/// Decide whether an agent-desktop failure describes the environment
/// (BLOCKED) or the product (FAIL).
pub(crate) fn is_blocked_desktop_failure(
    command: &str,
    failure: &str,
    body: &[u8],
) -> (bool, String) {
    if command == "icacls" {
        return (true, failure.trim().to_string());
    }
    let lower = failure.to_lowercase();
    if lower.contains("look up") || lower.contains("executable file not found") {
        return (true, failure.trim().to_string());
    }
    if let Some(error) = parse::parse_json::<ErrorEnvelope>(body).and_then(|env| env.error) {
        let code = error.code.trim().to_uppercase();
        let message = error.message.trim().to_string();
        let detail = format!("{}: {message}", error.code.trim());
        if matches!(
            code.as_str(),
            "INTERNAL" | "PERM_DENIED" | "POLICY_DENIED" | "ACCESS_DENIED" | "E_ACCESSDENIED"
        ) || environment_phrase(&message)
        {
            return (true, detail);
        }
    }
    (false, String::new())
}

fn environment_phrase(message: &str) -> bool {
    let lower = message.to_lowercase();
    [
        "dacl",
        "untrusted principal",
        "access is denied",
        "permission denied",
        "permission_denied",
    ]
    .iter()
    .any(|phrase| lower.contains(phrase))
}

pub(crate) fn desktop_failure_detail(command: &str, failure: &str, body: &[u8]) -> String {
    let (_, detail) = is_blocked_desktop_failure(command, failure, body);
    if !detail.is_empty() {
        return detail;
    }
    if command == "icacls" {
        let text = String::from_utf8_lossy(body);
        let lines: Vec<&str> = text.trim().lines().collect();
        let start = lines.len().saturating_sub(5);
        let tail = lines[start..].join("\n");
        if !tail.trim().is_empty() {
            return format!("{}: {}", failure.trim(), tail.trim());
        }
    }
    failure.trim().to_string()
}

const REVERIFY_CONDITION: &str = "re-verify after repairing the reported environment condition";

pub(crate) fn with_reverify(detail: &str) -> String {
    let detail = detail.trim();
    if detail.is_empty() {
        return REVERIFY_CONDITION.to_string();
    }
    if detail.ends_with(REVERIFY_CONDITION) {
        return detail.to_string();
    }
    format!("{detail}; {REVERIFY_CONDITION}")
}

/// Whether agent-desktop reported that the app no longer exists.
pub(crate) fn app_not_found(body: &[u8]) -> bool {
    parse::parse_json::<ErrorEnvelope>(body)
        .and_then(|envelope| envelope.error)
        .map(|error| error.code.trim().eq_ignore_ascii_case("APP_NOT_FOUND"))
        .unwrap_or(false)
}

/// Whether `list-windows` reported no window for the app.
pub(crate) fn windows_gone(body: &[u8]) -> bool {
    #[derive(Deserialize)]
    struct Envelope {
        #[serde(default)]
        data: Vec<serde_json::Value>,
    }
    parse::parse_json::<Envelope>(body)
        .map(|envelope| envelope.data.is_empty())
        .unwrap_or(false)
}

/// Resolve the agent-desktop CLI: `ANVIL_AGENT_DESKTOP`, then the pinned
/// install, then PATH.
pub(crate) fn agent_desktop_executable() -> Option<PathBuf> {
    if let Ok(configured) = std::env::var("ANVIL_AGENT_DESKTOP")
        && !configured.trim().is_empty()
    {
        let path = PathBuf::from(configured);
        if path.is_file() {
            return Some(path);
        }
    }
    let pinned = PathBuf::from(
        r"C:\Users\Administrator\AppData\Local\pi-node\current\node_modules\agent-desktop\bin\agent-desktop-win32-x64.exe",
    );
    if pinned.is_file() {
        return Some(pinned);
    }
    let path = std::env::var("PATH").ok()?;
    for dir in path.split(';') {
        for name in ["agent-desktop.exe", "agent-desktop.cmd"] {
            let candidate = Path::new(dir).join(name);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_requires_windows_and_granted_accessibility() {
        let granted = br#"{"ok":true,"data":{"platform":"windows","permissions":{"accessibility":{"state":"granted"}}}}"#;
        assert!(check_agent_desktop_status(granted).is_ok());

        let denied = br#"{"ok":true,"data":{"platform":"windows","permissions":{"accessibility":{"state":"denied"}}}}"#;
        let err = check_agent_desktop_status(denied).expect_err("must reject");
        assert!(err.contains("want granted"), "{err}");

        let mac = br#"{"ok":true,"data":{"platform":"macos","permissions":{"accessibility":{"state":"granted"}}}}"#;
        assert!(check_agent_desktop_status(mac).is_err());
    }

    #[test]
    fn snapshot_completeness_is_read_from_the_envelope() {
        assert!(snapshot_complete(br#"{"data":{"complete":true}}"#));
        assert!(!snapshot_complete(br#"{"data":{"complete":false}}"#));
        assert!(!snapshot_complete(b"not json"));
    }

    #[test]
    fn environment_failures_are_blocked_instead_of_failed() {
        let acl = is_blocked_desktop_failure(
            "agent-desktop",
            "exit 1",
            br#"{"ok":false,"error":{"code":"PERM_DENIED","message":"access is denied"}}"#,
        );
        assert!(acl.0, "PERM_DENIED must be BLOCKED");

        let dacl = is_blocked_desktop_failure(
            "agent-desktop",
            "exit 1",
            br#"{"ok":false,"error":{"code":"E_UNKNOWN","message":"private file DACL grants an untrusted principal"}}"#,
        );
        assert!(dacl.0, "a DACL complaint must be BLOCKED");

        let missing = is_blocked_desktop_failure("agent-desktop", "look up \"agent-desktop\"", b"");
        assert!(missing.0);

        let product = is_blocked_desktop_failure(
            "agent-desktop",
            "window title = \"Anvil Dev\", want Anvil",
            b"",
        );
        assert!(!product.0, "a product assertion must stay FAIL");
    }

    #[test]
    fn reverify_condition_is_appended_once() {
        let once = with_reverify("boom");
        assert_eq!(once, format!("boom; {REVERIFY_CONDITION}"));
        assert_eq!(with_reverify(&once), once);
        assert_eq!(with_reverify(""), REVERIFY_CONDITION);
    }

    #[test]
    fn a_missing_app_after_close_is_not_a_failure() {
        assert!(app_not_found(
            br#"{"ok":false,"error":{"code":"APP_NOT_FOUND","message":"Application 'anvil.exe' was not found"}}"#
        ));
        assert!(!app_not_found(
            br#"{"ok":false,"error":{"code":"TIMEOUT","message":"Operation exceeded its deadline"}}"#
        ));
        assert!(!app_not_found(b"not json"));
    }

    #[test]
    fn closed_window_is_an_empty_window_list() {
        assert!(windows_gone(br#"{"ok":true,"data":[]}"#));
        assert!(!windows_gone(
            br#"{"ok":true,"data":[{"id":"w1","title":"Anvil"}]}"#
        ));
        assert!(!windows_gone(b"not json"));
    }

    #[test]
    fn acl_arguments_use_sids_not_account_names() {
        let args = agent_desktop_acl_args(Path::new(r"D:\tmp\home")).expect("acl args");
        assert_eq!(args[0], r"D:\tmp\home");
        assert!(args.iter().any(|arg| arg == "/inheritance:r"));
        assert!(args.iter().any(|arg| arg.contains(":(OI)(CI)F")));
        assert!(!args.iter().any(|arg| arg == "/T"));
    }
}
