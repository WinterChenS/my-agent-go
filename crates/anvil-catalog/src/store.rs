//! SQLite persistence for the runtime registry and every synced resource.
//!
//! Design rules that matter for the acceptance cases:
//!
//! * one transaction per scan, so a failing adapter cannot leave a half-written
//!   registry;
//! * a successful sync replaces that runtime's rows for one kind, a failed sync
//!   keeps the previous rows and marks the snapshot stale (an empty list must
//!   never be produced by a failure);
//! * custom models are user data and survive a discovered-model refresh;
//! * credentials are stored as opaque ciphertext blobs; nothing here can read
//!   them without the OS key.

use std::path::Path;

use rusqlite::{Connection, OptionalExtension, params};

use crate::error::{Error, Result};
use crate::ids::{instance_key, path_key};
use crate::model::{
    AdapterFailure, AgentRecord, Capabilities, CatalogSnapshot, InventoryKind, McpRecord,
    ModelRecord, RuntimeHealth, RuntimeRecord, RuntimeRegistration, RuntimeStatus, SkillRecord,
    SyncOutcome, ToolRecord,
};

const SCHEMA_VERSION: i64 = 1;

/// Id of the agent derived from the catalog's default model. It has no row: the
/// service synthesises it, so it can never point at a model that is gone.
pub const DEFAULT_AGENT_ID: &str = "default";

/// Handle to `<data dir>/catalog.sqlite`.
pub struct Store {
    connection: Connection,
}

impl Store {
    /// Open (or create) the catalog database.
    pub fn open(path: &Path) -> Result<Self> {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let connection = Connection::open(path)?;
        connection.pragma_update(None, "journal_mode", "WAL")?;
        connection.pragma_update(None, "foreign_keys", "ON")?;
        connection.pragma_update(None, "busy_timeout", 5_000)?;
        let store = Self { connection };
        store.migrate()?;
        Ok(store)
    }

    /// In-memory database for contract tests.
    pub fn open_in_memory() -> Result<Self> {
        let connection = Connection::open_in_memory()?;
        let store = Self { connection };
        store.migrate()?;
        Ok(store)
    }

    fn migrate(&self) -> Result<()> {
        self.connection.execute_batch(
            r#"
            CREATE TABLE IF NOT EXISTS meta (
                key TEXT PRIMARY KEY,
                value TEXT NOT NULL
            );
            CREATE TABLE IF NOT EXISTS runtimes (
                id TEXT PRIMARY KEY,
                runtime_id TEXT NOT NULL,
                display_name TEXT NOT NULL,
                version TEXT,
                install_path TEXT NOT NULL,
                install_key TEXT NOT NULL,
                install_source TEXT NOT NULL,
                config_path TEXT,
                status TEXT NOT NULL,
                capabilities TEXT NOT NULL,
                health TEXT NOT NULL,
                first_seen TEXT NOT NULL,
                last_seen TEXT NOT NULL,
                UNIQUE (runtime_id, install_key)
            );
            CREATE TABLE IF NOT EXISTS syncs (
                runtime_id TEXT NOT NULL,
                kind TEXT NOT NULL,
                ok INTEGER NOT NULL,
                item_count INTEGER NOT NULL,
                fetched_at TEXT NOT NULL,
                stale INTEGER NOT NULL DEFAULT 0,
                error TEXT,
                PRIMARY KEY (runtime_id, kind)
            );
            CREATE TABLE IF NOT EXISTS models (
                runtime_id TEXT NOT NULL,
                model_id TEXT NOT NULL,
                display_name TEXT NOT NULL,
                origin TEXT NOT NULL,
                protocol TEXT,
                endpoint TEXT,
                credential_ref TEXT,
                verified INTEGER NOT NULL DEFAULT 0,
                verified_at TEXT,
                is_default INTEGER NOT NULL DEFAULT 0,
                enabled INTEGER NOT NULL DEFAULT 0,
                updated_at TEXT NOT NULL,
                PRIMARY KEY (runtime_id, model_id)
            );
            CREATE TABLE IF NOT EXISTS agents (
                id TEXT PRIMARY KEY,
                name TEXT NOT NULL,
                runtime_id TEXT NOT NULL,
                model_id TEXT NOT NULL,
                is_default INTEGER NOT NULL DEFAULT 0,
                updated_at TEXT NOT NULL
            );
            CREATE TABLE IF NOT EXISTS skills (
                runtime_id TEXT NOT NULL,
                name TEXT NOT NULL,
                root TEXT NOT NULL,
                path TEXT NOT NULL,
                description TEXT NOT NULL,
                state TEXT NOT NULL,
                reason TEXT,
                updated_at TEXT NOT NULL,
                PRIMARY KEY (runtime_id, path)
            );
            CREATE TABLE IF NOT EXISTS tools (
                runtime_id TEXT NOT NULL,
                name TEXT NOT NULL,
                kind TEXT NOT NULL,
                executable TEXT,
                permission TEXT NOT NULL,
                source TEXT NOT NULL,
                PRIMARY KEY (runtime_id, name, kind)
            );
            CREATE TABLE IF NOT EXISTS mcp (
                runtime_id TEXT NOT NULL,
                name TEXT NOT NULL,
                transport TEXT NOT NULL,
                command TEXT,
                args TEXT NOT NULL,
                url TEXT,
                env_keys TEXT NOT NULL,
                header_keys TEXT NOT NULL,
                source TEXT NOT NULL,
                state TEXT NOT NULL,
                probed_at TEXT,
                PRIMARY KEY (runtime_id, name)
            );
            CREATE TABLE IF NOT EXISTS secrets (
                name TEXT PRIMARY KEY,
                ciphertext BLOB NOT NULL,
                created_at TEXT NOT NULL
            );
            "#,
        )?;
        // `CREATE TABLE IF NOT EXISTS` never adds a column to a database that
        // already exists, and the catalog has lived on user machines since G1.
        self.add_column_if_missing("models", "enabled", "INTEGER NOT NULL DEFAULT 0")?;
        // The default agent used to be derived from the default model and never
        // stored, which left the user no row to bind a runtime to. It is a real
        // row now, so an existing catalog needs the column.
        self.add_column_if_missing("agents", "is_default", "INTEGER NOT NULL DEFAULT 0")?;
        // Upgrading must not leave the chat page with nothing to pick: the model
        // that was already the default stays enabled.
        self.connection.execute(
            "UPDATE models SET enabled = 1 WHERE is_default = 1 AND enabled = 0",
            [],
        )?;
        self.connection.execute(
            "INSERT INTO meta (key, value) VALUES ('schema_version', ?1)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            params![SCHEMA_VERSION.to_string()],
        )?;
        Ok(())
    }

