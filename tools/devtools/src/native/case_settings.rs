//! `R0-12-native-settings`: the section navigation, the real appearance and
//! language controls, the model/runtime/observability/security panes, the
//! deep-linked hash behaviour and the compact single-column layout.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/settings",
    normal_marker: "系统设置",
    normal_exact: true,
    fixtures: &[
        "gpt-5.1",
        "sk-proj-••••7K2P",
        "pi · v0.48.2",
        "6.2M",
        "风险判定规则",
    ],
    normal_shot: "native/settings-normal-1440x900.png",
    normal_compact_shot: "native/settings-normal-390x844.png",
    sample_url: "/__sample/settings",
    sample_marker: "系统设置",
    sample_shot: "native/settings-sample-1440x900.png",
    tag: "settings",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "settings");
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
    if !session.reset_data_dirs("settings")
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
    // normal_entry closes the client, so every assertion below runs on the
    // sample entry, which renders the same shell and section navigation.
    if !pages::sample_entry(session, &SPEC, &mut probes, &viewports) {
        return;
    }

    // The section navigation must expose all five sections. WebView2 reports the
    // nav buttons without a rectangle, so they are asserted by ref instead.
    for (log_id, label) in [
        ("settings-general", "通用"),
        ("settings-models-nav", "模型管理"),
        ("settings-runtime-nav", "运行时"),
        ("settings-observability-nav", "观测"),
        ("settings-security-nav", "安全与权限"),
    ] {
        if session
            .find_ref(log_id, Some("button"), label, 15)
            .is_none()
        {
            return;
        }
    }
    if !session.screenshot("native/settings-general-1440x900.png") {
        return;
    }

    // Models: the fixture provider table replaces the normal empty state.
    let Some(models) = session.find_ref("settings-sample-models", Some("button"), "模型管理", 15)
    else {
        return;
    };
    let click_models = StepSpec::owned(
        "settings-sample-open-models",
        "agent-desktop",
        vec!["click".to_string(), models],
    )
    .timeout(60);
    if !session.run(&[click_models])
        || !session.wait_text("settings-models-heading", "模型供应商", false, 15)
        || !session.wait_text("settings-provider", "api.openai.com", false, 15)
    {
        return;
    }
    if !session.screenshot("native/settings-sample-models-1440x900.png") {
        return;
    }

    let Some(observability) =
        session.find_ref("settings-nav-observability", Some("button"), "观测", 15)
    else {
        return;
    };
    let click_observability = StepSpec::owned(
        "settings-open-observability",
        "agent-desktop",
        vec!["click".to_string(), observability],
    )
    .timeout(60);
    if !session.run(&[click_observability])
        || !session.wait_text("settings-usage-stat", "6.2M", false, 15)
    {
        return;
    }
    let Some(traces) = session.find_ref("settings-tab-traces", Some("button"), "链路", 15) else {
        return;
    };
    let click_traces = StepSpec::owned(
        "settings-open-traces",
        "agent-desktop",
        vec!["click".to_string(), traces],
    )
    .timeout(60);
    if !session.run(&[click_traces])
        || !session.wait_text("settings-trace-row", "tr_8f2c91a4", false, 15)
    {
        return;
    }
    if !session.screenshot("native/settings-sample-traces-1440x900.png") {
        return;
    }

    let Some(security) =
        session.find_ref("settings-nav-security", Some("button"), "安全与权限", 15)
    else {
        return;
    };
    let click_security = StepSpec::owned(
        "settings-open-security",
        "agent-desktop",
        vec!["click".to_string(), security],
    )
    .timeout(60);
    if !session.run(&[click_security])
        || !session.wait_text("settings-rules", "风险判定规则", false, 15)
    {
        return;
    }
    if !session.screenshot("native/settings-sample-security-1440x900.png") {
        return;
    }

    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample settings page");
        return;
    }
    probes = next;
    if !session.screenshot("native/settings-sample-390x844.png") {
        return;
    }
    if !session.close_client("settings-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
