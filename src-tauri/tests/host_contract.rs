//! R0-RS02 host contract tests.
//!
//! These tests exercise the behaviour the retired Go host provided
//! (`cmd/anvil/main.go`, `theme.go`, `theme_system_windows.go`) without opening a
//! window: preflight, preferences, logging, initial injection, window options,
//! capability whitelist and the runtime asset provider.
//!
//! `HostConfig::prepare` is the only path that creates the data/WebView2/log
//! directories, and `desktop::run` only builds the Tauri app after it returns
//! `Ok`, so a rejected configuration can never create a window.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};

use anvil::assets::DiskAssets;
use anvil::host::{
    DATA_DIR_ENV, FRONTEND_DIST_ENV, HostConfig, HostError, INITIAL_URL_ENV, WEBVIEW_DIR_ENV,
    WINDOW_HEIGHT_ENV, WINDOW_WIDTH_ENV, preferences_path, resolve_frontend_dist,
};
use anvil::inject::{InitialState, initialization_script, render_index};
use anvil::log::{HostLog, utc_timestamp};
use anvil::options::{
    DEFAULT_HEIGHT, DEFAULT_WIDTH, MIN_HEIGHT, MIN_WIDTH, initial_url, parse_window_size,
};
use anvil::prefs::{
    LANGUAGE_EN_US, LANGUAGE_ZH_CN, PREFERENCES_FILE, PreferenceStore, Preferences, THEME_DARK,
    THEME_LIGHT, THEME_SYSTEM, normalize_language, normalize_theme, preferences_json,
    resolve_theme,
};
use anvil::state::sanitize_probe;
use anvil::system_theme;
use tauri::utils::assets::AssetKey;

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

static UNIQUE: AtomicU64 = AtomicU64::new(0);

fn volume_of(path: &Path) -> String {
    let text = path.to_string_lossy();
    if text.len() >= 2 && text.as_bytes()[1] == b':' {
        text[..2].to_ascii_uppercase()
    } else {
        String::from("<none>")
    }
}

struct TestDir {
    path: PathBuf,
    junctions: Vec<PathBuf>,
}

impl TestDir {
    fn new(tag: &str) -> Self {
        let base = std::env::temp_dir();
        assert_eq!(
            volume_of(&base),
            "D:",
            "test TEMP {} is not on D:; load scripts/env-rs.ps1",
            base.display()
        );
        let unique = UNIQUE.fetch_add(1, Ordering::SeqCst);
        let path = base.join(format!("anvil-rs02-{tag}-{}-{unique}", std::process::id()));
        fs::create_dir_all(&path).expect("create fixture");
        Self {
            path,
            junctions: Vec::new(),
        }
    }

    fn path(&self) -> &Path {
        &self.path
    }

    fn join(&self, name: &str) -> PathBuf {
        self.path.join(name)
    }

    fn junction(&mut self, name: &str, target: &Path) -> PathBuf {
        let link = self.path.join(name);
        let output = Command::new("cmd")
            .args(["/c", "mklink", "/J"])
            .arg(&link)
            .arg(target)
            .output()
            .expect("run mklink /J");
        assert!(
            output.status.success(),
            "mklink /J {} {} failed: {}{}",
            link.display(),
            target.display(),
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        self.junctions.push(link.clone());
        link
    }
}

impl Drop for TestDir {
    fn drop(&mut self) {
        for junction in &self.junctions {
            let _ = fs::remove_dir(junction);
        }
        let _ = fs::remove_dir_all(&self.path);
    }
}

fn env_from(pairs: &[(&str, &str)]) -> impl Fn(&str) -> Option<String> + 'static {
    let map: BTreeMap<String, String> = pairs
        .iter()
        .map(|(key, value)| ((*key).to_string(), (*value).to_string()))
        .collect();
    move |name: &str| map.get(name).cloned()
}

fn make_dist(dir: &Path) {
    fs::create_dir_all(dir.join("assets")).expect("create dist assets");
    fs::write(
        dir.join("index.html"),
        "<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><title>Anvil</title></head><body><div id=\"root\"></div></body></html>",
    )
    .expect("write index.html");
    fs::write(dir.join("assets/app.js"), "console.log('anvil');").expect("write asset");
}

