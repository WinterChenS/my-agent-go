//! Credential storage.
//!
//! On Windows the secret is encrypted with DPAPI under the current user, so the
//! ciphertext in the catalog database is useless to another account and cannot
//! be read back without the OS key. Nothing in this module logs a plaintext.

use crate::error::{Error, Result};

/// Encrypt a secret for at-rest storage.
#[cfg(windows)]
pub fn protect(plaintext: &str) -> Result<Vec<u8>> {
    imp::protect(plaintext.as_bytes())
}

/// Decrypt a stored secret.
#[cfg(windows)]
pub fn unprotect(ciphertext: &[u8]) -> Result<String> {
    let bytes = imp::unprotect(ciphertext)?;
    String::from_utf8(bytes).map_err(|_| Error::Format("credential is not valid UTF-8".into()))
}

#[cfg(not(windows))]
pub fn protect(_plaintext: &str) -> Result<Vec<u8>> {
    Err(Error::Rejected(
        "credential storage requires the Windows DPAPI; this build has no key store".into(),
    ))
}

#[cfg(not(windows))]
pub fn unprotect(_ciphertext: &[u8]) -> Result<String> {
    Err(Error::Rejected(
        "credential storage requires the Windows DPAPI; this build has no key store".into(),
    ))
}

#[cfg(windows)]
mod imp {
    use std::ptr;

    use windows_sys::Win32::Foundation::LocalFree;
    use windows_sys::Win32::Security::Cryptography::{
        CRYPT_INTEGER_BLOB, CRYPTPROTECT_UI_FORBIDDEN, CryptProtectData, CryptUnprotectData,
    };

    use crate::error::{Error, Result};

    fn blob(bytes: &[u8]) -> CRYPT_INTEGER_BLOB {
        CRYPT_INTEGER_BLOB {
            cbData: bytes.len() as u32,
            pbData: bytes.as_ptr() as *mut u8,
        }
    }

    fn take(output: CRYPT_INTEGER_BLOB) -> Result<Vec<u8>> {
        if output.pbData.is_null() {
            return Err(Error::Store("DPAPI returned no buffer".into()));
        }
        // SAFETY: the buffer belongs to DPAPI and is copied before it is freed.
        let bytes = unsafe { std::slice::from_raw_parts(output.pbData, output.cbData as usize) };
        let copy = bytes.to_vec();
        // SAFETY: LocalFree is the documented release for this buffer.
        unsafe {
            LocalFree(output.pbData as *mut core::ffi::c_void);
        }
        Ok(copy)
    }

    pub(super) fn protect(plaintext: &[u8]) -> Result<Vec<u8>> {
        let input = blob(plaintext);
        let mut output = CRYPT_INTEGER_BLOB {
            cbData: 0,
            pbData: ptr::null_mut(),
        };
        // SAFETY: both blobs point at live buffers for the duration of the call.
        let ok = unsafe {
            CryptProtectData(
                &input,
                ptr::null(),
                ptr::null(),
                ptr::null(),
                ptr::null(),
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output,
            )
        };
        if ok == 0 {
            return Err(Error::Store(format!(
                "CryptProtectData failed: {}",
                std::io::Error::last_os_error()
            )));
        }
        take(output)
    }

    pub(super) fn unprotect(ciphertext: &[u8]) -> Result<Vec<u8>> {
        let input = blob(ciphertext);
        let mut output = CRYPT_INTEGER_BLOB {
            cbData: 0,
            pbData: ptr::null_mut(),
        };
        // SAFETY: same contract as protect.
        let ok = unsafe {
            CryptUnprotectData(
                &input,
                ptr::null_mut(),
                ptr::null(),
                ptr::null(),
                ptr::null(),
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output,
            )
        };
        if ok == 0 {
            return Err(Error::Store(format!(
                "CryptUnprotectData failed: {}",
                std::io::Error::last_os_error()
            )));
        }
        take(output)
    }
}

#[cfg(all(test, windows))]
mod tests {
    use super::*;

    #[test]
    fn round_trips_a_secret() {
        let secret = "sk-test-0123456789";
        let ciphertext = protect(secret).expect("protect");
        assert_ne!(ciphertext, secret.as_bytes());
        assert!(!String::from_utf8_lossy(&ciphertext).contains("sk-test"));
        assert_eq!(unprotect(&ciphertext).expect("unprotect"), secret);
    }

    #[test]
    fn rejects_garbage_ciphertext() {
        let error = unprotect(&[0u8, 1, 2, 3]).expect_err("garbage");
        assert_eq!(error.code(), "store");
    }
}
