//! `verify`: execute one registered case and write its evidence.
//!
//! Ported from `internal/acceptance/runner.go`. A rerun replaces the previous
//! case directory entirely so stale logs can never be mistaken for this run,
//! and every artifact is re-hashed after the case ends (the launched client
//! keeps writing while it starts up).

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::{Command as Process, Stdio};
use std::time::{Duration, Instant};

use anvil_paths::SystemResolver;

use crate::evidence::{
    Artifact, CaseResult, Command, RESULT_SCHEMA_VERSION, STATUS_BLOCKED, STATUS_FAIL, STATUS_PASS,
};
use crate::gate::{EXIT_ASSERTION, EXIT_INCOMPLETE, EXIT_PASS};
use crate::hash::{file_sha256, from_slash, glob_relative, hash_artifact};
use crate::matrix::{contains, is_hex_sha, load_matrix, normalize_sha, validate_identifier};
use crate::osinfo;
use crate::plans::{CasePlan, StepSpec, lookup};
use crate::preflight::{case_dir as make_case_dir, ensure_dir};

/// Default per-step timeout (20 minutes), matching the Go runner.
const DEFAULT_STEP_TIMEOUT: Duration = Duration::from_secs(20 * 60);

/// Selects the case to execute and where to record evidence.
#[derive(Debug, Clone)]
pub struct VerifyOptions {
    pub repo_root: PathBuf,
    pub matrix_path: PathBuf,
    pub evidence_root: String,
    pub case_id: String,
    pub target: String,
    pub git_sha: String,
}

/// What a verification run produced.
#[derive(Debug, Clone)]
pub struct VerifyOutcome {
    pub code: i32,
    pub status: String,
    pub case_dir: Option<PathBuf>,
    pub problems: Vec<String>,
}

impl VerifyOutcome {
    fn fail(message: String) -> Self {
        Self {
            code: EXIT_INCOMPLETE,
            status: STATUS_BLOCKED.to_string(),
            case_dir: None,
            problems: vec![message],
        }
    }
}

