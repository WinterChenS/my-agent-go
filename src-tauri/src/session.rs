//! Session commands: the chat page's data path.
//!
//! Two SQLite connections, on purpose:
//!
//! * the **writer** owns the `SessionService` and is held for the whole of a
//!   run, because `send` needs `&mut self`;
//! * the **reader** answers list/detail/event queries from a second connection.
//!
//! The ledger is WAL, so a reader sees committed rows while a run is still
//! writing. Without the second connection the UI could not stream a token: the
//! only handle to the database would be locked by the very call it is waiting
//! on.
//!
//! The UI polls the ledger instead of relying on pushed IPC events. The ledger
//! is the source of truth anyway, and a poll that arrives late still shows every
//! token, whereas a dropped event would silently lose text.

use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard};

use anvil_session::{
    Cancels, Message, RunEvent, SessionContext, SessionDetail, SessionPage, SessionService,
    SessionSpec, SessionSummary, Store,
};
use serde::Serialize;
use tauri::{AppHandle, Manager, State};

/// Wall-clock budget for one turn.
///
/// A runtime that hangs must not hang the chat: the adapter sees this budget and
/// ends the run as failed, which the UI renders as an error card.
const RUN_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(300);

/// Session state shared by the commands.
pub struct SessionState {
    database: PathBuf,
    state_dir: PathBuf,
    writer: Mutex<Option<SessionService>>,
    reader: Mutex<Option<Store>>,
    cancels: Mutex<Option<Cancels>>,
    init_error: Mutex<Option<String>>,
}

impl SessionState {
    /// Build the state; both connections open on first use, so window creation
    /// never waits for SQLite.
    pub fn new(database: PathBuf, state_dir: PathBuf) -> Self {
        Self {
            database,
            state_dir,
            writer: Mutex::new(None),
            reader: Mutex::new(None),
            cancels: Mutex::new(None),
            init_error: Mutex::new(None),
        }
    }

    /// Path of the ledger (for the startup log line).
    pub fn database(&self) -> &Path {
        &self.database
    }

    fn fail(&self, message: String) -> String {
        if let Ok(mut slot) = self.init_error.lock() {
            *slot = Some(message.clone());
        }
        message
    }

    fn ensure(&self) -> Result<(), String> {
        if let Some(error) = self.init_error.lock().ok().and_then(|slot| slot.clone()) {
            return Err(error);
        }
        let mut writer = self
            .writer
            .lock()
            .map_err(|_| "session: state lock poisoned".to_string())?;
        if writer.is_some() {
            return Ok(());
        }

        let store = Store::open(&self.database).map_err(|error| {
            self.fail(format!(
                "session: open {}: {error}",
                self.database.display()
            ))
        })?;
        let reader = Store::open(&self.database)
            .map_err(|error| self.fail(format!("session: open reader: {error}")))?;
        let mut service = SessionService::new(
            store,
            anvil_session::default_registry(),
            SessionContext {
                state_dir: self.state_dir.clone(),
                run_timeout: RUN_TIMEOUT,
            },
        );
        // A run interrupted by the previous process is `failed`, not `running`:
        // otherwise the first send after a restart would be refused forever.
        let recovered = service
            .recover()
            .map_err(|error| self.fail(format!("session: recover: {error}")))?;
        if recovered > 0 {
            eprintln!("anvil: session: {recovered} interrupted run(s) marked failed");
        }
        let cancels = service.cancels();
        if let Ok(mut slot) = self.reader.lock() {
            *slot = Some(reader);
        }
        if let Ok(mut slot) = self.cancels.lock() {
            *slot = Some(cancels);
        }
        *writer = Some(service);
        Ok(())
    }

    fn with_writer<T>(
        &self,
        action: impl FnOnce(&mut SessionService) -> Result<T, String>,
    ) -> Result<T, String> {
        self.ensure()?;
        let mut guard: MutexGuard<'_, Option<SessionService>> = self
            .writer
            .lock()
            .map_err(|_| "session: state lock poisoned".to_string())?;
        let service = guard
            .as_mut()
            .ok_or_else(|| "session: service is not initialised".to_string())?;
        action(service)
    }

    fn with_reader<T>(
        &self,
        action: impl FnOnce(&Store) -> Result<T, String>,
    ) -> Result<T, String> {
        self.ensure()?;
        let guard = self
            .reader
            .lock()
            .map_err(|_| "session: reader lock poisoned".to_string())?;
        let store = guard
            .as_ref()
            .ok_or_else(|| "session: reader is not initialised".to_string())?;
        action(store)
    }

    /// Flip the cancel flag for one session without touching the writer lock.
    fn abort(&self, session_id: &str) -> Result<bool, String> {
        self.ensure()?;
        let guard = self
            .cancels
            .lock()
            .map_err(|_| "session: cancel lock poisoned".to_string())?;
        Ok(guard
            .as_ref()
            .map(|cancels| cancels.cancel(session_id))
            .unwrap_or(false))
    }

    fn send(&self, session_id: &str, text: &str) -> Result<SendReport, String> {
        self.with_writer(|service| {
            let outcome = service
                .send(session_id, text)
                .map_err(|error| error.to_wire().to_string())?;
            Ok(SendReport {
                run_id: outcome.run.id,
                status: outcome.run.status.as_str().to_string(),
                error: outcome.run.error,
                message: outcome.message,
                event_count: outcome.event_count,
            })
        })
    }
}

