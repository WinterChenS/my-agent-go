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
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum PickKind {
    Folder,
    File,
    Files,
}

/// Dialog titles in the app's current language.
///
/// The pickers are native dialogs, so they cannot read the React translations.
/// Passing the title in makes an English UI stop showing a Chinese file dialog.
struct PickPrompts {
    folder: String,
    file: String,
    files: String,
}

/// Titles for one language.
///
/// The multi-select hint names the platform's own modifier key, because that is
/// the key the person actually has to hold down.
fn pick_prompts(language: &str) -> PickPrompts {
    let english = language.to_ascii_lowercase().starts_with("en");
    #[cfg(target_os = "macos")]
    let (many_zh, many_en) = (
        "（可多选，按住 ⌘ 或 Shift 连续选择）",
        " (hold Cmd or Shift to select several)",
    );
    #[cfg(not(target_os = "macos"))]
    let (many_zh, many_en) = (
        "（可多选，Ctrl / Shift 连续选择）",
        " (hold Ctrl or Shift to select several)",
    );

    if english {
        PickPrompts {
            folder: "Choose a workspace folder".to_string(),
            file: "Choose a file (its folder becomes the workspace)".to_string(),
            files: format!("Choose files{many_en}"),
        }
    } else if cfg!(target_os = "macos") {
        PickPrompts {
            folder: "选择工作区文件夹".to_string(),
            file: "选择文件（将使用所在目录）".to_string(),
            files: format!("选择文件{many_zh}"),
        }
    } else {
        PickPrompts {
            folder: "选择项目目录".to_string(),
            file: "选择文件（将使用所在目录）".to_string(),
            files: format!("选择文件{many_zh}"),
        }
    }
}

/// The language the user picked in 设置, for dialog titles.
fn current_language(app: &tauri::AppHandle) -> String {
    use tauri::Manager;
    app.try_state::<crate::state::HostState>()
        .map(|state| state.snapshot().language)
        .unwrap_or_else(|| crate::prefs::LANGUAGE_ZH_CN.to_string())
}

/// Ask the platform for a folder.
///
/// `None` means the user cancelled.
///
/// On Windows the pickers run on the *window's* thread, via
/// `run_on_main_thread`. A Tauri command executes on a runtime worker that never
/// pumps a Windows message queue; a modal shell dialog created there is either
/// invisible or returns immediately, which is exactly what the app log showed
/// (`workspace: browse` with nothing after it). The main thread owns the
/// webview's message pump, and the modal loop nested inside it still dispatches
/// messages, so the window keeps painting while the dialog is up.
///
/// macOS needs the opposite: the dialog is a *separate process* (`osascript`),
/// so blocking the main thread would only beachball the window behind it.
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
    let prompts = pick_prompts(&current_language(&app));
    log_pick(&app, "folder: opening");
    let outcome = run_picker(&app, owner, PickKind::Folder, &prompts);
    log_pick(&app, &format!("folder: {outcome:?}"));
    outcome.map(|picked| picked.and_then(|paths| paths.into_iter().next()))
}

/// Ask for a *file*. A workspace is a directory, so the file's parent becomes
/// the selection; the picker exists because people describe a project by one of
/// its files ("open the folder that has this spec in it").
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
    let prompts = pick_prompts(&current_language(&app));
    log_pick(&app, "file: opening");
    let outcome = run_picker(&app, owner, PickKind::File, &prompts);
    log_pick(&app, &format!("file: {outcome:?}"));
    outcome.map(|picked| picked.and_then(|paths| paths.into_iter().next()))
}

/// Ask for one or more *files* to attach as context.
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
    let prompts = pick_prompts(&current_language(&app));
    log_pick(&app, "files: opening");
    let outcome = run_picker(&app, owner, PickKind::Files, &prompts);
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

