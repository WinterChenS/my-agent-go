//! PIB03: a damaged bundled runtime must not fall back to the machine's `pi`.
//!
//! ADR 0009 makes the installer the only source of Pi. When the tree it shipped
//! cannot be loaded, the previous code path had no bundle to hand to the
//! catalogue, so the registry fell back to `PiAdapter::system()` — which scans
//! `PATH`. On a machine that already had an external `pi` the app would then
//! offer, discover and run *that* one, which is precisely the behaviour the ADR
//! removed.
//!
//! The fixture puts an external `pi` first on `PATH` and makes it write a marker
//! file the moment it is executed, so "the external pi never became a candidate"
//! is a fact about this process, not a claim about the code.
//!
//! One test on purpose: it mutates process-wide environment values.

use std::path::{Path, PathBuf};

use anvil_catalog::runtime::adapters::registry_for_pi;
use anvil_catalog::runtime::service::Timeouts;
use anvil_catalog::{CatalogService, Store};

fn scratch(name: &str) -> PathBuf {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join(".tmp")
        .join("test")
        .join("damaged-pi")
        .join(name);
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create scratch");
    dir
}

/// An external `pi` that records the fact it was started.
fn stage_external_pi(dir: &Path, marker: &Path) -> PathBuf {
    std::fs::create_dir_all(dir).expect("external dir");
    let marker = marker.display().to_string().replace('\\', "/");
    if cfg!(windows) {
        let shim = dir.join("pi.cmd");
        std::fs::write(
            &shim,
            format!("@ECHO off\r\nECHO ran>\"{marker}\"\r\nECHO 1.6.0\r\n"),
        )
        .expect("write shim");
        shim
    } else {
        let shim = dir.join("pi");
        std::fs::write(
            &shim,
            format!("#!/bin/sh\necho ran > '{marker}'\necho 1.6.0\n"),
        )
        .expect("write shim");
        {
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                let mut permissions = std::fs::metadata(&shim).expect("metadata").permissions();
                permissions.set_mode(0o755);
                std::fs::set_permissions(&shim, permissions).expect("chmod");
            }
        }
        shim
    }
}

#[test]
fn a_damaged_bundle_never_falls_back_to_an_external_pi() {
    let root = scratch("fallback");
    let external = root.join("external");
    let marker = root.join("external-pi-was-run.txt");
    let external_pi = stage_external_pi(&external, &marker);

    let path = std::env::var_os("PATH").unwrap_or_default();
    let mut dirs = vec![external.clone()];
    dirs.extend(std::env::split_paths(&path));
    let joined = std::env::join_paths(dirs).expect("join PATH");
    // SAFETY: this test binary has exactly one test, so nothing else reads or
    // writes the environment while it runs.
    unsafe {
        std::env::set_var("PATH", &joined);
    }

    // The reason the host would hold after `BundledPi::load` failed.
    let reason = "the bundled Pi runtime is incomplete: cli.js is missing; reinstall Anvil";
    let registry = registry_for_pi(None, Some(reason)).expect("registry");
    let adapter = registry.get("pi").expect("pi stays registered");
    assert_eq!(
        adapter.unavailable_reason(),
        Some(reason),
        "the registry has to know why there is no pi"
    );

    // 1. Discovery offers nothing at all — not the bundled runtime, and not the
    //    machine's.
    let candidates = adapter
        .discover(&anvil_catalog::runtime::adapter::DiscoveryContext {
            home: root.join("home"),
            timeout: std::time::Duration::from_secs(5),
        })
        .expect("discover");
    assert!(
        candidates.is_empty(),
        "a damaged bundle offers no candidate: {candidates:?}"
    );
    assert!(
        !candidates
            .iter()
            .any(|candidate| candidate.executable == external_pi),
        "the external pi must never become a candidate"
    );

    // 2. A full scan commits no runnable pi: every row for pi has an empty
    //    install path, so nothing can be started from the runtime page.
    let home = root.join("home");
    std::fs::create_dir_all(&home).expect("home");
    let store = Store::open(&root.join("catalog.sqlite")).expect("store");
    let mut service =
        CatalogService::with_registry(store, home, registry, Timeouts::default()).expect("service");
    service.scan().expect("scan");
    let snapshot = service.snapshot().expect("snapshot");
    let pi_rows: Vec<_> = snapshot
        .runtimes
        .iter()
        .filter(|row| row.runtime_id == "pi")
        .collect();
    assert!(
        pi_rows
            .iter()
            .all(|row| row.install_path.is_empty() && row.version.is_none()),
        "no pi installation may be offered: {pi_rows:?}"
    );
    for status in ["available", "detected", "probe_failed", "unauthenticated"] {
        assert!(
            !pi_rows.iter().any(|row| row.status == status),
            "a damaged bundle must not report {status}: {pi_rows:?}"
        );
    }
    // What it does report is the bundle's own error, so the page has something
    // to show instead of "pi is not installed" — which would invite the reader
    // to install the very thing this build does not use.
    let broken = pi_rows
        .iter()
        .find(|row| row.status == "adapter_failed")
        .expect("the damaged runtime has to be reported");
    assert_eq!(
        broken.health.detail.as_deref(),
        Some(reason),
        "the row carries the runtime's own words"
    );

    // 3. Nothing spawns it: the fixture would have left a marker file.
    assert!(
        !marker.exists(),
        "the external pi at {} was executed",
        external_pi.display()
    );

    let _ = std::fs::remove_dir_all(&root);
}
