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

/// Install `runtime_id` into the shared prefix.
pub fn install(runtime_id: &str) -> Result<InstallReport, String> {
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
    // `common::shared_shim` and the adapters look. A local install would drop
    // the shim in `node_modules/.bin`, where nothing scans for it.
    let args = [
        "install",
        "--global",
        "--prefix",
        prefix_arg.as_str(),
        spec.package,
        "--no-fund",
        "--no-audit",
        "--loglevel=error",
    ];
    let output = process::run(&npm, &args, INSTALL_TIMEOUT)
        .map_err(|error| format!("运行 npm 失败：{error}"))?;

    // A clean exit code is not enough: the shim is what discovery and the
    // session adapters actually execute, so its presence is the real signal.
    let installed = common::shared_shim(&prefix, spec.binary);
    let installed = installed.is_file().then(|| installed);
    let ok = output.success() && installed.is_some();
    Ok(InstallReport {
        runtime_id: runtime_id.to_string(),
        ok,
        package: spec.package.to_string(),
        prefix: prefix.display().to_string(),
        installed_path: installed.map(|path| path.display().to_string()),
        output: output_tail(&output.text(), 1200),
        elapsed_ms: output.elapsed_ms,
    })
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
        for spec in &SPECS {
            assert!(!spec.package.is_empty(), "{} has no package", spec.id);
            assert!(!spec.binary.is_empty(), "{} has no shim", spec.id);
            assert!(spec(spec.id).is_some());
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
