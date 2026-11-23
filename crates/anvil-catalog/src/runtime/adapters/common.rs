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

/// Re-exported so session adapters can pin the same Node as discovery.
pub use crate::runtime::process::preferred_node;

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

/// Shared directory the npm-installed runtime shims live in.
///
/// `ANVIL_RUNTIME_PREFIX` wins when set, so a portable install (or an isolated
/// test rig) can put the shims somewhere else. The installer resolves the prefix
/// through this very function, which keeps "where we install" and "where we
/// look" from ever drifting apart.
///
/// Windows keeps the established `%LOCALAPPDATA%\pi-node\current`: every runtime
/// already on a Windows box lives there, so a new install lands beside them and
/// is discovered without touching `PATH`. macOS and Linux have no such shared
/// folder, so they get a per-user one under `~/.anvil/runtimes` (no `sudo`,
/// nothing written outside the user's own directory).
pub fn shared_prefix() -> Option<PathBuf> {
    if let Some(value) = std::env::var_os("ANVIL_RUNTIME_PREFIX") {
        let path = PathBuf::from(value);
        if !path.as_os_str().is_empty() {
            return Some(path);
        }
    }
    #[cfg(windows)]
    {
        if let Some(local) = std::env::var_os("LOCALAPPDATA") {
            return Some(PathBuf::from(local).join("pi-node").join("current"));
        }
    }
    Some(home().join(".anvil").join("runtimes"))
}

/// Where npm puts a package's `bin` shim inside [`shared_prefix`].
///
/// npm writes `<name>.cmd` at the prefix root on Windows, and `<prefix>/bin/<name>`
/// on macOS and Linux.
pub fn shared_shim(prefix: &Path, binary: &str) -> PathBuf {
    #[cfg(windows)]
    {
        prefix.join(format!("{binary}.cmd"))
    }
    #[cfg(not(windows))]
    {
        prefix.join("bin").join(binary)
    }
}

/// The npm executable to use for an install into [`shared_prefix`].
///
/// Prefers a copy shipped inside the prefix (Windows bundles one beside the
/// shims), then falls back to whatever `PATH` offers.
pub fn shared_npm(prefix: &Path) -> Option<PathBuf> {
    #[cfg(windows)]
    {
        for name in ["npm.cmd", "npm.exe", "npm"] {
            let candidate = prefix.join(name);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    #[cfg(not(windows))]
    {
        let candidate = prefix.join("bin").join("npm");
        if candidate.is_file() {
            return Some(candidate);
        }
    }
    process::which("npm")
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

/// Find every install of `executable` on `PATH`, in known global npm/Node
/// prefixes, and at the explicit candidates supplied by the adapter.
///
/// A GUI-launched app does not always inherit the shell's `PATH`, so scanning
/// `PATH` alone missed perfectly valid npm-global installs (for example a shim
/// in `%APPDATA%\npm`). Known prefixes are checked even when they are not on
/// `PATH`, and uniqueness is by normalised path so the same shim is reported
/// once no matter how many places it was found in.
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

    let path_dirs = std::env::var_os("PATH")
        .map(|value| std::env::split_paths(&value).collect::<Vec<_>>())
        .unwrap_or_default();
    for candidate in executable_paths(executable, &path_dirs) {
        push(candidate, InstallSource::Path);
    }
    for candidate in executable_paths(executable, &process::known_install_dirs()) {
        push(candidate, InstallSource::KnownDir);
    }
    for extra in extras {
        push(extra.clone(), InstallSource::KnownDir);
    }
    found
}

/// Expand one executable name into every runnable spelling in each directory.
fn executable_paths(executable: &str, directories: &[PathBuf]) -> Vec<PathBuf> {
    let extensions = executable_extensions();
    let mut found = Vec::new();
    for directory in directories {
        for extension in &extensions {
            let candidate = directory.join(format!("{executable}{extension}"));
            if candidate.is_file() {
                found.push(candidate);
            }
        }
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
            .map(|part| part.trim().to_lowercase())
            .collect()
    } else {
        vec![String::new()]
    }
}

/// Run npm shims with a known-good Node first on `PATH`.
///
/// npm shims call `node` by name. Windows boxes often have an old Node from a
/// development tool ahead of the real one, which makes `pi --version` fail
/// after a perfectly successful install. Prepending the newest working Node we
/// can start keeps probes and sessions on the same interpreter npm used.
pub fn node_env() -> Vec<(String, String)> {
    let Some(node) = process::preferred_node() else {
        return Vec::new();
    };
    let Some(directory) = node.parent() else {
        return Vec::new();
    };
    let original = std::env::var_os("PATH").unwrap_or_default();
    let mut entries = vec![directory.to_path_buf()];
    for path in std::env::split_paths(&original) {
        if path != directory {
            entries.push(path);
        }
    }
    match std::env::join_paths(entries) {
        Ok(value) => vec![("PATH".to_string(), value.to_string_lossy().into_owned())],
        Err(_) => Vec::new(),
    }
}

/// Run `--version` and return the parsed version plus the raw first line.
pub fn version_probe(
    executable: &Path,
    timeout: Duration,
) -> Result<(Option<Version>, String, u64)> {
    let output = process::run_with_env(executable, &["--version"], timeout, &node_env())?;
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn executable_paths_finds_the_platform_shim_in_a_directory() {
        let root = std::env::temp_dir().join(format!(
            "anvil-discover-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).expect("create dir");
        let name = if cfg!(windows) { "pi.cmd" } else { "pi" };
        let shim = root.join(name);
        std::fs::write(&shim, "@ECHO off\n").expect("write shim");

        let found = executable_paths("pi", &[root.clone()]);
        assert!(found.contains(&shim), "missed {}: {found:?}", shim.display());

        let _ = std::fs::remove_dir_all(&root);
    }
}
