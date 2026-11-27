//! Copying an old `~/.pi` install into the Anvil data root (PIB02).
//!
//! The bundled runtime keeps its agent directory at `HostConfig.data_dir` and
//! its user skills at `<data_dir>/skill` (ADR 0009). A user upgrading from a
//! build that borrowed their `~/.pi` therefore has configuration and skills in a
//! place this runtime never reads.
//!
//! The import is deliberately dull, because it runs over files the user made:
//!
//! * an allowlist, not a copy-everything: `models.json`, `settings.json`,
//!   `mcp.json`, `extensions/`, `agents/`, `prompts/` and the skills directories;
//! * `auth.json` and `sessions/` are **never** touched — credentials are
//!   re-entered by the user through the normal sign-in, and conversation history
//!   belongs to the ledger that wrote it;
//! * an existing target is skipped and reported, never overwritten;
//! * one failed item does not abort the rest, so a second run retries only what
//!   is left.
//!
//! [`plan`] is read-only and is what a page shows before asking; [`run`] does
//! the copying and reports what happened.

use std::path::{Path, PathBuf};

use serde::Serialize;

/// Agent-directory entries worth carrying over.
///
/// Everything absent from this list stays behind on purpose. That is the whole
/// safety argument of the import: a new name in `~/.pi/agent` cannot silently
/// become part of an Anvil install.
pub const AGENT_ENTRIES: [&str; 6] = [
    "models.json",
    "settings.json",
    "mcp.json",
    "extensions",
    "agents",
    "prompts",
];

/// Files inside the agent directory that must never be copied.
pub const NEVER_COPY: [&str; 3] = ["auth.json", "sessions", "logs"];

/// One entry the import would copy.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Item {
    /// Path relative to the source root.
    pub relative: String,
    pub source: String,
    pub target: String,
    /// `new`, `exists` or `unsupported`.
    pub state: String,
    /// Why, when the state is not `new`.
    pub detail: String,
}

/// The two sides of one import, already resolved.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Plan {
    pub agent_source: String,
    pub skill_source: String,
    pub agent_target: String,
    pub skill_target: String,
    /// `agent`, `skills` or both: what the source actually has.
    pub available: bool,
    pub items: Vec<Item>,
}

impl Plan {
    #[must_use]
    pub fn to_copy(&self) -> Vec<&Item> {
        self.items
            .iter()
            .filter(|item| item.state == "new")
            .collect()
    }

    #[must_use]
    pub fn skipped(&self) -> Vec<&Item> {
        self.items
            .iter()
            .filter(|item| item.state == "exists")
            .collect()
    }
}

/// What one `run` did.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Report {
    pub copied: usize,
    /// Targets left alone because they already existed.
    pub skipped: usize,
    /// Items that failed, with the reason. A retry re-plans and tries again.
    pub failed: Vec<String>,
}

/// Legacy agent directory: `~/.pi/agent`.
#[must_use]
pub fn legacy_agent_dir(home: &Path) -> PathBuf {
    home.join(".pi").join("agent")
}

/// Legacy skill root: `~/.pi/skills`.
#[must_use]
pub fn legacy_skill_root(home: &Path) -> PathBuf {
    home.join(".pi").join("skills")
}

/// What an import from these locations would do.
#[must_use]
pub fn plan(
    legacy_agent: &Path,
    legacy_skills: &Path,
    agent_target: &Path,
    skill_target: &Path,
) -> Plan {
    let mut items = Vec::new();
    if legacy_agent.is_dir() {
        for entry in AGENT_ENTRIES {
            let source = legacy_agent.join(entry);
            if !source.exists() {
                continue;
            }
            push_item(
                &mut items,
                &source,
                &agent_target.join(entry),
                entry,
                legacy_agent,
            );
        }
    }
    if legacy_skills.is_dir() {
        let Ok(entries) = std::fs::read_dir(legacy_skills) else {
            return finish(
                items,
                legacy_agent,
                legacy_skills,
                agent_target,
                skill_target,
            );
        };
        let mut names: Vec<String> = entries
            .filter_map(|entry| entry.ok())
            .map(|entry| entry.file_name().to_string_lossy().into_owned())
            .collect();
        names.sort();
        for name in names {
            if NEVER_COPY.contains(&name.as_str()) {
                continue;
            }
            let source = legacy_skills.join(&name);
            push_item(
                &mut items,
                &source,
                &skill_target.join(&name),
                &name,
                legacy_skills,
            );
        }
    }
    finish(
        items,
        legacy_agent,
        legacy_skills,
        agent_target,
        skill_target,
    )
}

