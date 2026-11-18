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

/// `<agent-dir>`: `PI_CODING_AGENT_DIR` wins, otherwise `~/.pi/agent`.
///
/// Pi documents the same precedence; honouring the variable matters because a
/// user who isolated pi's config would otherwise get an extension installed into
/// a directory pi never reads.
#[must_use]
pub fn agent_dir() -> Option<PathBuf> {
    if let Some(dir) = std::env::var_os("PI_CODING_AGENT_DIR")
        && !dir.is_empty()
    {
        return Some(PathBuf::from(dir));
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
fn status_at(agent_dir: &Path, source: Option<&Path>) -> PiSubagentStatus {
    let dir = extension_dir(agent_dir);
    let installed = has_entry(&dir);
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
    PiSubagentStatus {
        installed,
        agent_dir: display_path(agent_dir),
        extension_dir: display_path(&dir),
        source: source.map(display_path),
        agents: count_markdown(&agent_dir.join("agents")),
        prompts: count_markdown(&agent_dir.join("prompts")),
        can_install: !installed && source.is_some(),
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
        Some(dir) => status_at(&dir, source.as_deref()),
        None => PiSubagentStatus {
            installed: false,
            agent_dir: String::new(),
            extension_dir: String::new(),
            source: source.map(|path| display_path(&path)),
            agents: 0,
            prompts: 0,
            can_install: false,
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
        text.push_str(INSTALL_NOTE);
        std::fs::write(&readme, text)?;
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
        status: status_at(agent_dir, Some(source)),
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
        let status = status_at(&agent_dir, Some(&source));
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
        let status = status_at(&root.join("agent"), None);
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
        let status = status_at(&agent_dir, None);
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
    fn a_verbatim_path_is_shown_without_the_windows_prefix() {
        // The source is canonicalised before it is reported; the notice must not
        // tell the user to look in `\\?\C:\...`.
        let status = status_at(Path::new(r"\\?\C:\pi\agent"), None);
        assert_eq!(status.agent_dir, r"C:\pi\agent");
        assert_eq!(status.extension_dir, r"C:\pi\agent\extensions\subagent");
    }
}
