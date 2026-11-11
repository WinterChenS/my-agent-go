//! Contract fixtures for the gate and `verify` front ends.
//!
//! Every scenario runs on fixtures inside `.tmp/test/devtools-fixtures` (D:).
//! Volume rejections use an injected resolver, so no test ever creates a
//! directory on another drive — the same rule the Go suite followed.

use std::collections::HashMap;
use std::io;
use std::path::{Path, PathBuf};

use anvil_devtools::gate::{
    EXIT_ASSERTION, EXIT_INCOMPLETE, EXIT_PASS, GateOptions, evaluate_gate,
};
use anvil_devtools::matrix::load_matrix;
use anvil_devtools::runner::{VerifyOptions, run_case};
use anvil_paths::{PathError, Resolution, Resolver};
use serde_json::json;

const SHA: &str = "e3767873d838fc6abc9f9da93ce69bd11ffbe407";
const TARGET: &str = "win-x64";

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .to_path_buf()
}

fn fixture_dir(name: &str) -> PathBuf {
    let dir = repo_root()
        .join(".tmp")
        .join("test")
        .join("devtools-fixtures")
        .join(name);
    if dir.exists() {
        std::fs::remove_dir_all(&dir).expect("clear fixture");
    }
    std::fs::create_dir_all(&dir).expect("create fixture");
    dir
}

/// Resolver that reports a fixed volume and allows per-path overrides, so the
/// reject branches never touch another drive.
struct FakeResolver {
    volume: String,
    overrides: HashMap<String, PathBuf>,
    resolved_volume: String,
}

impl FakeResolver {
    fn on_d() -> Self {
        Self {
            volume: "D:".to_string(),
            overrides: HashMap::new(),
            resolved_volume: "D:".to_string(),
        }
    }

    fn on_c() -> Self {
        Self {
            volume: "C:".to_string(),
            overrides: HashMap::new(),
            resolved_volume: "C:".to_string(),
        }
    }

    fn redirect(mut self, from: &Path, to: &Path) -> Self {
        self.overrides.insert(key(from), to.to_path_buf());
        self
    }
}

fn key(path: &Path) -> String {
    path.to_string_lossy().to_lowercase()
}

impl Resolver for FakeResolver {
    fn resolve(&self, path: &Path) -> Result<Resolution, PathError> {
        let resolved = self
            .overrides
            .get(&key(path))
            .cloned()
            .unwrap_or_else(|| path.to_path_buf());
        let volume = self
            .overrides
            .get(&key(path))
            .map(|_| self.resolved_volume.clone())
            .unwrap_or_else(|| self.volume.clone());
        Ok(Resolution {
            requested: path.to_path_buf(),
            existing_ancestor: resolved.clone(),
            resolved,
            volume,
        })
    }

    fn create_dir_all(&self, _path: &Path) -> io::Result<()> {
        Ok(())
    }

    fn probe_writable(&self, _dir: &Path) -> io::Result<()> {
        Ok(())
    }
}

fn write_matrix(dir: &Path, required_tasks: &[&str], case: serde_json::Value) -> PathBuf {
    let path = dir.join("matrix.json");
    let matrix = json!({
        "schemaVersion": 1,
        "gates": [{
            "id": "G0",
            "targets": [TARGET, "linux-x64"],
            "requiredTasks": required_tasks,
            "cases": [case],
        }],
    });
    std::fs::write(
        &path,
        serde_json::to_string_pretty(&matrix).expect("matrix"),
    )
    .expect("write");
    path
}

fn write_result(case_dir: &Path, status: &str, artifact: Option<(&str, String)>) {
    std::fs::create_dir_all(case_dir).expect("case dir");
    let artifacts: Vec<serde_json::Value> = match artifact {
        Some((path, sha256)) => vec![json!({ "path": path, "sha256": sha256 })],
        None => Vec::new(),
    };
    let result = json!({
        "schemaVersion": 1,
        "caseId": case_dir.file_name().expect("case name").to_string_lossy(),
        "gitSHA": SHA,
        "target": TARGET,
        "status": status,
        "evidenceKind": "automation",
        "os": { "name": "Windows 10 Pro", "version": "10.0.19045", "build": "19045.4529", "arch": "amd64" },
        "toolVersions": { "cargo": "cargo 1.95.0" },
        "runtimeVersion": "tauri 2 (no agent runtime in this case)",
        "commands": [{ "command": "cargo test", "workDir": ".", "exitCode": 0 }],
        "artifacts": artifacts,
        "limitations": if status == "BLOCKED" { vec!["no machine"] } else { Vec::<&str>::new() },
    });
    std::fs::write(
        case_dir.join("result.json"),
        serde_json::to_string_pretty(&result).expect("result"),
    )
    .expect("write result");
}

