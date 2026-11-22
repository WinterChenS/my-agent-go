//! Where a conversation runs.
//!
//! Every session is bound to one directory at creation time (the ledger's
//! `project_path`), and the history panel groups conversations by it. Two
//! conventions make that useful instead of accidental:
//!
//! - The host picks a real home for sessions that name no directory. It used to
//!   fall back to the process working directory, which for the installed app is
//!   the unpacked bundle — so conversations silently grouped under
//!   "anvil-win-x64-20261116-v12" and whatever a tool wrote landed inside the
//!   program folder.
//! - Projects created from the app live under one root, so "default workspace"
//!   and "my projects" are both discoverable instead of being scattered.
//!
//! The app never creates a directory the user did not ask for except this one
//! root: `<data dir>/workspace` and its `Projects` subfolder — both under
//! `%USERPROFILE%\.anvil`, so everything the app owns lives in one place.

use std::path::{Path, PathBuf};

use serde::Serialize;

/// Root the app writes into: `<data dir>/workspace` (`%USERPROFILE%\.anvil\workspace`).
///
/// The default used to be `<home>/Anvil`, a second location next to the app's
/// data directory. Keeping the workspace inside `.anvil` means one place to find
/// (or delete) everything the app created.
pub fn default_workspace() -> PathBuf {
    crate::host::default_data_dir().join("workspace")
}

/// Where projects created from the app are placed: `<workspace>/Projects`.
pub fn projects_root() -> PathBuf {
    default_workspace().join("Projects")
}


/// Render a path the way the user expects to see it.
///
/// On Windows `canonicalize` returns extended-length paths such as
/// `\\?\C:\Users\me\Anvil`. Those are fine for the OS but wrong in two
/// places that matter here: shown literally in the project dialog, and stored
/// as a session's group key. If the default path were stored with the prefix in
/// one place and without it in another, the same folder would appear as two
/// groups in the history panel. Strip the prefix at the boundary so the UI and
/// the ledger agree on one spelling. UNC shares keep their double slash.
pub fn display_path(path: &Path) -> String {
    let text = path.display().to_string();
    if let Some(rest) = text.strip_prefix(r"\\?\UNC\") {
        format!(r"\\{rest}")
    } else if let Some(rest) = text.strip_prefix(r"\\?\") {
        rest.to_string()
    } else {
        text
    }
}

/// The two paths the UI needs to explain itself.
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct WorkspaceDefaults {
    pub default_workspace: String,
    pub projects_root: String,
}

/// Create `dir` (and parents) if missing, then hand it back.
fn ensure_dir(dir: &Path) -> Result<PathBuf, String> {
    std::fs::create_dir_all(dir)
        .map_err(|error| format!("cannot create {}: {error}", dir.display()))?;
    Ok(std::fs::canonicalize(dir).unwrap_or_else(|_| dir.to_path_buf()))
}

/// The default workspace and the projects root, both created on demand.
#[tauri::command]
pub fn workspace_defaults() -> Result<WorkspaceDefaults, String> {
    let workspace = ensure_dir(&default_workspace())?;
    let projects = ensure_dir(&projects_root())?;
    Ok(WorkspaceDefaults {
        default_workspace: display_path(&workspace),
        projects_root: display_path(&projects),
    })
}

/// Validate a directory the user picked or typed.
///
/// Returns the canonical path so two spellings of the same folder (`D:\work`
/// and `d:/work`) become one group in the history panel.
#[tauri::command]
pub fn workspace_ensure(path: String) -> Result<String, String> {
    let candidate = Path::new(path.trim());
    if path.trim().is_empty() {
        return Err("the path is empty".to_string());
    }
    let metadata = std::fs::metadata(candidate)
        .map_err(|error| format!("{} is not reachable: {error}", candidate.display()))?;
    if !metadata.is_dir() {
        return Err(format!("{} is a file, not a folder", candidate.display()));
    }
    let canonical = std::fs::canonicalize(candidate)
        .map_err(|error| format!("{} is not usable: {error}", candidate.display()))?;
    Ok(display_path(&canonical))
}

