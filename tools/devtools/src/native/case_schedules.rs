//! `R0-10-native-schedules`: the task table in both entries, the run detail,
//! the seven-day chart, row selection, the new-task dialog and the compact
//! single-column layout.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/schedules",
    normal_marker: "定时任务",
    normal_exact: true,
    // Fixture strings become log file names, so they must not contain "/" or
    // ":"; the cron expression itself is asserted further down.
    fixtures: &["每日站会纪要汇总", "6m08s", "98.2%"],
    normal_shot: "native/schedules-normal-1440x900.png",
    normal_compact_shot: "native/schedules-normal-390x844.png",
    sample_url: "/__sample/schedules",
    sample_marker: "任务列表",
    sample_shot: "native/schedules-sample-1440x900.png",
    tag: "schedules",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "schedules");
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
    if !session.reset_data_dirs("schedules")
        || !session.prepare_home()
        || !session.require_status()
        || !session.build_client()
        || !session.check_paths()
    {
        return;
    }
    let Some((mut probes, viewports)) = pages::normal_entry(session, &SPEC) else {
        return;
    };
    if !pages::sample_entry(session, &SPEC, &mut probes, &viewports) {
        return;
    }

    // The first task row and both plan columns must paint real rectangles.
    for (log_id, text) in [
        ("schedules-standup", "每日站会纪要汇总"),
        ("schedules-cron", "0 9 * * 1-5"),
        ("schedules-agent", "文档助手"),
        ("schedules-when", "工作日 09:00 · 下次 明天 09:00"),
    ] {
        if session.text_node_bounds(log_id, text, 15).is_none() {
            return;
        }
    }
    if !session.screenshot("native/schedules-table-1440x900.png") {
        return;
    }

    // Selecting the second row must switch the detail pane to that task.
    // The row name is its cells joined, so it is matched by contained text.
    let Some(row) = session.find_ref_contains("schedules-row-vuln", "row", "依赖漏洞扫描", 15)
    else {
        return;
    };
    let click_row = StepSpec::owned(
        "schedules-select-vuln",
        "agent-desktop",
        vec!["click".to_string(), row],
    )
    .timeout(60);
    if !session.run(&[click_row]) {
        return;
    }
    if !session.wait_text("schedules-detail-agent", "安全审计", false, 15)
        || !session.wait_text("schedules-detail-perm", "完全控制", false, 15)
    {
        return;
    }
    if !session.screenshot("native/schedules-detail-1440x900.png") {
        return;
    }

    // New-task dialog: open, capture, close with Escape and return focus.
    let Some(new_task) = session.find_ref("schedules-new", Some("button"), "新建任务", 15)
    else {
        return;
    };
    let click_new = StepSpec::owned(
        "schedules-open-new",
        "agent-desktop",
        vec!["click".to_string(), new_task.clone()],
    )
    .timeout(60);
    if !session.run(&[click_new]) {
        return;
    }
    if !session.wait_role_count("schedules-new-dialog", "window", "新建定时任务", 1, 10)
        || !session.screenshot("native/schedules-new-dialog-1440x900.png")
    {
        return;
    }
    if !session.send_key("schedules-close-new", "escape") {
        return;
    }
    if !session.wait_role_count("schedules-new-escape", "window", "新建定时任务", 0, 20)
        || !session.wait_focused(&new_task, "schedules-new")
    {
        return;
    }

    // Compact sample: the split collapses to a single column.
    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample schedules page");
        return;
    }
    probes = next;
    if !session.screenshot("native/schedules-sample-390x844.png") {
        return;
    }
    if !session.close_client("schedules-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
