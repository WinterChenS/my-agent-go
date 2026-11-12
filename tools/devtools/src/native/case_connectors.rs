//! `R0-09-native-connectors`: the connector grid in both entries, the policy
//! table, the credential health panel, the add dialog and the compact cards.
//!
//! Ported from `internal/acceptance/runner_connectors_native.go`.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/connectors",
    normal_marker: "连接器",
    normal_exact: true,
    fixtures: &[
        "chen.yu@anvil.dev",
        "imap.anvil.dev:993",
        "mail.send_message",
        "1,284",
        "令牌已过期 2 天",
    ],
    normal_shot: "native/connectors-normal-1440x900.png",
    normal_compact_shot: "native/connectors-normal-390x844.png",
    sample_url: "/__sample/connectors",
    sample_marker: "动作与确认策略",
    sample_shot: "native/connectors-sample-1440x900.png",
    tag: "connectors",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "connectors");
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
    if !session.reset_data_dirs("connectors")
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

    // Every connector card and the policy rows must be real, painted text.
    for (log_id, text) in [
        ("connectors-card-mail", "邮箱"),
        ("connectors-unverified", "需重新授权"),
        ("connectors-policy", "mail.send_group"),
        ("connectors-health", "53 天后续期"),
    ] {
        if session.text_node_bounds(log_id, text, 15).is_none() {
            return;
        }
    }
    if !session.screenshot("native/connectors-cards-1440x900.png") {
        return;
    }

    // Add-connector dialog: open, capture, close with Escape and return focus.
    let Some(add) = session.find_ref("connectors-add", Some("button"), "添加连接器", 15)
    else {
        return;
    };
    let click = StepSpec::owned(
        "connectors-open-add",
        "agent-desktop",
        vec!["click".to_string(), add.clone()],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_role_count("connectors-add-dialog", "window", "添加连接器", 1, 10)
        || !session.screenshot("native/connectors-add-dialog-1440x900.png")
    {
        return;
    }
    if !session.send_key("connectors-close-add", "escape") {
        return;
    }
    if !session.wait_role_count("connectors-add-escape", "window", "添加连接器", 0, 20)
        || !session.wait_focused(&add, "connectors-add")
    {
        return;
    }

    // Per-connector settings dialog.
    let Some(configure) = session.find_ref("connectors-configure", Some("button"), "配置", 15)
    else {
        return;
    };
    let open_settings = StepSpec::owned(
        "connectors-open-settings",
        "agent-desktop",
        vec!["click".to_string(), configure],
    )
    .timeout(60);
    if !session.run(&[open_settings])
        || !session.wait_role_count("connectors-settings", "window", "邮箱", 1, 10)
        || !session.screenshot("native/connectors-settings-1440x900.png")
    {
        return;
    }
    if !session.send_key("connectors-close-settings", "escape")
        || !session.wait_role_count("connectors-settings-escape", "window", "邮箱", 0, 20)
    {
        return;
    }

    // Compact sample: one-column cards.
    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample connector page");
        return;
    }
    probes = next;
    if !session.screenshot("native/connectors-sample-390x844.png") {
        return;
    }
    if !session.close_client("connectors-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
