//! Persisted host preferences at `<dataDir>/preferences.json`.
//!
//! Format, defaults, normalization and logging match the Go host
//! (`cmd/anvil/theme.go`): `{"theme", "language"}`, 2-space indent, trailing
//! newline, `tmp` + rename replacement, defaults `system`/`zh-CN`, unknown
//! values normalized and logged.

use std::io;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use serde::{Deserialize, Serialize};

use crate::log::HostLog;

/// File name inside the data directory.
pub const PREFERENCES_FILE: &str = "preferences.json";
/// Theme mode that follows the operating system.
pub const THEME_SYSTEM: &str = "system";
/// Forced light theme.
pub const THEME_LIGHT: &str = "light";
/// Forced dark theme.
pub const THEME_DARK: &str = "dark";
/// Chinese (default language).
pub const LANGUAGE_ZH_CN: &str = "zh-CN";
/// English.
pub const LANGUAGE_EN_US: &str = "en-US";

/// Persisted theme mode and language.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Preferences {
    /// Theme mode: `system`, `light` or `dark`.
    #[serde(default)]
    pub theme: String,
    /// Language: `zh-CN` or `en-US`.
    #[serde(default)]
    pub language: String,
}

impl Default for Preferences {
    fn default() -> Self {
        Self {
            theme: THEME_SYSTEM.to_string(),
            language: LANGUAGE_ZH_CN.to_string(),
        }
    }
}

/// Normalizes a theme mode (case-insensitive, trimmed) or returns `None`.
#[must_use]
pub fn normalize_theme(value: &str) -> Option<&'static str> {
    match value.trim().to_ascii_lowercase().as_str() {
        "system" => Some(THEME_SYSTEM),
        "light" => Some(THEME_LIGHT),
        "dark" => Some(THEME_DARK),
        _ => None,
    }
}

/// Normalizes a language or returns `None`.
#[must_use]
pub fn normalize_language(value: &str) -> Option<&'static str> {
    match value {
        LANGUAGE_ZH_CN => Some(LANGUAGE_ZH_CN),
        LANGUAGE_EN_US => Some(LANGUAGE_EN_US),
        _ => None,
    }
}

/// Resolves the effective light/dark value for a mode.
#[must_use]
pub fn resolve_theme(mode: &str, system_dark: bool) -> &'static str {
    match mode {
        THEME_DARK => THEME_DARK,
        THEME_LIGHT => THEME_LIGHT,
        _ => {
            if system_dark {
                THEME_DARK
            } else {
                THEME_LIGHT
            }
        }
    }
}

/// Serializes preferences exactly like the Go host: 2-space indent + `\n`.
#[must_use]
pub fn preferences_json(preferences: &Preferences) -> String {
    let mut body = serde_json::to_string_pretty(preferences)
        .expect("preferences always serialize to a JSON object");
    body.push('\n');
    body
}

/// Thread-safe preference store backed by one file.
pub struct PreferenceStore {
    path: PathBuf,
    preferences: Mutex<Preferences>,
}

impl PreferenceStore {
    /// Loads `<data_dir>/preferences.json`, logging and falling back to
    /// defaults on read/parse/validation problems (never fails to start).
    #[must_use]
    pub fn load(data_dir: &Path, log: &HostLog) -> Self {
        let path = data_dir.join(PREFERENCES_FILE);
        let mut preferences = Preferences::default();

        let body = match std::fs::read(&path) {
            Ok(body) => Some(body),
            Err(error) if error.kind() == io::ErrorKind::NotFound => None,
            Err(error) => {
                log.info(&format!(
                    "theme-options: read preferences: {error}; using system"
                ));
                None
            }
        };

        if let Some(body) = body {
            match serde_json::from_slice::<Preferences>(&body) {
                Ok(parsed) => {
                    match normalize_theme(&parsed.theme) {
                        Some(mode) => preferences.theme = mode.to_string(),
                        None => log.info(&format!(
                            "theme-options: unknown theme {:?}; using system",
                            parsed.theme
                        )),
                    }
                    match normalize_language(&parsed.language) {
                        Some(language) => preferences.language = language.to_string(),
                        None if !parsed.language.is_empty() => log.info(&format!(
                            "i18n-options: unknown language {:?}; using zh-CN",
                            parsed.language
                        )),
                        None => {}
                    }
                }
                Err(error) => log.info(&format!(
                    "theme-options: invalid preferences file: {error}; using system"
                )),
            }
        }

        Self {
            path,
            preferences: Mutex::new(preferences),
        }
    }

    /// Path of the preferences file.
    #[must_use]
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Current in-process preferences.
    #[must_use]
    pub fn snapshot(&self) -> Preferences {
        self.preferences
            .lock()
            .map(|guard| guard.clone())
            .unwrap_or_default()
    }

    /// Validates and persists a theme mode.
    pub fn set_theme(&self, raw: &str) -> Result<Preferences, String> {
        let Some(mode) = normalize_theme(raw) else {
            return Err(format!("unknown theme {raw:?}"));
        };
        let snapshot = {
            let mut guard = self
                .preferences
                .lock()
                .map_err(|_| "preference state is poisoned".to_string())?;
            guard.theme = mode.to_string();
            guard.clone()
        };
        self.write(&snapshot)
            .map_err(|error| format!("write {}: {error}", self.path.display()))?;
        Ok(snapshot)
    }

    /// Validates and persists a language.
    pub fn set_language(&self, raw: &str, log: &HostLog) -> Result<Preferences, String> {
        let Some(language) = normalize_language(raw) else {
            return Err(format!("unknown language {raw:?}"));
        };
        let snapshot = {
            let mut guard = self
                .preferences
                .lock()
                .map_err(|_| "preference state is poisoned".to_string())?;
            guard.language = language.to_string();
            guard.clone()
        };
        self.write(&snapshot)
            .map_err(|error| format!("write {}: {error}", self.path.display()))?;
        log.info(&format!("i18n-options: language={language}"));
        Ok(snapshot)
    }

    fn write(&self, preferences: &Preferences) -> io::Result<()> {
        let body = preferences_json(preferences);
        let tmp = self.path.with_extension("json.tmp");
        std::fs::write(&tmp, body)?;
        if let Err(error) = std::fs::rename(&tmp, &self.path) {
            let _ = std::fs::remove_file(&tmp);
            return Err(error);
        }
        Ok(())
    }
}
