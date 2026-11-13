//! `@` references: resolution, confinement and prompt folding.
//!
//! Three rules shape this module:
//!
//! * **The session's project is the boundary.** A reference inside it is read
//!   without ceremony; one outside it is not read until the user approved that
//!   specific path, and a refusal leaves nothing behind.
//! * **Resolution goes through `canonicalize`.** `..`, junction points and
//!   symlinks all collapse to one absolute path, so a link that points out of the
//!   project is caught by the same check as a literal `..\..`.
//! * **Budgets are visible.** A cut file says it was cut, a directory says how
//!   many entries were not listed, and the ledger records what was inlined.
//!   Silently sending half a file would make the model's answer look wrong for
//!   no stated reason.

use std::path::{Path, PathBuf};

use crate::error::{Error, Result};
use crate::model::{ContextEntry, ContextKind, ContextRef, ResolvedRef};

/// More references than this in one turn is a mistake, not a workflow.
pub const MAX_REFS: usize = 8;
/// How much of one file is inlined.
pub const MAX_FILE_BYTES: usize = 8 * 1024;
/// How much context one turn may carry in total.
pub const MAX_TOTAL_CHARS: usize = 24 * 1024;
/// Directory listings are shallow and bounded.
const MAX_DIR_ENTRIES: usize = 50;
/// How deep the composer's `@` list walks.
const LIST_DEPTH: usize = 3;
/// Directories the `@` list never walks into.
const LIST_SKIP: [&str; 7] = [
    ".git",
    "node_modules",
    "target",
    "dist",
    ".tmp",
    ".venv",
    "__pycache__",
];

/// One reference after planning: either ready to inline, or waiting for consent.
#[derive(Debug)]
pub enum Step {
    Ready(Box<Prepared>),
    /// The path resolves outside the project; the caller must ask the user.
    Awaiting {
        reference: ContextRef,
        path: PathBuf,
    },
}

/// A reference that was read (or listed) and rendered.
#[derive(Debug)]
pub struct Prepared {
    pub resolved: ResolvedRef,
    pub block: String,
}

/// Resolve every reference, reading the ones that are inside the boundary.
///
/// Outside-root references come back as [`Step::Awaiting`] with their canonical
/// path: the caller decides whether to ask, and reads them with [`allow`] only
/// after the user said yes.
pub fn plan(
    project_root: &str,
    skill_roots: &[PathBuf],
    references: &[ContextRef],
) -> Result<Vec<Step>> {
    if references.len() > MAX_REFS {
        return Err(Error::Rejected(format!(
            "at most {MAX_REFS} context references per turn, got {}",
            references.len()
        )));
    }
    let root = canonical_dir(project_root)?;
    let mut steps = Vec::with_capacity(references.len());
    let mut seen: Vec<PathBuf> = Vec::new();
    for reference in references {
        let path = resolve_path(&root, &reference.path)?;
        // Two ways of naming the same file must not double the prompt.
        if seen.iter().any(|known| same_path(known, &path)) {
            continue;
        }
        seen.push(path.clone());
        if reference.kind == ContextKind::Skill {
            let inside = skill_roots
                .iter()
                .any(|skill_root| is_within(skill_root, &path));
            if !inside {
                return Err(Error::Rejected(format!(
                    "skill {} is not under a configured skill root",
                    path.display()
                )));
            }
            steps.push(Step::Ready(Box::new(read(reference, &path, false)?)));
            continue;
        }
        if is_within(&root, &path) {
            steps.push(Step::Ready(Box::new(read(reference, &path, false)?)));
        } else {
            steps.push(Step::Awaiting {
                reference: reference.clone(),
                path,
            });
        }
    }
    Ok(steps)
}

/// Read a reference the user explicitly approved, even though it is outside the
/// project. Kept separate from [`plan`] so "outside" can never be silently read.
pub fn allow(reference: &ContextRef, path: &Path) -> Result<Prepared> {
    read(reference, path, true)
}

/// Fold the prepared references into the prompt the runtime receives.
///
/// The user's words stay last and untouched: every runtime reads the tail of a
/// prompt more reliably than the middle.
pub fn fold(prompt: &str, prepared: &[Prepared]) -> String {
    if prepared.is_empty() {
        return prompt.to_string();
    }
    let mut budget = MAX_TOTAL_CHARS;
    let mut blocks: Vec<&str> = Vec::new();
    for entry in prepared {
        if entry.block.chars().count() > budget {
            blocks.push("<context omitted=\"budget exhausted\"/>");
            budget = 0;
            continue;
        }
        budget -= entry.block.chars().count();
        blocks.push(&entry.block);
    }
    format!(
        "The user attached the following context. Use it when answering the last message.\n\n{}\n\n{prompt}",
        blocks.join("\n\n")
    )
}

