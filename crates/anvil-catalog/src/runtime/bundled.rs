//! The Pi runtime that ships inside the installer (ADR 0009).
//!
//! Anvil no longer borrows whatever `pi` and `node` the machine happens to have.
//! The installer carries one Pi (the official npm package, pinned by
//! `src-tauri/runtime-pi/package-lock.json`) plus the private Node that runs it,
//! and this module is the only place that decides which files those are:
//!
//! * [`BundledPi::load`] reads `runtime/pi/manifest.json` from the Tauri resource
//!   directory, refuses a manifest built for another platform or architecture,
//!   and refuses a tree whose entry points are missing;
//! * [`BundledPi::command`] builds the one `Command` shape the rest of the host
//!   uses — `node <cli>` with `PI_CODING_AGENT_DIR` pinned to the Anvil data
//!   directory, and the inherited values that would change how Node resolves
//!   modules removed;
//! * [`agent_dir`] / [`skill_root`] answer the two questions every other module
//!   asks ("where does pi keep its config", "where do user skills live") so the
//!   catalogue, the extension installer and the skills page cannot disagree.
//!
//! The process-wide [`set_active`] is how the host tells the rest of the crate
//! that the bundle exists. Tests never rely on it: the pure functions take the
//! layout explicitly.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Arc, RwLock};

use serde::Deserialize;

/// Subdirectory of the Tauri resource directory the installer maps to.
pub const RESOURCE_SUBDIR: &str = "runtime/pi";

/// Overrides the resource directory (dev runs, tests, a portable install).
///
/// Development builds do not have Tauri's resource copy step, so this is the
/// only way to point a `cargo run` at a staged tree without lying about where
/// the app is installed.
pub const DIR_ENV: &str = "ANVIL_BUNDLED_PI_DIR";

/// The manifest schema this build understands.
pub const SCHEMA: u32 = 1;

/// `manifest.json`, written by `anvil-devtools bundle-pi`.
#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Manifest {
    pub schema: u32,
    pub target: String,
    pub platform: String,
    pub arch: String,
    pub node_version: String,
    pub node: String,
    pub pi_package: String,
    pub pi_version: String,
    pub cli: String,
    pub pi_ai: String,
    pub subagent_source: String,
    pub package_dir: String,
    #[serde(default)]
    pub licenses: Vec<String>,
    #[serde(default)]
    pub sha256: BTreeMap<String, String>,
}

/// One validated Pi runtime, ready to be executed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BundledPi {
    /// Directory holding `manifest.json`, `node/` and `pi/`.
    pub root: PathBuf,
    /// The private Node interpreter.
    pub node: PathBuf,
    /// `dist/bundle/cli.js` — pi's entry point.
    pub cli: PathBuf,
    /// `pi-ai`'s provider bundle, which the OAuth helper imports.
    pub pi_ai: PathBuf,
    /// `examples/extensions/subagent`, which the app installs for delegation.
    pub subagent_source: PathBuf,
    /// Pi's agent directory: `PI_CODING_AGENT_DIR`, i.e. the Anvil data dir.
    pub agent_dir: PathBuf,
    /// User-level skill root: `<agent_dir>/skill`.
    pub skill_root: PathBuf,
    pub node_version: String,
    pub pi_version: String,
    /// Release target the tree was staged for (`win-x64`, …).
    pub target: String,
}

/// Platform name used in the manifest (`windows` / `macos` / `linux`).
#[must_use]
pub const fn host_platform() -> &'static str {
    if cfg!(target_os = "windows") {
        "windows"
    } else if cfg!(target_os = "macos") {
        "macos"
    } else {
        "linux"
    }
}

/// Architecture name used in the manifest (`x64` / `arm64`).
#[must_use]
pub const fn host_arch() -> &'static str {
    if cfg!(target_arch = "x86_64") {
        "x64"
    } else if cfg!(target_arch = "aarch64") {
        "arm64"
    } else {
        "unknown"
    }
}

