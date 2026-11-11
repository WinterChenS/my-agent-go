//! `tests/acceptance/rebuild.json`: the acceptance matrix document.
//!
//! Ported from `internal/acceptance/matrix.go` (R0-RS03). The validation rules
//! and the error text are kept identical so old evidence and gate transcripts
//! still read the same way.

use std::path::Path;

use serde::Deserialize;

/// The only matrix schema this build understands.
pub const MATRIX_SCHEMA_VERSION: i64 = 1;
/// A matrix document larger than this is rejected instead of parsed.
const MAX_MATRIX_BYTES: u64 = 4 << 20;

/// The acceptance matrix document.
#[derive(Debug, Clone, Deserialize)]
pub struct Matrix {
    #[serde(rename = "schemaVersion")]
    pub schema_version: i64,
    #[serde(default)]
    pub gates: Vec<Gate>,
}

/// One stage gate: targets, required tasks and the cases registered so far.
#[derive(Debug, Clone, Deserialize)]
pub struct Gate {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub targets: Vec<String>,
    #[serde(rename = "requiredTasks", default)]
    pub required_tasks: Vec<String>,
    #[serde(default)]
    pub cases: Vec<Case>,
}

/// One registered acceptance case.
#[derive(Debug, Clone, Deserialize)]
pub struct Case {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub task: String,
    #[serde(rename = "evidenceKind", default)]
    pub evidence_kind: String,
    #[serde(default)]
    pub required: bool,
}

/// Read and validate the matrix document.
///
/// # Errors
///
/// Returns the same human-readable reasons the Go implementation produced:
/// unreadable file, oversized document, parse failure, wrong schema version,
/// no gates, duplicate or empty gate ids, gates without targets or tasks and
/// identifiers that cannot be used as a path segment.
pub fn load_matrix(path: &Path) -> Result<Matrix, String> {
    if path.as_os_str().is_empty() {
        return Err("matrix path is empty".to_string());
    }
    let meta = std::fs::metadata(path).map_err(|err| format!("read matrix: {err}"))?;
    if meta.len() > MAX_MATRIX_BYTES {
        return Err(format!("matrix {} exceeds 4 MiB", path.display()));
    }
    let data = std::fs::read(path).map_err(|err| format!("read matrix: {err}"))?;
    if data.len() as u64 > MAX_MATRIX_BYTES {
        return Err(format!("matrix {} exceeds 4 MiB", path.display()));
    }
    let matrix: Matrix =
        serde_json::from_slice(&data).map_err(|err| format!("parse matrix: {err}"))?;
    if matrix.schema_version != MATRIX_SCHEMA_VERSION {
        return Err(format!(
            "matrix schemaVersion = {}, want {MATRIX_SCHEMA_VERSION}",
            matrix.schema_version
        ));
    }
    if matrix.gates.is_empty() {
        return Err("matrix declares no gates".to_string());
    }
    let mut seen: Vec<&str> = Vec::new();
    for gate in &matrix.gates {
        if gate.id.trim().is_empty() {
            return Err("matrix contains a gate without an id".to_string());
        }
        if seen.contains(&gate.id.as_str()) {
            return Err(format!("matrix declares duplicate gate {:?}", gate.id));
        }
        seen.push(gate.id.as_str());
        if gate.targets.is_empty() {
            return Err(format!("gate {:?} declares no targets", gate.id));
        }
        if gate.required_tasks.is_empty() {
            return Err(format!("gate {:?} declares no required tasks", gate.id));
        }
        for target in &gate.targets {
            validate_identifier(target, "target")
                .map_err(|err| format!("gate {:?}: {err}", gate.id))?;
        }
        for task in &gate.required_tasks {
            validate_identifier(task, "task id")
                .map_err(|err| format!("gate {:?}: {err}", gate.id))?;
        }
    }
    Ok(matrix)
}

/// Reject anything that cannot be used as a single, predictable path segment.
///
/// Allowed: letters, digits, `.`, `_`, `-`; 1..=128 bytes; the first character
/// must be alphanumeric so `.`, `..` and hidden names cannot slip through.
pub fn validate_identifier(value: &str, what: &str) -> Result<(), String> {
    if value.is_empty() || value.len() > 128 {
        return Err(format!("invalid {what} {value:?}"));
    }
    for (index, byte) in value.bytes().enumerate() {
        let allowed = byte.is_ascii_alphanumeric() || byte == b'.' || byte == b'_' || byte == b'-';
        let leading_bad = index == 0 && (byte == b'.' || byte == b'_' || byte == b'-');
        if !allowed || leading_bad {
            return Err(format!(
                "invalid {what} {value:?} (letters, digits, dot, underscore and dash only)"
            ));
        }
    }
    Ok(())
}

/// Whether `needle` appears in `haystack` (small helper for gate checks).
#[must_use]
pub fn contains(values: &[String], want: &str) -> bool {
    values.iter().any(|value| value == want)
}

/// Lower-case, trimmed commit id.
#[must_use]
pub fn normalize_sha(value: &str) -> String {
    value.trim().to_ascii_lowercase()
}

/// Whether the value is a full 40-character lower-case hex commit id.
#[must_use]
pub fn is_hex_sha(value: &str) -> bool {
    value.len() == 40
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// Whether the value is a 64-character lower-case hex digest.
#[must_use]
pub fn is_sha256(value: &str) -> bool {
    value.len() == 64
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn identifiers_reject_path_traversal_and_empty_values() {
        for bad in ["", "..", ".hidden", "_x", "-x", "a/b", "a\\b", "a b", "a:b"] {
            assert!(
                validate_identifier(bad, "case id").is_err(),
                "{bad:?} must be rejected"
            );
        }
        for good in ["R0-00-build-gate", "win-x64", "a.b_c-d", "A1"] {
            assert!(
                validate_identifier(good, "case id").is_ok(),
                "{good:?} must be accepted"
            );
        }
    }

    #[test]
    fn shas_must_be_full_length_hex() {
        assert!(is_hex_sha("21c4839513a12ddbbbfc50f92a8c2471574bd6a6"));
        assert!(!is_hex_sha("21c4839"));
        assert!(!is_hex_sha("21C4839513A12DDBBBFC50F92A8C2471574BD6A6"));
        assert!(is_sha256(&"a".repeat(64)));
        assert!(!is_sha256(&"a".repeat(63)));
    }
}
