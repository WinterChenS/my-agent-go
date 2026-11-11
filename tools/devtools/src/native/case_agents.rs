//! `R0-06-native-agents`: the agent list/detail page in both entries, the
//! detail tabs, the new-agent dialog and the compact list/detail switch.
//!
//! Ported from `internal/acceptance/runner_agents_native.go`.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/agents",
    normal_marker: "Agent 管理",
    normal_exact: false,
    fixtures: &["迁移工程师", "数据分析师", "claude-sonnet-4.5"],
    normal_shot: "native/agents-normal-1440x900.png",
    normal_compact_shot: "native/agents-normal-390x844.png",
    sample_url: "/__sample/agents",
    sample_marker: "迁移工程师",
    sample_shot: "native/agents-sample-1440x900.png",
    tag: "agents",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "agents");
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
    if !session.reset_data_dirs("agents")
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

    let Some(runs_tab) = session.find_ref("agents-runs-tab", None, "运行记录", 15) else {
        return;
    };
    let click = StepSpec::owned(
        "agents-click-runs-tab",
        "agent-desktop",
        vec!["click".to_string(), runs_tab],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_text("agents-runs-pane", "API 迁移评估：v1 → v2", false, 10)
        || !session.screenshot("native/agents-sample-runs-1440x900.png")
    {
        return;
    }

    let Some(new_agent) = session.find_ref("agents-new", Some("button"), "新建 Agent", 15) else {
        return;
    };
    let open = StepSpec::owned(
        "agents-open-new",
        "agent-desktop",
        vec!["click".to_string(), new_agent.clone()],
    )
    .timeout(60);
    if !session.run(&[open]) {
        return;
    }
    if !session.wait_role_count("agents-new-dialog", "window", "新建 Agent", 1, 10)
        || !session.screenshot("native/agents-new-dialog-1440x900.png")
    {
        return;
    }
    if !session.send_key("agents-close-new", "escape") {
        return;
    }
    if !session.wait_role_count("agents-new-dialog-escape", "window", "新建 Agent", 0, 20)
        || !session.wait_focused(&new_agent, "agents-new")
    {
        return;
    }

    // Compact sample: detail view then back to the list.
    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample agents page");
        return;
    }
    probes = next;
    let Some(basic_tab) = session.find_ref("agents-basic-tab", None, "基础配置", 15) else {
        return;
    };
    let click = StepSpec::owned(
        "agents-compact-basic-tab",
        "agent-desktop",
        vec!["click".to_string(), basic_tab],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_text("agents-compact-basic", "Agent 运行时", false, 10)
        || !session.screenshot("native/agents-sample-390x844.png")
    {
        return;
    }
    let Some(back) = session.find_ref("agents-back", Some("button"), "返回 Agent 列表", 15)
    else {
        return;
    };
    let click = StepSpec::owned(
        "agents-back-to-list",
        "agent-desktop",
        vec!["click".to_string(), back],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_text("agents-compact-list", "主 Agent · 4", false, 10)
        || !session.screenshot("native/agents-list-390x844.png")
    {
        return;
    }
    if !session.close_client("agents-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
