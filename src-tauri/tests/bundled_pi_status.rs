//! PIB03: the backend surfaces the settings page needs for the bundled runtime.
//!
//! Four things are measured here, all of them without a window:
//!
//! 1. `pi_runtime_status` reads a staged bundle field by field, and a tree whose
//!    CLI is gone reads as **damaged** with the loader's own words — never as
//!    "pi is not installed";
//! 2. `runtime_install("pi")` is refused in both of those states, so an old page
//!    cannot run `npm install -g` against the user's machine;
//! 3. the Pi update check follows the Anvil installer instead of the npm
//!    registry;
//! 4. the legacy `~/.pi` import copies only what its allowlist names, and a
//!    second run copies nothing.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

use anvil::bundled::BundledPiStatus;
use anvil::catalog::bundled_pi_install_refusal;
use anvil::pi_import;
use anvil::runtimes::bundled_update_check;
use anvil_catalog::runtime::bundled::{BundledPi, host_arch, host_platform};

static UNIQUE: AtomicU64 = AtomicU64::new(0);

fn scratch(name: &str) -> PathBuf {
    let unique = UNIQUE.fetch_add(1, Ordering::SeqCst);
    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join(".tmp")
        .join("test")
        .join("pib03")
        .join(format!("{name}-{unique}"));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create scratch");
    dir
}

fn write(path: &Path, bytes: &[u8]) {
    std::fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
    std::fs::write(path, bytes).expect("write");
}

/// The smallest tree `BundledPi::from_dir` accepts. Nothing here is executed:
/// the loader only checks that the entry points exist.
fn stage_bundle(root: &Path, version: &str) -> BundledPi {
    let package_dir = "pi/node_modules/@earendil-works/pi-coding-agent";
    let node = if cfg!(windows) {
        "node/node.exe"
    } else {
        "node/node"
    };
    write(&root.join(node), b"");
    write(&root.join("node/LICENSE"), b"MIT");
    write(
        &root.join(package_dir).join("dist/bundle/cli.js"),
        b"// cli\n",
    );
    write(
        &root
            .join(package_dir)
            .join("node_modules/@earendil-works/pi-ai/dist/providers/all.js"),
        b"export {};\n",
    );
    write(
        &root
            .join(package_dir)
            .join("examples/extensions/subagent/index.ts"),
        b"//\n",
    );
    std::fs::create_dir_all(
        root.join(package_dir)
            .join("examples/extensions/subagent/agents"),
    )
    .expect("agents");
    std::fs::create_dir_all(
        root.join(package_dir)
            .join("examples/extensions/subagent/prompts"),
    )
    .expect("prompts");
    let platform = host_platform();
    let arch = host_arch();
    write(
        &root.join("manifest.json"),
        format!(
            r#"{{
  "schema": 1,
  "target": "{platform}-{arch}",
  "platform": "{platform}",
  "arch": "{arch}",
  "nodeVersion": "24.16.0",
  "node": "{node}",
  "piPackage": "@earendil-works/pi-coding-agent",
  "piVersion": "{version}",
  "cli": "{package_dir}/dist/bundle/cli.js",
  "piAi": "{package_dir}/node_modules/@earendil-works/pi-ai/dist/providers/all.js",
  "subagentSource": "{package_dir}/examples/extensions/subagent",
  "packageDir": "{package_dir}",
  "licenses": ["node/LICENSE"]
}}"#
        )
        .as_bytes(),
    );
    let data = root.join("data");
    BundledPi::from_dir(root, &data).expect("the staged tree must load")
}

#[test]
fn a_loaded_bundle_reports_bundled_with_its_own_paths() {
    let root = scratch("status-ok");
    let bundle = stage_bundle(&root, "1.6.0");
    let status = BundledPiStatus::ready(&bundle, &bundle.agent_dir);
    assert_eq!(status.state, "bundled");
    assert_eq!(status.version.as_deref(), Some("1.6.0"));
    assert_eq!(status.node_version.as_deref(), Some("24.16.0"));
    assert_eq!(status.install_root, root.display().to_string());
    assert_eq!(status.node_path, bundle.node.display().to_string());
    assert_eq!(status.cli_path, bundle.cli.display().to_string());
    assert_eq!(status.agent_dir, bundle.agent_dir.display().to_string());
    assert_eq!(
        status.skill_root,
        bundle.agent_dir.join("skill").display().to_string()
    );
    assert_eq!(status.data_dir, bundle.agent_dir.display().to_string());
    assert!(status.error.is_none());

    // The page renders these names directly, so the wire shape is part of the
    // contract and is checked rather than assumed.
    let json = serde_json::to_value(&status).expect("serialize");
    for key in [
        "state",
        "version",
        "nodeVersion",
        "installRoot",
        "nodePath",
        "cliPath",
        "agentDir",
        "skillRoot",
        "dataDir",
        "error",
    ] {
        assert!(json.get(key).is_some(), "missing {key} in {json}");
    }
    assert_eq!(json["state"], "bundled");
    let _ = std::fs::remove_dir_all(&root);
}