fn finish(
    items: Vec<Item>,
    legacy_agent: &Path,
    legacy_skills: &Path,
    agent_target: &Path,
    skill_target: &Path,
) -> Plan {
    Plan {
        agent_source: legacy_agent.display().to_string(),
        skill_source: legacy_skills.display().to_string(),
        agent_target: agent_target.display().to_string(),
        skill_target: skill_target.display().to_string(),
        available: !items.is_empty(),
        items,
    }
}

fn push_item(items: &mut Vec<Item>, source: &Path, target: &Path, label: &str, root: &Path) {
    let relative = source
        .strip_prefix(root)
        .map(|path| path.to_string_lossy().into_owned())
        .unwrap_or_else(|_| label.to_string());
    let (state, detail) = if target.exists() {
        (
            "exists",
            "already in the Anvil data directory; kept as it is".to_string(),
        )
    } else if !(source.is_file() || source.is_dir()) {
        (
            "unsupported",
            "neither a file nor a directory; skipped".to_string(),
        )
    } else {
        ("new", String::new())
    };
    items.push(Item {
        relative,
        source: source.display().to_string(),
        target: target.display().to_string(),
        state: state.to_string(),
        detail,
    });
}

/// Copy everything the plan marked `new`.
///
/// A missing target directory is created. Existing targets are skipped again
/// here — the plan may be a minute old by the time the user says yes.
#[must_use]
pub fn run(plan: &Plan) -> Report {
    let mut report = Report::default();
    for item in &plan.items {
        if item.state != "new" {
            report.skipped += 1;
            continue;
        }
        let source = Path::new(&item.source);
        let target = Path::new(&item.target);
        if target.exists() {
            report.skipped += 1;
            continue;
        }
        match copy(source, target) {
            Ok(()) => report.copied += 1,
            Err(error) => report.failed.push(format!("{}: {error}", item.relative)),
        }
    }
    report
}

fn copy(source: &Path, target: &Path) -> std::io::Result<()> {
    if source.is_file() {
        if let Some(parent) = target.parent() {
            std::fs::create_dir_all(parent)?;
        }
        std::fs::copy(source, target)?;
        return Ok(());
    }
    copy_tree(source, target)
}

