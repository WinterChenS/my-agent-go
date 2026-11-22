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
use crate::memory::{Memory, MemoryDraft, MemoryPatch, MemorySettings};
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

/// One row of the memory table as the UI sees it.
fn read_memory(row: &rusqlite::Row<'_>) -> rusqlite::Result<Memory> {
    Ok(Memory {
        id: row.get(0)?,
        scope: row.get(1)?,
        session_id: row.get(2)?,
        session_title: row.get(3)?,
        agent_id: row.get(4)?,
        kind: row.get(5)?,
        text: row.get(6)?,
        source: row.get(7)?,
        pinned: row.get::<_, i64>(8)? != 0,
        enabled: row.get::<_, i64>(9)? != 0,
        created_at: row.get(10)?,
        updated_at: row.get(11)?,
    })
}

/// Whether the ledger should record a conclusion for the session that just
/// answered. Read inside the message transaction: the switch is a setting, and a
/// setting read from a stale cache would keep recording after the user said no.
fn record_sessions(connection: &Connection) -> bool {
    connection
        .query_row(
            "SELECT value FROM memory_settings WHERE key = 'recordSessions'",
            [],
            |row| row.get::<_, String>(0),
        )
        .map(|value| value != "0")
        .unwrap_or(true)
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
            );
            -- Memory: what the app is allowed to remember. `scope='user'` rows are
            -- injected into every turn; `scope='session'` rows are one conclusion
            -- per conversation, kept for the user to review or promote.
            CREATE TABLE IF NOT EXISTS memories (
                id TEXT PRIMARY KEY,
                scope TEXT NOT NULL,
                session_id TEXT NOT NULL DEFAULT '',
                agent_id TEXT NOT NULL DEFAULT '',
                kind TEXT NOT NULL,
                text TEXT NOT NULL,
                source TEXT NOT NULL,
                pinned INTEGER NOT NULL DEFAULT 0,
                enabled INTEGER NOT NULL DEFAULT 1,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );
            CREATE INDEX IF NOT EXISTS memories_order ON memories (scope, pinned DESC, updated_at DESC, id DESC);
            CREATE INDEX IF NOT EXISTS memories_session ON memories (session_id);
            CREATE TABLE IF NOT EXISTS memory_settings (
                key TEXT PRIMARY KEY,
                value TEXT NOT NULL
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

    // ---- memory ---------------------------------------------------------

    /// Memory rows, pinned first then newest, optionally narrowed by scope and
    /// a substring. The page renders exactly these rows; injection reads the
    /// same ones, so nothing reaches the model that the list does not show.
    pub fn memories(&self, scope: Option<&str>, query: Option<&str>) -> Result<Vec<Memory>> {
        let needle = query
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(|value| format!("%{}%", value.replace('%', "\\%")));
        let mut statement = self.connection.prepare(
            "SELECT m.id, m.scope, m.session_id, COALESCE(s.title, ''), m.agent_id,
                    m.kind, m.text, m.source, m.pinned, m.enabled, m.created_at, m.updated_at
             FROM memories m LEFT JOIN sessions s ON s.id = m.session_id
             WHERE (?1 IS NULL OR m.scope = ?1)
               AND (?2 IS NULL OR m.text LIKE ?2)
             ORDER BY m.pinned DESC, m.updated_at DESC, m.id DESC
             LIMIT 500",
        )?;
        let rows = statement.query_map(params![scope, needle], read_memory)?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    pub fn memory(&self, id: &str) -> Result<Memory> {
        let mut statement = self.connection.prepare(
            "SELECT m.id, m.scope, m.session_id, COALESCE(s.title, ''), m.agent_id,
                    m.kind, m.text, m.source, m.pinned, m.enabled, m.created_at, m.updated_at
             FROM memories m LEFT JOIN sessions s ON s.id = m.session_id
             WHERE m.id = ?1",
        )?;
        statement
            .query_row(params![id], read_memory)
            .optional()?
            .ok_or_else(|| Error::NotFound(format!("memory {id:?}")))
    }

    /// Save a new memory. The text is flattened and capped here rather than in
    /// the page, so the CLI and the UI cannot produce two different shapes.
    pub fn add_memory(&mut self, draft: &MemoryDraft, at: &str) -> Result<Memory> {
        let text = crate::memory::normalize_text(&draft.text);
        if text.is_empty() {
            return Err(Error::Rejected("a memory needs some text".to_string()));
        }
        let scope = crate::memory::normalize_scope(&draft.scope);
        if scope == "session" && draft.session_id.trim().is_empty() {
            return Err(Error::Rejected(
                "a session memory needs the session it belongs to".to_string(),
            ));
        }
        let id = self.next_memory_id()?;
        let source = match draft.source.trim() {
            "agent" => "agent",
            "session" => "session",
            _ => "manual",
        };
        self.connection.execute(
            "INSERT INTO memories (id, scope, session_id, agent_id, kind, text, source, pinned, enabled, created_at, updated_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, 1, ?9, ?9)",
            params![
                id,
                scope,
                draft.session_id.trim(),
                draft.agent_id.trim(),
                crate::memory::normalize_kind(&draft.kind),
                text,
                source,
                i64::from(draft.pinned),
                at
            ],
        )?;
        self.memory(&id)
    }

    /// Edit one row. A user edit flips `source` to `manual`, which is what stops
    /// the host from overwriting it with the next conversation's conclusion.
    pub fn update_memory(&mut self, id: &str, patch: &MemoryPatch, at: &str) -> Result<Memory> {
        let current = self.memory(id)?;
        let text = match &patch.text {
            Some(value) => {
                let text = crate::memory::normalize_text(value);
                if text.is_empty() {
                    return Err(Error::Rejected("a memory needs some text".to_string()));
                }
                text
            }
            None => current.text.clone(),
        };
        let kind = patch
            .kind
            .as_deref()
            .map_or_else(|| current.kind.clone(), crate::memory::normalize_kind);
        let pinned = patch.pinned.unwrap_or(current.pinned);
        let enabled = patch.enabled.unwrap_or(current.enabled);
        let source = if patch.text.is_some() && current.source != "manual" {
            "manual"
        } else {
            current.source.as_str()
        };
        self.connection.execute(
            "UPDATE memories SET text = ?2, kind = ?3, pinned = ?4, enabled = ?5, source = ?6, updated_at = ?7
             WHERE id = ?1",
            params![id, text, kind, i64::from(pinned), i64::from(enabled), source, at],
        )?;
        self.memory(id)
    }

    pub fn delete_memory(&mut self, id: &str) -> Result<()> {
        let removed = self
            .connection
            .execute("DELETE FROM memories WHERE id = ?1", params![id])?;
        if removed == 0 {
            return Err(Error::NotFound(format!("memory {id:?}")));
        }
        Ok(())
    }

    /// Remove every row in a scope (or all of them). Real deletes: a "clear all"
    /// that left rows behind would be the lie this feature exists to avoid.
    pub fn clear_memories(&mut self, scope: Option<&str>) -> Result<usize> {
        let removed = match scope {
            Some(scope) => self
                .connection
                .execute("DELETE FROM memories WHERE scope = ?1", params![scope])?,
            None => self.connection.execute("DELETE FROM memories", [])?,
        };
        Ok(removed)
    }

    pub fn memory_settings(&self) -> Result<MemorySettings> {
        let mut statement = self
            .connection
            .prepare("SELECT key, value FROM memory_settings")?;
        let rows = statement.query_map([], |row| {
            Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
        })?;
        let mut settings = MemorySettings::default();
        for row in rows {
            let (key, value) = row?;
            match key.as_str() {
                "inject" => settings.inject = value != "0",
                "recordSessions" => settings.record_sessions = value != "0",
                _ => {}
            }
        }
        Ok(settings)
    }

    /// Change one or both switches. `None` keeps the stored value.
    pub fn set_memory_settings(
        &mut self,
        inject: Option<bool>,
        record_sessions: Option<bool>,
    ) -> Result<MemorySettings> {
        for (key, value) in [("inject", inject), ("recordSessions", record_sessions)] {
            let Some(value) = value else { continue };
            self.connection.execute(
                "INSERT INTO memory_settings (key, value) VALUES (?1, ?2)
                 ON CONFLICT(key) DO UPDATE SET value = excluded.value",
                params![key, if value { "1" } else { "0" }],
            )?;
        }
        self.memory_settings()
    }

    /// `mem-<millis>-<seq>`, checked against the table: a counter resets with
    /// the process, and a second-resolution id silently replaced a row once.
    fn next_memory_id(&self) -> Result<String> {
        static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        loop {
            let seq = SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            let candidate = format!("mem-{}-{seq}", crate::clock::unix_millis());
            let taken: Option<i64> = self
                .connection
                .query_row(
                    "SELECT 1 FROM memories WHERE id = ?1",
                    params![candidate],
                    |row| row.get(0),
                )
                .optional()?;
            if taken.is_none() {
                return Ok(candidate);
            }
        }
    }

    /// The run records of one agent, newest first.
    ///
    /// A session used to be the only source, which lost every delegated child:
    /// pi runs sub agents inside their parent's process, so they exist only as
    /// `sub_agent` events. The two sources are merged here — main rows from
    /// sessions owned by this agent, sub rows from delegation events naming it.
    ///
    /// `aliases` carries the agent's slug (sessions store the slug, while the
    /// agents page selects by id) and, for the built-in default, the empty
    /// agent id of a conversation opened from the model picker.
    pub fn agent_runs(
        &self,
        agent_id: &str,
        aliases: &[String],
        limit: usize,
    ) -> Result<Vec<AgentRun>> {
        let mut names: Vec<String> = Vec::new();
        for candidate in std::iter::once(agent_id).chain(aliases.iter().map(String::as_str)) {
            if !candidate.trim().is_empty() && !names.iter().any(|name| name == candidate) {
                names.push(candidate.to_string());
            }
        }
        let mut session_ids: Vec<String> = aliases.to_vec();
        if !session_ids.iter().any(|id| id == agent_id) {
            session_ids.push(agent_id.to_string());
        }
        session_ids.dedup();

        let mut rows: Vec<AgentRun> = Vec::new();
        if !session_ids.is_empty() {
            let marks = vec!["?"; session_ids.len()].join(", ");
            let sql = format!(
                "SELECT s.id, s.title, s.updated_at, s.status,
                        (SELECT COUNT(*) FROM runs r WHERE r.session_id = s.id),
                        (SELECT COUNT(*) FROM events e
                           WHERE e.session_id = s.id AND e.kind = 'tool_call')
                        +
                        (SELECT COUNT(DISTINCT json_extract(e.payload, '$.agentId'))
                           FROM events e
                           WHERE e.session_id = s.id AND e.kind = 'sub_agent'
                             AND json_extract(e.payload, '$.phase') = 'started'),
                        COALESCE((
                           SELECT SUM(CAST(json_extract(e.payload, '$.usage.totalTokens') AS INTEGER))
                           FROM events e
                           WHERE e.session_id = s.id AND e.kind = 'usage'
                        ), 0)
                 FROM sessions s
                 WHERE s.agent_id IN ({marks})
                 ORDER BY s.updated_at DESC, s.id DESC LIMIT ?"
            );
            let mut args: Vec<&dyn rusqlite::types::ToSql> =
                session_ids.iter().map(|id| id as &dyn rusqlite::types::ToSql).collect();
            let cap = limit as i64;
            args.push(&cap);
            let mut statement = self.connection.prepare(&sql)?;
            let found = statement.query_map(rusqlite::params_from_iter(args), |row| {
                Ok(AgentRun {
                    session_id: row.get(0)?,
                    title: row.get(1)?,
                    at: row.get(2)?,
                    status: row.get(3)?,
                    turns: row.get(4)?,
                    tool_calls: row.get(5)?,
                    tokens: row.get(6)?,
                    kind: "main".to_string(),
                    agent_name: agent_id.to_string(),
                    run_key: String::new(),
                })
            })?;
            for row in found {
                let mut run = row?;
                run.run_key = run.session_id.clone();
                rows.push(run);
            }
        }

        // Delegations: fold the event stream into one row per child call.
        //
        // Filter in Rust, not SQL: a progress snapshot can omit `name` even
        // though it belongs to a child announced with one. Selecting only rows
        // that carry the name used to drop every tool call from the count.
        if !names.is_empty() {
            let mut statement = self.connection.prepare(
                "SELECT e.session_id, COALESCE(s.title, ''), e.payload, e.at
                 FROM events e LEFT JOIN sessions s ON s.id = e.session_id
                 WHERE e.kind = 'sub_agent'
                 ORDER BY e.session_id, e.at, e.seq LIMIT 5000",
            )?;
            let found = statement.query_map([], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, String>(3)?,
                ))
            })?;
            let mut raw: Vec<(String, String, serde_json::Value, String)> = Vec::new();
            for row in found {
                let (session_id, title, payload, at) = row?;
                let event: serde_json::Value = serde_json::from_str(&payload).unwrap_or_default();
                raw.push((session_id, title, event, at));
            }
            let key_of = |session: &str, event: &serde_json::Value| -> String {
                format!(
                    "{}|{}",
                    session,
                    event.get("agentId").and_then(serde_json::Value::as_str).unwrap_or("")
                )
            };
            let matching: std::collections::BTreeSet<String> = raw
                .iter()
                .filter(|(_, _, event, _)| {
                    event
                        .get("name")
                        .and_then(serde_json::Value::as_str)
                        .is_some_and(|name| names.iter().any(|wanted| wanted == name))
                })
                .map(|(session, _, event, _)| key_of(session, event))
                .collect();
            let mut children: std::collections::BTreeMap<String, AgentRun> =
                std::collections::BTreeMap::new();
            for (session_id, session_title, event, at) in raw {
                let key = key_of(&session_id, &event);
                if !matching.contains(&key) {
                    continue;
                }
                let phase = event.get("phase").and_then(serde_json::Value::as_str).unwrap_or("");
                let child = children.entry(key.clone()).or_insert_with(|| AgentRun {
                    session_id: session_id.clone(),
                    title: session_title.clone(),
                    at: at.clone(),
                    turns: 1,
                    tool_calls: 0,
                    tokens: 0,
                    status: "running".to_string(),
                    kind: "sub".to_string(),
                    agent_name: event
                        .get("name")
                        .and_then(serde_json::Value::as_str)
                        .unwrap_or_default()
                        .to_string(),
                    run_key: key,
                });
                if phase == "started" {
                    if let Some(task) = event.get("task").and_then(serde_json::Value::as_str) {
                        let short: String = task.chars().take(80).collect();
                        child.title = if task.chars().count() > 80 {
                            format!("{short}…")
                        } else {
                            short
                        };
                    }
                    child.at = at.clone();
                }
                if phase == "snapshot" {
                    let calls = event
                        .get("steps")
                        .and_then(serde_json::Value::as_array)
                        .map(|steps| {
                            steps
                                .iter()
                                .filter(|step| {
                                    step.get("kind").and_then(serde_json::Value::as_str)
                                        == Some("tool")
                                })
                                .count() as i64
                        })
                        .unwrap_or(0);
                    child.tool_calls = child.tool_calls.max(calls);
                }
                if matches!(phase, "completed" | "failed" | "interrupted") {
                    child.status = phase.to_string();
                    if let Some(usage) = event.get("usage") {
                        let number = |key: &str| -> i64 {
                            usage
                                .get(key)
                                .and_then(serde_json::Value::as_i64)
                                .unwrap_or(0)
                        };
                        if let Some(turns) = usage.get("turns").and_then(serde_json::Value::as_i64) {
                            child.turns = turns.max(1);
                        }
                        let total = number("totalTokens");
                        child.tokens = if total > 0 {
                            total
                        } else {
                            number("input") + number("output") + number("cacheRead")
                        };
                    }
                }
            }
            rows.extend(children.into_values());
        }

        rows.sort_by(|left, right| right.at.cmp(&left.at).then_with(|| right.run_key.cmp(&left.run_key)));
        rows.truncate(limit);
        Ok(rows)
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
            // One conclusion row per conversation, from the assistant's own last
            // answer. A row the user edited (`source='manual'`) or pinned is left
            // alone: their version wins over the machine's.
            if record_sessions(&transaction) {
                let body = crate::memory::normalize_text(text);
                let body: String = body.chars().take(600).collect();
                if !body.is_empty() {
                    let agent: String = transaction
                        .query_row(
                            "SELECT agent_id FROM sessions WHERE id = ?1",
                            params![session_id],
                            |row| row.get(0),
                        )
                        .unwrap_or_default();
                    transaction.execute(
                        "INSERT INTO memories (id, scope, session_id, agent_id, kind, text, source, pinned, enabled, created_at, updated_at)
                         VALUES (?1, 'session', ?2, ?3, 'conclusion', ?4, 'session', 0, 1, ?5, ?5)
                         ON CONFLICT(id) DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at
                         WHERE memories.source = 'session'",
                        params![
                            format!("session-memory:{session_id}"),
                            session_id,
                            agent,
                            body,
                            at
                        ],
                    )?;
                }
            }
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
        let runs = store.agent_runs("agent-a", &[], 20).expect("runs");
        assert_eq!(runs.len(), 2);
        let first = runs.iter().find(|run| run.session_id == "a1").expect("a1");
        assert_eq!(first.tool_calls, 1);
        assert_eq!(first.tokens, 42);
        assert_eq!(first.turns, 0, "no run row means no turns");
        assert!(
            store
                .agent_runs("agent-b", &[], 20)
                .expect("b")
                .iter()
                .all(|r| r.session_id == "b1")
        );
        assert!(store.agent_memory("agent-a", 3).expect("memory").is_empty());
    }

    /// The ledger stores the slug, the page selects by id. One without the
    /// other used to make every saved agent's run list empty.
    #[test]
    fn a_saved_agent_finds_its_sessions_by_slug() {
        let mut store = Store::open_in_memory().expect("store");
        session(&mut store, "s1", "night-reviewer");
        let none = store.agent_runs("agent-123", &[], 20).expect("runs");
        assert!(none.is_empty(), "id alone cannot match a stored slug");
        let found = store
            .agent_runs("agent-123", &["night-reviewer".to_string()], 20)
            .expect("runs");
        assert_eq!(found.len(), 1);
        assert_eq!(found[0].session_id, "s1");
        assert_eq!(found[0].kind, "main");
        assert_eq!(found[0].run_key, "s1");
    }

    /// A delegated child has no session of its own. Its whole record is in the
    /// event stream, and the snapshots repeat steps, so `max` not `sum`.
    #[test]
    fn delegated_children_get_their_own_run_rows() {
        let mut store = Store::open_in_memory().expect("store");
        session(&mut store, "s1", "parent");
        let mut event = |payload: serde_json::Value| {
            store
                .append_event("s1", "r1", EventKind::SubAgent, &payload)
                .expect("event");
        };
        event(serde_json::json!({
            "agentId": "call_1#0", "name": "scout", "phase": "started",
            "task": "清点仓库里的测试文件，并报告数量", "status": "running"
        }));
        event(serde_json::json!({
            "agentId": "call_1#0", "phase": "snapshot", "status": "running",
            "steps": [
                { "kind": "tool", "name": "ls" },
                { "kind": "tool", "name": "find" },
                { "kind": "text", "name": "" }
            ]
        }));
        event(serde_json::json!({
            "agentId": "call_1#0", "phase": "snapshot", "status": "running",
            "steps": [
                { "kind": "tool", "name": "ls" },
                { "kind": "tool", "name": "find" },
                { "kind": "tool", "name": "cat" },
                { "kind": "text", "name": "" }
            ]
        }));
        event(serde_json::json!({
            "agentId": "call_1#0", "name": "scout", "phase": "completed",
            "status": "completed", "result": "12 files",
            "usage": { "turns": 2, "input": 30, "output": 7, "cacheRead": 12 }
        }));
        // A different child in the same conversation must not be folded in.
        event(serde_json::json!({
            "agentId": "call_1#1", "name": "reviewer", "phase": "started",
            "task": "审阅改动", "status": "running"
        }));

        // The parent only owns the conversation; the children are separate
        // rows on the agents named in the delegation events.
        let parent = store
            .agent_runs("parent", &["parent".to_string()], 20)
            .expect("parent runs");
        assert_eq!(parent.len(), 1);
        assert_eq!(parent[0].kind, "main");
        assert_eq!(
            parent[0].tool_calls, 2,
            "two delegations are two tool calls, but the child's tools are its own"
        );

        let rows = store
            .agent_runs("scout", &["scout".to_string()], 20)
            .expect("scout runs");
        assert_eq!(rows.len(), 1, "one child call, however many observations");
        let scout = &rows[0];
        assert_eq!(scout.tool_calls, 3, "cumulative snapshots must not be summed");
        assert_eq!(scout.turns, 2);
        assert_eq!(scout.tokens, 49, "input + output + cache read");
        assert_eq!(scout.status, "completed");
        assert!(scout.title.contains("清点仓库"));
        let reviewer = store
            .agent_runs("reviewer", &["reviewer".to_string()], 20)
            .expect("reviewer runs");
        assert_eq!(reviewer.len(), 1);
        assert_eq!(reviewer[0].tool_calls, 0);
        assert_eq!(reviewer[0].status, "running");
    }
}
