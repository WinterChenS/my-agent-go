//! `R0-04-native-workbench`: the workbench entries, the layout probe and the
//! global search dialog.
//!
//! Ported from `internal/acceptance/runner_workbench_native.go`: the normal and
//! isolated sample entries at the wide and compact viewports, the real layout
//! probe, and the palette's Escape/focus-return behaviour.

use std::time::Duration;

use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::parse;
use super::session::Session;

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "workbench");
    if session.ensure_native_dir().is_ok() {
        run_case(&mut session);
    }
    if session.out.status.is_empty() {
        session.pass()
    } else {
        session.fail_cleanup()
    }
}

fn run_case(session: &mut Session<'_>) {
    if !session.reset_data_dirs("workbench")
        || !session.prepare_home()
        || !session.require_status()
        || !session.build_client()
        || !session.check_paths()
    {
        return;
    }

    // Normal entry at the wide viewport.
    session.initial_url = String::new();
    if !session.launch_client(1440, 900, "launch-workbench-normal") || !session.activate() {
        return;
    }
    if !session.assert_sample_banner(false) {
        return;
    }
    if let Some(count) = session.text_count("1,489", "normal-fixture-check")
        && count != 0
    {
        session.fail("normal workbench rendered sample fixture 1,489");
        return;
    }
    if !session.screenshot("native/workbench-normal-1440x900.png") {
        return;
    }
    let mut probes = session.wait_probe(0, Duration::from_secs(10));
    if probes < 1 {
        session.fail("layout probe missing for the normal workbench");
        return;
    }

    // Search dialog: real click, Escape key, focus restored by the app.
    let Some(search) = session.find_ref("workbench-search", Some("button"), "搜索", 10) else {
        return;
    };
    let open = crate::plans::StepSpec::owned(
        "open-global-search",
        "agent-desktop",
        vec!["click".to_string(), search.clone()],
    )
    .timeout(60);
    if !session.run(&[open]) {
        return;
    }
    if !session.wait_text("global-search-open", "快速跳转", false, 10)
        || !session.screenshot("native/workbench-search-1440x900.png")
    {
        return;
    }
    if !session.send_key("close-global-search", "escape") {
        return;
    }
    if !session.wait_text_gone("global-search-escape", "快速跳转", 10)
        || !session.wait_focused(&search, "workbench-search")
    {
        return;
    }

    // Responsive spot checks plus the compact reference screenshot.
    if !session.resize(1366, 768, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next > probes {
        probes = next;
    } else {
        session.fail("layout probe missing at 1366x768");
        return;
    }
    if !session.resize(820, 1180, false) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next > probes {
        probes = next;
    } else {
        session.fail("layout probe missing at 820x1180");
        return;
    }
    if !session.resize(390, 844, true) {
        return;
    }
    session.wait_probe(probes, Duration::from_secs(10));
    if !session.screenshot("native/workbench-normal-390x844.png") {
        return;
    }
    if !session.close_client("workbench-normal") {
        return;
    }

    // Isolated sample entry at both reference viewports.
    session.initial_url = "/__sample/workbench".to_string();
    if !session.launch_client(1440, 900, "launch-workbench-sample") || !session.activate() {
        return;
    }
    if !session.assert_sample_banner(true)
        || !session.wait_text("sample-fixtures", "1,489", false, 10)
        || !session.screenshot("native/workbench-sample-1440x900.png")
    {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next > probes {
        probes = next;
    }
    if !session.resize(390, 844, true) {
        return;
    }
    session.wait_probe(probes, Duration::from_secs(10));
    if !session.screenshot("native/workbench-sample-390x844.png") {
        return;
    }
    if !session.close_client("workbench-sample") {
        return;
    }

    let log = session.log_text();
    let recorded = parse::parse_layout_probes(&log);
    if recorded.len() < 6 {
        session.fail(&format!(
            "only {} layout probes recorded, want at least 6",
            recorded.len()
        ));
        return;
    }
    let mut unique: Vec<String> = Vec::new();
    for probe in &recorded {
        if probe.overflow {
            session.fail(&format!(
                "horizontal overflow at viewport {}: scrollWidth={} clientWidth={}",
                probe.viewport, probe.scroll_width, probe.client_width
            ));
            return;
        }
        if !unique.contains(&probe.viewport) {
            unique.push(probe.viewport.clone());
        }
    }
    if unique.len() < 3 {
        session.fail(&format!(
            "only {} distinct viewports probed, want at least 3",
            unique.len()
        ));
        return;
    }
    session.collect_app_log();
}