/// Entries for the composer's `@` list, relative to `project_root`.
pub fn entries(project_root: &str, query: &str, limit: usize) -> Result<Vec<ContextEntry>> {
    let root = canonical_dir(project_root)?;
    let needle = query.trim().to_lowercase();
    let limit = limit.clamp(1, 100);
    let mut found: Vec<ContextEntry> = Vec::new();
    walk(&root, &root, 0, &needle, limit, &mut found);
    // Shallow names first: the thing the user is typing is usually near the top.
    found.sort_by_key(|entry| {
        (
            entry.relative.matches(['/', '\\']).count(),
            entry.relative.clone(),
        )
    });
    found.truncate(limit);
    Ok(found)
}

fn walk(
    root: &Path,
    directory: &Path,
    depth: usize,
    needle: &str,
    limit: usize,
    found: &mut Vec<ContextEntry>,
) {
    if depth > LIST_DEPTH || found.len() >= limit * 4 {
        return;
    }
    let Ok(entries) = std::fs::read_dir(directory) else {
        return;
    };
    let mut paths: Vec<PathBuf> = entries.flatten().map(|entry| entry.path()).collect();
    paths.sort();
    for path in paths {
        let Some(name) = path.file_name().and_then(|name| name.to_str()) else {
            continue;
        };
        if name.starts_with('.') && name != ".env" {
            continue;
        }
        let is_dir = path.is_dir();
        if is_dir && LIST_SKIP.contains(&name) {
            continue;
        }
        let relative = path
            .strip_prefix(root)
            .unwrap_or(&path)
            .to_string_lossy()
            .replace('\\', "/");
        if needle.is_empty() || relative.to_lowercase().contains(needle) {
            found.push(ContextEntry {
                path: path.to_string_lossy().to_string(),
                relative: if is_dir {
                    format!("{relative}/")
                } else {
                    relative.clone()
                },
                kind: if is_dir {
                    ContextKind::Dir
                } else {
                    ContextKind::File
                },
                bytes: if is_dir {
                    0
                } else {
                    std::fs::metadata(&path).map(|meta| meta.len()).unwrap_or(0)
                },
            });
        }
        if is_dir && found.len() < limit * 4 {
            walk(root, &path, depth + 1, needle, limit, found);
        }
    }
}

fn read(reference: &ContextRef, path: &Path, outside_root: bool) -> Result<Prepared> {
    let metadata = std::fs::metadata(path)
        .map_err(|error| Error::NotFound(format!("{}: {error}", path.display())))?;
    let label = reference.label.clone();
    match reference.kind {
        ContextKind::Dir => {
            if !metadata.is_dir() {
                return Err(Error::Rejected(format!(
                    "{} is not a directory",
                    path.display()
                )));
            }
            let listing = listing(path)?;
            Ok(Prepared {
                block: format!(
                    "<directory path=\"{}\">\n{}\n</directory>",
                    path.display(),
                    listing.0
                ),
                resolved: ResolvedRef {
                    kind: ContextKind::Dir,
                    path: path.to_string_lossy().to_string(),
                    label,
                    bytes: 0,
                    chars: listing.0.chars().count(),
                    truncated: listing.1,
                    outside_root,
                },
            })
        }
        ContextKind::File | ContextKind::Skill => {
            let source = if reference.kind == ContextKind::Skill && metadata.is_dir() {
                path.join("SKILL.md")
            } else {
                path.to_path_buf()
            };
            if source.is_dir() {
                return Err(Error::Rejected(format!(
                    "{} is a directory; reference it as a directory",
                    path.display()
                )));
            }
            let raw = std::fs::read(&source)
                .map_err(|error| Error::NotFound(format!("{}: {error}", source.display())))?;
            let bytes = metadata.len();
            let text = String::from_utf8(raw).map_err(|_| {
                Error::Rejected(format!("{} is not a UTF-8 text file", source.display()))
            })?;
            let (body, truncated) = truncate(&text, MAX_FILE_BYTES);
            let label_text = if reference.kind == ContextKind::Skill {
                let name = label.clone().unwrap_or_else(|| {
                    source
                        .parent()
                        .and_then(|dir| dir.file_name())
                        .map(|name| name.to_string_lossy().to_string())
                        .unwrap_or_default()
                });
                format!("skill {name} ({})", source.display())
            } else {
                format!("file {}", path.display())
            };
            let mut block = format!("--- {label_text} ---\n{body}\n--- end ---");
            if truncated {
                block.push_str(&format!(
                    "\n<!-- truncated at {MAX_FILE_BYTES} bytes of {bytes} -->"
                ));
            }
            Ok(Prepared {
                block,
                resolved: ResolvedRef {
                    kind: reference.kind,
                    path: path.to_string_lossy().to_string(),
                    label,
                    bytes,
                    chars: body.chars().count(),
                    truncated,
                    outside_root,
                },
            })
        }
    }
}

