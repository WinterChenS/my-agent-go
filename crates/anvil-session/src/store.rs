//! SQLite ledger.
//!
//! Four tables, append-only for events and messages:
//! * `sessions` — the index the history panel reads;
//! * `messages` — what the UI renders;
//! * `runs` — one row per turn, with its terminal state;
//! * `events` — every adapter event, in `seq` order, kept for evidence.
//!
//! `seq` is allocated inside the same transaction that inserts the row, so two
//! writers cannot produce the same sequence number for one session.

use std::path::Path;

use rusqlite::{Connection, OptionalExtension, params};

use crate::error::{Error, Result};
use crate::model::{
    EventKind, Message, Role, Run, RunEvent, RunStatus, SessionPage, SessionStatus, SessionSummary,
};

/// Open ledger handle. One connection, used behind the host's mutex.
pub struct Store {
    connection: Connection,
}

impl Store {
    pub fn open(path: &Path) -> Result<Self> {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let connection = Connection::open(path)?;
        Self::prepare(connection)
    }

    pub fn open_in_memory() -> Result<Self> {
        Self::prepare(Connection::open_in_memory()?)
    }

    fn prepare(connection: Connection) -> Result<Self> {
        connection.pragma_update(None, "journal_mode", "WAL")?;
        connection.pragma_update(None, "foreign_keys", "ON")?;
        connection.execute_batch(
            "CREATE TABLE IF NOT EXISTS sessions (
                id TEXT PRIMARY KEY,
                runtime_id TEXT NOT NULL,
                title TEXT NOT NULL,
                status TEXT NOT NULL,
                native_id TEXT NOT NULL DEFAULT '',
                model TEXT NOT NULL DEFAULT '',
                project_path TEXT NOT NULL DEFAULT '',
                pinned INTEGER NOT NULL DEFAULT 0,
                archived INTEGER NOT NULL DEFAULT 0,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL,
                preview TEXT NOT NULL DEFAULT ''
            );
            CREATE TABLE IF NOT EXISTS messages (
                id TEXT PRIMARY KEY,
                session_id TEXT NOT NULL,
                run_id TEXT,
                role TEXT NOT NULL,
                text TEXT NOT NULL,
                seq INTEGER NOT NULL,
                created_at TEXT NOT NULL,
                meta TEXT,
                UNIQUE (session_id, seq),
                FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
            );
            CREATE TABLE IF NOT EXISTS runs (
                id TEXT PRIMARY KEY,
                session_id TEXT NOT NULL,
                status TEXT NOT NULL,
                started_at TEXT NOT NULL,
                ended_at TEXT,
                error TEXT,
                prompt TEXT NOT NULL,
                FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
            );
            CREATE TABLE IF NOT EXISTS events (
                session_id TEXT NOT NULL,
                run_id TEXT NOT NULL,
                seq INTEGER NOT NULL,
                kind TEXT NOT NULL,
                at TEXT NOT NULL,
                payload TEXT NOT NULL,
                PRIMARY KEY (session_id, seq),
                FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
            );
            CREATE INDEX IF NOT EXISTS sessions_order ON sessions (pinned DESC, updated_at DESC, id DESC);
            CREATE INDEX IF NOT EXISTS messages_order ON messages (session_id, seq);",
        )?;
        Ok(Self { connection })
    }

    // ---- sessions -------------------------------------------------------

    pub fn create_session(
        &mut self,
        id: &str,
        runtime_id: &str,
        title: &str,
        model: &str,
        project_path: &str,
        at: &str,
    ) -> Result<()> {
        self.connection.execute(
            "INSERT INTO sessions (id, runtime_id, title, status, native_id, model, project_path,
                                   pinned, archived, created_at, updated_at, preview)
             VALUES (?1, ?2, ?3, 'idle', '', ?4, ?5, 0, 0, ?6, ?6, '')",
            params![id, runtime_id, title, model, project_path, at],
        )?;
        Ok(())
    }

    pub fn set_native_id(&mut self, id: &str, native_id: &str, at: &str) -> Result<()> {
        let changed = self.connection.execute(
            "UPDATE sessions SET native_id = ?2, updated_at = ?3 WHERE id = ?1",
            params![id, native_id, at],
        )?;
        expect_one(changed, "session", id)
    }

    pub fn set_status(&mut self, id: &str, status: SessionStatus, at: &str) -> Result<()> {
        let changed = self.connection.execute(
            "UPDATE sessions SET status = ?2, updated_at = ?3 WHERE id = ?1",
            params![id, status.as_str(), at],
        )?;
        expect_one(changed, "session", id)
    }

    pub fn rename_session(&mut self, id: &str, title: &str, at: &str) -> Result<()> {
        let changed = self.connection.execute(
            "UPDATE sessions SET title = ?2, updated_at = ?3 WHERE id = ?1",
            params![id, title, at],
        )?;
        expect_one(changed, "session", id)
    }

    pub fn set_pinned(&mut self, id: &str, pinned: bool, at: &str) -> Result<()> {
        let changed = self.connection.execute(
            "UPDATE sessions SET pinned = ?2, updated_at = ?3 WHERE id = ?1",
            params![id, i64::from(pinned), at],
        )?;
        expect_one(changed, "session", id)
    }

    pub fn set_archived(&mut self, id: &str, archived: bool, at: &str) -> Result<()> {
        let status = if archived {
            SessionStatus::Archived
        } else {
            SessionStatus::Idle
        };
        let changed = self.connection.execute(
            "UPDATE sessions SET archived = ?2, status = ?3, updated_at = ?4 WHERE id = ?1",
            params![id, i64::from(archived), status.as_str(), at],
        )?;
        expect_one(changed, "session", id)
    }

    pub fn delete_session(&mut self, id: &str) -> Result<()> {
        let changed = self
            .connection
            .execute("DELETE FROM sessions WHERE id = ?1", params![id])?;
        expect_one(changed, "session", id)
    }

    pub fn session(&self, id: &str) -> Result<SessionSummary> {
        self.connection
            .query_row(
                &format!("{SESSION_SELECT} WHERE s.id = ?1"),
                params![id],
                map_session,
            )
            .optional()?
            .ok_or_else(|| Error::NotFound(format!("session {id:?}")))
    }

    /// History page. `cursor` is the opaque `updated_at|id` of the last row of
    /// the previous page; ordering is `pinned DESC, updated_at DESC, id DESC`,
    /// which is total, so pages never overlap or skip.
    pub fn sessions(
        &self,
        runtime_id: Option<&str>,
        include_archived: bool,
        search: Option<&str>,
        limit: usize,
        cursor: Option<&str>,
    ) -> Result<SessionPage> {
        let limit = limit.clamp(1, 200);
        let mut clauses: Vec<String> = Vec::new();
        let mut args: Vec<rusqlite::types::Value> = Vec::new();

        if let Some(runtime_id) = runtime_id {
            clauses.push(format!("s.runtime_id = ?{}", args.len() + 1));
            args.push(runtime_id.to_string().into());
        }
        if !include_archived {
            clauses.push("s.archived = 0".to_string());
        }
        if let Some(search) = search.map(str::trim).filter(|text| !text.is_empty()) {
            clauses.push(format!(
                "(s.title LIKE ?{0} OR s.preview LIKE ?{0} OR EXISTS (
                    SELECT 1 FROM messages m
                    WHERE m.session_id = s.id AND m.text LIKE ?{0}))",
                args.len() + 1
            ));
            args.push(format!("%{search}%").into());
        }
        if let Some(cursor) = cursor.and_then(split_cursor) {
            let base = args.len();
            clauses.push(format!(
                "(s.pinned, s.updated_at, s.id) < (SELECT pinned, updated_at, id FROM sessions WHERE id = ?{})",
                base + 1
            ));
            args.push(cursor.into());
        }

        let where_clause = if clauses.is_empty() {
            String::new()
        } else {
            format!(" WHERE {}", clauses.join(" AND "))
        };
        let sql = format!(
            "{SESSION_SELECT}{where_clause} ORDER BY s.pinned DESC, s.updated_at DESC, s.id DESC LIMIT {}",
            limit + 1
        );
        let mut statement = self.connection.prepare(&sql)?;
        let rows = statement.query_map(rusqlite::params_from_iter(args), map_session)?;
        let mut sessions = rows.collect::<std::result::Result<Vec<_>, _>>()?;

        let next_cursor = if sessions.len() > limit {
            sessions.truncate(limit);
            sessions
                .last()
                .map(|row| format!("{}|{}", row.updated_at, row.id))
        } else {
            None
        };

        let total = self.count_sessions(runtime_id, include_archived, search)?;
        Ok(SessionPage {
            sessions,
            next_cursor,
            total,
        })
    }

    fn count_sessions(
        &self,
        runtime_id: Option<&str>,
        include_archived: bool,
        search: Option<&str>,
    ) -> Result<i64> {
        let mut clauses: Vec<String> = Vec::new();
        let mut args: Vec<rusqlite::types::Value> = Vec::new();
        if let Some(runtime_id) = runtime_id {
            clauses.push(format!("s.runtime_id = ?{}", args.len() + 1));
            args.push(runtime_id.to_string().into());
        }
        if !include_archived {
            clauses.push("s.archived = 0".to_string());
        }
        if let Some(search) = search.map(str::trim).filter(|text| !text.is_empty()) {
            clauses.push(format!(
                "(s.title LIKE ?{0} OR s.preview LIKE ?{0} OR EXISTS (
                    SELECT 1 FROM messages m
                    WHERE m.session_id = s.id AND m.text LIKE ?{0}))",
                args.len() + 1
            ));
            args.push(format!("%{search}%").into());
        }
        let where_clause = if clauses.is_empty() {
            String::new()
        } else {
            format!(" WHERE {}", clauses.join(" AND "))
        };
        let sql = format!("SELECT COUNT(*) FROM sessions s{where_clause}");
        let total: i64 =
            self.connection
                .query_row(&sql, rusqlite::params_from_iter(args), |row| row.get(0))?;
        Ok(total)
    }

    // ---- messages -------------------------------------------------------

    /// Insert a message and bump the session preview. `seq` is allocated here so
    /// the caller cannot invent one.
    pub fn append_message(
        &mut self,
        session_id: &str,
        run_id: Option<&str>,
        role: Role,
        text: &str,
        at: &str,
        meta: Option<&serde_json::Value>,
    ) -> Result<i64> {
        let transaction = self.connection.transaction()?;
        let seq: i64 = transaction.query_row(
            "SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_id = ?1",
            params![session_id],
            |row| row.get(0),
        )?;
        let id = format!("{session_id}:m{seq}");
        transaction.execute(
            "INSERT INTO messages (id, session_id, run_id, role, text, seq, created_at, meta)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            params![
                id,
                session_id,
                run_id,
                role.as_str(),
                text,
                seq,
                at,
                meta.map(std::string::ToString::to_string)
            ],
        )?;
        if role == Role::Assistant {
            let preview: String = text.chars().take(160).collect();
            transaction.execute(
                "UPDATE sessions SET preview = ?2, updated_at = ?3 WHERE id = ?1",
                params![session_id, preview, at],
            )?;
        } else {
            transaction.execute(
                "UPDATE sessions SET updated_at = ?2 WHERE id = ?1",
                params![session_id, at],
            )?;
        }
        transaction.commit()?;
        Ok(seq)
    }

    pub fn messages(&self, session_id: &str) -> Result<Vec<Message>> {
        let mut statement = self.connection.prepare(
            "SELECT id, session_id, run_id, role, text, seq, created_at, meta
             FROM messages WHERE session_id = ?1 ORDER BY seq",
        )?;
        let rows = statement.query_map(params![session_id], |row| {
            let role: String = row.get(3)?;
            let meta: Option<String> = row.get(7)?;
            Ok(Message {
                id: row.get(0)?,
                session_id: row.get(1)?,
                run_id: row.get(2)?,
                role: Role::from_label(&role).unwrap_or(Role::System),
                text: row.get(4)?,
                seq: row.get(5)?,
                created_at: row.get(6)?,
                meta: meta.and_then(|text| serde_json::from_str(&text).ok()),
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// Drop every message of a session, used after a successful compaction.
    pub fn replace_messages(&mut self, session_id: &str, messages: &[Message]) -> Result<()> {
        let transaction = self.connection.transaction()?;
        transaction.execute(
            "DELETE FROM messages WHERE session_id = ?1",
            params![session_id],
        )?;
        {
            let mut insert = transaction.prepare(
                "INSERT INTO messages (id, session_id, run_id, role, text, seq, created_at, meta)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            )?;
            for message in messages {
                insert.execute(params![
                    message.id,
                    message.session_id,
                    message.run_id,
                    message.role.as_str(),
                    message.text,
                    message.seq,
                    message.created_at,
                    message.meta.as_ref().map(std::string::ToString::to_string)
                ])?;
            }
        }
        transaction.commit()?;
        Ok(())
    }

    // ---- runs -----------------------------------------------------------

    pub fn start_run(&mut self, run: &Run) -> Result<()> {
        self.connection.execute(
            "INSERT INTO runs (id, session_id, status, started_at, ended_at, error, prompt)
             VALUES (?1, ?2, ?3, ?4, NULL, NULL, ?5)",
            params![
                run.id,
                run.session_id,
                run.status.as_str(),
                run.started_at,
                run.prompt
            ],
        )?;
        Ok(())
    }

    pub fn finish_run(
        &mut self,
        id: &str,
        status: RunStatus,
        ended_at: &str,
        error: Option<&str>,
    ) -> Result<()> {
        let changed = self.connection.execute(
            "UPDATE runs SET status = ?2, ended_at = ?3, error = ?4 WHERE id = ?1",
            params![id, status.as_str(), ended_at, error],
        )?;
        expect_one(changed, "run", id)
    }

    pub fn runs(&self, session_id: &str) -> Result<Vec<Run>> {
        let mut statement = self.connection.prepare(
            "SELECT id, session_id, status, started_at, ended_at, error, prompt
             FROM runs WHERE session_id = ?1 ORDER BY started_at",
        )?;
        let rows = statement.query_map(params![session_id], |row| {
            let status: String = row.get(2)?;
            Ok(Run {
                id: row.get(0)?,
                session_id: row.get(1)?,
                status: RunStatus::from_label(&status).unwrap_or(RunStatus::Failed),
                started_at: row.get(3)?,
                ended_at: row.get(4)?,
                error: row.get(5)?,
                prompt: row.get(6)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// Mark every run still marked running as failed. Called on start-up: a
    /// process that died mid-run must not leave a session "running" forever.
    pub fn fail_interrupted_runs(&mut self, at: &str) -> Result<usize> {
        let changed = self.connection.execute(
            "UPDATE runs SET status = 'failed', ended_at = ?1, error = 'process exited during the run'
             WHERE status = 'running'",
            params![at],
        )?;
        Ok(changed)
    }

    // ---- events ---------------------------------------------------------

    /// Append one adapter event, allocating the session-wide `seq` here.
    pub fn append_event(
        &mut self,
        session_id: &str,
        run_id: &str,
        kind: EventKind,
        payload: &serde_json::Value,
    ) -> Result<RunEvent> {
        let transaction = self.connection.transaction()?;
        let seq: i64 = transaction.query_row(
            "SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE session_id = ?1",
            params![session_id],
            |row| row.get(0),
        )?;
        let at = crate::clock::now_rfc3339();
        transaction.execute(
            "INSERT INTO events (session_id, run_id, seq, kind, at, payload)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            params![
                session_id,
                run_id,
                seq,
                kind.as_str(),
                at,
                payload.to_string()
            ],
        )?;
        transaction.commit()?;
        Ok(RunEvent {
            session_id: session_id.to_string(),
            run_id: run_id.to_string(),
            seq,
            kind,
            at,
            payload: payload.clone(),
        })
    }

    pub fn events(&self, session_id: &str) -> Result<Vec<RunEvent>> {
        let mut statement = self.connection.prepare(
            "SELECT session_id, run_id, seq, kind, at, payload
             FROM events WHERE session_id = ?1 ORDER BY seq",
        )?;
        let rows = statement.query_map(params![session_id], |row| {
            let kind: String = row.get(3)?;
            let payload: String = row.get(5)?;
            Ok(RunEvent {
                session_id: row.get(0)?,
                run_id: row.get(1)?,
                seq: row.get(2)?,
                kind: EventKind::from_label(&kind).unwrap_or(EventKind::Unknown),
                at: row.get(4)?,
                payload: serde_json::from_str(&payload).unwrap_or(serde_json::Value::Null),
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    #[must_use]
    pub fn raw(&self) -> &Connection {
        &self.connection
    }
}

const SESSION_SELECT: &str = "SELECT s.id, s.runtime_id, s.title, s.status, s.native_id, s.model,
        s.project_path, s.pinned, s.archived, s.created_at, s.updated_at, s.preview,
        (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)
     FROM sessions s";

fn map_session(row: &rusqlite::Row<'_>) -> rusqlite::Result<SessionSummary> {
    let status: String = row.get(3)?;
    Ok(SessionSummary {
        id: row.get(0)?,
        runtime_id: row.get(1)?,
        title: row.get(2)?,
        status: SessionStatus::from_label(&status).unwrap_or(SessionStatus::Failed),
        native_id: row.get(4)?,
        model: row.get(5)?,
        project_path: row.get(6)?,
        pinned: row.get::<_, i64>(7)? == 1,
        archived: row.get::<_, i64>(8)? == 1,
        created_at: row.get(9)?,
        updated_at: row.get(10)?,
        preview: row.get(11)?,
        message_count: row.get(12)?,
    })
}

fn split_cursor(cursor: &str) -> Option<String> {
    let (_, id) = cursor.rsplit_once('|')?;
    if id.is_empty() {
        None
    } else {
        Some(id.to_string())
    }
}

fn expect_one(changed: usize, what: &str, id: &str) -> Result<()> {
    if changed == 0 {
        Err(Error::NotFound(format!("{what} {id:?}")))
    } else {
        Ok(())
    }
}
