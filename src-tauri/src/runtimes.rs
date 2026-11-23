//! One-click runtime installation.
//!
//! Every runtime this build drives is an npm package, and the catalog adapters
//! look for its `bin` shim in one shared prefix. Installing there means a
//! freshly installed runtime is discovered by the very next scan without
//! editing `PATH` or restarting the app.
//!
//! The prefix is resolved by [`anvil_catalog::runtime::adapters::common`], the
//! same code the adapters scan with, so the two can never disagree:
//!
//! * Windows — `%LOCALAPPDATA%\pi-node\current` (npm puts `<name>.cmd` there)
//! * macOS / Linux — `~/.anvil/runtimes` (npm puts `bin/<name>` there)
//! * anything — `ANVIL_RUNTIME_PREFIX` overrides both
//!
//! Nothing here is Windows-specific: the same npm invocation runs on every
//! platform, and only the shim location differs. Nothing here guesses either —
//! the package names are the ones npm publishes for these CLIs, the report
//! carries npm's own output, and success requires a clean exit code *and* the
//! shim existing on disk.

use std::path::PathBuf;
use std::time::Duration;

use anvil_catalog::runtime::adapters::common;
use anvil_catalog::runtime::process;
use serde::Serialize;

/// Where the shared runtime prefix can be overridden from.
pub const PREFIX_ENV: &str = "ANVIL_RUNTIME_PREFIX";

/// Generous: a cold npm download of a large CLI can take minutes.
const INSTALL_TIMEOUT: Duration = Duration::from_secs(600);

/// A registry metadata read is quick; a hung network must not hang the page.
const UPDATE_TIMEOUT: Duration = Duration::from_secs(45);

/// One installable runtime: catalog id, npm package, shim name.
pub struct Spec {
    pub id: &'static str,
    pub package: &'static str,
    pub binary: &'static str,
}

/// The npm catalogue, not the list of runtimes this build drives.
///
/// Whether an id is *wired* is decided by the catalog's adapter registry
/// (`anvil_catalog::runtime::adapters::default_registry`), and `runtime_install`
/// refuses anything that is not registered there. Right now that is pi only, so
/// the other three entries are here for the day their adapter is switched back
/// on — the package names and shims stay correct in the meantime.
pub const SPECS: [Spec; 4] = [
    Spec {
        id: "pi",
        package: "@earendil-works/pi-coding-agent",
        binary: "pi",
    },
    Spec {
        id: "codex",
        package: "@openai/codex",
        binary: "codex",
    },
    Spec {
        id: "opencode",
        package: "opencode-ai",
        binary: "opencode",
    },
    Spec {
        id: "claude",
        package: "@anthropic-ai/claude-code",
        binary: "claude",
    },
];

/// What one install attempt did, in terms the UI can show verbatim.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct InstallReport {
    pub runtime_id: String,
    pub ok: bool,
    pub package: String,
    pub prefix: String,
    pub installed_path: Option<String>,
    /// The version the shim actually printed after installation, if it runs.
    pub version: Option<String>,
    /// npm's combined output, trimmed for display.
    pub output: String,
    pub elapsed_ms: u64,
}

pub fn spec(runtime_id: &str) -> Option<&'static Spec> {
    SPECS.iter().find(|spec| spec.id == runtime_id)
}

/// The prefix to install into — the same one the adapters scan.
pub fn prefix() -> Result<PathBuf, String> {
    common::shared_prefix().ok_or_else(|| {
        format!("找不到安装目录：请设置 {PREFIX_ENV}，或确保 HOME / LOCALAPPDATA 可用")
    })
}