fn listing(directory: &Path) -> Result<(String, bool)> {
    let mut names: Vec<String> = std::fs::read_dir(directory)?
        .flatten()
        .map(|entry| {
            let name = entry.file_name().to_string_lossy().to_string();
            let is_dir = entry.path().is_dir();
            if is_dir { format!("{name}/") } else { name }
        })
        .collect();
    names.sort();
    let total = names.len();
    let truncated = total > MAX_DIR_ENTRIES;
    names.truncate(MAX_DIR_ENTRIES);
    let mut text = names.join("\n");
    if truncated {
        text.push_str(&format!(
            "\n(+{} more entries not listed)",
            total - MAX_DIR_ENTRIES
        ));
    }
    Ok((text, truncated))
}

/// Cut on a character boundary; a multi-byte character is never split.
fn truncate(text: &str, limit: usize) -> (String, bool) {
    if text.len() <= limit {
        return (text.to_string(), false);
    }
    let mut end = limit;
    while end > 0 && !text.is_char_boundary(end) {
        end -= 1;
    }
    (text[..end].to_string(), true)
}

fn canonical_dir(path: &str) -> Result<PathBuf> {
    let trimmed = path.trim();
    if trimmed.is_empty() {
        return Err(Error::Rejected(
            "a session without a project directory cannot take context references".into(),
        ));
    }
    std::fs::canonicalize(trimmed)
        .map_err(|error| Error::NotFound(format!("project directory {trimmed}: {error}")))
}

fn resolve_path(root: &Path, reference: &str) -> Result<PathBuf> {
    let candidate = Path::new(reference);
    let joined = if candidate.is_absolute() {
        candidate.to_path_buf()
    } else {
        root.join(candidate)
    };
    std::fs::canonicalize(&joined)
        .map_err(|error| Error::NotFound(format!("{}: {error}", joined.display())))
}

/// Whether `path` is inside `root`, after both were canonicalised.
///
/// Windows paths compare case-insensitively; on every platform the comparison is
/// by component so `D:\work\anvil` does not contain `D:\work\anvil-other`.
fn is_within(root: &Path, path: &Path) -> bool {
    let Ok(root) = std::fs::canonicalize(root) else {
        return false;
    };
    let root_parts: Vec<String> = root
        .components()
        .map(|part| part.as_os_str().to_string_lossy().to_lowercase())
        .collect();
    let path_parts: Vec<String> = path
        .components()
        .map(|part| part.as_os_str().to_string_lossy().to_lowercase())
        .collect();
    path_parts.len() >= root_parts.len() && path_parts[..root_parts.len()] == root_parts[..]
}