/// The PIB03-E shape: an install directory with the CLI removed. The answer is
/// "damaged, reinstall Anvil", because "not installed" would send the reader to
/// install the runtime this build carries.
#[test]
fn a_bundle_without_its_cli_reports_damaged_with_the_loaders_words() {
    let root = scratch("status-damaged");
    let bundle = stage_bundle(&root, "1.6.0");
    let cli = bundle.cli.clone();
    std::fs::remove_file(&cli).expect("remove cli");

    let error = BundledPi::from_dir(&root, &bundle.agent_dir).expect_err("must not load");
    assert!(error.contains("reinstall Anvil"), "{error}");
    let status = BundledPiStatus::damaged(&error, &root.display().to_string(), &bundle.agent_dir);
    assert_eq!(status.state, "damaged");
    assert_eq!(status.error.as_deref(), Some(error.as_str()));
    assert!(
        status.error.as_deref().is_some_and(|text| {
            text.contains("reinstall Anvil") || text.contains("重新安装")
        }),
        "the page needs an instruction it can show: {:?}",
        status.error
    );
    assert!(status.version.is_none());
    assert!(status.node_version.is_none());
    assert!(status.cli_path.is_empty(), "no path to a file that is gone");
    assert_eq!(status.data_dir, bundle.agent_dir.display().to_string());

    let json = serde_json::to_value(&status).expect("serialize");
    assert_eq!(json["state"], "damaged");
    assert!(json["error"].is_string());
    let _ = std::fs::remove_dir_all(&root);
}

/// ADR 0009: an old page's 一键安装 must not reach `npm install -g`. Both the
/// working and the damaged case are refused, and the refusal names what fixes
/// each one; only a host that never had a bundle (development, tests) keeps the
/// old discover-and-install path.
#[test]
fn installing_pi_is_refused_for_a_bundled_install() {
    let root = scratch("refusal");
    let bundle = stage_bundle(&root, "1.6.0");

    let bundled = bundled_pi_install_refusal(Some(&bundle), None).expect("refused");
    assert!(bundled.contains("1.6.0"), "names the version: {bundled}");
    assert!(bundled.contains("已内置"), "{bundled}");
    assert!(bundled.contains("重新安装 Anvil"), "{bundled}");

    let reason = "the bundled Pi runtime is missing (manifest.json): not found";
    let damaged = bundled_pi_install_refusal(None, Some(reason)).expect("refused");
    assert!(damaged.contains("不可用"), "{damaged}");
    assert!(damaged.contains(reason), "keeps the reason: {damaged}");
    assert!(damaged.contains("重新安装 Anvil"), "{damaged}");
    assert!(
        damaged.contains("不会通过 npm 安装"),
        "says npm is not the answer: {damaged}"
    );

    assert!(
        bundled_pi_install_refusal(None, None).is_none(),
        "a development host with no install resources keeps the old path"
    );
    let _ = std::fs::remove_dir_all(&root);
}

/// Upgrading pi means installing a newer Anvil: the check reports the app's
/// release, not `npm view @earendil-works/pi-coding-agent`.
#[test]
fn the_pi_update_check_follows_the_anvil_installer() {
    let newer = bundled_update_check("pi", Some("1.6.0"), Some("1.7.0"), true);
    assert_eq!(newer.runtime_id, "pi");
    assert_eq!(newer.installed.as_deref(), Some("1.6.0"));
    assert_eq!(newer.latest.as_deref(), Some("1.7.0"));
    assert!(newer.update_available);
    assert!(
        newer.package.contains("Anvil"),
        "the update arrives with the Anvil installer: {}",
        newer.package
    );
    assert!(newer.error.is_none());

    let current = bundled_update_check("pi", Some("1.6.0"), Some("1.6.0"), false);
    assert!(!current.update_available);
    assert_eq!(current.latest.as_deref(), Some("1.6.0"));

    // A damaged bundle has no version to compare; the app's answer still stands.
    let unknown = bundled_update_check("pi", None, None, false);
    assert!(unknown.installed.is_none());
    assert!(!unknown.update_available);
    assert!(unknown.error.is_none());
}

