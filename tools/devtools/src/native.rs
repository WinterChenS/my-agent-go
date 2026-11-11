//! Native desktop cases: launch the real client, observe it through
//! agent-desktop, capture a screenshot and prove the process is gone.
//!
//! Ported from `internal/acceptance/runner_native_desktop.go` +
//! `native_failure.go` + `native_acl_windows.go` (R0-RS03 phase 2). The client
//! is the Tauri host (`cargo build -p anvil`), not the retired Wails binary.
//!
//! Failure classification matters as much as the assertions: a missing
//! accessibility permission, an untrusted DACL or a broken CLI is an
//! environment problem (BLOCKED, exit 2), while "no window", "wrong title" or a
//! screenshot that is too small is a product failure (FAIL, exit 1).

use std::path::{Path, PathBuf};
use std::time::Duration;

use anvil_paths::SystemResolver;
use serde::Deserialize;

use crate::evidence::{Artifact, Command, STATUS_BLOCKED, STATUS_FAIL, STATUS_PASS};
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

impl NativeOutcome {
    fn blocked(detail: &str) -> Self {
        Self {
            status: STATUS_BLOCKED.to_string(),
            limitations: vec![with_reverify(detail)],
            ..Self::default()
        }
    }

    fn fail(detail: &str) -> Self {
        Self {
            status: STATUS_FAIL.to_string(),
            limitations: vec![detail.to_string()],
            ..Self::default()
        }
    }
}

/// Dispatch a native case by kind. Only the window case is ported so far; the
/// rest still refuse to run rather than inventing a verdict.
pub(crate) fn run(
    kind: NativeKind,
    runner: &AutomationRunner,
    sha: &str,
    target: &str,
) -> NativeOutcome {
    match kind {
        NativeKind::Window => run_window(runner, sha, target),
        other => NativeOutcome::blocked(&format!(
            "native runner for {other:?} is not ported yet (R0-RS03 phase 2, window first)"
        )),
    }
}