/// Execute a registered case and write
/// `<evidence>/<gitSHA>/<target>/<caseId>/result.json`.
#[must_use]
pub fn run_case(opts: &VerifyOptions) -> VerifyOutcome {
    if opts.repo_root.as_os_str().is_empty()
        || opts.case_id.trim().is_empty()
        || opts.target.trim().is_empty()
    {
        return VerifyOutcome::fail("repo root, case id and target are required".to_string());
    }
    let sha = normalize_sha(&opts.git_sha);
    if !is_hex_sha(&sha) {
        return VerifyOutcome::fail(format!(
            "git SHA {:?} is not a full 40-character commit",
            opts.git_sha
        ));
    }
    if let Err(err) = validate_identifier(&opts.target, "target") {
        return VerifyOutcome::fail(err);
    }

    let matrix = match load_matrix(&opts.matrix_path) {
        Ok(matrix) => matrix,
        Err(err) => return VerifyOutcome::fail(err),
    };
    let mut registered = None;
    let mut gate_targets: Vec<String> = Vec::new();
    for gate in &matrix.gates {
        if let Some(case) = gate.cases.iter().find(|case| case.id == opts.case_id) {
            registered = Some(case.clone());
            gate_targets = gate.targets.clone();
        }
    }
    let Some(registered) = registered else {
        return VerifyOutcome::fail(format!(
            "case {:?} is not registered in {}",
            opts.case_id,
            opts.matrix_path.display()
        ));
    };
    if !contains(&gate_targets, &opts.target) {
        return VerifyOutcome::fail(format!(
            "target {:?} is not declared for case {:?}",
            opts.target, opts.case_id
        ));
    }
    let Some(plan) = lookup(&opts.case_id) else {
        return VerifyOutcome::fail(format!(
            "case {:?} has no automated runner; record it through its documented procedure",
            opts.case_id
        ));
    };
    if plan.evidence_kind != registered.evidence_kind {
        return VerifyOutcome::fail(format!(
            "case {:?} runner produces {:?} evidence, matrix declares {:?}",
            opts.case_id, plan.evidence_kind, registered.evidence_kind
        ));
    }

    let resolver = SystemResolver;
    let root = match ensure_dir(&resolver, Path::new(&opts.evidence_root)) {
        Ok(resolution) => resolution,
        Err(err) => return VerifyOutcome::fail(format!("evidence root: {err}")),
    };
    let case_dir = match make_case_dir(&root.resolved, &sha, &opts.target, &opts.case_id) {
        Ok(dir) => dir,
        Err(err) => {
            return VerifyOutcome::fail(format!("case directory escapes the evidence root: {err}"));
        }
    };
    // A rerun replaces the previous attempt entirely so stale logs cannot be
    // mistaken for this run's evidence.
    if let Err(err) = std::fs::remove_dir_all(&case_dir)
        && err.kind() != std::io::ErrorKind::NotFound
    {
        return VerifyOutcome::fail(format!("reset case directory: {err}"));
    }
    if let Err(err) = std::fs::create_dir_all(case_dir.join("logs")) {
        return VerifyOutcome::fail(format!("create case directory: {err}"));
    }
    for rel in &plan.reset {
        let target = opts.repo_root.join(from_slash(rel));
        if let Err(err) = std::fs::remove_dir_all(&target)
            && err.kind() != std::io::ErrorKind::NotFound
        {
            return VerifyOutcome::fail(format!("reset {rel:?}: {err}"));
        }
    }

    let (env, env_settings) = case_env(&opts.repo_root, &root.resolved);
    let env_artifact = match write_json_artifact(&case_dir, "env.json", &env_settings) {
        Ok(artifact) => artifact,
        Err(err) => return VerifyOutcome::fail(format!("record environment: {err}")),
    };

    let runner = AutomationRunner {
        repo: opts.repo_root.clone(),
        case_dir: case_dir.clone(),
        env,
        evidence: root.resolved.clone(),
    };

    let mut commands: Vec<Command> = Vec::new();
    let mut artifacts: Vec<Artifact> = vec![env_artifact];
    let mut limitations: Vec<String> = Vec::new();
    let status = if let Some(kind) = plan.native {
        // Native desktop cases drive the real client; the shared step loop,
        // artifact hashing and exit-code mapping stay in this module.
        let native = crate::native::run(kind, &runner, &sha, &opts.target);
        commands.extend(native.commands);
        artifacts.extend(native.artifacts);
        limitations.extend(native.limitations);
        if native.status.is_empty() {
            STATUS_BLOCKED.to_string()
        } else {
            native.status
        }
    } else {
        match run_automation(
            &runner,
            &plan,
            &mut commands,
            &mut artifacts,
            &mut limitations,
        ) {
            Ok(status) => status,
            Err(err) => return VerifyOutcome::fail(err),
        }
    };

    // The launched client inherits the launch command's stdout and keeps writing
    // while it starts up. Re-hash every artifact after the case has finished so
    // result.json matches the final bytes.
    for artifact in &mut artifacts {
        let path = case_dir.join(from_slash(&artifact.path));
        match file_sha256(&path) {
            Ok(digest) => artifact.sha256 = digest,
            Err(err) => {
                return VerifyOutcome::fail(format!("rehash artifact {:?}: {err}", artifact.path));
            }
        }
    }

    let mut tool_versions = collect_tool_versions(&runner);
    tool_versions.insert("rust".to_string(), format!("rustc {}", rustc_version()));
    let result = CaseResult {
        schema_version: RESULT_SCHEMA_VERSION,
        case_id: opts.case_id.clone(),
        git_sha: sha,
        target: opts.target.clone(),
        status: status.clone(),
        evidence_kind: plan.evidence_kind.clone(),
        os: osinfo::current(),
        tool_versions,
        runtime_version: "tauri 2 (no agent runtime in this case)".to_string(),
        commands,
        artifacts,
        limitations: limitations.clone(),
    };
    let encoded = match serde_json::to_string_pretty(&result) {
        Ok(text) => text,
        Err(err) => return VerifyOutcome::fail(format!("encode result.json: {err}")),
    };
    if let Err(err) = std::fs::write(case_dir.join("result.json"), encoded) {
        return VerifyOutcome::fail(format!("write result.json: {err}"));
    }

    match status.as_str() {
        STATUS_PASS => VerifyOutcome {
            code: EXIT_PASS,
            status,
            case_dir: Some(case_dir),
            problems: Vec::new(),
        },
        STATUS_FAIL => VerifyOutcome {
            code: EXIT_ASSERTION,
            status,
            case_dir: Some(case_dir),
            problems: limitations,
        },
        _ => VerifyOutcome {
            code: EXIT_INCOMPLETE,
            status,
            case_dir: Some(case_dir),
            problems: limitations,
        },
    }
}

