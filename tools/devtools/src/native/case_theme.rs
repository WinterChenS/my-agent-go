//! `R0-02-native-theme`: the real theme pipeline.
//!
//! Ported from `internal/acceptance/runner_theme_native.go`: the settings
//! control, dark/light/system rendering, real `HKCU\…\Personalize` flips while
//! in system mode (follows) and manual mode (ignores), and restart persistence.
//! The registry value is restored on every exit path, including failures.

use std::time::Duration;

use serde::Serialize;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::parse;
use super::session::Session;
use super::{NativeOutcome, write_json_artifact};

#[derive(Debug, Default, Serialize)]
struct RegistryEvidence {
    original: i64,
    #[serde(rename = "systemFlip")]
    system_flip: i64,
    #[serde(rename = "manualLightFlip")]
    manual_light_flip: i64,
    #[serde(rename = "manualDarkFlip")]
    manual_dark_flip: i64,
    restored: i64,
    #[serde(rename = "restoredOK")]
    restored_ok: bool,
}

/// Tracks the Windows value the case must put back.
#[derive(Debug, Default)]
struct RegistryGuard {
    original: Option<i64>,
    restored: bool,
}

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "theme");
    let mut guard = RegistryGuard::default();
    if session.ensure_native_dir().is_ok() {
        run_case(&mut session, &mut guard);
    }
    restore_registry(&mut session, &mut guard);
    if session.out.status.is_empty() {
        session.pass()
    } else {
        session.fail_cleanup()
    }
}

/// Put the user's Windows theme back, whatever happened.
fn restore_registry(session: &mut Session<'_>, guard: &mut RegistryGuard) {
    let Some(original) = guard.original else {
        return;
    };
    if guard.restored {
        return;
    }
    guard.restored = registry_set(session, original, "registry-restore-on-exit");
    if !guard.restored && session.out.status.is_empty() {
        session.fail("failed to restore AppsUseLightTheme to its original value");
    }
}

