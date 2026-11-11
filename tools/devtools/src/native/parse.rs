//! Parsers for the native desktop evidence.
//!
//! Ported from `internal/acceptance/runner_{shell,theme,i18n}_parse.go`. The Go
//! runner used regular expressions; these are hand-written scanners so the
//! devtools crate keeps a small dependency set. Each parser accepts the same
//! inputs as its Go counterpart, including application log lines that carry a
//! timestamp prefix.

use std::path::Path;

use serde::Deserialize;

/// One `layout-probe:` line emitted by the frontend.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct LayoutProbe {
    pub viewport: String,
    pub scroll_width: i64,
    pub client_width: i64,
    pub overflow: bool,
}

/// `window-options: requested=WxH min=WxH` from the host log.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub(crate) struct WindowGeometry {
    pub width: i64,
    pub height: i64,
    pub min_width: i64,
    pub min_height: i64,
}

/// `theme: mode=… resolved=… bg=…` from the frontend.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct ThemeLogEntry {
    pub mode: String,
    pub resolved: String,
    pub background: String,
}

/// `i18n: language=… htmlLang=…` from the frontend.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct LanguageLogEntry {
    pub language: String,
    pub html_lang: String,
}

/// Screen-space rectangle of an accessibility element.
#[derive(Debug, Clone, Copy, Default, Deserialize)]
pub(crate) struct ElementBounds {
    #[serde(default)]
    pub x: f64,
    #[serde(default)]
    pub y: f64,
    #[serde(default)]
    pub width: f64,
    #[serde(default)]
    pub height: f64,
}

impl ElementBounds {
    /// Center of the element, rounded down like the Go `int(...)` conversion.
    pub(crate) fn center(&self) -> (i64, i64) {
        (
            (self.x + self.width / 2.0) as i64,
            (self.y + self.height / 2.0) as i64,
        )
    }
}

/// One window reported by `agent-desktop list-windows`.
#[derive(Debug, Clone, Default, Deserialize)]
pub(crate) struct WindowBounds {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub pid: i64,
    #[serde(default)]
    pub process_instance: String,
    #[serde(default)]
    pub x: f64,
    #[serde(default)]
    pub y: f64,
    #[serde(default)]
    pub width: f64,
    #[serde(default)]
    pub height: f64,
}

/// One entry of `frontend/src/shell/routes.json`.
#[derive(Debug, Clone, Deserialize)]
pub(crate) struct ShellRoute {
    pub id: String,
    pub path: String,
    #[serde(default)]
    pub title: String,
    #[serde(default, rename = "navLabel")]
    pub nav_label: String,
    #[serde(default)]
    pub group: String,
    #[serde(default)]
    pub marker: String,
    #[serde(default, rename = "breadcrumbParent")]
    pub breadcrumb_parent: String,
    #[serde(default)]
    pub chrome: String,
}

/// One entry of `responsiveViewports` in the prototype manifest.
#[derive(Debug, Clone, Deserialize)]
pub(crate) struct ManifestViewport {
    pub width: i64,
    pub height: i64,
}

/// One accessibility match from `agent-desktop find`.
#[derive(Debug, Clone, Default, Deserialize)]
pub(crate) struct FindMatch {
    // The CLI emits `null` for every property a node does not have, so these
    // are optional rather than defaulted strings: `null` is not a missing key.
    #[serde(default)]
    pub name: Option<String>,
    #[serde(default)]
    pub role: Option<String>,
    #[serde(default, rename = "ref_id")]
    pub ref_id: Option<String>,
    #[serde(default)]
    pub bounds: Option<ElementBounds>,
}

#[derive(Debug, Deserialize)]
struct FindEnvelope {
    #[serde(default)]
    data: FindData,
}

#[derive(Debug, Default, Deserialize)]
struct FindData {
    #[serde(default)]
    r#match: Option<FindMatch>,
    #[serde(default)]
    matches: Vec<FindMatch>,
    #[serde(default)]
    count: i64,
    #[serde(default)]
    total_matches: i64,
}