fn run_automation(
    runner: &AutomationRunner,
    plan: &CasePlan,
    commands: &mut Vec<Command>,
    artifacts: &mut Vec<Artifact>,
    limitations: &mut Vec<String>,
) -> Result<String, String> {
    let status = STATUS_PASS.to_string();
    for step in &plan.steps {
        let (command, artifact, failure) = runner.run_step(step);
        commands.push(command);
        if let Some(artifact) = artifact {
            artifacts.push(artifact);
        }
        if let Some(failure) = failure {
            limitations.push(failure);
            return Ok(STATUS_FAIL.to_string());
        }
    }
    for pattern in &plan.collect_artifacts {
        let matches = glob_relative(&runner.case_dir, pattern)
            .map_err(|err| format!("collect artifacts {pattern:?}: {err}"))?;
        if matches.is_empty() {
            return Err(format!("collect artifacts {pattern:?}: no files matched"));
        }
        for relative in matches {
            let artifact = hash_artifact(&runner.case_dir, &relative)
                .map_err(|err| format!("collect artifact {relative:?}: {err}"))?;
            artifacts.push(artifact);
        }
    }
    Ok(status)
}

pub(crate) struct AutomationRunner {
    pub(crate) repo: PathBuf,
    pub(crate) case_dir: PathBuf,
    pub(crate) env: Vec<(String, String)>,
    pub(crate) evidence: PathBuf,
}

impl AutomationRunner {
    /// Repository root the case runs against.
    pub(crate) fn repo(&self) -> &Path {
        &self.repo
    }

    /// Case evidence directory (`<root>/<sha>/<target>/<caseId>`).
    pub(crate) fn case_dir(&self) -> &Path {
        &self.case_dir
    }

    /// Read the merged stdout/stderr log of a finished step.
    pub(crate) fn read_log(&self, step_name: &str) -> Vec<u8> {
        std::fs::read(self.case_dir.join("logs").join(format!("{step_name}.log")))
            .unwrap_or_default()
    }

