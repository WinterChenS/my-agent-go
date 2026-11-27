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

use std::path::{Path, PathBuf};

use rusqlite::{Connection, OptionalExtension, params, params_from_iter};

use crate::connector::{ConnectorCall, ConnectorCallStat, ConnectorRow};
use crate::error::{Error, Result};
use crate::ids::{instance_key, path_key};
use crate::model::{
    AdapterFailure, AgentDraft, AgentRecord, Capabilities, CatalogSnapshot, InstallSource,
    InventoryKind, McpRecord, ModelRecord, RuntimeHealth, RuntimeRecord, RuntimeRegistration,
    RuntimeStatus, SkillRecord, SyncOutcome, ToolRecord,
};

const SCHEMA_VERSION: i64 = 1;

/// Id of the agent derived from the catalog's default model. It has no row: the
/// service synthesises it, so it can never point at a model that is gone.
pub const DEFAULT_AGENT_ID: &str = "default";

/// Handle to `<data dir>/catalog.sqlite`.
pub struct Store {
    connection: Connection,
    /// Directory the database lives in — also where the Unix credential key
    /// sits. Empty for an in-memory store, which has no credentials to keep.
    dir: PathBuf,
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
        let store = Self {
            connection,
            dir: path
                .parent()
                .map(Path::to_path_buf)
                .unwrap_or_else(|| PathBuf::from(".")),
        };
        store.migrate()?;
        Ok(store)
    }

    /// In-memory database for contract tests.
    pub fn open_in_memory() -> Result<Self> {
        let connection = Connection::open_in_memory()?;
        let store = Self {
            connection,
            dir: std::env::temp_dir().join("anvil-catalog-memory"),
        };
        store.migrate()?;
        Ok(store)
    }

    /// Where the credential key for this store lives.
    pub fn dir(&self) -> &Path {
        &self.dir
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
                tools TEXT NOT NULL DEFAULT '[]',
                PRIMARY KEY (runtime_id, name)
            );
            CREATE TABLE IF NOT EXISTS secrets (
                name TEXT PRIMARY KEY,
                ciphertext BLOB NOT NULL,
                created_at TEXT NOT NULL
            );
            -- Connectors are services with their own credentials (mail, Notion,
            -- DingTalk docs), not MCP servers. They live beside the MCP table
            -- rather than inside it: nothing about them is a JSON-RPC endpoint.
            CREATE TABLE IF NOT EXISTS connectors (
                id TEXT PRIMARY KEY,
                kind TEXT NOT NULL,
                name TEXT NOT NULL,
                config TEXT NOT NULL,
                enabled INTEGER NOT NULL DEFAULT 1,
                state TEXT NOT NULL DEFAULT 'declared',
                probed_at TEXT,
                last_error TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            );
            -- One row per tool call the agent made through a connector. The
            -- page's call counts are these rows, never a guess from a log line.
            CREATE TABLE IF NOT EXISTS connector_calls (
                seq INTEGER PRIMARY KEY AUTOINCREMENT,
                connector_id TEXT NOT NULL,
                tool TEXT NOT NULL,
                ok INTEGER NOT NULL,
                detail TEXT,
                at TEXT NOT NULL,
                duration_ms INTEGER NOT NULL DEFAULT 0
            );
            CREATE INDEX IF NOT EXISTS connector_calls_at
                ON connector_calls (connector_id, at);
            "#,
        )?;
        // `CREATE TABLE IF NOT EXISTS` never adds a column to a database that
        // already exists, and the catalog has lived on user machines since G1.
        self.add_column_if_missing("models", "enabled", "INTEGER NOT NULL DEFAULT 0")?;
        // A probe's tool list is what makes a connector page say "12 actions"
        // instead of guessing; databases from before that column just have none.
        self.add_column_if_missing("mcp", "tools", "TEXT NOT NULL DEFAULT '[]'")?;
        // The default agent used to be derived from the default model and never
        // stored, which left the user no row to bind a runtime to. It is a real
        // row now, so an existing catalog needs the column.
        self.add_column_if_missing("agents", "is_default", "INTEGER NOT NULL DEFAULT 0")?;
        // The page grew from "a name on a model" to the agent's persona. Every
        // column is added here so a catalog written by an older build opens with
        // the defaults the page shows, not with a failed read.
        for (column, ddl) in [
            ("slug", "TEXT NOT NULL DEFAULT ''"),
            ("description", "TEXT NOT NULL DEFAULT ''"),
            ("thinking", "TEXT NOT NULL DEFAULT 'medium'"),
            ("context_window", "TEXT NOT NULL DEFAULT '400K'"),
            ("memory", "INTEGER NOT NULL DEFAULT 0"),
            ("auto_compact", "INTEGER NOT NULL DEFAULT 1"),
            ("system_prompt", "TEXT NOT NULL DEFAULT ''"),
            ("tools", "TEXT NOT NULL DEFAULT ''"),
            ("skills", "TEXT NOT NULL DEFAULT ''"),
            ("permission", "TEXT NOT NULL DEFAULT 'medium'"),
            ("writable_dirs", "TEXT NOT NULL DEFAULT ''"),
            ("command_allowlist", "TEXT NOT NULL DEFAULT ''"),
            ("network", "INTEGER NOT NULL DEFAULT 1"),
            ("confirm_dangerous", "INTEGER NOT NULL DEFAULT 1"),
            ("group_kind", "TEXT NOT NULL DEFAULT 'main'"),
            ("delegated_from", "TEXT NOT NULL DEFAULT ''"),
            ("delegation_policy", "TEXT NOT NULL DEFAULT 'auto'"),
            ("concurrency", "INTEGER NOT NULL DEFAULT 4"),
            ("enabled", "INTEGER NOT NULL DEFAULT 1"),
        ] {
            self.add_column_if_missing("agents", column, ddl)?;
        }
        // A row saved before the slug existed has no identifier the user can
        // point at; the row id is the one stable value it already has.
        self.connection
            .execute("UPDATE agents SET slug = id WHERE TRIM(slug) = ''", [])?;
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
            // A bundled runtime has exactly one install (ADR 0009). Rows an
            // older build discovered for the same runtime — a managed copy under
            // the data directory, a PATH shim — are not a second install to keep:
            // the page rendered two "Pi Agent" rows after an upgrade, one of them
            // "not installed" and still advising `pi login`.
            let mut bundled: Vec<&str> = registrations
                .iter()
                .filter(|registration| matches!(registration.source, InstallSource::Bundled))
                .map(|registration| registration.runtime_id.as_str())
                .collect();
            bundled.sort_unstable();
            bundled.dedup();
            if !bundled.is_empty() {
                let mut drop_foreign = transaction.prepare(
                    "DELETE FROM runtimes WHERE runtime_id = ?1 AND install_source <> ?2",
                )?;
                for runtime_id in bundled {
                    drop_foreign
                        .execute(params![runtime_id, InstallSource::Bundled.as_str()])?;
                }
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
        {
            // A "not installed" placeholder must not survive the runtime's first
            // real install. Rows are keyed by executable path, so the placeholder
            // and the install are two different rows: the stale one sorted first
            // and the UI kept reporting "未安装" right after a successful install.
            //
            // Only runtimes that actually reported an install are touched — a
            // still-missing runtime keeps its placeholder, which is the only row
            // that tells the page it exists at all.
            let missing_marker = RuntimeStatus::NotInstalled.as_str();
            let mut installed: Vec<&str> = registrations
                .iter()
                .filter(|registration| registration.status.as_str() != missing_marker)
                .map(|registration| registration.runtime_id.as_str())
                .collect();
            installed.sort_unstable();
            installed.dedup();
            let mut drop_placeholder = transaction.prepare(
                "DELETE FROM runtimes
                  WHERE runtime_id = ?1 AND status = ?2
                    AND (install_path IS NULL OR install_path = '')",
            )?;
            for runtime_id in installed {
                drop_placeholder.execute(params![runtime_id, missing_marker])?;
            }
        }
        transaction.commit()?;
        let _ = scanned_at;
        Ok(())
    }

    /// Drop every row belonging to a runtime this build has no adapter for.
    ///
    /// A parked adapter (see `adapters::PARKED`) must not leave rows behind.
    /// They are not harmless: the runtime list would offer a runtime nothing can
    /// start, `pickModel` could hand the default agent a model that is no longer
    /// reachable, and the settings page would show a "一键安装" for a runtime this
    /// build cannot use even after installing it.
    ///
    /// Everything removed here is discovery output, and it is rebuilt the next
    /// time the adapter is registered again. Credentials live in their own table
    /// and are deliberately left alone.
    ///
    /// An empty `keep` is a registry that failed to build, not a statement about
    /// the build; nothing is touched.
    pub fn forget_unregistered(&mut self, keep: &[&str]) -> Result<usize> {
        if keep.is_empty() {
            return Ok(0);
        }
        let names: Vec<rusqlite::types::Value> = keep
            .iter()
            .map(|id| rusqlite::types::Value::Text((*id).to_string()))
            .collect();
        let placeholders = (0..names.len())
            .map(|index| format!("?{}", index + 1))
            .collect::<Vec<_>>()
            .join(",");
        let transaction = self.connection.transaction()?;
        let mut removed = 0;
        for table in ["syncs", "models", "skills", "tools", "mcp"] {
            removed += transaction.execute(
                &format!("DELETE FROM {table} WHERE runtime_id NOT IN ({placeholders})"),
                params_from_iter(names.clone()),
            )?;
        }
        removed += transaction.execute(
            &format!("DELETE FROM runtimes WHERE runtime_id NOT IN ({placeholders})"),
            params_from_iter(names.clone()),
        )?;
        // A saved agent bound to a runtime that no longer exists would look
        // saved and then fail on its first turn. Blank the binding and keep the
        // persona; the next snapshot repairs the default one from what is left.
        transaction.execute(
            &format!(
                "UPDATE agents SET runtime_id = '', model_id = ''
                  WHERE runtime_id NOT IN ({placeholders})"
            ),
            params_from_iter(names),
        )?;
        transaction.commit()?;
        Ok(removed)
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
                 ) VALUES (?1, ?2, ?3, 'discovered', ?4, ?5, NULL, 0, NULL, 0, 0, ?6)
                 ON CONFLICT(runtime_id, model_id) DO UPDATE SET
                    display_name = excluded.display_name,
                    -- The endpoint is what a model check posts to; dropping it
                    -- left every discovered row unverifiable.
                    endpoint = excluded.endpoint,
                    updated_at = excluded.updated_at",
            )?;
            for model in models {
                insert.execute(params![
                    runtime_id,
                    model.model_id,
                    model.display_name,
                    model.protocol,
                    model.endpoint,
                    fetched_at
                ])?;
            }
        }
        // Adopt the runtime's own default model, but only while this runtime
        // has no default yet. A discovered model arrives disabled — the reader
        // must choose — yet a machine that has pi configured landed on a chat
        // page saying "未启用模型（设置 → 模型 里启用）" with a full, perfectly
        // usable model list right below it. Once a default exists (adopted here
        // or chosen by the reader) this never runs again, so it cannot
        // re-enable a model somebody deliberately turned off.
        for model in models.iter().filter(|model| model.is_default) {
            transaction.execute(
                "UPDATE models SET is_default = 1, enabled = 1
                  WHERE runtime_id = ?1 AND model_id = ?2
                    AND NOT EXISTS (
                        SELECT 1 FROM models WHERE runtime_id = ?1 AND is_default = 1
                    )",
                params![runtime_id, model.model_id],
            )?;
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
        // A probe is the app's own observation, not part of the runtime's config
        // file. Syncing the inventory rewrites the rows from that file, so without
        // carrying the previous probe forward every sync would reset a server to
        // `declared` and silently erase "8 tools, connected 3 minutes ago".
        let previous: std::collections::HashMap<String, (String, Option<String>, String)> = {
            let mut statement = transaction
                .prepare("SELECT name, state, probed_at, tools FROM mcp WHERE runtime_id = ?1")?;
            let rows = statement.query_map(params![runtime_id], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    (
                        row.get::<_, String>(1)?,
                        row.get::<_, Option<String>>(2)?,
                        row.get::<_, String>(3)?,
                    ),
                ))
            })?;
            rows.collect::<std::result::Result<_, _>>()?
        };
        transaction.execute("DELETE FROM mcp WHERE runtime_id = ?1", params![runtime_id])?;
        {
            let mut insert = transaction.prepare(
                "INSERT INTO mcp (
                    runtime_id, name, transport, command, args, url, env_keys,
                    header_keys, source, state, probed_at, tools
                 ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)",
            )?;
            for server in servers {
                let carry = previous
                    .get(&server.name)
                    .filter(|(state, _, _)| server.state == "declared" && state != "declared");
                let state = carry
                    .map(|(state, _, _)| state.clone())
                    .unwrap_or_else(|| server.state.clone());
                let probed_at = carry
                    .and_then(|(_, at, _)| at.clone())
                    .or_else(|| server.probed_at.clone());
                let tools = carry.map(|(_, _, tools)| tools.clone()).unwrap_or_else(|| {
                    serde_json::to_string(&server.tools).unwrap_or_else(|_| "[]".into())
                });
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
                    state,
                    probed_at,
                    tools
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
    /// Whether the user finished first-run setup.
    ///
    /// A `meta` row rather than WebView state: the onboarding wizard must stay
    /// closed across a localStorage reset, and it cannot be inferred from
    /// "some model is enabled" because discovery switches one on by itself.
    pub fn setup_complete(&self) -> Result<bool> {
        let value: Option<String> = self
            .connection
            .query_row(
                "SELECT value FROM meta WHERE key = 'setup_done'",
                [],
                |row| row.get(0),
            )
            .optional()?;
        Ok(value.is_some())
    }

    /// Record that the user finished first-run setup.
    pub fn mark_setup_complete(&self, now: &str) -> Result<()> {
        self.connection.execute(
            "INSERT INTO meta (key, value) VALUES ('setup_done', ?1)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value",
            params![now],
        )?;
        Ok(())
    }

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
            "SELECT id, name, slug, description, runtime_id, model_id, is_default, thinking,
                    context_window, memory, auto_compact, system_prompt, tools, skills,
                    permission, writable_dirs, command_allowlist, network, confirm_dangerous,
                    group_kind, delegated_from, delegation_policy, concurrency, enabled,
                    updated_at
             FROM agents ORDER BY is_default DESC, group_kind, name, id",
        )?;
        let rows = statement.query_map([], agent_from_row)?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// One agent by id, for the host when it opens a session.
    pub fn agent(&self, id: &str) -> Result<Option<AgentRecord>> {
        let mut statement = self.connection.prepare(
            "SELECT id, name, slug, description, runtime_id, model_id, is_default, thinking,
                    context_window, memory, auto_compact, system_prompt, tools, skills,
                    permission, writable_dirs, command_allowlist, network, confirm_dangerous,
                    group_kind, delegated_from, delegation_policy, concurrency, enabled,
                    updated_at
             FROM agents WHERE id = ?1",
        )?;
        let mut rows = statement.query_map(params![id], agent_from_row)?;
        match rows.next() {
            Some(row) => Ok(Some(row?)),
            None => Ok(None),
        }
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
            "INSERT INTO agents (id, name, slug, runtime_id, model_id, is_default, updated_at)
             VALUES (?1, 'Default Agent', ?1, ?2, ?3, 1, ?4)
             ON CONFLICT(id) DO NOTHING",
            params![DEFAULT_AGENT_ID, runtime_id, model_id, now],
        )?;
        self.repair_default_agent()?;
        self.grant_delegation_to_untouched_agents()
    }

    /// Add the delegation tool to agents saved before it was offered.
    ///
    /// `--tools` is a whole allowlist, so a list stored without `subagent` keeps
    /// hiding delegation: the runtime could spawn a child, the model could not
    /// ask for one. Only a list that still equals the old built-in set is
    /// extended — an edited list is a user decision, including "no sub-agents".
    fn grant_delegation_to_untouched_agents(&self) -> Result<()> {
        let mut statement = self
            .connection
            .prepare("SELECT id, tools FROM agents WHERE TRIM(tools) <> ''")?;
        let rows = statement
            .query_map([], |row| {
                Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
            })?
            .collect::<std::result::Result<Vec<_>, _>>()?;
        drop(statement);

        let legacy: Vec<String> = crate::model::BUILTIN_TOOLS
            .iter()
            .map(|name| (*name).to_string())
            .collect();
        for (id, raw) in rows {
            let current = decode_names(&raw, &[]);
            if current != legacy {
                continue;
            }
            let mut next = current;
            next.extend(
                crate::model::DELEGATION_TOOLS
                    .iter()
                    .map(|name| (*name).to_string()),
            );
            self.connection.execute(
                "UPDATE agents SET tools = ?2 WHERE id = ?1",
                params![id, encode_names(&next)],
            )?;
        }
        Ok(())
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

    /// Create or update one agent from the page's draft. `draft.id` empty means
    /// "new".
    ///
    /// The pair has to name a model the catalog knows: a binding to a runtime
    /// that is not installed would look saved and then fail at the first turn.
    /// Everything else is the agent's persona and is stored as sent, so saving
    /// a page and reopening it shows exactly what the user typed.
    /// A fresh `agent-…` id that no row holds.
    ///
    /// Second-resolution ids collided: two agents created inside the same second
    /// shared an id, and the upsert silently replaced the first one — the user
    /// saw "saved" and ended up with one agent. Milliseconds plus a process-wide
    /// counter, then a real existence check, because a counter resets on restart.
    fn next_agent_id(&self) -> Result<String> {
        static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let millis = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|value| value.as_millis())
            .unwrap_or_default();
        loop {
            let seq = SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            let candidate = format!("agent-{millis}-{seq}");
            let taken: Option<i64> = self
                .connection
                .query_row(
                    "SELECT 1 FROM agents WHERE id = ?1",
                    params![candidate],
                    |row| row.get(0),
                )
                .optional()?;
            if taken.is_none() {
                return Ok(candidate);
            }
        }
    }

    pub fn save_agent(&self, draft: &AgentDraft, now: &str) -> Result<AgentRecord> {
        let name = draft.name.trim();
        if name.is_empty() {
            return Err(Error::Rejected("an agent needs a name".to_string()));
        }
        if draft.runtime_id.trim().is_empty() || draft.model_id.trim().is_empty() {
            return Err(Error::Rejected(
                "an agent needs both a runtime and a model".to_string(),
            ));
        }
        let known: Option<i64> = self
            .connection
            .query_row(
                "SELECT 1 FROM models WHERE runtime_id = ?1 AND model_id = ?2",
                params![draft.runtime_id, draft.model_id],
                |row| row.get(0),
            )
            .optional()?;
        if known.is_none() {
            return Err(Error::Rejected(format!(
                "model {}/{} is not in the catalog",
                draft.runtime_id, draft.model_id
            )));
        }
        let id = match draft.id.as_deref().filter(|value| !value.trim().is_empty()) {
            Some(value) => value.to_string(),
            None => self.next_agent_id()?,
        };
        let slug = match draft.slug.trim() {
            "" => id.clone(),
            value if value.chars().any(char::is_whitespace) => {
                return Err(Error::Rejected(
                    "an agent's identifier cannot contain spaces".to_string(),
                ));
            }
            value => value.to_string(),
        };
        let taken: Option<String> = self
            .connection
            .query_row(
                "SELECT id FROM agents WHERE slug = ?1 AND id <> ?2",
                params![slug, id],
                |row| row.get(0),
            )
            .optional()?;
        if let Some(other) = taken {
            return Err(Error::Rejected(format!(
                "the identifier {slug} is already used by {other}"
            )));
        }
        let is_default = id == DEFAULT_AGENT_ID;
        self.connection.execute(
            "INSERT INTO agents (
                id, name, slug, description, runtime_id, model_id, is_default, thinking,
                context_window, memory, auto_compact, system_prompt, tools, skills,
                permission, writable_dirs, command_allowlist, network, confirm_dangerous,
                group_kind, delegated_from, delegation_policy, concurrency, enabled, updated_at
             ) VALUES (
                ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18,
                ?19, ?20, ?21, ?22, ?23, ?24, ?25
             )
             ON CONFLICT(id) DO UPDATE SET
                name = excluded.name,
                slug = excluded.slug,
                description = excluded.description,
                runtime_id = excluded.runtime_id,
                model_id = excluded.model_id,
                thinking = excluded.thinking,
                context_window = excluded.context_window,
                memory = excluded.memory,
                auto_compact = excluded.auto_compact,
                system_prompt = excluded.system_prompt,
                tools = excluded.tools,
                skills = excluded.skills,
                permission = excluded.permission,
                writable_dirs = excluded.writable_dirs,
                command_allowlist = excluded.command_allowlist,
                network = excluded.network,
                confirm_dangerous = excluded.confirm_dangerous,
                group_kind = excluded.group_kind,
                delegated_from = excluded.delegated_from,
                delegation_policy = excluded.delegation_policy,
                concurrency = excluded.concurrency,
                enabled = excluded.enabled,
                updated_at = excluded.updated_at",
            params![
                id,
                name,
                slug,
                draft.description.trim(),
                draft.runtime_id,
                draft.model_id,
                i64::from(is_default),
                draft.thinking.trim(),
                draft.context_window.trim(),
                i64::from(draft.memory),
                i64::from(draft.auto_compact),
                draft.system_prompt,
                encode_names(&draft.tools),
                encode_names(&draft.skills),
                draft.permission.trim(),
                draft.writable_dirs.trim(),
                draft.command_allowlist.trim(),
                i64::from(draft.network),
                i64::from(draft.confirm_dangerous),
                draft.group_kind.trim(),
                draft.delegated_from.trim(),
                draft.delegation_policy.trim(),
                i64::from(draft.concurrency),
                i64::from(draft.enabled),
                now,
            ],
        )?;
        if is_default {
            // Binding the built-in agent to a model also makes that model the
            // default and turns it on: a default the picker hides is a default
            // the user cannot see they have.
            self.connection.execute(
                "UPDATE models SET is_default = CASE
                    WHEN runtime_id = ?1 AND model_id = ?2 THEN 1 ELSE 0 END,
                    enabled = CASE WHEN runtime_id = ?1 AND model_id = ?2 THEN 1 ELSE enabled END",
                params![draft.runtime_id, draft.model_id],
            )?;
        } else {
            // Naming a binding is configuration, so the model it names becomes
            // selectable: an agent the chat picker filters out is not a binding
            // the user can use.
            self.connection.execute(
                "UPDATE models SET enabled = 1 WHERE runtime_id = ?1 AND model_id = ?2",
                params![draft.runtime_id, draft.model_id],
            )?;
        }
        self.agent(&id)?
            .ok_or_else(|| Error::NotFound(format!("agent {id} did not save")))
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
                    header_keys, source, state, probed_at, tools
             FROM mcp ORDER BY runtime_id, name",
        )?;
        let rows = statement.query_map([], |row| {
            let args: String = row.get(4)?;
            let env_keys: String = row.get(6)?;
            let header_keys: String = row.get(7)?;
            let tools: String = row.get(11)?;
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
                tools: serde_json::from_str(&tools).unwrap_or_default(),
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

    /// Whether *any* model of `runtime_id` carries a credential this app stored.
    ///
    /// Discovery only reads the credential files a runtime writes itself, so a
    /// key the reader typed into this app is invisible to it — and the runtime
    /// read as "not signed in" while its own picker held a working model.
    pub fn has_runtime_credential(&self, runtime_id: &str) -> Result<bool> {
        let count: i64 = self.connection.query_row(
            "SELECT COUNT(*) FROM models WHERE runtime_id = ?1 AND credential_ref IS NOT NULL",
            params![runtime_id],
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

    /// Record the tool list a successful probe returned.
    ///
    /// Kept apart from [`Self::set_mcp_state`] so a probe that fails does not
    /// leave yesterday's tools looking current: the caller writes both.
    pub fn set_mcp_tools(&mut self, runtime_id: &str, name: &str, tools: &[String]) -> Result<()> {
        self.connection.execute(
            "UPDATE mcp SET tools = ?3 WHERE runtime_id = ?1 AND name = ?2",
            params![
                runtime_id,
                name,
                serde_json::to_string(tools).unwrap_or_else(|_| "[]".to_string())
            ],
        )?;
        Ok(())
    }

    // ---- connectors -------------------------------------------------------

    /// Every configured connector, oldest first.
    pub fn connectors(&self) -> Result<Vec<ConnectorRow>> {
        let mut statement = self.connection.prepare(
            "SELECT id, kind, name, config, enabled, state, probed_at, last_error,
                    created_at, updated_at
             FROM connectors ORDER BY created_at, id",
        )?;
        let rows = statement.query_map([], |row| {
            Ok(ConnectorRow {
                id: row.get(0)?,
                kind: row.get(1)?,
                name: row.get(2)?,
                config: decode_config(&row.get::<_, String>(3)?),
                enabled: row.get::<_, i64>(4)? != 0,
                state: row.get(5)?,
                probed_at: row.get(6)?,
                last_error: row.get(7)?,
                created_at: row.get(8)?,
                updated_at: row.get(9)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// One connector by id.
    pub fn connector(&self, id: &str) -> Result<Option<ConnectorRow>> {
        Ok(self.connectors()?.into_iter().find(|row| row.id == id))
    }

    /// Insert or update a connector. `probed_at`/`last_error` are written by the
    /// probe path, so an edit must not clear them: they stay as they were.
    pub fn save_connector(&self, row: &ConnectorRow) -> Result<()> {
        let config = serde_json::to_string(&row.config)?;
        self.connection.execute(
            "INSERT INTO connectors (
                id, kind, name, config, enabled, state, probed_at, last_error,
                created_at, updated_at
             ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)
             ON CONFLICT(id) DO UPDATE SET
                kind = excluded.kind, name = excluded.name, config = excluded.config,
                enabled = excluded.enabled, state = excluded.state,
                updated_at = excluded.updated_at",
            params![
                row.id,
                row.kind,
                row.name,
                config,
                i64::from(row.enabled),
                row.state,
                row.probed_at,
                row.last_error,
                row.created_at,
                row.updated_at
            ],
        )?;
        Ok(())
    }

    pub fn delete_connector(&self, id: &str) -> Result<()> {
        self.connection
            .execute("DELETE FROM connectors WHERE id = ?1", params![id])?;
        self.connection.execute(
            "DELETE FROM connector_calls WHERE connector_id = ?1",
            params![id],
        )?;
        Ok(())
    }

    pub fn set_connector_enabled(&self, id: &str, enabled: bool, at: &str) -> Result<()> {
        self.connection.execute(
            "UPDATE connectors SET enabled = ?2, updated_at = ?3 WHERE id = ?1",
            params![id, i64::from(enabled), at],
        )?;
        Ok(())
    }

    /// Record what a probe observed. A successful probe clears the old error;
    /// a failed one keeps the message so the card can explain itself.
    pub fn set_connector_state(
        &self,
        id: &str,
        state: &str,
        probed_at: Option<&str>,
        error: Option<&str>,
    ) -> Result<()> {
        self.connection.execute(
            "UPDATE connectors SET state = ?2, probed_at = ?3, last_error = ?4 WHERE id = ?1",
            params![id, state, probed_at, error],
        )?;
        Ok(())
    }

    /// Append a call row; returns its sequence for logs.
    pub fn record_connector_call(
        &self,
        connector_id: &str,
        tool: &str,
        ok: bool,
        detail: Option<&str>,
        at: &str,
        duration_ms: i64,
    ) -> Result<i64> {
        self.connection.execute(
            "INSERT INTO connector_calls (connector_id, tool, ok, detail, at, duration_ms)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            params![connector_id, tool, i64::from(ok), detail, at, duration_ms],
        )?;
        Ok(self.connection.last_insert_rowid())
    }

    /// Calls per connector tool since `since` (RFC 3339, inclusive).
    pub fn connector_call_stats(&self, since: &str) -> Result<Vec<ConnectorCallStat>> {
        let mut statement = self.connection.prepare(
            "SELECT connector_id, tool, COUNT(*), SUM(CASE WHEN ok = 0 THEN 1 ELSE 0 END),
                    MAX(at)
             FROM connector_calls WHERE at >= ?1
             GROUP BY connector_id, tool ORDER BY COUNT(*) DESC",
        )?;
        let rows = statement.query_map(params![since], |row| {
            Ok(ConnectorCallStat {
                connector_id: row.get(0)?,
                tool: row.get(1)?,
                calls: row.get::<_, i64>(2)? as u64,
                failures: row.get::<_, i64>(3)? as u64,
                last_at: row.get(4)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
    }

    /// The latest calls across every connector, newest first.
    pub fn recent_connector_calls(&self, limit: usize) -> Result<Vec<ConnectorCall>> {
        let mut statement = self.connection.prepare(
            "SELECT connector_id, tool, ok, detail, at, duration_ms
             FROM connector_calls ORDER BY seq DESC LIMIT ?1",
        )?;
        let rows = statement.query_map(params![limit as i64], |row| {
            Ok(ConnectorCall {
                connector_id: row.get(0)?,
                tool: row.get(1)?,
                ok: row.get::<_, i64>(2)? != 0,
                detail: row.get(3)?,
                at: row.get(4)?,
                duration_ms: row.get(5)?,
            })
        })?;
        Ok(rows.collect::<std::result::Result<Vec<_>, _>>()?)
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
            // Both are process/UI facts the host fills in, not store facts.
            scanning: false,
            setup_done: self.setup_complete()?,
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

/// Decode a name list column.
///
/// An empty column is "written by a build that did not store this field", so it
/// falls back to `fallback`; a stored `[]` is the user's explicit "none". Those
/// two must stay distinguishable, or turning every tool off would silently
/// restore the default set on the next read.
fn decode_names(raw: &str, fallback: &[&str]) -> Vec<String> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return fallback.iter().map(|name| (*name).to_string()).collect();
    }
    serde_json::from_str::<Vec<String>>(trimmed).unwrap_or_default()
}

/// A connector config column is a JSON object of strings. A row written by an
/// older build (or hand-edited) reads as empty instead of failing the page.
fn decode_config(raw: &str) -> serde_json::Map<String, serde_json::Value> {
    serde_json::from_str::<serde_json::Map<String, serde_json::Value>>(raw).unwrap_or_default()
}

fn encode_names(names: &[String]) -> String {
    serde_json::to_string(names).unwrap_or_else(|_| "[]".to_string())
}

/// Read one `agents` row; the column order is the one both queries use.
fn agent_from_row(row: &rusqlite::Row<'_>) -> rusqlite::Result<AgentRecord> {
    Ok(AgentRecord {
        id: row.get(0)?,
        name: row.get(1)?,
        slug: row.get(2)?,
        description: row.get(3)?,
        runtime_id: row.get(4)?,
        model_id: row.get(5)?,
        is_default: row.get::<_, i64>(6)? != 0,
        thinking: row.get(7)?,
        context_window: row.get(8)?,
        memory: row.get::<_, i64>(9)? != 0,
        auto_compact: row.get::<_, i64>(10)? != 0,
        system_prompt: row.get(11)?,
        tools: {
            let raw: String = row.get(12)?;
            // A row saved before tools existed gets the full default set,
            // delegation included: otherwise an existing install could never
            // delegate without editing the agent first.
            decode_names(&raw, &crate::model::DEFAULT_TOOLS)
        },
        skills: {
            let raw: String = row.get(13)?;
            decode_names(&raw, &[])
        },
        permission: row.get(14)?,
        writable_dirs: row.get(15)?,
        command_allowlist: row.get(16)?,
        network: row.get::<_, i64>(17)? != 0,
        confirm_dangerous: row.get::<_, i64>(18)? != 0,
        group_kind: row.get(19)?,
        delegated_from: row.get(20)?,
        delegation_policy: row.get(21)?,
        concurrency: row.get::<_, i64>(22)? as u32,
        enabled: row.get::<_, i64>(23)? != 0,
        updated_at: row.get(24)?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn registration(id: &str, source: InstallSource, status: RuntimeStatus) -> RuntimeRegistration {
        RuntimeRegistration {
            id: id.to_string(),
            runtime_id: "pi".to_string(),
            display_name: "Pi Agent".to_string(),
            version: Some("1.6.0".to_string()),
            executable: std::path::PathBuf::from(format!("{id}/pi")),
            source,
            config_path: None,
            status,
            capabilities: Capabilities::none(),
            health: RuntimeHealth::default(),
            last_seen: "2026-01-01T00:00:00Z".to_string(),
        }
    }

    /// An upgrade from the host-Pi build leaves a `known_dir` row behind; the
    /// bundled runtime is the only install then, so the old row must go instead
    /// of lingering as a second "not installed" Pi Agent.
    #[test]
    fn a_bundled_scan_drops_rows_an_older_install_left_behind() {
        let root = std::env::temp_dir().join(format!(
            "anvil-store-bundled-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).expect("scratch");
        let mut store = Store::open(&root.join("catalog.sqlite")).expect("store");
        let scanned = vec!["pi".to_string()];

        let old = registration("pi:old", InstallSource::KnownDir, RuntimeStatus::NotInstalled);
        store
            .apply_scan(&[old], &scanned, "2026-01-01T00:00:00Z")
            .expect("first scan");

        let bundled = registration(
            "pi:bundled",
            InstallSource::Bundled,
            RuntimeStatus::Unauthenticated,
        );
        store
            .apply_scan(&[bundled], &scanned, "2026-01-01T00:00:01Z")
            .expect("second scan");

        let rows = store.runtimes().expect("rows");
        assert_eq!(rows.len(), 1, "{rows:?}");
        assert_eq!(rows[0].install_source, "bundled");
        assert_eq!(rows[0].id, "pi:bundled");
    }
}