fn find_matches(body: &[u8]) -> Vec<FindMatch> {
    let Some(envelope) = parse_json::<FindEnvelope>(body) else {
        return Vec::new();
    };
    let mut matches = Vec::new();
    if let Some(single) = envelope.data.r#match {
        matches.push(single);
    }
    matches.extend(envelope.data.matches);
    matches
}

/// The first `ref_id` reported by `find`.
pub(crate) fn parse_find_ref(body: &[u8]) -> Option<String> {
    find_matches(body)
        .into_iter()
        .filter_map(|entry| entry.ref_id)
        .find(|ref_id| !ref_id.is_empty())
}

/// The match count reported by `find`, preferring `total_matches`.
pub(crate) fn parse_find_count(body: &[u8]) -> i64 {
    let Some(envelope) = parse_json::<FindEnvelope>(body) else {
        return 0;
    };
    if envelope.data.total_matches > 0 {
        return envelope.data.total_matches;
    }
    if envelope.data.count > 0 {
        return envelope.data.count;
    }
    envelope.data.matches.len() as i64
}

/// Names of every match reported by `find`.
pub(crate) fn parse_find_names(body: &[u8]) -> Vec<String> {
    find_matches(body)
        .into_iter()
        .filter_map(|entry| entry.name)
        .filter(|name| !name.is_empty())
        .collect()
}

/// Every match with its role and bounds.
pub(crate) fn parse_find_matches(body: &[u8]) -> Vec<FindMatch> {
    find_matches(body)
}

/// `agent-desktop get <ref> --property bounds`.
pub(crate) fn parse_element_bounds(body: &[u8]) -> Option<ElementBounds> {
    #[derive(Deserialize)]
    struct Envelope {
        #[serde(default)]
        data: Data,
    }
    #[derive(Default, Deserialize)]
    struct Data {
        #[serde(default)]
        value: ElementBounds,
    }
    let envelope: Envelope = parse_json(body)?;
    let bounds = envelope.data.value;
    if bounds.width <= 0.0 || bounds.height <= 0.0 {
        return None;
    }
    Some(bounds)
}

/// Window list from `list-windows`.
pub(crate) fn parse_windows(body: &[u8]) -> Option<Vec<WindowBounds>> {
    #[derive(Deserialize)]
    struct Envelope {
        #[serde(default)]
        data: Vec<WindowListEntry>,
    }
    #[derive(Default, Deserialize)]
    struct WindowListEntry {
        #[serde(default)]
        id: String,
        #[serde(default)]
        title: String,
        #[serde(default)]
        pid: i64,
        #[serde(default)]
        process_instance: String,
        #[serde(default)]
        bounds: ElementBounds,
    }
    let envelope: Envelope = parse_json(body)?;
    Some(
        envelope
            .data
            .into_iter()
            .map(|entry| WindowBounds {
                id: entry.id,
                title: entry.title,
                pid: entry.pid,
                process_instance: entry.process_instance,
                x: entry.bounds.x,
                y: entry.bounds.y,
                width: entry.bounds.width,
                height: entry.bounds.height,
            })
            .collect(),
    )
}

/// First window, if any.
pub(crate) fn parse_first_window(body: &[u8]) -> Option<WindowBounds> {
    parse_windows(body).and_then(|windows| windows.into_iter().next())
}

/// Every `layout-probe:` line of the application log.
pub(crate) fn parse_layout_probes(log: &str) -> Vec<LayoutProbe> {
    let mut probes = Vec::new();
    for line in log.lines() {
        let Some(rest) = find_after(line.trim(), "layout-probe: ") else {
            continue;
        };
        let mut viewport = String::new();
        let mut scroll_width = 0;
        let mut client_width = 0;
        let mut overflow = false;
        for token in rest.split_whitespace() {
            if let Some(value) = token.strip_prefix("viewport=") {
                viewport = value.to_string();
            } else if let Some(value) = token.strip_prefix("scrollWidth=") {
                scroll_width = value.parse().unwrap_or(0);
            } else if let Some(value) = token.strip_prefix("clientWidth=") {
                client_width = value.parse().unwrap_or(0);
            } else if let Some(value) = token.strip_prefix("overflow=") {
                overflow = value == "true";
            }
        }
        if viewport.is_empty() {
            continue;
        }
        probes.push(LayoutProbe {
            viewport,
            scroll_width,
            client_width,
            overflow,
        });
    }
    probes
}

