//! `HKCU\…\Themes\Personalize\AppsUseLightTheme` without spawning `reg.exe`.
//!
//! The theme case has to flip the real Windows value to prove that system mode
//! follows the OS and manual mode ignores it. The Go runner shelled out to
//! `reg query` / `reg add`; on this host process creation for `reg.exe` is
//! refused (`os error 5`), so the value is read and written through advapi32
//! directly. The devtools CLI exposes it as `registry --get` / `registry --set`,
//! which keeps every flip a recorded, exit-coded step in the evidence.

/// Key holding the per-user app theme preference.
pub(crate) const PERSONALIZE_KEY: &str =
    r"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize";

/// Value name the client polls.
pub(crate) const APPS_USE_LIGHT_THEME: &str = "AppsUseLightTheme";

/// Read the current value: `1` means the OS asks for light app chrome.
pub(crate) fn read_apps_use_light() -> Result<i64, String> {
    imp::read()
}

/// Write the value; only `0` and `1` are meaningful.
pub(crate) fn write_apps_use_light(value: i64) -> Result<(), String> {
    imp::write(value)
}

#[cfg(windows)]
mod imp {
    use std::ffi::c_void;

    use windows_sys::Win32::Foundation::ERROR_SUCCESS;
    use windows_sys::Win32::System::Registry::{
        HKEY_CURRENT_USER, REG_DWORD, RRF_RT_REG_DWORD, RegGetValueW, RegSetKeyValueW,
    };

    use super::{APPS_USE_LIGHT_THEME, PERSONALIZE_KEY};

    fn wide(text: &str) -> Vec<u16> {
        text.encode_utf16().chain(std::iter::once(0)).collect()
    }

    pub(super) fn read() -> Result<i64, String> {
        let subkey = wide(PERSONALIZE_KEY);
        let value = wide(APPS_USE_LIGHT_THEME);
        let mut data: u32 = 0;
        let mut size = u32::try_from(std::mem::size_of::<u32>()).expect("u32 size fits");
        let mut kind: u32 = 0;
        // SAFETY: every pointer refers to a live local of the documented type,
        // and `size` states the byte length of `data`.
        let status = unsafe {
            RegGetValueW(
                HKEY_CURRENT_USER,
                subkey.as_ptr(),
                value.as_ptr(),
                RRF_RT_REG_DWORD,
                &mut kind,
                (&raw mut data).cast::<c_void>(),
                &mut size,
            )
        };
        if status != ERROR_SUCCESS {
            return Err(format!("RegGetValueW failed with error {status}"));
        }
        Ok(i64::from(data))
    }

    pub(super) fn write(value: i64) -> Result<(), String> {
        let data = u32::try_from(value).map_err(|_| format!("{value} is not a DWORD"))?;
        let subkey = wide(PERSONALIZE_KEY);
        let name = wide(APPS_USE_LIGHT_THEME);
        // SAFETY: `data` is a live u32 and its size is passed verbatim.
        let status = unsafe {
            RegSetKeyValueW(
                HKEY_CURRENT_USER,
                subkey.as_ptr(),
                name.as_ptr(),
                REG_DWORD,
                (&raw const data).cast::<c_void>(),
                u32::try_from(std::mem::size_of::<u32>()).expect("u32 size fits"),
            )
        };
        if status != ERROR_SUCCESS {
            return Err(format!("RegSetKeyValueW failed with error {status}"));
        }
        Ok(())
    }
}

#[cfg(not(windows))]
mod imp {
    pub(super) fn read() -> Result<i64, String> {
        Err("the Windows app theme value is only readable on Windows".to_string())
    }

    pub(super) fn write(_value: i64) -> Result<(), String> {
        Err("the Windows app theme value is only writable on Windows".to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_key_matches_the_per_user_personalize_location() {
        assert_eq!(
            PERSONALIZE_KEY,
            r"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize"
        );
        assert_eq!(APPS_USE_LIGHT_THEME, "AppsUseLightTheme");
    }

    #[test]
    fn writing_a_non_dword_value_is_rejected() {
        if cfg!(windows) {
            let err = write_apps_use_light(-1).expect_err("must reject");
            assert!(err.contains("not a DWORD"), "{err}");
        }
    }
}
