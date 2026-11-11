//! Evidence documents: `<evidence>/<gitSHA>/<target>/<caseId>/result.json`.
//!
//! Ported from `internal/acceptance/evidence.go`.

use std::collections::BTreeMap;
use std::path::Path;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

/// The only evidence schema this build understands.
pub const RESULT_SCHEMA_VERSION: i64 = 1;
/// A result document larger than this is rejected instead of parsed.
const MAX_RESULT_BYTES: u64 = 1 << 20;

/// Evidence kinds declared by the matrix and copied into `result.json`.
pub const EVIDENCE_AUTOMATION: &str = "automation";
/// Native desktop evidence produced by driving the real client.
pub const EVIDENCE_NATIVE_DESKTOP: &str = "native-desktop";
/// Evidence produced against a real installed agent runtime.
pub const EVIDENCE_REAL_RUNTIME: &str = "real-runtime";

/// Only `PASS` advances a gate; `FAIL` is a valid assertion failure (exit 1);
/// `BLOCKED` and `NOT_RUN` keep the gate closed (exit 2).
pub const STATUS_PASS: &str = "PASS";
/// A valid run whose assertions failed.
pub const STATUS_FAIL: &str = "FAIL";
/// A required precondition (machine, credentials, network) is missing.
pub const STATUS_BLOCKED: &str = "BLOCKED";
/// The case was declared but never executed.
pub const STATUS_NOT_RUN: &str = "NOT_RUN";

/// The machine an evidence run was produced on.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct OsInfo {
    pub name: String,
    pub version: String,
    pub build: String,
    pub arch: String,
}

/// One command actually executed by a case.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Command {
    pub command: String,
    #[serde(rename = "workDir")]
    pub work_dir: String,
    #[serde(rename = "exitCode")]
    pub exit_code: i64,
}

/// A file produced by a case, relative to the case directory.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Artifact {
    pub path: String,
    pub sha256: String,
}

/// The evidence document written by `verify`.
///
/// Named `CaseResult` rather than `Result` so it never shadows `std::result`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CaseResult {
    #[serde(rename = "schemaVersion")]
    pub schema_version: i64,
    #[serde(rename = "caseId")]
    pub case_id: String,
    #[serde(rename = "gitSHA")]
    pub git_sha: String,
    pub target: String,
    pub status: String,
    #[serde(rename = "evidenceKind")]
    pub evidence_kind: String,
    pub os: OsInfo,
    #[serde(rename = "toolVersions")]
    pub tool_versions: BTreeMap<String, String>,
    #[serde(rename = "runtimeVersion")]
    pub runtime_version: String,
    pub commands: Vec<Command>,
    pub artifacts: Vec<Artifact>,
    pub limitations: Vec<String>,
}

const REQUIRED_RESULT_FIELDS: [&str; 12] = [
    "schemaVersion",
    "caseId",
    "gitSHA",
    "target",
    "status",
    "evidenceKind",
    "os",
    "toolVersions",
    "runtimeVersion",
    "commands",
    "artifacts",
    "limitations",
];

/// Read one `result.json`, keeping the raw object for presence checks that
/// serde defaults would otherwise hide (for example an empty `commands` array
/// is an error, but a missing `commands` key must be reported as missing).
///
/// # Errors
///
/// Reports a missing file, an oversized document, malformed JSON, or any of
/// the fields the schema requires.
pub fn load_result(path: &Path) -> Result<(CaseResult, Map<String, Value>), String> {
    let meta = match std::fs::metadata(path) {
        Ok(meta) => meta,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            return Err("missing result.json".to_string());
        }
        Err(err) => return Err(format!("read result.json: {err}")),
    };
    if meta.len() > MAX_RESULT_BYTES {
        return Err("result.json exceeds 1 MiB".to_string());
    }
    let data = std::fs::read(path).map_err(|err| format!("read result.json: {err}"))?;
    if data.len() as u64 > MAX_RESULT_BYTES {
        return Err("result.json exceeds 1 MiB".to_string());
    }
    let raw: Map<String, Value> =
        serde_json::from_slice(&data).map_err(|err| format!("invalid result.json: {err}"))?;
    for field in REQUIRED_RESULT_FIELDS {
        if !raw.contains_key(field) {
            return Err(format!("result.json is missing field {field:?}"));
        }
    }
    let result: CaseResult =
        serde_json::from_slice(&data).map_err(|err| format!("invalid result.json: {err}"))?;
    Ok((result, raw))
}

/// Validate the raw `commands` array: present, non-empty, and every entry
/// carries `command`, `workDir` and `exitCode`.
///
/// # Errors
///
/// Returns the same wording the Go gate used so old transcripts stay readable.
pub fn validate_commands(raw: &Map<String, Value>) -> Result<(), String> {
    let entries = raw
        .get("commands")
        .and_then(Value::as_array)
        .ok_or_else(|| "commands must be an array of objects".to_string())?;
    if entries.is_empty() {
        return Err("commands must not be empty".to_string());
    }
    for (index, entry) in entries.iter().enumerate() {
        let Some(object) = entry.as_object() else {
            return Err("commands must be an array of objects".to_string());
        };
        for key in ["command", "workDir", "exitCode"] {
            if !object.contains_key(key) {
                return Err(format!("commands[{index}] is missing field {key:?}"));
            }
        }
    }
    Ok(())
}

/// Validate the raw `artifacts` array shape (path + sha256 on every entry).
///
/// # Errors
///
/// Reports the first malformed entry.
pub fn validate_artifact_fields(raw: &Map<String, Value>) -> Result<(), String> {
    let entries = raw
        .get("artifacts")
        .and_then(Value::as_array)
        .ok_or_else(|| "artifacts must be an array of objects".to_string())?;
    for (index, entry) in entries.iter().enumerate() {
        let Some(object) = entry.as_object() else {
            return Err("artifacts must be an array of objects".to_string());
        };
        for key in ["path", "sha256"] {
            if !object.contains_key(key) {
                return Err(format!("artifacts[{index}] is missing field {key:?}"));
            }
        }
    }
    Ok(())
}

/// Whether the status string is one of the four the schema allows.
#[must_use]
pub fn valid_status(status: &str) -> bool {
    matches!(
        status,
        STATUS_PASS | STATUS_FAIL | STATUS_BLOCKED | STATUS_NOT_RUN
    )
}

/// Whether the evidence kind is one of the three the schema allows.
#[must_use]
pub fn valid_evidence_kind(kind: &str) -> bool {
    matches!(
        kind,
        EVIDENCE_AUTOMATION | EVIDENCE_NATIVE_DESKTOP | EVIDENCE_REAL_RUNTIME
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_the_four_statuses_are_valid() {
        for good in [STATUS_PASS, STATUS_FAIL, STATUS_BLOCKED, STATUS_NOT_RUN] {
            assert!(valid_status(good));
        }
        assert!(!valid_status("pass"));
        assert!(!valid_status("INVALID"));
    }
}