fn read_log(path: &Path) -> String {
    fs::read_to_string(path).expect("read host log")
}

fn host_error(result: Result<HostConfig, HostError>) -> HostError {
    match result {
        Ok(_) => panic!("expected a preflight error"),
        Err(error) => error,
    }
}

// ---------------------------------------------------------------------------
// Window size options (Go: parseWindowSize)
// ---------------------------------------------------------------------------

#[test]
fn window_size_defaults_match_the_frozen_geometry() {
    let (size, warnings) = parse_window_size("", "");
    assert_eq!((size.width, size.height), (DEFAULT_WIDTH, DEFAULT_HEIGHT));
    assert_eq!((size.width, size.height), (1180, 820));
    assert_eq!((MIN_WIDTH, MIN_HEIGHT), (360, 560));
    assert!(warnings.is_empty());
}

#[test]
fn window_size_accepts_valid_overrides() {
    let (size, warnings) = parse_window_size(" 1000 ", "700");
    assert_eq!((size.width, size.height), (1000, 700));
    assert!(warnings.is_empty());
}

#[test]
fn window_size_rejects_invalid_and_out_of_range_values() {
    let (size, warnings) = parse_window_size("100", "5000");
    assert_eq!((size.width, size.height), (DEFAULT_WIDTH, DEFAULT_HEIGHT));
    assert_eq!(warnings.len(), 2);
    assert_eq!(
        warnings[0],
        "ANVIL_WINDOW_WIDTH=\"100\" is invalid or outside 360..3840; using 1180"
    );
    assert_eq!(
        warnings[1],
        "ANVIL_WINDOW_HEIGHT=\"5000\" is invalid or outside 560..2160; using 820"
    );

    let (_, warnings) = parse_window_size("abc", "");
    assert_eq!(warnings.len(), 1);
    let (_, warnings) = parse_window_size("1180", "2161");
    assert_eq!(warnings.len(), 1);
    assert!(warnings[0].contains("ANVIL_WINDOW_HEIGHT"));
}

// ---------------------------------------------------------------------------
// Initial route (Go: initialURLFromEnv)
// ---------------------------------------------------------------------------

#[test]
fn initial_url_accepts_same_origin_paths() {
    assert_eq!(initial_url(""), ("/".to_string(), None));
    assert_eq!(initial_url("   "), ("/".to_string(), None));
    assert_eq!(initial_url("/chat"), ("/chat".to_string(), None));
    assert_eq!(initial_url(" /settings "), ("/settings".to_string(), None));
}

#[test]
fn initial_url_rejects_external_and_malformed_values() {
    for raw in ["https://evil.example/", "//evil.example/x", "chat", "/a b"] {
        let (value, warning) = initial_url(raw);
        assert_eq!(value, "/", "raw {raw:?}");
        let warning = warning.expect("warning");
        assert!(
            warning.starts_with("window-options: invalid ANVIL_INITIAL_URL"),
            "raw {raw:?}: {warning}"
        );
    }
}

// ---------------------------------------------------------------------------
// Preferences (Go: themeState load/normalize/write)
// ---------------------------------------------------------------------------

fn log_in(dir: &Path) -> HostLog {
    HostLog::open(&dir.join("logs")).expect("open host log")
}

#[test]
fn preferences_default_to_system_and_zh_cn_when_the_file_is_missing() {
    let dir = TestDir::new("prefs-missing");
    let log = log_in(dir.path());
    let store = PreferenceStore::load(dir.path(), &log);
    assert_eq!(
        store.snapshot(),
        Preferences {
            theme: THEME_SYSTEM.to_string(),
            language: LANGUAGE_ZH_CN.to_string(),
        }
    );
    assert!(!read_log(log.path()).contains("theme-options:"));
}

#[test]
fn preferences_fall_back_and_log_on_invalid_json() {
    let dir = TestDir::new("prefs-invalid");
    let log = log_in(dir.path());
    fs::write(dir.join(PREFERENCES_FILE), b"{ not json").expect("write invalid prefs");
    let store = PreferenceStore::load(dir.path(), &log);
    assert_eq!(store.snapshot().theme, THEME_SYSTEM);
    assert_eq!(store.snapshot().language, LANGUAGE_ZH_CN);
    assert!(read_log(log.path()).contains("theme-options: invalid preferences file"));
}

