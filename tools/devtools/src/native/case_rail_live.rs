//! `R2-04-rail-native`: the right rail, on the real client, after a real turn.
//!
//! The automation case proves the aggregation against a ledger and real files.
//! This one proves the *page*: the four panes exist on the shipped build, the
//! panes say they are empty when the turn produced nothing (instead of showing
//! the prototype's placeholder rows), and at a narrow width the rail is a
//! drawer that opens and closes with the toggle.
//!
//! Deliberately no write tool: the point of the empty panes is that this build
//! does not invent content. A turn that produced no file must show no file.

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::session::Session;

const COMPOSER_LABEL: &str = "继续追问";
const PROMPT: &str = "Reply with exactly: ok";

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "rail-live");
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
    if !session.reset_data_dirs("rail-live")
        || !session.prepare_home()
        || !session.require_status()
        || !session.build_client()
        || !session.check_paths()
    {
        return;
    }

    session.initial_url = "/chat".to_string();
    if !session.launch_client(1440, 900, "launch-rail-live") || !session.activate() {
        return;
    }
    if !session.wait_text("chat-page", "对话", false, 20) {
        return;
    }

    let Some(composer) =
        session.find_ref_enabled("chat-composer", "textfield", COMPOSER_LABEL, 120)
    else {
        return;
    };
    let Some(bounds) = session.element_bounds("chat-composer", &composer) else {
        return;
    };
    if !session.click_at("chat-composer", bounds) {
        return;
    }
    let typed = StepSpec::owned(
        "rail-type-prompt",
        "agent-desktop",
        vec![
            "type".to_string(),
            "--headed".to_string(),
            composer,
            PROMPT.to_string(),
        ],
    )
    .timeout(60);
    if !session.run(&[typed]) {
        return;
    }
    let Some(send) = session.find_ref_enabled("chat-send", "button", "发送", 15) else {
        return;
    };
    let click = StepSpec::owned(
        "rail-click-send",
        "agent-desktop",
        vec!["click".to_string(), send],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_text("rail-reply", "ok", true, 90) {
        return;
    }

    // The rail reads its panes from the ledger, so it has to be there once the
    // turn lands.
    let Some(exec_tab) = session.find_ref("rail-tab-exec", Some("tab"), "执行", 15) else {
        return;
    };
    if !session.click_ref_physical(&exec_tab, "rail-exec-tab") {
        return;
    }
    // No tool call this turn, no usage reported by this turn's accounting: both
    // panes must say so rather than render a plausible row.
    if !session.wait_text("rail-exec-empty", "还没有工具执行记录", false, 15) {
        return;
    }
    if !session.screenshot("native/rail-exec-1440x900.png") {
        return;
    }

    // The tab's accessible name carries a live count ("输出物 0"), so an exact
    // name lookup can never match it. Match the stable part of the label.
    let Some(artifacts_tab) =
        session.find_ref_contains("rail-tab-artifacts", "tab", "输出物", 15)
    else {
        return;
    };
    if !session.click_ref_physical(&artifacts_tab, "rail-artifacts-tab")
        || !session.wait_text("rail-artifacts-empty", "还没有产出文件", false, 15)
    {
        return;
    }

    let Some(browser_tab) = session.find_ref("rail-tab-browser", Some("tab"), "浏览器", 15)
    else {
        return;
    };
    if !session.click_ref_physical(&browser_tab, "rail-browser-tab")
        || !session.wait_text("rail-browser-empty", "没有可在本机预览的页面", false, 15)
    {
        return;
    }
    if !session.screenshot("native/rail-browser-1440x900.png") {
        return;
    }

    // Narrow width: the rail becomes a drawer, and the overlay path is the one
    // the user actually gets at this size.
    if !session.resize(760, 900, true) {
        return;
    }
    let Some(toggle) = session.find_ref("rail-toggle", None, "右侧面板", 15) else {
        return;
    };
    if !session.wait_expanded(&toggle, false, "rail-narrow-closed") {
        return;
    }
    if !session.click_ref_physical(&toggle, "rail-toggle-narrow")
        || !session.wait_expanded(&toggle, true, "rail-narrow-open")
    {
        return;
    }
    // The scrim only exists when the rail is an overlay, so finding it proves
    // the narrow layout is the drawer rather than a squeezed column.
    let Some(scrim) = session.find_ref("rail-scrim", Some("button"), "关闭输出物面板", 15)
    else {
        return;
    };
    if !session.screenshot("native/rail-drawer-760x900.png") {
        return;
    }
    let close = StepSpec::owned(
        "rail-click-scrim",
        "agent-desktop",
        vec!["click".to_string(), scrim],
    )
    .timeout(60);
    if !session.run(&[close]) || !session.wait_expanded(&toggle, false, "rail-scrim-closed") {
        return;
    }

    if !session.close_client("rail-live") {
        return;
    }
    session.collect_app_log();
}
