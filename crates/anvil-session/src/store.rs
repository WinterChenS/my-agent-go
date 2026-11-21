//! SQLite ledger.
//!
//! Five tables, append-only for events and messages:
//! * `sessions` — the index the history panel reads;
//! * `projects` — the directory index those sessions group by;
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
    AgentRun, EventKind, Message, ProjectSummary, Role, Run, RunEvent, RunStatus, SessionPage,
    SessionQuery, SessionStatus, SessionSummary,
};

/// One row to insert into `sessions`.
#[derive(Debug, Clone, Default)]
pub struct NewSession<'a> {
    pub id: &'a str,
    pub runtime_id: &'a str,
    pub title: &'a str,
    pub model: &'a str,
    pub project_path: &'a str,
    /// Parent session when this one is a branch; empty otherwise.
    pub parent_id: &'a str,
    /// `seq` in the parent the branch was taken at; `0` without a parent.
    pub branch_at: i64,
    pub allow_tools: bool,
    pub tools: &'a [String],
    /// Agent persona the session runs with; all of it is resolved at open time
    /// and stored, so a resumed turn does not depend on the page's current
    /// state.
    pub agent_id: &'a str,
    pub thinking: &'a str,
    pub system_prompt: &'a str,
    pub append_prompt: &'a str,
    pub skills: &'a [String],
    pub at: &'a str,
}

