//! Stage-gate evaluation over recorded evidence.
//!
//! Ported from `internal/acceptance/gate.go`. A gate only passes when the
//! matrix is valid, every required task has registered cases, and every
//! required case has complete, internally consistent evidence bound to the gate
//! commit and target. Missing or invalid input is exit 2; valid evidence that
//! reports FAIL is exit 1; everything passing is exit 0.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use anvil_paths::Resolver;

use crate::evidence::{
    Artifact, STATUS_BLOCKED, STATUS_FAIL, STATUS_NOT_RUN, STATUS_PASS, load_result,
    valid_evidence_kind, valid_status, validate_artifact_fields, validate_commands,
};
use crate::hash::file_sha256;
use crate::matrix::{Case, contains, is_hex_sha, is_sha256, load_matrix, normalize_sha};
use crate::preflight::{case_dir as make_case_dir, check_path, ensure_within};

/// Exit code for a gate that passed.
pub const EXIT_PASS: i32 = 0;
/// Exit code for valid evidence that reports FAIL.
pub const EXIT_ASSERTION: i32 = 1;
/// Exit code for incomplete input, invalid evidence, BLOCKED or NOT_RUN.
pub const EXIT_INCOMPLETE: i32 = 2;

/// Inputs for one gate evaluation.
#[derive(Debug, Clone)]
pub struct GateOptions {
    pub matrix_path: PathBuf,
    pub evidence_root: String,
    pub gate_id: String,
    pub target: String,
    pub git_sha: String,
}

/// The result of evaluating a gate.
#[derive(Debug, Default)]
pub struct GateOutcome {
    pub code: i32,
    pub problems: Vec<String>,
    pub statuses: BTreeMap<String, String>,
    pub passed: Vec<String>,
}

/// Evaluate a gate. `resolver` is the production [`anvil_paths::SystemResolver`]
/// in the CLI and a fake in tests, so cross-volume branches never need a
/// fixture on another drive.
#[must_use]
pub fn evaluate_gate(opts: &GateOptions, resolver: &dyn Resolver) -> GateOutcome {
    let mut out = GateOutcome {
        code: EXIT_INCOMPLETE,
        ..GateOutcome::default()
    };
    let problem = |out: &mut GateOutcome, message: String| out.problems.push(message);

    let matrix = match load_matrix(&opts.matrix_path) {
        Ok(matrix) => matrix,
        Err(err) => {
            problem(&mut out, err);
            return out;
        }
    };

    if opts.gate_id.trim().is_empty() {
        problem(&mut out, "gate id is empty".to_string());
        return out;
    }
    let Some(gate) = matrix.gates.iter().find(|gate| gate.id == opts.gate_id) else {
        problem(
            &mut out,
            format!("gate {:?} is not registered in the matrix", opts.gate_id),
        );
        return out;
    };
    if opts.target.trim().is_empty() {
        problem(&mut out, "target is empty".to_string());
        return out;
    }
    if let Err(err) = crate::matrix::validate_identifier(&opts.target, "target") {
        problem(&mut out, err);
        return out;
    }
    if !contains(&gate.targets, &opts.target) {
        problem(
            &mut out,
            format!(
                "target {:?} is not declared for gate {:?} (declared: {})",
                opts.target,
                gate.id,
                gate.targets.join(", ")
            ),
        );
        return out;
    }

    let sha = normalize_sha(&opts.git_sha);
    if !is_hex_sha(&sha) {
        problem(
            &mut out,
            format!(
                "gate commit {:?} is not a full 40-character git SHA",
                opts.git_sha
            ),
        );
        return out;
    }

    let root = match check_evidence_root(opts, resolver) {
        Ok(root) => root,
        Err(err) => {
            problem(&mut out, err);
            return out;
        }
    };

    // Every case declaration must be safe to turn into a path segment, and no
    // case may be registered twice.
    let mut declared: Vec<&str> = Vec::new();
    for case in &gate.cases {
        if let Err(err) = validate_matrix_case(case) {
            problem(&mut out, err);
            return out;
        }
        if declared.contains(&case.id.as_str()) {
            problem(
                &mut out,
                format!("duplicate case id {:?} in gate {:?}", case.id, gate.id),
            );
            return out;
        }
        declared.push(case.id.as_str());
    }

    // A required task without registered cases stays pending: the gate cannot
    // pass just because the task was left out of the matrix.
    for task in &gate.required_tasks {
        if !gate.cases.iter().any(|case| &case.task == task) {
            problem(
                &mut out,
                format!("required task {task:?} has no registered cases"),
            );
        }
    }
    let required: Vec<&Case> = gate.cases.iter().filter(|case| case.required).collect();
    if required.is_empty() {
        problem(
            &mut out,
            format!("gate {:?} has no required cases", gate.id),
        );
        return out;
    }

    let mut has_fail = false;
    for case in required {
        match evaluate_case(&root, &sha, &opts.target, case, resolver) {
            Ok(status) => {
                out.statuses.insert(case.id.clone(), status.clone());
                match status.as_str() {
                    STATUS_PASS => out.passed.push(case.id.clone()),
                    STATUS_FAIL => has_fail = true,
                    _ => problem(
                        &mut out,
                        format!("case {:?} is {status}: gate stays closed", case.id),
                    ),
                }
            }
            Err(err) => {
                problem(&mut out, format!("case {:?}: {err}", case.id));
                out.statuses.insert(case.id.clone(), "INVALID".to_string());
            }
        }
    }

    if !out.problems.is_empty() {
        return out;
    }
    if has_fail {
        out.code = EXIT_ASSERTION;
        return out;
    }
    out.code = EXIT_PASS;
    out
}

