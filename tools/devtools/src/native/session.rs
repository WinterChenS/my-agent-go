//! One native desktop session: the shared launch → observe → assert → close
//! sequence used by every native case.
//!
//! Ported from the `shellNative` helper set in
//! `internal/acceptance/runner_shell_native.go` and `runner_theme_robust.go`.
//! Keeping it in one place matters: the cases differ in what they assert, not
//! in how they start and stop the client.

use std::path::PathBuf;
use std::time::{Duration, Instant};

use crate::evidence::{Artifact, STATUS_BLOCKED, STATUS_FAIL, STATUS_PASS};
use crate::hash::{from_slash, hash_artifact};
use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::parse::{
    self, ElementBounds, LanguageLogEntry, ManifestViewport, ShellRoute, ThemeLogEntry,
    WindowBounds,
};
use super::{
    NativeOutcome, agent_desktop_home, agent_desktop_home_steps, app_not_found,
    check_agent_desktop_status, copy_app_log, desktop_failure_detail, is_blocked_desktop_failure,
    reset_directory, windows_gone, with_reverify,
};

/// The step timeout every desktop interaction uses.
const STEP_TIMEOUT: u64 = 60;

/// A saved status/limitations pair so a retried probe can fail without
/// poisoning the case verdict.
#[derive(Debug, Clone)]
pub(crate) struct Checkpoint {
    status: String,
    limitations: usize,
}

/// Shared state of one native case.
pub(crate) struct Session<'a> {
    pub(crate) runner: &'a AutomationRunner,
    pub(crate) out: NativeOutcome,
    pub(crate) data_dir: PathBuf,
    pub(crate) webview_dir: PathBuf,
    pub(crate) app_log: PathBuf,
    pub(crate) window_id: String,
    pub(crate) window: WindowBounds,
    pub(crate) process_instance: String,
    pub(crate) window_pid: i64,
    pub(crate) initial_url: String,
    pub(crate) last_launch_theme: ThemeLogEntry,
    pub(crate) last_launch_language: LanguageLogEntry,
}

impl<'a> Session<'a> {
    /// Build the session for `<sha[:12]>-<target>[-<suffix>]` data directories.
    pub(crate) fn new(runner: &'a AutomationRunner, sha: &str, target: &str, suffix: &str) -> Self {
        let short = super::short_sha(sha);
        let tag = if suffix.is_empty() {
            format!("{short}-{target}")
        } else {
            format!("{short}-{target}-{suffix}")
        };
        let data_dir = runner
            .repo()
            .join(".tmp")
            .join("test")
            .join("anvil-data")
            .join(&tag);
        let webview_dir = runner
            .repo()
            .join(".tmp")
            .join("test")
            .join("webview2")
            .join(&tag);
        let app_log = data_dir.join("logs").join("anvil.log");
        Self {
            runner,
            out: NativeOutcome::default(),
            data_dir,
            webview_dir,
            app_log,
            window_id: String::new(),
            window: WindowBounds::default(),
            process_instance: String::new(),
            window_pid: 0,
            initial_url: String::new(),
            last_launch_theme: ThemeLogEntry::default(),
            last_launch_language: LanguageLogEntry::default(),
        }
    }

    pub(crate) fn case_dir(&self) -> PathBuf {
        self.runner.case_dir().to_path_buf()
    }

    pub(crate) fn repo(&self) -> PathBuf {
        self.runner.repo().to_path_buf()
    }

    pub(crate) fn into_outcome(self) -> NativeOutcome {
        self.out
    }

    /// Create `<case>/native` and fail as BLOCKED when that is impossible.
    pub(crate) fn ensure_native_dir(&mut self) -> Result<(), ()> {
        match std::fs::create_dir_all(self.case_dir().join("native")) {
            Ok(()) => Ok(()),
            Err(err) => {
                self.blocked(&format!("create native evidence directory: {err}"));
                Err(())
            }
        }
    }

    /// Record a BLOCKED verdict with the re-verify condition appended.
    pub(crate) fn blocked(&mut self, detail: &str) {
        self.out.status = STATUS_BLOCKED.to_string();
        self.out.limitations.push(with_reverify(detail));
    }

    /// Record a FAIL verdict.
    pub(crate) fn fail(&mut self, detail: &str) {
        self.out.status = STATUS_FAIL.to_string();
        self.out.limitations.push(detail.to_string());
    }

    /// Collapse accumulated limitations into one re-verify note (BLOCKED).
    pub(crate) fn mark_blocked(&mut self) {
        self.out.status = STATUS_BLOCKED.to_string();
        let joined = self.out.limitations.join("; ");
        self.out.limitations = vec![with_reverify(&joined)];
    }

    pub(crate) fn checkpoint(&self) -> Checkpoint {
        Checkpoint {
            status: self.out.status.clone(),
            limitations: self.out.limitations.len(),
        }
    }

    pub(crate) fn restore(&mut self, checkpoint: &Checkpoint) {
        self.out.status = checkpoint.status.clone();
        self.out.limitations.truncate(checkpoint.limitations);
    }

    pub(crate) fn read(&self, name: &str) -> Vec<u8> {
        self.runner.read_log(name)
    }

    pub(crate) fn log_text(&self) -> String {
        std::fs::read(&self.app_log)
            .map(|body| String::from_utf8_lossy(&body).into_owned())
            .unwrap_or_default()
    }

    /// Run steps in order, classifying the first failure.
    pub(crate) fn run(&mut self, steps: &[StepSpec]) -> bool {
        for step in steps {
            let (command, artifact, failure) = self.runner.run_step(step);
            self.out.commands.push(command);
            if let Some(artifact) = artifact {
                self.out.artifacts.push(artifact);
            }
            let Some(failure) = failure else { continue };
            let body = self.read(&step.name);
            let (blocked, _detail) = is_blocked_desktop_failure(&step.command, &failure, &body);
            if blocked {
                self.out.status = STATUS_BLOCKED.to_string();
                self.out
                    .limitations
                    .push(with_reverify(&desktop_failure_detail(
                        &step.command,
                        &failure,
                        &body,
                    )));
            } else {
                self.out.status = STATUS_FAIL.to_string();
                self.out.limitations.push(failure);
            }
            return false;
        }
        true
    }

