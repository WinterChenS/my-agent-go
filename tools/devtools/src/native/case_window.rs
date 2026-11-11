//! `R0-00-native-window`: the client starts, exposes its window, is captured
//! and shuts down again.
//!
//! This is the smallest native case: it proves the host plumbing (D: data
//! directories, window title, accessibility surface) before the per-page cases
//! assert UI content.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::parse;
use super::session::Session;

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "");
    if session.ensure_native_dir().is_err() {
        return session.into_outcome();
    }
    if !session.reset_data_dirs("window") {
        return session.into_outcome();
    }
    if !session.prepare_home() {
        return session.into_outcome();
    }
    if !session.require_status() {
        return session.into_outcome();
    }
    if !session.build_client() {
        return session.fail_cleanup();
    }
    if !session.check_paths() {
        return session.fail_cleanup();
    }
    if !session.launch_default("launch") {
        return session.fail_cleanup();
    }
    if session.window.id.is_empty() {
        session.fail("launch did not record the window identity");
        return session.fail_cleanup();
    }
    let case_dir = session.case_dir();
    if let Ok(artifact) = super::copy_artifact(
        &case_dir,
        "logs/launch-list-windows.log",
        "native/windows.json",
    ) {
        session.out.artifacts.push(artifact);
    }

    // WebView2 exposes its document tree only after the first accessibility
    // walk: poll the marker first, then retry the snapshot until it is complete.
    let mut marker_found = false;
    for attempt in 1..=15 {
        let name = format!("find-build-marker-{attempt:02}");
        let step = StepSpec::owned(
            &name,
            "agent-desktop",
            vec![
                "find".to_string(),
                "--text".to_string(),
                "anvil-rebuild-shell".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
            ],
        )
        .timeout(60)
        .expect_contains("\"name\":\"anvil-rebuild-shell\"");
        let (_, failure) = session.run_raw(&step);
        let Some(failure) = failure else {
            marker_found = true;
            break;
        };
        let body = session.read(&name);
        let (blocked, detail) = super::is_blocked_desktop_failure("agent-desktop", &failure, &body);
        if blocked {
            session.blocked(&detail);
            return session.into_outcome();
        }
        std::thread::sleep(Duration::from_millis(500));
    }
    if !marker_found {
        session.fail(
            "frontend build marker was not found in the accessibility tree after 15 attempts",
        );
        return session.fail_cleanup();
    }

    let mut complete = false;
    for attempt in 1..=10 {
        let name = format!("snapshot-{attempt:02}");
        let step = StepSpec::owned(
            &name,
            "agent-desktop",
            vec![
                "snapshot".to_string(),
                "--app".to_string(),
                "anvil.exe".to_string(),
                "--window-id".to_string(),
                session.window_id.clone(),
                "--timeout-ms".to_string(),
                "30000".to_string(),
                "--compact".to_string(),
            ],
        )
        .timeout(90);
        let (_, failure) = session.run_raw(&step);
        if let Some(failure) = failure {
            let body = session.read(&name);
            let (blocked, detail) =
                super::is_blocked_desktop_failure("agent-desktop", &failure, &body);
            if blocked {
                session.blocked(&detail);
                return session.into_outcome();
            }
            session.fail(&failure);
            return session.fail_cleanup();
        }
        if super::snapshot_complete(&session.read(&name)) {
            complete = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(500));
    }
    if !complete {
        session.fail("accessibility snapshot did not become complete after 10 attempts");
        return session.fail_cleanup();
    }

    if !session.screenshot("native/screenshot.png") {
        return session.fail_cleanup();
    }
    if !session.close_client("window") {
        return session.into_outcome();
    }
    if !session.collect_app_log() {
        return session.into_outcome();
    }
    let _ = parse::parse_windows(&session.read("launch-list-windows"));
    session.pass()
}