/// Install `runtime_id` into the shared prefix, reporting npm's output live.
pub fn install<F>(runtime_id: &str, mut on_line: F) -> Result<InstallReport, String>
where
    F: FnMut(&str),
{
    let spec = spec(runtime_id).ok_or_else(|| {
        format!("不支持的运行时：{runtime_id}（可安装：pi / codex / opencode / claude）")
    })?;
    let prefix = prefix()?;
    std::fs::create_dir_all(&prefix)
        .map_err(|error| format!("创建安装目录 {} 失败：{error}", prefix.display()))?;
    let npm = common::shared_npm(&prefix)
        .ok_or_else(|| "未找到 npm：请先安装 Node.js（Anvil 用它安装运行时 CLI）".to_string())?;

    let prefix_arg = prefix.to_string_lossy().into_owned();
    // `--global` is what makes npm put the `bin` shim at the prefix root on
    // Windows and in `<prefix>/bin` on macOS/Linux — the two places
    // `common::shared_shim` and the adapters look. It is a per-user global
    // prefix, so no administrator rights and no change to the system PATH.
    let args = [
        "install",
        "--global",
        "--prefix",
        prefix_arg.as_str(),
        spec.package,
        "--no-fund",
        "--no-audit",
    ];
    on_line(&format!(
        "$ {} install --global --prefix \"{}\" {}",
        npm.display(),
        prefix.display(),
        spec.package
    ));
    // npm is a Node script with `#!/usr/bin/env node`; on macOS a Finder-launched
    // app has no node on PATH, so the install would die with "env: node: No such
    // file or directory" before npm printed anything.
    let env = process::child_env();
    let output = process::run_streaming(&npm, &args, INSTALL_TIMEOUT, &env, |line| on_line(line))
        .map_err(|error| format!("运行 npm 失败：{error}"))?;

    // A clean exit code is not enough: the shim is what discovery and the
    // session adapters actually execute, so it must exist *and* answer
    // `--version` with a version we can parse.
    let installed = common::shared_shim(&prefix, spec.binary);
    let installed = installed.is_file().then(|| installed);
    let mut notes: Vec<String> = Vec::new();
    if output.success() {
        if let Some(shim) = &installed {
            match pin_shim_to_node(&prefix, spec, &npm, shim) {
                Ok(Some(message)) => notes.push(message),
                Ok(None) => {}
                Err(message) => notes.push(format!("启动器绑定失败：{message}")),
            }
        }
    }
    let probe = installed
        .as_ref()
        .and_then(|path| common::version_probe(path, Duration::from_secs(15)).ok());
    let version = probe.as_ref().and_then(|(parsed, first_line, _)| {
        parsed
            .map(|value| value.to_string())
            .or_else(|| (!first_line.is_empty()).then(|| first_line.clone()))
    });
    let ok = output.success()
        && installed.is_some()
        && probe
            .as_ref()
            .is_some_and(|(parsed, _, _)| parsed.is_some());
    if !ok {
        if !output.success() {
            notes.push(format!("npm 退出码 {:?}", output.code));
        } else if installed.is_none() {
            notes.push(format!(
                "没有生成启动器 {}",
                common::shared_shim(&prefix, spec.binary).display()
            ));
        } else {
            notes.push("启动器已写入，但 `--version` 没有返回可识别的版本；通常是 PATH 中的 Node 版本过低。".to_string());
        }
    }
    for note in &notes {
        on_line(note);
    }
    let mut transcript = combined_output(&output);
    for note in &notes {
        transcript.push('\n');
        transcript.push_str(note);
    }
    Ok(InstallReport {
        runtime_id: runtime_id.to_string(),
        ok,
        package: spec.package.to_string(),
        prefix: prefix.display().to_string(),
        installed_path: installed.map(|path| path.display().to_string()),
        version,
        output: output_tail(&transcript, 4000),
        elapsed_ms: output.elapsed_ms,
    })
}

/// npm splits progress between the two pipes; keep both in the final report.
fn combined_output(output: &process::CommandOutput) -> String {
    let mut text = String::new();
    for stream in [&output.stdout, &output.stderr] {
        let trimmed = stream.trim();
        if trimmed.is_empty() {
            continue;
        }
        if !text.is_empty() {
            text.push('\n');
        }
        text.push_str(trimmed);
    }
    text
}

/// The Node version a package declares it needs, e.g. `>=22.19.0` → 22.19.0.
fn required_node(
    package_dir: &std::path::Path,
) -> Option<anvil_catalog::runtime::version::Version> {
    use anvil_catalog::runtime::version::Version;

    let path = package_dir.join("package.json");
    let text = std::fs::read_to_string(path).ok()?;
    let value: serde_json::Value = serde_json::from_str(&text).ok()?;
    let engines = value.get("engines")?.get("node")?.as_str()?;
    Version::find(engines)
}

