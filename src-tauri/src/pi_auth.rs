//! Provider accounts for the models page.
//!
//! Pi authenticates per provider, so "add a model" is really two questions: what
//! can this machine call, and which account pays for it. This module answers the
//! second one:
//!
//! * the catalogue and stored credentials come from [`providers`], which owns
//!   `auth.json` / `models.json` writes;
//! * an API key is written directly — same shape as `pi auth login --api-key`;
//! * a browser sign-in runs through pi's own OAuth flows (`pi_auth.mjs`, which
//!   imports the module pi's CLI uses) and streams its prompts back to the page.
//!
//! The helper exists because `pi auth login` refuses to run without a terminal
//! and because re-implementing nine OAuth flows here would drift from pi on its
//! next release. The script never writes a file; every credential lands in
//! `auth.json` through one code path in this crate.

use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anvil_catalog::providers::{self, CustomProviderDraft, ProviderSnapshot};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tauri::{AppHandle, Emitter, State};

use crate::catalog::CatalogState;

/// Streamed while a provider sign-in is running.
pub const PI_LOGIN_EVENT: &str = "anvil://pi-login";

/// The helper script, compiled in so the app never has to find a resource path.
const HELPER: &str = include_str!("pi_auth.mjs");

/// Name of the helper under the data directory.
const HELPER_NAME: &str = "pi-auth.mjs";

/// A sign-in that never resolves must not hold a session for ever.
const LOGIN_TIMEOUT: Duration = Duration::from_secs(300);

/// One extra credential value the form collected (Cloudflare account id, …).
#[derive(Debug, Clone, Deserialize)]
pub struct ExtraValue {
    pub name: String,
    pub value: String,
}

/// One selectable answer in a provider's sign-in menu.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LoginOption {
    pub id: String,
    pub label: String,
    pub description: Option<String>,
}

/// One event of a running sign-in, as the page receives it.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LoginEvent {
    pub session_id: String,
    pub provider_id: String,
    /// `auth_url`, `device_code`, `info`, `progress`, `prompt`, `done`, `error`.
    pub kind: String,
    pub message: Option<String>,
    pub url: Option<String>,
    pub user_code: Option<String>,
    pub verification_uri: Option<String>,
    pub instructions: Option<String>,
    /// Present on `prompt`: the id the answer has to be submitted with.
    pub prompt_id: Option<String>,
    /// Present on `prompt`: `secret`, `text`, `select`, or `manual_code`.
    pub prompt_kind: Option<String>,
    pub placeholder: Option<String>,
    pub options: Vec<LoginOption>,
    pub interval_seconds: Option<u64>,
    pub expires_in_seconds: Option<u64>,
}

impl LoginEvent {
    fn new(session_id: &str, provider_id: &str, kind: &str) -> Self {
        Self {
            session_id: session_id.to_string(),
            provider_id: provider_id.to_string(),
            kind: kind.to_string(),
            message: None,
            url: None,
            user_code: None,
            verification_uri: None,
            instructions: None,
            prompt_id: None,
            prompt_kind: None,
            placeholder: None,
            options: Vec::new(),
            interval_seconds: None,
            expires_in_seconds: None,
        }
    }
}

/// A live sign-in process.
struct LoginSession {
    child: Arc<Mutex<Option<Child>>>,
    stdin: Arc<Mutex<Option<ChildStdin>>>,
    cancelled: Arc<AtomicBool>,
}

/// Sign-in sessions, keyed by the id handed to the page.
#[derive(Default)]
pub struct PiLoginState {
    sessions: Mutex<HashMap<String, LoginSession>>,
}

impl PiLoginState {
    fn insert(&self, id: String, session: LoginSession) -> Result<(), String> {
        let mut sessions = self
            .sessions
            .lock()
            .map_err(|_| "pi login: state lock poisoned".to_string())?;
        sessions.insert(id, session);
        Ok(())
    }

    fn take(&self, id: &str) -> Option<LoginSession> {
        self.sessions.lock().ok()?.remove(id)
    }

