//! D:-only write-path checks, backed by the shared `anvil-paths` contract.
//!
//! Ported from `internal/preflight/preflight.go`. The gate and the `preflight`
//! subcommand both use these helpers, and both accept an injected
//! [`anvil_paths::Resolver`] so reject branches (other volume, reparse point,
//! unwritable directory) can be tested without creating fixtures on another
//! drive.

use std::path::{Component, Path, PathBuf};

use anvil_paths::{Resolution, Resolver};

/// The only volume Anvil tooling may write to on Windows.
pub const REQUIRED_VOLUME: &str = "D:";

/// Resolve `path` and require it to land on [`REQUIRED_VOLUME`]. Nothing is
/// created, so this is the check a read-only evidence root goes through.
///
/// # Errors
///
/// Returns a message that starts with `preflight:` and always names the
/// resolved volume, so a rejected path is diagnosable from the log alone.
pub fn check_path(resolver: &dyn Resolver, path: &Path) -> Result<Resolution, String> {
    if path.as_os_str().is_empty() {
        return Err(format!(
            "preflight: {REQUIRED_VOLUME} is required: empty path"
        ));
    }
    let resolution = resolver
        .resolve(path)
        .map_err(|err| format!("preflight: {err}"))?;
    if !volume_allowed(&resolution.volume) {
        return Err(format!(
            "preflight: path is outside the required {REQUIRED_VOLUME} volume: {} resolves to volume {}",
            quote(&path.display().to_string()),
            quote(&resolution.volume)
        ));
    }
    Ok(resolution)
}

/// Resolve `path`, require [`REQUIRED_VOLUME`], create the directory, re-resolve
/// it (a component may have become a junction in between) and prove it accepts
/// a write. No branch substitutes another directory.
///
/// # Errors
///
/// Returns the volume rejection, the creation failure or the write-probe
/// failure, prefixed with `preflight:`.
pub fn ensure_dir(resolver: &dyn Resolver, path: &Path) -> Result<Resolution, String> {
    let resolution = check_path(resolver, path)?;
    resolver
        .create_dir_all(&resolution.resolved)
        .map_err(|err| {
            format!(
                "preflight: path is not writable: create {}: {err}",
                resolution.resolved.display()
            )
        })?;
    let resolved = check_path(resolver, &resolution.resolved)?;
    resolver.probe_writable(&resolved.resolved).map_err(|err| {
        format!(
            "preflight: path is not writable: probe {}: {err}",
            resolved.resolved.display()
        )
    })?;
    Ok(resolved)
}

/// Whether the reported volume is the required one.
///
/// Normalizes `\\?\D:` to `D:` and compares case-insensitively, mirroring the
/// Go helper so a verbatim or UNC spelling cannot pass by accident.
#[must_use]
pub fn volume_allowed(volume: &str) -> bool {
    let trimmed = volume.trim();
    let stripped = trimmed.strip_prefix(r"\\?\").unwrap_or(trimmed);
    let without_separator = stripped.trim_end_matches(['\\', '/']);
    without_separator.eq_ignore_ascii_case(REQUIRED_VOLUME)
}

/// Reject a path that escapes `base` after lexical normalization.
///
/// This is the text-level guard that runs before any filesystem resolution;
/// the gate also resolves both paths and checks again, so a junction cannot
/// hand it evidence from outside the root.
///
/// # Errors
///
/// Returns `"<target>" is outside "<base>"`.
pub fn ensure_within(base: &Path, target: &Path) -> Result<(), String> {
    let base_key = lexical_key(base);
    let target_key = lexical_key(target);
    if target_key.len() < base_key.len() || target_key[..base_key.len()] != base_key[..] {
        return Err(format!(
            "{} is outside {}",
            quote(&target.display().to_string()),
            quote(&base.display().to_string())
        ));
    }
    Ok(())
}

/// Join a case directory and require it to stay under the evidence root.
///
/// # Errors
///
/// Returns the escape reason from [`ensure_within`].
pub fn case_dir(root: &Path, sha: &str, target: &str, case_id: &str) -> Result<PathBuf, String> {
    let dir = root.join(sha).join(target).join(case_id);
    ensure_within(root, &dir)?;
    Ok(dir)
}

/// Lower-cased component list used for case-insensitive Windows comparisons.
///
/// `..` components are applied rather than kept, so `a/../../b` cannot look
/// like it stays inside `a`.
fn lexical_key(path: &Path) -> Vec<String> {
    let mut parts: Vec<String> = Vec::new();
    for component in path.components() {
        match component {
            Component::Prefix(prefix) => {
                parts.push(prefix.as_os_str().to_string_lossy().to_lowercase());
            }
            Component::RootDir => parts.push(String::new()),
            Component::CurDir => {}
            Component::ParentDir => {
                parts.pop();
            }
            Component::Normal(name) => parts.push(name.to_string_lossy().to_lowercase()),
        }
    }
    parts
}

fn quote(value: &str) -> String {
    format!("\"{value}\"")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn volumes_are_compared_after_normalization() {
        assert!(volume_allowed("D:"));
        assert!(volume_allowed("d:"));
        assert!(volume_allowed(r"\\?\D:"));
        assert!(volume_allowed(r"D:\"));
        assert!(!volume_allowed("C:"));
        assert!(!volume_allowed(r"\\server\share"));
        assert!(!volume_allowed(""));
    }

    #[test]
    fn lexical_escape_is_rejected() {
        let base = Path::new(r"D:\work\.tmp\evidence");
        assert!(ensure_within(base, &base.join("sha").join("win-x64")).is_ok());
        assert!(ensure_within(base, Path::new(r"D:\work\.tmp\evidence\..\..\etc")).is_err());
        assert!(ensure_within(base, Path::new(r"D:\other\evidence\x")).is_err());
        assert!(ensure_within(base, Path::new(r"C:\evidence\x")).is_err());
    }
}