fn hash_of(path: &Path) -> String {
    anvil_devtools::hash::file_sha256(path).expect("hash")
}

fn gate_opts(matrix: &Path, evidence: &Path, target: &str, sha: &str) -> GateOptions {
    GateOptions {
        matrix_path: matrix.to_path_buf(),
        evidence_root: evidence.display().to_string(),
        gate_id: "G0".to_string(),
        target: target.to_string(),
        git_sha: sha.to_string(),
    }
}

#[test]
fn empty_matrix_is_rejected_before_any_evidence_is_read() {
    let dir = fixture_dir("empty-matrix");
    let matrix = dir.join("empty.json");
    std::fs::write(&matrix, r#"{"schemaVersion":1,"gates":[]}"#).expect("write");
    let err = load_matrix(&matrix).expect_err("empty matrix must be rejected");
    assert!(err.contains("declares no gates"), "{err}");

    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(outcome.problems[0].contains("declares no gates"));
}

#[test]
fn a_required_task_without_cases_keeps_the_gate_closed() {
    let dir = fixture_dir("missing-task");
    let matrix = write_matrix(
        &dir,
        &["R0-00", "R0-09"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": false }),
    );
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome
            .problems
            .iter()
            .any(|problem| problem.contains("required task \"R0-09\" has no registered cases")),
        "{:?}",
        outcome.problems
    );
}

#[test]
fn an_unknown_target_is_rejected() {
    let dir = fixture_dir("unknown-target");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, "win-ia64", SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome.problems[0].contains("is not declared for gate"),
        "{:?}",
        outcome.problems
    );
}

#[test]
fn an_invalid_commit_is_rejected() {
    let dir = fixture_dir("invalid-sha");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, "e376787"),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome.problems[0].contains("not a full 40-character git SHA"),
        "{:?}",
        outcome.problems
    );
}

#[test]
fn an_evidence_root_on_another_volume_is_rejected() {
    let dir = fixture_dir("cross-volume");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    // The fixture itself lives on D:, but the resolver reports C: — the same
    // shape a C:-hosted evidence root would have, without creating one.
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_c(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome.problems[0].contains("outside the required D: volume"),
        "{:?}",
        outcome.problems
    );
}

#[test]
fn a_case_directory_that_resolves_outside_the_root_is_rejected() {
    let dir = fixture_dir("escaped-case-dir");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let case_dir = dir.join(SHA).join(TARGET).join("R0-00-build-gate");
    write_result(&case_dir, "PASS", None);
    // A junction at the case directory would resolve to another tree entirely.
    let elsewhere = repo_root()
        .join(".tmp")
        .join("test")
        .join("devtools-escaped-target");
    std::fs::create_dir_all(&elsewhere).expect("elsewhere");
    let resolver = FakeResolver::on_d().redirect(&case_dir, &elsewhere);
    let outcome = evaluate_gate(&gate_opts(&matrix, &dir, TARGET, SHA), &resolver);
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome.problems[0].contains("resolves outside the evidence root"),
        "{:?}",
        outcome.problems
    );
}

#[test]
fn a_tampered_artifact_fails_the_hash_check() {
    let dir = fixture_dir("hash-mismatch");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let case_dir = dir.join(SHA).join(TARGET).join("R0-00-build-gate");
    std::fs::create_dir_all(&case_dir).expect("case dir");
    let payload = case_dir.join("screenshot.png");
    std::fs::write(&payload, b"captured").expect("payload");
    // Record a digest that belongs to different bytes.
    write_result(&case_dir, "PASS", Some(("screenshot.png", "0".repeat(64))));
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    let joined = outcome.problems.join(" | ");
    assert!(joined.contains("sha256 mismatch"), "{joined}");
    assert_eq!(outcome.statuses["R0-00-build-gate"], "INVALID");
}

#[test]
fn valid_fail_evidence_returns_exit_one() {
    let dir = fixture_dir("valid-fail");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let case_dir = dir.join(SHA).join(TARGET).join("R0-00-build-gate");
    std::fs::create_dir_all(&case_dir).expect("case dir");
    let log = case_dir.join("logs").join("cargo-test.log");
    std::fs::create_dir_all(log.parent().expect("logs")).expect("logs");
    std::fs::write(&log, b"test result: FAILED").expect("log");
    let digest = hash_of(&log);
    write_result(&case_dir, "FAIL", Some(("logs/cargo-test.log", digest)));
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_ASSERTION);
    assert!(outcome.problems.is_empty(), "{:?}", outcome.problems);
    assert_eq!(outcome.statuses["R0-00-build-gate"], "FAIL");
    assert!(outcome.passed.is_empty());
}

