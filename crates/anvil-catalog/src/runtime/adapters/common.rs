//! Shared helpers for the concrete adapters.
//!
//! Everything here is vendor-neutral: path candidates, bounded version calls and
//! config reads with a size limit.

use std::path::{Path, PathBuf};
use std::time::Duration;

use crate::error::Result;
use crate::ids::path_key;
use crate::model::{InstallSource, RuntimeCandidate};
use crate::runtime::process;
use crate::runtime::version::Version;

/// User home directory.
pub fn home() -> PathBuf {
    if let Some(value) = std::env::var_os("USERPROFILE") {
        return PathBuf::from(value);
    }
    if let Some(value) = std::env::var_os("HOME") {
        return PathBuf::from(value);
    }
    PathBuf::from(".")
}

/// Read a small text file, refusing anything above `limit` bytes.
pub fn read_text(path: &Path, limit: usize) -> Option<String> {
    let metadata = std::fs::metadata(path).ok()?;
    if metadata.len() as usize > limit {
        return None;
    }
    std::fs::read_to_string(path).ok()
}

/// Read a JSON file.
pub fn read_json(path: &Path, limit: usize) -> Option<serde_json::Value> {
    let text = read_text(path, limit)?;
    serde_json::from_str(&text).ok()
}

/// Find every install of `executable` on `PATH` plus the extra candidate paths.
///
/// Candidates are deduplicated by normalised path so a shim that is also on
/// `PATH` and in a known directory is discovered once.
pub fn discover_installs(
    runtime_id: &str,
    executable: &str,
    extras: &[PathBuf],
) -> Vec<RuntimeCandidate> {
    let mut found: Vec<RuntimeCandidate> = Vec::new();
    let mut seen: Vec<String> = Vec::new();

    let mut push = |path: PathBuf, source: InstallSource| {
        if !path.is_file() {
            return;
        }
        let key = path_key(&path);
        if seen.contains(&key) {
            return;
        }
        seen.push(key);
        found.push(RuntimeCandidate {
            runtime_id: runtime_id.to_string(),
            executable: path,
            source,
            instance: None,
        });
    };

    for directory in std::env::var_os("PATH")
        .map(|value| std::env::split_paths(&value).collect::<Vec<_>>())
        .unwrap_or_default()
    {
        for extension in executable_extensions() {
            let candidate = directory.join(format!("{executable}{extension}"));
            if candidate.is_file() {
                push(candidate, InstallSource::Path);
            }
        }
    }
    for extra in extras {
        push(extra.clone(), InstallSource::KnownDir);
    }
    found
}

fn executable_extensions() -> Vec<String> {
    if cfg!(windows) {
        let raw = std::env::var("PATHEXT").unwrap_or_else(|_| ".COM;.EXE;.BAT;.CMD".to_string());
        // Windows only runs what PATHEXT lists; the extensionless sibling of an
        // npm shim is a POSIX script, so it must not become a candidate.
        raw.split(';')
            .filter(|part| !part.trim().is_empty())
            .map(str::to_string)
            .collect()
    } else {
        vec![String::new()]
    }
}

/// Run `--version` and return the parsed version plus the raw first line.
pub fn version_probe(
    executable: &Path,
    timeout: Duration,
) -> Result<(Option<Version>, String, u64)> {
    let output = process::run(executable, &["--version"], timeout)?;
    let text = output.text();
    let first_line = text.lines().next().unwrap_or("").trim().to_string();
    Ok((Version::find(&text), first_line, output.elapsed_ms))
}

/// Whether a JSON object at `path` has at least one key (an auth store).
pub fn has_credentials(path: &Path) -> bool {
    match read_json(path, 1 << 20) {
        Some(serde_json::Value::Object(map)) => !map.is_empty(),
        _ => false,
    }
}
