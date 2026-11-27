//! PIB02: the desktop entry points must run the bundled runtime.
//!
//! Three situations the plan calls out, all of them measured here rather than
//! argued about:
//!
//! 1. the machine has an external `pi` and `node` **first** on PATH;
//! 2. the process inherited an external `PI_CODING_AGENT_DIR`;
//! 3. the bundled tree is missing a file.
//!
//! In the first two the bundled entry point and the Anvil data directory must
//! win, and the external `pi` must never be executed — the fixture writes a
//! marker file when it is, so "we did not run it" is a fact, not an intention.
//! The third has to read as a bundled-runtime error, because "pi is not
//! installed" invites the user to install the very thing this build does not
//! use.
//!
//! The file is one test on purpose: it mutates process-wide environment values,
//! and a second test running beside it would read them.

use std::path::{Path, PathBuf};

use anvil_catalog::model::{InstallSource, RuntimeStatus};
use anvil_catalog::runtime::adapter::{DiscoveryContext, ProbeContext, RuntimeAdapter};
use anvil_catalog::runtime::adapters::pi::PiAdapter;
use anvil_catalog::runtime::bundled::BundledPi;

fn scratch(name: &str) -> PathBuf {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join(".tmp")
        .join("test")
        .join("bundled-pi-it")
        .join(name);
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create scratch");
    dir
}

fn write(path: &Path, bytes: &[u8]) {
    std::fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
    std::fs::write(path, bytes).expect("write");
}

/// A Node for the fixture: hard-linked from the one on PATH so the test does not
/// depend on a 90 MB copy, and so `--version` is a real process.
fn stage_node(root: &Path) -> PathBuf {
    let source = anvil_catalog::runtime::adapters::common::preferred_node()
        .expect("a node on PATH (the CI runner installs one)");
    let name = if cfg!(windows) { "node.exe" } else { "node" };
    let destination = root.join("node").join("bin").join(name);
    std::fs::create_dir_all(destination.parent().expect("dir")).expect("mkdir");
    if std::fs::hard_link(&source, &destination).is_err() {
        std::fs::copy(&source, &destination).expect("copy node");
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mut permissions = std::fs::metadata(&destination)
            .expect("metadata")
            .permissions();
        permissions.set_mode(0o755);
        std::fs::set_permissions(&destination, permissions).expect("chmod");
    }
    destination
}