/// `window-options: requested=WxH min=WxH`.
pub(crate) fn parse_window_options(line: &str) -> Option<WindowGeometry> {
    let rest = find_after(line.trim(), "window-options: ")?;
    let mut geometry = WindowGeometry::default();
    let mut seen = false;
    for token in rest.split_whitespace() {
        if let Some(value) = token.strip_prefix("requested=") {
            let (width, height) = parse_pair(value)?;
            geometry.width = width;
            geometry.height = height;
            seen = true;
        } else if let Some(value) = token.strip_prefix("min=") {
            let (width, height) = parse_pair(value)?;
            geometry.min_width = width;
            geometry.min_height = height;
        }
    }
    seen.then_some(geometry)
}

/// Every `theme: mode=… resolved=… bg=…` line of the application log.
pub(crate) fn parse_theme_logs(text: &str) -> Vec<ThemeLogEntry> {
    let mut entries = Vec::new();
    for line in text.lines() {
        let Some(rest) = find_after(line.trim(), "theme: ") else {
            continue;
        };
        let mut entry = ThemeLogEntry::default();
        if let Some(index) = rest.find("bg=") {
            entry.background = rest[index + 3..].trim().to_string();
        }
        for token in rest.split_whitespace() {
            if let Some(value) = token.strip_prefix("mode=") {
                entry.mode = value.to_string();
            } else if let Some(value) = token.strip_prefix("resolved=") {
                entry.resolved = value.to_string();
            } else if token.starts_with("bg=") {
                break;
            }
        }
        if entry.mode.is_empty() || entry.resolved.is_empty() {
            continue;
        }
        entries.push(entry);
    }
    entries
}

/// The last theme entry, if the log has one.
pub(crate) fn latest_theme_log(text: &str) -> Option<ThemeLogEntry> {
    parse_theme_logs(text).pop()
}

/// Every `i18n: language=… htmlLang=…` line of the application log.
pub(crate) fn parse_i18n_logs(text: &str) -> Vec<LanguageLogEntry> {
    let mut entries = Vec::new();
    for line in text.lines() {
        let Some(rest) = find_after(line.trim(), "i18n: ") else {
            continue;
        };
        let mut entry = LanguageLogEntry::default();
        for token in rest.split_whitespace() {
            if let Some(value) = token.strip_prefix("language=") {
                entry.language = value.to_string();
            } else if let Some(value) = token.strip_prefix("htmlLang=") {
                entry.html_lang = value.to_string();
            }
        }
        if entry.language.is_empty() {
            continue;
        }
        entries.push(entry);
    }
    entries
}

/// The last `i18n-title: title=…` line.
pub(crate) fn parse_title_log(text: &str) -> Option<String> {
    let mut title = None;
    for line in text.lines() {
        if let Some(rest) = find_after(line.trim(), "i18n-title: title=") {
            title = Some(rest.trim().to_string());
        }
    }
    title
}

/// The `language` field of `preferences.json`.
pub(crate) fn parse_language_preference(body: &[u8]) -> Option<String> {
    #[derive(Deserialize)]
    struct Preference {
        #[serde(default)]
        language: String,
    }
    let preference: Preference = serde_json::from_slice(body).ok()?;
    (!preference.language.is_empty()).then_some(preference.language)
}

/// The first `0x…` DWORD printed by `reg query`.
pub(crate) fn parse_reg_apps_use_light(output: &str) -> Option<i64> {
    let index = output.find("0x")?;
    let digits: String = output[index + 2..]
        .chars()
        .take_while(char::is_ascii_hexdigit)
        .collect();
    if digits.is_empty() {
        return None;
    }
    i64::from_str_radix(&digits, 16).ok()
}

