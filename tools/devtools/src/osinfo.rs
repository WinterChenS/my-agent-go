//! OS facts recorded in every `result.json`.
//!
//! Ported from `internal/acceptance/osinfo_windows.go`: the edition, kernel
//! version, build/UBR and architecture come from
//! `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion` so the evidence shows the
//! machine it was actually produced on (Windows 10 22H2 here, not the Windows 11
//! 24H2 acceptance baseline).

use windows_sys::Win32::Foundation::ERROR_SUCCESS;
use windows_sys::Win32::System::Registry::{
    HKEY, HKEY_LOCAL_MACHINE, KEY_QUERY_VALUE, KEY_WOW64_64KEY, REG_DWORD, REG_SZ, RegCloseKey,
    RegOpenKeyExW, RegQueryValueExW,
};

use crate::evidence::OsInfo;

const CURRENT_VERSION: &str = r"SOFTWARE\Microsoft\Windows NT\CurrentVersion";

/// Read the OS identity recorded in evidence.
#[must_use]
pub fn current() -> OsInfo {
    let product = registry_string("ProductName").unwrap_or_default();
    let display = registry_string("DisplayVersion").unwrap_or_default();
    let build = registry_string("CurrentBuildNumber").unwrap_or_default();
    let ubr = registry_string("UBR").unwrap_or_default();

    let name = if display.is_empty() {
        product
    } else {
        format!("{} {}", product.trim(), display.trim())
            .trim()
            .to_string()
    };
    let version = if build.is_empty() {
        String::new()
    } else {
        format!("10.0.{build}")
    };
    let full_build = if ubr.is_empty() {
        build
    } else {
        format!("{build}.{ubr}")
    };
    OsInfo {
        name,
        version,
        build: full_build,
        arch: go_arch().to_string(),
    }
}

/// Report the architecture with the names the Go evidence used, so old and new
/// `result.json` files stay comparable.
#[must_use]
pub fn go_arch() -> &'static str {
    match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        "x86" => "386",
        other => other,
    }
}

fn registry_string(name: &str) -> Option<String> {
    let subkey = wide(CURRENT_VERSION);
    let mut key: HKEY = std::ptr::null_mut();
    // SAFETY: `subkey` is NUL-terminated and outlives the call; `key` is a valid
    // out-pointer for the opened handle.
    let status = unsafe {
        RegOpenKeyExW(
            HKEY_LOCAL_MACHINE,
            subkey.as_ptr(),
            0,
            KEY_QUERY_VALUE | KEY_WOW64_64KEY,
            &mut key,
        )
    };
    if status != ERROR_SUCCESS {
        return None;
    }
    let value = query_value(key, name);
    // SAFETY: `key` was opened successfully above and is closed exactly once.
    unsafe {
        RegCloseKey(key);
    }
    value
}

fn query_value(key: HKEY, name: &str) -> Option<String> {
    let name = wide(name);
    let mut kind: u32 = 0;
    let mut size: u32 = 0;
    // SAFETY: the name is NUL-terminated; a null data pointer with size 0 asks
    // only for the required buffer length.
    let status = unsafe {
        RegQueryValueExW(
            key,
            name.as_ptr(),
            std::ptr::null_mut(),
            &mut kind,
            std::ptr::null_mut(),
            &mut size,
        )
    };
    if status != ERROR_SUCCESS || size == 0 {
        return None;
    }
    if kind == REG_DWORD {
        let raw: u32 = 0;
        let mut raw_size = std::mem::size_of::<u32>() as u32;
        // SAFETY: the buffer matches the declared size for a REG_DWORD value.
        let status = unsafe {
            RegQueryValueExW(
                key,
                name.as_ptr(),
                std::ptr::null_mut(),
                &mut kind,
                (&raw as *const u32).cast_mut().cast::<u8>(),
                &mut raw_size,
            )
        };
        return (status == ERROR_SUCCESS).then(|| raw.to_string());
    }
    if kind != REG_SZ {
        return None;
    }
    let mut buffer = vec![0u8; size as usize];
    // SAFETY: `buffer` has exactly `size` bytes, which is what the API wrote.
    let status = unsafe {
        RegQueryValueExW(
            key,
            name.as_ptr(),
            std::ptr::null_mut(),
            &mut kind,
            buffer.as_mut_ptr(),
            &mut size,
        )
    };
    if status != ERROR_SUCCESS {
        return None;
    }
    let end = buffer
        .iter()
        .position(|byte| *byte == 0)
        .unwrap_or(buffer.len());
    let units: Vec<u16> = buffer[..end]
        .chunks_exact(2)
        .map(|pair| u16::from_le_bytes([pair[0], pair[1]]))
        .collect();
    Some(String::from_utf16_lossy(&units).trim().to_string())
}

fn wide(value: &str) -> Vec<u16> {
    value.encode_utf16().chain(std::iter::once(0)).collect()
}