#[test]
fn preferences_normalize_unknown_values_and_log_them() {
    let dir = TestDir::new("prefs-unknown");
    let log = log_in(dir.path());
    fs::write(
        dir.join(PREFERENCES_FILE),
        br#"{"theme":"neon","language":"fr-FR"}"#,
    )
    .expect("write prefs");
    let store = PreferenceStore::load(dir.path(), &log);
    assert_eq!(store.snapshot().theme, THEME_SYSTEM);
    assert_eq!(store.snapshot().language, LANGUAGE_ZH_CN);
    let log_text = read_log(log.path());
    assert!(log_text.contains("theme-options: unknown theme \"neon\"; using system"));
    assert!(log_text.contains("i18n-options: unknown language \"fr-FR\"; using zh-CN"));
}

#[test]
fn preferences_accept_valid_values_and_case_insensitive_theme() {
    let dir = TestDir::new("prefs-valid");
    let log = log_in(dir.path());
    fs::write(
        dir.join(PREFERENCES_FILE),
        br#"{"theme":" DARK ","language":"en-US"}"#,
    )
    .expect("write prefs");
    let store = PreferenceStore::load(dir.path(), &log);
    assert_eq!(store.snapshot().theme, THEME_DARK);
    assert_eq!(store.snapshot().language, LANGUAGE_EN_US);
    assert!(!read_log(log.path()).contains("unknown"));
}

#[test]
fn preferences_write_is_two_space_json_with_trailing_newline_and_atomic_replace() {
    let dir = TestDir::new("prefs-write");
    let log = log_in(dir.path());
    let store = PreferenceStore::load(dir.path(), &log);

    store.set_theme("dark").expect("set theme");
    let path = dir.join(PREFERENCES_FILE);
    let body = fs::read_to_string(&path).expect("read preferences");
    assert_eq!(
        body,
        "{\n  \"theme\": \"dark\",\n  \"language\": \"zh-CN\"\n}\n"
    );
    assert!(
        !path.with_extension("json.tmp").exists(),
        "tmp file left behind"
    );
    assert_eq!(store.snapshot().theme, THEME_DARK);

    store.set_language("en-US", &log).expect("set language");
    let body = fs::read_to_string(&path).expect("read preferences");
    assert_eq!(
        body,
        "{\n  \"theme\": \"dark\",\n  \"language\": \"en-US\"\n}\n"
    );
    assert!(read_log(log.path()).contains("i18n-options: language=en-US"));
    assert_eq!(
        preferences_json(&store.snapshot()),
        "{\n  \"theme\": \"dark\",\n  \"language\": \"en-US\"\n}\n"
    );
}

#[test]
fn preferences_reject_unknown_values_without_writing() {
    let dir = TestDir::new("prefs-reject");
    let log = log_in(dir.path());
    let store = PreferenceStore::load(dir.path(), &log);
    let error = store.set_theme("neon").expect_err("unknown theme");
    assert!(error.contains("unknown theme"), "{error}");
    assert!(
        !dir.join(PREFERENCES_FILE).exists(),
        "invalid value was written"
    );
    let error = store
        .set_language("fr-FR", &log)
        .expect_err("unknown language");
    assert!(error.contains("unknown language"), "{error}");
    assert_eq!(normalize_theme("Light"), Some(THEME_LIGHT));
    assert_eq!(normalize_theme("SYSTEM"), Some(THEME_SYSTEM));
    assert_eq!(normalize_theme("neon"), None);
    assert_eq!(normalize_language("zh-CN"), Some(LANGUAGE_ZH_CN));
    assert_eq!(normalize_language("en-GB"), None);
}

#[test]
fn theme_resolution_matches_the_host() {
    assert_eq!(resolve_theme(THEME_SYSTEM, true), THEME_DARK);
    assert_eq!(resolve_theme(THEME_SYSTEM, false), THEME_LIGHT);
    assert_eq!(resolve_theme(THEME_LIGHT, true), THEME_LIGHT);
    assert_eq!(resolve_theme(THEME_DARK, false), THEME_DARK);
}

