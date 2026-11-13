//! Runtime-independent skill inventory.
//!
//! Sync only reads metadata: no command from a skill is executed, links are not
//! followed (a junction could point anywhere), malformed skills are listed as
//! `broken` instead of disappearing, and the same skill in two roots stays two
//! rows because the root is part of the identity.

use std::path::{Path, PathBuf};

use crate::error::Result;
use crate::model::SkillRecord;

/// One directory a runtime searches for skills.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SkillRoot {
    /// Short label shown in the UI (`claude`, `opencode`, …).
    pub label: String,
    pub path: PathBuf,
    /// Whether this root belongs to the runtime being synced.
    pub runtime_id: String,
}

impl SkillRoot {
    pub fn new(runtime_id: &str, label: &str, path: PathBuf) -> Self {
        Self {
            label: label.to_string(),
            path,
            runtime_id: runtime_id.to_string(),
        }
    }
}

/// Scan every root and return a stable, sorted inventory.
pub fn scan(roots: &[SkillRoot], updated_at: &str) -> Result<Vec<SkillRecord>> {
    let mut records = Vec::new();
    for root in roots {
        if !root.path.is_dir() {
            continue;
        }
        let canonical_root =
            std::fs::canonicalize(&root.path).unwrap_or_else(|_| root.path.clone());
        let entries = match std::fs::read_dir(&root.path) {
            Ok(entries) => entries,
            Err(error) => {
                records.push(SkillRecord {
                    runtime_id: root.runtime_id.clone(),
                    name: root.label.clone(),
                    description: String::new(),
                    root: root.path.to_string_lossy().into_owned(),
                    path: root.path.to_string_lossy().into_owned(),
                    state: "broken".to_string(),
                    reason: Some(format!("skill root is unreadable: {error}")),
                    updated_at: updated_at.to_string(),
                });
                continue;
            }
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if !path.is_dir() {
                continue;
            }
            records.push(read_skill(root, &canonical_root, &path, updated_at));
        }
    }
    records.sort_by(|left, right| {
        left.name
            .cmp(&right.name)
            .then_with(|| left.root.cmp(&right.root))
    });
    Ok(records)
}

fn read_skill(
    root: &SkillRoot,
    canonical_root: &Path,
    path: &Path,
    updated_at: &str,
) -> SkillRecord {
    let name_fallback = path
        .file_name()
        .map(|value| value.to_string_lossy().into_owned())
        .unwrap_or_else(|| "unknown".to_string());
    let base = |state: &str, reason: Option<String>| SkillRecord {
        runtime_id: root.runtime_id.clone(),
        name: name_fallback.clone(),
        description: String::new(),
        root: root.path.to_string_lossy().into_owned(),
        path: path.to_string_lossy().into_owned(),
        state: state.to_string(),
        reason,
        updated_at: updated_at.to_string(),
    };

    // A junction or symlink can point outside the root; report it and do not
    // read through it.
    match std::fs::symlink_metadata(path) {
        Ok(metadata) => {
            if metadata.file_type().is_symlink() || is_reparse_point(&metadata) {
                return base(
                    "linked",
                    Some("symbolic link or junction; the target was not read".into()),
                );
            }
        }
        Err(error) => {
            return base(
                "broken",
                Some(format!("cannot stat skill directory: {error}")),
            );
        }
    }

    if let Ok(canonical) = std::fs::canonicalize(path)
        && !canonical.starts_with(canonical_root)
    {
        return base(
            "linked",
            Some("resolves outside the skill root; the target was not read".into()),
        );
    }

    let manifest = path.join("SKILL.md");
    if !manifest.is_file() {
        return base(
            "broken",
            Some("SKILL.md is missing; the directory is not a skill".into()),
        );
    }
    let text = match std::fs::read_to_string(&manifest) {
        Ok(text) => text,
        Err(error) => return base("broken", Some(format!("SKILL.md is unreadable: {error}"))),
    };
    match parse_frontmatter(&text) {
        Some(metadata) => {
            if metadata.name.trim().is_empty() {
                return base(
                    "broken",
                    Some("frontmatter has no name; the skill cannot be routed".into()),
                );
            }
            SkillRecord {
                runtime_id: root.runtime_id.clone(),
                name: metadata.name,
                description: metadata.description,
                root: root.path.to_string_lossy().into_owned(),
                path: path.to_string_lossy().into_owned(),
                state: "valid".to_string(),
                reason: None,
                updated_at: updated_at.to_string(),
            }
        }
        None => base("broken", Some("SKILL.md has no YAML frontmatter".into())),
    }
}

#[cfg(windows)]
fn is_reparse_point(metadata: &std::fs::Metadata) -> bool {
    use std::os::windows::fs::MetadataExt;
    const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x0400;
    metadata.file_attributes() & FILE_ATTRIBUTE_REPARSE_POINT != 0
}

#[cfg(not(windows))]
fn is_reparse_point(_metadata: &std::fs::Metadata) -> bool {
    false
}

