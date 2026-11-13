//! Stable registration identifiers.
//!
//! A registration is identified by the adapter's runtime id, the normalised
//! install path and an optional config-instance key. The identity must survive
//! a restart and must not depend on discovery order, so it is a digest rather
//! than a row id.

use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};

/// Normalise an install path for comparison and identity.
///
/// Windows resolves the same file many ways (`C:\X\codex.cmd`,
/// `c:\x\CODEX.CMD`, `\\?\C:\X\codex.cmd`), so the key is the canonical path
/// with the verbatim prefix removed and the whole string lowercased.
pub fn path_key(path: &Path) -> String {
    let canonical = std::fs::canonicalize(path).unwrap_or_else(|_| PathBuf::from(path));
    let text = canonical.to_string_lossy().replace('\\', "/");
    let text = text.strip_prefix("//?/").unwrap_or(&text).to_string();
    let text = text.strip_prefix("//./").unwrap_or(&text).to_string();
    text.trim_end_matches('/').to_lowercase()
}

/// Composite key used for uniqueness inside the store.
pub fn instance_key(path: &Path, instance: Option<&str>) -> String {
    match instance {
        Some(instance) => format!("{}|{}", path_key(path), instance.to_lowercase()),
        None => path_key(path),
    }
}

/// Stable registration id: `<runtime id>:<16 hex chars>`.
pub fn registration_id(runtime_id: &str, path: &Path, instance: Option<&str>) -> String {
    let mut hasher = Sha256::new();
    hasher.update(runtime_id.as_bytes());
    hasher.update(b"\0");
    hasher.update(instance_key(path, instance).as_bytes());
    let digest = hasher.finalize();
    let hex: String = digest[..8]
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect();
    format!("{runtime_id}:{hex}")
}

/// Id for a row whose adapter is no longer registered.
///
/// Unknown adapters still need a row so the UI can show what was found before,
/// and the id must not collide with a live registration.
pub fn orphan_id(path: &Path) -> String {
    registration_id("orphan", path, None)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalises_separators_and_case() {
        let a = path_key(Path::new(r"C:\Users\Public\Codex.CMD"));
        let b = path_key(Path::new(r"c:/users/public/codex.cmd"));
        assert_eq!(a, b);
        assert!(a.starts_with("c:/"));
        assert!(!a.contains('\\'));
    }

    #[test]
    fn strips_verbatim_prefix() {
        let key = path_key(Path::new(r"\\?\C:\Tools\pi.exe"));
        assert_eq!(key, "c:/tools/pi.exe");
    }

    #[test]
    fn ids_are_stable_and_instance_scoped() {
        let path = Path::new(r"C:\Tools\pi.exe");
        let first = registration_id("pi", path, None);
        let second = registration_id("pi", Path::new(r"c:\tools\PI.EXE"), None);
        assert_eq!(first, second);
        assert!(first.starts_with("pi:"));
        assert_eq!(first.len(), 3 + 16);

        let scoped = registration_id("pi", path, Some("Work"));
        assert_ne!(first, scoped);
    }

    #[test]
    fn runtime_id_changes_the_digest() {
        let path = Path::new(r"C:\Tools\cli.exe");
        assert_ne!(
            registration_id("pi", path, None),
            registration_id("codex", path, None)
        );
    }
}
