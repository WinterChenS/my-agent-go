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
    AdapterFailure, Capabilities, CatalogSnapshot, InventoryKind, McpRecord, ModelRecord,
    RuntimeHealth, RuntimeRecord, RuntimeRegistration, RuntimeStatus, SkillRecord, SyncOutcome,
    ToolRecord,
};

const SCHEMA_VERSION: i64 = 1;

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
                updated_at TEXT NOT NULL,
                PRIMARY KEY (runtime_id, model_id)
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
        self.connection.execute(
            "INSERT INTO meta (key, value) VALUES ('schema_version', ?1)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            params![SCHEMA_VERSION.to_string()],
        )?;
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
        transaction.execute(
            "DELETE FROM models WHERE runtime_id = ?1 AND origin = 'discovered'",
            params![runtime_id],
        )?;
        {
            let mut insert = transaction.prepare(
                "INSERT INTO models (
                    runtime_id, model_id, display_name, origin, protocol, endpoint,
                    credential_ref, verified, verified_at, is_default, updated_at
                 ) VALUES (?1, ?2, ?3, 'discovered', ?4, NULL, NULL, 0, NULL, 0, ?5)
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
                    verified, verified_at, is_default, updated_at, credential_ref
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
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
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
                credential_ref, verified, verified_at, is_default, updated_at
             ) VALUES (?1, ?2, ?3, 'custom', ?4, ?5, ?6, 0, NULL, 0, ?7)
             ON CONFLICT(runtime_id, model_id) DO UPDATE SET
                display_name = excluded.display_name,
                origin = 'custom',
                protocol = excluded.protocol,
                endpoint = excluded.endpoint,
                credential_ref = excluded.credential_ref,
                verified = 0,
                verified_at = NULL,
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
            "UPDATE models SET is_default = 1 WHERE runtime_id = ?1 AND model_id = ?2",
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
        Ok(CatalogSnapshot {
            runtimes: self.runtimes()?,
            models: self.models()?,
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