fn same_path(left: &Path, right: &Path) -> bool {
    left.to_string_lossy().to_lowercase() == right.to_string_lossy().to_lowercase()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    fn scratch(name: &str) -> PathBuf {
        let root = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("context")
            .join(name);
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(root.join("src")).expect("create scratch");
        std::fs::write(root.join("src/main.rs"), "fn main() {}\n").expect("write");
        std::fs::write(root.join("README.md"), "# demo\n").expect("write");
        std::fs::create_dir_all(root.join("src/nested")).expect("create");
        root
    }

    fn file(path: &str) -> ContextRef {
        ContextRef {
            kind: ContextKind::File,
            path: path.to_string(),
            label: None,
        }
    }

    #[test]
    fn a_relative_reference_is_read_inside_the_project() {
        let root = scratch("relative");
        let steps = plan(&root.to_string_lossy(), &[], &[file("src/main.rs")]).expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(!prepared.resolved.outside_root);
                assert!(prepared.block.contains("fn main"));
                assert_eq!(prepared.resolved.chars, "fn main() {}\n".chars().count());
                assert!(!prepared.resolved.truncated);
            }
            Step::Awaiting { .. } => panic!("inside the project must not ask"),
        }
    }

    #[test]
    fn a_parent_escape_is_not_read_without_consent() {
        let root = scratch("escape");
        let outside = root.parent().expect("parent").join("secret.txt");
        std::fs::write(&outside, "top-secret").expect("write");
        let steps = plan(&root.to_string_lossy(), &[], &[file("../secret.txt")]).expect("plan");
        match &steps[0] {
            Step::Awaiting { path, .. } => {
                assert!(path.ends_with("secret.txt"));
            }
            Step::Ready(_) => panic!("a path outside the project must need approval"),
        }
        let _ = std::fs::remove_file(&outside);
    }

    #[test]
    fn duplicate_references_collapse_to_one_block() {
        let root = scratch("dupe");
        let steps = plan(
            &root.to_string_lossy(),
            &[],
            &[file("src/main.rs"), file("src/./main.rs")],
        )
        .expect("plan");
        assert_eq!(steps.len(), 1);
    }

    #[test]
    fn a_binary_file_is_refused_instead_of_mangled() {
        let root = scratch("binary");
        let mut handle = std::fs::File::create(root.join("blob.bin")).expect("create");
        handle.write_all(&[0xff, 0xfe, 0x00, 0x01]).expect("write");
        let error = plan(&root.to_string_lossy(), &[], &[file("blob.bin")]).expect_err("binary");
        assert_eq!(error.code(), "rejected");
    }

    #[test]
    fn a_missing_reference_is_not_found_not_an_empty_block() {
        let root = scratch("missing");
        let error = plan(&root.to_string_lossy(), &[], &[file("nope.rs")]).expect_err("missing");
        assert_eq!(error.code(), "not_found");
    }

    #[test]
    fn a_directory_lists_names_and_says_when_it_was_cut() {
        let root = scratch("listing");
        for index in 0..(MAX_DIR_ENTRIES + 3) {
            std::fs::write(root.join(format!("src/f{index}.rs")), "x").expect("write");
        }
        let steps = plan(
            &root.to_string_lossy(),
            &[],
            &[ContextRef {
                kind: ContextKind::Dir,
                path: "src".into(),
                label: None,
            }],
        )
        .expect("plan");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert!(prepared.resolved.truncated);
                assert!(prepared.block.contains("more entries not listed"));
            }
            Step::Awaiting { .. } => panic!("inside the project"),
        }
    }

    #[test]
    fn a_skill_outside_the_configured_roots_is_refused() {
        let root = scratch("skill");
        let skill = ContextRef {
            kind: ContextKind::Skill,
            path: "README.md".into(),
            label: Some("demo".into()),
        };
        let error =
            plan(&root.to_string_lossy(), &[], std::slice::from_ref(&skill)).expect_err("no roots");
        assert_eq!(error.code(), "rejected");
        let steps = plan(
            &root.to_string_lossy(),
            std::slice::from_ref(&root),
            &[skill],
        )
        .expect("with a root");
        match &steps[0] {
            Step::Ready(prepared) => {
                assert_eq!(prepared.resolved.kind, ContextKind::Skill);
                assert!(prepared.block.starts_with("--- skill demo ("));
            }
            Step::Awaiting { .. } => panic!("a skill root covers it"),
        }
    }

    #[test]
    fn folding_keeps_the_user_words_last() {
        let root = scratch("fold");
        let steps = plan(&root.to_string_lossy(), &[], &[file("README.md")]).expect("plan");
        let prepared: Vec<Prepared> = steps
            .into_iter()
            .filter_map(|step| match step {
                Step::Ready(prepared) => Some(*prepared),
                Step::Awaiting { .. } => None,
            })
            .collect();
        let prompt = fold("what is this?", &prepared);
        assert!(prompt.starts_with("The user attached the following context."));
        assert!(prompt.ends_with("what is this?"));
    }

    #[test]
    fn the_at_list_is_bounded_and_skips_vendor_directories() {
        let root = scratch("entries");
        std::fs::create_dir_all(root.join("node_modules/pkg")).expect("create");
        std::fs::write(root.join("node_modules/pkg/index.js"), "x").expect("write");
        let listed = entries(&root.to_string_lossy(), "", 20).expect("entries");
        assert!(
            listed
                .iter()
                .all(|entry| !entry.relative.contains("node_modules"))
        );
        assert!(listed.iter().any(|entry| entry.relative == "src/main.rs"));
        let narrowed = entries(&root.to_string_lossy(), "main", 5).expect("entries");
        assert!(narrowed.iter().all(|entry| entry.relative.contains("main")));
    }
}
