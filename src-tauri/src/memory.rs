//! Memory commands: the two lists in 设置 → 记忆, and the two switches above them.
//!
//! Every read and write goes through the session ledger's own `memories` table —
//! the same rows [`SessionState::memory_notes`] injects into a turn. That is the
//! whole point: a "记忆" page that showed rows the model never sees, or a prompt
//! that used rows the page could not show, would be the ghost-memory failure this
//! feature exists to avoid.
//!
//! Mutations answer with the full overview instead of the changed row, so the page
//! never has to merge a local guess with the host's idea of counts.

use anvil_session::memory::{Memory, MemoryDraft, MemoryPatch, MemorySettings};
use serde::Serialize;
use serde_json::{Value, json};
use tauri::{AppHandle, Manager, State};

use crate::session::SessionState;

/// What the page draws in one reply.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryOverview {
    pub settings: MemorySettings,
    /// Rows the agent gets on every turn.
    pub user: Vec<Memory>,
    /// One conclusion per conversation, newest first.
    pub session: Vec<Memory>,
    pub counts: MemoryCounts,
    /// Where the rows live, so a curious user can look at the database.
    pub database: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryCounts {
    pub user: usize,
    pub session: usize,
    pub enabled: usize,
}

fn overview(state: &State<'_, SessionState>, query: Option<&str>) -> Result<MemoryOverview, String> {
    state.with_reader(|store| {
        let user = store
            .memories(Some("user"), query)
            .map_err(|error| error.to_wire().to_string())?;
        let session = store
            .memories(Some("session"), query)
            .map_err(|error| error.to_wire().to_string())?;
        let settings = store
            .memory_settings()
            .map_err(|error| error.to_wire().to_string())?;
        let counts = MemoryCounts {
            user: user.len(),
            session: session.len(),
            enabled: user.iter().filter(|row| row.enabled).count(),
        };
        Ok(MemoryOverview {
            settings,
            user,
            session,
            counts,
            database: state.database().display().to_string(),
        })
    })
}

/// Both lists plus the switches. `query` narrows both by substring.
#[tauri::command]
pub async fn memory_overview(
    state: State<'_, SessionState>,
    query: Option<String>,
) -> Result<MemoryOverview, String> {
    overview(&state, query.as_deref())
}

/// Add one memory. Empty text is rejected by the store, not silently dropped.
#[tauri::command]
pub async fn memory_save(
    state: State<'_, SessionState>,
    memory: MemoryDraft,
    query: Option<String>,
) -> Result<MemoryOverview, String> {
    let at = anvil_session::clock::now_rfc3339();
    state.with_writer(|service| {
        service
            .store_mut()
            .add_memory(&memory, &at)
            .map_err(|error| error.to_wire().to_string())?;
        Ok(())
    })?;
    overview(&state, query.as_deref())
}

/// Edit text, kind, pin or the enabled switch. A text edit also marks the row as
/// user-authored, which stops the conversation recorder from overwriting it.
#[tauri::command]
pub async fn memory_update(
    state: State<'_, SessionState>,
    id: String,
    patch: MemoryPatch,
    query: Option<String>,
) -> Result<MemoryOverview, String> {
    let at = anvil_session::clock::now_rfc3339();
    state.with_writer(|service| {
        service
            .store_mut()
            .update_memory(&id, &patch, &at)
            .map_err(|error| error.to_wire().to_string())?;
        Ok(())
    })?;
    overview(&state, query.as_deref())
}

#[tauri::command]
pub async fn memory_delete(
    state: State<'_, SessionState>,
    id: String,
    query: Option<String>,
) -> Result<MemoryOverview, String> {
    state.with_writer(|service| {
        service
            .store_mut()
            .delete_memory(&id)
            .map_err(|error| error.to_wire().to_string())?;
        Ok(())
    })?;
    overview(&state, query.as_deref())
}