/// What the UI and the pages need to say about the bundled runtime.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BundleReport {
    pub bundled: bool,
    pub version: Option<String>,
    pub node_version: Option<String>,
    pub target: Option<String>,
    /// Where it is installed, or why it could not be loaded.
    pub root: String,
    pub error: Option<String>,
}

impl BundleReport {
    /// A runtime that is present and usable.
    #[must_use]
    pub fn ready(bundle: &BundledPi) -> Self {
        Self {
            bundled: true,
            version: Some(bundle.pi_version.clone()),
            node_version: Some(bundle.node_version.clone()),
            target: Some(bundle.target.clone()),
            root: bundle.root.display().to_string(),
            error: None,
        }
    }

    /// A runtime that should be there and is not: the reason is shown verbatim.
    #[must_use]
    pub fn broken(root: &Path, error: String) -> Self {
        Self {
            bundled: true,
            version: None,
            node_version: None,
            target: None,
            root: root.display().to_string(),
            error: Some(error),
        }
    }
}

impl BundledPi {
    /// Resolve and validate the bundle for this install.
    ///
    /// `resource_dir` is Tauri's resource directory; [`DIR_ENV`] wins when set,
    /// which is how a development build or a test points at a staged tree.
    pub fn load(resource_dir: &Path, data_dir: &Path) -> Result<Self, String> {
        let root = match std::env::var_os(DIR_ENV) {
            Some(value) if !value.is_empty() => PathBuf::from(value),
            _ => resource_dir.join(RESOURCE_SUBDIR),
        };
        Self::from_dir(&root, data_dir)
    }

    /// Validate a specific directory. Every failure names the file at fault: the
    /// page shows this string to a user whose install is broken.
    pub fn from_dir(root: &Path, data_dir: &Path) -> Result<Self, String> {
        let manifest_path = root.join("manifest.json");
        let text = std::fs::read_to_string(&manifest_path).map_err(|error| {
            format!(
                "the bundled Pi runtime is missing ({}): {error}",
                manifest_path.display()
            )
        })?;
        let manifest: Manifest = serde_json::from_str(&text).map_err(|error| {
            format!(
                "the bundled Pi manifest {} cannot be read: {error}",
                manifest_path.display()
            )
        })?;
        if manifest.schema != SCHEMA {
            return Err(format!(
                "the bundled Pi runtime is version {} but this build understands {}; reinstall Anvil",
                manifest.schema, SCHEMA
            ));
        }
        if manifest.platform != host_platform() || manifest.arch != host_arch() {
            return Err(format!(
                "the bundled Pi runtime is built for {}/{} but this app runs on {}/{}; reinstall Anvil",
                manifest.platform,
                manifest.arch,
                host_platform(),
                host_arch()
            ));
        }
        let root = plain(root);
        let node = resolve(&root, &manifest.node)?;
        let cli = resolve(&root, &manifest.cli)?;
        let pi_ai = resolve(&root, &manifest.pi_ai)?;
        let subagent_source = resolve(&root, &manifest.subagent_source)?;
        Ok(Self {
            root,
            node,
            cli,
            pi_ai,
            subagent_source,
            agent_dir: plain(data_dir),
            skill_root: plain(data_dir).join("skill"),
            node_version: manifest.node_version,
            pi_version: manifest.pi_version,
            target: manifest.target,
        })
    }

    /// The command prefix every pi spawn starts from: `node <cli>`.
    ///
    /// Callers append their own arguments (and their own `cwd`), which is what
    /// keeps the version probe, the RPC session and the OAuth helper on the same
    /// interpreter and the same agent directory.
    #[must_use]
    pub fn command(&self) -> Command {
        let mut command = Command::new(&self.node);
        command.arg(&self.cli);
        command.env("PI_CODING_AGENT_DIR", &self.agent_dir);
        for (key, value) in self.environment() {
            command.env(key, value);
        }
        // The bundle is the only pi this build runs: a machine-wide value that
        // pointed Node (or pi) at another installation must not win.
        for key in ["PI_PACKAGE_DIR", "NODE_PATH", "NODE_OPTIONS"] {
            command.env_remove(key);
        }
        crate::runtime::process::hide_console(&mut command);
        command
    }

