//! `R0-07-native-skills`: the skill grid in both entries, the builder dialog
//! with an expandable workflow step, the import dialog and the compact grid.
//!
//! Ported from `internal/acceptance/runner_skills_native.go`.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/skills",
    normal_marker: "Skill",
    normal_exact: true,
    fixtures: &["代码审查", "v2.3", "97%"],
    normal_shot: "native/skills-normal-1440x900.png",
    normal_compact_shot: "native/skills-normal-390x844.png",
    sample_url: "/__sample/skills",
    sample_marker: "代码审查",
    sample_shot: "native/skills-sample-1440x900.png",
    tag: "skills",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "skills");
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
    if !session.reset_data_dirs("skills")
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

    let Some(card) = session.find_ref("skills-card-code-review", None, "打开 代码审查", 15)
    else {
        return;
    };
    let open = StepSpec::owned(
        "skills-open-builder",
        "agent-desktop",
        vec!["click".to_string(), card.clone()],
    )
    .timeout(60);
    if !session.run(&[open]) {
        return;
    }
    if !session.wait_role_count("skills-builder", "window", "创建可复用能力", 1, 10) {
        return;
    }
    let Some(triggers) =
        session.find_ref("skills-builder-triggers", Some("button"), "触发与资源", 15)
    else {
        return;
    };
    let click = StepSpec::owned(
        "skills-builder-triggers",
        "agent-desktop",
        vec!["click".to_string(), triggers],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    let Some(step) = session.find_ref("skills-workflow-step", None, "展开或收起步骤", 15)
    else {
        return;
    };
    let expand = StepSpec::owned(
        "skills-expand-step",
        "agent-desktop",
        vec!["expand".to_string(), step.clone()],
    )
    .timeout(60);
    if !session.run(&[expand]) {
        return;
    }
    if !session.wait_expanded(&step, true, "skills-step")
        || !session.screenshot("native/skills-workflow-1440x900.png")
    {
        return;
    }
    let Some(test_step) = session.find_ref("skills-builder-test", Some("button"), "测试与发布", 15)
    else {
        return;
    };
    let click = StepSpec::owned(
        "skills-builder-test",
        "agent-desktop",
        vec!["click".to_string(), test_step],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_text("skills-builder-checks", "SKILL.md 结构完整", false, 10)
        || !session.screenshot("native/skills-builder-test-1440x900.png")
    {
        return;
    }
    if !session.send_key("skills-close-builder", "escape") {
        return;
    }
    if !session.wait_role_count("skills-builder-escape", "window", "创建可复用能力", 0, 10)
        || !session.wait_focused(&card, "skills-card")
    {
        return;
    }

    let Some(import) = session.find_ref("skills-import", Some("button"), "导入 Skill", 15) else {
        return;
    };
    let click = StepSpec::owned(
        "skills-open-import",
        "agent-desktop",
        vec!["click".to_string(), import.clone()],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_role_count("skills-import-dialog", "window", "导入 Skill", 1, 10)
        || !session.screenshot("native/skills-import-dialog-1440x900.png")
    {
        return;
    }
    if !session.send_key("skills-close-import", "escape") {
        return;
    }
    if !session.wait_role_count("skills-import-escape", "window", "导入 Skill", 0, 10)
        || !session.wait_focused(&import, "skills-import")
    {
        return;
    }

    // Compact sample: the same grid becomes one column.
    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample skills page");
        return;
    }
    probes = next;
    if !session.wait_text("skills-compact-grid", "代码审查", false, 10)
        || !session.screenshot("native/skills-sample-390x844.png")
    {
        return;
    }
    if !session.close_client("skills-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
