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
    default_registry_with_pi(std::sync::Arc::new(pi::PiAdapter::new(locate("pi"))))
}

/// Same registry, with the pi adapter supplied by the caller.
///
/// The desktop host builds it around the runtime that ships in the installer
/// (ADR 0009); tests build it around a stub or a scratch bundle.
#[must_use]
pub fn default_registry_with_pi(pi: std::sync::Arc<dyn crate::adapter::SessionAdapter>) -> Registry {
    let mut registry = Registry::new();
    let _ = registry.register(pi);
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
/// Uses the catalog's search list, not the raw environment: on macOS an app
/// started from Finder inherits launchd's minimal `PATH`, so a Homebrew or nvm
/// install would be invisible here even though the catalog had just discovered
/// it. On Windows only files with a real executable extension are accepted —
/// this machine ships extension-less POSIX shims (`pi`, `codex`) that `PATH`
/// resolves first, and handing one to the process API fails with "not a valid
/// Win32 application".
#[must_use]
pub fn find_on_path(name: &str) -> Option<PathBuf> {
    anvil_catalog::runtime::process::which(name)
}

/// Whether a path is a batch wrapper that needs a shell to start.
#[must_use]
pub fn is_script(path: &Path) -> bool {
    let name = path.to_string_lossy().to_ascii_lowercase();
    name.ends_with(".cmd") || name.ends_with(".bat")
}

/// Build a `Command` that can actually start `executable`.
///
/// Three cases, in order:
///
/// 1. an npm-style `.cmd` shim is rewritten into `node <cli.js>`, so the
///    arguments reach the runtime untouched — the interpreter splits an argument
///    on newlines, which silently turned a multi-line prompt into a handful of
///    stray arguments and a run that produced nothing;
/// 2. any other `.cmd`/`.bat` is wrapped in the interpreter, because it cannot
///    be started directly;
/// 3. everything else is started as-is.
#[must_use]
pub fn command_for(executable: &Path) -> Command {
    let mut command = if cfg!(windows) && is_script(executable) {
        if let Some((node, script)) = node_shim_target(executable) {
            let mut command = Command::new(node);
            command.arg(script);
            command
        } else {
            let mut command = Command::new("cmd");
            command.arg("/c").arg(executable);
            command
        }
    } else {
        Command::new(executable)
    };
    // The child has to run in the same PATH discovery searched. A macOS npm
    // shim starts with `#!/usr/bin/env node`, and an app launched from Finder
    // has no Node on its inherited PATH: the shim would exit with "env: node:
    // No such file or directory" right after being discovered successfully.
    for (key, value) in anvil_catalog::runtime::process::child_env() {
        command.env(key, value);
    }
    // A GUI host otherwise gets a console window for every runtime turn.
    anvil_catalog::runtime::process::hide_console(&mut command);
    command
}

/// Resolve an npm-style shim to the Node script it launches.
///
/// The shim is text; the interesting line looks like
/// `"%_prog%"  "%dp0%\node_modules\<pkg>\dist\bundle\cli.js" %*`. Only that shape
/// is accepted: a batch file that happens to mention a `.js` path but does more
/// than launch it must keep going through the interpreter.
fn node_shim_target(script: &Path) -> Option<(PathBuf, PathBuf)> {
    let text = std::fs::read_to_string(script).ok()?;
    let line = text
        .lines()
        .find(|line| line.contains("node_modules") && line.contains(".js"))?;
    let raw = line
        .split('"')
        .find(|part| part.to_ascii_lowercase().contains("node_modules") && part.ends_with(".js"))?;
    let directory = script.parent()?;
    // `%dp0%` is the shim's own directory; the trailing separator is kept by the
    // shim, so strip it together with the variable.
    let relative = raw
        .replace("%dp0%\\", "")
        .replace("%dp0%/", "")
        .replace("%dp0%", "");
    let target = if Path::new(&relative).is_absolute() {
        PathBuf::from(relative)
    } else {
        directory.join(relative)
    };
    if !target.is_file() {
        return None;
    }
    // Prefer an absolute Node pinned by our installer. A plain `node` falls
    // back to PATH, where an old Node from another tool can shadow the one npm
    // used and make the CLI fail before it prints a version.
    let quoted: Vec<&str> = line.split('"').collect();
    let node = quoted
        .iter()
        .find(|part| {
            let name = Path::new(part)
                .file_name()
                .and_then(|value| value.to_str())
                .unwrap_or("")
                .to_ascii_lowercase();
            (name == "node.exe" || name == "node")
                && Path::new(part).is_absolute()
                && Path::new(part).is_file()
        })
        .map(PathBuf::from)
        .or_else(|| {
            let sibling = directory.join("node.exe");
            sibling.is_file().then_some(sibling)
        })
        .or_else(|| {
            anvil_catalog::runtime::adapters::common::preferred_node().map(Path::to_path_buf)
        })
        .unwrap_or_else(|| PathBuf::from("node"));
    Some((node, target))
}

/// Terminate a child **and its descendants**.
///
/// A wrapped batch file keeps the real runtime as a grandchild; killing only the
/// wrapper leaves the runtime holding files and locks — that exact bug cost a
/// codex app-server its writer lock during development.
pub fn kill_tree(child: &mut std::process::Child) {
    let pid = child.id().to_string();
    let mut taskkill = Command::new("taskkill");
    taskkill
        .args(["/PID", &pid, "/T", "/F"])
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null());
    anvil_catalog::runtime::process::hide_console(&mut taskkill);
    let killed = taskkill
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
    fn a_batch_file_that_is_not_a_node_shim_goes_through_the_interpreter() {
        let program = command_for(Path::new("C:/tools/pi.cmd"))
            .get_program()
            .to_owned();
        if cfg!(windows) {
            assert_eq!(program.to_string_lossy().to_ascii_lowercase(), "cmd");
        }
    }

    #[test]
    fn an_npm_shim_is_rewritten_to_launch_node_directly() {
        if !cfg!(windows) {
            return;
        }
        let root = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("shim");
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(root.join("node_modules/pkg/dist")).expect("create");
        std::fs::write(root.join("node_modules/pkg/dist/cli.js"), "process.exit(0)")
            .expect("write");
        std::fs::write(root.join("node.exe"), "").expect("write node");
        let shim = root.join("pkg.cmd");
        std::fs::write(
            &shim,
            "@ECHO off\r\n\"%_prog%\"  \"%dp0%\\node_modules\\pkg\\dist\\cli.js\" %*\r\n",
        )
        .expect("write shim");

        let command = command_for(&shim);
        assert_eq!(
            command.get_program().to_string_lossy().to_ascii_lowercase(),
            root.join("node.exe").to_string_lossy().to_ascii_lowercase()
        );
        let args: Vec<String> = command
            .get_args()
            .map(|arg| arg.to_string_lossy().to_string())
            .collect();
        assert_eq!(args.len(), 1);
        assert!(args[0].ends_with("cli.js"), "{args:?}");
    }

    #[test]
    fn an_installer_pinned_node_beats_the_path_lookup() {
        if !cfg!(windows) {
            return;
        }
        let root = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("shim-pinned");
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(root.join("node_modules/pkg/dist")).expect("create");
        std::fs::write(root.join("node_modules/pkg/dist/cli.js"), "process.exit(0)")
            .expect("write");
        std::fs::create_dir_all(root.join("pinned")).expect("create pinned dir");
        let pinned = root.join("pinned/node.exe");
        std::fs::write(&pinned, "").expect("write pinned node");
        std::fs::write(root.join("node.exe"), "").expect("write sibling node");
        let shim = root.join("pkg.cmd");
        std::fs::write(
            &shim,
            format!(
                "@ECHO off\r\n\"{}\" \"%dp0%\\node_modules\\pkg\\dist\\cli.js\" %*\r\n",
                pinned.display()
            ),
        )
        .expect("write shim");

        let command = command_for(&shim);
        assert_eq!(
            command.get_program().to_string_lossy().to_lowercase(),
            pinned.to_string_lossy().to_lowercase()
        );
    }
}
