//! Installing and authoring skills.
//!
//! The skill list itself is the catalog's inventory: it scans the runtimes' own
//! directories, so "installed" means "on disk where the runtime looks" rather
//! than "marked installed in a table". These commands therefore do the only two
//! things that make sense — write a directory into that root (import), or write a
//! new `SKILL.md` there (create) — and then re-scan, so the page shows the file
//! that exists a second later.
//!
//! Copies never follow a symlink or junction and never overwrite an existing
//! skill; a name that is already taken gets a `-2` suffix, the same rule the
//! library import uses.

use std::path::{Path, PathBuf};

use tauri::State;

use crate::catalog::CatalogState;

/// The directory the pi runtime reads.
///
/// With the bundled runtime that is `<dataDir>/skill` (ADR 0009); a development
/// host falls back to the adapter's own rule for an external install. The same
/// function the catalogue scans with, so a created skill is discovered without
/// any bookkeeping.
fn skills_root() -> PathBuf {
    if let Some(root) = anvil_catalog::runtime::bundled::skill_root() {
        return root;
    }
    let home = anvil_catalog::runtime::adapters::common::home();
    anvil_catalog::runtime::adapters::pi::skills_root(&home)
}

/// A folder name that is safe on every filesystem and readable in a list.
fn slug(text: &str) -> String {
    let mut slug = String::new();
    for character in text.trim().chars() {
        if character.is_alphanumeric() {
            for lower in character.to_lowercase() {
                slug.push(lower);
            }
        } else if character.is_whitespace() || matches!(character, '-' | '_' | '·' | '/' | '\\') {
            if !slug.ends_with('-') && !slug.is_empty() {
                slug.push('-');
            }
        }
    }
    let slug = slug.trim_matches('-').to_string();
    if slug.is_empty() {
        "skill".to_string()
    } else {
        slug.chars().take(48).collect()
    }
}

fn unique_dir(root: &Path, base: &str) -> PathBuf {
    let candidate = root.join(base);
    if !candidate.exists() {
        return candidate;
    }
    for index in 2..1000 {
        let next = root.join(format!("{base}-{index}"));
        if !next.exists() {
            return next;
        }
    }
    candidate
}

/// Make sure pi is told about the skill root before the page rescans.
///
/// A skill written into a directory pi does not load would show up in the
/// catalogue and never reach a conversation. Startup registers the root too;
/// this covers the case where the user removed the entry (or the file) since.
fn register_root() {
    if let Some(bundle) = anvil_catalog::runtime::bundled::active()
        && let Err(error) = anvil_catalog::runtime::bundled::ensure_skill_root(&bundle)
    {
        eprintln!("[skills] could not register the skill root: {error}");
    }
}

/// Rescan just the skill inventory so the page reflects the disk now.
fn rescan(state: &State<'_, CatalogState>) -> Result<(), String> {
    register_root();
    state.with_service(|service| {
        service
            .sync_inventory()
            .map(|_| ())
            .map_err(|error| error.to_wire())
    })
}

/// Create a skill from the builder: name, one-line description, and the body.
#[tauri::command]
pub async fn skill_create(
    state: State<'_, CatalogState>,
    name: String,
    description: String,
    body: String,
) -> Result<String, String> {
    let name = name.trim().to_string();
    if name.is_empty() {
        return Err("skill: a name is required".to_string());
    }
    if name.contains('\n') {
        return Err("skill: the name must be one line".to_string());
    }
    let root = skills_root();
    std::fs::create_dir_all(&root).map_err(|error| format!("skill: {error}"))?;
    let directory = unique_dir(&root, &slug(&name));
    std::fs::create_dir_all(&directory).map_err(|error| format!("skill: {error}"))?;
    let description = description.trim().replace('\n', " ");
    let mut manifest = String::from("---\n");
    manifest.push_str(&format!("name: {name}\n"));
    if !description.is_empty() {
        manifest.push_str(&format!("description: {description}\n"));
    }
    manifest.push_str("---\n\n");
    let body = body.trim();
    if !body.is_empty() {
        manifest.push_str(body);
        manifest.push('\n');
    }
    let path = directory.join("SKILL.md");
    std::fs::write(&path, manifest).map_err(|error| format!("skill: {error}"))?;
    rescan(&state)?;
    Ok(crate::workspace::display_path(&directory))
}

/// Copy a skill folder the user picked into the runtime's skill root.
#[tauri::command]
pub async fn skill_import(state: State<'_, CatalogState>, path: String) -> Result<String, String> {
    let source = PathBuf::from(&path);
    if !source.is_dir() {
        return Err("skill: pick a folder that contains SKILL.md".to_string());
    }
    if !source.join("SKILL.md").is_file() {
        return Err("skill: that folder has no SKILL.md".to_string());
    }
    let root = skills_root();
    std::fs::create_dir_all(&root).map_err(|error| format!("skill: {error}"))?;
    let base = source
        .file_name()
        .map(|value| value.to_string_lossy().into_owned())
        .unwrap_or_else(|| "skill".to_string());
    let target = unique_dir(&root, &slug(&base));
    copy_tree(&source, &target, 0)?;
    rescan(&state)?;
    Ok(crate::workspace::display_path(&target))
}

/// Remove one installed skill, but only from inside the runtime's own root.
#[tauri::command]
pub async fn skill_remove(state: State<'_, CatalogState>, path: String) -> Result<(), String> {
    let root = std::fs::canonicalize(skills_root()).map_err(|error| format!("skill: {error}"))?;
    let target = std::fs::canonicalize(&path).map_err(|error| format!("skill: {error}"))?;
    if !target.starts_with(&root) || target == root {
        return Err("skill: that path is outside the skill root".to_string());
    }
    std::fs::remove_dir_all(&target).map_err(|error| format!("skill: {error}"))?;
    rescan(&state)?;
    Ok(())
}

/// Recursive copy without following links; depth-capped so a self-referencing
/// tree cannot spin.
fn copy_tree(source: &Path, target: &Path, depth: usize) -> Result<(), String> {
    if depth > 12 {
        return Err("skill: the folder is nested too deeply".to_string());
    }
    std::fs::create_dir_all(target).map_err(|error| format!("skill: {error}"))?;
    let entries = std::fs::read_dir(source).map_err(|error| format!("skill: {error}"))?;
    for entry in entries.flatten() {
        let from = entry.path();
        let metadata =
            std::fs::symlink_metadata(&from).map_err(|error| format!("skill: {error}"))?;
        if metadata.file_type().is_symlink() || is_reparse_point(&metadata) {
            continue;
        }
        let name = match from.file_name() {
            Some(name) => name.to_os_string(),
            None => continue,
        };
        let to = target.join(name);
        if metadata.is_dir() {
            copy_tree(&from, &to, depth + 1)?;
        } else if metadata.is_file() {
            std::fs::copy(&from, &to).map_err(|error| format!("skill: {error}"))?;
        }
    }
    Ok(())
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

#[cfg(test)]
mod tests {
    use super::slug;

    #[test]
    fn slugs_keep_it_readable_and_safe() {
        assert_eq!(slug("每周竞品巡检"), "每周竞品巡检");
        assert_eq!(slug("Weekly Pricing Watch"), "weekly-pricing-watch");
        assert_eq!(slug("  report / writer  "), "report-writer");
        assert_eq!(slug("a__b"), "a-b");
        assert_eq!(slug(""), "skill");
    }
}
