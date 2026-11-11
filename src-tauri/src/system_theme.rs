//! Windows system theme detection.
//!
//! Port of `cmd/anvil/theme_system_windows.go`: the app theme comes from
//! `HKCU\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`
//! `AppsUseLightTheme` (0 = dark). Missing key/value or any error means "light",
//! matching the Go host. The desktop host polls this value and pushes changes to
//! the frontend as a Tauri event; the browser/jsdom path keeps its own polling
//! fallback in `frontend/src/preferences/theme.ts`.

use std::time::Duration;

/// Registry key holding the Windows app theme.
pub const PERSONALIZE_KEY: &str = r"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize";
/// Registry value: 0 = dark, non-zero = light.
pub const APPS_USE_LIGHT_THEME: &str = "AppsUseLightTheme";
/// Host polling interval for system theme changes (the Go host used 1.2 s).
pub const SYSTEM_THEME_POLL_INTERVAL: Duration = Duration::from_millis(1200);
/// Tauri event name used to push system theme changes to the frontend.
pub const SYSTEM_THEME_EVENT: &str = "anvil://system-theme";

/// Whether the operating system currently uses the dark app theme.
#[cfg(windows)]
#[must_use]
pub fn system_dark() -> bool {
    use std::ptr;

    use windows_sys::Win32::System::Registry::{
        HKEY, HKEY_CURRENT_USER, KEY_QUERY_VALUE, REG_DWORD, RegCloseKey, RegOpenKeyExW,
        RegQueryValueExW,
    };

    fn wide(value: &str) -> Vec<u16> {
        value.encode_utf16().chain(std::iter::once(0)).collect()
    }

    let key_name = wide(PERSONALIZE_KEY);
    let mut key: HKEY = ptr::null_mut();
    // SAFETY: `key_name` is a NUL-terminated UTF-16 buffer; `key` receives the handle.
    let status = unsafe {
        RegOpenKeyExW(
            HKEY_CURRENT_USER,
            key_name.as_ptr(),
            0,
            KEY_QUERY_VALUE,
            &mut key,
        )
    };
    if status != 0 {
        return false;
    }

    let value_name = wide(APPS_USE_LIGHT_THEME);
    let mut kind = REG_DWORD;
    let mut data: u32 = 1;
    let mut size = std::mem::size_of::<u32>() as u32;
    // SAFETY: the buffer is a valid `u32` and `size` describes its length.
    let status = unsafe {
        RegQueryValueExW(
            key,
            value_name.as_ptr(),
            ptr::null(),
            &mut kind,
            (&mut data as *mut u32).cast::<u8>(),
            &mut size,
        )
    };
    // SAFETY: `key` was opened above and is closed exactly once here.
    unsafe {
        RegCloseKey(key);
    }
    if status != 0 || kind != REG_DWORD {
        return false;
    }
    data == 0
}

/// Non-Windows builds have no Windows app theme; the desktop host is Windows-first.
#[cfg(not(windows))]
#[must_use]
pub fn system_dark() -> bool {
    false
}
