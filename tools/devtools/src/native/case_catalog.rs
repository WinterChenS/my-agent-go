//! `R1-08-native-catalog`: discovery to display in the real desktop client.
//!
//! The case opens the settings page, switches to the model catalog, and requires
//! the rows the host actually found on this machine (a real model id from Pi's
//! catalog and Pi's real version). It also proves the catalog database was
//! created inside the D:-only data directory instead of somewhere else.

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::session::Session;

/// A model id Pi's catalogue publishes on this machine.
const REAL_MODEL: &str = "auto/gpt-5.6-sol";

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "catalog");
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
    if !session.reset_data_dirs("catalog")
        || !session.prepare_home()
        || !session.require_status()
        || !session.build_client()
        || !session.check_paths()
    {
        return;
    }

    session.initial_url = "/settings".to_string();
    if !session.launch_client(1440, 900, "launch-catalog") || !session.activate() {
        return;
    }
    if !session.wait_text("catalog-page", "系统设置", false, 10) {
        return;
    }

    // The models section asks the host for the catalog on mount.
    let Some(models_nav) = session.find_ref("catalog-nav-models", Some("button"), "模型管理", 15)
    else {
        return;
    };
    let click_models = StepSpec::owned(
        "catalog-open-models",
        "agent-desktop",
        vec!["click".to_string(), models_nav],
    )
    .timeout(60);
    if !session.run(&[click_models])
        || !session.wait_text("catalog-models-title", "模型目录", false, 25)
        || !session.wait_text("catalog-models-real", REAL_MODEL, false, 25)
    {
        return;
    }
    if !session.screenshot("native/catalog-models-1440x900.png") {
        return;
    }

    // The runtime section lists status, version and install path.
    let Some(runtime_nav) = session.find_ref("catalog-nav-runtime", Some("button"), "运行时", 15)
    else {
        return;
    };
    let click_runtime = StepSpec::owned(
        "catalog-open-runtimes",
        "agent-desktop",
        vec!["click".to_string(), runtime_nav],
    )
    .timeout(60);
    if !session.run(&[click_runtime])
        || !session.wait_text("catalog-runtimes-title", "本机运行时", false, 25)
        || !session.wait_text("catalog-pi-version", "1.5.0", false, 25)
        || !session.wait_text("catalog-pi-status", "可用", false, 25)
    {
        return;
    }
    if !session.screenshot("native/catalog-runtimes-1440x900.png") {
        return;
    }

    // Persistence must land inside the D:-only data directory.
    let database = session.data_dir.join("catalog.sqlite");
    let exists = StepSpec::new(
        "catalog-database",
        "self",
        &["exists", &format!("--path={}", database.display())],
    );
    if !session.run(&[exists]) {
        return;
    }

    if !session.close_client("catalog") {
        return;
    }
    session.collect_app_log();
}