    /// Stop every running sign-in. Called on window close so a cancelled app
    /// does not leave a helper waiting on a browser that will never come back.
    pub fn cancel_all(&self) {
        let Ok(mut sessions) = self.sessions.lock() else {
            return;
        };
        for (_, session) in sessions.drain() {
            session.cancelled.store(true, Ordering::SeqCst);
            if let Ok(mut slot) = session.child.lock() {
                if let Some(child) = slot.as_mut() {
                    let _ = child.kill();
                }
            }
        }
    }
}

fn now_millis() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|value| value.as_millis())
        .unwrap_or_default()
}

/// The pi shim this machine runs, from the catalogue's own discovery.
fn pi_executable(state: &CatalogState) -> Result<PathBuf, String> {
    state.with_service(|service| {
        let snapshot = service.snapshot().map_err(|error| error.to_wire())?;
        snapshot
            .runtimes
            .into_iter()
            .find(|runtime| runtime.runtime_id == "pi" && runtime.status != "not_installed")
            .map(|runtime| PathBuf::from(runtime.install_path))
            .ok_or_else(|| "pi is not installed: install it from 设置 → 运行时 first".to_string())
    })
}

/// Where `@earendil-works/pi-ai`'s provider bundle lives, given pi's shim.
///
/// npm hoists as it likes, so both the nested and the flat layout are checked,
/// from the shim's directory and from every parent (a `bin/pi` shim resolves to
/// `<prefix>/lib/node_modules/...` only after the symlink is followed).
fn locate_pi_ai(executable: &Path) -> Option<PathBuf> {
    const TAIL: &str = "dist/providers/all.js";
    let agents = "@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai";
    let flat = "@earendil-works/pi-ai";

    let mut roots: Vec<PathBuf> = Vec::new();
    if let Some(parent) = executable.parent() {
        roots.push(parent.to_path_buf());
    }
    if let Ok(real) = std::fs::canonicalize(executable) {
        if let Some(parent) = real.parent() {
            roots.push(parent.to_path_buf());
        }
    }
    // `parent` itself plus a few ancestors: `<prefix>/bin/pi` needs one hop,
    // `<prefix>/lib/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js`
    // (the unix real path) needs four.
    for root in roots.clone() {
        let mut current = Some(root.as_path());
        for _ in 0..6 {
            let Some(dir) = current else { break };
            for candidate in [
                dir.join("node_modules").join(agents).join(TAIL),
                dir.join("node_modules").join(flat).join(TAIL),
                dir.join("node_modules").join("@earendil-works").join("pi-coding-agent/node_modules/@earendil-works/pi-ai").join(TAIL),
            ] {
                if candidate.is_file() {
                    return Some(candidate);
                }
            }
            current = dir.parent();
        }
    }
    None
}

