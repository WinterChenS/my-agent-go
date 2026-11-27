//! Runtime extensions the chat path depends on.
//!
//! `pi` ships no sub-agents in its core binary. Delegation comes from the
//! official `subagent` extension, which lives inside the installed package as
//! `examples/extensions/subagent` and is *not* active until its files are copied
//! into the agent directory (`~/.pi/agent`, or `PI_CODING_AGENT_DIR`):
//!
//! ```text
//! <agent-dir>/extensions/subagent/index.ts   entry point (directory form)
//! <agent-dir>/extensions/subagent/agents.ts  agent discovery, imported by index
//! <agent-dir>/agents/*.md                    the agents the tool can delegate to
//! <agent-dir>/prompts/*.md                   optional workflow presets
//! ```
//!
//! Measured in this build: with the extension absent, `--tools subagent` names a
//! tool that does not exist, pi silently ignores the unknown name, and the model
//! answers that it has no subagent tool. That failure is invisible from the
//! outside, which is why the UI asks this module instead of guessing from the
//! turn's output.
//!
//! Installing is a copy, not a package manager: the example is source, the agent
//! directory is where pi looks, and `pi install` expects a package manifest the
//! example does not have. Existing agent and prompt files are never overwritten —
//! they are user-editable prompts, and an upgrade that replaced somebody's
//! `reviewer.md` would be a data loss, not an update.

use std::path::{Path, PathBuf};

use serde::Serialize;

use crate::error::{Error, Result};

/// Package-relative location of the extension inside the installed pi.
const PACKAGE_RELATIVE: &str = "node_modules/@earendil-works/pi-coding-agent";
/// Directory name of the extension inside `examples/extensions`.
const EXTENSION_RELATIVE: &str = "examples/extensions/subagent";
const EXTENSION_NAME: &str = "subagent";
/// Entry files, in the order they are copied.
const ENTRY_FILES: &[&str] = &["index.ts", "agents.ts", "README.md"];

/// What the UI needs to decide between "nothing to do" and "offer install".
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PiSubagentStatus {
    /// The extension entry point exists in the agent directory.
    pub installed: bool,
    /// `<agent-dir>`; shown so the user can open it.
    pub agent_dir: String,
    /// `<agent-dir>/extensions/subagent`; exists only after an install.
    pub extension_dir: String,
    /// The installed package's copy of the example, when it was found.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,
    /// Agent definitions the extension will offer, from `<agent-dir>/agents`.
    pub agents: usize,
    /// Workflow presets, from `<agent-dir>/prompts`.
    pub prompts: usize,
    /// Install is possible: the source exists and the extension is not there.
    pub can_install: bool,
    /// The installed copy differs from the one inside the current pi package.
    ///
    /// pi is upgraded as a package, and its `examples/extensions/subagent` moves
    /// with it — the file copied earlier does not. `false` when either side is
    /// missing, because "cannot compare" is not "up to date".
    pub update_available: bool,
    /// Content digest of the installed entry files, when they exist.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub installed_digest: Option<String>,
    /// Content digest of the package's own entry files, when they exist.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_digest: Option<String>,
    /// Why install is not possible (or why nothing was found), for the tooltip.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// What one install actually wrote.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PiSubagentInstall {
    /// Files created or replaced (the extension's own sources).
    pub written: usize,
    /// Agent/prompt files left alone because they already existed.
    pub skipped: usize,
    /// Status read back after the copy, so the caller never has to ask twice.
    pub status: PiSubagentStatus,
}

/// `<agent-dir>`: the bundled runtime's data root wins, then
/// `PI_CODING_AGENT_DIR`, then `~/.pi/agent`.
///
/// The order is ADR 0009's: this build always runs the Pi it ships, so an
/// extension or an agent file has to land in *that* runtime's directory. The
/// variable and the home fallback stay for development hosts and tests.
#[must_use]
pub fn agent_dir() -> Option<PathBuf> {
    if let Some(dir) = anvil_catalog::runtime::bundled::agent_dir() {
        return Some(dir);
    }
    home_dir().map(|home| home.join(".pi").join("agent"))
}

fn home_dir() -> Option<PathBuf> {
    std::env::var_os("USERPROFILE")
        .filter(|value| !value.is_empty())
        .or_else(|| std::env::var_os("HOME").filter(|value| !value.is_empty()))
        .map(PathBuf::from)
}

fn extension_dir(agent_dir: &Path) -> PathBuf {
    agent_dir.join("extensions").join(EXTENSION_NAME)
}

fn has_entry(dir: &Path) -> bool {
    dir.join("index.ts").is_file() || dir.join("index.js").is_file()
}

/// Files whose bytes decide whether a copy is current.
///
/// `README.md` is excluded on purpose: the installed copy carries the note this
/// app appends, so its digest can never match the package's and every check
/// would report an update that is not there.
const CODE_FILES: &[&str] = &["index.ts", "agents.ts"];