#[test]
fn complete_pass_evidence_returns_exit_zero() {
    let dir = fixture_dir("full-pass");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let case_dir = dir.join(SHA).join(TARGET).join("R0-00-build-gate");
    std::fs::create_dir_all(&case_dir).expect("case dir");
    let log = case_dir.join("logs").join("cargo-test.log");
    std::fs::create_dir_all(log.parent().expect("logs")).expect("logs");
    std::fs::write(&log, b"test result: ok. 31 passed").expect("log");
    let digest = hash_of(&log);
    write_result(&case_dir, "PASS", Some(("logs/cargo-test.log", digest)));
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_PASS, "{:?}", outcome.problems);
    assert_eq!(outcome.passed, vec!["R0-00-build-gate".to_string()]);
}

#[test]
fn blocked_evidence_without_limitations_is_invalid() {
    let dir = fixture_dir("blocked-without-limitations");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let case_dir = dir.join(SHA).join(TARGET).join("R0-00-build-gate");
    std::fs::create_dir_all(&case_dir).expect("case dir");
    let mut result = json!({
        "schemaVersion": 1,
        "caseId": "R0-00-build-gate",
        "gitSHA": SHA,
        "target": TARGET,
        "status": "BLOCKED",
        "evidenceKind": "automation",
        "os": { "name": "Windows 10 Pro", "version": "10.0.19045", "build": "19045.4529", "arch": "amd64" },
        "toolVersions": { "cargo": "cargo 1.95.0" },
        "runtimeVersion": "n/a",
        "commands": [{ "command": "cargo test", "workDir": ".", "exitCode": 0 }],
        "artifacts": [],
        "limitations": [],
    });
    std::fs::write(
        case_dir.join("result.json"),
        serde_json::to_string_pretty(&result).expect("json"),
    )
    .expect("write");
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome
            .problems
            .join(" ")
            .contains("limitations must explain BLOCKED evidence"),
        "{:?}",
        outcome.problems
    );

    // A missing field must be reported as missing, not defaulted.
    result["limitations"] = json!(["no machine"]);
    result.as_object_mut().expect("object").remove("commands");
    std::fs::write(
        case_dir.join("result.json"),
        serde_json::to_string_pretty(&result).expect("json"),
    )
    .expect("write");
    let outcome = evaluate_gate(
        &gate_opts(&matrix, &dir, TARGET, SHA),
        &FakeResolver::on_d(),
    );
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome
            .problems
            .join(" ")
            .contains("missing field \"commands\""),
        "{:?}",
        outcome.problems
    );
}

#[test]
fn verify_rejects_an_unregistered_case_and_a_dirty_tree_rule_is_documented() {
    let dir = fixture_dir("verify-unregistered");
    let matrix = write_matrix(
        &dir,
        &["R0-00"],
        json!({ "id": "R0-00-build-gate", "task": "R0-00", "evidenceKind": "automation", "required": true }),
    );
    let outcome = run_case(&VerifyOptions {
        repo_root: repo_root(),
        matrix_path: matrix,
        evidence_root: dir.display().to_string(),
        case_id: "R0-99-nope".to_string(),
        target: TARGET.to_string(),
        git_sha: SHA.to_string(),
    });
    assert_eq!(outcome.code, EXIT_INCOMPLETE);
    assert!(
        outcome.problems[0].contains("is not registered"),
        "{:?}",
        outcome.problems
    );
}

#[test]
fn every_registered_native_case_has_a_runner() {
    // `verify` reports "no automated runner" (exit 2) for a case without a
    // plan. Now that every native runner is ported, no matrix entry may be in
    // that state: a missing runner would silently keep the gate closed.
    let matrix = anvil_devtools::matrix::load_matrix(
        &repo_root()
            .join("tests")
            .join("acceptance")
            .join("rebuild.json"),
    )
    .expect("acceptance matrix");
    for gate in &matrix.gates {
        for case in &gate.cases {
            if case.evidence_kind != "native-desktop" {
                continue;
            }
            let plan = anvil_devtools::plans::lookup(&case.id)
                .unwrap_or_else(|| panic!("{} has no case plan", case.id));
            assert!(
                plan.native.is_some(),
                "{} is registered as native-desktop but its plan has no native runner",
                case.id
            );
        }
    }
}