/// `R0-00-native-window`.
fn run_window(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut out = NativeOutcome::default();
    let case_dir = runner.case_dir().to_path_buf();
    let native_dir = case_dir.join("native");
    if let Err(err) = std::fs::create_dir_all(&native_dir) {
        return NativeOutcome::blocked(&format!("create native evidence directory: {err}"));
    }

    let short_sha: String = sha.chars().take(12).collect();
    let data_dir = runner
        .repo()
        .join(".tmp")
        .join("test")
        .join("anvil-data")
        .join(format!("{short_sha}-{target}"));
    let webview_dir = runner
        .repo()
        .join(".tmp")
        .join("test")
        .join("webview2")
        .join(format!("{short_sha}-{target}"));
    for dir in [&data_dir, &webview_dir] {
        if let Err(err) = reset_directory(dir) {
            return NativeOutcome::blocked(&format!("prepare {} on D:: {err}", dir.display()));
        }
    }

    // agent-desktop refuses a private state file whose DACL grants an untrusted
    // principal, which a fresh worktree inherits from .tmp.
    let home = runner
        .repo()
        .join(".tmp")
        .join("tooling")
        .join("agent-desktop-home");
    let mut prep_failed = false;
    for step in agent_desktop_home_steps(&home) {
        let (command, artifact, failure) = runner.run_step(&step);
        out.commands.push(command);
        if let Some(artifact) = artifact {
            out.artifacts.push(artifact);
        }
        if let Some(failure) = failure {
            prep_failed = true;
            out.status = STATUS_BLOCKED.to_string();
            out.limitations.push(with_reverify(&desktop_failure_detail(
                &step.command,
                &failure,
                &runner.read_log(&step.name),
            )));
            break;
        }
    }
    if prep_failed {
        return out;
    }

    if !run_steps(
        runner,
        &mut out,
        &[StepSpec::new("agent-desktop-status", "agent-desktop", &["status"]).timeout(60)],
        true,
    ) {
        return finish_blocked(&mut out);
    }
    if let Err(err) = check_agent_desktop_status(&runner.read_log("agent-desktop-status")) {
        out.status = STATUS_BLOCKED.to_string();
        out.limitations.push(with_reverify(&err));
        return out;
    }

    // Build the client and its frontend inside the case, so the evidence is
    // bound to this tree rather than to whatever happened to be on disk.
    if !run_steps(
        runner,
        &mut out,
        &[
            StepSpec::new("npm-build", "npm", &["run", "build"])
                .dir("frontend")
                .timeout(600),
            StepSpec::new(
                "cargo-build",
                "cargo",
                &["build", "--locked", "-p", "anvil"],
            )
            .timeout(1200),
            StepSpec::new(
                "path-data",
                "self",
                &[
                    "preflight",
                    &format!("--path={}", data_dir.display()),
                    "--create",
                ],
            ),
            StepSpec::new(
                "path-webview",
                "self",
                &[
                    "preflight",
                    &format!("--path={}", webview_dir.display()),
                    "--create",
                ],
            ),
        ],
        false,
    ) {
        if out.status == STATUS_FAIL && out.limitations.iter().any(|l| l.contains("path-data")) {
            return finish_blocked(&mut out);
        }
        return cleanup_after_failure(runner, out, &data_dir);
    }

    let exe = runner
        .repo()
        .join(".tmp")
        .join("tooling")
        .join("cargo-target")
        .join("debug")
        .join("anvil.exe");
    if !exe.is_file() {
        return NativeOutcome::fail(&format!("built client {} is missing", exe.display()));
    }
    let dist = runner.repo().join("frontend").join("dist");

    let launch = StepSpec::new(
        "launch",
        "agent-desktop",
        &[
            "launch",
            &exe.display().to_string(),
            "--no-attach",
            "--env",
            &format!("ANVIL_DATA_DIR={}", data_dir.display()),
            "--env",
            &format!("ANVIL_WEBVIEW_DATA_DIR={}", webview_dir.display()),
            "--env",
            &format!("ANVIL_FRONTEND_DIST={}", dist.display()),
            "--timeout",
            "30000",
        ],
    )
    .timeout(90)
    .expect_contains("\"title\":\"Anvil\"");
    if !run_steps(runner, &mut out, &[launch], false) {
        return cleanup_after_failure(runner, out, &data_dir);
    }

    if !run_steps(
        runner,
        &mut out,
        &[
            StepSpec::new(
                "wait-window",
                "agent-desktop",
                &[
                    "wait",
                    "--window",
                    "Anvil",
                    "--app",
                    "anvil.exe",
                    "--timeout",
                    "30000",
                ],
            )
            .timeout(60),
            StepSpec::new(
                "list-windows",
                "agent-desktop",
                &["list-windows", "--app", "anvil.exe"],
            )
            .timeout(60),
        ],
        false,
    ) {
        return cleanup_after_failure(runner, out, &data_dir);
    }

    let window = match first_window(&runner.read_log("list-windows")) {
        Ok(window) => window,
        Err(err) => {
            out.status = STATUS_FAIL.to_string();
            out.limitations.push(err);
            return cleanup_after_failure(runner, out, &data_dir);
        }
    };
    if window.title != "Anvil" {
        out.status = STATUS_FAIL.to_string();
        out.limitations
            .push(format!("window title = {:?}, want Anvil", window.title));
        return cleanup_after_failure(runner, out, &data_dir);
    }
    if let Ok(artifact) = copy_artifact(&case_dir, "logs/list-windows.log", "native/windows.json") {
        out.artifacts.push(artifact);
    }

    // WebView2 exposes its document tree only after the first accessibility
    // walk: poll the marker first, then retry the snapshot until it is complete.
    let mut marker_found = false;
    for attempt in 1..=15 {
        let name = format!("find-build-marker-{attempt:02}");
        let step = StepSpec::new(
            &name,
            "agent-desktop",
            &[
                "find",
                "--text",
                "anvil-rebuild-shell",
                "--app",
                "anvil.exe",
            ],
        )
        .timeout(60)
        .expect_contains("\"name\":\"anvil-rebuild-shell\"");
        let (command, artifact, failure) = runner.run_step(&step);
        out.commands.push(command);
        if let Some(artifact) = artifact {
            out.artifacts.push(artifact);
        }
        match failure {
            None => {
                marker_found = true;
                break;
            }
            Some(failure) => {
                let body = runner.read_log(&name);
                if let (true, detail) = is_blocked_desktop_failure("agent-desktop", &failure, &body)
                {
                    out.status = STATUS_BLOCKED.to_string();
                    out.limitations.push(with_reverify(&detail));
                    return out;
                }
                std::thread::sleep(Duration::from_millis(500));
            }
        }
    }
    if !marker_found {
        out.status = STATUS_FAIL.to_string();
        out.limitations.push(
            "frontend build marker was not found in the accessibility tree after 15 attempts"
                .to_string(),
        );
        return cleanup_after_failure(runner, out, &data_dir);
    }

    let mut complete_snapshot = false;
    for attempt in 1..=10 {
        let name = format!("snapshot-{attempt:02}");
        let step = StepSpec::new(
            &name,
            "agent-desktop",
            &[
                "snapshot",
                "--app",
                "anvil.exe",
                "--window-id",
                &window.id,
                "--timeout-ms",
                "30000",
                "--compact",
            ],
        )
        .timeout(90);
        let (command, artifact, failure) = runner.run_step(&step);
        out.commands.push(command);
        if let Some(artifact) = artifact {
            out.artifacts.push(artifact);
        }
        if let Some(failure) = failure {
            let body = runner.read_log(&name);
            if let (true, detail) = is_blocked_desktop_failure("agent-desktop", &failure, &body) {
                out.status = STATUS_BLOCKED.to_string();
                out.limitations.push(with_reverify(&detail));
                return out;
            }
            out.status = STATUS_FAIL.to_string();
            out.limitations.push(failure);
            return cleanup_after_failure(runner, out, &data_dir);
        }
        if snapshot_complete(&runner.read_log(&name)) {
            complete_snapshot = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(500));
    }
    if !complete_snapshot {
        out.status = STATUS_FAIL.to_string();
        out.limitations
            .push("accessibility snapshot did not become complete after 10 attempts".to_string());
        return cleanup_after_failure(runner, out, &data_dir);
    }

    let screenshot = native_dir.join("screenshot.png");
    let step = StepSpec::new(
        "screenshot",
        "agent-desktop",
        &[
            "screenshot",
            "--app",
            "anvil.exe",
            "--window-id",
            &window.id,
            &screenshot.display().to_string(),
        ],
    )
    .timeout(60);
    if !run_steps(runner, &mut out, &[step], false) {
        return cleanup_after_failure(runner, out, &data_dir);
    }
    match std::fs::metadata(&screenshot) {
        Ok(meta) if meta.len() >= 1024 => match hash_artifact(&case_dir, "native/screenshot.png") {
            Ok(artifact) => out.artifacts.push(artifact),
            Err(err) => {
                out.status = STATUS_FAIL.to_string();
                out.limitations.push(format!("hash screenshot: {err}"));
                return cleanup_after_failure(runner, out, &data_dir);
            }
        },
        Ok(meta) => {
            out.status = STATUS_FAIL.to_string();
            out.limitations.push(format!(
                "screenshot {} is too small ({} bytes)",
                screenshot.display(),
                meta.len()
            ));
            return cleanup_after_failure(runner, out, &data_dir);
        }
        Err(err) => {
            out.status = STATUS_FAIL.to_string();
            out.limitations.push(format!(
                "screenshot {} is missing: {err}",
                screenshot.display()
            ));
            return cleanup_after_failure(runner, out, &data_dir);
        }
    }

    // Close the client. agent-desktop's semantic terminate does not reach a
    // WebView2 host: it reports `terminate_request succeeded, verified=false`
    // and then times out, while the same window closes on WM_CLOSE in about a
    // second. Try it first (it is the documented graceful path), record the
    // outcome, and fall back to `--force` so the case still proves the client
    // can be shut down.
    let graceful =
        StepSpec::new("close-app", "agent-desktop", &["close-app", "anvil.exe"]).timeout(60);
    let (command, artifact, failure) = runner.run_step(&graceful);
    out.commands.push(command);
    if let Some(artifact) = artifact {
        out.artifacts.push(artifact);
    }
    let graceful_failed = failure.is_some();
    if graceful_failed {
        out.limitations.push(format!(
            "agent-desktop semantic close did not terminate the Tauri host ({}); closed with --force",
            failure.unwrap_or_default()
        ));
    }
    let mut closed = windows_gone(&runner.read_log("close-app"));
    if !closed {
        let force = StepSpec::new(
            "close-app-force",
            "agent-desktop",
            &["close-app", "anvil.exe", "--force"],
        )
        .timeout(60);
        let (command, artifact, failure) = runner.run_step(&force);
        out.commands.push(command);
        if let Some(artifact) = artifact {
            out.artifacts.push(artifact);
        }
        if let Some(failure) = failure {
            out.status = STATUS_FAIL.to_string();
            out.limitations.push(failure);
            return out;
        }
    }
    // Wait for the window to disappear instead of trusting the close command.
    for attempt in 1..=10 {
        let step = StepSpec::new(
            &format!("list-windows-after-close-{attempt:02}"),
            "agent-desktop",
            &["list-windows", "--app", "anvil.exe"],
        )
        .timeout(60);
        let (command, artifact, failure) = runner.run_step(&step);
        out.commands.push(command);
        if let Some(artifact) = artifact {
            out.artifacts.push(artifact);
        }
        if failure.is_some() {
            out.status = STATUS_FAIL.to_string();
            out.limitations
                .push(failure.unwrap_or_else(|| "list-windows failed".to_string()));
            return out;
        }
        if windows_gone(&runner.read_log(&format!("list-windows-after-close-{attempt:02}"))) {
            closed = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(500));
    }
    if !closed {
        out.status = STATUS_FAIL.to_string();
        out.limitations
            .push("the client window was still reported after close".to_string());
        return out;
    }

    match copy_app_log(&case_dir, &data_dir) {
        Some(artifact) => out.artifacts.push(artifact),
        None => {
            out.status = STATUS_FAIL.to_string();
            out.limitations.push(
                "application log was not produced; startup evidence is incomplete".to_string(),
            );
            return out;
        }
    }

    out.status = STATUS_PASS.to_string();
    out
}