/// What one `session_send` produced.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SendReport {
    pub run_id: String,
    /// `completed`, `aborted` or `failed`.
    pub status: String,
    pub error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<Message>,
    pub event_count: usize,
}

/// One page of the session list (pinned first, then most recently updated).
#[tauri::command]
pub async fn session_list(
    state: State<'_, SessionState>,
    runtime_id: Option<String>,
    include_archived: Option<bool>,
    search: Option<String>,
    limit: Option<u32>,
    cursor: Option<String>,
) -> Result<SessionPage, String> {
    let search = search.filter(|value| !value.trim().is_empty());
    state.with_reader(|store| {
        store
            .sessions(
                runtime_id.as_deref(),
                include_archived.unwrap_or(false),
                search.as_deref(),
                limit.unwrap_or(50) as usize,
                cursor.as_deref(),
            )
            .map_err(|error| error.to_wire().to_string())
    })
}

/// One session with its messages and runs.
#[tauri::command]
pub async fn session_detail(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<SessionDetail, String> {
    state.with_reader(|store| {
        let session = store
            .session(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        let messages = store
            .messages(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        let runs = store
            .runs(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        Ok(SessionDetail {
            session,
            messages,
            runs,
        })
    })
}

/// Events after `after_seq`, so the UI can poll incrementally.
#[tauri::command]
pub async fn session_events(
    state: State<'_, SessionState>,
    session_id: String,
    after_seq: Option<i64>,
) -> Result<Vec<RunEvent>, String> {
    let after = after_seq.unwrap_or(0);
    state.with_reader(|store| {
        let mut events = store
            .events(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        events.retain(|event| event.seq > after);
        Ok(events)
    })
}

/// Create a session on one runtime. Nothing is sent yet.
#[tauri::command]
pub async fn session_open(
    state: State<'_, SessionState>,
    runtime_id: String,
    model: String,
    cwd: Option<String>,
    title: Option<String>,
) -> Result<SessionSummary, String> {
    state.with_writer(|service| {
        let spec = SessionSpec {
            runtime_id,
            model,
            cwd: cwd.unwrap_or_else(default_cwd),
            title,
            native_id: None,
            // Tools stay off: a headless run that stops to ask for approval
            // would sit there until the timeout.
            allow_tools: false,
        };
        service
            .open(&spec)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Run one turn. Blocking on purpose: it runs on a blocking thread, and the UI
/// streams the interim text by polling [`session_events`].
#[tauri::command]
pub async fn session_send(
    app: AppHandle,
    session_id: String,
    text: String,
) -> Result<SendReport, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SessionState>();
        state.send(&session_id, &text)
    })
    .await
    .map_err(|error| format!("io: the run task ended unexpectedly: {error}"))?
}

/// Ask the in-flight run to stop. `false` means nothing was running.
#[tauri::command]
pub async fn session_abort(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<bool, String> {
    state.abort(&session_id)
}

/// Rename a session.
#[tauri::command]
pub async fn session_rename(
    state: State<'_, SessionState>,
    session_id: String,
    title: String,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .rename(&session_id, &title)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Pin or unpin a session in the history list.
#[tauri::command]
pub async fn session_pin(
    state: State<'_, SessionState>,
    session_id: String,
    pinned: bool,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .set_pinned(&session_id, pinned)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Archive a session out of the default list, or bring it back.
#[tauri::command]
pub async fn session_archive(
    state: State<'_, SessionState>,
    session_id: String,
    archived: bool,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .set_archived(&session_id, archived)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Delete a session and everything derived from it.
#[tauri::command]
pub async fn session_delete(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .delete(&session_id)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Directory a session runs in when the caller names none.
fn default_cwd() -> String {
    std::env::current_dir()
        .map(|path| path.display().to_string())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn state(name: &str) -> SessionState {
        let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join(".tmp")
            .join("test")
            .join("host-session")
            .join(name);
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create scratch dir");
        SessionState::new(dir.join("sessions.sqlite"), dir.join("state"))
    }

    #[test]
    fn the_reader_sees_an_empty_ledger_before_anything_runs() {
        let state = state("empty");
        let page = state
            .with_reader(|store| {
                store
                    .sessions(None, false, None, 10, None)
                    .map_err(|error| error.to_wire().to_string())
            })
            .expect("list");
        assert_eq!(page.total, 0);
        assert!(page.sessions.is_empty());
    }

    #[test]
    fn abort_on_a_session_without_a_run_reports_nothing_to_stop() {
        let state = state("abort");
        state.ensure().expect("init");
        assert!(!state.abort("nope").expect("abort"));
    }

    #[test]
    fn an_unknown_session_is_reported_as_not_found() {
        let state = state("unknown");
        let error = state
            .with_reader(|store| {
                store
                    .session("no-such-session")
                    .map_err(|error| error.to_wire().to_string())
            })
            .expect_err("missing session must not resolve");
        // The wire shape keeps the code, so the UI can tell "gone" from
        // "database broken" without matching on prose.
        assert!(error.contains("\"code\":\"not_found\""), "{error}");
    }
}