fn validate_matrix_case(case: &Case) -> Result<(), String> {
    crate::matrix::validate_identifier(&case.id, "case id")?;
    crate::matrix::validate_identifier(&case.task, "task id")?;
    if !valid_evidence_kind(&case.evidence_kind) {
        return Err(format!(
            "case {:?} has invalid evidenceKind {:?} (want automation, native-desktop or real-runtime)",
            case.id, case.evidence_kind
        ));
    }
    Ok(())
}

fn check_evidence_root(opts: &GateOptions, resolver: &dyn Resolver) -> Result<PathBuf, String> {
    let raw = opts.evidence_root.trim();
    if raw.is_empty() {
        return Err("evidence root is empty".to_string());
    }
    let path = Path::new(raw);
    if !path.is_absolute() {
        return Err(format!("evidence root {raw:?} must be an absolute D: path"));
    }
    let resolution = check_path(resolver, path).map_err(|err| format!("evidence root: {err}"))?;
    if !resolution.resolved.is_dir() {
        return Err(format!(
            "evidence root {:?} does not exist or is not a directory",
            resolution.resolved.display().to_string()
        ));
    }
    Ok(resolution.resolved)
}

/// Validate one case directory and return its recorded status.
fn evaluate_case(
    evidence_root: &Path,
    sha: &str,
    target: &str,
    case: &Case,
    resolver: &dyn Resolver,
) -> Result<String, String> {
    let case_dir = make_case_dir(evidence_root, sha, target, &case.id)
        .map_err(|_| "case directory escapes the evidence root".to_string())?;
    // The textual path is not enough: a junction at the case directory must not
    // hand the gate evidence from outside the evidence root.
    let resolved_root = resolver
        .resolve(evidence_root)
        .map_err(|err| format!("resolve evidence root: {err}"))?;
    let resolved_case = resolver
        .resolve(&case_dir)
        .map_err(|err| format!("resolve case directory: {err}"))?;
    if ensure_within(&resolved_root.resolved, &resolved_case.resolved).is_err() {
        return Err("case directory resolves outside the evidence root".to_string());
    }

    let (result, raw) = load_result(&case_dir.join("result.json"))?;
    if result.schema_version != crate::evidence::RESULT_SCHEMA_VERSION {
        return Err(format!(
            "schemaVersion = {}, want {}",
            result.schema_version,
            crate::evidence::RESULT_SCHEMA_VERSION
        ));
    }
    if result.case_id != case.id {
        return Err(format!(
            "caseId {:?} does not match registered case {:?}",
            result.case_id, case.id
        ));
    }
    if normalize_sha(&result.git_sha) != sha {
        return Err(format!(
            "gitSHA {:?} does not match gate commit {sha:?}",
            result.git_sha
        ));
    }
    if result.target != target {
        return Err(format!(
            "target {:?} does not match gate target {target:?}",
            result.target
        ));
    }
    if result.evidence_kind != case.evidence_kind {
        return Err(format!(
            "evidenceKind {:?} does not match matrix evidenceKind {:?}",
            result.evidence_kind, case.evidence_kind
        ));
    }
    if !valid_status(&result.status) {
        return Err(format!(
            "status {:?} is not one of PASS, FAIL, BLOCKED, NOT_RUN",
            result.status
        ));
    }
    if result.os.version.is_empty() || result.os.build.is_empty() || result.os.arch.is_empty() {
        return Err("os.version, os.build and os.arch must be recorded".to_string());
    }
    if result.tool_versions.is_empty() {
        return Err("toolVersions must record the tools used by the case".to_string());
    }
    validate_commands(&raw)?;
    validate_artifact_fields(&raw)?;
    if matches!(result.status.as_str(), STATUS_PASS | STATUS_FAIL) && result.artifacts.is_empty() {
        return Err(format!(
            "artifacts must not be empty for {} evidence",
            result.status
        ));
    }
    if matches!(result.status.as_str(), STATUS_BLOCKED | STATUS_NOT_RUN)
        && result.limitations.is_empty()
    {
        return Err(format!(
            "limitations must explain {} evidence",
            result.status
        ));
    }

    for artifact in &result.artifacts {
        validate_artifact(evidence_root, &case_dir, artifact, resolver)?;
    }
    Ok(result.status)
}