/// The nine screens declared by the shell.
pub(crate) fn read_shell_routes(repo: &Path) -> Result<Vec<ShellRoute>, String> {
    #[derive(Deserialize)]
    struct Document {
        #[serde(default)]
        screens: Vec<ShellRoute>,
    }
    let path = repo
        .join("frontend")
        .join("src")
        .join("shell")
        .join("routes.json");
    let body = std::fs::read(&path)
        .map_err(|err| format!("read shell routes ({}): {err}", path.display()))?;
    let document: Document = serde_json::from_slice(&body)
        .map_err(|err| format!("parse shell routes ({}): {err}", path.display()))?;
    if document.screens.len() != 9 {
        return Err(format!(
            "shell routes declares {} screens, want 9",
            document.screens.len()
        ));
    }
    Ok(document.screens)
}

/// The responsive viewport matrix of the prototype manifest.
pub(crate) fn read_manifest_viewports(repo: &Path) -> Result<Vec<ManifestViewport>, String> {
    #[derive(Deserialize)]
    struct Manifest {
        #[serde(default, rename = "responsiveViewports")]
        responsive_viewports: Vec<ManifestViewport>,
    }
    let path = repo
        .join("docs")
        .join("ui")
        .join("Agent-客户端产品原型")
        .join("DESIGN-MANIFEST.json");
    let body = std::fs::read(&path)
        .map_err(|err| format!("read prototype manifest ({}): {err}", path.display()))?;
    let manifest: Manifest = serde_json::from_slice(&body)
        .map_err(|err| format!("parse prototype manifest ({}): {err}", path.display()))?;
    if manifest.responsive_viewports.is_empty() {
        return Err("prototype manifest declares no responsive viewports".to_string());
    }
    Ok(manifest.responsive_viewports)
}

/// Parse the trailing JSON object of a step log.
///
/// The launched client inherits agent-desktop's stdout, so a step log can hold
/// application lines before the JSON envelope; the first parsable object wins.
pub(crate) fn parse_json<T: for<'de> Deserialize<'de>>(body: &[u8]) -> Option<T> {
    let text = String::from_utf8_lossy(body);
    let trimmed = text.trim();
    for (index, byte) in trimmed.bytes().enumerate() {
        if byte != b'{' {
            continue;
        }
        let candidate = &trimmed[index..];
        if let Ok(value) = serde_json::from_str::<T>(candidate) {
            return Some(value);
        }
    }
    None
}

/// Everything after `needle`, or `None` when the line does not contain it.
fn find_after<'a>(line: &'a str, needle: &str) -> Option<&'a str> {
    line.find(needle).map(|index| &line[index + needle.len()..])
}

