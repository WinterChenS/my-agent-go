//! `R0-01-native-shell`: every route is reachable through the real shell at
//! the compact, narrow and wide viewports, the layout probe stays clean, and
//! the platform history shortcut goes back.
//!
//! Ported from `internal/acceptance/runner_shell_native.go`. The build step is
//! `cargo build -p anvil` (the Wails binary is gone) and the client is launched
//! through agent-desktop exactly as before.

use std::time::Duration;

use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::parse;
use super::session::Session;

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "shell");
    let repo = runner.repo();
    if session.ensure_native_dir().is_err() {
        return session.into_outcome();
    }
    if !session.reset_data_dirs("shell") {
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
    if !session.launch_client(390, 844, "launch-390x844") {
        return session.fail_cleanup();
    }
    let mut probes = session.wait_probe(0, Duration::from_secs(10));
    if probes < 1 {
        session.fail("layout probe was not written to the application log after launch");
        return session.fail_cleanup();
    }
    if !session.activate() {
        return session.fail_cleanup();
    }
    let routes = match parse::read_shell_routes(repo) {
        Ok(routes) => routes,
        Err(err) => {
            session.fail(&err);
            return session.fail_cleanup();
        }
    };
    for route in &routes {
        if !session.navigate(route, "narrow") {
            return session.fail_cleanup();
        }
    }
    if !session.screenshot("native/screenshot-390x844.png") {
        return session.fail_cleanup();
    }

    if !session.resize(360, 800, true) {
        return session.fail_cleanup();
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next > probes {
        probes = next;
    } else {
        session.fail("layout probe did not record the 360x800 resize");
        return session.fail_cleanup();
    }
    if let Some(last) = routes.last()
        && last.chrome == "shell"
        && !last.breadcrumb_parent.is_empty()
        && !session.visible_static_text("current-parent-360x800", &last.breadcrumb_parent, 10)
    {
        return session.fail_cleanup();
    }
    if !session.screenshot("native/screenshot-360x800.png") {
        return session.fail_cleanup();
    }
    if !session.go_back("narrow", "/schedules") {
        return session.fail_cleanup();
    }

    if !session.resize(1440, 900, true) {
        return session.fail_cleanup();
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next > probes {
        probes = next;
    } else {
        session.fail("layout probe did not record the 1440x900 resize");
        return session.fail_cleanup();
    }
    for route in &routes {
        if !session.navigate(route, "wide") {
            return session.fail_cleanup();
        }
    }
    if !session.screenshot("native/screenshot-1440x900.png") {
        return session.fail_cleanup();
    }

    let viewports = match parse::read_manifest_viewports(repo) {
        Ok(viewports) => viewports,
        Err(err) => {
            session.fail(&err);
            return session.fail_cleanup();
        }
    };
    for viewport in &viewports {
        if !session.resize(viewport.width, viewport.height, false) {
            return session.fail_cleanup();
        }
        let next = session.wait_probe(probes, Duration::from_secs(10));
        if next > probes {
            probes = next;
        } else {
            session.fail(&format!(
                "layout probe did not record viewport {}x{}",
                viewport.width, viewport.height
            ));
            return session.fail_cleanup();
        }
    }

    if !session.close_client("shell") {
        return session.into_outcome();
    }
    let probes = parse::parse_layout_probes(&session.log_text());
    if probes.len() < 10 {
        session.fail(&format!(
            "application log has {} layout probes, want at least 10",
            probes.len()
        ));
        return session.into_outcome();
    }
    let mut unique: Vec<String> = Vec::new();
    for probe in &probes {
        if probe.overflow {
            session.fail(&format!(
                "horizontal overflow at viewport {}: scrollWidth={} clientWidth={}",
                probe.viewport, probe.scroll_width, probe.client_width
            ));
            return session.into_outcome();
        }
        if !unique.contains(&probe.viewport) {
            unique.push(probe.viewport.clone());
        }
    }
    if unique.len() < 8 {
        session.fail(&format!(
            "layout probe recorded only {} distinct viewports",
            unique.len()
        ));
        return session.into_outcome();
    }
    if !session.collect_app_log() {
        return session.into_outcome();
    }
    session.pass()
}