/// Run steps in order, classifying the first failure. `environment_only` marks
/// steps whose failure is always an environment problem (agent-desktop status).
fn run_steps(
    runner: &AutomationRunner,
    out: &mut NativeOutcome,
    steps: &[StepSpec],
    environment_only: bool,
) -> bool {
    for step in steps {
        let (command, artifact, failure) = runner.run_step(step);
        out.commands.push(command);
        if let Some(artifact) = artifact {
            out.artifacts.push(artifact);
        }
        let Some(failure) = failure else { continue };
        let body = runner.read_log(&step.name);
        let (blocked, detail) = is_blocked_desktop_failure(&step.command, &failure, &body);
        if blocked || environment_only {
            out.status = STATUS_BLOCKED.to_string();
            out.limitations
                .push(with_reverify(&detail_or(&detail, &failure)));
        } else {
            out.status = STATUS_FAIL.to_string();
            out.limitations.push(failure);
        }
        return false;
    }
    true
}

fn detail_or(detail: &str, failure: &str) -> String {
    if detail.trim().is_empty() {
        failure.trim().to_string()
    } else {
        detail.trim().to_string()
    }
}

fn finish_blocked(out: &mut NativeOutcome) -> NativeOutcome {
    out.status = STATUS_BLOCKED.to_string();
    let joined = out.limitations.join("; ");
    out.limitations = vec![with_reverify(&joined)];
    std::mem::take(out)
}

