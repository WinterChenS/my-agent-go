//! The resource library: every file a session actually wrote, plus imports.
//!
//! The index is not a table. A file that a turn produced lives in that session's
//! project directory, and the truth about it is the file itself — an index row
//! would only go stale when the user renames or deletes it outside the app. So
//! this reads the same `rail::artifact_files` the chat's right rail uses (same
//! confinement, same scratch exclusions), deduplicates by canonical path and
//! merges `<data dir>/library`, where uploads are copied.
//!
//! Nothing is invented: a size is the file's size, a date is its mtime, and the
//! session a file belongs to is the session whose tool calls named it.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::time::UNIX_EPOCH;

use anvil_session::{EventKind, RunEvent};
use rusqlite::params;
use serde::Serialize;
use tauri::State;

use crate::session::SessionState;

/// Most sessions scanned in one call. The page sorts by recency anyway, and a
/// library that took ten seconds to open would not be used.
const MAX_SESSIONS: usize = 200;
/// Tool calls examined per session.
const MAX_CALLS: usize = 120;

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LibraryAsset {
    pub id: String,
    pub name: String,
    pub path: String,
    pub relative: String,
    pub ext: String,
    /// `doc` | `sheet` | `page` | `chart` | `code` | `other`.
    pub kind: String,
    pub bytes: u64,
    pub session: String,
    pub session_id: String,
    /// RFC 3339, from the file's modification time.
    pub at: String,
    /// `session` (a turn wrote it) or `library` (imported).
    pub source: String,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LibraryStats {
    pub assets: u64,
    pub bytes: u64,
    pub week_added: u64,
    /// Distinct sessions that produced at least one file.
    pub sessions: u64,
    pub imported: u64,
    pub by_kind: BTreeMap<String, u64>,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LibraryView {
    pub assets: Vec<LibraryAsset>,
    pub stats: LibraryStats,
    /// Where uploads are copied, shown in the dialog.
    pub root: String,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ImportReport {
    pub imported: Vec<LibraryAsset>,
    pub skipped: Vec<String>,
    pub root: String,
}

#[tauri::command]
pub async fn library_overview(state: State<'_, SessionState>) -> Result<LibraryView, String> {
    let root = library_root(&state);
    state.with_reader(|store| {
        let connection = store.raw();
        let mut found: BTreeMap<String, LibraryAsset> = BTreeMap::new();
        let sessions = sessions_of(connection)?;
        for (session_id, title, project_path) in &sessions {
            if project_path.trim().is_empty() {
                continue;
            }
            let events = tool_calls(connection, session_id)?;
            for artifact in anvil_session::rail::artifact_files(project_path, &events) {
                let path = PathBuf::from(&artifact.path);
                let stamp = file_stamp(&path);
                let key = artifact.path.to_lowercase();
                let row = LibraryAsset {
                    id: key.clone(),
                    name: file_name(&path, &artifact.relative),
                    path: artifact.path.clone(),
                    relative: artifact.relative.clone(),
                    ext: artifact.ext.to_lowercase(),
                    kind: kind_of(&artifact.ext).to_string(),
                    bytes: artifact.bytes,
                    session: title.clone(),
                    session_id: session_id.clone(),
                    at: stamp,
                    source: "session".to_string(),
                };
                match found.get(&key) {
                    Some(previous) if previous.at >= row.at => {}
                    _ => {
                        found.insert(key, row);
                    }
                }
            }
        }
        for row in scan_library_dir(&root) {
            found.entry(row.id.clone()).or_insert(row);
        }
        Ok(LibraryView {
            stats: stats_of(&found),
            assets: sort_recent(found.into_values().collect()),
            root: crate::workspace::display_path(&root),
        })
    })
}

/// Copy picked files into the library directory.
///
/// The page runs the native multi-select itself (the composer already has that
/// command), so this takes paths: one code path for "import from the dialog" and
/// "import this path" from a test.
#[tauri::command]
pub async fn library_import(
    state: State<'_, SessionState>,
    paths: Vec<String>,
) -> Result<ImportReport, String> {
    let root = library_root(&state);
    std::fs::create_dir_all(&root).map_err(|error| format!("library: {error}"))?;
    let mut imported = Vec::new();
    let mut skipped = Vec::new();
    for raw in paths {
        let source = PathBuf::from(&raw);
        if !source.is_file() {
            skipped.push(raw);
            continue;
        }
        let name = source
            .file_name()
            .map(|value| value.to_string_lossy().into_owned())
            .unwrap_or_else(|| "file".to_string());
        let target = unique_target(&root, &name);
        match std::fs::copy(&source, &target) {
            Ok(_) => imported.push(imported_asset(&target)),
            Err(error) => {
                eprintln!("anvil: library: copy {}: {error}", source.display());
                skipped.push(raw);
            }
        }
    }
    Ok(ImportReport {
        root: crate::workspace::display_path(&root),
        imported,
        skipped,
    })
}

/// Copy the selection into a dated folder under `exports`, returning that folder.
#[tauri::command]
pub async fn library_export(
    state: State<'_, SessionState>,
    paths: Vec<String>,
) -> Result<String, String> {
    let stamp = anvil_session::clock::now_rfc3339()
        .replace([':', '-'], "")
        .replace('T', "-");
    let directory = library_root(&state)
        .parent()
        .map(|dir| dir.join("exports"))
        .unwrap_or_else(|| PathBuf::from("exports"))
        .join(format!("library-{stamp}"));
    std::fs::create_dir_all(&directory).map_err(|error| format!("library: {error}"))?;
    let mut copied = 0usize;
    for raw in paths {
        let source = PathBuf::from(&raw);
        if !source.is_file() {
            continue;
        }
        let name = source
            .file_name()
            .map(|value| value.to_string_lossy().into_owned())
            .unwrap_or_else(|| "file".to_string());
        let target = unique_target(&directory, &name);
        if std::fs::copy(&source, &target).is_ok() {
            copied += 1;
        }
    }
    if copied == 0 {
        return Err("library: nothing to export".to_string());
    }
    Ok(crate::workspace::display_path(&directory))
}

/// First slice of a text file, for the detail dialog.
///
/// Only paths inside a project or the library root are readable: the command is
/// reachable from the window, and a preview must not become a file-read gadget.
#[tauri::command]
pub async fn library_preview(
    state: State<'_, SessionState>,
    path: String,
) -> Result<String, String> {
    let root = library_root(&state);
    let allowed = state.with_reader(|store| {
        let sessions = sessions_of(store.raw())?;
        Ok::<_, String>(
            sessions
                .into_iter()
                .map(|(_, _, dir)| dir)
                .collect::<Vec<_>>(),
        )
    })?;
    let canonical = std::fs::canonicalize(&path).map_err(|error| format!("library: {error}"))?;
    let inside = canonical.starts_with(canonical_or(&root))
        || allowed
            .iter()
            .filter(|dir| !dir.trim().is_empty())
            .any(|dir| canonical.starts_with(canonical_or(Path::new(dir))));
    if !inside {
        return Err("library: that path is outside the library".to_string());
    }
    let ext = canonical
        .extension()
        .map(|value| value.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    if !is_text(&ext) {
        return Ok(String::new());
    }
    let bytes = std::fs::read(&canonical).map_err(|error| format!("library: {error}"))?;
    let slice = &bytes[..bytes.len().min(120 * 1024)];
    // A preview is read by people: decode UTF-8 (lossy at a cut boundary) so a
    // Chinese report is text, not a row of replacement glyphs.
    Ok(String::from_utf8_lossy(slice).into_owned())
}

/// Show a file in the system file manager.
#[tauri::command]
pub async fn library_reveal(path: String) -> Result<(), String> {
    let target = PathBuf::from(&path);
    if !target.exists() {
        return Err("library: that file no longer exists".to_string());
    }
    #[cfg(windows)]
    {
        std::process::Command::new("explorer")
            .arg(format!("/select,{}", target.display()))
            .spawn()
            .map_err(|error| format!("library: {error}"))?;
    }
    #[cfg(not(windows))]
    {
        let parent = target.parent().unwrap_or(Path::new("."));
        std::process::Command::new("open")
            .arg(parent)
            .spawn()
            .map_err(|error| format!("library: {error}"))?;
    }
    Ok(())
}

// ---- helpers --------------------------------------------------------------

fn library_root(state: &SessionState) -> PathBuf {
    state
        .database()
        .parent()
        .map(|dir| dir.join("library"))
        .unwrap_or_else(|| PathBuf::from("library"))
}

fn canonical_or(path: &Path) -> PathBuf {
    std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
}

fn sessions_of(connection: &rusqlite::Connection) -> Result<Vec<(String, String, String)>, String> {
    let mut statement = connection
        .prepare(
            "SELECT id, title, project_path FROM sessions
             WHERE archived = 0 ORDER BY updated_at DESC LIMIT ?1",
        )
        .map_err(|error| error.to_string())?;
    let rows = statement
        .query_map(params![MAX_SESSIONS as i64], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
            ))
        })
        .map_err(|error| error.to_string())?;
    rows.collect::<rusqlite::Result<Vec<_>>>()
        .map_err(|error| error.to_string())
}

fn tool_calls(
    connection: &rusqlite::Connection,
    session_id: &str,
) -> Result<Vec<RunEvent>, String> {
    let mut statement = connection
        .prepare(
            "SELECT run_id, seq, at, payload FROM events
             WHERE session_id = ?1 AND kind = 'tool_call'
             ORDER BY seq DESC LIMIT ?2",
        )
        .map_err(|error| error.to_string())?;
    let rows = statement
        .query_map(params![session_id, MAX_CALLS as i64], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, i64>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, String>(3)?,
            ))
        })
        .map_err(|error| error.to_string())?;
    let collected = rows
        .collect::<rusqlite::Result<Vec<_>>>()
        .map_err(|error| error.to_string())?;
    Ok(collected
        .into_iter()
        .map(|(run_id, seq, at, payload)| RunEvent {
            session_id: session_id.to_string(),
            run_id,
            seq,
            kind: EventKind::ToolCall,
            at,
            payload: serde_json::from_str(&payload).unwrap_or_default(),
        })
        .collect())
}