// ---------------------------------------------------------------------------
// Log format and prefixes
// ---------------------------------------------------------------------------

#[test]
fn log_timestamp_is_utc_and_go_compatible() {
    let epoch = std::time::UNIX_EPOCH + std::time::Duration::from_secs(1_700_000_000);
    assert_eq!(utc_timestamp(epoch), "2023/11/14 22:13:20");
    let epoch = std::time::UNIX_EPOCH;
    assert_eq!(utc_timestamp(epoch), "1970/01/01 00:00:00");
}

#[test]
fn host_log_appends_timestamped_lines_to_the_data_directory() {
    let dir = TestDir::new("log");
    let log = log_in(dir.path());
    assert!(log.path().ends_with(Path::new("logs").join("anvil.log")));
    log.info("theme: mode=dark resolved=dark bg=rgb(0, 0, 0)");
    log.info("layout-probe: viewport=1180x820");
    let text = read_log(log.path());
    let lines: Vec<&str> = text.lines().collect();
    assert_eq!(lines.len(), 2);
    assert!(lines[0].starts_with("20"), "line: {}", lines[0]);
    assert!(lines[0].contains(" theme: mode=dark resolved=dark bg=rgb(0, 0, 0)"));
    assert!(lines[1].contains(" layout-probe: viewport=1180x820"));
}

#[test]
fn probe_sanitizing_collapses_newlines_and_caps_length() {
    assert_eq!(sanitize_probe("a\r\nb\tc"), "a  b\tc");
    assert_eq!(sanitize_probe("   "), "");
    let long = "x".repeat(600);
    assert_eq!(sanitize_probe(&long).chars().count(), 512);
}

/// The native runner tails `anvil.log` while the client runs, so the log handle
/// must not lock the file. Go's `os.OpenFile` shares read/write/delete; the
/// default `std::fs` mode does not, and the R0-RS02 acceptance hit exactly that
/// ("file is being used by another process") when reading the live log.
#[test]
fn host_log_stays_readable_while_the_host_holds_it() {
    let dir = TestDir::new("log-sharing");
    let log = log_in(dir.path());
    log.info("starting: data=D:\\probe");

    let text = std::fs::read_to_string(log.path()).expect("the live log must be readable");
    assert!(text.contains("starting: data=D:\\probe"));

    // A plain write-mode reader (like `Get-Content`) must work too.
    let mut file = std::fs::File::open(log.path()).expect("open the live log for reading");
    let mut buffer = String::new();
    use std::io::Read as _;
    file.read_to_string(&mut buffer).expect("read the live log");
    assert!(buffer.contains("starting:"));

    log.info("theme-options: mode=system resolved=dark");
    let after = std::fs::read_to_string(log.path()).expect("still readable after append");
    assert_eq!(after.lines().count(), 2);
}

// ---------------------------------------------------------------------------
// First-frame injection
// ---------------------------------------------------------------------------

#[test]
fn initialization_script_contains_the_persisted_state() {
    let state = InitialState::new(
        &Preferences {
            theme: THEME_DARK.to_string(),
            language: LANGUAGE_EN_US.to_string(),
        },
        true,
    );
    let script = initialization_script(&state);
    assert!(script.contains("window.__ANVIL_INITIAL_THEME__={mode:state.mode,resolved:state.resolved,systemDark:state.systemDark};"));
    assert!(script.contains("\"mode\":\"dark\""));
    assert!(script.contains("\"resolved\":\"dark\""));
    assert!(script.contains("\"language\":\"en-US\""));
    assert!(script.contains("\"systemDark\":true"));
    assert!(script.contains("el.dataset.theme=state.resolved"));
    assert!(script.contains("el.style.colorScheme=state.resolved"));
    assert!(script.contains("el.lang=state.language"));
    // No whitespace/newline before the first frame work: a single-line script.
    assert!(!script.contains('\n'));
}

