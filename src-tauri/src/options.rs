//! Window and initial-route options, ported 1:1 from the Go host
//! (`cmd/anvil/main.go`: `parseWindowSize`, `initialURLFromEnv`).
//!
//! Defaults are the frozen R0-00 geometry: 1180x820, minimum 360x560.
//! `ANVIL_WINDOW_WIDTH`/`ANVIL_WINDOW_HEIGHT` exist for native acceptance and
//! development only; invalid or out-of-range values log a warning and keep the
//! default. `ANVIL_INITIAL_URL` opens an isolated sample route and only accepts
//! same-origin absolute paths.

/// Default window width in logical pixels.
pub const DEFAULT_WIDTH: u32 = 1180;
/// Default window height in logical pixels.
pub const DEFAULT_HEIGHT: u32 = 820;
/// Minimum window width in logical pixels.
pub const MIN_WIDTH: u32 = 360;
/// Minimum window height in logical pixels.
pub const MIN_HEIGHT: u32 = 560;
/// Upper bound accepted from `ANVIL_WINDOW_WIDTH`.
pub const MAX_WIDTH: u32 = 3840;
/// Upper bound accepted from `ANVIL_WINDOW_HEIGHT`.
pub const MAX_HEIGHT: u32 = 2160;

/// Effective window geometry.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct WindowSize {
    /// Width in logical pixels.
    pub width: u32,
    /// Height in logical pixels.
    pub height: u32,
}

impl Default for WindowSize {
    fn default() -> Self {
        Self {
            width: DEFAULT_WIDTH,
            height: DEFAULT_HEIGHT,
        }
    }
}

/// Parses the development window override, keeping the default for invalid or
/// out-of-range values and returning one warning per rejected variable.
#[must_use]
pub fn parse_window_size(width_raw: &str, height_raw: &str) -> (WindowSize, Vec<String>) {
    let mut size = WindowSize::default();
    let mut warnings = Vec::new();
    size.width = dimension(
        width_raw,
        size.width,
        MIN_WIDTH,
        MAX_WIDTH,
        "ANVIL_WINDOW_WIDTH",
        &mut warnings,
    );
    size.height = dimension(
        height_raw,
        size.height,
        MIN_HEIGHT,
        MAX_HEIGHT,
        "ANVIL_WINDOW_HEIGHT",
        &mut warnings,
    );
    (size, warnings)
}

fn dimension(
    raw: &str,
    current: u32,
    min: u32,
    max: u32,
    name: &str,
    warnings: &mut Vec<String>,
) -> u32 {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return current;
    }
    match trimmed.parse::<u32>() {
        Ok(value) if (min..=max).contains(&value) => value,
        _ => {
            warnings.push(format!(
                "{name}={trimmed:?} is invalid or outside {min}..{max}; using {current}"
            ));
            current
        }
    }
}

/// Parses `ANVIL_INITIAL_URL`. Only same-origin absolute paths are accepted;
/// malformed values fall back to `/` and produce a `window-options:` warning.
#[must_use]
pub fn initial_url(raw: &str) -> (String, Option<String>) {
    let value = raw.trim();
    if value.is_empty() {
        return ("/".to_string(), None);
    }
    let invalid = value.contains("://")
        || value.starts_with("//")
        || value.chars().any(char::is_whitespace)
        || !value.starts_with('/');
    if invalid {
        (
            "/".to_string(),
            Some(format!(
                "window-options: invalid ANVIL_INITIAL_URL {raw:?}; using /"
            )),
        )
    } else {
        (value.to_string(), None)
    }
}
