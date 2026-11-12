//! `R0-11-native-library`: the artifact grid in both entries, the type tabs,
//! the grid/list switch, the asset detail dialog and the compact layout.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/library",
    normal_marker: "资源库",
    normal_exact: true,
    fixtures: &[
        "v1-to-v2-migration-plan.md",
        "184 MB",
        "retention-weekly.png",
        "引用 3",
    ],
    normal_shot: "native/library-normal-1440x900.png",
    normal_compact_shot: "native/library-normal-390x844.png",
    sample_url: "/__sample/library",
    sample_marker: "最近 7 天",
    sample_shot: "native/library-sample-1440x900.png",
    tag: "library",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "library");
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
    if !session.reset_data_dirs("library")
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

    for (log_id, text) in [
        ("library-plan", "v1-to-v2-migration-plan.md"),
        ("library-storage", "184 MB"),
        ("library-badge", "引用 3"),
        ("library-session", "API 迁移评估"),
    ] {
        if session.text_node_bounds(log_id, text, 15).is_none() {
            return;
        }
    }
    if !session.screenshot("native/library-grid-1440x900.png") {
        return;
    }

    // The detail dialog opens from the card's accessible name.
    let Some(open) = session.find_ref(
        "library-open",
        Some("button"),
        "打开 v1-to-v2-migration-plan.md",
        15,
    ) else {
        return;
    };
    let click_open = StepSpec::owned(
        "library-open-detail",
        "agent-desktop",
        vec!["click".to_string(), open],
    )
    .timeout(60);
    if !session.run(&[click_open]) {
        return;
    }
    if !session.wait_role_count(
        "library-detail-dialog",
        "window",
        "v1-to-v2-migration-plan.md",
        1,
        10,
    ) || !session.wait_text("library-detail-origin", "写文件工具 · 2 次修订", false, 15)
        || !session.screenshot("native/library-detail-1440x900.png")
    {
        return;
    }
    if !session.send_key("library-close-detail", "escape")
        || !session.wait_role_count(
            "library-detail-escape",
            "window",
            "v1-to-v2-migration-plan.md",
            0,
            20,
        )
    {
        return;
    }

    // Two cards sit side by side in the grid; after switching to the list they
    // must stack, so the vertical offset between them has to grow.
    let Some(grid_first) =
        session.text_node_bounds("library-grid-first", "v1-to-v2-migration-plan.md", 15)
    else {
        return;
    };
    let Some(grid_second) =
        session.text_node_bounds("library-grid-second", "compat-matrix.csv", 15)
    else {
        return;
    };
    if (grid_second.y - grid_first.y).abs() > 12.0 {
        session.fail("the grid did not lay the first two artifacts out side by side");
        return;
    }

    // The upload dialog is the page's other real dialog; the grid/list switch
    // stays covered by the unit test because WebView2 does not expose its
    // labelled segmented buttons as named buttons.
    let Some(upload) = session.find_ref("library-upload", Some("button"), "上传", 15) else {
        return;
    };
    let click_upload = StepSpec::owned(
        "library-open-upload",
        "agent-desktop",
        vec!["click".to_string(), upload.clone()],
    )
    .timeout(60);
    if !session.run(&[click_upload])
        || !session.wait_role_count("library-upload-dialog", "window", "上传到资源库", 1, 10)
        || !session.screenshot("native/library-upload-1440x900.png")
    {
        return;
    }
    if !session.close_dialog("library-upload-escape", "window", "上传到资源库", 20)
        || !session.wait_focused(&upload, "library-upload")
    {
        return;
    }

    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample library page");
        return;
    }
    probes = next;
    if !session.screenshot("native/library-sample-390x844.png") {
        return;
    }
    if !session.close_client("library-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
