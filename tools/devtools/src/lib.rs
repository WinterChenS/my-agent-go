//! `anvil-devtools`: the single entry point for verification and gates.
//!
//! Rust replacement for `cmd/devtools` (R0-RS03). Exit codes follow
//! `docs/adr/0001-devtools-exit-code-fidelity.md`: 0 pass, 1 assertion failure
//! (valid evidence reports FAIL), 2 incomplete input, missing evidence,
//! configuration error or BLOCKED/NOT_RUN.
//!
//! The binary is built by `scripts/devtools.ps1`, which passes the exit code
//! through unchanged. Never wrap it in something that normalizes exit codes.

pub mod evidence;
pub mod gate;
pub mod hash;
mod key;
pub mod matrix;
pub(crate) mod native;
pub mod osinfo;
pub mod plans;
pub mod preflight;
mod registry;
pub mod runner;

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use anvil_paths::SystemResolver;
use serde_json::json;

use crate::gate::{GateOptions, evaluate_gate};
use crate::preflight::ensure_dir;
use crate::runner::{VerifyOptions, run_case};

const USAGE: &str = "usage:
  anvil-devtools verify --case=<id> --target=<target> [--evidence=<abs path>] [--matrix=<path>] [--sha=<sha>]
  anvil-devtools gate --id=<gate> --target=<target> --evidence=<abs path> [--matrix=<path>] [--sha=<sha>]
  anvil-devtools preflight --path=<path> [--create]
  anvil-devtools exists --path=<file>
  anvil-devtools registry --get | --set=<0|1>
  anvil-devtools key --title=<window title> --combo=alt+left
";

/// Parse `args` (without the program name) and return the process exit code.
#[must_use]
pub fn run(args: &[String]) -> i32 {
    let Some(command) = args.first() else {
        eprint!("{USAGE}");
        return gate::EXIT_INCOMPLETE;
    };
    match command.as_str() {
        "gate" => run_gate(&args[1..]),
        "verify" => run_verify(&args[1..]),
        "preflight" => run_preflight(&args[1..]),
        "exists" => run_exists(&args[1..]),
        "registry" => run_registry(&args[1..]),
        "key" => run_key(&args[1..]),
        "version" => {
            println!("anvil-devtools (Anvil rebuild, Rust)");
            gate::EXIT_PASS
        }
        "-h" | "--help" | "help" => {
            print!("{USAGE}");
            gate::EXIT_PASS
        }
        other => {
            eprintln!("unknown command {other:?}\n\n{USAGE}");
            gate::EXIT_INCOMPLETE
        }
    }
}