/// Pick the newest Node we can actually start, preferring npm's own sibling.
///
/// Windows boxes often carry an old Node from another tool ahead of the real
/// one on PATH. npm itself uses the Node beside `npm.cmd`, but the `.cmd` shim
/// it writes resolves `node` from PATH again — binding the shim to this path
/// makes install, discovery, and sessions agree on one interpreter.
fn node_for_npm(
    npm: &std::path::Path,
    required: Option<anvil_catalog::runtime::version::Version>,
) -> Result<(std::path::PathBuf, anvil_catalog::runtime::version::Version), String> {
    use anvil_catalog::runtime::version::Version;

    let mut candidates = Vec::new();
    if let Some(directory) = npm.parent() {
        for name in ["node.exe", "node"] {
            candidates.push(directory.join(name));
        }
    }
    for directory in std::env::var_os("PATH")
        .map(|value| std::env::split_paths(&value).collect::<Vec<_>>())
        .unwrap_or_default()
    {
        for name in ["node.exe", "node"] {
            candidates.push(directory.join(name));
        }
    }
    if let Some(found) = process::which("node") {
        candidates.push(found);
    }

    let mut seen = std::collections::HashSet::new();
    let mut best: Option<(std::path::PathBuf, Version)> = None;
    let mut newest_seen: Option<Version> = None;
    for candidate in candidates {
        if !candidate.is_file() {
            continue;
        }
        let key = candidate.to_string_lossy().to_lowercase();
        if !seen.insert(key) {
            continue;
        }
        let Ok(output) = process::run(&candidate, &["--version"], Duration::from_secs(3)) else {
            continue;
        };
        if !output.success() {
            continue;
        }
        let Some(version) = Version::find(&output.text()) else {
            continue;
        };
        if newest_seen.is_none_or(|current| version > current) {
            newest_seen = Some(version);
        }
        if required.is_some_and(|minimum| version < minimum) {
            continue;
        }
        if best.as_ref().is_none_or(|(_, current)| version > *current) {
            best = Some((candidate, version));
        }
    }
    best.ok_or_else(|| match (required, newest_seen) {
        (Some(minimum), Some(found)) => {
            format!("需要 Node >= {minimum}，PATH 上找到的最高版本是 {found}；请升级 Node 后重试")
        }
        (Some(minimum), None) => format!("找不到可用的 Node（需要 >= {minimum}）"),
        _ => "找不到可执行的 Node.js".to_string(),
    })
}

/// Rewrite npm's PATH-dependent shim so it launches the pinned Node directly.
///
/// Only the generated `<name>.cmd` / `bin/<name>` is touched; the package and
/// its own files are left alone, so a later npm update simply replaces it.
fn pin_shim_to_node(
    prefix: &std::path::Path,
    spec: &Spec,
    npm: &std::path::Path,
    shim: &std::path::Path,
) -> Result<Option<String>, String> {
    let package_dir = prefix.join("node_modules").join(spec.package);
    let manifest = std::fs::read_to_string(package_dir.join("package.json"))
        .map_err(|error| format!("读取 {} 的 package.json 失败：{error}", spec.package))?;
    let manifest: serde_json::Value = serde_json::from_str(&manifest)
        .map_err(|error| format!("解析 {} 的 package.json 失败：{error}", spec.package))?;
    let relative = manifest
        .get("bin")
        .and_then(|bin| {
            bin.as_str().map(str::to_string).or_else(|| {
                bin.get(spec.binary)
                    .and_then(|value| value.as_str())
                    .map(str::to_string)
            })
        })
        .ok_or_else(|| format!("{} 没有声明 {} 入口", spec.package, spec.binary))?;
    let cli = package_dir.join(relative);
    if !cli.is_file() {
        return Err(format!("找不到 {} 的入口 {}", spec.package, cli.display()));
    }

    let (node, version) = node_for_npm(npm, required_node(&package_dir))?;
    #[cfg(windows)]
    {
        let node_literal = node.to_string_lossy().replace('%', "%%");
        let cli_literal = cli.to_string_lossy().replace('%', "%%");
        let body = format!(
            "@ECHO off\r\n\"{node_literal}\" \"{cli_literal}\" %*\r\nEXIT /b %ERRORLEVEL%\r\n"
        );
        std::fs::write(shim, body)
            .map_err(|error| format!("写入 {} 失败：{error}", shim.display()))?;
    }
    #[cfg(not(windows))]
    {
        let escape = |path: &std::path::Path| path.to_string_lossy().replace('"', "\\\"");
        let body = format!(
            "#!/bin/sh\nexec \"{}\" \"{}\" \"$@\"\n",
            escape(&node),
            escape(&cli)
        );
        std::fs::write(shim, body)
            .map_err(|error| format!("写入 {} 失败：{error}", shim.display()))?;
        use std::os::unix::fs::PermissionsExt;
        let mut permissions = std::fs::metadata(shim)
            .map_err(|error| error.to_string())?
            .permissions();
        permissions.set_mode(0o755);
        std::fs::set_permissions(shim, permissions).map_err(|error| error.to_string())?;
    }
    Ok(Some(format!(
        "已把 {} 启动器绑定到 Node {}（{}），避免 PATH 里的旧版 Node 干扰",
        spec.binary,
        version,
        node.display()
    )))
}