    pub(crate) fn run_step(&self, step: &StepSpec) -> (Command, Option<Artifact>, Option<String>) {
        let command_name = expand(&step.command, self);
        let args: Vec<String> = step.args.iter().map(|arg| expand(arg, self)).collect();
        let work_dir = match &step.dir {
            Some(dir) => self.repo.join(from_slash(dir)),
            None => self.repo.clone(),
        };
        for dir in &step.mkdir {
            let target = expand(dir, self);
            let target = if Path::new(&target).is_absolute() {
                PathBuf::from(target)
            } else {
                self.repo.join(from_slash(&target))
            };
            let _ = std::fs::create_dir_all(target);
        }

        let log_relative = format!("logs/{}.log", step.name);
        let log_path = self.case_dir.join(from_slash(&log_relative));
        let mut record = Command {
            command: format_command(&command_name, &args),
            work_dir: work_dir.display().to_string(),
            exit_code: -1,
        };

        let Ok(log_file) = std::fs::File::create(&log_path) else {
            record.command = "log open failed".to_string();
            return (
                record,
                None,
                Some(format!("step {:?}: create log", step.name)),
            );
        };
        let Ok(stderr_file) = log_file.try_clone() else {
            return (
                record,
                None,
                Some(format!("step {:?}: clone log handle", step.name)),
            );
        };

        let resolved = match self.resolve_command(&command_name, &args) {
            Ok(resolved) => resolved,
            Err(err) => {
                use std::io::Write;
                let mut file = log_file;
                let _ = writeln!(
                    file,
                    "$ {}\n$ workdir: {}\n\nresolve: {err}",
                    record.command, record.work_dir
                );
                let artifact = hash_artifact(&self.case_dir, &log_relative).ok();
                return (
                    record,
                    artifact,
                    Some(format!("step {:?}: {err}", step.name)),
                );
            }
        };
        record.command = format_command(&resolved.0, &resolved.1);

        {
            use std::io::Write;
            let mut file = log_file.try_clone().expect("log handle is open");
            let timeout = step_timeout(step);
            let _ = writeln!(
                file,
                "$ {}\n$ workdir: {}\n$ timeout: {}s\n",
                record.command,
                record.work_dir,
                timeout.as_secs()
            );
        }

        let mut child = match Process::new(&resolved.0)
            .args(&resolved.1)
            .current_dir(&work_dir)
            .envs(self.env.iter().map(|(key, value)| (key, value)))
            .stdin(Stdio::null())
            .stdout(Stdio::from(log_file))
            .stderr(Stdio::from(stderr_file))
            .spawn()
        {
            Ok(child) => child,
            Err(err) => {
                let artifact = hash_artifact(&self.case_dir, &log_relative).ok();
                return (
                    record,
                    artifact,
                    Some(format!("step {:?}: spawn: {err}", step.name)),
                );
            }
        };

        let deadline = Instant::now() + step_timeout(step);
        let mut failure = None;
        let exit_code = loop {
            match child.try_wait() {
                Ok(Some(status)) => break status.code().unwrap_or(-1),
                Ok(None) => {
                    if Instant::now() >= deadline {
                        let _ = child.kill();
                        let _ = child.wait();
                        failure = Some(format!(
                            "step {:?} timed out after {}s",
                            step.name,
                            step_timeout(step).as_secs()
                        ));
                        break -1;
                    }
                    std::thread::sleep(Duration::from_millis(50));
                }
                Err(err) => {
                    failure = Some(format!("step {:?}: wait: {err}", step.name));
                    break -1;
                }
            }
        };
        record.exit_code = i64::from(exit_code);

        if failure.is_none() && exit_code != step.expect_exit {
            failure = Some(format!(
                "step {:?} exited {exit_code}, want {}",
                step.name, step.expect_exit
            ));
        }
        if failure.is_none()
            && let Some(expected) = &step.expect_output_contains
        {
            match std::fs::read_to_string(&log_path) {
                Ok(body) if body.contains(expected) => {}
                Ok(_) => {
                    failure = Some(format!(
                        "step {:?} output does not contain {expected:?}",
                        step.name
                    ));
                }
                Err(err) => {
                    failure = Some(format!("step {:?}: read log: {err}", step.name));
                }
            }
        }
        if let Some(note) = &failure {
            append_note(&log_path, note);
        }
        let artifact = match hash_artifact(&self.case_dir, &log_relative) {
            Ok(artifact) => Some(artifact),
            Err(err) => {
                if failure.is_none() {
                    failure = Some(format!("step {:?}: hash log: {err}", step.name));
                }
                None
            }
        };
        (record, artifact, failure)
    }

