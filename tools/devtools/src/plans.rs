//! Registered case plans: the executable form of a matrix case.
//!
//! Ported from `internal/acceptance/runner.go` (`casePlans`). Every step that
//! used to call `go` now calls the Cargo equivalent, because the Rust + Tauri
//! route dropped Go entirely (ADR 0007). The mapping is recorded in
//! `docs/coordination/rs03-case-mapping.md`.
//!
//! Native desktop cases are not in this table yet: their agent-desktop runners
//! are ported in R0-RS03 phase 2. Until then `verify` reports
//! "no automated runner" (exit 2) for them instead of pretending to pass.

use std::time::Duration;

use crate::evidence::{EVIDENCE_AUTOMATION, EVIDENCE_NATIVE_DESKTOP};

/// One command a case executes.
#[derive(Debug, Clone)]
pub struct StepSpec {
    /// Step name; also the log file name (`logs/<name>.log`).
    pub name: String,
    /// Executable name, `self`, `node` or `npm`.
    pub command: String,
    pub args: Vec<String>,
    /// Repo-relative working directory (the repo root when empty).
    pub dir: Option<String>,
    pub timeout: Duration,
    /// Exit code the step must produce.
    pub expect_exit: i32,
    /// Text the merged stdout/stderr log must contain.
    pub expect_output_contains: Option<String>,
    /// Directories to create before the step runs (repo-relative or absolute).
    pub mkdir: Vec<String>,
}

impl StepSpec {
    pub(crate) fn new(name: &str, command: &str, args: &[&str]) -> Self {
        Self {
            name: name.to_string(),
            command: command.to_string(),
            args: args.iter().map(|arg| (*arg).to_string()).collect(),
            dir: None,
            timeout: Duration::ZERO,
            expect_exit: 0,
            expect_output_contains: None,
            mkdir: Vec::new(),
        }
    }

    /// Build a step whose arguments are computed at run time.
    pub(crate) fn owned(name: &str, command: &str, args: Vec<String>) -> Self {
        let mut spec = Self::new(name, command, &[]);
        spec.args = args;
        spec
    }

    pub(crate) fn dir(mut self, dir: &str) -> Self {
        self.dir = Some(dir.to_string());
        self
    }

    pub(crate) fn timeout(mut self, seconds: u64) -> Self {
        self.timeout = Duration::from_secs(seconds);
        self
    }

    pub(crate) fn expect_exit(mut self, code: i32) -> Self {
        self.expect_exit = code;
        self
    }

    pub(crate) fn expect_contains(mut self, text: &str) -> Self {
        self.expect_output_contains = Some(text.to_string());
        self
    }

    pub(crate) fn mkdir(mut self, dir: &str) -> Self {
        self.mkdir.push(dir.to_string());
        self
    }
}

/// How a native case drives the real client. Recorded now, executed in phase 2.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NativeKind {
    Window,
    Shell,
    Theme,
    I18n,
    Workbench,
    Chat,
    Agents,
    Skills,
    Mcp,
}

/// The executable form of a registered case.
#[derive(Debug, Clone)]
pub struct CasePlan {
    pub id: String,
    pub evidence_kind: String,
    pub steps: Vec<StepSpec>,
    /// Paths removed before the case runs (repo-relative).
    pub reset: Vec<String>,
    pub native: Option<NativeKind>,
    /// Case-directory globs whose files are hashed into `result.json`.
    pub collect_artifacts: Vec<String>,
}

impl CasePlan {
    fn automation(id: &str, steps: Vec<StepSpec>) -> Self {
        Self {
            id: id.to_string(),
            evidence_kind: EVIDENCE_AUTOMATION.to_string(),
            steps,
            reset: Vec::new(),
            native: None,
            collect_artifacts: Vec::new(),
        }
    }

    fn native(id: &str, kind: NativeKind) -> Self {
        Self {
            id: id.to_string(),
            evidence_kind: EVIDENCE_NATIVE_DESKTOP.to_string(),
            steps: Vec::new(),
            reset: Vec::new(),
            native: Some(kind),
            collect_artifacts: Vec::new(),
        }
    }

    fn collects(mut self, patterns: &[&str]) -> Self {
        self.collect_artifacts = patterns.iter().map(|p| (*p).to_string()).collect();
        self
    }
}

/// The build/test steps shared by the page automation cases.
///
/// `R0-00-build-gate` owns the whole-workspace build and test, so the page cases
/// run the same gates plus the frontend checks that page needs.
fn build_suite() -> Vec<StepSpec> {
    vec![
        StepSpec::new("rustc-version", "rustc", &["--version"]).expect_contains("rustc 1.95.0"),
        StepSpec::new("cargo-fmt", "cargo", &["fmt", "--all", "--", "--check"]),
        StepSpec::new(
            "cargo-clippy",
            "cargo",
            &[
                "clippy",
                "--workspace",
                "--all-targets",
                "--locked",
                "--",
                "-D",
                "warnings",
            ],
        )
        .timeout(600),
        StepSpec::new("cargo-test", "cargo", &["test", "--locked", "--workspace"]).timeout(1800),
        StepSpec::new("node-version", "node", &["-v"]).expect_contains("v24.16.0"),
        StepSpec::new("npm-version", "npm", &["-v"]),
        StepSpec::new("npm-ci", "npm", &["ci"])
            .dir("frontend")
            .timeout(1200),
        StepSpec::new("npm-test", "npm", &["test"])
            .dir("frontend")
            .timeout(900),
        StepSpec::new("npm-build", "npm", &["run", "build"])
            .dir("frontend")
            .timeout(600),
        StepSpec::new("check-dist", "node", &["scripts/check-dist.mjs"]).dir("frontend"),
    ]
}