    /// Add one column when an older database predates it.
    fn add_column_if_missing(&self, table: &str, column: &str, ddl: &str) -> Result<()> {
        let mut statement = self
            .connection
            .prepare(&format!("PRAGMA table_info({table})"))?;
        let present = statement
            .query_map([], |row| row.get::<_, String>(1))?
            .collect::<std::result::Result<Vec<_>, _>>()?
            .iter()
            .any(|name| name == column);
        if !present {
            self.connection
                .execute_batch(&format!("ALTER TABLE {table} ADD COLUMN {column} {ddl}"))?;
        }
        Ok(())
    }

    /// Persist one scan atomically.
    ///
    /// Every registration seen in this scan is upserted. Registrations whose
    /// adapter ran successfully but no longer reported them are marked
    /// `not_installed` instead of being deleted, so the UI can show that a
    /// runtime disappeared rather than silently dropping it.
    pub fn apply_scan(
        &mut self,
        registrations: &[RuntimeRegistration],
        scanned_runtime_ids: &[String],
        scanned_at: &str,
    ) -> Result<()> {
        let transaction = self.connection.transaction()?;
        {
            let mut upsert = transaction.prepare(
                "INSERT INTO runtimes (
                    id, runtime_id, display_name, version, install_path, install_key,
                    install_source, config_path, status, capabilities, health,
                    first_seen, last_seen
                 ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?12)
                 ON CONFLICT(id) DO UPDATE SET
                    display_name = excluded.display_name,
                    version = excluded.version,
                    install_path = excluded.install_path,
                    install_key = excluded.install_key,
                    install_source = excluded.install_source,
                    config_path = excluded.config_path,
                    status = excluded.status,
                    capabilities = excluded.capabilities,
                    health = excluded.health,
                    last_seen = excluded.last_seen",
            )?;
            for registration in registrations {
                let capabilities = serde_json::to_string(&registration.capabilities)
                    .map_err(|error| Error::Format(error.to_string()))?;
                let health = serde_json::to_string(&registration.health)
                    .map_err(|error| Error::Format(error.to_string()))?;
                upsert.execute(params![
                    registration.id,
                    registration.runtime_id,
                    registration.display_name,
                    registration.version,
                    registration.executable.to_string_lossy(),
                    instance_key(&registration.executable, None),
                    registration.source.as_str(),
                    registration
                        .config_path
                        .as_ref()
                        .map(|path| path.to_string_lossy().into_owned()),
                    registration.status.as_str(),
                    capabilities,
                    health,
                    registration.last_seen,
                ])?;
            }
        }
        {
            let seen: Vec<&str> = registrations
                .iter()
                .map(|registration| registration.id.as_str())
                .collect();
            let mut missing =
                transaction.prepare("SELECT id FROM runtimes WHERE runtime_id = ?1")?;
            let mut mark = transaction.prepare("UPDATE runtimes SET status = ?2 WHERE id = ?1")?;
            for runtime_id in scanned_runtime_ids {
                let ids: Vec<String> = missing
                    .query_map(params![runtime_id], |row| row.get::<_, String>(0))?
                    .collect::<std::result::Result<_, _>>()?;
                for id in ids {
                    if !seen.contains(&id.as_str()) {
                        mark.execute(params![id, RuntimeStatus::NotInstalled.as_str()])?;
                    }
                }
            }
        }
        transaction.commit()?;
        let _ = scanned_at;
        Ok(())
    }

    /// Replace one runtime's discovered models. Custom rows are kept.
    pub fn replace_models(
        &mut self,
        runtime_id: &str,
        models: &[ModelRecord],
        fetched_at: &str,
    ) -> Result<()> {
        let transaction = self.connection.transaction()?;
        // Delete only what discovery no longer reports. A blanket delete also
        // threw away the user's `enabled` and `verified` flags for every model
        // that was still there, so each rescan emptied the chat picker.
        if models.is_empty() {
            transaction.execute(
                "DELETE FROM models WHERE runtime_id = ?1 AND origin = 'discovered'",
                params![runtime_id],
            )?;
        } else {
            let placeholders = (0..models.len())
                .map(|index| format!("?{}", index + 2))
                .collect::<Vec<_>>()
                .join(",");
            let mut args: Vec<rusqlite::types::Value> = vec![runtime_id.to_string().into()];
            for model in models {
                args.push(model.model_id.clone().into());
            }
            transaction.execute(
                &format!(
                    "DELETE FROM models WHERE runtime_id = ?1 AND origin = 'discovered'
                     AND model_id NOT IN ({placeholders})"
                ),
                rusqlite::params_from_iter(args),
            )?;
        }
        {
            let mut insert = transaction.prepare(
                "INSERT INTO models (
                    runtime_id, model_id, display_name, origin, protocol, endpoint,
                    credential_ref, verified, verified_at, is_default, enabled, updated_at
                 ) VALUES (?1, ?2, ?3, 'discovered', ?4, NULL, NULL, 0, NULL, 0, 0, ?5)
                 ON CONFLICT(runtime_id, model_id) DO UPDATE SET
                    display_name = excluded.display_name,
                    updated_at = excluded.updated_at",
            )?;
            for model in models {
                insert.execute(params![
                    runtime_id,
                    model.model_id,
                    model.display_name,
                    model.protocol,
                    fetched_at
                ])?;
            }
        }
        finish_sync(
            &transaction,
            runtime_id,
            InventoryKind::Models,
            models.len(),
            fetched_at,
        )?;
        transaction.commit()?;
        Ok(())
    }

    /// Replace one runtime's skills.
    pub fn replace_skills(
        &mut self,
        runtime_id: &str,
        skills: &[SkillRecord],
        fetched_at: &str,
    ) -> Result<()> {
        let transaction = self.connection.transaction()?;
        transaction.execute(
            "DELETE FROM skills WHERE runtime_id = ?1",
            params![runtime_id],
        )?;
        {
            let mut insert = transaction.prepare(
                "INSERT INTO skills (
                    runtime_id, name, root, path, description, state, reason, updated_at
                 ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            )?;
            for skill in skills {
                insert.execute(params![
                    runtime_id,
                    skill.name,
                    skill.root,
                    skill.path,
                    skill.description,
                    skill.state,
                    skill.reason,
                    skill.updated_at
                ])?;
            }
        }
        finish_sync(
            &transaction,
            runtime_id,
            InventoryKind::Skills,
            skills.len(),
            fetched_at,
        )?;
        transaction.commit()?;
        Ok(())
    }

    /// Replace one runtime's local tools.
    pub fn replace_tools(
        &mut self,
        runtime_id: &str,
        tools: &[ToolRecord],
        fetched_at: &str,
    ) -> Result<()> {
        let transaction = self.connection.transaction()?;
        transaction.execute(
            "DELETE FROM tools WHERE runtime_id = ?1",
            params![runtime_id],
        )?;
        {
            let mut insert = transaction.prepare(
                "INSERT INTO tools (runtime_id, name, kind, executable, permission, source)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            )?;
            for tool in tools {
                insert.execute(params![
                    runtime_id,
                    tool.name,
                    tool.kind,
                    tool.executable,
                    tool.permission,
                    tool.source
                ])?;
            }
        }
        finish_sync(
            &transaction,
            runtime_id,
            InventoryKind::Tools,
            tools.len(),
            fetched_at,
        )?;
        transaction.commit()?;
        Ok(())
    }

    /// Replace one runtime's MCP declarations.
    pub fn replace_mcp(
        &mut self,
        runtime_id: &str,
        servers: &[McpRecord],
        fetched_at: &str,
    ) -> Result<()> {
        let transaction = self.connection.transaction()?;
        transaction.execute("DELETE FROM mcp WHERE runtime_id = ?1", params![runtime_id])?;
        {
            let mut insert = transaction.prepare(
                "INSERT INTO mcp (
                    runtime_id, name, transport, command, args, url, env_keys,
                    header_keys, source, state, probed_at
                 ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)",
            )?;
            for server in servers {
                insert.execute(params![
                    runtime_id,
                    server.name,
                    server.transport,
                    server.command,
                    serde_json::to_string(&server.args)?,
                    server.url,
                    serde_json::to_string(&server.env_keys)?,
                    serde_json::to_string(&server.header_keys)?,
                    server.source,
                    server.state,
                    server.probed_at
                ])?;
            }
        }
        finish_sync(
            &transaction,
            runtime_id,
            InventoryKind::Mcp,
            servers.len(),
            fetched_at,
        )?;
        transaction.commit()?;
        Ok(())
    }

    /// Record a failed sync without touching the previous snapshot.
    pub fn mark_sync_failed(
        &mut self,
        runtime_id: &str,
        kind: InventoryKind,
        error: &str,
        attempted_at: &str,
    ) -> Result<()> {
        let transaction = self.connection.transaction()?;
        let updated = transaction.execute(
            "UPDATE syncs SET ok = 0, stale = 1, error = ?3 WHERE runtime_id = ?1 AND kind = ?2",
            params![runtime_id, kind.as_str(), error],
        )?;
        if updated == 0 {
            transaction.execute(
                "INSERT INTO syncs (runtime_id, kind, ok, item_count, fetched_at, stale, error)
                 VALUES (?1, ?2, 0, 0, ?3, 1, ?4)",
                params![runtime_id, kind.as_str(), attempted_at, error],
            )?;
        }
        transaction.commit()?;
        Ok(())
    }

    /// Every runtime row, newest scan first.
    pub fn runtimes(&self) -> Result<Vec<RuntimeRecord>> {
        let mut statement = self.connection.prepare(
            "SELECT r.id, r.runtime_id, r.display_name, r.version, r.install_path,
                    r.install_source, r.config_path, r.status, r.capabilities, r.health,
                    r.last_seen, r.first_seen,
                    (SELECT COUNT(*) FROM syncs s WHERE s.runtime_id = r.runtime_id AND s.stale = 1),
                    (SELECT COUNT(*) FROM syncs s WHERE s.runtime_id = r.runtime_id AND s.stale = 0)
             FROM runtimes r
             ORDER BY r.runtime_id, r.install_path",
        )?;
        let rows = statement.query_map([], |row| {
            let capabilities: String = row.get(8)?;
            let health: String = row.get(9)?;
            let stale: i64 = row.get(12)?;
            let fresh: i64 = row.get(13)?;
            Ok(RuntimeRecord {
                id: row.get(0)?,
                runtime_id: row.get(1)?,
                display_name: row.get(2)?,
                version: row.get(3)?,
                install_path: row.get(4)?,
                install_source: row.get(5)?,
                config_path: row.get(6)?,
                status: row.get(7)?,
                capabilities: serde_json::from_str(&capabilities).unwrap_or_default(),
                health: serde_json::from_str(&health).unwrap_or_default(),
                last_seen: row.get(10)?,
                first_seen: row.get(11)?,
                snapshot_state: if stale > 0 {
                    "stale".to_string()
                } else if fresh > 0 {
                    "fresh".to_string()
                } else {
                    "never".to_string()
                },
                adapter_registered: true,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// Models, ordered by runtime then default first.
    pub fn models(&self) -> Result<Vec<ModelRecord>> {
        let mut statement = self.connection.prepare(
            "SELECT runtime_id, model_id, display_name, origin, protocol, endpoint,
                    verified, verified_at, is_default, updated_at, credential_ref, enabled
             FROM models ORDER BY runtime_id, is_default DESC, model_id",
        )?;
        let rows = statement.query_map([], |row| {
            let credential: Option<String> = row.get(10)?;
            Ok(ModelRecord {
                runtime_id: row.get(0)?,
                model_id: row.get(1)?,
                display_name: row.get(2)?,
                origin: row.get(3)?,
                protocol: row.get(4)?,
                endpoint: row.get(5)?,
                verified: row.get::<_, i64>(6)? == 1,
                verified_at: row.get(7)?,
                is_default: row.get::<_, i64>(8)? == 1,
                updated_at: row.get(9)?,
                has_credential: credential.is_some(),
                enabled: row.get::<_, i64>(11)? == 1,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// Seed the chat picker on a catalog that has never had one.
    ///
    /// Discovery reports every model a runtime can reach, which is the wrong
    /// list for a picker. It is also wrong to offer nothing: a fresh install
    /// would open chat with a dead composer and no clue. So exactly one model
    /// per runtime is turned on, once; everything else waits for the user.
    /// Whether this runtime's first-run seeding already happened.
    pub fn picker_seeded(&self, runtime_id: &str) -> Result<bool> {
        // Per runtime, not once for the whole catalog: a runtime installed
        // later must also arrive with one model switched on, and a catalog
        // whose models were already wiped once would otherwise stay empty.
        let seeded: Option<String> = self
            .connection
            .query_row(
                "SELECT value FROM meta WHERE key = ?1",
                params![format!("picker_seeded:{runtime_id}")],
                |row| row.get(0),
            )
            .optional()?;
        Ok(seeded.is_some())
    }

    /// Switch one model on as this runtime's first pick and note the seeding.
    ///
    /// The caller decides *which* model (it may have proven one answers, see
    /// `Service::seed_picker`); the store only records the decision.
    pub fn seed_picker(
        &self,
        runtime_id: &str,
        model_id: &str,
        verified: bool,
        now: &str,
    ) -> Result<()> {
        let enabled: i64 = self.connection.query_row(
            "SELECT COUNT(*) FROM models WHERE runtime_id = ?1 AND enabled = 1",
            params![runtime_id],
            |row| row.get(0),
        )?;
        if enabled == 0 {
            let changed = self.connection.execute(
                "UPDATE models SET enabled = 1,
                    verified = CASE WHEN ?3 THEN 1 ELSE verified END,
                    verified_at = CASE WHEN ?3 THEN ?4 ELSE verified_at END
                 WHERE runtime_id = ?1 AND model_id = ?2",
                params![runtime_id, model_id, i64::from(verified), now],
            )?;
            if changed == 0 {
                return Err(crate::error::Error::NotFound(format!(
                    "model {runtime_id}/{model_id} is not in the catalog"
                )));
            }
        }
        self.connection.execute(
            "INSERT INTO meta (key, value) VALUES (?1, ?2)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            params![format!("picker_seeded:{runtime_id}"), now],
        )?;
        Ok(())
    }

    /// Seed the alphabetically first model. Kept for callers and tests that
    /// have no adapter to prove a model with.
    pub fn seed_picker_once(&self, runtime_id: &str) -> Result<bool> {
        if self.picker_seeded(runtime_id)? {
            return Ok(false);
        }
        let first: Option<String> = self
            .connection
            .query_row(
                "SELECT MIN(model_id) FROM models WHERE runtime_id = ?1",
                params![runtime_id],
                |row| row.get(0),
            )
            .optional()?;
        let now = crate::clock::now_rfc3339();
        if let Some(model_id) = first {
            self.seed_picker(runtime_id, &model_id, false, &now)?;
        } else {
            self.connection.execute(
                "INSERT INTO meta (key, value) VALUES (?1, ?2)
                 ON CONFLICT(key) DO UPDATE SET value = excluded.value",
                params![format!("picker_seeded:{runtime_id}"), now],
            )?;
        }
        Ok(true)
    }

    /// Turn one model on or off for the chat picker.
    pub fn set_model_enabled(&self, runtime_id: &str, model_id: &str, enabled: bool) -> Result<()> {
        let changed = self.connection.execute(
            "UPDATE models SET enabled = ?3 WHERE runtime_id = ?1 AND model_id = ?2",
            params![runtime_id, model_id, i64::from(enabled)],
        )?;
        if changed == 0 {
            return Err(crate::error::Error::NotFound(format!(
                "model {runtime_id}/{model_id} is not in the catalog"
            )));
        }
        Ok(())
    }

    /// Every agent binding, the built-in one first.
    ///
    /// The built-in row is stored like any other: "the system has one agent"
    /// has to be something the user can point at a runtime and a model, and a
    /// row derived from the default model gave them nothing to edit.
    pub fn agents(&self) -> Result<Vec<AgentRecord>> {
        let mut statement = self.connection.prepare(
            "SELECT id, name, runtime_id, model_id, is_default, updated_at
             FROM agents ORDER BY is_default DESC, name, id",
        )?;
        let rows = statement.query_map([], |row| {
            Ok(AgentRecord {
                id: row.get(0)?,
                name: row.get(1)?,
                runtime_id: row.get(2)?,
                model_id: row.get(3)?,
                is_default: row.get::<_, i64>(4)? != 0,
                updated_at: row.get(5)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// Create the built-in agent once, bound to whatever model is the default.
    ///
    /// Called before every snapshot, so an upgraded catalog gets the row too.
    /// A catalog with no models yet keeps the agent pointing at empty strings
    /// until a scan lands; [`Self::repair_default_agent`] adopts the first
    /// default model after that.
    pub fn ensure_default_agent(&self, now: &str) -> Result<()> {
        let binding: Option<(String, String)> = self
            .connection
            .query_row(
                // The default model is the intended binding; before anyone has
                // promoted one, the model discovery enabled is the closest thing
                // to "the one I run", and a catalog with only disabled rows
                // still gets a binding rather than a blank agent.
                "SELECT runtime_id, model_id FROM models
                 ORDER BY is_default DESC, enabled DESC, runtime_id, model_id LIMIT 1",
                [],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()?;
        let (runtime_id, model_id) = binding.unwrap_or_default();
        self.connection.execute(
            "INSERT INTO agents (id, name, runtime_id, model_id, is_default, updated_at)
             VALUES (?1, 'Default Agent', ?2, ?3, 1, ?4)
             ON CONFLICT(id) DO NOTHING",
            params![DEFAULT_AGENT_ID, runtime_id, model_id, now],
        )?;
        self.repair_default_agent()
    }

    /// Point the built-in agent at the default model while it has no binding.
    ///
    /// Only fills a blank row: once the user bound the agent themselves,
    /// changing the default model must not silently move their agent.
    fn repair_default_agent(&self) -> Result<()> {
        self.connection.execute(
            "UPDATE agents SET runtime_id = (
                SELECT runtime_id FROM models
                ORDER BY is_default DESC, enabled DESC, runtime_id, model_id LIMIT 1
             ), model_id = (
                SELECT model_id FROM models
                ORDER BY is_default DESC, enabled DESC, runtime_id, model_id LIMIT 1
             )
             WHERE id = ?1 AND (TRIM(runtime_id) = '' OR TRIM(model_id) = '')
               AND EXISTS (SELECT 1 FROM models)",
            params![DEFAULT_AGENT_ID],
        )?;
        Ok(())
    }

    /// Create or rename one agent binding. `id` empty means "new".
    ///
    /// The pair has to name a model the catalog knows: a binding to a runtime
    /// that is not installed would look saved and then fail at the first turn.
    pub fn save_agent(
        &self,
        id: Option<&str>,
        name: &str,
        runtime_id: &str,
        model_id: &str,
        now: &str,
    ) -> Result<AgentRecord> {
        let name = name.trim();
        if name.is_empty() {
            return Err(Error::Rejected("an agent needs a name".to_string()));
        }
        if runtime_id.trim().is_empty() || model_id.trim().is_empty() {
            return Err(Error::Rejected(
                "an agent needs both a runtime and a model".to_string(),
            ));
        }
        let known: Option<i64> = self
            .connection
            .query_row(
                "SELECT 1 FROM models WHERE runtime_id = ?1 AND model_id = ?2",
                params![runtime_id, model_id],
                |row| row.get(0),
            )
            .optional()?;
        if known.is_none() {
            return Err(Error::Rejected(format!(
                "model {runtime_id}/{model_id} is not in the catalog"
            )));
        }
        let id = match id.filter(|value| !value.trim().is_empty()) {
            Some(value) => value.to_string(),
            None => format!("agent-{}", crate::clock::unix_now()),
        };
        let is_default = id == DEFAULT_AGENT_ID;
        self.connection.execute(
            "INSERT INTO agents (id, name, runtime_id, model_id, is_default, updated_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)
             ON CONFLICT(id) DO UPDATE SET
                name = excluded.name,
                runtime_id = excluded.runtime_id,
                model_id = excluded.model_id,
                updated_at = excluded.updated_at",
            params![id, name, runtime_id, model_id, i64::from(is_default), now],
        )?;
        if is_default {
            // Binding the built-in agent to a model also makes that model the
            // default and turns it on: a default the picker hides is a default
            // the user cannot see they have.
            self.connection.execute(
                "UPDATE models SET is_default = CASE
                    WHEN runtime_id = ?1 AND model_id = ?2 THEN 1 ELSE 0 END,
                    enabled = CASE WHEN runtime_id = ?1 AND model_id = ?2 THEN 1 ELSE enabled END",
                params![runtime_id, model_id],
            )?;
        } else {
            // Naming a binding is configuration, so the model it names becomes
            // selectable: an agent the chat picker filters out is not a binding
            // the user can use.
            self.connection.execute(
                "UPDATE models SET enabled = 1 WHERE runtime_id = ?1 AND model_id = ?2",
                params![runtime_id, model_id],
            )?;
        }
        Ok(AgentRecord {
            id,
            name: name.to_string(),
            runtime_id: runtime_id.to_string(),
            model_id: model_id.to_string(),
            is_default,
            updated_at: now.to_string(),
        })
    }

    /// Remove one saved agent. The built-in one cannot be removed.
    pub fn delete_agent(&self, id: &str) -> Result<()> {
        if id == DEFAULT_AGENT_ID {
            return Err(Error::Rejected(
                "the default agent cannot be removed".to_string(),
            ));
        }
        let changed = self
            .connection
            .execute("DELETE FROM agents WHERE id = ?1", params![id])?;
        if changed == 0 {
            return Err(crate::error::Error::NotFound(format!(
                "agent {id} does not exist"
            )));
        }
        Ok(())
    }

    pub fn skills(&self) -> Result<Vec<SkillRecord>> {
        let mut statement = self.connection.prepare(
            "SELECT runtime_id, name, root, path, description, state, reason, updated_at
             FROM skills ORDER BY runtime_id, name",
        )?;
        let rows = statement.query_map([], |row| {
            Ok(SkillRecord {
                runtime_id: row.get(0)?,
                name: row.get(1)?,
                root: row.get(2)?,
                path: row.get(3)?,
                description: row.get(4)?,
                state: row.get(5)?,
                reason: row.get(6)?,
                updated_at: row.get(7)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    pub fn tools(&self) -> Result<Vec<ToolRecord>> {
        let mut statement = self.connection.prepare(
            "SELECT runtime_id, name, kind, executable, permission, source
             FROM tools ORDER BY runtime_id, name",
        )?;
        let rows = statement.query_map([], |row| {
            Ok(ToolRecord {
                runtime_id: row.get(0)?,
                name: row.get(1)?,
                kind: row.get(2)?,
                executable: row.get(3)?,
                permission: row.get(4)?,
                source: row.get(5)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    pub fn mcp(&self) -> Result<Vec<McpRecord>> {
        let mut statement = self.connection.prepare(
            "SELECT runtime_id, name, transport, command, args, url, env_keys,
                    header_keys, source, state, probed_at
             FROM mcp ORDER BY runtime_id, name",
        )?;
        let rows = statement.query_map([], |row| {
            let args: String = row.get(4)?;
            let env_keys: String = row.get(6)?;
            let header_keys: String = row.get(7)?;
            Ok(McpRecord {
                runtime_id: row.get(0)?,
                name: row.get(1)?,
                transport: row.get(2)?,
                command: row.get(3)?,
                args: serde_json::from_str(&args).unwrap_or_default(),
                url: row.get(5)?,
                env_keys: serde_json::from_str(&env_keys).unwrap_or_default(),
                header_keys: serde_json::from_str(&header_keys).unwrap_or_default(),
                source: row.get(8)?,
                state: row.get(9)?,
                probed_at: row.get(10)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    pub fn syncs(&self) -> Result<Vec<SyncOutcome>> {
        let mut statement = self.connection.prepare(
            "SELECT runtime_id, kind, ok, item_count, fetched_at, stale, error
             FROM syncs ORDER BY runtime_id, kind",
        )?;
        let rows = statement.query_map([], |row| {
            Ok(SyncOutcome {
                runtime_id: row.get(0)?,
                kind: row.get(1)?,
                ok: row.get::<_, i64>(2)? == 1,
                item_count: row.get::<_, i64>(3)? as usize,
                fetched_at: row.get(4)?,
                stale: row.get::<_, i64>(5)? == 1,
                error: row.get(6)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// Add or update a user model. A discovered row with the same id is
    /// upgraded instead of duplicated, and a duplicate custom row is rejected.
    pub fn upsert_custom_model(&mut self, model: CustomModel<'_>) -> Result<()> {
        if model.model_id.trim().is_empty() {
            return Err(Error::Rejected("model id must not be empty".into()));
        }
        if model.model_id.contains(char::is_whitespace) {
            return Err(Error::Rejected(format!(
                "model id {:?} must not contain whitespace",
                model.model_id
            )));
        }
        if let Some(endpoint) = model.endpoint {
            let lowered = endpoint.to_ascii_lowercase();
            if !(lowered.starts_with("https://") || lowered.starts_with("http://")) {
                return Err(Error::Rejected(format!(
                    "endpoint {endpoint:?} must be an http(s) URL"
                )));
            }
        }
        let existing: Option<String> = self
            .connection
            .query_row(
                "SELECT origin FROM models WHERE runtime_id = ?1 AND model_id = ?2",
                params![model.runtime_id, model.model_id],
                |row| row.get(0),
            )
            .optional()?;
        if existing.as_deref() == Some("custom") {
            return Err(Error::Rejected(format!(
                "custom model {:?} already exists for {}",
                model.model_id, model.runtime_id
            )));
        }
        self.connection.execute(
            "INSERT INTO models (
                runtime_id, model_id, display_name, origin, protocol, endpoint,
                credential_ref, verified, verified_at, is_default, enabled, updated_at
             ) VALUES (?1, ?2, ?3, 'custom', ?4, ?5, ?6, 0, NULL, 0, 1, ?7)
             ON CONFLICT(runtime_id, model_id) DO UPDATE SET
                display_name = excluded.display_name,
                origin = 'custom',
                protocol = excluded.protocol,
                endpoint = excluded.endpoint,
                credential_ref = excluded.credential_ref,
                verified = 0,
                verified_at = NULL,
                enabled = 1,
                updated_at = excluded.updated_at",
            params![
                model.runtime_id,
                model.model_id,
                model.display_name,
                model.protocol,
                model.endpoint,
                model.credential_ref,
                model.updated_at
            ],
        )?;
        Ok(())
    }

    /// Record a model verification attempt. Only a success unlocks the default.
    pub fn set_model_verified(
        &mut self,
        runtime_id: &str,
        model_id: &str,
        ok: bool,
        at: &str,
    ) -> Result<()> {
        let updated = self.connection.execute(
            "UPDATE models SET verified = ?3, verified_at = ?4 WHERE runtime_id = ?1 AND model_id = ?2",
            params![runtime_id, model_id, i64::from(ok), at],
        )?;
        if updated == 0 {
            return Err(Error::NotFound(format!(
                "model {model_id:?} for {runtime_id}"
            )));
        }
        if !ok {
            // A failed verification must not leave a stale default behind.
            self.connection.execute(
                "UPDATE models SET is_default = 0 WHERE runtime_id = ?1 AND model_id = ?2",
                params![runtime_id, model_id],
            )?;
        }
        Ok(())
    }

    /// Make one verified model the default. Exactly one row stays default.
    pub fn set_default_model(&mut self, runtime_id: &str, model_id: &str) -> Result<()> {
        let verified: Option<i64> = self
            .connection
            .query_row(
                "SELECT verified FROM models WHERE runtime_id = ?1 AND model_id = ?2",
                params![runtime_id, model_id],
                |row| row.get(0),
            )
            .optional()?;
        match verified {
            None => {
                return Err(Error::NotFound(format!(
                    "model {model_id:?} for {runtime_id}"
                )));
            }
            Some(0) => {
                return Err(Error::Rejected(format!(
                    "model {model_id:?} has not been verified against {runtime_id}"
                )));
            }
            Some(_) => {}
        }
        let transaction = self.connection.transaction()?;
        transaction.execute("UPDATE models SET is_default = 0 WHERE is_default = 1", [])?;
        transaction.execute(
            // The default model is the one guaranteed entry in the chat picker,
            // so promoting it turns it on as well.
            "UPDATE models SET is_default = 1, enabled = 1 WHERE runtime_id = ?1 AND model_id = ?2",
            params![runtime_id, model_id],
        )?;
        transaction.commit()?;
        Ok(())
    }

    /// Remove a custom model; discovered models are managed by their adapter.
    pub fn delete_custom_model(&mut self, runtime_id: &str, model_id: &str) -> Result<()> {
        let removed = self.connection.execute(
            "DELETE FROM models WHERE runtime_id = ?1 AND model_id = ?2 AND origin = 'custom'",
            params![runtime_id, model_id],
        )?;
        if removed == 0 {
            return Err(Error::NotFound(format!(
                "custom model {model_id:?} for {runtime_id}"
            )));
        }
        Ok(())
    }

    /// Store an opaque, already-encrypted credential blob.
    pub fn store_credential(&mut self, name: &str, ciphertext: &[u8], at: &str) -> Result<()> {
        self.connection.execute(
            "INSERT INTO secrets (name, ciphertext, created_at) VALUES (?1, ?2, ?3)
             ON CONFLICT(name) DO UPDATE SET ciphertext = excluded.ciphertext,
                created_at = excluded.created_at",
            params![name, ciphertext, at],
        )?;
        Ok(())
    }

    pub fn load_credential(&self, name: &str) -> Result<Option<Vec<u8>>> {
        Ok(self
            .connection
            .query_row(
                "SELECT ciphertext FROM secrets WHERE name = ?1",
                params![name],
                |row| row.get::<_, Vec<u8>>(0),
            )
            .optional()?)
    }

    pub fn has_credential(&self, name: &str) -> Result<bool> {
        let count: i64 = self.connection.query_row(
            "SELECT COUNT(*) FROM secrets WHERE name = ?1",
            params![name],
            |row| row.get(0),
        )?;
        Ok(count > 0)
    }

    pub fn delete_credential(&mut self, name: &str) -> Result<()> {
        self.connection
            .execute("DELETE FROM secrets WHERE name = ?1", params![name])?;
        Ok(())
    }

    /// Record the MCP probe result for one server.
    pub fn set_mcp_state(
        &mut self,
        runtime_id: &str,
        name: &str,
        state: &str,
        probed_at: &str,
    ) -> Result<()> {
        let updated = self.connection.execute(
            "UPDATE mcp SET state = ?3, probed_at = ?4 WHERE runtime_id = ?1 AND name = ?2",
            params![runtime_id, name, state, probed_at],
        )?;
        if updated == 0 {
            return Err(Error::NotFound(format!(
                "MCP server {name:?} for {runtime_id}"
            )));
        }
        Ok(())
    }

    /// Assemble the payload the UI renders.
    pub fn snapshot(
        &self,
        failures: Vec<AdapterFailure>,
        scanned_at: Option<String>,
    ) -> Result<CatalogSnapshot> {
        self.ensure_default_agent(&crate::clock::now_rfc3339())?;
        Ok(CatalogSnapshot {
            runtimes: self.runtimes()?,
            models: self.models()?,
            agents: self.agents()?,
            skills: self.skills()?,
            tools: self.tools()?,
            mcp: self.mcp()?,
            syncs: self.syncs()?,
            adapter_failures: failures,
            scanned_at,
        })
    }

    /// Path key helper used by callers that need the same normalisation.
    pub fn path_key(path: &Path) -> String {
        path_key(path)
    }
}

fn finish_sync(
    transaction: &rusqlite::Transaction<'_>,
    runtime_id: &str,
    kind: InventoryKind,
    count: usize,
    fetched_at: &str,
) -> Result<()> {
    transaction.execute(
        "INSERT INTO syncs (runtime_id, kind, ok, item_count, fetched_at, stale, error)
         VALUES (?1, ?2, 1, ?3, ?4, 0, NULL)
         ON CONFLICT(runtime_id, kind) DO UPDATE SET
            ok = 1, item_count = excluded.item_count,
            fetched_at = excluded.fetched_at, stale = 0, error = NULL",
        params![runtime_id, kind.as_str(), count as i64, fetched_at],
    )?;
    Ok(())
}

/// Fields of one user model, grouped so the write stays readable.
pub struct CustomModel<'a> {
    pub runtime_id: &'a str,
    pub model_id: &'a str,
    pub display_name: &'a str,
    pub protocol: Option<&'a str>,
    pub endpoint: Option<&'a str>,
    pub credential_ref: Option<&'a str>,
    pub updated_at: &'a str,
}

/// Convenience for tests: the capability payload a fresh row carries.
pub fn empty_capabilities() -> Capabilities {
    Capabilities::none()
}

/// Convenience for tests: a health record checked just now.
pub fn health_ok(detail: Option<&str>) -> RuntimeHealth {
    RuntimeHealth {
        checked_at: crate::clock::now_rfc3339(),
        latency_ms: 0,
        detail: detail.map(str::to_string),
    }
}