fn run_case(session: &mut Session<'_>, guard: &mut RegistryGuard) {
    if !session.reset_data_dirs("theme")
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

    if !session.launch_client(390, 844, "launch-theme-390")
        || !session.activate()
        || !session.navigate(&settings, "theme")
    {
        return;
    }
    let system_entry = session.last_launch_theme.clone();
    if system_entry.mode != "system" {
        session.fail(&format!(
            "initial theme was not system: mode={} resolved={}",
            system_entry.mode, system_entry.resolved
        ));
        return;
    }
    if !session.screenshot("native/theme-system-390x844.png") {
        return;
    }
    if session
        .click_theme_option(1, "theme-light-390", "light")
        .is_none()
        || !session.screenshot("native/theme-light-390x844.png")
    {
        return;
    }
    if session
        .click_theme_option(2, "theme-dark-390", "dark")
        .is_none()
        || !session.screenshot("native/theme-dark-390x844.png")
    {
        return;
    }

    let Some(original) = registry_query(session, "registry-query-original") else {
        session.fail("could not read AppsUseLightTheme; system-follow evidence unavailable");
        return;
    };
    guard.original = Some(original);
    let system_flip = 1 - original;
    let expected_flip = if system_flip == 1 { "light" } else { "dark" };
    let expected_original = if original == 1 { "light" } else { "dark" };

    if session
        .click_theme_option(0, "theme-system-flip", "system")
        .is_none()
    {
        return;
    }
    if !registry_set(session, system_flip, "registry-flip-system") {
        return;
    }
    if session
        .wait_theme_for("system", expected_flip, Duration::from_secs(15))
        .is_none()
    {
        session.fail(&format!(
            "system mode did not follow AppsUseLightTheme={system_flip}"
        ));
        return;
    }
    if !registry_set(session, original, "registry-restore-system") {
        return;
    }
    if session
        .wait_theme_for("system", expected_original, Duration::from_secs(15))
        .is_none()
    {
        session.fail("system mode did not return to the original Windows theme");
        return;
    }

    // Manual light ignores a system flip to dark.
    if session
        .click_theme_option(1, "theme-light-manual", "light")
        .is_none()
    {
        return;
    }
    if !registry_set(session, 0, "registry-flip-manual-light") {
        return;
    }
    std::thread::sleep(Duration::from_millis(2500));
    match session.latest_theme() {
        Some(entry) if entry.mode == "light" && entry.resolved == "light" => {}
        other => {
            session.fail(&format!("manual light followed a system flip: {other:?}"));
            return;
        }
    }
    if !registry_set(session, original, "registry-restore-manual-light") {
        return;
    }

    // Manual dark ignores a system flip to light.
    if session
        .click_theme_option(2, "theme-dark-manual", "dark")
        .is_none()
    {
        return;
    }
    if !registry_set(session, 1, "registry-flip-manual-dark") {
        return;
    }
    std::thread::sleep(Duration::from_millis(2500));
    match session.latest_theme() {
        Some(entry) if entry.mode == "dark" && entry.resolved == "dark" => {}
        other => {
            session.fail(&format!("manual dark followed a system flip: {other:?}"));
            return;
        }
    }
    if !registry_set(session, original, "registry-restore-manual-dark") {
        return;
    }

    // Wide screenshots for all three modes.
    if !session.resize(1440, 900, true) {
        return;
    }
    if session
        .click_theme_option(0, "theme-system-wide", "system")
        .is_none()
        || !session.screenshot("native/theme-system-1440x900.png")
    {
        return;
    }
    if session
        .click_theme_option(1, "theme-light-wide", "light")
        .is_none()
        || !session.screenshot("native/theme-light-1440x900.png")
    {
        return;
    }
    if session
        .click_theme_option(2, "theme-dark-wide", "dark")
        .is_none()
        || !session.screenshot("native/theme-dark-1440x900.png")
    {
        return;
    }

    // Restart persistence: dark survives a relaunch.
    if !session.close_client("theme") || !session.launch_client(390, 844, "relaunch-dark-390") {
        return;
    }
    if !session.activate() {
        return;
    }
    let restart = session.last_launch_theme.clone();
    if restart.mode != "dark" || restart.resolved != "dark" {
        session.fail(&format!(
            "dark theme did not survive a restart: mode={} resolved={}",
            restart.mode, restart.resolved
        ));
        return;
    }
    if !session.screenshot("native/theme-restart-dark-390x844.png") {
        return;
    }
    if !session.navigate(&settings, "theme-restart") {
        return;
    }
    if session
        .click_theme_option(0, "theme-system-restart", "system")
        .is_none()
    {
        return;
    }
    if !session.close_client("theme-restart") {
        return;
    }

    // Restart persistence: system survives another relaunch.
    if !session.launch_client(390, 844, "relaunch-system-390") || !session.activate() {
        return;
    }
    let entry = session.last_launch_theme.clone();
    if entry.mode != "system" || entry.resolved != expected_original {
        session.fail(&format!(
            "system theme did not survive a restart: mode={} resolved={}",
            entry.mode, entry.resolved
        ));
        return;
    }
    if !session.close_client("theme-system") {
        return;
    }

    // Restore explicitly before writing evidence.
    if !registry_set(session, original, "registry-restore-explicit") {
        session.fail("failed to restore AppsUseLightTheme");
        return;
    }
    let restored_value = registry_query(session, "registry-query-restored").unwrap_or(-1);
    guard.restored = restored_value == original;
    let evidence = RegistryEvidence {
        original,
        system_flip,
        manual_light_flip: 0,
        manual_dark_flip: 1,
        restored: restored_value,
        restored_ok: guard.restored,
    };
    let case_dir = session.case_dir();
    match write_json_artifact(&case_dir, "native/registry.json", &evidence) {
        Ok(artifact) => session.out.artifacts.push(artifact),
        Err(err) => session.fail(&format!("write registry evidence: {err}")),
    }
    if !guard.restored {
        session.fail(&format!(
            "AppsUseLightTheme restore mismatch: original={original} restored={restored_value}"
        ));
        return;
    }
    session.collect_app_log();
}

/// Read `AppsUseLightTheme` through the devtools registry command.
fn registry_query(session: &mut Session<'_>, name: &str) -> Option<i64> {
    let step = StepSpec::owned(
        name,
        "self",
        vec!["registry".to_string(), "--get".to_string()],
    )
    .timeout(60);
    if !session.run(&[step]) {
        return None;
    }
    let value = parse::parse_registry_value(&session.read(name));
    if value.is_none() {
        session.fail("could not parse the AppsUseLightTheme reply");
    }
    value
}

/// Write `AppsUseLightTheme` through the devtools registry command.
fn registry_set(session: &mut Session<'_>, value: i64, name: &str) -> bool {
    let step = StepSpec::owned(
        name,
        "self",
        vec!["registry".to_string(), format!("--set={value}")],
    )
    .timeout(60);
    session.run(&[step])
}