/// Create a project folder under the projects root.
///
/// `name` is one path component: separators are rejected rather than sanitised,
/// because silently turning `../etc` into a folder somewhere else is worse than
/// refusing it.
#[tauri::command]
pub fn workspace_create(name: String) -> Result<String, String> {
    let name = name.trim();
    if name.is_empty() {
        return Err("the project needs a name".to_string());
    }
    if name.contains('/')
        || name.contains('\\')
        || name == "."
        || name == ".."
        || name.contains(':')
    {
        return Err(format!("{name:?} is not a folder name"));
    }
    let root = ensure_dir(&projects_root())?;
    let target = root.join(name);
    if target.exists() {
        // Reopening an existing project is what a person means by picking the
        // same name again; only refuse when the path is not a folder.
        if target.is_dir() {
            return workspace_ensure(target.display().to_string());
        }
        return Err(format!(
            "{} already exists and is not a folder",
            target.display()
        ));
    }
    let created = ensure_dir(&target)?;
    Ok(display_path(&created))
}

/// Which shell dialog to show.
#[derive(Clone, Copy)]
enum PickKind {
    Folder,
    File,
    Files,
}

/// Ask Windows for a folder.
///
/// The app has no dialog plugin, and the shell's own folder picker needs no
/// extra dependency: `SHBrowseForFolderW` is part of shell32, which the host
/// already links. `None` means the user cancelled.
///
/// Both pickers run on the *window's* thread, via `run_on_main_thread`. A Tauri
/// command executes on a runtime worker that never pumps a Windows message
/// queue; a modal shell dialog created there is either invisible or returns
/// immediately, which is exactly what the app log showed (`workspace: browse`
/// with nothing after it). The main thread owns the webview's message pump, and
/// the modal loop nested inside it still dispatches messages, so the window
/// keeps painting while the dialog is up.
#[tauri::command]
pub async fn workspace_pick(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
) -> Result<Option<String>, String> {
    #[cfg(windows)]
    let owner = window.hwnd().ok().map(|handle| handle.0 as isize);
    #[cfg(not(windows))]
    let owner = {
        let _ = &window;
        None
    };
    log_pick(&app, "folder: opening");
    let outcome = pick_on_main_thread(&app, owner, PickKind::Folder);
    log_pick(&app, &format!("folder: {outcome:?}"));
    outcome.map(|picked| picked.and_then(|paths| paths.into_iter().next()))
}

/// Ask Windows for a *file*. A workspace is a directory, so the file's parent
/// becomes the selection; the picker exists because people describe a project
/// by one of its files ("open the folder that has this spec in it").
#[tauri::command]
pub async fn workspace_pick_file(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
) -> Result<Option<String>, String> {
    #[cfg(windows)]
    let owner = window.hwnd().ok().map(|handle| handle.0 as isize);
    #[cfg(not(windows))]
    let owner = {
        let _ = &window;
        None
    };
    log_pick(&app, "file: opening");
    let outcome = pick_on_main_thread(&app, owner, PickKind::File);
    log_pick(&app, &format!("file: {outcome:?}"));
    outcome.map(|picked| picked.and_then(|paths| paths.into_iter().next()))
}

/// Ask Windows for one or more *files* to attach as context.
///
/// This is the composer's "文件" entry, not the project picker: several files
/// are the normal case for a turn ("these three configs"), and selecting them
/// one dialog at a time is the reason the old control felt broken.
#[tauri::command]
pub async fn workspace_pick_files(
    app: tauri::AppHandle,
    window: tauri::WebviewWindow,
) -> Result<Vec<String>, String> {
    #[cfg(windows)]
    let owner = window.hwnd().ok().map(|handle| handle.0 as isize);
    #[cfg(not(windows))]
    let owner = {
        let _ = &window;
        None
    };
    log_pick(&app, "files: opening");
    let outcome = pick_on_main_thread(&app, owner, PickKind::Files);
    log_pick(&app, &format!("files: {outcome:?}"));
    Ok(outcome?.unwrap_or_default())
}

/// Records one picker step in the host log, so "the button did nothing" can be
/// told apart from "the dialog opened and was cancelled".
fn log_pick(app: &tauri::AppHandle, message: &str) {
    use tauri::Manager;
    if let Some(state) = app.try_state::<crate::state::HostState>() {
        state.log_event(&format!("workspace: pick {message}"));
    }
}