// ---------------------------------------------------------------------------
// Legacy `~/.pi` import
// ---------------------------------------------------------------------------

fn legacy_pi(home: &Path) {
    let agent = home.join(".pi").join("agent");
    write(&agent.join("models.json"), br#"{"providers":{}}"#);
    write(&agent.join("settings.json"), br#"{"defaultModel":"m"}"#);
    write(&agent.join("auth.json"), br#"{"deepseek":{"type":"oauth"}}"#);
    write(&agent.join("sessions/old.jsonl"), b"{}\n");
    write(&agent.join("logs/anvil.log"), b"log\n");
    write(&agent.join("extensions/keep/index.ts"), b"//\n");
    write(&home.join(".pi").join("skills").join("writer").join("SKILL.md"), b"# writer\n");
}

#[test]
fn the_import_plan_reads_the_legacy_home_and_the_anvil_data_root() {
    let root = scratch("import-plan");
    let home = root.join("home");
    let data = root.join("data");
    legacy_pi(&home);

    let plan = pi_import::plan_in(&home, &data);
    assert!(plan.available, "{plan:?}");
    assert_eq!(
        plan.agent_source,
        home.join(".pi").join("agent").display().to_string()
    );
    assert_eq!(
        plan.skill_source,
        home.join(".pi").join("skills").display().to_string()
    );
    assert_eq!(plan.agent_target, data.display().to_string());
    assert_eq!(
        plan.skill_target,
        data.join("skill").display().to_string()
    );
    let relatives: Vec<&str> = plan
        .items
        .iter()
        .map(|item| item.relative.as_str())
        .collect();
    for kept in ["models.json", "settings.json", "extensions", "writer"] {
        assert!(relatives.contains(&kept), "{kept} missing from {relatives:?}");
    }
    for never in ["auth.json", "sessions", "logs"] {
        assert!(
            !relatives
                .iter()
                .any(|name| *name == never || name.starts_with(&format!("{never}/"))),
            "{never} must never be imported: {relatives:?}"
        );
    }

    // The page renders the plan directly.
    let json = serde_json::to_value(&plan).expect("serialize");
    for key in [
        "agentSource",
        "skillSource",
        "agentTarget",
        "skillTarget",
        "available",
        "items",
    ] {
        assert!(json.get(key).is_some(), "missing {key} in {json}");
    }
    assert!(json["items"][0].get("relative").is_some());
    let _ = std::fs::remove_dir_all(&root);
}

#[test]
fn running_the_import_twice_copies_once() {
    let root = scratch("import-run");
    let home = root.join("home");
    let data = root.join("data");
    legacy_pi(&home);
    // A file the user already has in the Anvil data root is theirs.
    write(&data.join("models.json"), br#"{"edited":true}"#);

    let first = pi_import::run_in(&home, &data);
    assert!(first.failed.is_empty(), "{:?}", first.failed);
    assert!(first.copied > 0, "{first:?}");
    assert!(
        data.join("skill/writer/SKILL.md").is_file(),
        "skills are imported under <data_dir>/skill"
    );
    assert!(!data.join("auth.json").exists(), "credentials stay behind");
    assert!(!data.join("sessions").exists(), "history stays behind");
    assert!(!data.join("logs").exists(), "logs stay behind");
    assert_eq!(
        std::fs::read_to_string(data.join("models.json")).expect("read"),
        r#"{"edited":true}"#,
        "an existing target is never overwritten"
    );

    let second = pi_import::run_in(&home, &data);
    assert_eq!(
        second.copied, 0,
        "a second import has nothing left to copy: {second:?}"
    );
    assert!(second.failed.is_empty(), "{:?}", second.failed);

    let empty = pi_import::run_in(&root.join("nobody"), &data);
    assert_eq!(empty.copied, 0, "{empty:?}");
    let _ = std::fs::remove_dir_all(&root);
}