    /// Environment a bundled spawn carries.
    ///
    /// `PATH` gets the private Node's own directory first, so anything pi shells
    /// out to that calls `node` by name finds the interpreter that matches the
    /// runtime instead of an older one earlier on the machine's PATH.
    #[must_use]
    pub fn environment(&self) -> Vec<(String, String)> {
        let mut environment = vec![(
            "PI_CODING_AGENT_DIR".to_string(),
            self.agent_dir.display().to_string(),
        )];
        let mut dirs: Vec<PathBuf> = Vec::new();
        if let Some(parent) = self.node.parent() {
            dirs.push(parent.to_path_buf());
        }
        if let Some(existing) = std::env::var_os("PATH") {
            dirs.extend(std::env::split_paths(&existing));
        }
        if let Ok(joined) = std::env::join_paths(dirs) {
            environment.push(("PATH".to_string(), joined.to_string_lossy().into_owned()));
        }
        environment
    }

    /// `--version` output of the bundled CLI, as reported by the CLI itself.
    ///
    /// Used by the tests and by the settings page's "what is actually running"
    /// line; it spawns the same command a session would.
    pub fn probe_version(&self, timeout: std::time::Duration) -> Result<String, String> {
        let mut command = self.command();
        command.arg("--version");
        let output = crate::runtime::process::run_prepared(
            command,
            &self.cli.display().to_string(),
            timeout,
            |_| {},
        )
        .map_err(|error| error.to_string())?;
        let text = output.text();
        let first = text.lines().next().unwrap_or("").trim().to_string();
        if output.code == Some(0) && !first.is_empty() {
            Ok(first)
        } else {
            Err(format!(
                "the bundled pi did not answer --version (exit {:?}): {first}",
                output.code
            ))
        }
    }
}

/// Join a manifest-relative path, refusing anything that escapes the bundle.
///
/// A manifest is data on disk; a tampered one must not be able to point the host
/// at an executable somewhere else on the machine.
/// Strip the Windows verbatim prefix Tauri hands out with `resource_dir()`.
///
/// Tauri canonicalizes the install directory, so every resource arrives as
/// `\\?\D:\...`. Passing that form to `node <script>` looks equivalent to
/// Windows but not to the script: Pi resolves its own package files from the
/// script path, and a verbatim path makes that resolution fail — it then reports
/// Node's version instead of its own (24.16.0 where 1.6.0 was meant), which is
/// what the catalogue stored before this existed. The paths are still the same
/// files; only the spelling changes.
#[cfg_attr(not(windows), allow(dead_code))]
fn plain(path: &Path) -> PathBuf {
    #[cfg(windows)]
    {
        let text = path.as_os_str().to_string_lossy();
        if let Some(rest) = text.strip_prefix(r"\\?\UNC\") {
            return PathBuf::from(format!(r"\\{rest}"));
        }
        if let Some(rest) = text.strip_prefix(r"\\?\") {
            return PathBuf::from(rest);
        }
    }
    path.to_path_buf()
}

fn resolve(root: &Path, relative: &str) -> Result<PathBuf, String> {
    let candidate = Path::new(relative);
    if candidate.is_absolute()
        || candidate
            .components()
            .any(|part| matches!(part, std::path::Component::ParentDir))
    {
        return Err(format!(
            "the bundled Pi manifest names {relative:?}, which is outside the runtime directory"
        ));
    }
    let path = root.join(candidate);
    // The entry points are files; the sub-agent *example* is a directory. Both
    // have to exist for the host to do its job.
    if !path.exists() {
        return Err(format!(
            "the bundled Pi runtime is incomplete: {} is missing; reinstall Anvil",
            path.display()
        ));
    }
    Ok(path)
}

