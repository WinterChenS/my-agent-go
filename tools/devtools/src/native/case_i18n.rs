//! `R0-03-native-i18n`: the real language switch.
//!
//! Ported from `internal/acceptance/runner_i18n_native.go`: default Chinese,
//! English navigation/breadcrumb/placeholder/accessibility/title/lang, a return
//! to Chinese, both viewports, and English persistence across a restart.

use std::time::Duration;

use crate::hash::hash_artifact;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::parse;
use super::session::Session;

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "i18n");
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
    if !session.reset_data_dirs("i18n")
        || !session.prepare_home()
        || !session.require_status()
        || !session.build_client()
        || !session.check_paths()
    {
        return;
    }
    let repo = session.repo();
    let routes = match parse::read_shell_routes(&repo) {
        Ok(routes) => routes,
        Err(err) => {
            session.fail(&err);
            return;
        }
    };
    let Some(settings) = routes.iter().find(|route| route.id == "settings").cloned() else {
        session.fail("settings route is missing from routes.json");
        return;
    };

    if !launch_with_language(session, 390, 844, "launch-i18n-390", "zh-CN")
        || !session.activate()
        || !session.navigate(&settings, "i18n-zh")
        || !assert_chinese_ui(session, "zh")
        || !session.screenshot("native/i18n-zh-390x844.png")
    {
        return;
    }

    // Chinese -> English through the actual language radio control.
    if !session.click_language_option(4, "i18n-en-390", "en-US")
        || !assert_english_ui(session, "en")
        || !session.screenshot("native/i18n-en-390x844.png")
    {
        return;
    }

    // Back to Chinese for the second half of the viewport pair.
    if !session.click_language_option(3, "i18n-zh-back", "zh-CN") {
        return;
    }
    if !session.wait_title("设置 · Anvil", Duration::from_secs(10)) {
        session.fail("Chinese document title was not restored");
        return;
    }
    if !session.resize(1440, 900, true) || !session.screenshot("native/i18n-zh-1440x900.png") {
        return;
    }
    if !session.click_language_option(4, "i18n-en-wide", "en-US")
        || !assert_english_ui(session, "en-wide")
        || !session.screenshot("native/i18n-en-1440x900.png")
    {
        return;
    }

    // English must survive a restart, and the preferences file must say so.
    if !session.close_client("i18n")
        || !launch_with_language(session, 390, 844, "relaunch-i18n-en", "en-US")
        || !session.activate()
        || !assert_english_home(session, "restart")
        || !session.screenshot("native/i18n-restart-en-390x844.png")
    {
        return;
    }
    let preferences = session.data_dir.join("preferences.json");
    match std::fs::read(&preferences) {
        Ok(body) => {
            let language = parse::parse_language_preference(&body);
            if language.as_deref() != Some("en-US") {
                session.fail(&format!(
                    "preferences.json language = {:?}",
                    language.unwrap_or_default()
                ));
                return;
            }
            let destination = session.case_dir().join("native").join("preferences.json");
            if let Err(err) = std::fs::write(&destination, &body) {
                session.fail(&format!("copy preferences.json: {err}"));
                return;
            }
            match hash_artifact(&session.case_dir(), "native/preferences.json") {
                Ok(artifact) => session.out.artifacts.push(artifact),
                Err(err) => {
                    session.fail(&format!("hash preferences.json: {err}"));
                    return;
                }
            }
        }
        Err(err) => {
            session.fail(&format!("read preferences.json after restart: {err}"));
            return;
        }
    }

    if !session.close_client("i18n-en") {
        return;
    }
    session.collect_app_log();
}

/// Launch and require the frontend to report `expected` as its language.
fn launch_with_language(
    session: &mut Session<'_>,
    width: i64,
    height: i64,
    label: &str,
    expected: &str,
) -> bool {
    let before = session.i18n_entries().len();
    if !session.launch_client(width, height, label) {
        return false;
    }
    let Some(entry) = session.wait_language_since(before, expected, Duration::from_secs(20)) else {
        session.fail(&format!(
            "frontend did not report language {expected} after launch"
        ));
        return false;
    };
    session.last_launch_language = entry;
    true
}

fn assert_chinese_ui(session: &mut Session<'_>, log_id: &str) -> bool {
    let entry = session.last_launch_language.clone();
    if entry.language != "zh-CN" || entry.html_lang != "zh-CN" {
        session.fail(&format!(
            "initial language = {} / {}, want zh-CN",
            entry.language, entry.html_lang
        ));
        return false;
    }
    if !session.wait_text(&format!("{log_id}-nav"), "工作台", false, 10)
        || !session.wait_text(&format!("{log_id}-crumb"), "系统 / 设置", false, 10)
        || !session.wait_text(&format!("{log_id}-placeholder"), "路由占位", false, 10)
    {
        return false;
    }
    if session
        .find_ref(
            &format!("{log_id}-workbench-link"),
            Some("link"),
            "工作台",
            10,
        )
        .is_none()
    {
        return false;
    }
    if session
        .find_ref(&format!("{log_id}-search"), Some("button"), "搜索", 10)
        .is_none()
    {
        return false;
    }
    if !session.wait_title("设置 · Anvil", Duration::from_secs(10)) {
        session.fail("Chinese document title was not observed");
        return false;
    }
    true
}

fn assert_english_ui(session: &mut Session<'_>, log_id: &str) -> bool {
    let Some(entry) = session.wait_language("en-US", Duration::from_secs(10)) else {
        session.fail("English language was not reported");
        return false;
    };
    if entry.html_lang != "en-US" {
        session.fail(&format!(
            "English html lang = {:?}, want en-US",
            entry.html_lang
        ));
        return false;
    }
    if !session.wait_text(&format!("{log_id}-nav"), "Dashboard", false, 10)
        || !session.wait_text(&format!("{log_id}-crumb"), "System / Settings", false, 10)
        || !session.wait_text(
            &format!("{log_id}-placeholder"),
            "Route placeholder",
            false,
            10,
        )
        || !session.wait_text(
            &format!("{log_id}-status"),
            "Runtime not connected",
            false,
            10,
        )
    {
        return false;
    }
    if session
        .find_ref(
            &format!("{log_id}-dashboard-link"),
            Some("link"),
            "Dashboard",
            10,
        )
        .is_none()
    {
        return false;
    }
    if session
        .find_ref(&format!("{log_id}-search"), Some("button"), "Search", 10)
        .is_none()
    {
        return false;
    }
    if !session.wait_title("Settings · Anvil", Duration::from_secs(10)) {
        session.fail("English document title was not observed");
        return false;
    }
    true
}

fn assert_english_home(session: &mut Session<'_>, log_id: &str) -> bool {
    let Some(entry) = session.wait_language("en-US", Duration::from_secs(10)) else {
        session.fail("English language was not reported after restart");
        return false;
    };
    if entry.html_lang != "en-US" {
        session.fail(&format!(
            "English language/html lang = {} / {}",
            entry.language, entry.html_lang
        ));
        return false;
    }
    if !session.wait_text(&format!("{log_id}-nav"), "Dashboard", false, 10) {
        return false;
    }
    if session
        .find_ref(
            &format!("{log_id}-dashboard-link"),
            Some("link"),
            "Dashboard",
            10,
        )
        .is_none()
    {
        return false;
    }
    if !session.wait_title("Dashboard · Anvil", Duration::from_secs(10)) {
        session.fail("English home document title was not observed");
        return false;
    }
    true
}
