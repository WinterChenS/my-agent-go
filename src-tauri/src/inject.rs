//! First-frame theme/language injection.
//!
//! The Go host served `index.html` with an inline `<script>` in `<head>` so the
//! persisted theme, resolved light/dark value and language were applied before
//! CSS and the bundle ran (no light flash, no wrong language). Tauri v2 has two
//! equivalent hooks and R0-RS02 uses both:
//!
//! 1. `WebviewWindowBuilder::initialization_script` — runs after the global
//!    object exists but before the HTML document is parsed.
//! 2. The runtime asset provider rewrites `/index.html` on every request, so a
//!    reload inside one session also gets the current values.
//!
//! Both produce the same script from [`InitialState`].

use serde::Serialize;

use crate::prefs::{Preferences, resolve_theme};

/// Height of the window's own title-bar strip, in logical pixels.
///
/// With a transparent title bar macOS draws no chrome of its own, so the strip
/// is painted by the page — and the traffic lights land on top of it. The page
/// reserves exactly this much space so they sit on the chrome colour instead of
/// the sidebar's logo. Measured as the gap the opaque title bar used to take:
/// an 820 pt window laid its page out in 792 pt.
pub const TITLEBAR_INSET: u32 = if cfg!(target_os = "macos") { 28 } else { 0 };

/// Values injected before the first frame.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct InitialState {
    /// Theme mode (`system`, `light`, `dark`).
    pub mode: String,
    /// Resolved theme (`light`, `dark`).
    pub resolved: String,
    /// Language (`zh-CN`, `en-US`).
    pub language: String,
    /// Whether the OS currently reports a dark app theme.
    pub system_dark: bool,
    /// Space to leave for the window's own title bar, in logical pixels.
    pub window_top_inset: u32,
}

impl InitialState {
    /// Builds the injected state from the persisted preferences.
    #[must_use]
    pub fn new(preferences: &Preferences, system_dark: bool) -> Self {
        Self {
            mode: preferences.theme.clone(),
            resolved: resolve_theme(&preferences.theme, system_dark).to_string(),
            language: preferences.language.clone(),
            system_dark,
            window_top_inset: TITLEBAR_INSET,
        }
    }
}

const SCRIPT_TEMPLATE: &str = r#"(function(){var state=__STATE__;window.__ANVIL_INITIAL_THEME__={mode:state.mode,resolved:state.resolved,systemDark:state.systemDark};window.__ANVIL_INITIAL_LANGUAGE__=state.language;function apply(){var el=document.documentElement;if(!el){return false;}el.lang=state.language;el.dataset.theme=state.resolved;el.style.colorScheme=state.resolved;el.style.setProperty('--window-top-inset',state.windowTopInset+'px');return true;}if(!apply()){var observer=new MutationObserver(function(){if(apply()){observer.disconnect();}});observer.observe(document,{childList:true,subtree:true});document.addEventListener('DOMContentLoaded',apply,{once:true});}})();"#;

/// JavaScript applied before any page script runs.
#[must_use]
pub fn initialization_script(state: &InitialState) -> String {
    let payload =
        serde_json::to_string(state).expect("initial injection state always serializes to JSON");
    SCRIPT_TEMPLATE.replace("__STATE__", &payload)
}

/// Re-reserves the title-bar strip after the fact.
///
/// macOS drops its title bar in fullscreen, and with it the strip the page keeps
/// for the traffic lights; leaving the space reserved would draw a band of
/// nothing above a window that has no title bar.
#[must_use]
pub fn top_inset_script(px: u32) -> String {
    format!("document.documentElement.style.setProperty('--window-top-inset','{px}px')")
}

/// Injects the script into `<index.html>` before `</head>` and sets the `<html>`
/// language attribute, mirroring the Go host's `injectedIndex`.
#[must_use]
pub fn render_index(html: &str, state: &InitialState) -> String {
    let script = format!("<script>{}</script>", initialization_script(state));
    let localized = replace_html_language(html, &state.language);
    match localized.find("</head>") {
        Some(index) => format!("{}{}{}", &localized[..index], script, &localized[index..]),
        None => format!("{script}{localized}"),
    }
}

fn replace_html_language(html: &str, language: &str) -> String {
    const OPEN: &str = "<html lang=\"";
    let Some(position) = html.find(OPEN) else {
        return html.to_string();
    };
    let value_start = position + OPEN.len();
    let Some(relative_end) = html[value_start..].find('"') else {
        return html.to_string();
    };
    let value_end = value_start + relative_end;
    format!("{}{}{}", &html[..value_start], language, &html[value_end..])
}
