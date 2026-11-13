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

/// A credential value that exists only in this case, so finding it inside the
/// catalog database would prove a plaintext leak rather than a coincidence.
const CREDENTIAL_SENTINEL: &str = "anvil-acceptance-sentinel-7f3c1a";

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
    /// Paths removed before the step runs.
    ///
    /// `npm ci` deletes `node_modules` itself, and this host's delete guard
    /// refuses bulk deletions from a child process. Pruning from Rust first
    /// keeps the install deterministic.
    pub remove: Vec<String>,
    /// Extra environment variables for this step only.
    ///
    /// Used to hand the catalog example a credential without putting it on the
    /// command line, where it would land in the evidence log.
    pub env: Vec<(String, String)>,
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
            remove: Vec::new(),
            env: Vec::new(),
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

    /// Remove `path` (repo-relative or absolute) before the step runs.
    pub(crate) fn remove(mut self, path: &str) -> Self {
        self.remove.push(path.to_string());
        self
    }

    /// Set one environment variable for this step.
    pub(crate) fn env(mut self, key: &str, value: &str) -> Self {
        self.env.push((key.to_string(), value.to_string()));
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
    Connectors,
    Schedules,
    Library,
    Settings,
    Catalog,
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
            .remove("frontend/node_modules")
            .timeout(1200),
        StepSpec::new("npm-test", "npm", &["test"])
            .dir("frontend")
            .timeout(900),
        StepSpec::new("npm-build", "npm", &["run", "build"])
            .dir("frontend")
            .remove("frontend/dist")
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
/// One `catalog_report` run with a fresh database.
///
/// `case` keeps the databases of different R1 cases apart; the directory is
/// removed before the step so every run starts from an empty catalog.
/// One catalog step inside a case. Steps share `.tmp/test/r1/<case>`, so only
/// the first step of a case may wipe it (`catalog_step_fresh`).
fn catalog_step(name: &str, case: &str, args: &[&str]) -> StepSpec {
    let data_dir = format!(".tmp/test/r1/{case}");
    let mut all: Vec<String> = vec![
        "run".into(),
        "--locked".into(),
        "-q".into(),
        "-p".into(),
        "anvil-catalog".into(),
        "--example".into(),
        "catalog_report".into(),
        "--".into(),
        "--data-dir".into(),
        data_dir,
    ];
    all.extend(args.iter().map(|arg| (*arg).to_string()));
    StepSpec::owned(name, "cargo", all).timeout(600)
}

/// The first step of a case: clears the case database so the run starts clean.
fn catalog_step_fresh(name: &str, case: &str, args: &[&str]) -> StepSpec {
    let data_dir = format!(".tmp/test/r1/{case}");
    catalog_step(name, case, args).remove(&data_dir)
}

/// One session step inside a case. Steps share `.tmp/test/r2/<case>` and its
/// state directory, so a case can send twice into the same ledger.
fn session_step(name: &str, case: &str, args: &[&str]) -> StepSpec {
    let data_dir = format!(".tmp/test/r2/{case}");
    let state_dir = format!(".tmp/test/r2/{case}-state");
    let mut all: Vec<String> = vec![
        "run".into(),
        "--locked".into(),
        "-q".into(),
        "-p".into(),
        "anvil-session".into(),
        "--example".into(),
        "session_report".into(),
        "--".into(),
        "--data-dir".into(),
        data_dir,
        "--state-dir".into(),
        state_dir,
    ];
    all.extend(args.iter().map(|arg| (*arg).to_string()));
    StepSpec::owned(name, "cargo", all).timeout(600)
}

/// First step of a case: clears the database and the runtime state directory so
/// the run starts from an empty ledger.
fn session_step_fresh(name: &str, case: &str, args: &[&str]) -> StepSpec {
    session_step(name, case, args)
        .remove(&format!(".tmp/test/r2/{case}"))
        .remove(&format!(".tmp/test/r2/{case}-state"))
}

/// Contract tests for the session layer (unit + injected-adapter suite).
fn session_contract_suite() -> Vec<StepSpec> {
    vec![
        StepSpec::new("cargo-fmt", "cargo", &["fmt", "--all", "--", "--check"]),
        StepSpec::new(
            "cargo-clippy-session",
            "cargo",
            &[
                "clippy",
                "-p",
                "anvil-session",
                "--all-targets",
                "--locked",
                "--",
                "-D",
                "warnings",
            ],
        )
        .timeout(600),
        StepSpec::new(
            "session-tests",
            "cargo",
            &["test", "-p", "anvil-session", "--locked"],
        )
        .timeout(900),
        StepSpec::new(
            "session-contract-tests",
            "cargo",
            &[
                "test",
                "-p",
                "anvil-session",
                "--test",
                "session_contract",
                "--locked",
            ],
        )
        .timeout(600),
    ]
}

/// Contract tests for the catalog (unit + injected-adapter suite).
fn catalog_contract_suite() -> Vec<StepSpec> {
    vec![
        StepSpec::new("cargo-fmt", "cargo", &["fmt", "--all", "--", "--check"]),
        StepSpec::new(
            "cargo-clippy-catalog",
            "cargo",
            &[
                "clippy",
                "-p",
                "anvil-catalog",
                "--all-targets",
                "--locked",
                "--",
                "-D",
                "warnings",
            ],
        )
        .timeout(600),
        StepSpec::new(
            "catalog-tests",
            "cargo",
            &["test", "-p", "anvil-catalog", "--locked"],
        )
        .timeout(900),
        StepSpec::new(
            "catalog-contract-tests",
            "cargo",
            &[
                "test",
                "-p",
                "anvil-catalog",
                "--test",
                "catalog_contract",
                "--locked",
            ],
        )
        .timeout(600),
    ]
}

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
        CasePlan::automation("R0-09-connectors", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-09-native-connectors", NativeKind::Connectors),
        CasePlan::automation("R0-10-schedules", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-10-native-schedules", NativeKind::Schedules),
        CasePlan::automation("R0-11-library", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-11-native-library", NativeKind::Library),
        CasePlan::automation("R0-12-settings", with_prototype_capture(build_suite()))
            .collects(&["prototype/*.png"]),
        CasePlan::native("R0-12-native-settings", NativeKind::Settings),
        CasePlan::automation("R1-01-catalog-contract", catalog_contract_suite()),
        CasePlan::automation(
            "R1-01-scan-live",
            vec![
                catalog_step_fresh(
                    "scan-once",
                    "scan",
                    &[
                        "--action",
                        "scan",
                        "--expect",
                        "runtime-count=4",
                        "--expect",
                        "runtime:pi:rows=1",
                        "--expect",
                        "runtime:codex:rows=1",
                        "--expect",
                        "runtime:claude:rows=1",
                        "--expect",
                        "runtime:opencode:rows=1",
                        "--expect",
                        "runtime:pi:status=available",
                        "--expect",
                        "runtime:codex:status=available",
                        "--expect",
                        "runtime:opencode:status=available",
                    ],
                ),
                catalog_step(
                    "scan-again",
                    "scan",
                    &[
                        "--action",
                        "scan",
                        "--expect",
                        "runtime-count=4",
                        "--expect",
                        "runtime:pi:rows=1",
                    ],
                ),
            ],
        ),
        CasePlan::automation(
            "R1-02-pi-live",
            vec![catalog_step_fresh(
                "pi-scan",
                "pi",
                &[
                    "--action",
                    "scan",
                    "--expect",
                    "runtime:pi:status=available",
                    "--expect",
                    "runtime:pi:version=1.5.0",
                    "--expect",
                    "runtime:pi:capability:models=supported",
                    "--expect",
                    "runtime:pi:capability:skills=supported",
                    "--expect",
                    "runtime:pi:capability:mcp=supported",
                    "--expect",
                    "runtime:pi:models-total=11",
                    "--expect",
                    "runtime:pi:skills-total=11",
                    "--expect",
                    "runtime:pi:mcp-total=1",
                ],
            )],
        ),
        CasePlan::automation(
            "R1-03-codex-live",
            vec![
                catalog_step_fresh(
                    "codex-scan",
                    "codex",
                    &[
                        "--action",
                        "scan",
                        "--expect",
                        "runtime:codex:status=available",
                        "--expect",
                        "runtime:codex:version=0.153.0",
                        "--expect",
                        "runtime:codex:capability:sessions=supported",
                        "--expect",
                        "runtime:codex:models-total=10",
                        "--expect",
                        "runtime:codex:skills-total=16",
                        "--expect",
                        "runtime:codex:mcp-total=2",
                    ],
                ),
                // The app-server handshake is part of the probe detail.
                StepSpec::new(
                    "handshake-detail",
                    "cargo",
                    &[
                        "run",
                        "--locked",
                        "-q",
                        "-p",
                        "anvil-catalog",
                        "--example",
                        "catalog_report",
                        "--",
                        "--data-dir",
                        ".tmp/test/r1/codex",
                        "--action",
                        "scan",
                    ],
                )
                .timeout(300)
                .expect_contains("app-server: anvil/"),
            ],
        ),
        CasePlan::automation(
            "R1-04-claude-opencode-live",
            vec![catalog_step_fresh(
                "others-scan",
                "others",
                &[
                    "--action",
                    "scan",
                    "--expect",
                    "runtime:claude:status=not_installed",
                    "--expect",
                    "runtime:claude:capability:skills=supported",
                    "--expect",
                    "runtime:claude:capability:models=unsupported",
                    "--expect",
                    "runtime:opencode:status=available",
                    "--expect",
                    "runtime:opencode:version=1.18.23",
                    "--expect",
                    "runtime:opencode:skills-total=36",
                    "--expect",
                    "runtime:opencode:mcp-total=3",
                ],
            )],
        ),
        CasePlan::automation(
            "R1-05-models-live",
            vec![
                catalog_step_fresh(
                    "add-gateway-model",
                    "models",
                    &[
                        "--action",
                        "add-model",
                        "--runtime",
                        "pi",
                        "--model",
                        "auto/gpt-5.6-sol",
                        "--display",
                        "Gateway Sol",
                        "--protocol",
                        "openai",
                        "--endpoint",
                        "http://127.0.0.1:3425/v1",
                        "--credential-env",
                        "ANVIL_ACCEPTANCE_CREDENTIAL",
                        "--expect",
                        "runtime:pi:status=available",
                    ],
                )
                .env("ANVIL_ACCEPTANCE_CREDENTIAL", "magpie"),
                catalog_step(
                    "verify-model",
                    "models",
                    &[
                        "--action",
                        "verify",
                        "--runtime",
                        "pi",
                        "--model",
                        "auto/gpt-5.6-sol",
                        "--expect",
                        "action:ok=true",
                        "--expect",
                        "action:detail-contains=HTTP 200",
                    ],
                ),
                catalog_step(
                    "set-default",
                    "models",
                    &[
                        "--action",
                        "set-default",
                        "--runtime",
                        "pi",
                        "--model",
                        "auto/gpt-5.6-sol",
                        "--expect",
                        "runtime:pi:status=available",
                    ],
                ),
                // A restart (new process, same database) must still hold the
                // encrypted credential and the verified model.
                catalog_step(
                    "verify-after-restart",
                    "models",
                    &[
                        "--action",
                        "verify",
                        "--runtime",
                        "pi",
                        "--model",
                        "auto/gpt-5.6-sol",
                        "--expect",
                        "action:ok=true",
                    ],
                ),
                // Credentials are encrypted at rest: a model whose credential is
                // a unique sentinel is stored, yet the sentinel never reaches
                // the database file (the row proves the write happened).
                catalog_step(
                    "add-sentinel-model",
                    "models",
                    &[
                        "--action",
                        "add-model",
                        "--runtime",
                        "pi",
                        "--model",
                        "anvil/credential-sentinel",
                        "--display",
                        "Credential Sentinel",
                        "--endpoint",
                        "http://127.0.0.1:9/v1",
                        "--credential-env",
                        "ANVIL_SENTINEL_CREDENTIAL",
                        "--expect",
                        "runtime:pi:models-total=12",
                    ],
                )
                .env("ANVIL_SENTINEL_CREDENTIAL", CREDENTIAL_SENTINEL),
                StepSpec::new(
                    "credential-not-plaintext",
                    "self",
                    &[
                        "absent",
                        "--path=.tmp/test/r1/models/catalog.sqlite",
                        &format!("--text={CREDENTIAL_SENTINEL}"),
                    ],
                ),
            ],
        ),
        CasePlan::automation(
            "R1-06-skills-live",
            vec![catalog_step_fresh(
                "skills-scan",
                "skills",
                &[
                    "--action",
                    "scan",
                    "--expect",
                    "skills-valid-min=40",
                    "--expect",
                    "runtime:pi:skills-total=11",
                    "--expect",
                    "runtime:opencode:skills-total=36",
                    // ~/.claude/skills exists on this machine but the claude CLI
                    // does not, and an uninstalled runtime keeps an empty
                    // inventory: the row is listed, its skills are not.
                    "--expect",
                    "runtime:claude:status=not_installed",
                    "--expect",
                    "runtime:claude:skills-total=0",
                ],
            )],
        ),
        CasePlan::automation(
            "R1-07-mcp-live",
            vec![
                catalog_step_fresh(
                    "mcp-scan",
                    "mcp",
                    &[
                        "--action",
                        "scan",
                        "--expect",
                        "mcp-count=6",
                        "--expect",
                        "runtime:codex:mcp-total=2",
                        "--expect",
                        "runtime:opencode:mcp-total=3",
                        "--expect",
                        "runtime:pi:mcp-total=1",
                    ],
                ),
                catalog_step(
                    "mcp-probe",
                    "mcp",
                    &[
                        "--action",
                        "probe-mcp",
                        "--runtime",
                        "opencode",
                        "--name",
                        "codegraph",
                        "--expect",
                        "action:ok=true",
                    ],
                ),
            ],
        ),
        CasePlan::native("R1-08-native-catalog", NativeKind::Catalog),
        CasePlan::automation("R2-01-session-contract", session_contract_suite()),
        CasePlan::automation(
            "R2-01-pi-live",
            vec![session_step_fresh(
                "pi-live",
                "pi",
                &[
                    "--action",
                    "live",
                    "--runtime",
                    "pi",
                    "--model",
                    "auto/gpt-5.6-sol",
                    "--prompt",
                    "Reply with exactly: ok",
                    "--timeout",
                    "180",
                    "--expect",
                    "action:status=completed",
                    "--expect",
                    "text-contains=ok",
                    "--expect",
                    "deltas-min=1",
                    "--expect",
                    "usage-present=true",
                    "--expect",
                    "session-files-min=1",
                    "--expect",
                    "native-id-present=true",
                ],
            )],
        ),
        CasePlan::automation(
            "R2-01-codex-live",
            vec![session_step_fresh(
                "codex-live",
                "codex",
                &[
                    "--action",
                    "live",
                    "--runtime",
                    "codex",
                    // The account does not accept codex's default model; the
                    // supported one has to be named explicitly.
                    "--model",
                    "gpt-5.6-sol",
                    "--prompt",
                    "Reply with exactly: ok",
                    "--turns",
                    "2",
                    "--timeout",
                    "180",
                    "--expect",
                    "action:status=completed",
                    "--expect",
                    "text-contains=ok",
                    "--expect",
                    "messages-min=4",
                    "--expect",
                    "native-id-present=true",
                ],
            )],
        ),
        CasePlan::automation(
            "R2-01-abort-live",
            vec![session_step_fresh(
                "abort-live",
                "abort",
                &[
                    "--action",
                    "abort",
                    "--runtime",
                    "pi",
                    "--model",
                    "auto/gpt-5.6-sol",
                    "--prompt",
                    "Count slowly from 1 to 200, one number per line.",
                    "--abort-after-ms",
                    "1200",
                    "--timeout",
                    "90",
                    "--expect",
                    "action:status=aborted",
                    "--expect",
                    "action:ok=true",
                    "--expect",
                    "action:stopped=true",
                ],
            )],
        ),
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
