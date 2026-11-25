//! Credential storage.
//!
//! Two implementations, one contract: a secret goes in, opaque bytes come out,
//! and only this module can turn them back. Nothing here logs a plaintext.
//!
//! * Windows — DPAPI under the current user, so the ciphertext in the catalog
//!   database is useless to another account and cannot be read back without the
//!   OS key.
//! * macOS / Linux — no DPAPI. The key lives in `<data dir>/credential.key`
//!   (mode 0600) and the bytes are sealed with a SHA-256 counter-mode keystream
//!   and a tag. This matches the posture of the runtime's own credential file
//!   (`~/.pi/agent/auth.json` is a plaintext JSON object this user can read) and
//!   it is described as such on purpose: it protects the database from being
//!   read *on its own* — a backup, a copy, a shared folder — not from a process
//!   already running as this user.
//!
//! Before this, every non-Windows build answered `Rejected("credential storage
//! requires the Windows DPAPI")`, so adding a model with an API key failed on
//! Linux and macOS no matter what was typed — and the runtime stayed "not
//! signed in" with no way out of it from the app.

use crate::error::{Error, Result};

/// Bytes of nonce in front of every Unix ciphertext.
#[cfg(not(windows))]
const NONCE: usize = 16;
/// Bytes of tag after that nonce.
#[cfg(not(windows))]
const TAG: usize = 16;
/// Length of the key file.
#[cfg(not(windows))]
const KEY: usize = 32;

/// Encrypt a secret for at-rest storage.
///
/// `dir` is the directory the catalog database lives in; Windows encrypts with
/// the OS key and ignores it.
pub fn protect(dir: &std::path::Path, plaintext: &str) -> Result<Vec<u8>> {
    #[cfg(windows)]
    {
        let _ = dir;
        imp::protect(plaintext.as_bytes())
    }
    #[cfg(not(windows))]
    {
        file::protect(dir, plaintext.as_bytes())
    }
}