fn with_prototype_capture(mut steps: Vec<StepSpec>) -> Vec<StepSpec> {
    steps.push(
        StepSpec::new(
            "capture-prototype",
            "node",
            &[
                "scripts/capture-prototype.mjs",
                "--out",
                "{caseDir}/prototype",
            ],
        )
        .dir("frontend")
        .timeout(300),
    );
    steps
}

/// Every registered plan, in matrix order.
#[must_use]
pub fn case_plans() -> Vec<CasePlan> {
    vec![
        CasePlan::automation("R0-00-build-gate", build_suite()),
        CasePlan::automation(
            "R0-00-d-drive-preflight",
            vec![
                StepSpec::new(
                    "paths-tests",
                    "cargo",
                    &["test", "--locked", "-p", "anvil-paths"],
                )
                .timeout(600),
                StepSpec::new(
                    "path-worktree",
                    "self",
                    &["preflight", "--path={repo}", "--create"],
                ),
                StepSpec::new(
                    "path-temp",
                    "self",
                    &["preflight", "--path={repo}/.tmp/tooling/tmp", "--create"],
                ),
                StepSpec::new(
                    "path-cargo-target",
                    "self",
                    &[
                        "preflight",
                        "--path={repo}/.tmp/tooling/cargo-target",
                        "--create",
                    ],
                ),
                StepSpec::new(
                    "path-npm-cache",
                    "self",
                    &[
                        "preflight",
                        "--path={repo}/.tmp/tooling/npm-cache",
                        "--create",
                    ],
                ),
                StepSpec::new(
                    "path-agent-desktop",
                    "self",
                    &[
                        "preflight",
                        "--path={repo}/.tmp/tooling/agent-desktop-home",
                        "--create",
                    ],
                ),
                StepSpec::new(
                    "path-test-data",
                    "self",
                    &["preflight", "--path={repo}/.tmp/test", "--create"],
                ),
                StepSpec::new(
                    "path-evidence",
                    "self",
                    &["preflight", "--path={repo}/.tmp/evidence", "--create"],
                ),
                StepSpec::new(
                    "path-evidence-root",
                    "self",
                    &["preflight", "--path={evidence}", "--create"],
                ),
                StepSpec::new(
                    "reject-other-drive",
                    "self",
                    &[
                        "preflight",
                        r"--path=C:\Users\Administrator\AppData\Local\anvil-r0-00-must-not-exist",
                    ],
                )
                .expect_exit(2)
                .expect_contains("D:"),
                StepSpec::new(
                    "junction-create",
                    "cmd",
                    &[
                        "/c",
                        "mklink",
                        "/J",
                        r"{repo}\.tmp\test\junction-fixture\to-c",
                        r"C:\Windows",
                    ],
                )
                .mkdir(".tmp/test/junction-fixture"),
                StepSpec::new(
                    "reject-junction",
                    "self",
                    &[
                        "preflight",
                        r"--path={repo}\.tmp\test\junction-fixture\to-c\anvil-r0-00-must-not-exist",
                    ],
                )
                .expect_exit(2)
                .expect_contains("D:"),
                StepSpec::new(
                    "junction-remove",
                    "cmd",
                    &["/c", "rmdir", r"{repo}\.tmp\test\junction-fixture\to-c"],
                ),
                StepSpec::new(
                    "junction-dir-remove",
                    "cmd",
                    &["/c", "rmdir", r"{repo}\.tmp\test\junction-fixture"],
                ),
            ],
        ),
        CasePlan::native("R0-00-native-window", NativeKind::Window),
        CasePlan::automation("R0-01-shell", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-01-native-shell", NativeKind::Shell),
        CasePlan::automation("R0-02-theme", build_suite()),
        CasePlan::native("R0-02-native-theme", NativeKind::Theme),
        CasePlan::automation("R0-03-i18n", build_suite()),
        CasePlan::native("R0-03-native-i18n", NativeKind::I18n),
        CasePlan::automation("R0-04-workbench", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-04-native-workbench", NativeKind::Workbench),
        CasePlan::automation("R0-05-chat", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-05-native-chat", NativeKind::Chat),
        CasePlan::automation("R0-06-agents", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-06-native-agents", NativeKind::Agents),
        CasePlan::automation("R0-07-skills", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-07-native-skills", NativeKind::Skills),
        CasePlan::automation("R0-08-mcp", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-08-native-mcp", NativeKind::Mcp),
    ]
}

/// Look up the plan for a registered case.
#[must_use]
pub fn lookup(case_id: &str) -> Option<CasePlan> {
    case_plans().into_iter().find(|plan| plan.id == case_id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_plan_id_is_a_valid_path_segment() {
        for plan in case_plans() {
            assert!(
                crate::matrix::validate_identifier(&plan.id, "case id").is_ok(),
                "{} is not a safe case id",
                plan.id
            );
        }
    }

    #[test]
    fn no_plan_step_invokes_go() {
        for plan in case_plans() {
            for step in &plan.steps {
                assert_ne!(
                    step.command, "go",
                    "{} still calls the removed Go toolchain",
                    plan.id
                );
                assert!(
                    !step.args.iter().any(|arg| arg == "go.mod"),
                    "{} still references go.mod",
                    plan.id
                );
            }
        }
    }

    #[test]
    fn plan_ids_are_unique() {
        let plans = case_plans();
        for (index, plan) in plans.iter().enumerate() {
            assert!(
                !plans[index + 1..].iter().any(|other| other.id == plan.id),
                "duplicate plan {}",
                plan.id
            );
        }
    }
}
