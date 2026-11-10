//! Windows final-path resolution used by the D: contract.
//!
//! `GetFinalPathNameByHandleW` is the authoritative answer for "which volume
//! does this write land on": unlike `std::fs::canonicalize` on older toolchains,
//! it follows junctions, symlinks and mount points and reports the volume of the
//! real target.

use std::ffi::OsString;
use std::io;
use std::os::windows::ffi::{OsStrExt, OsStringExt};
use std::path::{Path, PathBuf};

use windows_sys::Win32::Foundation::{CloseHandle, HANDLE, INVALID_HANDLE_VALUE};
use windows_sys::Win32::Storage::FileSystem::{
    CreateFileW, FILE_FLAG_BACKUP_SEMANTICS, FILE_SHARE_DELETE, FILE_SHARE_READ, FILE_SHARE_WRITE,
    GetFinalPathNameByHandleW, OPEN_EXISTING,
};

const VERBATIM: &[u16] = &[b'\\' as u16, b'\\' as u16, b'?' as u16, b'\\' as u16];
const VERBATIM_UNC: &[u16] = &[b'U' as u16, b'N' as u16, b'C' as u16, b'\\' as u16];

pub(crate) fn final_path(path: &Path) -> io::Result<PathBuf> {
    let wide = wide_verbatim(path);

    // SAFETY: `wide` is a NUL-terminated UTF-16 buffer that outlives the call.
    // The security attributes and template handles are null, access mode 0 asks
    // only for attribute access, and FILE_FLAG_BACKUP_SEMANTICS is required to
    // open directories.
    let handle = unsafe {
        CreateFileW(
            wide.as_ptr(),
            0,
            FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
            std::ptr::null(),
            OPEN_EXISTING,
            FILE_FLAG_BACKUP_SEMANTICS,
            std::ptr::null_mut(),
        )
    };
    if handle == INVALID_HANDLE_VALUE {
        return Err(io::Error::last_os_error());
    }

    let result = query_final_path(handle);
    // SAFETY: the handle was opened successfully above and is closed once here.
    unsafe {
        CloseHandle(handle);
    }
    result
}

fn query_final_path(handle: HANDLE) -> io::Result<PathBuf> {
    let mut buffer = vec![0u16; 512];
    loop {
        // SAFETY: the buffer is valid for `buffer.len()` UTF-16 units.
        let written = unsafe {
            GetFinalPathNameByHandleW(handle, buffer.as_mut_ptr(), buffer.len() as u32, 0)
        };
        if written == 0 {
            return Err(io::Error::last_os_error());
        }
        let written = written as usize;
        if written < buffer.len() {
            buffer.truncate(written);
            return Ok(PathBuf::from(OsString::from_wide(&strip_verbatim(&buffer))));
        }
        buffer = vec![0u16; written + 1];
    }
}

/// Build a NUL-terminated verbatim path (`\\?\…`) so the Win32 call is not
/// limited to `MAX_PATH` and does not apply DOS-path normalization twice.
fn wide_verbatim(path: &Path) -> Vec<u16> {
    let units: Vec<u16> = path.as_os_str().encode_wide().collect();
    let mut wide = if units.starts_with(VERBATIM) {
        units
    } else {
        let mut prefixed: Vec<u16> = VERBATIM.to_vec();
        prefixed.extend_from_slice(&units);
        prefixed
    };
    wide.push(0);
    wide
}

/// Remove the `\\?\` marker, mapping `\\?\UNC\host\share` back to `\\host\share`.
fn strip_verbatim(units: &[u16]) -> Vec<u16> {
    if units.len() > VERBATIM.len() + VERBATIM_UNC.len()
        && units.starts_with(VERBATIM)
        && units[VERBATIM.len()..].starts_with(VERBATIM_UNC)
    {
        let mut stripped: Vec<u16> = vec![b'\\' as u16, b'\\' as u16];
        stripped.extend_from_slice(&units[VERBATIM.len() + VERBATIM_UNC.len()..]);
        return stripped;
    }
    if units.starts_with(VERBATIM) {
        return units[VERBATIM.len()..].to_vec();
    }
    units.to_vec()
}