    fn resolve_command(
        &self,
        command: &str,
        args: &[String],
    ) -> Result<(String, Vec<String>), String> {
        match command {
            "self" => {
                let exe =
                    std::env::current_exe().map_err(|err| format!("current executable: {err}"))?;
                Ok((exe.display().to_string(), args.to_vec()))
            }
            "node" => {
                let node = node_executable().ok_or_else(|| {
                    "node executable not found (set ANVIL_NODE_DIR or install Node 24.16.0)"
                        .to_string()
                })?;
                Ok((node, args.to_vec()))
            }
            "npm" => {
                let npm = npm_executable().ok_or_else(|| {
                    "npm.cmd not found (set ANVIL_NPM or install Node 24.16.0)".to_string()
                })?;
                let mut full = vec!["/c".to_string(), npm];
                full.extend_from_slice(args);
                Ok(("cmd".to_string(), full))
            }
            "agent-desktop" => {
                let cli = crate::native::agent_desktop_executable().ok_or_else(|| {
                    "agent-desktop CLI not found (set ANVIL_AGENT_DESKTOP or install agent-desktop)"
                        .to_string()
                })?;
                Ok((cli.display().to_string(), args.to_vec()))
            }
            other => {
                let lower = other.to_ascii_lowercase();
                if lower.ends_with(".cmd") || lower.ends_with(".bat") {
                    let mut full = vec!["/c".to_string(), "call".to_string(), other.to_string()];
                    full.extend_from_slice(args);
                    Ok(("cmd".to_string(), full))
                } else {
                    Ok((other.to_string(), args.to_vec()))
                }
            }
        }
    }
}

fn step_timeout(step: &StepSpec) -> Duration {
    if step.timeout.is_zero() {
        DEFAULT_STEP_TIMEOUT
    } else {
        step.timeout
    }
}

fn expand(value: &str, runner: &AutomationRunner) -> String {
    value
        .replace("{repo}", &runner.repo.display().to_string())
        .replace("{evidence}", &runner.evidence.display().to_string())
        .replace("{caseDir}", &runner.case_dir.display().to_string())
}

fn format_command(command: &str, args: &[String]) -> String {
    let mut parts = vec![command.to_string()];
    for arg in args {
        if arg.contains(' ') || arg.contains('\t') {
            parts.push(format!("\"{arg}\""));
        } else {
            parts.push(arg.clone());
        }
    }
    parts.join(" ")
}

fn append_note(log_path: &Path, note: &str) {
    use std::io::Write;
    if let Ok(mut file) = std::fs::OpenOptions::new().append(true).open(log_path) {
        let _ = writeln!(file, "\n[verify] {note}");
    }
}

fn write_json_artifact(
    case_dir: &Path,
    relative: &str,
    value: &BTreeMap<String, String>,
) -> Result<Artifact, String> {
    let text = serde_json::to_string_pretty(value).map_err(|err| err.to_string())?;
    let path = case_dir.join(from_slash(relative));
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|err| err.to_string())?;
    }
    std::fs::write(&path, text).map_err(|err| err.to_string())?;
    hash_artifact(case_dir, relative)
}

/// Environment every case process sees: all writable locations pinned inside
/// the D: worktree, Node 24 first on PATH.
fn case_env(
    repo: &Path,
    evidence_root: &Path,
) -> (Vec<(String, String)>, BTreeMap<String, String>) {
    let tmp = repo.join(".tmp").join("tooling").join("tmp");
    let mut settings = BTreeMap::new();
    settings.insert("TEMP".to_string(), tmp.display().to_string());
    settings.insert("TMP".to_string(), tmp.display().to_string());
    settings.insert("TMPDIR".to_string(), tmp.display().to_string());
    settings.insert(
        "npm_config_cache".to_string(),
        repo.join(".tmp")
            .join("tooling")
            .join("npm-cache")
            .display()
            .to_string(),
    );
    settings.insert(
        "AGENT_DESKTOP_HOME".to_string(),
        repo.join(".tmp")
            .join("tooling")
            .join("agent-desktop-home")
            .display()
            .to_string(),
    );
    settings.insert(
        "ANVIL_EVIDENCE_ROOT".to_string(),
        evidence_root.display().to_string(),
    );

    let mut env: Vec<(String, String)> = std::env::vars().collect();
    for (key, value) in &settings {
        set_env(&mut env, key, value);
    }
    if let Some(dir) = node_directory() {
        let existing = std::env::var("PATH").unwrap_or_default();
        let prefix = dir.display().to_string();
        let path = if existing.is_empty() {
            prefix
        } else {
            format!("{prefix};{existing}")
        };
        set_env(&mut env, "PATH", &path);
    }
    (env, settings)
}