/// Recursive copy that keeps directory shape and refuses to overwrite a file
/// that appeared while the user was deciding.
fn copy_tree(source: &Path, target: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(target)?;
    for entry in std::fs::read_dir(source)? {
        let entry = entry?;
        let name = entry.file_name();
        let from = entry.path();
        let to = target.join(&name);
        if to.exists() {
            // A file that is already there is the user's; leaving it alone is
            // the same rule the top level follows.
            continue;
        }
        if from.is_dir() {
            copy_tree(&from, &to)?;
        } else {
            std::fs::copy(&from, &to)?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("pi-import")
            .join(name);
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("scratch");
        dir
    }

    fn write(path: &Path, body: &str) {
        std::fs::create_dir_all(path.parent().expect("parent")).expect("mkdir");
        std::fs::write(path, body).expect("write");
    }

    fn legacy(root: &Path) -> (PathBuf, PathBuf) {
        let agent = root.join("legacy/agent");
        write(&agent.join("models.json"), "{\"providers\":{}}");
        write(&agent.join("settings.json"), "{\"defaultModel\":\"m\"}");
        write(
            &agent.join("auth.json"),
            "{\"deepseek\":{\"type\":\"oauth\"}}",
        );
        write(&agent.join("extensions/keep/index.ts"), "// extension");
        write(&agent.join("sessions/old.jsonl"), "{}");
        let skills = root.join("legacy/skills");
        write(&skills.join("writer/SKILL.md"), "# writer");
        write(&skills.join("reviewer/SKILL.md"), "# reviewer");
        (agent, skills)
    }

    /// The allowlist is the safety property: a credential store and a transcript
    /// directory sitting in the same folder must not come along.
    #[test]
    fn credentials_and_sessions_are_never_part_of_the_plan() {
        let root = scratch("allowlist");
        let (agent, skills) = legacy(&root);
        let plan = plan(
            &agent,
            &skills,
            &root.join("target"),
            &root.join("target/skill"),
        );
        let relatives: Vec<&str> = plan
            .items
            .iter()
            .map(|item| item.relative.as_str())
            .collect();
        assert!(relatives.contains(&"models.json"), "{relatives:?}");
        assert!(relatives.contains(&"extensions"), "{relatives:?}");
        assert!(relatives.contains(&"writer"), "{relatives:?}");
        assert!(!relatives.iter().any(|name| name.contains("auth.json")));
        assert!(!relatives.iter().any(|name| name.contains("sessions")));
        assert!(plan.available);
        assert_eq!(plan.to_copy().len(), plan.items.len());
    }

    #[test]
    fn an_import_copies_files_directories_and_skills() {
        let root = scratch("copy");
        let (agent, skills) = legacy(&root);
        let target = root.join("target");
        let skill_target = target.join("skill");
        let plan = plan(&agent, &skills, &target, &skill_target);
        let report = run(&plan);
        assert_eq!(report.failed, Vec::<String>::new());
        assert_eq!(report.copied, plan.items.len());
        assert!(target.join("models.json").is_file());
        assert!(target.join("extensions/keep/index.ts").is_file());
        assert!(skill_target.join("writer/SKILL.md").is_file());
        assert!(!target.join("auth.json").exists());
        assert!(!target.join("sessions").exists());
    }

    /// A second import must not overwrite what is already there: the files in
    /// the data directory are the ones the user has been editing since.
    #[test]
    fn a_second_import_skips_everything_and_changes_nothing() {
        let root = scratch("idempotent");
        let (agent, skills) = legacy(&root);
        let target = root.join("target");
        let skill_target = target.join("skill");
        assert_eq!(
            run(&plan(&agent, &skills, &target, &skill_target))
                .failed
                .len(),
            0
        );
        // The user edits one file after the import.
        write(&target.join("models.json"), "{\"edited\":true}");

        let second = plan(&agent, &skills, &target, &skill_target);
        assert!(second.to_copy().is_empty(), "{:?}", second.items);
        let report = run(&second);
        assert_eq!(report.copied, 0);
        assert_eq!(report.skipped, second.items.len());
        assert_eq!(
            std::fs::read_to_string(target.join("models.json")).expect("read"),
            "{\"edited\":true}",
            "an existing file is never overwritten"
        );
    }

    /// One blocked item must not take the rest with it, and the reason has to be
    /// specific enough to act on.
    #[test]
    fn a_failed_item_is_reported_and_the_rest_still_copies() {
        let root = scratch("partial");
        let (agent, skills) = legacy(&root);
        let target = root.join("target");
        // The agent target's parent is a file, so every agent item fails while
        // the skills (a different root) still land. That is the shape of a
        // half-broken install, and the report has to name it instead of
        // pretending the import worked.
        write(&target.join("blocked"), "a file, not a directory");
        let items = plan(
            &agent,
            &skills,
            &target.join("blocked"),
            &target.join("skill"),
        );
        let report = run(&items);
        assert!(!report.failed.is_empty(), "a blocked copy must be reported");
        assert!(
            report.failed[0].contains("models.json"),
            "{:?}",
            report.failed
        );
        assert!(
            target.join("skill/writer/SKILL.md").is_file(),
            "the rest still copied"
        );
        assert!(!target.join("blocked/models.json").exists());
    }

    /// The allowlist is closed, and the three forbidden names are not in it: a
    /// credential store, a transcript directory and the runtime's own logs can
    /// all sit in the same folder as the files worth keeping.
    #[test]
    fn the_allowlist_is_closed_and_the_forbidden_names_are_outside_it() {
        for never in NEVER_COPY {
            assert!(
                !AGENT_ENTRIES.contains(&never),
                "{never} must never be an allowlisted agent entry"
            );
        }
        let root = scratch("closed");
        let (agent, skills) = legacy(&root);
        write(&agent.join("logs/anvil.log"), "log\n");
        let plan = plan(
            &agent,
            &skills,
            &root.join("target"),
            &root.join("target/skill"),
        );
        for never in NEVER_COPY {
            assert!(
                !plan.items.iter().any(|item| item.relative == never),
                "{never} must not be imported: {:?}",
                plan.items
                    .iter()
                    .map(|item| item.relative.as_str())
                    .collect::<Vec<_>>()
            );
        }
        assert!(plan.items.iter().all(|item| item.state == "new"));
    }

    #[test]
    fn an_empty_machine_has_nothing_to_offer() {
        let root = scratch("empty");
        let plan = plan(
            &root.join("nope/agent"),
            &root.join("nope/skills"),
            &root.join("target"),
            &root.join("target/skill"),
        );
        assert!(!plan.available);
        assert!(plan.items.is_empty());
        assert_eq!(run(&plan).copied, 0);
    }
}