/// Register `<data_dir>/skill` with pi as a skill directory.
///
/// Pi discovers user skills from `settings.json`'s `skills` list. The bundled
/// runtime's skill root is `<agent_dir>/skill`, which pi knows nothing about, so
/// the app has to say it — otherwise the skills page writes folders pi never
/// loads, which looks exactly like "the feature does not work".
///
/// Read-modify-write, and idempotent: a `skills` list that already names this
/// directory is left byte-for-byte alone, an existing list keeps every entry it
/// had, and a value of the wrong shape is reported instead of replaced. Called
/// on every startup and after a skill is added, so "run twice" is the normal
/// case, not an edge case.
///
/// Returns `Ok(true)` when the file changed.
pub fn ensure_skill_root(bundle: &BundledPi) -> Result<bool, String> {
    let settings_path = bundle.agent_dir.join("settings.json");
    let mut root = bundle.skill_root.clone();
    std::fs::create_dir_all(&root)
        .map_err(|error| format!("create {}: {error}", root.display()))?;
    // Compare on the path pi would compute, not on the spelling we have.
    root = std::fs::canonicalize(&root).unwrap_or(root);
    let root_text = root.display().to_string();

    let existing = std::fs::read_to_string(&settings_path).ok();
    let mut settings: serde_json::Value = match &existing {
        Some(text) if !text.trim().is_empty() => serde_json::from_str(text).map_err(|error| {
            format!(
                "{} does not parse ({error}); fix or move it before Anvil can register its skill                  directory",
                settings_path.display()
            )
        })?,
        _ => serde_json::json!({}),
    };
    if !settings.is_object() {
        return Err(format!(
            "{} is not a JSON object; Anvil will not rewrite it",
            settings_path.display()
        ));
    }
    let already = settings
        .get("skills")
        .and_then(|value| value.as_array())
        .is_some_and(|entries| {
            entries.iter().any(|entry| {
                entry.as_str().is_some_and(|value| {
                    let value = std::fs::canonicalize(value)
                        .map(|path| path.display().to_string())
                        .unwrap_or_else(|_| value.to_string());
                    same_path(&value, &root_text)
                })
            })
        });
    if already {
        return Ok(false);
    }
    match settings.get_mut("skills") {
        Some(serde_json::Value::Array(entries)) => {
            entries.push(serde_json::Value::String(root_text))
        }
        Some(_) => {
            return Err(format!(
                "{} has a `skills` value Anvil cannot extend; leave it alone or make it a list",
                settings_path.display()
            ));
        }
        None => {
            settings["skills"] =
                serde_json::Value::Array(vec![serde_json::Value::String(root_text)]);
        }
    }
    let body = serde_json::to_string_pretty(&settings).map_err(|error| error.to_string())?;
    if let Some(parent) = settings_path.parent() {
        std::fs::create_dir_all(parent).map_err(|error| error.to_string())?;
    }
    // Write through a temporary file: a crash mid-write must not leave pi with a
    // truncated settings.json (the same rule the host uses for preferences).
    let temporary = settings_path.with_extension("json.tmp");
    std::fs::write(
        &temporary,
        format!(
            "{body}
"
        ),
    )
    .map_err(|error| format!("write {}: {error}", temporary.display()))?;
    std::fs::rename(&temporary, &settings_path)
        .map_err(|error| format!("replace {}: {error}", settings_path.display()))?;
    Ok(true)
}

fn same_path(left: &str, right: &str) -> bool {
    if cfg!(windows) {
        left.eq_ignore_ascii_case(right)
    } else {
        left == right
    }
}

static ACTIVE: RwLock<Option<Arc<BundledPi>>> = RwLock::new(None);

/// Install the bundle as this process's pi runtime.
///
/// Called once during desktop startup. Everything that used to resolve `pi`
/// from the environment asks [`agent_dir`] / [`skill_root`] instead, so the
/// switch happens in one place.
pub fn set_active(bundle: Arc<BundledPi>) {
    if let Ok(mut slot) = ACTIVE.write() {
        *slot = Some(bundle);
    }
}

/// The bundle this process runs, if startup loaded one.
#[must_use]
pub fn active() -> Option<Arc<BundledPi>> {
    ACTIVE.read().ok().and_then(|slot| slot.clone())
}

