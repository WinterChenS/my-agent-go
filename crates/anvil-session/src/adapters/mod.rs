//! The shipped adapters.
//!
//! Each one owns exactly one runtime and is constructible with an explicit
//! executable, so a test can point it at a stub script instead of the machine's
//! installation.

pub mod codex;
pub mod pi;

use std::path::{Path, PathBuf};
use std::process::Command;

use crate::adapter::Registry;

/// Registry with every adapter this build ships.
///
/// An adapter whose executable is missing is still registered: opening a
/// session on it then fails with a message that names the missing binary, which
/// is what the UI needs to show. Silently omitting it would look like "not
/// installed" for a build that is simply misconfigured.
#[must_use]
pub fn default_registry() -> Registry {
    let mut registry = Registry::new();
    let _ = registry.register(std::sync::Arc::new(pi::PiAdapter::new(locate("pi"))));
    let _ = registry.register(std::sync::Arc::new(codex::CodexAdapter::new(locate(
        "codex",
    ))));
    registry
}

fn locate(name: &str) -> Option<PathBuf> {
    find_on_path(name)
}

/// Locate an executable on `PATH`, honouring `PATHEXT`.
///
/// On Windows only files with a real executable extension are accepted. This
/// machine ships extension-less POSIX shims (`pi`, `codex`) that `PATH` resolves
/// first; handing one to the process API fails with "not a valid Win32
/// application", which is exactly the bug the catalog hit in R1.
#[must_use]
pub fn find_on_path(name: &str) -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    let extensions = path_extensions();

    for directory in std::env::split_paths(&path) {
        if directory.as_os_str().is_empty() {
            continue;
        }
        if !cfg!(windows) {
            let direct = directory.join(name);
            if direct.is_file() {
                return Some(direct);
            }
            continue;
        }
        for extension in &extensions {
            let candidate = directory.join(format!("{name}{extension}"));
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    None
}

fn path_extensions() -> Vec<String> {
    if cfg!(windows) {
        std::env::var("PATHEXT")
            .unwrap_or_else(|_| ".COM;.EXE;.BAT;.CMD".to_string())
            .split(';')
            .filter(|extension| !extension.trim().is_empty())
            .map(|extension| extension.to_ascii_lowercase())
            .collect()
    } else {
        Vec::new()
    }
}

/// Whether a path is a batch wrapper that needs a shell to start.
#[must_use]
pub fn is_script(path: &Path) -> bool {
    let name = path.to_string_lossy().to_ascii_lowercase();
    name.ends_with(".cmd") || name.ends_with(".bat")
}

/// Build a `Command` that can actually start `executable`.
///
/// A `.cmd`/`.bat` cannot be started directly, so it is wrapped in the system
/// command interpreter. Callers get one extra process layer, which is why
/// killing a run must terminate the tree (see [`kill_tree`]).
#[must_use]
pub fn command_for(executable: &Path) -> Command {
    if cfg!(windows) && is_script(executable) {
        let mut command = Command::new("cmd");
        command.arg("/c").arg(executable);
        return command;
    }
    Command::new(executable)
}

/// Terminate a child **and its descendants**.
///
/// A wrapped batch file keeps the real runtime as a grandchild; killing only the
/// wrapper leaves the runtime holding files and locks — that exact bug cost a
/// codex app-server its writer lock during development.
pub fn kill_tree(child: &mut std::process::Child) {
    let pid = child.id().to_string();
    let killed = Command::new("taskkill")
        .args(["/PID", &pid, "/T", "/F"])
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|status| status.success())
        .unwrap_or(false);
    if !killed {
        let _ = child.kill();
    }
    let _ = child.wait();
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_registry_has_both_runtimes() {
        let registry = default_registry();
        let ids = registry.ids();
        assert_eq!(ids, vec!["codex".to_string(), "pi".to_string()]);
    }

    #[test]
    fn finds_an_executable_that_exists() {
        // `cmd` resolves through PATHEXT to a real executable, never a shim.
        let found = find_on_path("cmd").expect("PATH lookup must work");
        let name = found.to_string_lossy().to_ascii_lowercase();
        assert!(name.ends_with(".exe"), "must not resolve to a shim: {name}");
        assert!(find_on_path("no-such-binary-anvil").is_none());
    }

    #[test]
    fn batch_wrappers_go_through_the_command_interpreter() {
        let program = command_for(Path::new("C:/tools/pi.cmd"))
            .get_program()
            .to_owned();
        if cfg!(windows) {
            assert_eq!(program.to_string_lossy().to_ascii_lowercase(), "cmd");
        }
    }
}