/// Minimal YAML frontmatter reader: `name` and `description`, including folded
/// (`>`) and literal (`|`) blocks. Anything else is ignored on purpose.
pub fn parse_frontmatter(text: &str) -> Option<SkillMetadata> {
    let mut lines = text.lines();
    if lines.next()?.trim() != "---" {
        return None;
    }
    let mut name = String::new();
    let mut description = String::new();
    let mut field: Option<&'static str> = None;
    let mut block: Vec<String> = Vec::new();
    let mut closed = false;

    let flush = |field: &mut Option<&'static str>,
                 block: &mut Vec<String>,
                 name: &mut String,
                 description: &mut String| {
        if let Some(target) = field.take() {
            let joined = block.join(" ").trim().to_string();
            block.clear();
            match target {
                "name" => *name = joined,
                _ => *description = joined,
            }
        }
    };

    for line in lines {
        if line.trim() == "---" {
            closed = true;
            break;
        }
        let is_continuation =
            line.starts_with(' ') || line.starts_with('\t') || line.trim().is_empty();
        if is_continuation && field.is_some() {
            let text = line.trim();
            if !text.is_empty() {
                block.push(text.to_string());
            }
            continue;
        }
        flush(&mut field, &mut block, &mut name, &mut description);
        let Some((key, value)) = line.split_once(':') else {
            continue;
        };
        let key = key.trim();
        let value = value.trim();
        if key != "name" && key != "description" {
            continue;
        }
        if value == ">" || value == "|" || value == ">-" || value == "|-" {
            field = Some(if key == "name" { "name" } else { "description" });
            continue;
        }
        let unquoted = value.trim_matches('"').trim_matches('\'').to_string();
        if key == "name" {
            name = unquoted;
        } else {
            description = unquoted;
        }
    }
    if !closed {
        return None;
    }
    flush(&mut field, &mut block, &mut name, &mut description);
    Some(SkillMetadata { name, description })
}

/// The two fields the catalog keeps.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SkillMetadata {
    pub name: String,
    pub description: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_root(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "anvil-skills-{name}-{}-{}",
            std::process::id(),
            crate::clock::unix_now()
        ));
        std::fs::create_dir_all(&dir).expect("temp root");
        dir
    }

    #[test]
    fn parses_plain_and_folded_frontmatter() {
        let plain = "---\nname: demo\ndescription: A demo skill\n---\nbody\n";
        let parsed = parse_frontmatter(plain).expect("frontmatter");
        assert_eq!(parsed.name, "demo");
        assert_eq!(parsed.description, "A demo skill");

        let folded = "---\nname: folded\ndescription: >\n  first line\n  second line\n---\n";
        let parsed = parse_frontmatter(folded).expect("frontmatter");
        assert_eq!(parsed.description, "first line second line");
    }

    #[test]
    fn rejects_documents_without_frontmatter() {
        assert!(parse_frontmatter("# just markdown\n").is_none());
        assert!(parse_frontmatter("---\nname: open\n").is_none());
    }

    #[test]
    fn scans_valid_broken_and_missing_skills() {
        let root = temp_root("scan");
        let valid = root.join("demo");
        std::fs::create_dir_all(&valid).expect("demo");
        std::fs::write(
            valid.join("SKILL.md"),
            "---\nname: demo\ndescription: does things\n---\n",
        )
        .expect("write");

        let broken = root.join("broken");
        std::fs::create_dir_all(&broken).expect("broken");
        std::fs::write(broken.join("SKILL.md"), "# no frontmatter\n").expect("write");

        let empty = root.join("not-a-skill");
        std::fs::create_dir_all(&empty).expect("empty");

        let roots = vec![SkillRoot::new("demo-runtime", "demo", root.clone())];
        let records = scan(&roots, "2026-01-01T00:00:00Z").expect("scan");
        assert_eq!(records.len(), 3);
        let states: Vec<&str> = records.iter().map(|row| row.state.as_str()).collect();
        assert!(states.contains(&"valid"));
        assert_eq!(states.iter().filter(|state| **state == "broken").count(), 2);
        let valid_row = records
            .iter()
            .find(|row| row.name == "demo")
            .expect("valid row");
        assert_eq!(valid_row.description, "does things");
        assert_eq!(valid_row.runtime_id, "demo-runtime");

        std::fs::remove_dir_all(&root).ok();
    }

    #[test]
    fn same_name_in_two_roots_stays_two_rows() {
        let first = temp_root("roots-a");
        let second = temp_root("roots-b");
        for root in [&first, &second] {
            let skill = root.join("shared");
            std::fs::create_dir_all(&skill).expect("skill");
            std::fs::write(
                skill.join("SKILL.md"),
                "---\nname: shared\ndescription: x\n---\n",
            )
            .expect("write");
        }
        let roots = vec![
            SkillRoot::new("demo-runtime", "a", first.clone()),
            SkillRoot::new("demo-runtime", "b", second.clone()),
        ];
        let records = scan(&roots, "2026-01-01T00:00:00Z").expect("scan");
        assert_eq!(records.len(), 2);
        assert_ne!(records[0].root, records[1].root);
        std::fs::remove_dir_all(&first).ok();
        std::fs::remove_dir_all(&second).ok();
    }
}
