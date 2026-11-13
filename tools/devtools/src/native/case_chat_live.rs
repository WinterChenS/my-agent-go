//! `R2-01-native-chat`: one real turn through the desktop UI.
//!
//! The automation cases drive `session_report`, which talks to the runtime
//! directly. This case drives the product: open the chat page, type into the
//! composer, press send, and wait for the assistant's text to appear. It is the
//! only proof that the host commands, the ACL, the ledger and the page are wired
//! to each other rather than each working in isolation.

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::session::Session;

/// The composer's accessible name is its `aria-label`; nothing else on the page
/// shares it, so a name lookup is enough and no role guess is needed.
const COMPOSER_LABEL: &str = "继续追问，或 @ 引用文件与 Agent…";

/// Asking for a fixed word makes the assertion unambiguous: the reply either is
/// `ok` or the case fails.
const PROMPT: &str = "Reply with exactly: ok";

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "chat-live");
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
    if !session.reset_data_dirs("chat-live")
        || !session.prepare_home()
        || !session.require_status()
        || !session.build_client()
        || !session.check_paths()
    {
        return;
    }

    session.initial_url = "/chat".to_string();
    if !session.launch_client(1440, 900, "launch-chat-live") || !session.activate() {
        return;
    }
    if !session.wait_text("chat-page", "对话", false, 20) {
        return;
    }

    // The composer stays disabled until the catalog scan has produced a model
    // list, so this lookup doubles as the "the scan finished" wait.
    let Some(composer) = session.find_ref("chat-composer", None, COMPOSER_LABEL, 60) else {
        return;
    };
    let typed = StepSpec::owned(
        "chat-type-prompt",
        "agent-desktop",
        vec!["type".to_string(), composer, PROMPT.to_string()],
    )
    .timeout(60);
    if !session.run(&[typed]) {
        return;
    }

    let Some(send) = session.find_ref("chat-send", Some("button"), "发送", 15) else {
        return;
    };
    let click = StepSpec::owned(
        "chat-click-send",
        "agent-desktop",
        vec!["click".to_string(), send],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }

    // A real turn: the runtime is asked, the text streams into the page, and the
    // ledger gets the assistant message. 90 attempts at ~2s each.
    if !session.wait_text("chat-reply", "ok", true, 90) {
        return;
    }
    if !session.screenshot("native/chat-live-1440x900.png") {
        return;
    }

    // The turn must be durable, not just rendered.
    let ledger = session.data_dir.join("sessions.sqlite");
    let exists = StepSpec::new(
        "chat-ledger",
        "self",
        &["exists", &format!("--path={}", ledger.display())],
    );
    if !session.run(&[exists]) {
        return;
    }

    if !session.close_client("chat-live") {
        return;
    }
    session.collect_app_log();
}