/// Decrypt a stored secret.
pub fn unprotect(dir: &std::path::Path, ciphertext: &[u8]) -> Result<String> {
    let bytes = {
        #[cfg(windows)]
        {
            let _ = dir;
            imp::unprotect(ciphertext)?
        }
        #[cfg(not(windows))]
        {
            file::unprotect(dir, ciphertext)?
        }
    };
    String::from_utf8(bytes).map_err(|_| Error::Format("credential is not valid UTF-8".into()))
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

/// Unix key store: one key file beside the catalog database.
#[cfg(not(windows))]
mod file {
    use std::fs::{File, OpenOptions};
    use std::io::{Read, Write};
    use std::os::unix::fs::OpenOptionsExt;
    use std::path::{Path, PathBuf};

    use sha2::{Digest, Sha256};

    use super::{KEY, NONCE, TAG};
    use crate::error::{Error, Result};

    fn key_path(dir: &Path) -> PathBuf {
        dir.join("credential.key")
    }

    /// `len` random bytes from the OS.
    ///
    /// `/dev/urandom` is the one source every Unix has, and it needs no crate:
    /// this module is already the only place that touches secrets, so a
    /// dependency just for entropy would be the wrong trade.
    fn random(len: usize) -> Result<Vec<u8>> {
        let mut source = File::open("/dev/urandom")
            .map_err(|_| Error::Store("no /dev/urandom to seed the credential key".into()))?;
        let mut out = vec![0u8; len];
        source
            .read_exact(&mut out)
            .map_err(|_| Error::Store("could not read randomness".into()))?;
        Ok(out)
    }

    /// The key, created on first use.
    ///
    /// `create_new` keeps two hosts from racing a half-written key into place;
    /// the loser reads the winner's file instead of overwriting it.
    fn key(dir: &Path) -> Result<[u8; KEY]> {
        let path = key_path(dir);
        if let Ok(mut file) = File::open(&path) {
            let mut key = [0u8; KEY];
            file.read_exact(&mut key)
                .map_err(|_| Error::Store(format!("{} is not a key file", path.display())))?;
            return Ok(key);
        }
        std::fs::create_dir_all(dir)?;
        let fresh = random(KEY)?;
        let mut options = OpenOptions::new();
        // 0600: the secret is only as private as this file is.
        options.write(true).create_new(true).mode(0o600);
        match options.open(&path) {
            Ok(mut file) => {
                file.write_all(&fresh)?;
                file.flush()?;
                let mut key = [0u8; KEY];
                key.copy_from_slice(&fresh);
                Ok(key)
            }
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => key(dir),
            Err(error) => Err(error.into()),
        }
    }

    /// SHA-256 in counter mode. The key never leaves this process.
    fn keystream(key: &[u8], nonce: &[u8], len: usize) -> Vec<u8> {
        let mut out = Vec::with_capacity(len + 32);
        let mut counter: u32 = 0;
        while out.len() < len {
            let mut hasher = Sha256::new();
            hasher.update(key);
            hasher.update(nonce);
            hasher.update(counter.to_be_bytes());
            out.extend_from_slice(&hasher.finalize());
            counter = counter.wrapping_add(1);
        }
        out.truncate(len);
        out
    }

    fn tag(key: &[u8], nonce: &[u8], ciphertext: &[u8]) -> [u8; TAG] {
        let mut hasher = Sha256::new();
        hasher.update(b"anvil-credential-tag");
        hasher.update(key);
        hasher.update(nonce);
        hasher.update(ciphertext);
        let digest = hasher.finalize();
        let mut out = [0u8; TAG];
        out.copy_from_slice(&digest[..TAG]);
        out
    }

    fn xor(bytes: &[u8], stream: &[u8]) -> Vec<u8> {
        bytes
            .iter()
            .zip(stream.iter())
            .map(|(byte, pad)| byte ^ pad)
            .collect()
    }

    pub(super) fn protect(dir: &Path, plaintext: &[u8]) -> Result<Vec<u8>> {
        let key = key(dir)?;
        let nonce = random(NONCE)?;
        let ciphertext = xor(plaintext, &keystream(&key, &nonce, plaintext.len()));
        let mut out = Vec::with_capacity(NONCE + TAG + ciphertext.len());
        out.extend_from_slice(&nonce);
        out.extend_from_slice(&tag(&key, &nonce, &ciphertext));
        out.extend_from_slice(&ciphertext);
        Ok(out)
    }

    pub(super) fn unprotect(dir: &Path, sealed: &[u8]) -> Result<Vec<u8>> {
        if sealed.len() < NONCE + TAG {
            return Err(Error::Format("credential blob is truncated".into()));
        }
        let key = key(dir)?;
        let nonce = &sealed[..NONCE];
        let stored = &sealed[NONCE..NONCE + TAG];
        let ciphertext = &sealed[NONCE + TAG..];
        let expected = tag(&key, nonce, ciphertext);
        // Compare every byte: a short-circuit here leaks how much of the tag
        // matched, and this tag is the only integrity check the store has.
        let mut diff = 0u8;
        for (left, right) in stored.iter().zip(expected.iter()) {
            diff |= left ^ right;
        }
        if diff != 0 {
            return Err(Error::Rejected(
                "this credential was sealed with another key (credential.key was replaced)".into(),
            ));
        }
        Ok(xor(ciphertext, &keystream(&key, nonce, ciphertext.len())))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "anvil-secrets-{name}-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("scratch dir");
        dir
    }

    #[test]
    fn round_trips_a_secret() {
        let dir = scratch("round-trip");
        let secret = "sk-test-0123456789";
        let ciphertext = protect(&dir, secret).expect("protect");
        assert_ne!(ciphertext, secret.as_bytes());
        assert!(!String::from_utf8_lossy(&ciphertext).contains("sk-test"));
        assert_eq!(unprotect(&dir, &ciphertext).expect("unprotect"), secret);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn rejects_garbage_ciphertext() {
        let dir = scratch("garbage");
        let error = unprotect(&dir, &[0u8, 1, 2, 3]).expect_err("garbage");
        // A truncated blob is a format problem on Unix and a DPAPI failure on
        // Windows; both are refusals, which is the property that matters.
        assert!(
            matches!(error.code(), "format" | "store"),
            "unexpected code: {}",
            error.code()
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// Two secrets must not share a keystream: the same plaintext seals to
    /// different bytes, which is the whole point of the nonce.
    #[test]
    fn the_same_secret_seals_differently_each_time() {
        let dir = scratch("nonce");
        let first = protect(&dir, "sk-same").expect("protect");
        let second = protect(&dir, "sk-same").expect("protect");
        assert_ne!(first, second);
        assert_eq!(unprotect(&dir, &first).expect("first"), "sk-same");
        assert_eq!(unprotect(&dir, &second).expect("second"), "sk-same");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A ciphertext from another data directory must not open: that is what
    /// the tag is for, and the error has to say so instead of returning bytes.
    ///
    /// Windows has no key file — DPAPI is bound to the account, not to a
    /// directory — so this property only exists on the Unix store.
    #[cfg(not(windows))]
    #[test]
    fn another_key_is_refused() {
        let left = scratch("key-a");
        let right = scratch("key-b");
        let sealed = protect(&left, "sk-foreign").expect("protect");
        let error = unprotect(&right, &sealed).expect_err("wrong key");
        assert_eq!(error.code(), "rejected");
        let _ = std::fs::remove_dir_all(&left);
        let _ = std::fs::remove_dir_all(&right);
    }
}