fn file_name(path: &Path, fallback: &str) -> String {
    path.file_name()
        .map(|value| value.to_string_lossy().into_owned())
        .unwrap_or_else(|| fallback.to_string())
}

fn file_stamp(path: &Path) -> String {
    std::fs::metadata(path)
        .ok()
        .and_then(|meta| meta.modified().ok())
        .and_then(|time| time.duration_since(UNIX_EPOCH).ok())
        .map(|duration| {
            anvil_session::clock::rfc3339(i64::try_from(duration.as_secs()).unwrap_or(i64::MAX))
        })
        .unwrap_or_default()
}

fn kind_of(ext: &str) -> &'static str {
    match ext.to_ascii_lowercase().as_str() {
        "md" | "markdown" | "txt" | "doc" | "docx" | "pdf" | "rtf" => "doc",
        "csv" | "tsv" | "xls" | "xlsx" | "xlsm" => "sheet",
        "html" | "htm" => "page",
        "png" | "jpg" | "jpeg" | "gif" | "webp" | "bmp" | "svg" => "chart",
        "ts" | "tsx" | "js" | "jsx" | "mjs" | "py" | "rs" | "go" | "java" | "json" | "yaml"
        | "yml" | "toml" | "sh" | "css" | "sql" | "c" | "h" | "cpp" => "code",
        _ => "other",
    }
}