/// Runs one blocking picker on the thread that owns the window message pump and
/// waits for its answer here. Blocking this runtime worker is intended: a modal
/// dialog is up, and only one can exist at a time.
fn pick_on_main_thread(
    app: &tauri::AppHandle,
    owner: Option<isize>,
    kind: PickKind,
) -> Result<Option<Vec<String>>, String> {
    let (sender, receiver) = std::sync::mpsc::channel();
    app.run_on_main_thread(move || {
        let outcome = match kind {
            PickKind::Folder => pick_folder(owner).map(|one| one.map(|path| vec![path])),
            PickKind::File => pick_file(owner).map(|one| one.map(|path| vec![path])),
            PickKind::Files => pick_files(owner),
        };
        let _ = sender.send(outcome);
    })
    .map_err(|error| format!("cannot reach the window thread: {error}"))?;
    // A person may leave the dialog open for a while; only a truly stuck thread
    // should be reported, and the message says so.
    receiver
        .recv_timeout(std::time::Duration::from_secs(900))
        .map_err(|_| "the picker thread did not answer".to_string())?
}

#[cfg(windows)]
fn pick_folder(owner: Option<isize>) -> Result<Option<String>, String> {
    use windows_sys::Win32::System::Com::{
        COINIT_APARTMENTTHREADED, COINIT_DISABLE_OLE1DDE, CoInitializeEx, CoTaskMemFree,
    };
    use windows_sys::Win32::UI::Shell::{
        BIF_EDITBOX, BIF_NEWDIALOGSTYLE, BIF_RETURNONLYFSDIRS, BROWSEINFOW, SHBrowseForFolderW,
        SHGetPathFromIDListW,
    };

    let title: Vec<u16> = "选择项目目录\0".encode_utf16().collect();
    let info = BROWSEINFOW {
        hwndOwner: owner.unwrap_or(0) as *mut core::ffi::c_void,
        lpszTitle: title.as_ptr(),
        ulFlags: BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE | BIF_EDITBOX,
        ..Default::default()
    };

    // SAFETY: every pointer handed to the shell outlives the call; the item id
    // list the shell allocates is freed with the allocator it documents.
    unsafe {
        // Picking a folder uses shell UI, which wants COM on the calling thread.
        // A second initialisation on the same thread is harmless (S_FALSE).
        let _ = CoInitializeEx(
            std::ptr::null(),
            (COINIT_APARTMENTTHREADED | COINIT_DISABLE_OLE1DDE) as u32,
        );
        let list = SHBrowseForFolderW(&info);
        if list.is_null() {
            return Ok(None);
        }
        let mut buffer = [0u16; 1024];
        let ok = SHGetPathFromIDListW(list, buffer.as_mut_ptr());
        CoTaskMemFree(list as *const core::ffi::c_void);
        if ok == 0 {
            return Ok(None);
        }
        let len = buffer
            .iter()
            .position(|unit| *unit == 0)
            .unwrap_or(buffer.len());
        Ok(Some(String::from_utf16_lossy(&buffer[..len])))
    }
}

#[cfg(windows)]
fn pick_file(owner: Option<isize>) -> Result<Option<String>, String> {
    use windows_sys::Win32::UI::Controls::Dialogs::{
        GetOpenFileNameW, OFN_FILEMUSTEXIST, OFN_PATHMUSTEXIST, OPENFILENAMEW,
    };

    let mut file = [0u16; 2048];
    let filter: Vec<u16> = "All files\0*.*\0\0".encode_utf16().collect();
    let title: Vec<u16> = "选择文件（将使用所在目录）\0".encode_utf16().collect();

    let mut options = OPENFILENAMEW {
        lStructSize: std::mem::size_of::<OPENFILENAMEW>() as u32,
        hwndOwner: owner.unwrap_or(0) as *mut core::ffi::c_void,
        lpstrFilter: filter.as_ptr(),
        nFilterIndex: 1,
        lpstrFile: file.as_mut_ptr(),
        nMaxFile: file.len() as u32,
        lpstrTitle: title.as_ptr(),
        Flags: OFN_FILEMUSTEXIST | OFN_PATHMUSTEXIST,
        ..Default::default()
    };

    // SAFETY: the filter/title/file buffers outlive the modal call; on success
    // the dialog writes a NUL-terminated path into `file`.
    let ok = unsafe { GetOpenFileNameW(&mut options) };
    if ok == 0 {
        return Ok(None);
    }
    let len = file
        .iter()
        .position(|unit| *unit == 0)
        .unwrap_or(file.len());
    Ok(Some(String::from_utf16_lossy(&file[..len])))
}

