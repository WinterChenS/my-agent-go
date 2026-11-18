//! Process helpers with deadlines.
//!
//! Discovery and probing must never hang the host: every child gets a deadline,
//! its pipes are drained on separate threads so a chatty CLI cannot deadlock,
//! and no window is created.

use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use crate::error::{Error, Result};

/// Result of one bounded child process.
#[derive(Clone, Debug, Default)]
pub struct CommandOutput {
    pub code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
    pub elapsed_ms: u64,
    pub timed_out: bool,
}

impl CommandOutput {
    /// Combined output, trimmed, for version and handshake parsing.
    pub fn text(&self) -> String {
        let mut text = self.stdout.trim().to_string();
        if text.is_empty() {
            text = self.stderr.trim().to_string();
        }
        text
    }

    pub fn success(&self) -> bool {
        self.code == Some(0)
    }
}

/// Run `program` with `args`, killing it at `timeout`.
pub fn run(program: &Path, args: &[&str], timeout: Duration) -> Result<CommandOutput> {
    run_with_env(program, args, timeout, &[])
}

/// Same as [`run`] with extra environment variables.
pub fn run_with_env(
    program: &Path,
    args: &[&str],
    timeout: Duration,
    env: &[(String, String)],
) -> Result<CommandOutput> {
    if !program.exists() {
        return Err(Error::NotFound(format!(
            "executable {} does not exist",
            program.display()
        )));
    }
    let mut command = Command::new(program);
    command
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    for (key, value) in env {
        command.env(key, value);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        command.creation_flags(CREATE_NO_WINDOW);
    }

    let started = Instant::now();
    let mut child = command
        .spawn()
        .map_err(|error| Error::Io(format!("spawn {}: {error}", program.display())))?;

    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let stdout_reader = std::thread::spawn(move || read_all(stdout));
    let stderr_reader = std::thread::spawn(move || read_all(stderr));

    let mut timed_out = false;
    let code = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status.code(),
            Ok(None) => {}
            Err(error) => {
                let _ = child.kill();
                return Err(Error::Io(format!("wait {}: {error}", program.display())));
            }
        }
        if started.elapsed() >= timeout {
            timed_out = true;
            let _ = child.kill();
            let _ = child.wait();
            break None;
        }
        std::thread::sleep(Duration::from_millis(25));
    };

    let stdout = stdout_reader.join().unwrap_or_default();
    let stderr = stderr_reader.join().unwrap_or_default();
    Ok(CommandOutput {
        code,
        stdout,
        stderr,
        elapsed_ms: started.elapsed().as_millis() as u64,
        timed_out,
    })
}

fn read_all<R: Read>(reader: Option<R>) -> String {
    let Some(mut reader) = reader else {
        return String::new();
    };
    let mut buffer = Vec::new();
    // A CLI that keeps writing after the deadline is killed, so a short read is
    // expected; the partial text is still what the caller wants to inspect.
    let _ = reader.read_to_end(&mut buffer);
    String::from_utf8_lossy(&buffer).into_owned()
}

/// First `name` found on `PATH`, honouring `PATHEXT` on Windows.
pub fn which(name: &str) -> Option<PathBuf> {
    let extensions = executable_extensions();
    for directory in path_entries() {
        if let Some(found) = find_in_dir(&directory, name, &extensions) {
            return Some(found);
        }
    }
    None
}

fn path_entries() -> Vec<PathBuf> {
    std::env::var_os("PATH")
        .map(|value| std::env::split_paths(&value).collect())
        .unwrap_or_default()
}

fn executable_extensions() -> Vec<String> {
    if cfg!(windows) {
        let raw = std::env::var("PATHEXT").unwrap_or_else(|_| ".COM;.EXE;.BAT;.CMD".to_string());
        // No empty extension on Windows: the extensionless sibling of an npm
        // shim is a POSIX script that CreateProcess cannot run, and listing it
        // produced a second, always-failing registration per runtime.
        raw.split(';')
            .filter(|part| !part.trim().is_empty())
            .map(|part| part.trim().to_lowercase())
            .collect()
    } else {
        vec![String::new()]
    }
}

fn find_in_dir(directory: &Path, name: &str, extensions: &[String]) -> Option<PathBuf> {
    for extension in extensions {
        let candidate = directory.join(format!("{name}{extension}"));
        if candidate.is_file() {
            return Some(candidate);
        }
        // Windows is case-insensitive but the on-disk name may differ from the
        // requested one; a directory listing is cheap for the few roots here.
        if cfg!(windows) {
            let wanted = format!("{name}{extension}").to_lowercase();
            if let Ok(entries) = std::fs::read_dir(directory) {
                for entry in entries.flatten() {
                    let file_name = entry.file_name().to_string_lossy().to_lowercase();
                    if file_name == wanted && entry.path().is_file() {
                        return Some(entry.path());
                    }
                }
            }
        }
    }
    None
}

/// Directories a runtime can be installed into without being on `PATH`.
pub fn known_install_dirs() -> Vec<PathBuf> {
    let mut dirs = Vec::new();
    // The installer honours `ANVIL_RUNTIME_PREFIX`; lookups must follow it,
    // otherwise a portable prefix installs fine but is never discovered. Pushed
    // first so it wins over the per-user default.
    if let Some(value) = std::env::var_os("ANVIL_RUNTIME_PREFIX") {
        let path = PathBuf::from(value);
        if !path.as_os_str().is_empty() {
            dirs.push(path);
        }
    }
    let mut push_env = |name: &str, suffix: &str| {
        if let Some(base) = std::env::var_os(name) {
            let mut path = PathBuf::from(base);
            for part in suffix.split('/').filter(|part| !part.is_empty()) {
                path.push(part);
            }
            dirs.push(path);
        }
    };
    push_env("LOCALAPPDATA", "pi-node/current");
    push_env("LOCALAPPDATA", "Programs");
    push_env("APPDATA", "npm");
    // macOS and Linux have no `LOCALAPPDATA`: the per-user prefix the installer
    // writes to is `~/.anvil/runtimes`, whose `bin/` is where npm drops shims.
    // The other entries are the usual per-user npm/bun install locations.
    for home in ["USERPROFILE", "HOME"] {
        push_env(home, ".anvil/runtimes/bin");
        push_env(home, ".local/bin");
        push_env(home, ".bun/bin");
        push_env(home, ".npm-global/bin");
    }
    dirs
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn which_finds_a_shell_command() {
        let name = if cfg!(windows) { "cmd" } else { "sh" };
        assert!(which(name).is_some(), "{name} should be on PATH");
    }

    #[test]
    fn missing_program_is_not_found() {
        let result = run(
            Path::new("definitely-not-a-program-anvil"),
            &["--version"],
            Duration::from_millis(500),
        );
        assert!(matches!(result, Err(Error::NotFound(_))));
    }

    #[test]
    fn timeout_is_reported() {
        let program = if cfg!(windows) {
            which("ping").expect("ping")
        } else {
            which("sleep").expect("sleep")
        };
        let args: Vec<&str> = if cfg!(windows) {
            vec!["-n", "6", "127.0.0.1"]
        } else {
            vec!["5"]
        };
        let output = run(&program, &args, Duration::from_millis(300)).expect("spawn");
        assert!(output.timed_out);
        assert!(output.code.is_none());
    }
}
