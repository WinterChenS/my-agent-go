//! `R0-05-native-chat`: the conversation page in both entries, the rail tabs,
//! the thread menu, the compact drawers and the layout probe.
//!
//! Ported from `internal/acceptance/runner_chat_native.go`.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/chat",
    normal_marker: "尚未接入会话",
    normal_exact: false,
    fixtures: &["API 迁移评估", "迁移工程师", "陈羽", "claude-sonnet-4.5"],
    normal_shot: "native/chat-normal-1440x900.png",
    normal_compact_shot: "native/chat-normal-390x844.png",
    sample_url: "/__sample/chat",
    sample_marker: "API 迁移评估",
    sample_shot: "native/chat-sample-1440x900.png",
    tag: "chat",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "chat");
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
    if !session.reset_data_dirs("chat")
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

    let Some(artifacts_tab) = session.find_ref("chat-artifacts-tab", None, "输出物 3", 15)
    else {
        return;
    };
    let click = StepSpec::owned(
        "chat-click-artifacts-tab",
        "agent-desktop",
        vec!["click".to_string(), artifacts_tab],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_text("chat-artifacts-pane", "本次会话产出 · 3", false, 10)
        || !session.screenshot("native/chat-sample-rail-artifacts-1440x900.png")
    {
        return;
    }

    let Some(more) = session.find_ref("chat-more", Some("button"), "更多操作", 15) else {
        return;
    };
    let open_menu = StepSpec::owned(
        "chat-open-thread-menu",
        "agent-desktop",
        vec!["click".to_string(), more.clone()],
    )
    .timeout(60);
    if !session.run(&[open_menu]) {
        return;
    }
    if !session.wait_role_count("chat-thread-menu", "window", "会话操作", 1, 10)
        || !session.screenshot("native/chat-thread-menu-1440x900.png")
    {
        return;
    }
    if !session.send_key("chat-close-thread-menu", "escape") {
        return;
    }
    if !session.wait_role_count("chat-thread-menu-escape", "window", "会话操作", 0, 10)
        || !session.wait_focused(&more, "chat-more")
    {
        return;
    }

    // Compact sample: the rail becomes a drawer; open, screenshot and close it.
    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample chat");
        return;
    }
    probes = next;
    if !session.screenshot("native/chat-sample-390x844.png") {
        return;
    }
    let Some(rail_toggle) = session.find_ref("chat-rail-expand", None, "右侧面板", 15) else {
        return;
    };
    if !session.click_ref_physical(&rail_toggle, "chat-rail-toggle")
        || !session.wait_expanded(&rail_toggle, true, "chat-rail-open")
        || !session.screenshot("native/chat-rail-drawer-390x844.png")
    {
        return;
    }
    let Some(rail_close) = session.find_ref("chat-rail-close", None, "关闭面板", 15) else {
        return;
    };
    if !session.click_ref_physical(&rail_close, "chat-rail-close")
        || !session.wait_expanded(&rail_toggle, false, "chat-rail-closed")
        || !session.screenshot("native/chat-rail-closed-390x844.png")
    {
        return;
    }

    // Compact history drawer: open, screenshot, Escape and focus return.
    let Some(history) = session.find_ref("chat-history-open", None, "历史会话", 15) else {
        return;
    };
    if !session.click_ref_physical(&history, "chat-history-toggle")
        || !session.wait_role_count("chat-history-drawer", "window", "历史会话", 1, 10)
        || !session.screenshot("native/chat-history-drawer-390x844.png")
    {
        return;
    }
    if !session.send_key("chat-close-history-drawer", "escape") {
        return;
    }
    if !session.wait_role_count("chat-history-drawer-escape", "window", "历史会话", 0, 10)
        || !session.wait_focused(&history, "chat-history")
    {
        return;
    }
    if !session.close_client("chat-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