/// The composer's file picker: `OFN_ALLOWMULTISELECT` with an explorer-style
/// dialog, which is the only combination where the dialog returns more than one
/// path.
///
/// The result is either one full path, or the chosen directory followed by the
/// names inside it — every entry NUL-terminated, the list ending with an empty
/// entry. Both shapes are handled below, because getting this wrong silently
/// produces paths like `C:\proj\a.rs b.rs`.
#[cfg(windows)]
fn pick_files(owner: Option<isize>) -> Result<Option<Vec<String>>, String> {
    use windows_sys::Win32::UI::Controls::Dialogs::{
        GetOpenFileNameW, OFN_ALLOWMULTISELECT, OFN_EXPLORER, OFN_FILEMUSTEXIST, OFN_PATHMUSTEXIST,
        OPENFILENAMEW,
    };

    // One buffer holds every name, and a selection that does not fit is refused
    // rather than truncated. 64 KiB of UTF-16 is ~2000 long paths, far past what
    // anyone clicks in one dialog.
    let mut file = vec![0u16; 32 * 1024];
    let filter: Vec<u16> = "All files\0*.*\0\0".encode_utf16().collect();
    let title: Vec<u16> = "选择文件（可多选，Ctrl / Shift 连续选择）\0"
        .encode_utf16()
        .collect();

    let mut options = OPENFILENAMEW {
        lStructSize: std::mem::size_of::<OPENFILENAMEW>() as u32,
        hwndOwner: owner.unwrap_or(0) as *mut core::ffi::c_void,
        lpstrFilter: filter.as_ptr(),
        nFilterIndex: 1,
        lpstrFile: file.as_mut_ptr(),
        nMaxFile: file.len() as u32,
        lpstrTitle: title.as_ptr(),
        Flags: OFN_ALLOWMULTISELECT | OFN_EXPLORER | OFN_FILEMUSTEXIST | OFN_PATHMUSTEXIST,
        ..Default::default()
    };

    // SAFETY: the filter/title/file buffers outlive the modal call; the dialog
    // fills `file` with NUL-terminated entries.
    let ok = unsafe { GetOpenFileNameW(&mut options) };
    if ok == 0 {
        return Ok(None);
    }
    parse_multi_selection(&file)
}

/// Reads the buffer `GetOpenFileNameW` writes when `OFN_ALLOWMULTISELECT` is
/// set.
///
/// Two shapes come back, and they are easy to confuse:
///
/// * one choice — the full path, NUL-terminated;
/// * several  — the directory, then one entry per selected *name*, NUL-terminated,
///   the list ending with an empty entry.
///
/// Treating the second as the first produces paths like `C:\proj\a.rs b.rs`.
/// This lives outside `#[cfg(windows)]` so a unit test can check it anywhere.
#[cfg_attr(not(windows), allow(dead_code))]
fn parse_multi_selection(file: &[u16]) -> Result<Option<Vec<String>>, String> {
    let mut entries: Vec<String> = Vec::new();
    let mut start = 0usize;
    let mut ended = false;
    for (index, unit) in file.iter().enumerate() {
        if *unit != 0 {
            continue;
        }
        if index == start {
            ended = true;
            break;
        }
        entries.push(String::from_utf16_lossy(&file[start..index]));
        start = index + 1;
    }
    if entries.is_empty() {
        return Ok(None);
    }
    if !ended {
        return Err("一次选中的文件太多，对话框放不下；请分两次添加。".to_string());
    }
    if entries.len() == 1 {
        return Ok(Some(entries));
    }
    let directory = PathBuf::from(&entries[0]);
    Ok(Some(
        entries[1..]
            .iter()
            .map(|name| directory.join(name).to_string_lossy().to_string())
            .collect(),
    ))
}

#[cfg(not(windows))]
fn pick_folder(_owner: Option<isize>) -> Result<Option<String>, String> {
    Err("the folder picker is only implemented on Windows".to_string())
}

