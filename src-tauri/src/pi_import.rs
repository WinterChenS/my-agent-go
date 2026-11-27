//! Bringing an old `~/.pi` install into the Anvil data root (PIB03).
//!
//! ADR 0009 moved the runtime's agent directory to `HostConfig.data_dir` and its
//! user skills to `<data_dir>/skill`. A user upgrading from a build that borrowed
//! their `~/.pi` therefore has configuration and skills in a place the bundled
//! runtime never reads, and the only honest way to move them is to ask first.
//!
//! This module is the host side of that question:
//!
//! * [`plan_in`] is read-only and answers "what would be copied, and what is
//!   already there" — the page shows it before the user says yes;
//! * [`run_in`] **re-plans** on the server and copies only what is still new, so
//!   a plan that sat in the WebView for a minute cannot overwrite a file the user
//!   created in the meantime;
//! * both are idempotent: a second run copies nothing, and reports why.
//!
//! The allowlist and the never-copy list live in
//! [`anvil_catalog::runtime::pi_import`]: `auth.json`, `sessions` and `logs` are
//! never touched, because credentials are re-entered through the normal sign-in
//! and conversation history belongs to the ledger that wrote it.

use tauri::State;

use anvil_catalog::runtime::pi_import::{self, Plan, Report};

use crate::state::HostState;

/// Default source of an old install: `~/.pi/agent` and `~/.pi/skills`.
///
/// Resolved per call rather than stored: the home directory is what the process
/// reports, and an isolated test rig sets it to a scratch directory.
#[must_use]
pub fn legacy_roots() -> (std::path::PathBuf, std::path::PathBuf) {
    let home = anvil_catalog::runtime::adapters::common::home();
    (
        pi_import::legacy_agent_dir(&home),
        pi_import::legacy_skill_root(&home),
    )
}

/// What an import from `home`'s `~/.pi` into `data_dir` would do.
#[must_use]
pub fn plan_in(home: &std::path::Path, data_dir: &std::path::Path) -> Plan {
    pi_import::plan(
        &pi_import::legacy_agent_dir(home),
        &pi_import::legacy_skill_root(home),
        data_dir,
        &data_dir.join("skill"),
    )
}

/// Copy the items an import would add, re-planning first.
///
/// Returns what actually happened: `copied`, `skipped` and the items that failed
/// with their reasons, so a retry can be offered for the ones that are left.
#[must_use]
pub fn run_in(home: &std::path::Path, data_dir: &std::path::Path) -> Report {
    pi_import::run(&plan_in(home, data_dir))
}

/// The legacy install this machine has, and what importing it would copy.
///
/// Read-only. `available: false` with an empty `items` is the normal answer on a
/// machine that never ran the old build, and it is not an error.
#[tauri::command]
pub fn pi_import_plan(host: State<'_, HostState>) -> Plan {
    let (agent, skills) = legacy_roots();
    pi_import::plan(
        &agent,
        &skills,
        &host.config.data_dir,
        &host.config.data_dir.join("skill"),
    )
}

/// Copy the legacy configuration and skills the plan marks as new.
///
/// The plan is recomputed here on purpose: the page's copy may be a minute old,
/// and the files in the data directory are the ones the user has been editing
/// since. Existing targets are skipped, never overwritten, so a second call
/// reports `copied: 0`. Call [`pi_import_plan`] again to render the new state.
#[tauri::command]
pub fn pi_import_run(host: State<'_, HostState>) -> Report {
    let (agent, skills) = legacy_roots();
    let plan = pi_import::plan(
        &agent,
        &skills,
        &host.config.data_dir,
        &host.config.data_dir.join("skill"),
    );
    let report = pi_import::run(&plan);
    host.config.log.info(&format!(
        "pi-import: copied {} of {} item(s), skipped {}, failed {}",
        report.copied,
        plan.items.len(),
        report.skipped,
        report.failed.len()
    ));
    report
}