/// FNV-1a over the entry files, names included.
///
/// Not a security hash: it only has to notice that two small files differ. A
/// missing file on one side changes the digest, so "half copied" also reads as
/// stale instead of matching by accident.
fn digest(dir: &Path) -> Option<String> {
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    let mut any = false;
    for name in CODE_FILES {
        let Ok(bytes) = std::fs::read(dir.join(name)) else {
            continue;
        };
        any = true;
        for byte in name.bytes().chain(bytes.iter().copied()) {
            hash ^= u64::from(byte);
            hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
        }
        // A separator that cannot appear in a file name keeps `a`+`bc` from
        // hashing like `ab`+`c`.
        hash ^= 0xff;
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    any.then(|| format!("{hash:016x}"))
}

fn count_markdown(dir: &Path) -> usize {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return 0;
    };
    entries
        .filter_map(std::result::Result::ok)
        .filter(|entry| {
            entry
                .path()
                .extension()
                .is_some_and(|extension| extension.eq_ignore_ascii_case("md"))
        })
        .count()
}

/// A path as the user should read it: canonicalising adds the Windows verbatim
/// prefix (`\\?\C:\...`), which is noise in a notice that says where files go.
fn display_path(path: &Path) -> String {
    let text = path.display().to_string();
    text.strip_prefix(r"\\?\").unwrap_or(&text).to_string()
}

/// Where the installed pi keeps the `subagent` example.
///
/// The executable is an npm shim, so three layouts have to be tried: the
/// package next to the shim (`%LOCALAPPDATA%\pi-node\current`), under `lib/`
/// (Unix prefixes), and — when the shim resolves to `dist/cli.js` — the package
/// root being one of the executable's own ancestors.
///
/// `ANVIL_PI_SUBAGENT_SOURCE` overrides the search, for tests and for a pi
/// installed somewhere this heuristic cannot see.
#[must_use]
pub fn source_for(executable: &Path) -> Option<PathBuf> {
    if let Some(custom) = std::env::var_os("ANVIL_PI_SUBAGENT_SOURCE")
        && !custom.is_empty()
    {
        let path = PathBuf::from(custom);
        if has_entry(&path) {
            return Some(path);
        }
    }
    let resolved = std::fs::canonicalize(executable).unwrap_or_else(|_| executable.to_path_buf());
    let mut roots: Vec<PathBuf> = Vec::new();
    for ancestor in resolved.ancestors().take(4) {
        roots.push(ancestor.to_path_buf());
    }
    // A global install puts the shim in `<prefix>/bin` and the package in
    // `<prefix>/lib/node_modules` (macOS, Linux) or `<prefix>/node_modules`
    // (Windows). Following only the executable's own path misses that when the
    // shim is a wrapper rather than a symlink, so the prefixes discovery uses
    // are searched too.
    for directory in anvil_catalog::runtime::process::search_dirs() {
        if let Some(parent) = directory.parent() {
            roots.push(parent.join("lib").join("node_modules"));
            roots.push(parent.join("node_modules"));
        }
        roots.push(directory.join("node_modules"));
    }
    for root in roots {
        for base in [root.clone(), root.join("lib")] {
            // The package root itself (executable inside `dist/`).
            let direct = base.join(EXTENSION_RELATIVE);
            if has_entry(&direct) {
                return Some(direct);
            }
            let packaged = base.join(PACKAGE_RELATIVE).join(EXTENSION_RELATIVE);
            if has_entry(&packaged) {
                return Some(packaged);
            }
        }
    }
    None
}

/// Status against an explicit agent directory. Pure: no env, no PATH.
/// One extension pi is set up to load.
///
/// The chat path only needs `subagent`, but a reader who installed three
/// plugins needs to see all three: "not detected" must never be the answer to
/// "is my plugin installed?" when the file is right there.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PiExtension {
    /// File stem, directory name, or package name.
    pub name: String,
    /// `file`, `dir`, `package`, `settings`, `builtin`, or `declared`.
    pub kind: String,
    /// `global`, `project`, or `settings`.
    pub scope: String,
    /// The file or directory pi loads; the spec itself for declared packages.
    pub path: String,
    /// Entry point inside a directory package, when one was resolved.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub entry: Option<String>,
    /// pi will load it: the entry point resolves.
    pub active: bool,
    /// Why an inactive entry is inactive, for the row's title.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

const SCRIPT_EXTENSIONS: &[&str] = &["ts", "js", "mjs", "cjs", "tsx", "jsx"];

fn script_name(path: &Path) -> Option<String> {
    let extension = path.extension()?.to_str()?.to_ascii_lowercase();
    if !SCRIPT_EXTENSIONS.contains(&extension.as_str()) {
        return None;
    }
    path.file_stem()
        .map(|name| name.to_string_lossy().into_owned())
}

/// The entry file of a directory extension, following pi's own rules:
/// `index.<script>` first, then a `pi.extensions` manifest.
fn directory_entry(dir: &Path) -> (Option<PathBuf>, Option<String>) {
    for extension in SCRIPT_EXTENSIONS {
        let candidate = dir.join(format!("index.{extension}"));
        if candidate.is_file() {
            return (Some(candidate), None);
        }
    }
    let manifest = dir.join("package.json");
    let Some(value) = std::fs::read_to_string(&manifest)
        .ok()
        .and_then(|text| serde_json::from_str::<serde_json::Value>(&text).ok())
    else {
        return (
            None,
            Some("no index.ts/index.js and no package.json manifest".to_string()),
        );
    };
    let declared = value
        .get("pi")
        .and_then(|pi| pi.get("extensions"))
        .and_then(|list| list.as_array())
        .or_else(|| value.get("pi.extensions").and_then(|list| list.as_array()));
    let Some(declared) = declared else {
        return (
            None,
            Some("package.json has no pi.extensions entry".to_string()),
        );
    };
    for item in declared.iter().filter_map(|item| item.as_str()) {
        if item.contains('*') {
            // A glob cannot be resolved without pi's matcher; the manifest is
            // still a declaration that the extension exists.
            return (Some(dir.join(item)), None);
        }
        let candidate = dir.join(item);
        if candidate.is_file() {
            return (Some(candidate), None);
        }
    }
    (
        None,
        Some("pi.extensions points at a file that is not there".to_string()),
    )
}

/// The readable name of a `pi install` spec: `npm:@me/pi-pack@1.0.0` →
/// `@me/pi-pack`, `git:github.com/user/repo@v1` → `github.com/user/repo`.
fn declared_name(spec: &str) -> String {
    let body = spec.split_once(':').map(|(_, rest)| rest).unwrap_or(spec);
    match body.rfind('@') {
        Some(index) if index > 0 => body[..index].to_string(),
        _ => body.to_string(),
    }
}

fn extension_row(dir: &Path, name: String, path: &Path) -> PiExtension {
    let (entry, detail) = directory_entry(dir);
    let active = entry.as_ref().is_some_and(|path| path.is_file());
    PiExtension {
        name,
        kind: if dir.join("package.json").is_file() {
            "package".to_string()
        } else {
            "dir".to_string()
        },
        scope: "global".to_string(),
        path: display_path(path),
        entry: entry.map(|path| display_path(&path)),
        active,
        detail,
    }
}

/// Every extension pi would load for this user.
///
/// Follows the discovery rules from pi's own docs: one level of the agent
/// directory's `extensions/`, a directory's `index.*` or its `pi.extensions`
/// manifest, plus anything `settings.json` adds (`extensions`, `builtin:`, and
/// `packages`). Project-local `.pi/extensions` belongs to a directory, not to
/// the user, and is deliberately not listed here.
#[must_use]
pub fn pi_extensions() -> Vec<PiExtension> {
    match agent_dir() {
        Some(dir) => extensions_at(&dir),
        None => Vec::new(),
    }
}

/// [`pi_extensions`] against an explicit agent directory. Pure: no env, no PATH.
#[must_use]
pub fn extensions_at(agent_dir: &Path) -> Vec<PiExtension> {
    let mut rows: Vec<PiExtension> = Vec::new();
    let root = agent_dir.join("extensions");
    if let Ok(entries) = std::fs::read_dir(&root) {
        let mut paths: Vec<PathBuf> = entries
            .filter_map(std::result::Result::ok)
            .map(|entry| entry.path())
            .filter(|path| {
                !path
                    .file_name()
                    .is_some_and(|name| name.to_string_lossy().starts_with('.'))
            })
            .collect();
        paths.sort();
        for path in paths {
            if path.is_dir() {
                let name = path
                    .file_name()
                    .map(|name| name.to_string_lossy().into_owned())
                    .unwrap_or_default();
                rows.push(extension_row(&path, name, &path));
            } else if let Some(name) = script_name(&path) {
                rows.push(PiExtension {
                    name,
                    kind: "file".to_string(),
                    scope: "global".to_string(),
                    path: display_path(&path),
                    entry: None,
                    active: true,
                    detail: None,
                });
            }
        }
    }

    // `settings.json` can point outside the agent directory, and a package
    // installed by `pi install` is recorded there rather than copied here.
    if let Some(settings) = std::fs::read_to_string(agent_dir.join("settings.json"))
        .ok()
        .and_then(|text| serde_json::from_str::<serde_json::Value>(&text).ok())
    {
        if let Some(list) = settings.get("extensions").and_then(|list| list.as_array()) {
            for item in list.iter().filter_map(|item| item.as_str()) {
                if let Some(builtin) = item.strip_prefix("builtin:") {
                    rows.push(PiExtension {
                        name: builtin.to_string(),
                        kind: "builtin".to_string(),
                        scope: "settings".to_string(),
                        path: item.to_string(),
                        entry: None,
                        active: true,
                        detail: None,
                    });
                    continue;
                }
                let path = PathBuf::from(item);
                let active = path.exists();
                rows.push(PiExtension {
                    name: path
                        .file_stem()
                        .or_else(|| path.file_name())
                        .map(|name| name.to_string_lossy().into_owned())
                        .unwrap_or_else(|| item.to_string()),
                    kind: "settings".to_string(),
                    scope: "settings".to_string(),
                    path: item.to_string(),
                    entry: None,
                    active,
                    detail: (!active)
                        .then(|| "settings.json lists a path that is not there".into()),
                });
            }
        }
        if let Some(list) = settings.get("packages").and_then(|list| list.as_array()) {
            for item in list.iter().filter_map(|item| item.as_str()) {
                rows.push(PiExtension {
                    name: declared_name(item),
                    kind: "declared".to_string(),
                    scope: "settings".to_string(),
                    path: item.to_string(),
                    entry: None,
                    active: true,
                    detail: Some(
                        "installed by pi install; its files live in pi's own store".into(),
                    ),
                });
            }
        }
    }
    rows
}

/// Whether an installed extension covers `needle` (case-insensitive).
///
/// Matches the row name and the entry path, so `extensions/subagent/index.ts`,
/// `subagent.ts`, and `@scope/pi-subagent` all count as the same plugin.
#[must_use]
pub fn has_extension(rows: &[PiExtension], needle: &str) -> bool {
    let needle = needle.to_ascii_lowercase();
    rows.iter().any(|row| {
        row.active
            && (row.name.to_ascii_lowercase().contains(&needle)
                || row.path.to_ascii_lowercase().contains(&needle)
                || row
                    .entry
                    .as_deref()
                    .is_some_and(|entry| entry.to_ascii_lowercase().contains(&needle)))
    })
}

fn status_at(agent_dir: &Path, source: Option<&Path>, detected: bool) -> PiSubagentStatus {
    let dir = extension_dir(agent_dir);
    let installed = has_entry(&dir) || detected;
    let detail = if installed {
        None
    } else if source.is_some() {
        Some("the subagent extension is not installed in the pi agent directory".to_string())
    } else {
        Some(
            "the installed pi package does not ship examples/extensions/subagent; update pi \
             or set ANVIL_PI_SUBAGENT_SOURCE to a copy of the extension"
                .to_string(),
        )
    };
    let installed_digest = if installed { digest(&dir) } else { None };
    let source_digest = source.and_then(digest);
    let update_available = match (&installed_digest, &source_digest) {
        (Some(installed), Some(packaged)) => installed != packaged,
        _ => false,
    };
    PiSubagentStatus {
        installed,
        agent_dir: display_path(agent_dir),
        extension_dir: display_path(&dir),
        source: source.map(display_path),
        agents: count_markdown(&agent_dir.join("agents")),
        prompts: count_markdown(&agent_dir.join("prompts")),
        can_install: !installed && source.is_some(),
        update_available,
        installed_digest,
        source_digest,
        detail,
    }
}

/// Current status, resolving pi on `PATH` for the package location.
///
/// Never fails: a missing pi is a status, not an error — the chat page uses it
/// to decide whether to draw the install banner at all.
#[must_use]
pub fn pi_subagent_status() -> PiSubagentStatus {
    let source = crate::adapters::find_on_path("pi").and_then(|exe| source_for(&exe));
    match agent_dir() {
        Some(dir) => {
            // The copy this installer writes is `extensions/subagent/`, but a
            // plugin installed as `subagent.ts`, as an npm package, or listed in
            // settings.json is just as installed — and the scanner knows all of
            // those shapes.
            let detected = has_extension(&pi_extensions(), "subagent");
            status_at(&dir, source.as_deref(), detected)
        }
        None => PiSubagentStatus {
            installed: false,
            agent_dir: String::new(),
            extension_dir: String::new(),
            source: source.as_ref().map(|path| display_path(path)),
            agents: 0,
            prompts: 0,
            can_install: false,
            update_available: false,
            installed_digest: None,
            source_digest: source.as_deref().and_then(digest),
            detail: Some(
                "cannot resolve pi's agent directory: neither PI_CODING_AGENT_DIR nor HOME is set"
                    .to_string(),
            ),
        },
    }
}

/// Copy the extension and its resources into `agent_dir`.
fn install_into(agent_dir: &Path, source: &Path) -> Result<PiSubagentInstall> {
    if !has_entry(source) {
        return Err(Error::NotFound(format!(
            "{} has no subagent entry point (index.ts or index.js)",
            source.display()
        )));
    }
    let target = extension_dir(agent_dir);
    std::fs::create_dir_all(&target)?;
    let mut written = 0usize;
    for name in ENTRY_FILES {
        let from = source.join(name);
        if !from.is_file() {
            continue;
        }
        // The extension's own sources are replaced: they are shipped code, and
        // a half-old copy is worse than a reinstall.
        std::fs::copy(&from, target.join(name))?;
        written += 1;
    }
    if !has_entry(&target) {
        return Err(Error::Failed(format!(
            "copied the subagent extension into {} but its entry point is missing",
            target.display()
        )));
    }
    // The copied README is the extension's own; the note records the one change
    // this installer makes, so the divergence is documented where a user looks.
    let readme = target.join("README.md");
    if readme.is_file() {
        let mut text = std::fs::read_to_string(&readme).unwrap_or_default();
        // An update replaces the README from the package, which has no note, but
        // a re-run on an installed copy must not stack the note twice.
        if !text.contains("## Installed by Anvil") {
            text.push_str(INSTALL_NOTE);
            std::fs::write(&readme, text)?;
        }
    }

    // Resources are the user's once they exist: same name, different words.
    let mut skipped = 0usize;
    for (from_dir, to_dir, localize) in [
        (source.join("agents"), agent_dir.join("agents"), true),
        (source.join("prompts"), agent_dir.join("prompts"), false),
    ] {
        let Ok(entries) = std::fs::read_dir(&from_dir) else {
            continue;
        };
        std::fs::create_dir_all(&to_dir)?;
        let mut files: Vec<PathBuf> = entries
            .filter_map(std::result::Result::ok)
            .map(|entry| entry.path())
            .filter(|path| path.is_file())
            .collect();
        files.sort();
        for from in files {
            let Some(name) = from.file_name() else {
                continue;
            };
            let to = to_dir.join(name);
            if to.exists() {
                skipped += 1;
                continue;
            }
            if localize {
                let text = std::fs::read_to_string(&from)?;
                std::fs::write(&to, unpin_model(&text))?;
            } else {
                std::fs::copy(&from, &to)?;
            }
            written += 1;
        }
    }

    Ok(PiSubagentInstall {
        written,
        skipped,
        status: status_at(agent_dir, Some(source), false),
    })
}

/// Appended to the copied README so the installed copy says what changed.
const INSTALL_NOTE: &str = "\n## Installed by Anvil\n\n\
Anvil copies this example into the pi agent directory and removes the `model:` \
pin from the sample agents, so they inherit the model of the agent that \
delegates to them instead of the author's Anthropic ids (which are ambiguous or \
unauthenticated on most machines). The original pin is kept as a comment; \
uncomment it to pin a model again. Existing agent and prompt files are never \
overwritten.\n";

/// Comment out a top-level `model:` line inside the YAML front matter.
///
/// The sample agents pin `claude-haiku-4-5` / `claude-sonnet-4-5`. On a machine
/// whose providers are not Anthropic the first delegation fails with
/// "Model ... is ambiguous across providers", and the user sees a broken feature
/// they just installed. Without the key the extension runs the child on the
/// parent's model (`agent.model ?? dispatchDefaults.model`), which is what an
/// agent the user never configured should do.
fn unpin_model(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    let mut in_front = false;
    let mut first = true;
    for line in text.split_inclusive('\n') {
        let trimmed = line.trim_end_matches(['\r', '\n']);
        if first {
            first = false;
            in_front = trimmed.trim() == "---";
            out.push_str(line);
            continue;
        }
        if in_front && trimmed.trim() == "---" {
            in_front = false;
            out.push_str(line);
            continue;
        }
        // Only the front matter's own keys count: `model:` inside the prompt is
        // prose, and an indented one belongs to a nested mapping.
        let is_key = in_front && trimmed.starts_with("model:");
        if is_key {
            let value = trimmed.trim_start_matches("model:").trim().to_string();
            out.push_str(&format!(
                "# model: {value} (Anvil: agents inherit the parent model; uncomment to pin)\n"
            ));
            continue;
        }
        out.push_str(line);
    }
    out
}

/// Install the `subagent` extension for the current user.
///
/// Fails with a message that names the missing piece: no pi on `PATH`, no agent
/// directory, or a pi package that does not ship the example. The UI shows the
/// message as-is; there is no silent no-op.
pub fn install_pi_subagent() -> Result<PiSubagentInstall> {
    let agent_dir = agent_dir().ok_or_else(|| {
        Error::NotFound(
            "cannot resolve pi's agent directory: neither PI_CODING_AGENT_DIR nor HOME is set"
                .to_string(),
        )
    })?;
    let executable = crate::adapters::find_on_path("pi").ok_or_else(|| {
        Error::NotFound("pi executable not found on PATH (install pi or fix PATH)".to_string())
    })?;
    let source = source_for(&executable).ok_or_else(|| {
        Error::NotFound(
            "the installed pi package does not ship examples/extensions/subagent; update pi"
                .to_string(),
        )
    })?;
    install_into(&agent_dir, &source)
}

/// Replace the installed copy with the one the current pi package ships.
///
/// Only the extension's own sources move: an agent or prompt file the user has
/// edited is theirs, and rewriting it would be a data loss dressed up as an
/// update. `update_available` in the status is what tells the UI to offer this.
pub fn update_pi_subagent() -> Result<PiSubagentInstall> {
    let status = pi_subagent_status();
    if !status.installed {
        return Err(Error::NotFound(
            "the subagent extension is not installed, so there is nothing to update".to_string(),
        ));
    }
    install_pi_subagent()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_root(tag: &str) -> PathBuf {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|elapsed| elapsed.as_nanos())
            .unwrap_or(0);
        let root =
            std::env::temp_dir().join(format!("anvil-pi-ext-{tag}-{}-{nanos}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).expect("create temp root");
        root
    }

    fn fake_source(root: &Path) -> PathBuf {
        let source = root.join("examples/extensions/subagent");
        std::fs::create_dir_all(source.join("agents")).expect("agents dir");
        std::fs::create_dir_all(source.join("prompts")).expect("prompts dir");
        std::fs::write(source.join("index.ts"), "export default function () {}\n").expect("index");
        std::fs::write(source.join("agents.ts"), "export const agents = [];\n").expect("agents.ts");
        std::fs::write(source.join("README.md"), "# subagent\n").expect("readme");
        std::fs::write(
            source.join("agents/scout.md"),
            "---\nname: scout\ndescription: Fast recon\ntools: read, grep\nmodel: claude-haiku-4-5\n---\n\n# scout\n",
        )
        .expect("scout");
        std::fs::write(source.join("agents/reviewer.md"), "# reviewer\n").expect("reviewer");
        std::fs::write(source.join("prompts/implement.md"), "# implement\n").expect("prompt");
        source
    }

    #[test]
    fn a_missing_extension_with_a_source_is_installable() {
        let root = temp_root("missing");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");
        let status = status_at(&agent_dir, Some(&source), false);
        assert!(!status.installed);
        assert!(status.can_install);
        assert_eq!(status.agents, 0);
        assert!(status.detail.is_some());
        assert_eq!(
            status.source.as_deref(),
            Some(source.display().to_string().as_str())
        );
    }

    #[test]
    fn without_a_source_the_banner_cannot_offer_install() {
        let root = temp_root("nosource");
        let status = status_at(&root.join("agent"), None, false);
        assert!(!status.installed);
        assert!(!status.can_install);
        // The reason has to name the missing package copy, not just say "no".
        let detail = status.detail.unwrap_or_default();
        assert!(detail.contains("examples/extensions/subagent"), "{detail}");
    }

    #[test]
    fn install_copies_the_entry_agents_and_prompts_and_reports_installed() {
        let root = temp_root("install");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");

        let report = install_into(&agent_dir, &source).expect("install");
        assert_eq!(
            report.written, 6,
            "index.ts + agents.ts + README.md + two agents + one prompt"
        );
        assert_eq!(report.skipped, 0);
        assert!(report.status.installed);
        assert!(!report.status.can_install);
        assert_eq!(report.status.agents, 2);
        assert_eq!(report.status.prompts, 1);
        assert!(agent_dir.join("extensions/subagent/index.ts").is_file());
        assert!(agent_dir.join("extensions/subagent/agents.ts").is_file());
        assert!(agent_dir.join("agents/scout.md").is_file());
        assert!(agent_dir.join("prompts/implement.md").is_file());
    }

    #[test]
    fn install_keeps_a_user_edited_agent_and_counts_it_as_skipped() {
        let root = temp_root("keep");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");
        std::fs::create_dir_all(agent_dir.join("agents")).expect("agents");
        let edited = agent_dir.join("agents/scout.md");
        std::fs::write(&edited, "# my own scout\n").expect("write");

        let report = install_into(&agent_dir, &source).expect("install");
        assert_eq!(report.skipped, 1, "the existing scout must not be replaced");
        assert_eq!(
            std::fs::read_to_string(&edited).expect("read"),
            "# my own scout\n"
        );
        // The other agent still arrives.
        assert!(agent_dir.join("agents/reviewer.md").is_file());
    }

    #[test]
    fn install_is_idempotent_and_upgrades_the_extension_sources() {
        let root = temp_root("twice");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");
        install_into(&agent_dir, &source).expect("first install");
        std::fs::write(source.join("index.ts"), "// v2\n").expect("update source");

        let report = install_into(&agent_dir, &source).expect("second install");
        assert_eq!(report.skipped, 3, "two agents and one prompt already exist");
        assert_eq!(
            std::fs::read_to_string(agent_dir.join("extensions/subagent/index.ts")).expect("read"),
            "// v2\n",
            "shipped sources are replaced so an upgrade is not a half-old copy"
        );
    }

    #[test]
    fn installing_from_a_directory_without_an_entry_point_fails_loudly() {
        let root = temp_root("empty");
        let source = root.join("empty");
        std::fs::create_dir_all(&source).expect("dir");
        let error = install_into(&root.join("agent"), &source).expect_err("must reject");
        assert_eq!(error.code(), "not_found");
    }

    #[test]
    fn an_agent_directory_with_index_js_counts_as_installed() {
        // A packaged extension can ship compiled JavaScript; requiring `.ts`
        // would report "missing" for a working install.
        let root = temp_root("js");
        let agent_dir = root.join("agent");
        let dir = extension_dir(&agent_dir);
        std::fs::create_dir_all(&dir).expect("dir");
        std::fs::write(dir.join("index.js"), "module.exports = {}\n").expect("write");
        let status = status_at(&agent_dir, None, false);
        assert!(status.installed);
        assert!(!status.can_install);
        assert!(status.detail.is_none());
    }

    #[test]
    fn installed_agents_lose_the_sample_model_pin_so_they_inherit_the_parent() {
        // Measured before this behaviour existed: a fresh install delegated to
        // `scout`, the child asked for `claude-haiku-4-5`, and the call failed
        // with "Model ... is ambiguous across providers". With no `model:` key
        // the extension runs the child on the parent's model.
        let root = temp_root("pin");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");
        install_into(&agent_dir, &source).expect("install");

        let scout = std::fs::read_to_string(agent_dir.join("agents/scout.md")).expect("read");
        assert!(
            !scout.contains("\nmodel: "),
            "the pin must be commented out: {scout}"
        );
        assert!(
            scout.contains("# model: claude-haiku-4-5"),
            "the original pin stays visible: {scout}"
        );
        assert!(
            scout.contains("name: scout"),
            "front matter is preserved: {scout}"
        );

        let readme =
            std::fs::read_to_string(agent_dir.join("extensions/subagent/README.md")).expect("read");
        assert!(readme.contains("Installed by Anvil"), "{readme}");
    }

    #[test]
    fn unpin_only_touches_a_front_matter_key_not_prose() {
        let text = "---\nname: a\nmodel: claude-haiku-4-5\ntools: read\n---\n\nmodel: keep me\n";
        let out = unpin_model(text);
        assert!(out.contains("# model: claude-haiku-4-5"), "{out}");
        assert!(
            out.contains("\nmodel: keep me\n"),
            "prose is not front matter: {out}"
        );
        assert!(out.contains("name: a"));
        assert!(out.contains("tools: read"));
    }

    #[test]
    fn text_without_front_matter_is_left_alone() {
        let text = "model: claude-haiku-4-5\nnot a manifest\n";
        assert_eq!(unpin_model(text), text);
    }

    #[test]
    fn an_installed_copy_reports_stale_only_after_the_package_changes() {
        let root = temp_root("stale");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");
        install_into(&agent_dir, &source).expect("install");

        let fresh = status_at(&agent_dir, Some(&source), false);
        assert!(fresh.installed);
        assert!(
            !fresh.update_available,
            "a fresh install must not offer an update"
        );
        assert_eq!(fresh.installed_digest, fresh.source_digest);

        // The package moves with a pi upgrade; the copied file does not.
        std::fs::write(source.join("index.ts"), "// pi 1.7 changed the tool\n").expect("write");
        let stale = status_at(&agent_dir, Some(&source), false);
        assert!(stale.update_available);
        assert_ne!(stale.installed_digest, stale.source_digest);

        // Updating copies the new source and the report says "current" again.
        let report = install_into(&agent_dir, &source).expect("update");
        assert!(report.written >= 1);
        assert!(!report.status.update_available);
        let text =
            std::fs::read_to_string(agent_dir.join("extensions/subagent/index.ts")).expect("read");
        assert!(text.contains("pi 1.7"));
    }

    #[test]
    fn an_update_does_not_stack_the_readme_note() {
        let root = temp_root("note");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");
        install_into(&agent_dir, &source).expect("install");
        install_into(&agent_dir, &source).expect("update");
        let readme =
            std::fs::read_to_string(agent_dir.join("extensions/subagent/README.md")).expect("read");
        assert_eq!(
            readme.matches("## Installed by Anvil").count(),
            1,
            "{readme}"
        );
    }

    #[test]
    fn without_a_package_copy_there_is_no_update_to_offer() {
        let root = temp_root("noupdate");
        let source = fake_source(&root);
        let agent_dir = root.join("agent");
        install_into(&agent_dir, &source).expect("install");
        let status = status_at(&agent_dir, None, false);
        assert!(status.installed);
        assert!(
            !status.update_available,
            "cannot compare is not up to date, but it is not an update either"
        );
        assert!(status.detail.is_none());
    }

    #[test]
    fn a_verbatim_path_is_shown_without_the_windows_prefix() {
        // The source is canonicalised before it is reported; the notice must not
        // tell the user to look in `\\?\C:\...`.
        let status = status_at(Path::new(r"\\?\C:\pi\agent"), None, false);
        assert_eq!(status.agent_dir, r"C:\pi\agent");
        assert_eq!(status.extension_dir, r"C:\pi\agent\extensions\subagent");
    }

    /// Everything pi's docs say it loads, in the shapes it documents.
    #[test]
    fn the_scanner_sees_every_layout_pi_loads() {
        let root = temp_root("scan");
        let agent = root.join("agent");
        let ext = agent.join("extensions");
        std::fs::create_dir_all(ext.join("folder")).expect("folder");
        std::fs::write(ext.join("folder/index.js"), "export default () => {}\n").expect("index");
        std::fs::create_dir_all(ext.join("manifest/src")).expect("manifest src");
        std::fs::write(
            ext.join("manifest/package.json"),
            r#"{"name":"@me/pi-plugin","pi":{"extensions":["./src/main.ts"]}}"#,
        )
        .expect("manifest");
        std::fs::write(ext.join("manifest/src/main.ts"), "\n").expect("main");
        std::fs::write(ext.join("single.ts"), "\n").expect("single");
        std::fs::create_dir_all(ext.join("hollow")).expect("hollow");
        std::fs::write(
            agent.join("settings.json"),
            r#"{"extensions":["builtin:snake","/nope/missing.ts"],"packages":["npm:@me/pi-pack@1.0.0"]}"#,
        )
        .expect("settings");

        let rows = extensions_at(&agent);
        let by_name = |name: &str| rows.iter().find(|row| row.name == name).cloned();

        let single = by_name("single").expect("file extension");
        assert_eq!(single.kind, "file");
        assert!(single.active);

        let folder = by_name("folder").expect("directory extension");
        assert_eq!(folder.kind, "dir");
        assert!(folder.active);

        let manifest = by_name("manifest").expect("manifest extension");
        assert_eq!(manifest.kind, "package");
        assert!(manifest.active, "pi.extensions resolved: {manifest:?}");
        assert!(manifest.entry.is_some());

        let hollow = by_name("hollow").expect("broken extension is still listed");
        assert!(!hollow.active);
        assert!(hollow.detail.is_some());

        let builtin = by_name("snake").expect("builtin entry");
        assert_eq!(builtin.kind, "builtin");
        assert!(builtin.active);

        let missing = by_name("missing").expect("settings path is listed");
        assert!(!missing.active);

        let declared = rows
            .iter()
            .find(|row| row.name.contains("pi-pack"))
            .expect("declared package");
        assert_eq!(declared.kind, "declared");

        assert!(has_extension(&rows, "folder"));
        assert!(!has_extension(&rows, "subagent"));
    }

    /// A plugin installed as `subagent.ts` or an npm package counts as
    /// installed: the flag used to read only `extensions/subagent/index.ts`.
    #[test]
    fn a_subagent_installed_in_any_shape_is_detected() {
        let root = temp_root("subagent-shapes");

        let single = root.join("single");
        std::fs::create_dir_all(single.join("extensions")).expect("dir");
        std::fs::write(single.join("extensions/subagent.ts"), "\n").expect("file");
        assert!(has_extension(&extensions_at(&single), "subagent"));

        let packaged = root.join("packaged");
        std::fs::create_dir_all(packaged.join("extensions/pi-subagent")).expect("dir");
        std::fs::write(packaged.join("extensions/pi-subagent/index.js"), "\n").expect("index");
        assert!(has_extension(&extensions_at(&packaged), "subagent"));

        let settings = root.join("settings");
        std::fs::create_dir_all(&settings).expect("dir");
        std::fs::write(
            settings.join("settings.json"),
            r#"{"packages":["npm:@scope/pi-subagent@2.0.0"]}"#,
        )
        .expect("settings");
        assert!(has_extension(&extensions_at(&settings), "subagent"));

        // A directory with no entry point is not an installed extension.
        let hollow = root.join("hollow");
        std::fs::create_dir_all(hollow.join("extensions/subagent")).expect("dir");
        assert!(!has_extension(&extensions_at(&hollow), "subagent"));
    }
}
