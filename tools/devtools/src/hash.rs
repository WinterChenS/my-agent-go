//! Small filesystem helpers shared by `verify` and `gate`: SHA-256 digests and
//! the case-directory artifact globs.

use std::io::Read;
use std::path::{Path, PathBuf};

use sha2::{Digest, Sha256};

use crate::evidence::Artifact;

/// Hash a file with SHA-256 and return the lower-case hex digest.
///
/// # Errors
///
/// Propagates open and read failures so the caller can report which artifact
/// could not be hashed.
pub fn file_sha256(path: &Path) -> std::io::Result<String> {
    let mut file = std::fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0u8; 64 * 1024];
    loop {
        let read = file.read(&mut buffer)?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
    }
    Ok(format!("{:x}", hasher.finalize()))
}

/// Hash `case_dir/rel` and describe it as an [`Artifact`].
///
/// # Errors
///
/// Returns the underlying hashing failure as text.
pub fn hash_artifact(case_dir: &Path, rel: &str) -> Result<Artifact, String> {
    let path = case_dir.join(from_slash(rel));
    let sha256 = file_sha256(&path).map_err(|err| format!("{err}"))?;
    Ok(Artifact {
        path: rel.to_string(),
        sha256,
    })
}

/// Expand a case-directory glob such as `prototype/*.png`.
///
/// Supports `*` and `?` inside a path segment; `*` also matches a whole
/// segment. Returns files only, sorted, with the case directory removed so the
/// caller can hash them as artifacts.
///
/// # Errors
///
/// Returns the directory read failure that stopped the walk.
pub fn glob_relative(case_dir: &Path, pattern: &str) -> Result<Vec<String>, String> {
    let segments: Vec<&str> = pattern.split('/').filter(|s| !s.is_empty()).collect();
    let mut current = vec![PathBuf::new()];
    for segment in &segments {
        let mut next = Vec::new();
        for base in &current {
            let dir = case_dir.join(base);
            let Ok(entries) = std::fs::read_dir(&dir) else {
                continue;
            };
            let mut names: Vec<PathBuf> = entries
                .filter_map(Result::ok)
                .map(|entry| entry.file_name().into())
                .collect();
            names.sort();
            for name in names {
                let name_text = name.to_string_lossy().to_string();
                if !wildcard_match(segment, &name_text) {
                    continue;
                }
                next.push(base.join(&name_text));
            }
        }
        current = next;
    }
    let mut matches: Vec<String> = Vec::new();
    for relative in current {
        if case_dir.join(&relative).is_file() {
            matches.push(to_slash(&relative));
        }
    }
    matches.sort();
    Ok(matches)
}

/// Convert a forward-slash relative path into a platform path.
#[must_use]
pub fn from_slash(rel: &str) -> PathBuf {
    PathBuf::from(rel.replace('/', std::path::MAIN_SEPARATOR_STR))
}

/// Convert a platform path into a forward-slash relative path.
#[must_use]
pub fn to_slash(path: &Path) -> String {
    path.to_string_lossy().replace('\\', "/")
}

fn wildcard_match(pattern: &str, name: &str) -> bool {
    let pattern: Vec<char> = pattern.chars().collect();
    let name: Vec<char> = name.chars().collect();
    let (mut p, mut n) = (0usize, 0usize);
    let (mut star, mut resume) = (None, 0usize);
    while n < name.len() {
        if p < pattern.len() && (pattern[p] == '?' || pattern[p] == name[n]) {
            p += 1;
            n += 1;
        } else if p < pattern.len() && pattern[p] == '*' {
            star = Some(p);
            resume = n;
            p += 1;
        } else if let Some(star_index) = star {
            p = star_index + 1;
            resume += 1;
            n = resume;
        } else {
            return false;
        }
    }
    while p < pattern.len() && pattern[p] == '*' {
        p += 1;
    }
    p == pattern.len()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wildcards_cover_prefix_and_suffix_forms() {
        assert!(wildcard_match("*.png", "route-settings.png"));
        assert!(!wildcard_match("*.png", "route-settings.json"));
        assert!(wildcard_match("route-*.png", "route-mcp.png"));
        assert!(wildcard_match("route-????.png", "route-chat.png"));
        assert!(!wildcard_match("route-?????.png", "route-chat.png"));
        assert!(wildcard_match("*", "anything"));
        assert!(!wildcard_match("a*b", "ab_c"));
    }

    #[test]
    fn hashing_matches_a_known_digest() {
        // Fixtures live inside the worktree (.tmp) so no test writes to C:.
        let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("devtools-hash");
        std::fs::create_dir_all(&dir).expect("create dir");
        let file = dir.join("payload.txt");
        std::fs::write(&file, b"anvil").expect("write");
        let digest = file_sha256(&file).expect("hash");
        assert_eq!(
            digest,
            "50bc66d8aae5ae9020e15d53a103ddf91de202b3af5aeb3fd124b6861cdd2405"
        );
        std::fs::remove_dir_all(&dir).ok();
    }
}