fn is_text(ext: &str) -> bool {
    matches!(kind_of(ext), "doc" | "sheet" | "page" | "code")
        && !matches!(ext.to_ascii_lowercase().as_str(), "pdf" | "doc" | "docx")
}

fn sort_recent(mut rows: Vec<LibraryAsset>) -> Vec<LibraryAsset> {
    rows.sort_by(|left, right| {
        right
            .at
            .cmp(&left.at)
            .then_with(|| left.name.to_lowercase().cmp(&right.name.to_lowercase()))
    });
    rows
}

fn stats_of(rows: &BTreeMap<String, LibraryAsset>) -> LibraryStats {
    let cutoff = anvil_session::clock::rfc3339(anvil_session::clock::unix_now() - 7 * 86_400);
    let mut stats = LibraryStats::default();
    let mut sessions = std::collections::BTreeSet::new();
    for row in rows.values() {
        stats.assets += 1;
        stats.bytes += row.bytes;
        if !row.at.is_empty() && row.at >= cutoff {
            stats.week_added += 1;
        }
        if !row.session_id.is_empty() {
            sessions.insert(row.session_id.clone());
        }
        if row.source == "library" {
            stats.imported += 1;
        }
        *stats.by_kind.entry(row.kind.clone()).or_insert(0) += 1;
    }
    stats.sessions = sessions.len() as u64;
    stats
}

/// Every file under the library root, oldest directory scan kept shallow on
/// purpose: uploads are files, not trees.
fn scan_library_dir(root: &Path) -> Vec<LibraryAsset> {
    let mut rows = Vec::new();
    let Ok(entries) = std::fs::read_dir(root) else {
        return rows;
    };
    for entry in entries.flatten().take(500) {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }
        rows.push(imported_asset(&path));
    }
    rows
}

fn imported_asset(path: &Path) -> LibraryAsset {
    let ext = path
        .extension()
        .map(|value| value.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    let name = path
        .file_name()
        .map(|value| value.to_string_lossy().into_owned())
        .unwrap_or_default();
    LibraryAsset {
        id: crate::workspace::display_path(path).to_lowercase(),
        name: name.clone(),
        path: crate::workspace::display_path(path),
        relative: name,
        kind: kind_of(&ext).to_string(),
        ext,
        bytes: std::fs::metadata(path).map(|meta| meta.len()).unwrap_or(0),
        session: String::new(),
        session_id: String::new(),
        at: file_stamp(path),
        source: "library".to_string(),
    }
}

/// `name.ext`, then `name-2.ext`, … so an import never overwrites a file.
fn unique_target(directory: &Path, name: &str) -> PathBuf {
    let candidate = directory.join(name);
    if !candidate.exists() {
        return candidate;
    }
    let path = Path::new(name);
    let stem = path
        .file_stem()
        .map(|value| value.to_string_lossy().into_owned())
        .unwrap_or_else(|| name.to_string());
    let ext = path
        .extension()
        .map(|value| value.to_string_lossy().into_owned());
    for index in 2..1000 {
        let next = match &ext {
            Some(ext) => directory.join(format!("{stem}-{index}.{ext}")),
            None => directory.join(format!("{stem}-{index}")),
        };
        if !next.exists() {
            return next;
        }
    }
    candidate
}
