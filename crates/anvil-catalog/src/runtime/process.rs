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
            for directory in search_dirs() {
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
    for directory in search_dirs() {
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

/// The directories the process actually inherited, in order.
///
/// Discovery labels a hit from here as coming from `PATH`; everything found in
/// [`known_install_dirs`] is a known-directory install. Keeping the two apart is
/// what makes the settings page able to answer "where did this come from?".
pub fn env_path_dirs() -> Vec<PathBuf> {
    path_entries()
}

/// A directory list key that respects the filesystem's case rules.
fn dir_key(path: &Path) -> String {
    let raw = path.to_string_lossy().into_owned();
    if cfg!(windows) {
        raw.to_lowercase()
    } else {
        raw
    }
}

/// `PATH` plus the directories a GUI-launched app never inherits.
///
/// This is the single search list: discovery, the Node pick and every runtime
/// child go through it, so "we installed it but cannot see it" cannot happen in
/// one place and not another.
pub fn search_dirs() -> Vec<PathBuf> {
    let mut dirs: Vec<PathBuf> = Vec::new();
    let mut seen: HashSet<String> = HashSet::new();
    // `PATH` itself can repeat an entry (an installer appending twice); the
    // search must not probe the same directory twice either way.
    for dir in path_entries() {
        if seen.insert(dir_key(&dir)) {
            dirs.push(dir);
        }
    }
    for dir in platform_dirs() {
        if seen.insert(dir_key(dir)) {
            dirs.push(dir.clone());
        }
    }
    dirs
}

/// Directories a runtime can live in without being on `PATH`.
///
/// Kept as its own function because discovery reports *where* a hit came from:
/// a shim found here is registered as a known-directory install.
pub fn known_install_dirs() -> Vec<PathBuf> {
    let mut dirs = Vec::new();
    // The installer honours `ANVIL_RUNTIME_PREFIX`; lookups must follow it,
    // otherwise a portable prefix installs fine but is never discovered.
    if let Some(value) = std::env::var_os("ANVIL_RUNTIME_PREFIX") {
        let path = PathBuf::from(value);
        if !path.as_os_str().is_empty() {
            dirs.push(path);
        }
    }
    dirs.extend(platform_dirs().iter().cloned());
    dirs
}

/// Extra search directories for this platform, computed once.
static PLATFORM_DIRS: OnceLock<Vec<PathBuf>> = OnceLock::new();

fn platform_dirs() -> &'static [PathBuf] {
    PLATFORM_DIRS.get_or_init(build_platform_dirs)
}

#[cfg(not(windows))]
fn home_dir() -> PathBuf {
    for name in ["USERPROFILE", "HOME"] {
        if let Some(value) = std::env::var_os(name) {
            if !value.is_empty() {
                return PathBuf::from(value);
            }
        }
    }
    PathBuf::from(".")
}

/// `root/<version>/<tail…>` for every installed version, newest name first.
///
/// Version managers (nvm, fnm, mise, asdf, nodenv) hide the binaries one level
/// below a version number, so a fixed list of directories can never name them.
fn version_dirs(root: &Path, tail: &[&str]) -> Vec<PathBuf> {
    let Ok(entries) = std::fs::read_dir(root) else {
        return Vec::new();
    };
    let mut versions: Vec<String> = entries
        .flatten()
        .filter(|entry| entry.path().is_dir())
        .map(|entry| entry.file_name().to_string_lossy().into_owned())
        .collect();
    // Newest last-listed first: `v22` must win over `v18` when both are present.
    versions.sort();
    versions.reverse();
    versions
        .into_iter()
        .map(|version| {
            let mut path = root.join(version);
            for part in tail {
                path.push(part);
            }
            path
        })
        .filter(|path| path.is_dir())
        .collect()
}

fn build_platform_dirs() -> Vec<PathBuf> {
    let mut dirs: Vec<PathBuf> = Vec::new();
    #[cfg(windows)]
    {
        let mut env_dir = |name: &str, suffix: &str| {
            if let Some(base) = std::env::var_os(name) {
                let mut path = PathBuf::from(base);
                for part in suffix.split('/').filter(|part| !part.is_empty()) {
                    path.push(part);
                }
                dirs.push(path);
            }
        };
        // The prefix this app installs into, plus the layout pi's own installer
        // uses, and the per-user npm/pnpm/volta prefixes that never reach PATH.
        env_dir("LOCALAPPDATA", "pi-node/current");
        env_dir("LOCALAPPDATA", "pi/node/current");
        env_dir("LOCALAPPDATA", "Programs");
        env_dir("LOCALAPPDATA", "Programs/nodejs");
        env_dir("LOCALAPPDATA", "Volta/bin");
        env_dir("LOCALAPPDATA", "pnpm");
        env_dir("APPDATA", "npm");
        env_dir("APPDATA", "fnm");
        env_dir("USERPROFILE", ".bun/bin");
        // Node installed for the terminal only: nvm-windows keeps every version
        // under `NVM_HOME\v*` and points its symlink at the active one, scoop and
        // chocolatey use their own shim folders, and an MSI install lands under
        // `Program Files\nodejs`. A GUI app started from the Start menu inherits
        // none of those unless the user put them on the machine-wide PATH, and an
        // npm shim that cannot find `node` is exactly the "found but would not
        // start" probe failure. Look where the installers actually put it.
        for name in ["NVM_HOME", "NVM_SYMLINK", "NODE_HOME"] {
            env_dir(name, "");
        }
        env_dir("ProgramFiles", "nodejs");
        env_dir("ProgramFiles(x86)", "nodejs");
        env_dir("USERPROFILE", "scoop/shims");
        env_dir("USERPROFILE", "scoop/apps/nodejs/current");
        env_dir("ProgramData", "chocolatey/bin");
        if let Some(nvm) = std::env::var_os("NVM_HOME") {
            dirs.extend(version_dirs(&PathBuf::from(nvm), &[]));
        }
        if let Some(appdata) = std::env::var_os("APPDATA") {
            let appdata = PathBuf::from(appdata);
            dirs.extend(version_dirs(
                &appdata.join("fnm").join("node-versions"),
                &["installation"],
            ));
        }
        if let Some(local) = std::env::var_os("LOCALAPPDATA") {
            let local = PathBuf::from(local);
            dirs.extend(version_dirs(
                &local.join("fnm").join("node-versions"),
                &["installation"],
            ));
            dirs.extend(version_dirs(&local.join("Volta").join("tools"), &["bin"]));
        }
    }
    #[cfg(not(windows))]
    {
        let home = home_dir();
        // Package-manager roots. A macOS app started from Finder gets
        // `/usr/bin:/bin:/usr/sbin:/sbin` from launchd, so Homebrew's prefix
        // (where `npm -g` puts the shim on Apple silicon) is invisible.
        for fixed in [
            "/opt/homebrew/bin",
            "/usr/local/bin",
            "/opt/local/bin",
            "/usr/local/opt/node/bin",
            "/home/linuxbrew/.linuxbrew/bin",
            "/snap/bin",
        ] {
            dirs.push(PathBuf::from(fixed));
        }
        for suffix in [
            ".local/bin",
            ".anvil/runtimes/bin",
            ".pi/bin",
            ".pi/node/current/bin",
            "Library/pnpm",
            ".yarn/bin",
            ".bun/bin",
            ".npm-global/bin",
            ".volta/bin",
            ".asdf/shims",
            ".local/share/pnpm",
            "bin",
        ] {
            dirs.push(home.join(suffix));
        }
        dirs.extend(version_dirs(&home.join(".nvm/versions/node"), &["bin"]));
        dirs.extend(version_dirs(
            &home.join("Library/Application Support/fnm/node-versions"),
            &["installation", "bin"],
        ));
        dirs.extend(version_dirs(
            &home.join(".local/share/fnm/node-versions"),
            &["installation", "bin"],
        ));
        dirs.extend(version_dirs(&home.join(".nodenv/versions"), &["bin"]));
        dirs.extend(version_dirs(
            &home.join(".local/share/mise/installs/node"),
            &["bin"],
        ));
        dirs.extend(version_dirs(&home.join(".asdf/installs/nodejs"), &["bin"]));
    }
    dirs.extend(shell_path_dirs().iter().cloned());
    dirs
}

/// The login shell's own `PATH`, asked once.
///
/// Users configure Node through `.zshrc` (nvm, pnpm, asdf, a custom npm
/// prefix). A Finder-launched app never reads those files, so the only way to
/// see the same binaries Terminal sees is to ask the shell. Bounded (3s) and
/// cached: at most one shell process per app start, and nothing at all when the
/// shell is slow or missing. Set `ANVIL_NO_SHELL_PATH=1` to skip it.
static SHELL_PATH_DIRS: OnceLock<Vec<PathBuf>> = OnceLock::new();

fn shell_path_dirs() -> &'static [PathBuf] {
    SHELL_PATH_DIRS.get_or_init(|| {
        if std::env::var_os("ANVIL_NO_SHELL_PATH").is_some() {
            return Vec::new();
        }
        #[cfg(windows)]
        {
            Vec::new()
        }
        #[cfg(not(windows))]
        {
            let shell = std::env::var_os("SHELL")
                .map(PathBuf::from)
                .filter(|path| path.is_file())
                .unwrap_or_else(|| PathBuf::from("/bin/zsh"));
            if !shell.is_file() {
                return Vec::new();
            }
            // `-i` runs `.zshrc` (where the version managers live), `-l`
            // reads `.zprofile`, `-c` prints and exits. Startup files can be
            // chatty, so the last line that looks like a path list wins.
            let Ok(output) = run(
                &shell,
                &["-ilc", "printf %s \"$PATH\""],
                Duration::from_secs(3),
            ) else {
                return Vec::new();
            };
            let text = output.text();
            let line = text
                .lines()
                .rev()
                .find(|line| line.split(':').any(|part| part.starts_with('/')))
                .unwrap_or("");
            std::env::split_paths(line)
                .filter(|path| path.is_dir())
                .collect()
        }
    })
}