fn run_gate(args: &[String]) -> i32 {
    let parsed = match Parsed::parse(args, &["id", "target", "evidence", "matrix", "sha"]) {
        Ok(parsed) => parsed,
        Err(err) => {
            eprintln!("gate: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let repo = match repo_root() {
        Ok(repo) => repo,
        Err(err) => {
            eprintln!("gate: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let matrix_path = parsed
        .value("matrix")
        .map(PathBuf::from)
        .unwrap_or_else(|| repo.join("tests").join("acceptance").join("rebuild.json"));
    let commit = match commit_for(&repo, parsed.value("sha")) {
        Ok(commit) => commit,
        Err(err) => {
            eprintln!("gate: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let gate_id = parsed.value("id").unwrap_or_default();
    let target = parsed.value("target").unwrap_or_default();
    let outcome = evaluate_gate(
        &GateOptions {
            matrix_path,
            evidence_root: parsed.value("evidence").unwrap_or_default(),
            gate_id: gate_id.clone(),
            target: target.clone(),
            git_sha: commit.clone(),
        },
        &SystemResolver,
    );
    let report = json!({
        "gate": gate_id,
        "target": target,
        "gitSHA": commit,
        "exitCode": outcome.code,
        "passed": outcome.passed,
        "statuses": outcome.statuses,
        "problems": outcome.problems,
    });
    write_json(&report);
    if outcome.code != gate::EXIT_PASS {
        eprintln!(
            "gate {gate_id}/{target}: exit {} ({} problem(s))",
            outcome.code,
            outcome.problems.len()
        );
    }
    outcome.code
}

fn run_verify(args: &[String]) -> i32 {
    let parsed = match Parsed::parse(args, &["case", "target", "evidence", "matrix", "sha"]) {
        Ok(parsed) => parsed,
        Err(err) => {
            eprintln!("verify: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let repo = match repo_root() {
        Ok(repo) => repo,
        Err(err) => {
            eprintln!("verify: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let matrix_path = parsed
        .value("matrix")
        .map(PathBuf::from)
        .unwrap_or_else(|| repo.join("tests").join("acceptance").join("rebuild.json"));
    let evidence_root = match parsed.value("evidence") {
        Some(value) if !value.trim().is_empty() => value,
        _ => std::env::var("ANVIL_EVIDENCE_ROOT").unwrap_or_default(),
    };
    if evidence_root.trim().is_empty() {
        eprintln!("verify: evidence root is required (pass --evidence or set ANVIL_EVIDENCE_ROOT)");
        return gate::EXIT_INCOMPLETE;
    }
    let case_id = parsed.value("case").unwrap_or_default();
    let target = parsed.value("target").unwrap_or_default();
    let commit = if let Some(sha) = parsed.value("sha") {
        if sha.trim().is_empty() {
            match commit_for(&repo, None) {
                Ok(commit) => commit,
                Err(err) => {
                    eprintln!("verify: {err}");
                    return gate::EXIT_INCOMPLETE;
                }
            }
        } else {
            sha
        }
    } else {
        let commit = match commit_for(&repo, None) {
            Ok(commit) => commit,
            Err(err) => {
                eprintln!("verify: {err}");
                return gate::EXIT_INCOMPLETE;
            }
        };
        // Evidence must describe the committed tree, not a work in progress.
        match git_dirty(&repo) {
            Ok(false) => commit,
            Ok(true) => {
                eprintln!(
                    "verify: working tree has tracked changes; commit first so evidence binds to the git SHA"
                );
                return gate::EXIT_INCOMPLETE;
            }
            Err(err) => {
                eprintln!("verify: {err}");
                return gate::EXIT_INCOMPLETE;
            }
        }
    };

    let outcome = run_case(&VerifyOptions {
        repo_root: repo,
        matrix_path,
        evidence_root,
        case_id: case_id.clone(),
        target: target.clone(),
        git_sha: commit.clone(),
    });
    let report = json!({
        "caseId": case_id,
        "target": target,
        "gitSHA": commit,
        "exitCode": outcome.code,
        "status": outcome.status,
        "caseDir": outcome.case_dir.map(|dir| dir.display().to_string()).unwrap_or_default(),
        "problems": outcome.problems,
    });
    write_json(&report);
    if outcome.code != gate::EXIT_PASS {
        eprintln!(
            "verify {case_id}/{target}: exit {} status {}",
            outcome.code, outcome.status
        );
    }
    outcome.code
}

/// `exists --path=<file>`: assert a file the case produced is really there.
///
/// Used by the native cases to prove where persisted state landed instead of
/// trusting the UI alone.
fn run_exists(args: &[String]) -> i32 {
    let parsed = match Parsed::parse(args, &["path"]) {
        Ok(parsed) => parsed,
        Err(err) => {
            eprintln!("exists: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let Some(path) = parsed.value("path") else {
        eprintln!("exists: --path is required");
        return gate::EXIT_INCOMPLETE;
    };
    let path = PathBuf::from(path);
    if path.is_file() {
        println!("exists: {}", path.display());
        gate::EXIT_PASS
    } else if path.is_dir() {
        eprintln!("exists: {} is a directory, not a file", path.display());
        gate::EXIT_ASSERTION
    } else {
        eprintln!("exists: {} is missing", path.display());
        gate::EXIT_ASSERTION
    }
}

fn run_key(args: &[String]) -> i32 {
    let parsed = match Parsed::parse(args, &["title", "combo"]) {
        Ok(parsed) => parsed,
        Err(err) => {
            eprintln!("key: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let title = parsed.value("title").unwrap_or_else(|| "Anvil".to_string());
    let combo = parsed.value("combo").unwrap_or_default();
    if combo.trim().is_empty() {
        eprintln!("key: --combo is required (alt+left or escape)");
        return gate::EXIT_INCOMPLETE;
    }
    key::run(&title, &combo)
}

/// Read or write `AppsUseLightTheme` in the current user's personalize key.
///
/// The native theme case flips the real Windows value; going through this
/// command keeps the flip a recorded step instead of an invisible FFI call.
fn run_registry(args: &[String]) -> i32 {
    let parsed = match Parsed::parse_with_flags(args, &["set"], &["get"]) {
        Ok(parsed) => parsed,
        Err(err) => {
            eprintln!("registry: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    if parsed.flag("get") {
        return match registry::read_apps_use_light() {
            Ok(value) => {
                write_json(&json!({ "ok": true, "value": value }));
                gate::EXIT_PASS
            }
            Err(err) => {
                write_json(&json!({ "ok": false, "error": err }));
                eprintln!("registry: {err}");
                gate::EXIT_INCOMPLETE
            }
        };
    }
    let Some(value) = parsed.value("set") else {
        eprintln!("registry: pass --get or --set=<0|1>");
        return gate::EXIT_INCOMPLETE;
    };
    let parsed_value = match value.trim().parse::<i64>() {
        Ok(value) => value,
        Err(err) => {
            eprintln!("registry: --set={value:?} is not a number: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    match registry::write_apps_use_light(parsed_value) {
        Ok(()) => {
            write_json(&json!({ "ok": true, "value": parsed_value }));
            gate::EXIT_PASS
        }
        Err(err) => {
            write_json(&json!({ "ok": false, "error": err }));
            eprintln!("registry: {err}");
            gate::EXIT_INCOMPLETE
        }
    }
}

fn run_preflight(args: &[String]) -> i32 {
    let parsed = match Parsed::parse_with_flags(args, &["path"], &["create"]) {
        Ok(parsed) => parsed,
        Err(err) => {
            eprintln!("preflight: {err}");
            return gate::EXIT_INCOMPLETE;
        }
    };
    let Some(path) = parsed.value("path") else {
        eprintln!("preflight: --path is required");
        return gate::EXIT_INCOMPLETE;
    };
    if path.trim().is_empty() {
        eprintln!("preflight: --path is required");
        return gate::EXIT_INCOMPLETE;
    }
    let create = parsed.flag("create");
    let resolver = SystemResolver;
    let path_buf = PathBuf::from(&path);
    let result = if create {
        ensure_dir(&resolver, &path_buf)
    } else {
        preflight::check_path(&resolver, &path_buf)
    };
    match result {
        Ok(resolution) => {
            let report = json!({
                "path": path,
                "existing": resolution.existing_ancestor.display().to_string(),
                "resolved": resolution.resolved.display().to_string(),
                "volume": resolution.volume,
                "created": create,
                "ok": true,
            });
            write_json(&report);
            gate::EXIT_PASS
        }
        Err(err) => {
            let report = json!({
                "path": path,
                "existing": "",
                "resolved": "",
                "volume": "",
                "created": create,
                "ok": false,
                "error": err,
            });
            write_json(&report);
            // `err` already carries the `preflight:` prefix from the contract.
            eprintln!("{err}");
            gate::EXIT_INCOMPLETE
        }
    }
}

fn write_json(value: &serde_json::Value) {
    println!(
        "{}",
        serde_json::to_string_pretty(value).unwrap_or_else(|_| "{}".to_string())
    );
}

/// Flags passed as `--name=value` or `--name value`; booleans as `--name`.
struct Parsed {
    values: BTreeMap<String, String>,
    flags: Vec<String>,
}

impl Parsed {
    fn parse(args: &[String], names: &[&str]) -> Result<Self, String> {
        Self::parse_with_flags(args, names, &[])
    }

    fn parse_with_flags(args: &[String], names: &[&str], flags: &[&str]) -> Result<Self, String> {
        let mut values = BTreeMap::new();
        let mut present: Vec<String> = Vec::new();
        let mut index = 0;
        while index < args.len() {
            let arg = &args[index];
            let Some(body) = arg.strip_prefix("--") else {
                return Err(format!("unexpected argument {arg:?}"));
            };
            let (name, inline) = match body.split_once('=') {
                Some((name, value)) => (name, Some(value.to_string())),
                None => (body, None),
            };
            if flags.contains(&name) {
                if inline.is_some() {
                    return Err(format!("--{name} does not take a value"));
                }
                present.push(name.to_string());
                index += 1;
                continue;
            }
            if !names.contains(&name) {
                return Err(format!("unknown flag --{name}"));
            }
            let value = match inline {
                Some(value) => value,
                None => {
                    index += 1;
                    args.get(index)
                        .cloned()
                        .ok_or_else(|| format!("--{name} requires a value"))?
                }
            };
            values.insert(name.to_string(), value);
            index += 1;
        }
        Ok(Self {
            values,
            flags: present,
        })
    }

    fn value(&self, name: &str) -> Option<String> {
        self.values.get(name).cloned()
    }

    fn flag(&self, name: &str) -> bool {
        self.flags.iter().any(|flag| flag == name)
    }
}

fn repo_root() -> Result<PathBuf, String> {
    let mut dir = std::env::current_dir().map_err(|err| format!("current directory: {err}"))?;
    loop {
        if dir.join("Cargo.toml").is_file() && dir.join(".git").exists() {
            return Ok(dir);
        }
        if !dir.pop() {
            return Err(
                "could not find the repository root (Cargo.toml + .git) above the working directory"
                    .to_string(),
            );
        }
    }
}

fn commit_for(repo: &Path, sha: Option<String>) -> Result<String, String> {
    if let Some(sha) = sha
        && !sha.trim().is_empty()
    {
        return Ok(sha.trim().to_string());
    }
    let output = std::process::Command::new("git")
        .arg("-C")
        .arg(repo)
        .args(["rev-parse", "HEAD"])
        .output()
        .map_err(|err| format!("git rev-parse HEAD: {err}"))?;
    if !output.status.success() {
        return Err("git rev-parse HEAD failed".to_string());
    }
    let sha = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if sha.len() != 40 {
        return Err(format!("git rev-parse HEAD returned {sha:?}"));
    }
    Ok(sha)
}

fn git_dirty(repo: &Path) -> Result<bool, String> {
    let output = std::process::Command::new("git")
        .arg("-C")
        .arg(repo)
        .args(["status", "--porcelain", "--untracked-files=no"])
        .output()
        .map_err(|err| format!("git status: {err}"))?;
    if !output.status.success() {
        return Err("git status failed".to_string());
    }
    Ok(!String::from_utf8_lossy(&output.stdout).trim().is_empty())
}