    /// Run one step without classifying it.
    ///
    /// The command and its artifact are recorded like every other step; only
    /// the verdict is left to the caller, which is what the retried probes and
    /// the tolerated close path need.
    pub(crate) fn run_raw(&mut self, step: &StepSpec) -> (Option<Artifact>, Option<String>) {
        let (command, artifact, failure) = self.runner.run_step(step);
        self.out.commands.push(command);
        if let Some(artifact) = &artifact {
            self.out.artifacts.push(artifact.clone());
        }
        (artifact, failure)
    }

    /// Prepare the agent-desktop state directory (path contract + DACL).
    pub(crate) fn prepare_home(&mut self) -> bool {
        let home = agent_desktop_home(&self.repo());
        for step in agent_desktop_home_steps(&home) {
            let (command, artifact, failure) = self.runner.run_step(&step);
            self.out.commands.push(command);
            if let Some(artifact) = artifact {
                self.out.artifacts.push(artifact);
            }
            let Some(failure) = failure else { continue };
            let body = self.read(&step.name);
            let detail = desktop_failure_detail(&step.command, &failure, &body);
            if step.command == "icacls" {
                self.blocked(&format!(
                    "agent-desktop home ACL normalisation failed: {detail}"
                ));
            } else {
                self.blocked(&format!(
                    "agent-desktop home is not writable on D:: {detail}"
                ));
            }
            return false;
        }
        true
    }

    /// Require a working, permitted accessibility surface.
    pub(crate) fn require_status(&mut self) -> bool {
        let step = StepSpec::new("agent-desktop-status", "agent-desktop", &["status"])
            .timeout(STEP_TIMEOUT);
        if !self.run(&[step]) {
            self.mark_blocked();
            return false;
        }
        match check_agent_desktop_status(&self.read("agent-desktop-status")) {
            Ok(()) => true,
            Err(err) => {
                self.blocked(&err);
                false
            }
        }
    }

    /// Build the frontend bundle and the client, inside the case.
    pub(crate) fn build_client(&mut self) -> bool {
        self.run(&[
            StepSpec::new("npm-build", "npm", &["run", "build"])
                .dir("frontend")
                .timeout(600),
            StepSpec::new(
                "cargo-build",
                "cargo",
                &["build", "--locked", "-p", "anvil"],
            )
            .timeout(1200),
        ])
    }

    /// Reset both per-case data directories on D:.
    pub(crate) fn reset_data_dirs(&mut self, what: &str) -> bool {
        for (label, dir) in [
            (
                format!("reset {what} data directory"),
                self.data_dir.clone(),
            ),
            (
                format!("reset {what} WebView data directory"),
                self.webview_dir.clone(),
            ),
        ] {
            if let Err(err) = reset_directory(&dir) {
                self.blocked(&format!("{label}: {err}"));
                return false;
            }
        }
        true
    }

    /// Verify both data directories against the D:-only contract.
    pub(crate) fn check_paths(&mut self) -> bool {
        let steps = [
            StepSpec::new(
                "path-data",
                "self",
                &[
                    "preflight",
                    &format!("--path={}", self.data_dir.display()),
                    "--create",
                ],
            ),
            StepSpec::new(
                "path-webview",
                "self",
                &[
                    "preflight",
                    &format!("--path={}", self.webview_dir.display()),
                    "--create",
                ],
            ),
        ];
        if !self.run(&steps) {
            self.mark_blocked();
            return false;
        }
        true
    }

    /// Path of the client built by `cargo build -p anvil`.
    pub(crate) fn client_exe(&self) -> PathBuf {
        self.repo()
            .join(".tmp")
            .join("tooling")
            .join("cargo-target")
            .join("debug")
            .join("anvil.exe")
    }

    pub(crate) fn dist_dir(&self) -> PathBuf {
        self.repo().join("frontend").join("dist")
    }