fn set_env(env: &mut Vec<(String, String)>, key: &str, value: &str) {
    if let Some(slot) = env
        .iter_mut()
        .find(|(existing, _)| existing.eq_ignore_ascii_case(key))
    {
        slot.1 = value.to_string();
        return;
    }
    env.push((key.to_string(), value.to_string()));
}

fn node_directory() -> Option<PathBuf> {
    if let Ok(override_dir) = std::env::var("ANVIL_NODE_DIR")
        && !override_dir.trim().is_empty()
    {
        return Some(PathBuf::from(override_dir));
    }
    let pinned = PathBuf::from(r"D:\Program Files\nodejs");
    if pinned.join("node.exe").is_file() {
        return Some(pinned);
    }
    None
}

fn node_executable() -> Option<String> {
    node_directory()
        .map(|dir| dir.join("node.exe").display().to_string())
        .filter(|path| Path::new(path).is_file())
        .or_else(|| Some("node".to_string()))
}

fn npm_executable() -> Option<String> {
    if let Ok(npm) = std::env::var("ANVIL_NPM")
        && !npm.trim().is_empty()
    {
        return Some(npm);
    }
    let dir = node_directory()?;
    let npm = dir.join("npm.cmd");
    npm.is_file().then(|| npm.display().to_string())
}

fn collect_tool_versions(runner: &AutomationRunner) -> BTreeMap<String, String> {
    let mut versions = BTreeMap::new();
    for (key, command, args) in [
        ("git", "git", vec!["--version".to_string()]),
        ("cargo", "cargo", vec!["--version".to_string()]),
    ] {
        if let Some(value) = capture_version(runner, command, &args) {
            versions.insert(key.to_string(), value);
        }
    }
    if let Some(node) = node_executable()
        && let Some(value) = capture_version(runner, &node, &["-v".to_string()])
    {
        versions.insert("node".to_string(), format!("{value} ({node})"));
    }
    if let Some(npm) = npm_executable()
        && let Some(value) = capture_version(
            runner,
            "cmd",
            &["/c".to_string(), npm.clone(), "-v".to_string()],
        )
    {
        versions.insert("npm".to_string(), format!("{value} ({npm})"));
    }
    versions
}

fn capture_version(runner: &AutomationRunner, command: &str, args: &[String]) -> Option<String> {
    let output = Process::new(command)
        .args(args)
        .current_dir(&runner.repo)
        .envs(runner.env.iter().map(|(key, value)| (key, value)))
        .stdin(Stdio::null())
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&output.stdout);
    text.lines().next().map(|line| line.trim().to_string())
}

fn rustc_version() -> String {
    Process::new("rustc")
        .arg("--version")
        .output()
        .ok()
        .map(|output| String::from_utf8_lossy(&output.stdout).trim().to_string())
        .and_then(|text| text.split_whitespace().nth(1).map(str::to_string))
        .unwrap_or_else(|| "unknown".to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn commands_with_spaces_are_quoted() {
        let text = format_command("cmd", &["/c".to_string(), "C:\\a b\\npm.cmd".to_string()]);
        assert_eq!(text, "cmd /c \"C:\\a b\\npm.cmd\"");
    }
}