#[cfg(not(windows))]
fn pick_file(_owner: Option<isize>) -> Result<Option<String>, String> {
    Err("the file picker is only implemented on Windows".to_string())
}

#[cfg(not(windows))]
fn pick_files(_owner: Option<isize>) -> Result<Option<Vec<String>>, String> {
    Err("the file picker is only implemented on Windows".to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn display_path_strips_the_windows_extended_prefix() {
        // A prefix in only one of the two places would split one folder into
        // two groups in the history panel.
        assert_eq!(
            display_path(Path::new(r"\\?\C:\Users\me\Anvil")),
            r"C:\Users\me\Anvil"
        );
        assert_eq!(
            display_path(Path::new(r"\\?\UNC\host\share")),
            r"\\host\share"
        );
        assert_eq!(display_path(Path::new(r"D:\work")), r"D:\work");
    }

    #[test]
    fn default_workspace_sits_inside_the_user_data_dir() {
        let workspace = default_workspace();
        assert_eq!(workspace.file_name().unwrap(), "workspace");
        assert_eq!(workspace.parent().unwrap(), crate::host::default_data_dir());
        assert_eq!(projects_root().parent().unwrap(), workspace);
    }

    #[test]
    fn create_makes_the_folder_and_reopens_it() {
        let root = std::env::temp_dir().join(format!("anvil-ws-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).unwrap();

        let target = root.join("demo");
        std::fs::create_dir_all(&target).unwrap();
        // Same path twice: the second call is a reopen, not an error.
        let first = workspace_ensure(target.display().to_string()).unwrap();
        let second = workspace_ensure(target.display().to_string()).unwrap();
        assert_eq!(first, second);

        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn ensure_refuses_a_file_and_a_missing_path() {
        let dir = std::env::temp_dir().join(format!("anvil-ws-file-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let file = dir.join("note.txt");
        std::fs::write(&file, b"x").unwrap();

        assert!(workspace_ensure(file.display().to_string()).is_err());
        assert!(workspace_ensure(dir.join("missing").display().to_string()).is_err());
        assert!(workspace_ensure("   ".to_string()).is_err());

        let _ = std::fs::remove_dir_all(&dir);
    }

    /// The buffer a dialog writes: NUL-terminated entries, zeros after the end.
    fn buffer(entries: &[&str]) -> Vec<u16> {
        let mut out = Vec::new();
        for entry in entries {
            out.extend(entry.encode_utf16());
            out.push(0);
        }
        out.push(0);
        out.extend(std::iter::repeat(0u16).take(16));
        out
    }

    #[test]
    fn one_selected_file_is_its_own_path() {
        // `OFN_ALLOWMULTISELECT` still writes a bare path when one file is
        // chosen; splitting it into directory + name would invent a directory
        // called `C:\a\b.txt`.
        let parsed = parse_multi_selection(&buffer(&[r"C:\proj\src\main.rs"])).unwrap();
        assert_eq!(parsed, Some(vec![r"C:\proj\src\main.rs".to_string()]));
    }

    #[test]
    fn several_selected_files_are_joined_to_their_directory() {
        let parsed = parse_multi_selection(&buffer(&[r"C:\proj\src", "main.rs", "lib.rs"])).unwrap();
        assert_eq!(
            parsed,
            Some(vec![
                r"C:\proj\src\main.rs".to_string(),
                r"C:\proj\src\lib.rs".to_string()
            ])
        );
    }

    #[test]
    fn a_cancelled_dialog_and_an_empty_buffer_both_mean_nothing() {
        assert_eq!(parse_multi_selection(&buffer(&[])).unwrap(), None);
    }

    #[test]
    fn a_list_that_filled_the_buffer_is_refused_instead_of_truncated() {
        // No terminating empty entry: the dialog ran out of room, and the paths
        // it did write cannot be trusted to be all of them.
        let mut filled = Vec::new();
        for name in [r"C:\proj", "a.rs", "b.rs"] {
            filled.extend(name.encode_utf16());
            filled.push(0);
        }
        let error = parse_multi_selection(&filled).unwrap_err();
        assert!(error.contains("分两次"), "{error}");
    }
}