/// What npm says about a runtime's package, compared with the installed CLI.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateCheck {
    pub runtime_id: String,
    pub package: String,
    /// The version the installed CLI reports (`--version`), if known.
    pub installed: Option<String>,
    /// The version npm publishes for the package right now.
    pub latest: Option<String>,
    pub update_available: bool,
    pub checked_at: String,
    /// Why the lookup failed, verbatim, so the button can explain itself.
    pub error: Option<String>,
}

/// Ask the registry for the latest published version and compare.
///
/// This is a network read (`npm view`), so it is explicit: opening settings
/// must not call out to npm on its own. `installed` comes from the catalog's
/// last scan, which read it from the CLI itself.
pub fn check_update(runtime_id: &str, installed: Option<&str>) -> Result<UpdateCheck, String> {
    use anvil_catalog::runtime::version::Version;

    let spec = spec(runtime_id).ok_or_else(|| format!("不支持的运行时：{runtime_id}"))?;
    let checked_at = anvil_catalog::clock::now_rfc3339();
    let mut check = UpdateCheck {
        runtime_id: runtime_id.to_string(),
        package: spec.package.to_string(),
        installed: installed.map(str::to_string),
        latest: None,
        update_available: false,
        checked_at,
        error: None,
    };
    let prefix = prefix()?;
    let npm =
        common::shared_npm(&prefix).ok_or_else(|| "未找到 npm：请先安装 Node.js".to_string())?;
    let output = process::run_with_env(
        &npm,
        &[
            "view",
            spec.package,
            "version",
            "--json",
            "--loglevel=error",
        ],
        UPDATE_TIMEOUT,
        // Same reason as the install: npm itself is `#!/usr/bin/env node`.
        &process::child_env(),
    )
    .map_err(|error| format!("查询 npm 失败：{error}"))?;
    if !output.success() {
        check.error = Some(output_tail(&output.text(), 400));
        return Ok(check);
    }
    let latest = output.text().trim().trim_matches('"').trim().to_string();
    if latest.is_empty() {
        check.error = Some("npm 没有返回版本号".to_string());
        return Ok(check);
    }
    check.latest = Some(latest.clone());
    check.update_available = match (installed.and_then(Version::find), Version::find(&latest)) {
        (Some(current), Some(remote)) => remote > current,
        // No installed version means the CLI is missing; installing is the fix,
        // and the install button already covers that case.
        _ => false,
    };
    Ok(check)
}

fn output_tail(text: &str, limit: usize) -> String {
    let trimmed = text.trim();
    if trimmed.chars().count() <= limit {
        return trimmed.to_string();
    }
    let tail: String = trimmed
        .chars()
        .rev()
        .take(limit)
        .collect::<Vec<_>>()
        .into_iter()
        .rev()
        .collect();
    format!("…{tail}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_supported_runtime_has_a_package_and_a_binary() {
        for entry in &SPECS {
            assert!(!entry.package.is_empty(), "{} has no package", entry.id);
            assert!(!entry.binary.is_empty(), "{} has no shim", entry.id);
            assert!(spec(entry.id).is_some());
        }
        assert!(spec("does-not-exist").is_none());
    }

    #[test]
    fn the_shim_lives_where_npm_writes_it() {
        let prefix = PathBuf::from("/tmp/anvil-prefix");
        let shim = common::shared_shim(&prefix, "pi");
        if cfg!(windows) {
            assert!(shim.ends_with("pi.cmd"));
        } else {
            assert!(shim.ends_with("bin/pi"));
        }
    }

    #[test]
    fn output_is_trimmed_from_the_end() {
        let text = "a".repeat(80);
        let tail = output_tail(&text, 10);
        assert!(tail.ends_with(&"a".repeat(10)));
        assert!(tail.starts_with('…'));
    }
}