/// A Node able to run the helper, preferring one that ships with pi.
fn node_executable(pi: &Path) -> Option<PathBuf> {
    if let Some(dir) = pi.parent() {
        for name in ["node.exe", "node"] {
            let candidate = dir.join(name);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }
    anvil_catalog::runtime::process::preferred_node().map(Path::to_path_buf)
}

/// Write the helper next to the catalogue database and return its path.
///
/// Rewritten only when the bytes differ, so an unchanged app does not touch the
/// file on every login.
fn helper_path(state: &CatalogState) -> Result<PathBuf, String> {
    let dir = state
        .database()
        .parent()
        .map(Path::to_path_buf)
        .unwrap_or_else(|| PathBuf::from("."));
    std::fs::create_dir_all(&dir).map_err(|error| format!("pi login: create {}: {error}", dir.display()))?;
    let path = dir.join(HELPER_NAME);
    let unchanged = std::fs::read_to_string(&path)
        .map(|existing| existing == HELPER)
        .unwrap_or(false);
    if !unchanged {
        std::fs::write(&path, HELPER)
            .map_err(|error| format!("pi login: write {}: {error}", path.display()))?;
    }
    Ok(path)
}

fn emit(app: &AppHandle, event: &LoginEvent) {
    let _ = app.emit(PI_LOGIN_EVENT, event);
}

/// Turn one helper message into the event the page understands.
fn notify_event(session_id: &str, provider_id: &str, value: &Value) -> Option<LoginEvent> {
    let kind = value.get("type").and_then(Value::as_str).unwrap_or("info");
    let mut event = LoginEvent::new(session_id, provider_id, kind);
    let text = |name: &str| {
        value
            .get(name)
            .and_then(Value::as_str)
            .map(str::to_string)
    };
    match kind {
        "auth_url" => {
            event.url = text("url");
            event.instructions = text("instructions");
        }
        "device_code" => {
            event.user_code = text("userCode");
            event.verification_uri = text("verificationUri");
            event.interval_seconds = value.get("intervalSeconds").and_then(Value::as_u64);
            event.expires_in_seconds = value.get("expiresInSeconds").and_then(Value::as_u64);
        }
        _ => {
            event.message = text("message");
        }
    }
    Some(event)
}

fn prompt_event(session_id: &str, provider_id: &str, value: &Value) -> LoginEvent {
    let mut event = LoginEvent::new(session_id, provider_id, "prompt");
    event.prompt_id = value.get("id").and_then(Value::as_str).map(str::to_string);
    event.prompt_kind = value
        .get("kind")
        .and_then(Value::as_str)
        .map(str::to_string);
    event.message = value
        .get("message")
        .and_then(Value::as_str)
        .map(str::to_string);
    event.placeholder = value
        .get("placeholder")
        .and_then(Value::as_str)
        .map(str::to_string);
    if let Some(options) = value.get("options").and_then(Value::as_array) {
        for option in options {
            let Some(id) = option.get("id").and_then(Value::as_str) else {
                continue;
            };
            event.options.push(LoginOption {
                id: id.to_string(),
                label: option
                    .get("label")
                    .and_then(Value::as_str)
                    .unwrap_or(id)
                    .to_string(),
                description: option
                    .get("description")
                    .and_then(Value::as_str)
                    .map(str::to_string),
            });
        }
    }
    event
}

/// The provider catalogue plus the credentials stored on this machine.
#[tauri::command]
pub async fn pi_providers(state: State<'_, CatalogState>) -> Result<ProviderSnapshot, String> {
    let home = state.home().to_path_buf();
    Ok(providers::snapshot(&home))
}

/// Save an API key (and any provider-scoped extras) for one provider.
#[tauri::command]
pub async fn pi_provider_set_key(
    state: State<'_, CatalogState>,
    provider_id: String,
    key: String,
    env: Option<Vec<ExtraValue>>,
) -> Result<ProviderSnapshot, String> {
    let pairs: Vec<(String, String)> = env
        .unwrap_or_default()
        .into_iter()
        .map(|item| (item.name, item.value))
        .collect();
    let home = state.home().to_path_buf();
    providers::set_key(&home, &provider_id, &key, &pairs).map_err(|error| error.to_wire())?;
    // A credential is what separates "未认证" from usable, so the runtime status
    // is refreshed in the same call: making the reader press 刷新 to see their
    // own key take effect was a gap worth closing.
    let _ = state.with_service(|service| {
        let _ = service.probe_runtime("pi");
        Ok(())
    });
    Ok(providers::snapshot(&home))
}

/// Forget the credential pi stored for one provider.
#[tauri::command]
pub async fn pi_provider_logout(
    state: State<'_, CatalogState>,
    provider_id: String,
) -> Result<ProviderSnapshot, String> {
    let home = state.home().to_path_buf();
    providers::logout(&home, &provider_id).map_err(|error| error.to_wire())?;
    let _ = state.with_service(|service| {
        let _ = service.probe_runtime("pi");
        Ok(())
    });
    Ok(providers::snapshot(&home))
}

/// Create a custom provider, or point a built-in one at another endpoint.
#[tauri::command]
pub async fn pi_provider_custom_save(
    state: State<'_, CatalogState>,
    draft: CustomProviderDraft,
) -> Result<ProviderSnapshot, String> {
    let home = state.home().to_path_buf();
    providers::save_custom_provider(&home, &draft).map_err(|error| error.to_wire())?;
    // The provider's models are new rows in the catalogue, so the model list is
    // re-read in the same call; otherwise the page would show an account whose
    // models never appear.
    let _ = state.with_service(|service| {
        let _ = service.probe_runtime("pi");
        let _ = service.sync_models("pi");
        Ok(())
    });
    Ok(providers::snapshot(&home))
}

/// Remove a custom provider from `models.json`.
#[tauri::command]
pub async fn pi_provider_custom_remove(
    state: State<'_, CatalogState>,
    provider_id: String,
) -> Result<ProviderSnapshot, String> {
    let home = state.home().to_path_buf();
    providers::remove_custom_provider(&home, &provider_id).map_err(|error| error.to_wire())?;
    let _ = state.with_service(|service| {
        let _ = service.sync_models("pi");
        Ok(())
    });
    Ok(providers::snapshot(&home))
}

/// Start a browser sign-in. Returns the session id the events carry.
///
/// The call returns as soon as the helper is running: the flow's next step is a
/// browser visit that can take a minute, and the page follows it through
/// [`PI_LOGIN_EVENT`] rather than a command that blocks that long.
#[tauri::command]
pub async fn pi_provider_login(
    app: AppHandle,
    state: State<'_, CatalogState>,
    logins: State<'_, PiLoginState>,
    provider_id: String,
) -> Result<String, String> {
    let spec = providers::spec(&provider_id)
        .ok_or_else(|| format!("pi does not ship a provider called {provider_id:?}"))?;
    if spec.oauth_label.is_none() {
        return Err(format!(
            "{} has no browser sign-in; save an API key instead",
            spec.name
        ));
    }
    let pi = pi_executable(&state)?;
    let module = locate_pi_ai(&pi).ok_or_else(|| {
        "cannot find pi's OAuth module on this machine: this install cannot sign in from the app, \
         run `pi auth login` in a terminal instead"
            .to_string()
    })?;
    let node = node_executable(&pi).ok_or_else(|| {
        "no Node.js runtime found to drive the sign-in; install Node or run `pi auth login` in a \
         terminal"
            .to_string()
    })?;
    let helper = helper_path(&state)?;

    let mut child = Command::new(&node)
        .arg(&helper)
        .arg("--module")
        .arg(&module)
        .arg("--provider")
        .arg(&provider_id)
        .env("NO_COLOR", "1")
        // A GUI process has no console; keep the helper from waiting on one.
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| format!("pi login: start {}: {error}", node.display()))?;

    let stdin = child.stdin.take();
    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let session_id = format!("{}-{}", now_millis(), provider_id);
    let child_slot = Arc::new(Mutex::new(Some(child)));
    let stdin_slot = Arc::new(Mutex::new(stdin));
    let cancelled = Arc::new(AtomicBool::new(false));

    logins.insert(
        session_id.clone(),
        LoginSession {
            child: Arc::clone(&child_slot),
            stdin: Arc::clone(&stdin_slot),
            cancelled: Arc::clone(&cancelled),
        },
    )?;

    let home = state.home().to_path_buf();
    let app_for_out = app.clone();
    let app_for_err = app.clone();
    let session_for_out = session_id.clone();
    let provider_for_out = provider_id.clone();
    let session_for_err = session_id.clone();
    let provider_for_err = provider_id.clone();
    let cancelled_for_out = Arc::clone(&cancelled);

    // stderr is the helper's own complaints (a module that would not import, a
    // flow that threw before it could notify). Surfaced as `info` so the page
    // has something to show instead of a spinner that never stops.
    if let Some(stream) = stderr {
        std::thread::spawn(move || {
            for line in BufReader::new(stream).lines().map_while(Result::ok) {
                let line = line.trim();
                if line.is_empty() {
                    continue;
                }
                let mut event = LoginEvent::new(&session_for_err, &provider_for_err, "info");
                event.message = Some(line.to_string());
                emit(&app_for_err, &event);
            }
        });
    }

    if let Some(stream) = stdout {
        std::thread::spawn(move || {
            let mut credential: Option<Value> = None;
            let mut failure: Option<String> = None;
            for line in BufReader::new(stream).lines().map_while(Result::ok) {
                let line = line.trim();
                if line.is_empty() {
                    continue;
                }
                let Ok(value) = serde_json::from_str::<Value>(line) else {
                    let mut event =
                        LoginEvent::new(&session_for_out, &provider_for_out, "info");
                    event.message = Some(line.to_string());
                    emit(&app_for_out, &event);
                    continue;
                };
                match value.get("type").and_then(Value::as_str) {
                    Some("credential") => {
                        credential = value.get("value").cloned();
                    }
                    Some("error") => {
                        failure = Some(
                            value
                                .get("message")
                                .and_then(Value::as_str)
                                .unwrap_or("sign-in failed")
                                .to_string(),
                        );
                    }
                    Some("prompt") => {
                        emit(
                            &app_for_out,
                            &prompt_event(&session_for_out, &provider_for_out, &value),
                        );
                    }
                    Some("notify") => {
                        if let Some(payload) = value.get("event") {
                            if let Some(event) =
                                notify_event(&session_for_out, &provider_for_out, payload)
                            {
                                emit(&app_for_out, &event);
                            }
                        }
                    }
                    _ => {}
                }
            }

            if cancelled_for_out.load(Ordering::SeqCst) {
                return;
            }
            let mut event = LoginEvent::new(&session_for_out, &provider_for_out, "done");
            match credential {
                Some(credential) => {
                    if let Err(error) = providers::store_credential(
                        &home,
                        &provider_for_out,
                        credential,
                    ) {
                        event.kind = "error".to_string();
                        event.message = Some(error.to_wire());
                    }
                }
                None => {
                    event.kind = "error".to_string();
                    event.message = Some(
                        failure.unwrap_or_else(|| {
                            "the sign-in ended without a credential".to_string()
                        }),
                    );
                }
            }
            emit(&app_for_out, &event);
        });
    }

    // A flow that never finishes must not keep a session (and its Node process)
    // for ever. Five minutes is comfortably past every provider's own expiry.
    let app_for_timer = app.clone();
    let session_for_timer = session_id.clone();
    let provider_for_timer = provider_id.clone();
    let child_for_timer = Arc::clone(&child_slot);
    let cancelled_for_timer = Arc::clone(&cancelled);
    std::thread::spawn(move || {
        std::thread::sleep(LOGIN_TIMEOUT);
        if cancelled_for_timer.load(Ordering::SeqCst) {
            return;
        }
        let still_running = child_for_timer
            .lock()
            .ok()
            .and_then(|mut slot| {
                slot.as_mut().and_then(|child| {
                    matches!(child.try_wait(), Ok(None)).then_some(())
                })
            })
            .is_some();
        if !still_running {
            return;
        }
        if let Ok(mut slot) = child_for_timer.lock() {
            if let Some(child) = slot.as_mut() {
                let _ = child.kill();
            }
        }
        let mut event = LoginEvent::new(&session_for_timer, &provider_for_timer, "error");
        event.message = Some("sign-in timed out; start it again".to_string());
        emit(&app_for_timer, &event);
    });

    Ok(session_id)
}