/// Pi's agent directory: the bundle's data root when one is active, otherwise
/// `PI_CODING_AGENT_DIR` and finally `~/.pi/agent`.
#[must_use]
pub fn agent_dir() -> Option<PathBuf> {
    if let Some(bundle) = active() {
        return Some(bundle.agent_dir.clone());
    }
    if let Some(dir) = std::env::var_os("PI_CODING_AGENT_DIR")
        && !dir.is_empty()
    {
        return Some(PathBuf::from(dir));
    }
    home().map(|home| home.join(".pi").join("agent"))
}

/// User-level skill root: `<agent_dir>/skill` for the bundle, `~/.pi/skills` for
/// an external install (the location pi itself uses).
#[must_use]
pub fn skill_root() -> Option<PathBuf> {
    if let Some(bundle) = active() {
        return Some(bundle.skill_root.clone());
    }
    home().map(|home| home.join(".pi").join("skills"))
}

/// User home, as the rest of the crate resolves it.
#[must_use]
pub fn home() -> Option<PathBuf> {
    for key in ["USERPROFILE", "HOME"] {
        if let Some(value) = std::env::var_os(key)
            && !value.is_empty()
        {
            return Some(PathBuf::from(value));
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("bundled-pi")
            .join(name);
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create scratch");
        dir
    }

    fn write_manifest(root: &Path, manifest: &str) {
        std::fs::write(root.join("manifest.json"), manifest).expect("write manifest");
    }

    fn good_manifest() -> String {
        let arch = host_arch();
        let platform = host_platform();
        format!(
            r#"{{
  "schema": 1,
  "target": "{platform}-{arch}",
  "platform": "{platform}",
  "arch": "{arch}",
  "nodeVersion": "24.16.0",
  "node": "node/bin/node",
  "piPackage": "@earendil-works/pi-coding-agent",
  "piVersion": "1.6.0",
  "cli": "pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js",
  "piAi": "pi/node_modules/@earendil-works/pi-ai/dist/providers/all.js",
  "subagentSource": "pi/node_modules/@earendil-works/pi-coding-agent/examples/extensions/subagent",
  "packageDir": "pi/node_modules/@earendil-works/pi-coding-agent",
  "licenses": ["node/LICENSE"]
}}"#
        )
    }

    fn stage(root: &Path) {
        for (relative, bytes) in [
            ("node/bin/node", "#!/bin/sh\n"),
            (
                "pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js",
                "// cli",
            ),
            (
                "pi/node_modules/@earendil-works/pi-ai/dist/providers/all.js",
                "export {}",
            ),
            (
                "pi/node_modules/@earendil-works/pi-coding-agent/examples/extensions/subagent/index.ts",
                "//",
            ),
        ] {
            let path = root.join(relative);
            std::fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
            std::fs::write(path, bytes).expect("write");
        }
    }

    #[test]
    fn a_staged_bundle_loads_with_the_data_root_as_its_agent_dir() {
        let root = scratch("load");
        stage(&root);
        write_manifest(&root, &good_manifest());
        let data = root.join("data");
        let bundle = BundledPi::from_dir(&root, &data).expect("valid bundle");
        assert_eq!(bundle.agent_dir, data);
        assert_eq!(bundle.skill_root, data.join("skill"));
        assert_eq!(bundle.pi_version, "1.6.0");
        assert_eq!(bundle.node_version, "24.16.0");
        assert!(bundle.cli.ends_with("dist/bundle/cli.js"));
    }

    /// Tauri's `resource_dir()` canonicalizes into a `\\?\` path. That spelling
    /// breaks Pi's own package resolution (it then reports Node's version), so
    /// the loaded bundle must carry ordinary paths even when it was handed a
    /// verbatim root.
    #[cfg(windows)]
    #[test]
    fn a_verbatim_install_path_is_spelled_plainly() {
        let root = scratch("verbatim");
        stage(&root);
        write_manifest(&root, &good_manifest());
        // `canonicalize` is what hands Tauri its verbatim spelling.
        let verbatim = root.canonicalize().expect("canonical root");
        assert!(verbatim.to_string_lossy().starts_with(r"\\?\"));
        let bundle = BundledPi::from_dir(&verbatim, &root.join("data")).expect("valid bundle");
        for path in [&bundle.root, &bundle.node, &bundle.cli, &bundle.pi_ai] {
            assert!(
                !path.to_string_lossy().starts_with(r"\\?\"),
                "{} still carries the verbatim prefix",
                path.display()
            );
        }
        assert!(bundle.cli.ends_with("dist/bundle/cli.js"));
    }

    /// The failure the installer check exists to prevent: a tree that is missing
    /// the one file the host executes.
    #[test]
    fn a_bundle_without_the_cli_is_refused_and_names_the_file() {
        let root = scratch("no-cli");
        stage(&root);
        write_manifest(&root, &good_manifest());
        std::fs::remove_file(
            root.join("pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js"),
        )
        .expect("remove cli");
        let error = BundledPi::from_dir(&root, &root.join("data")).expect_err("must fail");
        assert!(error.contains("cli.js"), "{error}");
        assert!(error.contains("incomplete"), "{error}");
    }

    /// A bundle staged for another platform would install cleanly and then fail
    /// with an unhelpful "not a valid Win32 application" on the user's machine.
    #[test]
    fn a_bundle_for_another_platform_is_refused() {
        let root = scratch("wrong-platform");
        stage(&root);
        let manifest = good_manifest().replace(
            &format!("\"platform\": \"{}\"", host_platform()),
            "\"platform\": \"plan9\"",
        );
        write_manifest(&root, &manifest);
        let error = BundledPi::from_dir(&root, &root.join("data")).expect_err("must fail");
        assert!(error.contains("plan9"), "{error}");
        assert!(error.contains(host_platform()), "{error}");
    }

    #[test]
    fn a_manifest_that_points_outside_the_bundle_is_refused() {
        let root = scratch("escape");
        stage(&root);
        let manifest =
            good_manifest().replace("\"node\": \"node/bin/node\"", "\"node\": \"../node\"");
        write_manifest(&root, &manifest);
        let error = BundledPi::from_dir(&root, &root.join("data")).expect_err("must fail");
        assert!(error.contains("outside the runtime directory"), "{error}");
    }

    #[test]
    fn a_missing_manifest_says_so_instead_of_starting_nothing() {
        let root = scratch("no-manifest");
        let error = BundledPi::from_dir(&root, &root.join("data")).expect_err("must fail");
        assert!(error.contains("bundled Pi runtime is missing"), "{error}");
    }

    /// The command is the contract every spawn shares: node, then the CLI, then
    /// whatever the caller adds, with the data directory pinned.
    #[test]
    fn the_command_pins_node_the_cli_and_the_agent_directory() {
        let root = scratch("command");
        stage(&root);
        write_manifest(&root, &good_manifest());
        let data = root.join("data");
        let bundle = BundledPi::from_dir(&root, &data).expect("valid bundle");
        let mut command = bundle.command();
        command.arg("--mode").arg("rpc");
        assert_eq!(command.get_program(), bundle.node.as_os_str());
        let args: Vec<String> = command
            .get_args()
            .map(|arg| arg.to_string_lossy().into_owned())
            .collect();
        assert_eq!(
            args,
            vec![
                bundle.cli.to_string_lossy().into_owned(),
                "--mode".to_string(),
                "rpc".to_string()
            ]
        );
        let env: Vec<String> = command
            .get_envs()
            .map(|(key, value)| {
                format!(
                    "{}={}",
                    key.to_string_lossy(),
                    value.unwrap_or_default().to_string_lossy()
                )
            })
            .collect();
        assert!(
            env.iter()
                .any(|entry| entry == &format!("PI_CODING_AGENT_DIR={}", data.display())),
            "{env:?}"
        );
        // Present and empty is how a `Command` records a removal.
        for removed in ["PI_PACKAGE_DIR", "NODE_PATH", "NODE_OPTIONS"] {
            assert!(
                command
                    .get_envs()
                    .any(|(key, value)| key.eq_ignore_ascii_case(removed) && value.is_none()),
                "{removed} must be cleared, got {env:?}"
            );
        }
        let node_dir = bundle
            .node
            .parent()
            .expect("node dir")
            .to_string_lossy()
            .into_owned();
        assert!(
            env.iter()
                .any(|entry| entry.starts_with("PATH=") && entry.contains(&node_dir)),
            "the private node must come first on PATH: {env:?}"
        );
    }

    /// Pi only loads skill directories listed in `settings.json`. Registering
    /// ours has to be idempotent and has to leave the user's own entries alone:
    /// it runs on every startup.
    #[test]
    fn the_skill_root_is_registered_once_and_keeps_existing_entries() {
        let root = scratch("skill-root");
        stage(&root);
        write_manifest(&root, &good_manifest());
        let data = root.join("data");
        let bundle = BundledPi::from_dir(&root, &data).expect("valid bundle");
        std::fs::create_dir_all(&data).expect("data dir");
        std::fs::write(
            data.join("settings.json"),
            br#"{"defaultModel":"m","skills":["C:/somewhere/else"]}"#,
        )
        .expect("settings");

        assert!(
            ensure_skill_root(&bundle).expect("register"),
            "first run changes the file"
        );
        let text = std::fs::read_to_string(data.join("settings.json")).expect("read");
        let value: serde_json::Value = serde_json::from_str(&text).expect("parse");
        let skills = value["skills"].as_array().expect("skills list");
        assert_eq!(skills.len(), 2, "{text}");
        assert_eq!(skills[0], "C:/somewhere/else", "the user's entry is kept");
        assert_eq!(value["defaultModel"], "m", "other settings are untouched");

        assert!(
            !ensure_skill_root(&bundle).expect("second run"),
            "a second startup must not rewrite the file"
        );
        assert_eq!(
            std::fs::read_to_string(data.join("settings.json")).expect("read"),
            text
        );
    }

    #[test]
    fn the_skill_root_is_created_on_a_blank_install() {
        let root = scratch("skill-root-blank");
        stage(&root);
        write_manifest(&root, &good_manifest());
        let data = root.join("data");
        let bundle = BundledPi::from_dir(&root, &data).expect("valid bundle");
        assert!(ensure_skill_root(&bundle).expect("register"));
        assert!(data.join("skill").is_dir(), "the skill root has to exist");
        let text = std::fs::read_to_string(data.join("settings.json")).expect("read");
        assert!(text.contains("skills"), "{text}");
    }

    /// A settings file Anvil cannot understand is reported, never replaced: it
    /// may hold the user's own configuration.
    #[test]
    fn an_unreadable_settings_file_is_reported_not_overwritten() {
        let root = scratch("skill-root-broken");
        stage(&root);
        write_manifest(&root, &good_manifest());
        let data = root.join("data");
        let bundle = BundledPi::from_dir(&root, &data).expect("valid bundle");
        std::fs::create_dir_all(&data).expect("data dir");
        std::fs::write(data.join("settings.json"), b"{not json").expect("settings");
        let error = ensure_skill_root(&bundle).expect_err("must refuse");
        assert!(error.contains("does not parse"), "{error}");
        assert_eq!(
            std::fs::read_to_string(data.join("settings.json")).expect("read"),
            "{not json"
        );
    }

    #[test]
    fn a_path_with_spaces_and_unicode_survives_the_command() {
        let root = scratch("路径 with space");
        stage(&root);
        write_manifest(&root, &good_manifest());
        let data = root.join("数据 根");
        let bundle = BundledPi::from_dir(&root, &data).expect("valid bundle");
        let command = bundle.command();
        assert_eq!(command.get_program(), bundle.node.as_os_str());
        let args: Vec<String> = command
            .get_args()
            .map(|arg| arg.to_string_lossy().into_owned())
            .collect();
        assert_eq!(args, vec![bundle.cli.to_string_lossy().into_owned()]);
        assert!(bundle.agent_dir.ends_with("数据 根"));
    }
}