#[test]
fn render_index_injects_before_head_and_sets_the_language() {
    let state = InitialState::new(
        &Preferences {
            theme: THEME_SYSTEM.to_string(),
            language: LANGUAGE_EN_US.to_string(),
        },
        false,
    );
    let html =
        "<!doctype html><html lang=\"zh-CN\"><head><title>Anvil</title></head><body></body></html>";
    let rendered = render_index(html, &state);
    assert!(rendered.contains("<html lang=\"en-US\">"));
    let script_index = rendered.find("<script>").expect("script");
    let head_end = rendered.find("</head>").expect("head end");
    assert!(script_index < head_end, "script must be inside head");
    assert!(rendered.contains("\"resolved\":\"light\""));
    assert!(rendered.contains("__ANVIL_INITIAL_LANGUAGE__"));
}

#[test]
fn render_index_prepends_when_no_head_exists() {
    let state = InitialState::new(&Preferences::default(), false);
    let rendered = render_index("<html><body>hi</body></html>", &state);
    assert!(rendered.starts_with("<script>"));
    assert!(rendered.ends_with("</body></html>"));
}

// ---------------------------------------------------------------------------
// Preflight (Go: appDataDir/EnsureDir/routes)
// ---------------------------------------------------------------------------

struct EnvFixture {
    dir: TestDir,
}

impl EnvFixture {
    fn new(tag: &str) -> Self {
        let dir = TestDir::new(tag);
        let dist = dir.join("dist");
        make_dist(&dist);
        Self { dir }
    }

    fn data(&self) -> PathBuf {
        self.dir.join("data")
    }

    fn dist(&self) -> PathBuf {
        self.dir.join("dist")
    }

    fn pairs(&self) -> Vec<(&'static str, String)> {
        vec![
            (DATA_DIR_ENV, self.data().to_string_lossy().into_owned()),
            (
                FRONTEND_DIST_ENV,
                self.dist().to_string_lossy().into_owned(),
            ),
        ]
    }

    fn prepare(&self, extra: &[(&str, &str)]) -> Result<HostConfig, HostError> {
        let mut pairs = self.pairs();
        for (key, value) in extra {
            pairs.push((*key, (*value).to_string()));
        }
        let refs: Vec<(&str, &str)> = pairs.iter().map(|(k, v)| (*k, v.as_str())).collect();
        HostConfig::prepare(&env_from(&refs))
    }
}

#[test]
fn preflight_rejects_missing_empty_and_relative_data_directory() {
    let fixture = EnvFixture::new("preflight-invalid");
    let dist = fixture.dist().to_string_lossy().into_owned();
    let env = env_from(&[(FRONTEND_DIST_ENV, &dist)]);
    let error = host_error(HostConfig::prepare(&env));
    assert!(
        matches!(error, HostError::DataDirectory(_)),
        "got {error:?}"
    );
    assert!(error.to_string().contains("ANVIL_DATA_DIR is required"));

    for raw in ["   ", r"relative\data"] {
        let env = env_from(&[(DATA_DIR_ENV, raw), (FRONTEND_DIST_ENV, &dist)]);
        let error = host_error(HostConfig::prepare(&env));
        assert!(
            matches!(error, HostError::DataDirectory(_)),
            "{raw:?} gave {error:?}"
        );
    }
}

#[test]
fn preflight_rejects_other_drives_without_creating_anything() {
    let fixture = EnvFixture::new("preflight-c");
    let c_target = r"C:\Users\Administrator\AppData\Local\anvil-rs02-must-not-exist";
    let c_webview = r"C:\Users\Administrator\AppData\Local\anvil-rs02-webview-must-not-exist";
    let dist = fixture.dist().to_string_lossy().into_owned();
    let env = env_from(&[(DATA_DIR_ENV, c_target), (FRONTEND_DIST_ENV, &dist)]);
    let error = host_error(HostConfig::prepare(&env));
    assert!(matches!(error, HostError::DataDirectory(_)), "{error:?}");
    assert!(
        error.to_string().contains("resolves to volume C:"),
        "{error}"
    );
    assert!(!Path::new(c_target).exists(), "C: data dir was created");

    let data = fixture.data().to_string_lossy().into_owned();
    let env = env_from(&[
        (DATA_DIR_ENV, &data),
        (WEBVIEW_DIR_ENV, c_webview),
        (FRONTEND_DIST_ENV, &dist),
    ]);
    let error = host_error(HostConfig::prepare(&env));
    assert!(matches!(error, HostError::WebviewDirectory(_)), "{error:?}");
    assert!(!Path::new(c_webview).exists(), "C: webview dir was created");
}