/// Delete every row, or every row in one scope.
#[tauri::command]
pub async fn memory_clear(
    state: State<'_, SessionState>,
    scope: Option<String>,
    query: Option<String>,
) -> Result<MemoryOverview, String> {
    let scope = scope.filter(|value| !value.trim().is_empty());
    state.with_writer(|service| {
        service
            .store_mut()
            .clear_memories(scope.as_deref())
            .map_err(|error| error.to_wire().to_string())?;
        Ok(())
    })?;
    overview(&state, query.as_deref())
}

/// Flip 注入 / 记录会话结论. Omitted fields keep their stored value.
#[tauri::command]
pub async fn memory_set_switches(
    state: State<'_, SessionState>,
    inject: Option<bool>,
    record_sessions: Option<bool>,
) -> Result<MemoryOverview, String> {
    state.with_writer(|service| {
        service
            .store_mut()
            .set_memory_settings(inject, record_sessions)
            .map_err(|error| error.to_wire().to_string())?;
        Ok(())
    })?;
    overview(&state, None)
}

// ---------------------------------------------------------------------------
// The agent's side of the same table
// ---------------------------------------------------------------------------

/// What the model gets when it asks: the user rows it may rely on, nothing else.
///
/// Session conclusions are deliberately absent — they are the user's record of
/// what happened, and handing them to the model would turn a reviewable list into
/// the invisible recall layer this design rejects.
pub(crate) fn list_for_agent(app: &AppHandle) -> Value {
    let state = app.state::<SessionState>();
    match state.with_reader(|store| {
        let rows = store
            .memories(Some("user"), None)
            .map_err(|error| error.to_wire().to_string())?;
        let settings = store
            .memory_settings()
            .map_err(|error| error.to_wire().to_string())?;
        Ok((rows, settings))
    }) {
        Ok((rows, settings)) => json!({
            "ok": true,
            "inject": settings.inject,
            "memories": rows
                .iter()
                .map(|row| json!({ "id": row.id, "kind": row.kind, "text": row.text, "pinned": row.pinned, "enabled": row.enabled }))
                .collect::<Vec<_>>(),
        }),
        Err(error) => json!({ "ok": false, "error": error }),
    }
}

/// `{"text": "...", "kind": "preference"}` — one memory the user asked to keep.
pub(crate) fn add_from_agent(app: &AppHandle, body: &str) -> Value {
    let parsed: Value = match serde_json::from_str(body) {
        Ok(value) => value,
        Err(error) => return json!({ "ok": false, "error": format!("bad json: {error}") }),
    };
    let draft = MemoryDraft {
        scope: "user".to_string(),
        session_id: String::new(),
        agent_id: parsed.get("agent").and_then(Value::as_str).unwrap_or_default().to_string(),
        kind: parsed.get("kind").and_then(Value::as_str).unwrap_or("fact").to_string(),
        text: parsed.get("text").and_then(Value::as_str).unwrap_or_default().to_string(),
        source: "agent".to_string(),
        pinned: parsed.get("pinned").and_then(Value::as_bool).unwrap_or(false),
    };
    let at = anvil_session::clock::now_rfc3339();
    let state = app.state::<SessionState>();
    match state.with_writer(|service| {
        service
            .store_mut()
            .add_memory(&draft, &at)
            .map_err(|error| error.to_wire().to_string())
    }) {
        Ok(memory) => json!({ "ok": true, "memory": { "id": memory.id, "kind": memory.kind, "text": memory.text } }),
        Err(error) => json!({ "ok": false, "error": error }),
    }
}

/// `{"id": "mem-…"}` — a real delete, same row the page shows.
pub(crate) fn forget_from_agent(app: &AppHandle, body: &str) -> Value {
    let parsed: Value = match serde_json::from_str(body) {
        Ok(value) => value,
        Err(error) => return json!({ "ok": false, "error": format!("bad json: {error}") }),
    };
    let Some(id) = parsed.get("id").and_then(Value::as_str) else {
        return json!({ "ok": false, "error": "forget needs the memory id" });
    };
    let state = app.state::<SessionState>();
    match state.with_writer(|service| {
        service
            .store_mut()
            .delete_memory(id)
            .map_err(|error| error.to_wire().to_string())
    }) {
        Ok(()) => json!({ "ok": true, "deleted": id }),
        Err(error) => json!({ "ok": false, "error": error }),
    }
}
