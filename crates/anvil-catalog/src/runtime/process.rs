//! Process helpers with deadlines.
//!
//! Discovery and probing must never hang the host: every child gets a deadline,
//! its pipes are drained on separate threads so a chatty CLI cannot deadlock,
//! and no window is created.

use std::collections::HashSet;
use std::io::{BufRead, BufReader, Read};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::OnceLock;
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant};

use crate::error::{Error, Result};
use crate::runtime::version::Version;

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

/// The newest Node.js executable this machine can actually start.
///
/// npm `.cmd` shims resolve `node` from `PATH` at call time. On Windows that
/// PATH can contain an old Node bundled with another tool ahead of the real
/// install; `pi --version` then dies with a syntax error even though npm
/// installed the package correctly. Probing and launching runtimes must pin a
/// working Node instead of trusting whatever happens to be first on PATH.
static PREFERRED_NODE: OnceLock<Option<PathBuf>> = OnceLock::new();

/// A Node executable that can run the installed CLIs, if the machine has one.
pub fn preferred_node() -> Option<&'static Path> {
    PREFERRED_NODE
        .get_or_init(|| {
            let mut seen = HashSet::new();
            let mut best: Option<(PathBuf, Version)> = None;
            let mut candidates = Vec::new();

            if let Some(found) = which("node") {
                candidates.push(found);
            }
            for directory in path_entries() {
                for name in ["node.exe", "node"] {
                    candidates.push(directory.join(name));
                }
            }

            for candidate in candidates {
                if !candidate.is_file() {
                    continue;
                }
                let key = candidate.to_string_lossy().to_lowercase();
                if !seen.insert(key) {
                    continue;
                }
                let Ok(output) = run(&candidate, &["--version"], Duration::from_secs(2)) else {
                    continue;
                };
                if !output.success() {
                    continue;
                }
                let Some(version) = Version::find(&output.text()) else {
                    continue;
                };
                if best.as_ref().is_none_or(|(_, current)| version > *current) {
                    best = Some((candidate, version));
                }
            }
            best.map(|(path, _)| path)
        })
        .as_deref()
}

/// Stop a console child from opening a window over the desktop app.
///
/// A GUI-subsystem host still gets a visible console for every console child
/// (`node`, `curl`, `taskkill`, …) unless `CREATE_NO_WINDOW` is set. Every
/// spawn in the product goes through this, so one operation cannot flash a
/// black window while its neighbour stays quiet.
#[cfg(windows)]
pub fn hide_console(command: &mut Command) {
    use std::os::windows::process::CommandExt;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    command.creation_flags(CREATE_NO_WINDOW);
}

#[cfg(not(windows))]
pub fn hide_console(_command: &mut Command) {}

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
    run_streaming(program, args, timeout, env, |_| {})
}

/// Run a child and deliver each stdout/stderr line as it arrives.
///
/// Used for installs, where a silent ten-minute wait reads as a hang. The
/// callback runs on the calling thread, so emitting a Tauri event from it is
/// safe; the pipe readers only move lines through a channel.
pub fn run_streaming<F>(
    program: &Path,
    args: &[&str],
    timeout: Duration,
    env: &[(String, String)],
    mut on_line: F,
) -> Result<CommandOutput>
where
    F: FnMut(&str),
{
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
    hide_console(&mut command);

    let started = Instant::now();
    let mut child = command
        .spawn()
        .map_err(|error| Error::Io(format!("spawn {}: {error}", program.display())))?;

    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let (sender, receiver) = mpsc::channel::<StreamLine>();
    let out_sender = sender.clone();
    let stdout_reader = thread::spawn(move || stream_lines(stdout, out_sender, false));
    let err_sender = sender.clone();
    let stderr_reader = thread::spawn(move || stream_lines(stderr, err_sender, true));
    drop(sender);

    let mut stdout = String::new();
    let mut stderr = String::new();
    let mut drain = |output: &mut String, line: &str| {
        if !line.is_empty() {
            on_line(line);
        }
        output.push_str(line);
        output.push('\n');
    };

    let mut timed_out = false;
    let code = loop {
        for item in receiver.try_iter() {
            match item {
                StreamLine::Out(line) => drain(&mut stdout, &line),
                StreamLine::Err(line) => drain(&mut stderr, &line),
            }
        }
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
        thread::sleep(Duration::from_millis(25));
    };

    let _ = stdout_reader.join();
    let _ = stderr_reader.join();
    for item in receiver.try_iter() {
        match item {
            StreamLine::Out(line) => drain(&mut stdout, &line),
            StreamLine::Err(line) => drain(&mut stderr, &line),
        }
    }
    Ok(CommandOutput {
        code,
        stdout,
        stderr,
        elapsed_ms: started.elapsed().as_millis() as u64,
        timed_out,
    })
}

enum StreamLine {
    Out(String),
    Err(String),
}

/// Forward one complete line at a time, including an unterminated final line.
fn stream_lines<R: Read>(reader: Option<R>, sender: mpsc::Sender<StreamLine>, is_stderr: bool) {
    let Some(reader) = reader else {
        return;
    };
    let mut reader = BufReader::new(reader);
    let mut buffer = Vec::new();
    loop {
        buffer.clear();
        match reader.read_until(b'\n', &mut buffer) {
            Ok(0) | Err(_) => break,
            Ok(_) => {
                let line = String::from_utf8_lossy(&buffer)
                    .trim_end_matches(['\r', '\n'])
                    .to_string();
                let item = if is_stderr {
                    StreamLine::Err(line)
                } else {
                    StreamLine::Out(line)
                };
                if sender.send(item).is_err() {
                    break;
                }
            }
        }
    }
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
    // Pi's own installer can bundle Node under `pi/node/current` (no hyphen).
    push_env("LOCALAPPDATA", "pi/node/current");
    push_env("LOCALAPPDATA", "Programs");
    push_env("APPDATA", "npm");
    // macOS and Linux have no `LOCALAPPDATA`: the per-user prefix the installer
    // writes to is `~/.anvil/runtimes`, whose `bin/` is where npm drops shims.
    // The rest are the usual per-user npm/pnpm/bun/pi install locations.
    for home in ["USERPROFILE", "HOME"] {
        push_env(home, ".anvil/runtimes/bin");
        push_env(home, ".pi/bin");
        push_env(home, ".pi/node/current");
        push_env(home, ".local/bin");
        push_env(home, ".local/share/pnpm");
        push_env(home, ".bun/bin");
        push_env(home, ".npm-global/bin");
        push_env(home, ".volta/bin");
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