/// Parse `WxH`.
fn parse_pair(value: &str) -> Option<(i64, i64)> {
    let (width, height) = value.split_once('x')?;
    Some((width.parse().ok()?, height.parse().ok()?))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn layout_probes_are_read_from_timestamped_lines() {
        let log = "2026-11-11T12:00:00Z layout-probe: viewport=1180x820 scrollWidth=1180 clientWidth=1180 overflow=false\n\
                   2026-11-11T12:00:01Z layout-probe: viewport=390x844 scrollWidth=402 clientWidth=390 overflow=true\n";
        let probes = parse_layout_probes(log);
        assert_eq!(probes.len(), 2);
        assert_eq!(probes[0].viewport, "1180x820");
        assert!(!probes[0].overflow);
        assert!(probes[1].overflow);
        assert_eq!(probes[1].scroll_width, 402);
        assert_eq!(probes[1].client_width, 390);
    }

    #[test]
    fn window_options_record_the_requested_geometry() {
        let geometry = parse_window_options(
            "2026-11-11T12:00:00Z window-options: requested=390x844 min=360x560",
        )
        .expect("geometry");
        assert_eq!(geometry.width, 390);
        assert_eq!(geometry.height, 844);
        assert_eq!(geometry.min_width, 360);
        assert_eq!(geometry.min_height, 560);
        assert!(parse_window_options("no options here").is_none());
    }

    #[test]
    fn theme_entries_keep_the_background_colour() {
        let log = "2026-11-11T12:00:00Z theme: mode=dark resolved=dark bg=rgb(20, 20, 24)\n\
                   2026-11-11T12:00:01Z theme: mode=system resolved=light bg=rgb(250, 250, 252)\n";
        let entries = parse_theme_logs(log);
        assert_eq!(entries.len(), 2);
        assert_eq!(entries[0].mode, "dark");
        assert_eq!(entries[0].background, "rgb(20, 20, 24)");
        assert_eq!(latest_theme_log(log).expect("latest").resolved, "light");
    }

    #[test]
    fn language_entries_and_titles_are_read_from_the_log() {
        let log = "2026-11-11T12:00:00Z i18n: language=zh-CN htmlLang=zh-CN\n\
                   2026-11-11T12:00:01Z i18n: language=en-US htmlLang=en-US\n\
                   2026-11-11T12:00:02Z i18n-title: title=Settings · Anvil\n";
        let entries = parse_i18n_logs(log);
        assert_eq!(entries.len(), 2);
        assert_eq!(entries[1].html_lang, "en-US");
        assert_eq!(parse_title_log(log).expect("title"), "Settings · Anvil");
        assert!(parse_title_log("nothing").is_none());
    }

    #[test]
    fn registry_output_yields_the_dword() {
        assert_eq!(
            parse_reg_apps_use_light("    AppsUseLightTheme    REG_DWORD    0x1"),
            Some(1)
        );
        assert_eq!(
            parse_reg_apps_use_light("    AppsUseLightTheme    REG_DWORD    0x0"),
            Some(0)
        );
        assert_eq!(parse_reg_apps_use_light("ERROR: not found"), None);
    }

    #[test]
    fn find_replies_expose_refs_names_and_counts() {
        let body = br#"{"ok":true,"data":{"match":{"name":"Anvil","role":"link","ref_id":"@s1:e1"},"matches":[{"name":"Anvil","ref_id":"@s1:e1"}],"count":1,"total_matches":4}}"#;
        assert_eq!(parse_find_ref(body).as_deref(), Some("@s1:e1"));
        assert_eq!(parse_find_count(body), 4);
        // `match` and `matches` are both collected, exactly like the Go
        // parser, so an exact-name check cannot miss the single-match reply.
        assert_eq!(
            parse_find_names(body),
            vec!["Anvil".to_string(), "Anvil".to_string()]
        );

        // The CLI writes `null` for absent properties; a plain `String` field
        // would fail the whole envelope and silently hide every match.
        let with_nulls = br#"{"ok":true,"data":{"matches":[{"bounds":null,"name":null,"ref_id":null,"role":"group"},{"bounds":{"x":1,"y":2,"width":3,"height":4},"name":"R0-04 workbench \u00b7 /","ref_id":null,"role":"statictext"}],"total_matches":2}}"#;
        assert_eq!(parse_find_count(with_nulls), 2);
        assert_eq!(
            parse_find_names(with_nulls),
            vec!["R0-04 workbench · /".to_string()]
        );
        assert!(parse_find_ref(with_nulls).is_none());
    }

    #[test]
    fn element_bounds_require_a_positive_rectangle() {
        let body = br#"{"data":{"value":{"x":10,"y":20,"width":100,"height":32}}}"#;
        let bounds = parse_element_bounds(body).expect("bounds");
        assert_eq!(bounds.center(), (60, 36));
        assert!(parse_element_bounds(br#"{"data":{"value":{"width":0,"height":0}}}"#).is_none());
    }

    #[test]
    fn windows_are_flattened_from_the_bounds_object() {
        let body = br#"{"ok":true,"data":[{"id":"w1","title":"Anvil","pid":42,"process_instance":"i1","bounds":{"x":1,"y":2,"width":390,"height":844}}]}"#;
        let windows = parse_windows(body).expect("windows");
        assert_eq!(windows.len(), 1);
        assert_eq!(windows[0].pid, 42);
        assert_eq!(windows[0].width, 390.0);
        assert!(parse_first_window(br#"{"ok":true,"data":[]}"#).is_none());
    }
}