/// The persona a session was opened with, as the adapter needs it on every
/// turn. A resumed turn uses this, not the agent page's current values.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SessionPersona {
    pub agent_id: String,
    pub thinking: String,
    pub system_prompt: String,
    pub append_prompt: String,
    /// Absolute or project-relative skill paths.
    pub skills: Vec<String>,
}

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
        // Two connections write this file now (the turn's writer and the
        // scheduler's claims). Without a busy timeout a schedule write landing
        // during a turn would fail instantly with SQLITE_BUSY instead of
        // waiting its turn.
        connection.busy_timeout(std::time::Duration::from_secs(5))?;
        connection.execute_batch(
            "CREATE TABLE IF NOT EXISTS sessions (
                id TEXT PRIMARY KEY,
                runtime_id TEXT NOT NULL,
                title TEXT NOT NULL,
                status TEXT NOT NULL,
                native_id TEXT NOT NULL DEFAULT '',
                model TEXT NOT NULL DEFAULT '',
                project_path TEXT NOT NULL DEFAULT '',
                parent_id TEXT NOT NULL DEFAULT '',
                branch_at INTEGER NOT NULL DEFAULT 0,
                allow_tools INTEGER NOT NULL DEFAULT 0,
                tools TEXT NOT NULL DEFAULT '',
                agent_id TEXT NOT NULL DEFAULT '',
                thinking TEXT NOT NULL DEFAULT '',
                system_prompt TEXT NOT NULL DEFAULT '',
                append_prompt TEXT NOT NULL DEFAULT '',
                skills TEXT NOT NULL DEFAULT '',
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
            CREATE INDEX IF NOT EXISTS messages_order ON messages (session_id, seq);
            CREATE TABLE IF NOT EXISTS projects (
                path TEXT PRIMARY KEY,
                name TEXT NOT NULL,
                added_at TEXT NOT NULL,
                last_used_at TEXT NOT NULL
            );",
        )?;
        // Databases written before the branch columns existed are still valid
        // history; add the columns instead of refusing to open them.
        ensure_column(
            &connection,
            "sessions",
            "parent_id",
            "TEXT NOT NULL DEFAULT ''",
        )?;
        ensure_column(
            &connection,
            "sessions",
            "branch_at",
            "INTEGER NOT NULL DEFAULT 0",
        )?;
        // A session opened before the agent page grew a persona has no persona.
        // Adding the columns keeps that history readable: the turn falls back to
        // the runtime's own defaults, exactly as it ran back then.
        for (column, declaration) in [
            ("agent_id", "TEXT NOT NULL DEFAULT ''"),
            ("thinking", "TEXT NOT NULL DEFAULT ''"),
            ("system_prompt", "TEXT NOT NULL DEFAULT ''"),
            ("append_prompt", "TEXT NOT NULL DEFAULT ''"),
            ("skills", "TEXT NOT NULL DEFAULT ''"),
        ] {
            ensure_column(&connection, "sessions", column, declaration)?;
        }
        connection.execute_batch(
            "CREATE INDEX IF NOT EXISTS sessions_project ON sessions (project_path, updated_at DESC);",
        )?;
        Ok(Self { connection })
    }

    // ---- sessions -------------------------------------------------------

    pub fn create_session(&mut self, new: &NewSession<'_>) -> Result<()> {
        let transaction = self.connection.transaction()?;
        transaction.execute(
            "INSERT INTO sessions (id, runtime_id, title, status, native_id, model, project_path,
                                   parent_id, branch_at, allow_tools, tools, pinned, archived,
                                   agent_id, thinking, system_prompt, append_prompt, skills,
                                   created_at, updated_at, preview)
             VALUES (?1, ?2, ?3, 'idle', '', ?4, ?5, ?6, ?7, ?8, ?9, 0, 0,
                     ?11, ?12, ?13, ?14, ?15, ?10, ?10, '')",
            params![
                new.id,
                new.runtime_id,
                new.title,
                new.model,
                // The directory is the grouping key: store it without stray
                // whitespace so a query for the same project always matches.
                new.project_path.trim(),
                new.parent_id,
                new.branch_at,
                i64::from(new.allow_tools),
                new.tools.join(","),
                new.at,
                new.agent_id.trim(),
                new.thinking.trim(),
                new.system_prompt,
                new.append_prompt,
                new.skills.join("\n"),
            ],
        )?;
        // The project index is written in the same transaction as the session
        // that justifies it, so a project can never exist without a session and
        // a session can never be missing from a project.
        if !new.project_path.trim().is_empty() {
            transaction.execute(
                "INSERT INTO projects (path, name, added_at, last_used_at) VALUES (?1, ?2, ?3, ?3)
                 ON CONFLICT (path) DO UPDATE SET last_used_at = excluded.last_used_at",
                params![
                    new.project_path.trim(),
                    project_name(new.project_path),
                    new.at
                ],
            )?;
        }
        transaction.commit()?;
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
        let transaction = self.connection.transaction()?;
        let project_path: Option<String> = transaction
            .query_row(
                "SELECT project_path FROM sessions WHERE id = ?1",
                params![id],
                |row| row.get(0),
            )
            .optional()?;
        let changed = transaction.execute("DELETE FROM sessions WHERE id = ?1", params![id])?;
        if changed == 0 {
            return Err(Error::NotFound(format!("session {id:?}")));
        }
        // Deleting the last session of a project retires the project row too;
        // otherwise the history panel would list an empty folder forever.
        if let Some(path) = project_path.filter(|path| !path.trim().is_empty()) {
            transaction.execute(
                "DELETE FROM projects WHERE path = ?1
                 AND NOT EXISTS (SELECT 1 FROM sessions WHERE project_path = ?1)",
                params![path],
            )?;
        }
        transaction.commit()?;
        Ok(())
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

    /// The persona a session was opened with.
    ///
    /// Kept out of [`SessionSummary`] on purpose: the history list returns
    /// dozens of rows and none of them need a full system prompt.
    pub fn persona(&self, id: &str) -> Result<SessionPersona> {
        let (agent_id, thinking, system_prompt, append_prompt, skills): (
            String,
            String,
            String,
            String,
            String,
        ) = self
            .connection
            .query_row(
                "SELECT agent_id, thinking, system_prompt, append_prompt, skills
                 FROM sessions WHERE id = ?1",
                params![id],
                |row| {
                    Ok((
                        row.get(0)?,
                        row.get(1)?,
                        row.get(2)?,
                        row.get(3)?,
                        row.get(4)?,
                    ))
                },
            )
            .optional()?
            .ok_or_else(|| Error::NotFound(format!("session {id:?}")))?;
        Ok(SessionPersona {
            agent_id,
            thinking,
            system_prompt,
            append_prompt,
            skills: skills
                .lines()
                .filter(|line| !line.trim().is_empty())
                .map(str::to_string)
                .collect(),
        })
    }

    /// Recent conclusions of one agent, for cross-session memory.
    ///
    /// Only the stored previews are read: injecting whole transcripts would
    /// invent context the user never approved. A session with no assistant text
    /// contributes nothing.
    pub fn agent_memory(&self, agent_id: &str, limit: usize) -> Result<Vec<(String, String)>> {
        let mut statement = self.connection.prepare(
            "SELECT title, preview FROM sessions
             WHERE agent_id = ?1 AND TRIM(preview) <> ''
             ORDER BY updated_at DESC, id DESC LIMIT ?2",
        )?;
        let rows = statement.query_map(params![agent_id, limit as i64], |row| {
            Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// The run records of one agent, newest first. Counts come from the ledger,
    /// so a session with no events is honestly `0` turns, `0` tools, `0` tokens.
    pub fn agent_runs(&self, agent_id: &str, limit: usize) -> Result<Vec<AgentRun>> {
        let mut statement = self.connection.prepare(
            "SELECT s.id, s.title, s.updated_at, s.status,
                    (SELECT COUNT(*) FROM runs r WHERE r.session_id = s.id),
                    (SELECT COUNT(*) FROM events e
                       WHERE e.session_id = s.id AND e.kind = 'tool_call'),
                    COALESCE((
                       SELECT SUM(CAST(json_extract(e.payload, '$.usage.totalTokens') AS INTEGER))
                       FROM events e
                       WHERE e.session_id = s.id AND e.kind = 'usage'
                    ), 0)
             FROM sessions s
             WHERE s.agent_id = ?1
             ORDER BY s.updated_at DESC, s.id DESC LIMIT ?2",
        )?;
        let rows = statement.query_map(params![agent_id, limit as i64], |row| {
            Ok(AgentRun {
                session_id: row.get(0)?,
                title: row.get(1)?,
                at: row.get(2)?,
                status: row.get(3)?,
                turns: row.get(4)?,
                tool_calls: row.get(5)?,
                tokens: row.get(6)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// History page. `cursor` is the opaque `updated_at|id` of the last row of
    /// the previous page; ordering is `pinned DESC, updated_at DESC, id DESC`,
    /// which is total, so pages never overlap or skip.
    pub fn sessions(&self, query: &SessionQuery) -> Result<SessionPage> {
        let query = query.normalized();
        let limit = query.page_size();
        let (where_clause, args) = session_filters(&query);

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

        let total = self.count_sessions(&query)?;
        Ok(SessionPage {
            sessions,
            next_cursor,
            total,
        })
    }

    fn count_sessions(&self, query: &SessionQuery) -> Result<i64> {
        let (where_clause, args) = session_filters(query);
        let sql = format!("SELECT COUNT(*) FROM sessions s{where_clause}");
        let total: i64 =
            self.connection
                .query_row(&sql, rusqlite::params_from_iter(args), |row| row.get(0))?;
        Ok(total)
    }

    /// Projects that still have at least one live session, most recent first.
    ///
    /// The count is computed from `sessions`, not stored on the project row: a
    /// denormalised counter is exactly the thing that drifts after a delete.
    pub fn projects(&self) -> Result<Vec<ProjectSummary>> {
        let mut statement = self.connection.prepare(
            "SELECT p.path, p.name, COUNT(s.id) AS session_count,
                    COALESCE(MAX(s.updated_at), p.last_used_at) AS last_updated
             FROM projects p
             LEFT JOIN sessions s ON s.project_path = p.path AND s.archived = 0
             GROUP BY p.path
             HAVING session_count > 0
             ORDER BY last_updated DESC, p.path",
        )?;
        let rows = statement.query_map([], |row| {
            Ok(ProjectSummary {
                path: row.get(0)?,
                name: row.get(1)?,
                session_count: row.get(2)?,
                last_updated: row.get(3)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
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

    /// Mark every run still marked running as failed, and reset the sessions
    /// that were waiting on it. Called on start-up: a process that died mid-run
    /// must not leave a session "running" forever, or its next send is refused.
    pub fn fail_interrupted_runs(&mut self, at: &str) -> Result<usize> {
        let transaction = self.connection.transaction()?;
        let changed = transaction.execute(
            "UPDATE runs SET status = 'failed', ended_at = ?1, error = 'process exited during the run'
             WHERE status = 'running'",
            params![at],
        )?;
        transaction.execute(
            "UPDATE sessions SET status = 'idle', updated_at = ?1
             WHERE status = 'running'
               AND NOT EXISTS (
                   SELECT 1 FROM runs r WHERE r.session_id = sessions.id AND r.status = 'running')",
            params![at],
        )?;
        transaction.commit()?;
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
        s.project_path, s.parent_id, s.branch_at, s.allow_tools, s.tools, s.pinned, s.archived,
        s.created_at, s.updated_at, s.preview, s.agent_id, s.thinking,
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
        parent_id: row.get(7)?,
        branch_at: row.get(8)?,
        allow_tools: row.get::<_, i64>(9)? == 1,
        tools: row
            .get::<_, String>(10)?
            .split(',')
            .filter(|name| !name.is_empty())
            .map(str::to_string)
            .collect(),
        pinned: row.get::<_, i64>(11)? == 1,
        archived: row.get::<_, i64>(12)? == 1,
        created_at: row.get(13)?,
        updated_at: row.get(14)?,
        preview: row.get(15)?,
        agent_id: row.get(16)?,
        thinking: row.get(17)?,
        message_count: row.get(18)?,
    })
}

/// `WHERE` clause and its positional arguments for one query.
///
/// The cursor is a row identity, not an offset: paging by offset over a list
/// that the running turn keeps updating would skip or repeat rows.
fn session_filters(query: &SessionQuery) -> (String, Vec<rusqlite::types::Value>) {
    let mut clauses: Vec<String> = Vec::new();
    let mut args: Vec<rusqlite::types::Value> = Vec::new();

    if let Some(runtime_id) = query.runtime_id.as_deref() {
        clauses.push(format!("s.runtime_id = ?{}", args.len() + 1));
        args.push(runtime_id.to_string().into());
    }
    if let Some(project_path) = query.project_path.as_deref() {
        clauses.push(format!("s.project_path = ?{}", args.len() + 1));
        args.push(project_path.to_string().into());
    }
    if !query.include_archived {
        clauses.push("s.archived = 0".to_string());
    }
    if let Some(search) = query.search.as_deref() {
        clauses.push(format!(
            "(s.title LIKE ?{0} OR s.preview LIKE ?{0} OR EXISTS (
                SELECT 1 FROM messages m
                WHERE m.session_id = s.id AND m.text LIKE ?{0}))",
            args.len() + 1
        ));
        args.push(format!("%{search}%").into());
    }
    if let Some(cursor) = query.cursor.as_deref().and_then(split_cursor) {
        let position = args.len() + 1;
        clauses.push(format!(
            "(s.pinned, s.updated_at, s.id) <
             (SELECT pinned, updated_at, id FROM sessions WHERE id = ?{position})"
        ));
        args.push(cursor.into());
    }

    let where_clause = if clauses.is_empty() {
        String::new()
    } else {
        format!(" WHERE {}", clauses.join(" AND "))
    };
    (where_clause, args)
}

/// Display name for a project directory: its last segment.
fn project_name(path: &str) -> String {
    let trimmed = path.trim().trim_end_matches(['\\', '/']);
    trimmed
        .rsplit(['\\', '/'])
        .next()
        .filter(|name| !name.is_empty())
        .unwrap_or(trimmed)
        .to_string()
}

/// Add a column to an existing table when it is missing.
fn ensure_column(
    connection: &Connection,
    table: &str,
    column: &str,
    declaration: &str,
) -> Result<()> {
    let mut statement = connection.prepare(&format!("PRAGMA table_info({table})"))?;
    let existing = statement
        .query_map([], |row| row.get::<_, String>(1))?
        .collect::<std::result::Result<Vec<_>, _>>()?;
    if existing.iter().any(|name| name == column) {
        return Ok(());
    }
    connection.execute_batch(&format!(
        "ALTER TABLE {table} ADD COLUMN {column} {declaration}"
    ))?;
    Ok(())
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

#[cfg(test)]
mod persona_tests {
    use super::*;

    fn session(store: &mut Store, id: &str, agent_id: &str) {
        store
            .create_session(&NewSession {
                id,
                runtime_id: "pi",
                title: "迁移评估",
                model: "auto/fast",
                project_path: "D:/work/api",
                parent_id: "",
                branch_at: 0,
                allow_tools: true,
                tools: &["read".to_string(), "grep".to_string()],
                agent_id,
                thinking: "high",
                system_prompt: "你是迁移工程师，仓库 {{repo_path}}",
                append_prompt: "历史结论摘要",
                skills: &["D:/skills/review.md".to_string()],
                at: "2026-11-15T00:00:00Z",
            })
            .expect("create");
    }

    #[test]
    fn the_persona_is_stored_and_read_back_for_every_later_turn() {
        let mut store = Store::open_in_memory().expect("store");
        session(&mut store, "s1", "migration-engineer");
        let persona = store.persona("s1").expect("persona");
        assert_eq!(persona.agent_id, "migration-engineer");
        assert_eq!(persona.thinking, "high");
        assert_eq!(persona.system_prompt, "你是迁移工程师，仓库 {{repo_path}}");
        assert_eq!(persona.append_prompt, "历史结论摘要");
        assert_eq!(persona.skills, vec!["D:/skills/review.md"]);
        let row = store.session("s1").expect("row");
        assert_eq!(row.agent_id, "migration-engineer");
        assert_eq!(row.thinking, "high");
    }

    #[test]
    fn an_old_session_without_a_persona_opens_with_empty_defaults() {
        let mut store = Store::open_in_memory().expect("store");
        session(&mut store, "s1", "");
        // The migration adds empty columns to rows written by the older build.
        store
            .raw()
            .execute(
                "UPDATE sessions SET thinking = '', system_prompt = '', append_prompt = '', skills = '' WHERE id = 's1'",
                [],
            )
            .expect("simulate an old row");
        let persona = store.persona("s1").expect("persona");
        assert_eq!(persona, SessionPersona::default());
    }

    #[test]
    fn run_records_are_counted_not_invented_and_are_scoped_to_one_agent() {
        let mut store = Store::open_in_memory().expect("store");
        session(&mut store, "a1", "agent-a");
        session(&mut store, "a2", "agent-a");
        session(&mut store, "b1", "agent-b");
        store
            .append_event(
                "a1",
                "r1",
                EventKind::ToolCall,
                &serde_json::json!({ "name": "read" }),
            )
            .expect("tool event");
        store
            .append_event(
                "a1",
                "r1",
                EventKind::Usage,
                &serde_json::json!({ "usage": { "totalTokens": 42 } }),
            )
            .expect("usage event");
        let runs = store.agent_runs("agent-a", 20).expect("runs");
        assert_eq!(runs.len(), 2);
        let first = runs.iter().find(|run| run.session_id == "a1").expect("a1");
        assert_eq!(first.tool_calls, 1);
        assert_eq!(first.tokens, 42);
        assert_eq!(first.turns, 0, "no run row means no turns");
        assert!(
            store
                .agent_runs("agent-b", 20)
                .expect("b")
                .iter()
                .all(|r| r.session_id == "b1")
        );
        assert!(store.agent_memory("agent-a", 3).expect("memory").is_empty());
    }
}