    /// Launch the client at `width`×`height` and adopt its window identity.
    pub(crate) fn launch_client(&mut self, width: i64, height: i64, label: &str) -> bool {
        let exe = self.client_exe();
        if !exe.is_file() {
            self.fail(&format!("built client {} is missing", exe.display()));
            return false;
        }
        if !self.ensure_no_client(&format!("{label}-preclean")) {
            return false;
        }
        let theme_before = self.theme_entries().len();
        let mut args: Vec<String> = vec![
            "launch".to_string(),
            exe.display().to_string(),
            "--no-attach".to_string(),
            "--env".to_string(),
            format!("ANVIL_DATA_DIR={}", self.data_dir.display()),
            "--env".to_string(),
            format!("ANVIL_WEBVIEW_DATA_DIR={}", self.webview_dir.display()),
            "--env".to_string(),
            format!("ANVIL_FRONTEND_DIST={}", self.dist_dir().display()),
            "--env".to_string(),
            format!("ANVIL_WINDOW_WIDTH={width}"),
            "--env".to_string(),
            format!("ANVIL_WINDOW_HEIGHT={height}"),
            "--timeout".to_string(),
            "30000".to_string(),
        ];
        if !self.initial_url.is_empty() {
            args.push("--env".to_string());
            args.push(format!("ANVIL_INITIAL_URL={}", self.initial_url));
        }
        let launch = StepSpec::owned(label, "agent-desktop", args)
            .timeout(90)
            .expect_contains("\"title\":\"Anvil\"");
        if !self.run(&[launch]) {
            return false;
        }
        let wait = StepSpec::owned(
            &format!("{label}-wait-window"),
            "agent-desktop",
            vec![
                "wait".to_string(),
                "--window".to_string(),
                "Anvil".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--timeout".to_string(),
                "30000".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        let list = StepSpec::owned(
            &format!("{label}-list-windows"),
            "agent-desktop",
            vec![
                "list-windows".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[wait, list]) {
            return false;
        }
        let body = self.read(&format!("{label}-list-windows"));
        let Some(window) = parse::parse_first_window(&body) else {
            self.fail("no Anvil window was reported after launch");
            return false;
        };
        if window.title != "Anvil"
            || !near(window.width, width as f64)
            || !near(window.height, height as f64)
        {
            self.fail(&format!(
                "window bounds = {:?} {}x{}, want Anvil {}x{}",
                window.title, window.width, window.height, width, height
            ));
            return false;
        }
        if window.process_instance.is_empty() || window.pid == 0 {
            self.fail(&format!(
                "window identity is missing (pid={} instance={:?})",
                window.pid, window.process_instance
            ));
            return false;
        }
        self.window_id = window.id.clone();
        self.window = window.clone();
        self.process_instance = window.process_instance.clone();
        self.window_pid = window.pid;
        let Some(entry) = self.wait_theme_since(theme_before, Duration::from_secs(20)) else {
            self.fail("frontend did not report a theme after launch");
            return false;
        };
        self.last_launch_theme = entry;
        true
    }

    /// Launch the client without overriding the window size.
    ///
    /// The window case asserts the host defaults instead of a requested size.
    pub(crate) fn launch_default(&mut self, label: &str) -> bool {
        let exe = self.client_exe();
        if !exe.is_file() {
            self.fail(&format!("built client {} is missing", exe.display()));
            return false;
        }
        let launch = StepSpec::owned(
            label,
            "agent-desktop",
            vec![
                "launch".to_string(),
                exe.display().to_string(),
                "--no-attach".to_string(),
                "--env".to_string(),
                format!("ANVIL_DATA_DIR={}", self.data_dir.display()),
                "--env".to_string(),
                format!("ANVIL_WEBVIEW_DATA_DIR={}", self.webview_dir.display()),
                "--env".to_string(),
                format!("ANVIL_FRONTEND_DIST={}", self.dist_dir().display()),
                "--timeout".to_string(),
                "30000".to_string(),
            ],
        )
        .timeout(90)
        .expect_contains("\"title\":\"Anvil\"");
        if !self.run(&[launch]) {
            return false;
        }
        let wait = StepSpec::owned(
            &format!("{label}-wait-window"),
            "agent-desktop",
            vec![
                "wait".to_string(),
                "--window".to_string(),
                "Anvil".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--timeout".to_string(),
                "30000".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        let list = StepSpec::owned(
            &format!("{label}-list-windows"),
            "agent-desktop",
            vec![
                "list-windows".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[wait, list]) {
            return false;
        }
        let Some(window) = parse::parse_first_window(&self.read(&format!("{label}-list-windows")))
        else {
            self.fail("no Anvil window was reported after launch");
            return false;
        };
        if window.title != "Anvil" {
            self.fail(&format!("window title = {:?}, want Anvil", window.title));
            return false;
        }
        self.window_id = window.id.clone();
        self.window = window;
        true
    }

    /// Close a stale client so the window identity always belongs to this run.
    pub(crate) fn ensure_no_client(&mut self, label: &str) -> bool {
        let list_name = format!("preclean-{label}-list");
        let list = StepSpec::owned(
            &list_name,
            "agent-desktop",
            vec![
                "list-windows".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[list]) {
            return false;
        }
        let Some(windows) = parse::parse_windows(&self.read(&list_name)) else {
            self.fail("could not parse the pre-launch window list");
            return false;
        };
        if windows.is_empty() {
            return true;
        }
        let close = StepSpec::owned(
            &format!("preclean-{label}-close"),
            "agent-desktop",
            vec![
                "close-app".to_string(),
                "anvil.exe".to_string(),
                "--force".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[close]) {
            return false;
        }
        std::thread::sleep(Duration::from_millis(300));
        self.wait_windows_gone(&format!("preclean-{label}-empty"))
            .is_ok()
    }

    /// Close the client and prove the window list is empty.
    ///
    /// agent-desktop's semantic terminate closes the Tauri host but never
    /// confirms it (`verified:false`, then TIMEOUT), so the window list is the
    /// assertion and `--force` is only used when a window is still reported.
    pub(crate) fn close_client(&mut self, label: &str) -> bool {
        let close = StepSpec::owned(
            &format!("{label}-close-app"),
            "agent-desktop",
            vec!["close-app".to_string(), "anvil.exe".to_string()],
        )
        .timeout(STEP_TIMEOUT);
        let (_, failure) = self.run_raw(&close);
        let mut closed = windows_gone(&self.read(&format!("{label}-close-app")));
        if !closed {
            match self.wait_windows_gone(&format!("{label}-after-close")) {
                Ok(gone) => closed = gone,
                Err(err) => {
                    self.fail(&err);
                    return false;
                }
            }
        }
        if !closed {
            let force = StepSpec::owned(
                &format!("{label}-close-app-force"),
                "agent-desktop",
                vec![
                    "close-app".to_string(),
                    "anvil.exe".to_string(),
                    "--force".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            let (_, failure) = self.run_raw(&force);
            let body = self.read(&format!("{label}-close-app-force"));
            if let Some(failure) = &failure
                && !app_not_found(&body)
            {
                self.fail(failure);
                return false;
            }
            match self.wait_windows_gone(&format!("{label}-after-force")) {
                Ok(gone) => closed = gone,
                Err(err) => {
                    self.fail(&err);
                    return false;
                }
            }
        }
        if let Some(failure) = failure {
            self.out.limitations.push(format!(
                "agent-desktop close-app exited without confirming shutdown ({failure}); the empty window list is the confirmation"
            ));
        }
        if !closed {
            self.fail("the client window was still reported after close");
            return false;
        }
        true
    }

    /// Poll `list-windows` until the app reports no window.
    pub(crate) fn wait_windows_gone(&mut self, log_id: &str) -> Result<bool, String> {
        for attempt in 1..=10 {
            let name = format!("{log_id}-{attempt:02}");
            let step = StepSpec::owned(
                &name,
                "agent-desktop",
                vec![
                    "list-windows".to_string(),
                    "--app".to_string(),
                    "anvil.exe".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            let (_, failure) = self.run_raw(&step);
            if let Some(failure) = failure {
                return Err(failure);
            }
            if windows_gone(&self.read(&name)) {
                return Ok(true);
            }
            std::thread::sleep(Duration::from_millis(500));
        }
        Ok(false)
    }

    /// Confirm the window still belongs to the process this case launched.
    pub(crate) fn verify_window_identity(&mut self, label: &str) -> bool {
        let name = format!("window-identity-{label}");
        let step = StepSpec::owned(
            &name,
            "agent-desktop",
            vec![
                "list-windows".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[step]) {
            return false;
        }
        let Some(windows) = parse::parse_windows(&self.read(&name)) else {
            self.fail("could not parse the window list");
            return false;
        };
        let Some(window) = windows.into_iter().next() else {
            self.fail("no Anvil window while verifying the active process");
            return false;
        };
        if window.process_instance != self.process_instance || window.pid != self.window_pid {
            self.fail(&format!(
                "window identity changed: expected pid={} instance={}, got pid={} instance={}",
                self.window_pid, self.process_instance, window.pid, window.process_instance
            ));
            return false;
        }
        self.window_id = window.id.clone();
        self.window = window;
        true
    }

    /// Wait until the accessibility tree exposes the shell navigation.
    pub(crate) fn activate(&mut self) -> bool {
        for attempt in 1..=30 {
            let name = format!("activate-{attempt:02}");
            let step = StepSpec::owned(
                &name,
                "agent-desktop",
                vec![
                    "find".to_string(),
                    "--role".to_string(),
                    "link".to_string(),
                    "--count".to_string(),
                    "--app".to_string(),
                    "anvil.exe".to_string(),
                    "--timeout-ms".to_string(),
                    "15000".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[step]) {
                return false;
            }
            if parse::parse_find_count(&self.read(&name)) >= 4 {
                return true;
            }
            std::thread::sleep(Duration::from_millis(500));
        }
        self.fail("the WebView accessibility tree did not expose the shell navigation");
        false
    }

    /// Click one shell route and prove its marker rendered.
    pub(crate) fn navigate(&mut self, route: &ShellRoute, label: &str) -> bool {
        let reference = if route.group == "tools" {
            let Some(trigger) = self.find_ref(
                &format!("tools-trigger-{label}"),
                Some("menubutton"),
                "工具与自动化",
                20,
            ) else {
                return false;
            };
            let expand = StepSpec::owned(
                &format!("expand-tools-{label}"),
                "agent-desktop",
                vec!["expand".to_string(), trigger],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[expand]) {
                return false;
            }
            self.find_ref(
                &format!("{}-{label}", route.id),
                Some("menuitem"),
                &route.nav_label,
                20,
            )
        } else {
            self.find_ref(
                &format!("{}-{label}", route.id),
                Some("link"),
                &route.nav_label,
                20,
            )
        };
        let Some(reference) = reference else {
            return false;
        };
        let click = StepSpec::owned(
            &format!("click-{}-{label}", route.id),
            "agent-desktop",
            vec!["click".to_string(), reference],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[click]) {
            return false;
        }
        if !self.wait_text(
            &format!("{}-{label}", route.id),
            &route.marker,
            route.path == "/",
            20,
        ) {
            return false;
        }
        if route.chrome == "shell" && !route.breadcrumb_parent.is_empty() {
            let crumb = format!("{} / {}", route.breadcrumb_parent, route.title);
            if !self.wait_text(&format!("{}-crumb-{label}", route.id), &crumb, false, 10) {
                return false;
            }
            // Aria-label is not enough: the visible parent static text must
            // have real bounds at this viewport (the narrow-layout assertion).
            if !self.visible_static_text(
                &format!("{}-parent-{label}", route.id),
                &route.breadcrumb_parent,
                10,
            ) {
                return false;
            }
        }
        let snapshot = StepSpec::owned(
            &format!("snapshot-{}-{label}", route.id),
            "agent-desktop",
            vec![
                "snapshot".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--window-id".to_string(),
                self.window_id.clone(),
                "--timeout-ms".to_string(),
                "30000".to_string(),
                "--compact".to_string(),
            ],
        )
        .timeout(90);
        self.run(&[snapshot])
    }

    /// Poll for a text node with the exact name.
    pub(crate) fn wait_text(
        &mut self,
        log_id: &str,
        text: &str,
        exact: bool,
        attempts: u32,
    ) -> bool {
        for attempt in 1..=attempts {
            let name = format!("marker-{log_id}-{attempt:02}");
            let step = StepSpec::owned(
                &name,
                "agent-desktop",
                vec![
                    "find".to_string(),
                    "--text".to_string(),
                    text.to_string(),
                    "--app".to_string(),
                    "anvil.exe".to_string(),
                    "--timeout-ms".to_string(),
                    "15000".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[step]) {
                return false;
            }
            let body = self.read(&name);
            let found = if exact {
                parse::parse_find_names(&body)
                    .iter()
                    .any(|candidate| candidate == text)
            } else {
                parse::parse_find_count(&body) > 0
            };
            if found {
                return true;
            }
            std::thread::sleep(Duration::from_millis(400));
        }
        self.fail(&format!(
            "route marker {text:?} did not appear after {attempts} attempts"
        ));
        false
    }

    /// Poll until the text disappears.
    pub(crate) fn wait_text_gone(&mut self, log_id: &str, text: &str, attempts: u32) -> bool {
        for attempt in 1..=attempts {
            let Some(count) = self.text_count(text, &format!("{log_id}-{attempt}")) else {
                return false;
            };
            if count == 0 {
                return true;
            }
            std::thread::sleep(Duration::from_millis(300));
        }
        self.fail(&format!("text {text:?} did not disappear after Escape"));
        false
    }

    /// Count the nodes whose text contains `text`.
    pub(crate) fn text_count(&mut self, text: &str, log_id: &str) -> Option<i64> {
        let name = format!("text-count-{log_id}");
        let step = StepSpec::owned(
            &name,
            "agent-desktop",
            vec![
                "find".to_string(),
                "--text".to_string(),
                text.to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--timeout-ms".to_string(),
                "15000".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[step]) {
            return None;
        }
        Some(parse::parse_find_count(&self.read(&name)))
    }

    /// Poll until `role`/`name` matches exactly `want` times.
    pub(crate) fn wait_role_count(
        &mut self,
        log_id: &str,
        role: &str,
        name: &str,
        want: i64,
        attempts: u32,
    ) -> bool {
        for attempt in 1..=attempts {
            let step_name = format!("role-count-{log_id}-{attempt:02}");
            let step = StepSpec::owned(
                &step_name,
                "agent-desktop",
                vec![
                    "find".to_string(),
                    "--role".to_string(),
                    role.to_string(),
                    "--name".to_string(),
                    name.to_string(),
                    "--exact".to_string(),
                    "--count".to_string(),
                    "--app".to_string(),
                    "anvil.exe".to_string(),
                    "--timeout-ms".to_string(),
                    "15000".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[step]) {
                return false;
            }
            if parse::parse_find_count(&self.read(&step_name)) == want {
                return true;
            }
            std::thread::sleep(Duration::from_millis(300));
        }
        self.fail(&format!("role {role} {name:?} count did not become {want}"));
        false
    }

    /// The first matching ref, retried while the tree settles.
    pub(crate) fn find_ref(
        &mut self,
        log_id: &str,
        role: Option<&str>,
        name: &str,
        attempts: u32,
    ) -> Option<String> {
        for attempt in 1..=attempts {
            let step_name = format!("find-{log_id}-{attempt:02}");
            let mut args = vec![
                "find".to_string(),
                "--name".to_string(),
                name.to_string(),
                "--exact".to_string(),
                "--first".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--timeout-ms".to_string(),
                "15000".to_string(),
            ];
            if let Some(role) = role {
                args.push("--role".to_string());
                args.push(role.to_string());
            }
            let step = StepSpec::owned(&step_name, "agent-desktop", args).timeout(STEP_TIMEOUT);
            if !self.run(&[step]) {
                return None;
            }
            if let Some(reference) = parse::parse_find_ref(&self.read(&step_name)) {
                return Some(reference);
            }
            std::thread::sleep(Duration::from_millis(400));
        }
        let role = role.unwrap_or("any");
        self.fail(&format!(
            "could not find {role} {name:?} in the accessibility tree after {attempts} attempts"
        ));
        None
    }

    /// The first ref whose text contains `text`.
    pub(crate) fn find_text_ref(
        &mut self,
        log_id: &str,
        text: &str,
        attempts: u32,
    ) -> Option<String> {
        for attempt in 1..=attempts {
            let step_name = format!("find-text-{log_id}-{attempt:02}");
            let step = StepSpec::owned(
                &step_name,
                "agent-desktop",
                vec![
                    "find".to_string(),
                    "--text".to_string(),
                    text.to_string(),
                    "--first".to_string(),
                    "--app".to_string(),
                    "anvil.exe".to_string(),
                    "--timeout-ms".to_string(),
                    "15000".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[step]) {
                return None;
            }
            if let Some(reference) = parse::parse_find_ref(&self.read(&step_name)) {
                return Some(reference);
            }
            std::thread::sleep(Duration::from_millis(400));
        }
        self.fail(&format!(
            "could not find text {text:?} in the accessibility tree after {attempts} attempts"
        ));
        None
    }

    /// Require a visible (non-zero bounds) static text node named `name`.
    pub(crate) fn visible_static_text(&mut self, log_id: &str, name: &str, attempts: u32) -> bool {
        for attempt in 1..=attempts {
            let step_name = format!("visible-{log_id}-{attempt:02}");
            let step = StepSpec::owned(
                &step_name,
                "agent-desktop",
                vec![
                    "find".to_string(),
                    "--text".to_string(),
                    name.to_string(),
                    "--app".to_string(),
                    "anvil.exe".to_string(),
                    "--timeout-ms".to_string(),
                    "15000".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[step]) {
                return false;
            }
            for entry in parse::parse_find_matches(&self.read(&step_name)) {
                let bounds = entry.bounds.unwrap_or_default();
                if entry.name == name
                    && entry.role == "statictext"
                    && bounds.width > 0.0
                    && bounds.height > 0.0
                {
                    return true;
                }
            }
            std::thread::sleep(Duration::from_millis(400));
        }
        self.fail(&format!(
            "visible breadcrumb parent {name:?} had no non-zero statictext bounds after {attempts} attempts"
        ));
        false
    }

    /// Whether the marker is present right now (no failure recorded).
    pub(crate) fn marker_present(&mut self, log_id: &str, text: &str, attempts: u32) -> bool {
        for attempt in 1..=attempts {
            let Some(count) = self.text_count(text, &format!("{log_id}-{attempt}")) else {
                return false;
            };
            if count > 0 {
                return true;
            }
            std::thread::sleep(Duration::from_millis(400));
        }
        false
    }

    /// Focus the page and inject the browser history shortcut.
    pub(crate) fn go_back(&mut self, label: &str, marker: &str) -> bool {
        let x = (self.window.x + self.window.width * 0.6) as i64;
        let y = (self.window.y + self.window.height * 0.6) as i64;
        for attempt in 1..=3 {
            let focus_window = StepSpec::owned(
                &format!("focus-window-back-{label}-{attempt:02}"),
                "agent-desktop",
                vec![
                    "focus-window".to_string(),
                    "--window-id".to_string(),
                    self.window_id.clone(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[focus_window]) {
                return false;
            }
            let focus_page = StepSpec::owned(
                &format!("focus-page-{label}-{attempt:02}"),
                "agent-desktop",
                vec![
                    "--headed".to_string(),
                    "mouse-click".to_string(),
                    "--xy".to_string(),
                    format!("{x},{y}"),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[focus_page]) {
                return false;
            }
            let back = StepSpec::owned(
                &format!("history-back-{label}-{attempt:02}"),
                "self",
                vec![
                    "key".to_string(),
                    "--title=Anvil".to_string(),
                    "--combo=alt+left".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[back]) {
                return false;
            }
            if self.marker_present(&format!("back-{label}-{attempt:02}"), marker, 6) {
                return true;
            }
        }
        self.fail(&format!(
            "history back to {marker:?} did not take effect after 3 focus+Alt+Left attempts"
        ));
        false
    }

    /// Inject a key combo into the client window.
    pub(crate) fn send_key(&mut self, log_id: &str, combo: &str) -> bool {
        let step = StepSpec::owned(
            log_id,
            "self",
            vec![
                "key".to_string(),
                "--title=Anvil".to_string(),
                format!("--combo={combo}"),
            ],
        )
        .timeout(STEP_TIMEOUT);
        self.run(&[step])
    }

    /// Capture a screenshot and hash it.
    pub(crate) fn screenshot(&mut self, rel: &str) -> bool {
        let path = self.case_dir().join(from_slash(rel));
        if let Some(parent) = path.parent()
            && let Err(err) = std::fs::create_dir_all(parent)
        {
            self.fail(&format!("create screenshot directory: {err}"));
            return false;
        }
        let step = StepSpec::owned(
            &format!("screenshot-{}", rel.replace('/', "-")),
            "agent-desktop",
            vec![
                "screenshot".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--window-id".to_string(),
                self.window_id.clone(),
                path.display().to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[step]) {
            return false;
        }
        match std::fs::metadata(&path) {
            Ok(meta) if meta.len() >= 1024 => {}
            Ok(meta) => {
                self.fail(&format!(
                    "screenshot {rel:?} is too small ({} bytes)",
                    meta.len()
                ));
                return false;
            }
            Err(err) => {
                self.fail(&format!("screenshot {rel:?} is missing: {err}"));
                return false;
            }
        }
        match hash_artifact(&self.case_dir(), rel) {
            Ok(artifact) => {
                self.out.artifacts.push(artifact);
                true
            }
            Err(err) => {
                self.fail(&format!("hash screenshot {rel:?}: {err}"));
                false
            }
        }
    }

    /// Resize the window and confirm the reported bounds.
    pub(crate) fn resize(&mut self, width: i64, height: i64, strict: bool) -> bool {
        let name = format!("resize-{width}x{height}");
        let resize = StepSpec::owned(
            &name,
            "agent-desktop",
            vec![
                "resize-window".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--window-id".to_string(),
                self.window_id.clone(),
                "--width".to_string(),
                width.to_string(),
                "--height".to_string(),
                height.to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        let (_, failure) = self.run_raw(&resize);
        if let Some(failure) = failure {
            let body = self.read(&name);
            if resize_landed_after_timeout(&body) {
                self.out.limitations.push(format!(
                    "resize-window {width}x{height} reported TIMEOUT after applying; bounds and layout probe confirm the resize"
                ));
            } else {
                let (blocked, detail) =
                    is_blocked_desktop_failure("agent-desktop", &failure, &body);
                if blocked {
                    self.out.status = STATUS_BLOCKED.to_string();
                    self.out.limitations.push(with_reverify(&detail));
                } else {
                    self.out.status = STATUS_FAIL.to_string();
                    self.out.limitations.push(failure);
                }
                return false;
            }
        }
        let list_name = format!("list-after-resize-{width}x{height}");
        let list = StepSpec::owned(
            &list_name,
            "agent-desktop",
            vec![
                "list-windows".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[list]) {
            return false;
        }
        let window = parse::parse_first_window(&self.read(&list_name)).unwrap_or_default();
        let width_ok = near(window.width, width as f64);
        let height_ok = near(window.height, height as f64);
        if !width_ok || (strict && !height_ok) {
            self.fail(&format!(
                "resize to {width}x{height} reported bounds {}x{}",
                window.width, window.height
            ));
            return false;
        }
        if !window.id.is_empty() {
            self.window = window;
        }
        true
    }

    /// Wait until the application log has more than `after` layout probes.
    pub(crate) fn wait_probe(&mut self, after: usize, timeout: Duration) -> usize {
        let deadline = Instant::now() + timeout;
        loop {
            let count = parse::parse_layout_probes(&self.log_text()).len();
            if count > after {
                return count;
            }
            if Instant::now() >= deadline {
                return after;
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }

    /// Theme entries recorded so far.
    pub(crate) fn theme_entries(&self) -> Vec<ThemeLogEntry> {
        parse::parse_theme_logs(&self.log_text())
    }

    pub(crate) fn latest_theme(&self) -> Option<ThemeLogEntry> {
        parse::latest_theme_log(&self.log_text())
    }

    /// Wait for a theme entry newer than `after`.
    pub(crate) fn wait_theme_since(
        &mut self,
        after: usize,
        timeout: Duration,
    ) -> Option<ThemeLogEntry> {
        let deadline = Instant::now() + timeout;
        loop {
            let entries = self.theme_entries();
            if entries.len() > after {
                return entries.last().cloned();
            }
            if Instant::now() >= deadline {
                return None;
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }

    /// Wait until the latest theme entry matches `mode` (+ `resolved` if given).
    pub(crate) fn wait_theme_for(
        &mut self,
        mode: &str,
        resolved: &str,
        timeout: Duration,
    ) -> Option<ThemeLogEntry> {
        let deadline = Instant::now() + timeout;
        loop {
            if let Some(last) = self.latest_theme()
                && last.mode == mode
                && (resolved.is_empty() || last.resolved == resolved)
            {
                return Some(last);
            }
            if Instant::now() >= deadline {
                return None;
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }

    /// Language entries recorded so far.
    pub(crate) fn i18n_entries(&self) -> Vec<LanguageLogEntry> {
        parse::parse_i18n_logs(&self.log_text())
    }

    /// Wait until the latest language entry is `expected`.
    pub(crate) fn wait_language(
        &mut self,
        expected: &str,
        timeout: Duration,
    ) -> Option<LanguageLogEntry> {
        let deadline = Instant::now() + timeout;
        loop {
            if let Some(last) = self.i18n_entries().last()
                && last.language == expected
            {
                return Some(last.clone());
            }
            if Instant::now() >= deadline {
                return None;
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }

    /// Wait for a language entry newer than `after` that equals `expected`.
    pub(crate) fn wait_language_since(
        &mut self,
        after: usize,
        expected: &str,
        timeout: Duration,
    ) -> Option<LanguageLogEntry> {
        let deadline = Instant::now() + timeout;
        loop {
            let entries = self.i18n_entries();
            if entries.len() > after
                && let Some(last) = entries.last()
                && last.language == expected
            {
                return Some(last.clone());
            }
            if Instant::now() >= deadline {
                return None;
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }

    /// Wait until the document title reported by the frontend is `expected`.
    pub(crate) fn wait_title(&mut self, expected: &str, timeout: Duration) -> bool {
        let deadline = Instant::now() + timeout;
        loop {
            if parse::parse_title_log(&self.log_text()).as_deref() == Some(expected) {
                return true;
            }
            if Instant::now() >= deadline {
                return false;
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }

    /// Bounds of one element.
    pub(crate) fn element_bounds(
        &mut self,
        log_id: &str,
        reference: &str,
    ) -> Option<ElementBounds> {
        let name = format!("bounds-{log_id}");
        let step = StepSpec::owned(
            &name,
            "agent-desktop",
            vec![
                "get".to_string(),
                reference.to_string(),
                "--property".to_string(),
                "bounds".to_string(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[step]) {
            return None;
        }
        let bounds = parse::parse_element_bounds(&self.read(&name));
        if bounds.is_none() {
            self.fail("element had no usable bounds");
        }
        bounds
    }

    /// Click the n-th radio button of the settings page.
    pub(crate) fn find_radio(&mut self, log_id: &str, index: usize) -> Option<String> {
        for attempt in 1..=20 {
            let name = format!("find-radio-{log_id}-{attempt:02}");
            let step = StepSpec::owned(
                &name,
                "agent-desktop",
                vec![
                    "find".to_string(),
                    "--role".to_string(),
                    "radiobutton".to_string(),
                    "--app".to_string(),
                    "anvil.exe".to_string(),
                    "--timeout-ms".to_string(),
                    "15000".to_string(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if !self.run(&[step]) {
                return None;
            }
            let matches = parse::parse_find_matches(&self.read(&name));
            if matches.len() >= 3 {
                let entry = matches.get(index)?;
                if entry.ref_id.is_empty() {
                    return None;
                }
                return Some(entry.ref_id.clone());
            }
            std::thread::sleep(Duration::from_millis(400));
        }
        self.fail("the settings radiogroup did not expose its options");
        None
    }

    /// Focus the window and click at a rectangle centre.
    pub(crate) fn click_at(&mut self, log_id: &str, bounds: ElementBounds) -> bool {
        let focus = StepSpec::owned(
            &format!("focus-{log_id}"),
            "agent-desktop",
            vec![
                "focus-window".to_string(),
                "--window-id".to_string(),
                self.window_id.clone(),
            ],
        )
        .timeout(STEP_TIMEOUT);
        if !self.run(&[focus]) {
            return false;
        }
        let (x, y) = bounds.center();
        let click = StepSpec::owned(
            &format!("click-{log_id}"),
            "agent-desktop",
            vec![
                "--headed".to_string(),
                "mouse-click".to_string(),
                "--xy".to_string(),
                format!("{x},{y}"),
            ],
        )
        .timeout(STEP_TIMEOUT);
        self.run(&[click])
    }

    /// Click one theme option and wait for the matching log entry.
    pub(crate) fn click_theme_option(
        &mut self,
        index: usize,
        log_id: &str,
        expected_mode: &str,
    ) -> Option<ThemeLogEntry> {
        for attempt in 1..=2 {
            let attempt_id = format!("{log_id}-{attempt:02}");
            let checkpoint = self.checkpoint();
            if self.verify_window_identity(&attempt_id)
                && let Some(entry) = self.click_theme_once(index, &attempt_id, expected_mode)
            {
                return Some(entry);
            }
            if self.out.status == STATUS_BLOCKED {
                return None;
            }
            self.restore(&checkpoint);
            std::thread::sleep(Duration::from_millis(300));
        }
        let detail = self
            .latest_theme()
            .map(|entry| {
                format!(
                    "last theme log mode={} resolved={}",
                    entry.mode, entry.resolved
                )
            })
            .unwrap_or_else(|| "no theme log".to_string());
        self.fail(&format!(
            "theme option {index} did not produce mode={expected_mode} after 2 attempts ({detail})"
        ));
        None
    }

    fn click_theme_once(
        &mut self,
        index: usize,
        log_id: &str,
        expected_mode: &str,
    ) -> Option<ThemeLogEntry> {
        let reference = self.find_radio(log_id, index)?;
        let bounds = self.element_bounds(log_id, &reference)?;
        if !self.click_at(&format!("theme-option-{log_id}"), bounds) {
            return None;
        }
        let resolved = if expected_mode == "system" {
            ""
        } else {
            expected_mode
        };
        let deadline = Instant::now() + Duration::from_secs(10);
        loop {
            if let Some(last) = self.latest_theme()
                && last.mode == expected_mode
                && (resolved.is_empty() || last.resolved == resolved)
            {
                return Some(last);
            }
            if Instant::now() >= deadline {
                return None;
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }

    /// Click one language option and wait for the language log.
    pub(crate) fn click_language_option(
        &mut self,
        index: usize,
        log_id: &str,
        expected_language: &str,
    ) -> bool {
        let Some(reference) = self.find_radio(log_id, index) else {
            return false;
        };
        let Some(bounds) = self.element_bounds(log_id, &reference) else {
            return false;
        };
        if !self.click_at(&format!("language-option-{log_id}"), bounds) {
            return false;
        }
        if self
            .wait_language(expected_language, Duration::from_secs(10))
            .is_some()
        {
            return true;
        }
        self.fail(&format!(
            "language option {index} did not switch to {expected_language}"
        ));
        false
    }

    /// Click by physical coordinates, falling back to a semantic click.
    pub(crate) fn click_ref_physical(&mut self, reference: &str, log_id: &str) -> bool {
        let Some(bounds) = self.element_bounds(log_id, reference) else {
            return false;
        };
        let checkpoint = self.checkpoint();
        if !self.focus_window_retry(log_id) {
            self.restore(&checkpoint);
            let click = StepSpec::owned(
                &format!("semantic-click-{log_id}"),
                "agent-desktop",
                vec!["click".to_string(), reference.to_string()],
            )
            .timeout(STEP_TIMEOUT);
            return self.run(&[click]);
        }
        let (x, y) = bounds.center();
        let click = StepSpec::owned(
            &format!("physical-click-{log_id}"),
            "agent-desktop",
            vec![
                "--headed".to_string(),
                "mouse-click".to_string(),
                "--xy".to_string(),
                format!("{x},{y}"),
            ],
        )
        .timeout(STEP_TIMEOUT);
        self.run(&[click])
    }

    /// Focus the client window, retried while the foreground settles.
    pub(crate) fn focus_window_retry(&mut self, label: &str) -> bool {
        for attempt in 1..=3 {
            let checkpoint = self.checkpoint();
            let step = StepSpec::owned(
                &format!("focus-{label}-{attempt:02}"),
                "agent-desktop",
                vec![
                    "focus-window".to_string(),
                    "--window-id".to_string(),
                    self.window_id.clone(),
                ],
            )
            .timeout(STEP_TIMEOUT);
            if self.run(&[step]) {
                return true;
            }
            if self.out.status == STATUS_BLOCKED {
                return false;
            }
            self.restore(&checkpoint);
            std::thread::sleep(Duration::from_millis(250));
        }
        self.fail("could not focus the Anvil window after 3 attempts");
        false
    }

    /// Poll `is <ref> --property focused` until it is true.
    pub(crate) fn wait_focused(&mut self, reference: &str, label: &str) -> bool {
        for attempt in 1..=10 {
            let checkpoint = self.checkpoint();
            let step = StepSpec::owned(
                &format!("focus-return-{label}-{attempt:02}"),
                "agent-desktop",
                vec![
                    "is".to_string(),
                    reference.to_string(),
                    "--property".to_string(),
                    "focused".to_string(),
                ],
            )
            .timeout(30)
            .expect_contains("\"result\":true");
            if self.run(&[step]) {
                return true;
            }
            if self.out.status == STATUS_BLOCKED {
                return false;
            }
            self.restore(&checkpoint);
            std::thread::sleep(Duration::from_millis(200));
        }
        self.fail("Escape closed the palette but focus did not return to the trigger");
        false
    }

    /// Poll `is <ref> --property expanded` until it equals `want`.
    pub(crate) fn wait_expanded(&mut self, reference: &str, want: bool, label: &str) -> bool {
        for attempt in 1..=10 {
            let checkpoint = self.checkpoint();
            let step = StepSpec::owned(
                &format!("expanded-{label}-{attempt:02}"),
                "agent-desktop",
                vec![
                    "is".to_string(),
                    reference.to_string(),
                    "--property".to_string(),
                    "expanded".to_string(),
                ],
            )
            .timeout(30)
            .expect_contains(&format!("\"result\":{want}"));
            if self.run(&[step]) {
                return true;
            }
            if self.out.status == STATUS_BLOCKED {
                return false;
            }
            self.restore(&checkpoint);
            std::thread::sleep(Duration::from_millis(200));
        }
        self.fail(&format!("element did not report expanded={want}"));
        false
    }

    /// The sample-data banner must appear in sample mode and only there.
    pub(crate) fn assert_sample_banner(&mut self, expected: bool) -> bool {
        let Some(count) = self.text_count("原型样例数据", "sample-banner-check") else {
            return false;
        };
        if expected && count == 0 {
            self.fail("sample banner was not rendered");
            return false;
        }
        if !expected && count != 0 {
            self.fail("sample banner appeared in the normal entry");
            return false;
        }
        true
    }

    /// Copy the application log into the evidence directory.
    pub(crate) fn collect_app_log(&mut self) -> bool {
        match copy_app_log(&self.case_dir(), &self.data_dir) {
            Some(artifact) => {
                self.out.artifacts.push(artifact);
                true
            }
            None => {
                self.fail("application log was not produced; evidence is incomplete");
                false
            }
        }
    }

    /// Close a leftover client after a product failure and keep the log.
    pub(crate) fn fail_cleanup(self) -> NativeOutcome {
        let mut out = self.out;
        if out.status != STATUS_FAIL {
            return out;
        }
        let case_dir = self.runner.case_dir().to_path_buf();
        if let Some(artifact) = copy_app_log(&case_dir, &self.data_dir) {
            out.artifacts.push(artifact);
        }
        let step = StepSpec::new(
            "cleanup-close-app",
            "agent-desktop",
            &["close-app", "anvil.exe", "--force"],
        )
        .timeout(STEP_TIMEOUT);
        let (command, artifact, failure) = self.runner.run_step(&step);
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

    /// Finish as PASS when nothing else was recorded.
    pub(crate) fn pass(mut self) -> NativeOutcome {
        if self.out.status.is_empty() {
            self.out.status = STATUS_PASS.to_string();
        }
        self.out
    }

    /// Resize to every viewport of the prototype manifest.
    pub(crate) fn probe_manifest_viewports(
        &mut self,
        viewports: &[ManifestViewport],
        probes: &mut usize,
    ) -> bool {
        for viewport in viewports {
            if !self.resize(viewport.width, viewport.height, false) {
                return false;
            }
            let next = self.wait_probe(*probes, Duration::from_secs(10));
            if next <= *probes {
                self.fail(&format!(
                    "layout probe missing at {}x{}",
                    viewport.width, viewport.height
                ));
                return false;
            }
            *probes = next;
        }
        true
    }

    /// Confirm the log holds enough distinct, non-overflowing probes.
    pub(crate) fn assert_probes(&mut self, minimum: usize, distinct: usize) -> bool {
        let probes = parse::parse_layout_probes(&self.log_text());
        if probes.len() < minimum {
            self.fail(&format!(
                "only {} layout probes recorded, want at least {minimum}",
                probes.len()
            ));
            return false;
        }
        let mut unique: Vec<String> = Vec::new();
        for probe in &probes {
            if probe.overflow {
                self.fail(&format!(
                    "horizontal overflow at viewport {}: scrollWidth={} clientWidth={}",
                    probe.viewport, probe.scroll_width, probe.client_width
                ));
                return false;
            }
            if !unique.contains(&probe.viewport) {
                unique.push(probe.viewport.clone());
            }
        }
        if unique.len() < distinct {
            self.fail(&format!(
                "only {} distinct viewports probed, want at least {distinct}",
                unique.len()
            ));
            return false;
        }
        true
    }
}

/// `agent-desktop` reports a delivered-but-unverified resize as TIMEOUT.
fn resize_landed_after_timeout(body: &[u8]) -> bool {
    #[derive(Default, serde::Deserialize)]
    struct Disposition {
        #[serde(default)]
        delivery: String,
    }
    #[derive(serde::Deserialize)]
    struct Error {
        #[serde(default)]
        code: String,
        #[serde(default)]
        disposition: Disposition,
    }
    #[derive(serde::Deserialize)]
    struct Envelope {
        #[serde(default)]
        error: Option<Error>,
        #[serde(default)]
        disposition: Disposition,
    }
    let Some(envelope) = parse::parse_json::<Envelope>(body) else {
        return false;
    };
    let Some(error) = envelope.error else {
        return false;
    };
    let delivery = if envelope.disposition.delivery.is_empty() {
        error.disposition.delivery
    } else {
        envelope.disposition.delivery
    };
    error.code == "TIMEOUT" && delivery == "delivered_unverified"
}

/// `true` when the window dimension is within 8 px of the request.
pub(crate) fn near(actual: f64, want: f64) -> bool {
    (actual - want).abs() <= 8.0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_resize_timeout_with_delivered_disposition_is_accepted() {
        assert!(resize_landed_after_timeout(
            br#"{"ok":false,"error":{"code":"TIMEOUT","disposition":{"delivery":"delivered_unverified"}}}"#
        ));
        assert!(!resize_landed_after_timeout(
            br#"{"ok":false,"error":{"code":"TIMEOUT","disposition":{"delivery":"not_delivered"}}}"#
        ));
        assert!(!resize_landed_after_timeout(
            br#"{"ok":false,"error":{"code":"INTERNAL","disposition":{"delivery":"delivered_unverified"}}}"#
        ));
        assert!(!resize_landed_after_timeout(b"not json"));
    }

    #[test]
    fn window_dimensions_tolerate_eight_pixels() {
        assert!(near(390.0, 390.0));
        assert!(near(398.0, 390.0));
        assert!(!near(399.0, 390.0));
    }
}