/// Answer the prompt a running sign-in is waiting on.
///
/// A `secret` answer is the API key of a provider that authenticates that way
/// (Bedrock, Cloudflare); a `manual_code` answer is the redirect URL a browser
/// on another machine produced.
#[tauri::command]
pub async fn pi_provider_login_submit(
    logins: State<'_, PiLoginState>,
    session_id: String,
    prompt_id: String,
    value: String,
) -> Result<(), String> {
    let sessions = logins
        .sessions
        .lock()
        .map_err(|_| "pi login: state lock poisoned".to_string())?;
    let session = sessions
        .get(&session_id)
        .ok_or_else(|| "that sign-in is no longer running".to_string())?;
    let mut slot = session
        .stdin
        .lock()
        .map_err(|_| "pi login: stdin lock poisoned".to_string())?;
    let stdin = slot
        .as_mut()
        .ok_or_else(|| "that sign-in already closed its input".to_string())?;
    let line = serde_json::json!({ "id": prompt_id, "value": value }).to_string();
    stdin
        .write_all(format!("{line}\n").as_bytes())
        .and_then(|_| stdin.flush())
        .map_err(|error| format!("pi login: answer: {error}"))
}

/// Stop a running sign-in.
#[tauri::command]
pub async fn pi_provider_login_cancel(
    logins: State<'_, PiLoginState>,
    session_id: String,
) -> Result<(), String> {
    if let Some(session) = logins.take(&session_id) {
        session.cancelled.store(true, Ordering::SeqCst);
        if let Ok(mut slot) = session.child.lock() {
            if let Some(child) = slot.as_mut() {
                let _ = child.kill();
                let _ = child.wait();
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_helper_is_compiled_in_and_looks_like_the_protocol_it_speaks() {
        assert!(HELPER.contains("builtinProviders"), "helper loads pi's catalogue");
        assert!(HELPER.contains("--provider"), "helper takes a provider id");
        assert!(
            !HELPER.contains("auth.json"),
            "the helper must not write credentials itself"
        );
    }

    #[test]
    fn a_login_event_carries_the_fields_the_page_needs() {
        let device = serde_json::json!({
            "type": "device_code",
            "userCode": "ABCD-1234",
            "verificationUri": "https://github.com/login/device",
            "intervalSeconds": 5,
            "expiresInSeconds": 900
        });
        let event = notify_event("s1", "github-copilot", &device).expect("event");
        assert_eq!(event.kind, "device_code");
        assert_eq!(event.user_code.as_deref(), Some("ABCD-1234"));
        assert_eq!(
            event.verification_uri.as_deref(),
            Some("https://github.com/login/device")
        );
        assert_eq!(event.interval_seconds, Some(5));

        let url = serde_json::json!({
            "type": "auth_url",
            "url": "https://auth.example.com/authorize?x=1",
            "instructions": "Approve in the browser"
        });
        let event = notify_event("s1", "anthropic", &url).expect("event");
        assert_eq!(event.kind, "auth_url");
        assert_eq!(
            event.url.as_deref(),
            Some("https://auth.example.com/authorize?x=1")
        );
        assert_eq!(event.instructions.as_deref(), Some("Approve in the browser"));
    }

    #[test]
    fn a_select_prompt_keeps_its_options() {
        let prompt = serde_json::json!({
            "type": "prompt",
            "id": "3",
            "kind": "select",
            "message": "Select Amazon Bedrock authentication method:",
            "options": [
                { "id": "bearer", "label": "Bearer token", "description": "Bedrock token" },
                { "id": "profile", "label": "AWS profile" }
            ]
        });
        let event = prompt_event("s1", "amazon-bedrock", &prompt);
        assert_eq!(event.kind, "prompt");
        assert_eq!(event.prompt_id.as_deref(), Some("3"));
        assert_eq!(event.prompt_kind.as_deref(), Some("select"));
        assert_eq!(event.options.len(), 2);
        assert_eq!(event.options[0].id, "bearer");
        assert_eq!(event.options[1].description, None);
    }

    #[test]
    fn locating_pi_ai_finds_the_nested_module_beside_a_windows_shim() {
        let root = std::env::temp_dir().join(format!(
            "anvil-pi-ai-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&root);
        let nested = root.join(
            "node_modules/@earendil-works/pi-coding-agent/node_modules/@earendil-works/pi-ai/dist/providers",
        );
        std::fs::create_dir_all(&nested).expect("dirs");
        std::fs::write(nested.join("all.js"), "export function builtinProviders() {}").expect("file");
        let shim = root.join("pi.cmd");
        std::fs::write(&shim, "@ECHO off\n").expect("shim");

        let found = locate_pi_ai(&shim).expect("module");
        assert_eq!(found, nested.join("all.js"));
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn locating_pi_ai_returns_nothing_for_an_install_without_the_module() {
        let root = std::env::temp_dir().join(format!(
            "anvil-pi-ai-missing-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).expect("dirs");
        let shim = root.join("pi");
        std::fs::write(&shim, "#!/bin/sh\n").expect("shim");
        assert!(locate_pi_ai(&shim).is_none());
        let _ = std::fs::remove_dir_all(&root);
    }
}