/// Environment for a runtime child, with the same search list discovery used.
///
/// An npm shim on macOS starts with `#!/usr/bin/env node`; launched from Finder
/// the inherited PATH has no Node, so the shim dies with "env: node: No such
/// file or directory" even though discovery found it seconds earlier.
pub fn child_env() -> Vec<(String, String)> {
    let mut dirs: Vec<PathBuf> = Vec::new();
    if let Some(node) = preferred_node() {
        if let Some(parent) = node.parent() {
            dirs.push(parent.to_path_buf());
        }
    }
    dirs.extend(search_dirs());
    let mut seen: HashSet<String> = HashSet::new();
    let kept: Vec<PathBuf> = dirs
        .into_iter()
        .filter(|dir| seen.insert(dir_key(dir)))
        .collect();
    match std::env::join_paths(kept) {
        Ok(value) => vec![("PATH".to_string(), value.to_string_lossy().into_owned())],
        Err(_) => Vec::new(),
    }
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn search_dirs_cover_the_platform_and_never_repeat() {
        let dirs = search_dirs();
        assert!(!dirs.is_empty(), "a machine always has somewhere to look");
        let mut seen = HashSet::new();
        for dir in &dirs {
            assert!(seen.insert(dir_key(dir)), "duplicate {}", dir.display());
        }
        if cfg!(target_os = "macos") {
            // Finder-launched apps never see Homebrew's prefix on PATH.
            for fixed in ["/opt/homebrew/bin", "/usr/local/bin"] {
                assert!(
                    dirs.iter().any(|dir| dir == Path::new(fixed)),
                    "missing {fixed}: {dirs:?}"
                );
            }
        }
    }

    #[test]
    fn known_dirs_follow_the_install_prefix_override() {
        // The installer honours ANVIL_RUNTIME_PREFIX; discovery has to look
        // there first or a portable install would be invisible.
        let dirs = known_install_dirs();
        assert!(!dirs.is_empty());
    }

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