/// Runs one picker where the platform needs it to run, and returns its answer.
#[cfg(windows)]
fn run_picker(
    app: &tauri::AppHandle,
    owner: Option<isize>,
    kind: PickKind,
    prompts: &PickPrompts,
) -> Result<Option<Vec<String>>, String> {
    let (sender, receiver) = std::sync::mpsc::channel();
    let titles = (
        prompts.folder.clone(),
        prompts.file.clone(),
        prompts.files.clone(),
    );
    app.run_on_main_thread(move || {
        let (folder, file, files) = &titles;
        let outcome = match kind {
            PickKind::Folder => pick_folder(owner, folder).map(|one| one.map(|path| vec![path])),
            PickKind::File => pick_file(owner, file).map(|one| one.map(|path| vec![path])),
            PickKind::Files => pick_files(owner, files),
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

/// macOS runs the picker as its own process, so it stays on this worker thread:
/// the window behind the dialog keeps painting and no beachball appears.
#[cfg(not(windows))]
fn run_picker(
    app: &tauri::AppHandle,
    owner: Option<isize>,
    kind: PickKind,
    prompts: &PickPrompts,
) -> Result<Option<Vec<String>>, String> {
    let _ = (app, owner);
    match kind {
        PickKind::Folder => {
            pick_folder(owner, &prompts.folder).map(|one| one.map(|path| vec![path]))
        }
        PickKind::File => pick_file(owner, &prompts.file).map(|one| one.map(|path| vec![path])),
        PickKind::Files => pick_files(owner, &prompts.files),
    }
}

#[cfg(windows)]
fn pick_folder(owner: Option<isize>, title: &str) -> Result<Option<String>, String> {
    use windows_sys::Win32::System::Com::{
        COINIT_APARTMENTTHREADED, COINIT_DISABLE_OLE1DDE, CoInitializeEx, CoTaskMemFree,
    };
    use windows_sys::Win32::UI::Shell::{
        BIF_EDITBOX, BIF_NEWDIALOGSTYLE, BIF_RETURNONLYFSDIRS, BROWSEINFOW, SHBrowseForFolderW,
        SHGetPathFromIDListW,
    };

    let title: Vec<u16> = format!("{title}\0").encode_utf16().collect();
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
fn pick_file(owner: Option<isize>, title: &str) -> Result<Option<String>, String> {
    use windows_sys::Win32::UI::Controls::Dialogs::{
        GetOpenFileNameW, OFN_FILEMUSTEXIST, OFN_PATHMUSTEXIST, OPENFILENAMEW,
    };

    let mut file = [0u16; 2048];
    let filter: Vec<u16> = "All files\0*.*\0\0".encode_utf16().collect();
    let title: Vec<u16> = format!("{title}\0").encode_utf16().collect();

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
fn pick_files(owner: Option<isize>, title: &str) -> Result<Option<Vec<String>>, String> {
    use windows_sys::Win32::UI::Controls::Dialogs::{
        GetOpenFileNameW, OFN_ALLOWMULTISELECT, OFN_EXPLORER, OFN_FILEMUSTEXIST, OFN_PATHMUSTEXIST,
        OPENFILENAMEW,
    };

    // One buffer holds every name, and a selection that does not fit is refused
    // rather than truncated. 64 KiB of UTF-16 is ~2000 long paths, far past what
    // anyone clicks in one dialog.
    let mut file = vec![0u16; 32 * 1024];
    let filter: Vec<u16> = "All files\0*.*\0\0".encode_utf16().collect();
    let title: Vec<u16> = format!("{title}\0").encode_utf16().collect();

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

/// macOS pickers, driven through `osascript`.
///
/// Two designs were available: link AppKit (`NSOpenPanel` via `objc2`) or ask
/// Standard Additions to show the panel. The second needs no new dependency and
/// no Objective-C plumbing, at the cost of the dialog being owned by
/// `osascript` — it still looks native, still remembers the last folder, and is
/// what every Electron-less Mac CLI does. `NSOpenPanel` would additionally let
/// the panel sheet off the window, which matters less than a picker that exists.
///
/// The scripts are plain string constants; `script_for` is compiled on every
/// platform so the shapes and the parsing stay under test off a Mac.
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
mod applescript {
    use super::PickKind;

    /// One AppleScript statement per line, so a test can assert the shape.
    pub(super) fn script_for(kind: PickKind, prompt: &str) -> String {
        let prompt = escape(prompt);
        match kind {
            PickKind::Folder => format!(
                "set chosen to choose folder with prompt \"{prompt}\"\n\
                 return POSIX path of chosen"
            ),
            PickKind::File => format!(
                "set chosen to choose file with prompt \"{prompt}\"\n\
                 return POSIX path of chosen"
            ),
            PickKind::Files => format!(
                "set chosen to choose file with prompt \"{prompt}\" with multiple selections allowed\n\
                 set output to \"\"\n\
                 repeat with one in chosen\n\
                 set output to output & (POSIX path of one) & linefeed\n\
                 end repeat\n\
                 return output"
            ),
        }
    }

    /// AppleScript string literals escape with a backslash, and nothing else.
    fn escape(text: &str) -> String {
        text.replace('\\', "\\\\").replace('"', "\\\"")
    }

    /// Turns osascript's stdout into the paths the host returns.
    ///
    /// `POSIX path of` a folder keeps the trailing slash, and the ledger keys
    /// sessions by the folder's spelling, so `/Users/me/proj/` and
    /// `/Users/me/proj` must not become two groups. Root keeps its slash.
    pub(super) fn parse_paths(stdout: &str) -> Vec<String> {
        stdout
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty())
            .map(|line| {
                let trimmed = line.trim_end_matches('/');
                if trimmed.is_empty() {
                    "/".to_string()
                } else {
                    trimmed.to_string()
                }
            })
            .collect()
    }

    /// Whether osascript's stderr means "the person pressed Cancel".
    ///
    /// Cancel surfaces as `(-128)` in every locale; the words around it are
    /// localized, so only the code is matched.
    pub(super) fn is_cancel(stderr: &str) -> bool {
        stderr.contains("-128")
    }
}

#[cfg(target_os = "macos")]
fn run_osascript(kind: PickKind, prompt: &str) -> Result<Option<Vec<String>>, String> {
    let script = applescript::script_for(kind, prompt);
    let output = std::process::Command::new("/usr/bin/osascript")
        .arg("-e")
        .arg(&script)
        .output()
        .map_err(|error| format!("cannot start the macOS file dialog: {error}"))?;
    if output.status.success() {
        let stdout = String::from_utf8_lossy(&output.stdout);
        let paths = applescript::parse_paths(&stdout);
        return Ok(if paths.is_empty() { None } else { Some(paths) });
    }
    let stderr = String::from_utf8_lossy(&output.stderr);
    if applescript::is_cancel(&stderr) {
        return Ok(None);
    }
    Err(format!(
        "the macOS file dialog failed: {}",
        stderr.trim().to_string()
    ))
}

#[cfg(target_os = "macos")]
fn pick_folder(_owner: Option<isize>, title: &str) -> Result<Option<String>, String> {
    run_osascript(PickKind::Folder, title)
        .map(|paths| paths.and_then(|list| list.into_iter().next()))
}

#[cfg(target_os = "macos")]
fn pick_file(_owner: Option<isize>, title: &str) -> Result<Option<String>, String> {
    run_osascript(PickKind::File, title).map(|paths| paths.and_then(|list| list.into_iter().next()))
}

#[cfg(target_os = "macos")]
fn pick_files(_owner: Option<isize>, title: &str) -> Result<Option<Vec<String>>, String> {
    run_osascript(PickKind::Files, title)
}

/// Linux: no dialog was ever implemented, and the app is not shipped there.
#[cfg(all(not(windows), not(target_os = "macos")))]
fn pick_folder(_owner: Option<isize>, _title: &str) -> Result<Option<String>, String> {
    Err("no native folder dialog on this platform; paste the path instead".to_string())
}

#[cfg(all(not(windows), not(target_os = "macos")))]
fn pick_file(_owner: Option<isize>, _title: &str) -> Result<Option<String>, String> {
    Err("no native file dialog on this platform; paste the path instead".to_string())
}

#[cfg(all(not(windows), not(target_os = "macos")))]
fn pick_files(_owner: Option<isize>, _title: &str) -> Result<Option<Vec<String>>, String> {
    Err("no native file dialog on this platform; paste the path instead".to_string())
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
        let parsed =
            parse_multi_selection(&buffer(&[r"C:\proj\src", "main.rs", "lib.rs"])).unwrap();
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

    // The macOS picker is a script handed to `osascript`; these tests keep its
    // shape and its output parsing honest on a machine without a Mac.

    #[test]
    fn the_folder_script_returns_a_posix_path() {
        let script = applescript::script_for(PickKind::Folder, "选择工作区文件夹");
        assert!(script.contains("choose folder with prompt"), "{script}");
        assert!(script.contains("选择工作区文件夹"), "{script}");
        assert!(script.contains("POSIX path of chosen"), "{script}");
        assert!(!script.contains("multiple selections"), "{script}");
    }

    #[test]
    fn the_multi_file_script_allows_several_selections() {
        let script = applescript::script_for(PickKind::Files, "Choose files");
        assert!(
            script.contains("with multiple selections allowed"),
            "{script}"
        );
        assert!(script.contains("linefeed"), "{script}");
    }

    #[test]
    fn a_quote_in_a_title_cannot_break_the_script() {
        let script = applescript::script_for(PickKind::File, "say \"hi\"");
        assert!(script.contains(r#"say \"hi\""#), "{script}");
    }

    #[test]
    fn folder_paths_lose_the_trailing_slash_but_root_keeps_it() {
        let paths = applescript::parse_paths("/Users/me/proj/\n/\n");
        assert_eq!(paths, vec!["/Users/me/proj".to_string(), "/".to_string()]);
    }

    #[test]
    fn several_paths_come_back_one_per_line() {
        let paths = applescript::parse_paths("/a/one.rs\n/b/two.rs\n");
        assert_eq!(
            paths,
            vec!["/a/one.rs".to_string(), "/b/two.rs".to_string()]
        );
    }

    #[test]
    fn cancelling_osascript_is_a_cancel_not_a_failure() {
        // Localized text around a stable code; only the code is matched.
        assert!(applescript::is_cancel(
            "execution error: User canceled. (-128)"
        ));
        assert!(applescript::is_cancel(
            "execution error: 用户已取消。(-128)"
        ));
        assert!(!applescript::is_cancel("execution error: x (-1743)"));
    }
}