/// Stage a tree the loader accepts, with a CLI that answers `--version`.
fn stage_bundle(root: &Path, version: &str) -> BundledPi {
    stage_node(root);
    write(&root.join("node/LICENSE"), b"MIT");
    let package_dir = "pi/node_modules/@earendil-works/pi-coding-agent";
    write(
        &root.join(package_dir).join("dist/bundle/cli.js"),
        format!(
            "if (process.argv.includes('--version')) {{ console.log('{version}'); process.exit(0); }}\n\
             console.log('rpc mode is not exercised here');\n"
        )
        .as_bytes(),
    );
    write(
        &root
            .join(package_dir)
            .join("node_modules/@earendil-works/pi-ai/dist/providers/all.js"),
        b"export function builtinProviders() { return []; }\n",
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
    let platform = anvil_catalog::runtime::bundled::host_platform();
    let arch = anvil_catalog::runtime::bundled::host_arch();
    let node = if cfg!(windows) {
        "node/bin/node.exe"
    } else {
        "node/bin/node"
    };
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

/// An external `pi` that records the fact it was started.
fn stage_external_pi(dir: &Path, marker: &Path) -> PathBuf {
    std::fs::create_dir_all(dir).expect("external dir");
    let marker = marker.display().to_string().replace('\\', "/");
    if cfg!(windows) {
        let shim = dir.join("pi.cmd");
        std::fs::write(
            &shim,
            format!("@ECHO off\r\nECHO ran>\"{marker}\"\r\nECHO 0.0.1\r\n"),
        )
        .expect("write shim");
        shim
    } else {
        let shim = dir.join("pi");
        std::fs::write(
            &shim,
            format!("#!/bin/sh\necho ran > '{marker}'\necho 0.0.1\n"),
        )
        .expect("write shim");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mut permissions = std::fs::metadata(&shim).expect("metadata").permissions();
            permissions.set_mode(0o755);
            std::fs::set_permissions(&shim, permissions).expect("chmod");
        }
        shim
    }
}

#[test]
fn the_bundled_runtime_wins_over_path_and_an_inherited_agent_dir() {
    let root = scratch("wins");
    let bundle = stage_bundle(&root.join("bundle"), "1.6.0");

    // The machine's own install, first on PATH, with its own agent directory.
    let external = root.join("external");
    let marker = root.join("external-pi-was-run.txt");
    let external_pi = stage_external_pi(&external, &marker);
    let external_agent = root.join("external-agent");
    std::fs::create_dir_all(&external_agent).expect("external agent");

    let path = std::env::var_os("PATH").unwrap_or_default();
    let mut dirs = vec![external.clone()];
    dirs.extend(std::env::split_paths(&path));
    let joined = std::env::join_paths(dirs).expect("join PATH");
    // SAFETY: this test binary has exactly one test, so nothing else reads or
    // writes the environment while it runs.
    unsafe {
        std::env::set_var("PATH", &joined);
        std::env::set_var("PI_CODING_AGENT_DIR", &external_agent);
    }

    // 1. Discovery reports the bundled runtime and only it.
    let adapter = PiAdapter::with_bundle(std::sync::Arc::new(bundle.clone()));
    let candidates = adapter
        .discover(&DiscoveryContext {
            home: root.join("home"),
            timeout: std::time::Duration::from_secs(5),
        })
        .expect("discover");
    assert_eq!(
        candidates.len(),
        1,
        "one install, the bundled one: {candidates:?}"
    );
    assert_eq!(candidates[0].source, InstallSource::Bundled);
    assert_eq!(candidates[0].executable, bundle.cli);
    assert_ne!(
        candidates[0].executable, external_pi,
        "an external pi must never become an execution path"
    );

    // 2. The probe runs `node <cli>` with the data directory, not the inherited
    //    agent directory, and reports the bundled version.
    std::fs::create_dir_all(&bundle.agent_dir).expect("data dir");
    std::fs::write(
        bundle.agent_dir.join("auth.json"),
        br#"{"deepseek":{"type":"oauth"}}"#,
    )
    .expect("credential in the data root");
    let report = adapter
        .probe(
            &ProbeContext {
                home: root.join("home"),
                timeout: std::time::Duration::from_secs(20),
            },
            &candidates[0],
        )
        .expect("probe");
    assert_eq!(report.version.as_deref(), Some("1.6.0"));
    assert_eq!(
        report.status,
        RuntimeStatus::Available,
        "{:?}",
        report.detail
    );
    assert_eq!(
        report.config_path.as_deref(),
        Some(bundle.agent_dir.as_path()),
        "the probe must read the data root, not the inherited directory"
    );

    // 3. The external pi never ran: no marker file, and the process that did run
    //    was the private Node (the probe would fail otherwise).
    assert!(
        !marker.exists(),
        "the external pi at {} was executed",
        external_pi.display()
    );

    // 4. The same holds for what a session would spawn.
    let command = bundle.command();
    assert_eq!(command.get_program(), bundle.node.as_os_str());
    let env: Vec<(String, Option<String>)> = command
        .get_envs()
        .map(|(key, value)| {
            (
                key.to_string_lossy().into_owned(),
                value.map(|value| value.to_string_lossy().into_owned()),
            )
        })
        .collect();
    let agent_env = env
        .iter()
        .find(|(key, _)| key == "PI_CODING_AGENT_DIR")
        .and_then(|(_, value)| value.clone())
        .expect("PI_CODING_AGENT_DIR is set");
    assert_eq!(
        Path::new(&agent_env),
        bundle.agent_dir,
        "the child must be told the data root explicitly"
    );
    assert_ne!(Path::new(&agent_env), external_agent);

    // 5. A damaged tree is a bundled-runtime error with the missing file named.
    std::fs::remove_file(&bundle.cli).expect("remove cli");
    let error = BundledPi::from_dir(&root.join("bundle"), &bundle.agent_dir)
        .expect_err("a tree without its CLI must not load");
    assert!(error.contains("incomplete"), "{error}");
    assert!(error.contains("cli.js"), "{error}");
    assert!(
        error.contains("reinstall Anvil"),
        "the message has to say what fixes it: {error}"
    );
}