#[test]
fn preflight_rejects_a_junction_to_another_volume() {
    let mut fixture = EnvFixture::new("preflight-junction");
    let link = fixture.dir.junction("to-c", Path::new(r"C:\"));
    let marker = link.join("anvil-rs02-marker");
    let dist = fixture.dist().to_string_lossy().into_owned();
    let marker_text = marker.to_string_lossy().into_owned();
    let env = env_from(&[(DATA_DIR_ENV, &marker_text), (FRONTEND_DIST_ENV, &dist)]);
    let error = host_error(HostConfig::prepare(&env));
    assert!(matches!(error, HostError::DataDirectory(_)), "{error:?}");
    assert!(
        error.to_string().contains("resolves to volume C:"),
        "{error}"
    );
    assert!(
        !Path::new(r"C:\anvil-rs02-marker").exists(),
        "wrote through junction"
    );
}

#[test]
fn preflight_creates_missing_data_webview_and_log_directories_on_d() {
    let fixture = EnvFixture::new("preflight-ok");
    let config = fixture.prepare(&[]).expect("valid preflight");
    assert_eq!(volume_of(&config.data_dir), "D:");
    assert!(config.data_dir.is_dir());
    assert_eq!(
        config.data_dir,
        anvil_paths::final_path(&fixture.data()).expect("final path")
    );
    assert_eq!(config.webview_dir, config.data_dir.join("webview2"));
    assert!(config.webview_dir.is_dir());
    assert_eq!(config.log_dir, config.data_dir.join("logs"));
    assert!(config.log.path().is_file());
    assert_eq!(config.size.width, DEFAULT_WIDTH);
    assert_eq!(config.size.height, DEFAULT_HEIGHT);
    assert_eq!(config.initial_url, "/");
    assert_eq!(config.initial_path(), "index.html");
    assert_eq!(
        preferences_path(&config.data_dir),
        config.data_dir.join(PREFERENCES_FILE)
    );
}

#[test]
fn preflight_honours_the_webview_override_and_initial_route() {
    let fixture = EnvFixture::new("preflight-override");
    let webview = fixture.dir.join("webview-custom");
    let webview_text = webview.to_string_lossy().into_owned();
    let config = fixture
        .prepare(&[
            (WEBVIEW_DIR_ENV, &webview_text),
            (INITIAL_URL_ENV, "/chat"),
            (WINDOW_WIDTH_ENV, "1000"),
            (WINDOW_HEIGHT_ENV, "700"),
        ])
        .expect("valid preflight");
    assert_eq!(
        config.webview_dir,
        anvil_paths::final_path(&webview).expect("final path")
    );
    assert!(config.webview_dir.is_dir());
    assert_eq!(config.initial_url, "/chat");
    assert_eq!(config.initial_path(), "chat");
    assert_eq!((config.size.width, config.size.height), (1000, 700));
    assert!(config.size_warnings.is_empty());
    assert!(config.initial_url_warning.is_none());
}

#[test]
fn preflight_loads_persisted_preferences_and_logs_startup_lines() {
    let fixture = EnvFixture::new("preflight-startup");
    fs::create_dir_all(fixture.data()).expect("create data dir");
    fs::write(
        fixture.data().join(PREFERENCES_FILE),
        "{\"theme\": \"dark\", \"language\": \"en-US\"}\n",
    )
    .expect("write prefs");
    let config = fixture
        .prepare(&[
            (WINDOW_WIDTH_ENV, "100"),
            (INITIAL_URL_ENV, "https://evil.example/x"),
        ])
        .expect("valid preflight");
    assert_eq!(config.preferences.snapshot().theme, THEME_DARK);
    assert_eq!(config.preferences.snapshot().language, LANGUAGE_EN_US);
    assert_eq!(config.size_warnings.len(), 1);
    assert!(config.initial_url_warning.is_some());

    config.log_startup(false);
    let log_text = read_log(config.log.path());
    assert!(
        log_text.contains("theme-options: mode=dark resolved=dark"),
        "{log_text}"
    );
    assert!(
        log_text.contains("i18n-options: language=en-US"),
        "{log_text}"
    );
    assert!(
        log_text.contains(
            "window-options: ANVIL_WINDOW_WIDTH=\"100\" is invalid or outside 360..3840; using 1180"
        ),
        "{log_text}"
    );
    assert!(
        log_text.contains(&format!(
            "starting: dist={} data={} webview={}",
            config.dist_dir.display(),
            config.data_dir.display(),
            config.webview_dir.display()
        )),
        "{log_text}"
    );
    assert!(
        log_text.contains("window-options: requested=1180x820 min=360x560"),
        "{log_text}"
    );
    assert!(
        log_text.contains(
            "window-options: invalid ANVIL_INITIAL_URL \"https://evil.example/x\"; using /"
        ),
        "{log_text}"
    );
    assert!(
        log_text.contains("window-options: initialURL=/"),
        "{log_text}"
    );
}

#[test]
fn frontend_dist_resolution_uses_the_override_then_falls_back() {
    let dir = TestDir::new("dist");
    let dist = dir.join("dist");
    make_dist(&dist);
    let text = dist.to_string_lossy().into_owned();
    let env = env_from(&[(FRONTEND_DIST_ENV, &text)]);
    let resolved = resolve_frontend_dist(&env, None, None).expect("override");
    assert_eq!(resolved, dist.canonicalize().expect("canonical"));

    let cwd = dir.join("cwd");
    let fallback = cwd.join("frontend").join("dist");
    make_dist(&fallback);
    let env = env_from(&[]);
    let resolved = resolve_frontend_dist(&env, Some(cwd.as_path()), None).expect("cwd fallback");
    assert_eq!(resolved, fallback.canonicalize().expect("canonical"));

    let empty = TestDir::new("dist-missing");
    let env = env_from(&[]);
    let error = resolve_frontend_dist(&env, Some(empty.path()), None).expect_err("missing dist");
    assert!(matches!(error, HostError::FrontendDist(_)), "{error:?}");
    assert!(error.to_string().contains("index.html"), "{error}");
}

// ---------------------------------------------------------------------------
// Runtime asset provider (serves the built frontend from disk)
// ---------------------------------------------------------------------------

#[test]
fn asset_provider_serves_the_frontend_and_rewrites_index() {
    let dir = TestDir::new("assets");
    let dist = dir.join("dist");
    make_dist(&dist);
    let assets = DiskAssets::load(&dist).expect("load assets");
    assert_eq!(assets.asset_count(), 2);
    assert_eq!(assets.dist(), dist.canonicalize().expect("canonical"));

    let key = AssetKey::from("index.html");
    let initial = <DiskAssets as tauri::Assets<tauri::Wry>>::get(&assets, &key)
        .expect("index.html served")
        .into_owned();
    let initial = String::from_utf8(initial).expect("utf8 html");
    assert!(
        !initial.contains("__ANVIL_INITIAL_THEME__"),
        "no injection before it is set"
    );

    assets.set_injection(InitialState::new(
        &Preferences {
            theme: THEME_DARK.to_string(),
            language: LANGUAGE_EN_US.to_string(),
        },
        true,
    ));
    let injected = <DiskAssets as tauri::Assets<tauri::Wry>>::get(&assets, &key)
        .expect("index.html served")
        .into_owned();
    let injected = String::from_utf8(injected).expect("utf8 html");
    assert!(injected.contains("<html lang=\"en-US\">"));
    assert!(injected.contains("\"mode\":\"dark\""));
    assert!(injected.contains("\"resolved\":\"dark\""));
    assert!(injected.contains("__ANVIL_INITIAL_LANGUAGE__"));

    let asset_key = AssetKey::from("assets/app.js");
    let script = <DiskAssets as tauri::Assets<tauri::Wry>>::get(&assets, &asset_key)
        .expect("asset served")
        .into_owned();
    assert_eq!(script, b"console.log('anvil');");

    let keys: Vec<String> = <DiskAssets as tauri::Assets<tauri::Wry>>::iter(&assets)
        .map(|(key, _)| key.into_owned())
        .collect();
    assert_eq!(
        keys,
        vec!["/assets/app.js".to_string(), "/index.html".to_string()]
    );
}

#[test]
fn asset_provider_requires_index_html() {
    let dir = TestDir::new("assets-empty");
    let dist = dir.join("dist");
    fs::create_dir_all(&dist).expect("create dist");
    fs::write(dist.join("app.js"), "x").expect("write file");
    let error = match DiskAssets::load(&dist) {
        Ok(_) => panic!("missing index.html must fail"),
        Err(error) => error,
    };
    assert_eq!(error.kind(), std::io::ErrorKind::NotFound);
    assert!(error.to_string().contains("index.html"));
}

// ---------------------------------------------------------------------------
// Capability whitelist + Tauri configuration
// ---------------------------------------------------------------------------

fn manifest_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
}

#[test]
fn capability_grants_only_the_main_window_and_the_host_commands() {
    let capability =
        fs::read_to_string(manifest_dir().join("capabilities/main.json")).expect("read capability");
    let value: serde_json::Value = serde_json::from_str(&capability).expect("parse capability");
    let windows = value["windows"].as_array().expect("windows array");
    assert_eq!(
        windows
            .iter()
            .map(|item| item.as_str().unwrap())
            .collect::<Vec<_>>(),
        vec!["main"]
    );
    let permissions: Vec<&str> = value["permissions"]
        .as_array()
        .expect("permissions array")
        .iter()
        .map(|item| item.as_str().expect("string permission"))
        .collect();
    assert_eq!(
        permissions,
        vec![
            "core:event:default",
            "allow-get-preferences",
            "allow-set-preferences",
            "allow-log-event"
        ]
    );
    for permission in &permissions {
        assert!(
            !permission.starts_with("core:window") && !permission.starts_with("core:webview"),
            "unexpected broad permission {permission}"
        );
    }

    let permissions_file = fs::read_to_string(manifest_dir().join("permissions/anvil.toml"))
        .expect("read permissions");
    for command in anvil::APP_COMMANDS {
        let slug = command.replace('_', "-");
        let expected = format!("allow-{slug}");
        assert!(
            permissions.iter().any(|permission| *permission == expected),
            "capability misses {expected}"
        );
        assert!(
            permissions_file.contains(&format!("commands.allow = [\"{command}\"]")),
            "permission file misses {command}"
        );
    }
    assert!(
        permissions_file.contains("permissions = [\"allow-get-preferences\", \"allow-set-preferences\", \"allow-log-event\"]"),
        "default permission set must list every host command"
    );
}

#[test]
fn tauri_config_declares_an_empty_window_list_and_the_frontend_dist() {
    let config =
        fs::read_to_string(manifest_dir().join("tauri.conf.json")).expect("read tauri config");
    let value: serde_json::Value = serde_json::from_str(&config).expect("parse tauri config");
    assert_eq!(value["productName"], "Anvil");
    assert_eq!(value["identifier"], "app.anvil.desktop");
    assert_eq!(value["build"]["frontendDist"], "../frontend/dist");
    assert_eq!(
        value["app"]["windows"].as_array().expect("windows").len(),
        0,
        "the window must be created by Rust after preflight"
    );
    assert!(value["app"]["windows"].as_array().unwrap().is_empty());
    for icon in value["bundle"]["icon"].as_array().expect("icons") {
        let icon = icon.as_str().expect("icon path");
        assert!(
            manifest_dir().join(icon).is_file(),
            "missing bundle icon {icon}"
        );
    }
}

// ---------------------------------------------------------------------------
// System theme
// ---------------------------------------------------------------------------

#[test]
fn system_theme_probe_returns_a_bool_without_panicking() {
    let _ = system_theme::system_dark();
    assert_eq!(system_theme::SYSTEM_THEME_EVENT, "anvil://system-theme");
    assert!(system_theme::SYSTEM_THEME_POLL_INTERVAL.as_millis() >= 1000);
}