fn validate_artifact(
    evidence_root: &Path,
    case_dir: &Path,
    artifact: &Artifact,
    resolver: &dyn Resolver,
) -> Result<(), String> {
    let path_text = artifact.path.trim();
    if path_text.is_empty() {
        return Err("artifact path is empty".to_string());
    }
    if Path::new(path_text).is_absolute() {
        return Err(format!(
            "artifact {:?} must be a relative path inside the case directory",
            artifact.path
        ));
    }
    let clean = crate::hash::from_slash(path_text);
    if clean
        .components()
        .any(|component| matches!(component, std::path::Component::ParentDir))
    {
        return Err(format!(
            "artifact {:?} escapes the case directory",
            artifact.path
        ));
    }
    let full = case_dir.join(&clean);
    if ensure_within(case_dir, &full).is_err() {
        return Err(format!(
            "artifact {:?} escapes the case directory",
            artifact.path
        ));
    }
    let Ok(meta) = std::fs::metadata(&full) else {
        return Err(format!("artifact {:?} is missing", artifact.path));
    };
    if !meta.is_file() {
        return Err(format!(
            "artifact {:?} is not a regular file",
            artifact.path
        ));
    }
    if !is_sha256(&normalize_sha(&artifact.sha256)) {
        return Err(format!(
            "artifact {:?} sha256 {:?} is not a 64-character hex digest",
            artifact.path, artifact.sha256
        ));
    }

    // Resolve reparse points before accepting the file: a junction inside the
    // case directory must not hand the gate a file from somewhere else.
    let resolved_case = resolver
        .resolve(case_dir)
        .map_err(|err| format!("resolve case directory: {err}"))?;
    let resolved_artifact = resolver
        .resolve(&full)
        .map_err(|err| format!("resolve artifact {:?}: {err}", artifact.path))?;
    if ensure_within(&resolved_case.resolved, &resolved_artifact.resolved).is_err() {
        return Err(format!(
            "artifact {:?} escapes the case directory",
            artifact.path
        ));
    }
    if ensure_within(evidence_root, &resolved_artifact.resolved).is_err() {
        return Err(format!(
            "artifact {:?} resolves outside the evidence root",
            artifact.path
        ));
    }

    let actual =
        file_sha256(&full).map_err(|err| format!("hash artifact {:?}: {err}", artifact.path))?;
    if !actual.eq_ignore_ascii_case(&artifact.sha256) {
        return Err(format!(
            "artifact {:?} sha256 mismatch: recorded {}, actual {actual}",
            artifact.path, artifact.sha256
        ));
    }
    Ok(())
}