/// Close any leftover client, keep the log, and return the outcome as FAIL.
fn cleanup_after_failure(
    runner: &AutomationRunner,
    mut out: NativeOutcome,
    data_dir: &Path,
) -> NativeOutcome {
    if let Some(artifact) = copy_app_log(runner.case_dir(), data_dir) {
        out.artifacts.push(artifact);
    }
    let step = StepSpec::new(
        "cleanup-close-app",
        "agent-desktop",
        &["close-app", "anvil.exe", "--force"],
    )
    .timeout(60);
    let (command, artifact, failure) = runner.run_step(&step);
    out.commands.push(command);
    if let Some(artifact) = artifact {
        out.artifacts.push(artifact);
    }
    if out.limitations.is_empty()
        && let Some(failure) = failure
    {
        out.limitations.push(failure);
    }
    out.status = STATUS_FAIL.to_string();
    out
}

fn agent_desktop_home_steps(home: &Path) -> Vec<StepSpec> {
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
fn agent_desktop_acl_args(home: &Path) -> Result<Vec<String>, String> {
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

fn reset_directory(path: &Path) -> Result<(), String> {
    check_path(&SystemResolver, path)?;
    match std::fs::metadata(path) {
        Ok(_) => std::fs::remove_dir_all(path).map_err(|err| err.to_string()),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(err) => Err(err.to_string()),
    }
}

#[derive(Debug, Deserialize)]
struct WindowsEnvelope {
    ok: bool,
    #[serde(default)]
    data: Vec<WindowEntry>,
}

#[derive(Debug, Deserialize)]
struct WindowEntry {
    id: String,
    title: String,
    #[serde(default)]
    visible: bool,
    #[serde(default)]
    accessible: bool,
    #[serde(default)]
    minimized: bool,
}

#[derive(Debug)]
struct Window {
    id: String,
    title: String,
}

fn first_window(body: &[u8]) -> Result<Window, String> {
    let json = last_json_object(body);
    let envelope: WindowsEnvelope =
        serde_json::from_str(&json).map_err(|err| format!("parse list-windows output: {err}"))?;
    if !envelope.ok {
        return Err("list-windows did not report ok".to_string());
    }
    let Some(window) = envelope.data.into_iter().next() else {
        return Err("no Anvil window was reported".to_string());
    };
    if !window.visible || window.minimized || !window.accessible {
        return Err(format!(
            "window {:?} visible={} minimized={} accessible={}; want a visible, accessible window",
            window.title, window.visible, window.minimized, window.accessible
        ));
    }
    Ok(Window {
        id: window.id,
        title: window.title,
    })
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

fn check_agent_desktop_status(body: &[u8]) -> Result<(), String> {
    let envelope: StatusEnvelope = serde_json::from_str(&last_json_object(body))
        .map_err(|err| format!("parse agent-desktop status: {err}"))?;
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

fn snapshot_complete(body: &[u8]) -> bool {
    serde_json::from_str::<SnapshotEnvelope>(&last_json_object(body))
        .map(|envelope| envelope.data.complete)
        .unwrap_or(false)
}

/// Tolerate application log lines before the JSON envelope: the launched client
/// inherits agent-desktop's stdout, so the first `{` is not always the envelope.
fn last_json_object(body: &[u8]) -> String {
    let text = String::from_utf8_lossy(body);
    let trimmed = text.trim();
    for (index, byte) in trimmed.bytes().enumerate() {
        if byte != b'{' {
            continue;
        }
        let candidate = &trimmed[index..];
        if serde_json::Deserializer::from_str(candidate)
            .into_iter::<serde_json::Value>()
            .next()
            .is_some_and(|result| result.is_ok())
        {
            return candidate.to_string();
        }
    }
    trimmed.to_string()
}

#[derive(Debug, Deserialize)]
struct ErrorEnvelope {
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
fn is_blocked_desktop_failure(command: &str, failure: &str, body: &[u8]) -> (bool, String) {
    if command == "icacls" {
        return (true, failure.trim().to_string());
    }
    let lower = failure.to_lowercase();
    if lower.contains("look up") || lower.contains("executable file not found") {
        return (true, failure.trim().to_string());
    }
    if let Ok(envelope) = serde_json::from_str::<ErrorEnvelope>(&last_json_object(body))
        && let Some(error) = envelope.error
    {
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

fn desktop_failure_detail(command: &str, failure: &str, body: &[u8]) -> String {
    let (_, detail) = is_blocked_desktop_failure(command, failure, body);
    if !detail.is_empty() {
        return detail;
    }
    if command == "icacls" {
        let text = String::from_utf8_lossy(body);
        let lines: Vec<&str> = text.trim().lines().collect();
        let tail = lines[lines.len().saturating_sub(5)..].join("\n");
        if !tail.trim().is_empty() {
            return format!("{}: {}", failure.trim(), tail.trim());
        }
    }
    failure.trim().to_string()
}

const REVERIFY_CONDITION: &str = "re-verify after repairing the reported environment condition";

fn with_reverify(detail: &str) -> String {
    let detail = detail.trim();
    if detail.is_empty() {
        return REVERIFY_CONDITION.to_string();
    }
    if detail.ends_with(REVERIFY_CONDITION) {
        return detail.to_string();
    }
    format!("{detail}; {REVERIFY_CONDITION}")
}

fn copy_artifact(case_dir: &Path, source_rel: &str, dest_rel: &str) -> Result<Artifact, String> {
    let source = std::fs::read(case_dir.join(crate::hash::from_slash(source_rel)))
        .map_err(|err| err.to_string())?;
    let dest = case_dir.join(crate::hash::from_slash(dest_rel));
    if let Some(parent) = dest.parent() {
        std::fs::create_dir_all(parent).map_err(|err| err.to_string())?;
    }
    std::fs::write(&dest, source).map_err(|err| err.to_string())?;
    hash_artifact(case_dir, dest_rel)
}

fn copy_app_log(case_dir: &Path, data_dir: &Path) -> Option<Artifact> {
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

/// Whether `list-windows` reported no window for the app.
fn windows_gone(body: &[u8]) -> bool {
    #[derive(Deserialize)]
    struct Envelope {
        #[serde(default)]
        data: Vec<serde_json::Value>,
    }
    serde_json::from_str::<Envelope>(&last_json_object(body))
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
    fn window_envelope_is_read_past_application_log_lines() {
        let body = b"2026/11/11 18:00:00 starting: host\n{\"ok\":true,\"data\":[{\"id\":\"w1\",\"title\":\"Anvil\",\"visible\":true,\"accessible\":true,\"minimized\":false}]}\n";
        let window = first_window(body).expect("window");
        assert_eq!(window.id, "w1");
        assert_eq!(window.title, "Anvil");
    }

    #[test]
    fn an_inaccessible_or_minimized_window_is_rejected() {
        let body = br#"{"ok":true,"data":[{"id":"w1","title":"Anvil","visible":true,"accessible":false,"minimized":false}]}"#;
        let err = first_window(body).expect_err("must reject");
        assert!(err.contains("accessible=false"), "{err}");
    }

    #[test]
    fn missing_window_is_a_product_failure() {
        let err = first_window(br#"{"ok":true,"data":[]}"#).expect_err("must reject");
        assert!(err.contains("no Anvil window"), "{err}");
    }

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
