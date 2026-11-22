//! Connectors: services with credentials and their own API (mail, Notion,
//! DingTalk docs), as opposed to MCP servers, which are tool hosts a runtime
//! speaks JSON-RPC to.
//!
//! The catalogue (types, fields, secrets) lives in `anvil_catalog::connector`;
//! this module owns the parts that need a process: the network probe, the tool
//! calls an agent makes, and the commands the page drives.
//!
//! Every tool call is written to `connector_calls`, so the page's "本周调用"
//! numbers are recorded facts rather than a guess from a transcript.

mod dingtalk;
mod http;
mod mail;
mod notion;

use std::collections::BTreeMap;
use std::time::Instant;

use anvil_catalog::connector::{
    self, ConnectorCallStat, ConnectorField, ConnectorRow, ConnectorType, FieldKind, FieldOption,
};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use tauri::{AppHandle, Manager, State};

use crate::catalog::CatalogState;

/// A connector's stored settings, read through typed accessors.
pub struct Config<'a> {
    inner: &'a serde_json::Map<String, Value>,
}

impl<'a> Config<'a> {
    pub fn new(inner: &'a serde_json::Map<String, Value>) -> Self {
        Self { inner }
    }

    pub fn get(&self, key: &str) -> String {
        self.inner
            .get(key)
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string()
    }

    pub fn number(&self, key: &str, fallback: u64) -> u64 {
        self.get(key).trim().parse().unwrap_or(fallback)
    }

    /// Only an explicit `false` disables a guard; an empty field keeps the
    /// permissive default the reference implementation used.
    pub fn flag(&self, key: &str, fallback: bool) -> bool {
        match self.get(key).trim().to_ascii_lowercase().as_str() {
            "true" | "1" | "yes" => true,
            "false" | "0" | "no" => false,
            _ => fallback,
        }
    }

    #[cfg(test)]
    pub fn from_pairs(pairs: &[(&str, &str)]) -> Config<'static> {
        let mut map = serde_json::Map::new();
        for (key, value) in pairs {
            map.insert((*key).to_string(), Value::String((*value).to_string()));
        }
        Config::new(Box::leak(Box::new(map)))
    }
}

/// The result of a connectivity test.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProbeOutcome {
    pub ok: bool,
    pub message: String,
    pub steps: Vec<String>,
}

impl ProbeOutcome {
    pub fn ok(message: &str, steps: Vec<String>) -> Self {
        Self {
            ok: true,
            message: message.to_string(),
            steps,
        }
    }

    pub fn failed(message: impl Into<String>) -> Self {
        Self {
            ok: false,
            message: message.into(),
            steps: Vec::new(),
        }
    }
}

/// One tool, as the page lists it.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolView {
    pub name: &'static str,
    pub description: &'static str,
    /// Writes or sends: the page marks these so the user knows what they allow.
    pub write: bool,
    pub calls: u64,
    pub failures: u64,
}

/// One type, as the add dialog renders it.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TypeView {
    pub id: &'static str,
    pub name: &'static str,
    pub description: &'static str,
    pub capabilities: &'static [&'static str],
    pub fields: &'static [ConnectorField],
    pub tools: Vec<ToolView>,
    /// How many connectors of this type exist.
    pub count: u64,
}

/// One configured connector.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorView {
    pub id: String,
    pub kind: String,
    pub kind_name: String,
    pub name: String,
    pub enabled: bool,
    pub state: String,
    pub probed_at: Option<String>,
    pub last_error: Option<String>,
    /// Secrets blanked; `secrets_set` says which are stored.
    pub config: serde_json::Map<String, Value>,
    pub secrets_set: Vec<String>,
    pub capabilities: &'static [&'static str],
    pub tools: Vec<ToolView>,
    pub calls: u64,
    pub failures: u64,
    pub last_call_at: Option<String>,
}

#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorStats {
    pub total: u64,
    pub connected: u64,
    pub unverified: u64,
    pub failed: u64,
    pub calls: u64,
    pub failures: u64,
    pub tools: u64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ActivityRow {
    pub connector_id: String,
    pub name: String,
    pub kind_name: String,
    pub tool: String,
    pub ok: bool,
    pub at: String,
    pub detail: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorOverview {
    pub types: Vec<TypeView>,
    pub connectors: Vec<ConnectorView>,
    pub stats: ConnectorStats,
    pub activity: Vec<ActivityRow>,
    pub updated_at: String,
}

/// A fresh connector id.
///
/// Timestamp alone is not enough: two saves in the same millisecond — a person
/// clicking save, a script importing a list — would overwrite each other. The
/// sequence suffix makes the id unique inside this process, and the existence
/// check covers ids created by an earlier run.
fn next_id(store: &anvil_catalog::Store) -> String {
    static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let millis = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|value| value.as_millis())
        .unwrap_or_default();
    loop {
        let seq = SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let candidate = format!("cn-{millis}-{seq}");
        if store.connector(&candidate).ok().flatten().is_none() {
            return candidate;
        }
    }
}

/// A write only counts as one when the tool name says so.
fn is_write_tool(name: &str) -> bool {
    name.contains("create") || name.contains("send") || name.contains("append")
}

fn since_days(utc_offset_minutes: i64, days: i64) -> String {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64
        + utc_offset_minutes * 60;
    let stamp = now - days * 86_400;
    anvil_catalog::clock::rfc3339(stamp)
}

/// Build the page's view from the store. Pure apart from reading rows.
fn build_overview(
    rows: Vec<ConnectorRow>,
    stats: Vec<ConnectorCallStat>,
    since: &str,
) -> ConnectorOverview {
    let mut calls: BTreeMap<(String, String), (u64, u64, String)> = BTreeMap::new();
    for row in &stats {
        calls.insert(
            (row.connector_id.clone(), row.tool.clone()),
            (row.calls, row.failures, row.last_at.clone()),
        );
    }
    let mut per_type: BTreeMap<&str, u64> = BTreeMap::new();
    for row in &rows {
        *per_type.entry(row.kind.as_str()).or_default() += 1;
    }

    let types: Vec<TypeView> = connector::types()
        .into_iter()
        .map(|kind| TypeView {
            id: kind.id,
            name: kind.name,
            description: kind.description,
            capabilities: kind.capabilities,
            fields: kind.fields,
            tools: kind
                .tools
                .iter()
                .map(|(name, description)| {
                    let record = rows
                        .iter()
                        .find(|row| row.kind.eq_ignore_ascii_case(kind.id))
                        .and_then(|row| calls.get(&(row.id.clone(), (*name).to_string())));
                    ToolView {
                        name,
                        description,
                        write: is_write_tool(name),
                        calls: record.map_or(0, |entry| entry.0),
                        failures: record.map_or(0, |entry| entry.1),
                    }
                })
                .collect(),
            count: per_type.get(kind.id).copied().unwrap_or(0),
        })
        .collect();

    let mut view = Vec::new();
    for row in rows {
        let Some(kind) = connector::descriptor(&row.kind) else {
            // A row whose type this build does not know is still listed, so the
            // user can delete it instead of being stuck with an invisible one.
            view.push(ConnectorView {
                id: row.id,
                kind: row.kind.clone(),
                kind_name: row.kind.clone(),
                name: row.name,
                enabled: row.enabled,
                state: row.state,
                probed_at: row.probed_at,
                last_error: row.last_error,
                config: serde_json::Map::new(),
                secrets_set: Vec::new(),
                capabilities: &[],
                tools: Vec::new(),
                calls: 0,
                failures: 0,
                last_call_at: None,
            });
            continue;
        };
        let (config, secrets_set) = kind.public_config(&row.config);
        let tools: Vec<ToolView> = kind
            .tools
            .iter()
            .map(|(name, description)| {
                let record = calls.get(&(row.id.clone(), (*name).to_string()));
                ToolView {
                    name,
                    description,
                    write: is_write_tool(name),
                    calls: record.map_or(0, |entry| entry.0),
                    failures: record.map_or(0, |entry| entry.1),
                }
            })
            .collect();
        let total_calls: u64 = tools.iter().map(|tool| tool.calls).sum();
        let total_failures: u64 = tools.iter().map(|tool| tool.failures).sum();
        let last_call = tools
            .iter()
            .filter_map(|tool| calls.get(&(row.id.clone(), tool.name.to_string())))
            .map(|entry| entry.2.clone())
            .max();
        view.push(ConnectorView {
            id: row.id,
            kind: row.kind.clone(),
            kind_name: kind.name.to_string(),
            name: row.name,
            enabled: row.enabled,
            state: row.state,
            probed_at: row.probed_at,
            last_error: row.last_error,
            config,
            secrets_set,
            capabilities: kind.capabilities,
            tools,
            calls: total_calls,
            failures: total_failures,
            last_call_at: last_call,
        });
    }

    let stats = ConnectorStats {
        total: view.len() as u64,
        connected: view.iter().filter(|row| row.state == "connected").count() as u64,
        unverified: view.iter().filter(|row| row.state == "declared").count() as u64,
        failed: view.iter().filter(|row| row.state == "failed").count() as u64,
        calls: stats.iter().map(|row| row.calls).sum(),
        failures: stats.iter().map(|row| row.failures).sum(),
        tools: types
            .iter()
            .flat_map(|kind| kind.tools.iter())
            .count() as u64,
    };
    ConnectorOverview {
        types,
        connectors: view,
        stats,
        activity: Vec::new(),
        updated_at: since.to_string(),
    }
}

#[tauri::command]
pub async fn connectors_overview(
    state: State<'_, CatalogState>,
    utc_offset_minutes: Option<i64>,
) -> Result<ConnectorOverview, String> {
    let since = since_days(utc_offset_minutes.unwrap_or(480), 7);
    let (rows, calls, recent) = state.with_service(|service| {
        let store = service.store();
        Ok((
            store.connectors().map_err(|error| error.to_wire())?,
            store
                .connector_call_stats(&since)
                .map_err(|error| error.to_wire())?,
            store
                .recent_connector_calls(8)
                .map_err(|error| error.to_wire())?,
        ))
    })?;
    let mut overview = build_overview(rows, calls, &since);
    let names: BTreeMap<String, (String, String)> = overview
        .connectors
        .iter()
        .map(|row| (row.id.clone(), (row.name.clone(), row.kind_name.clone())))
        .collect();
    overview.activity = recent
        .into_iter()
        .map(|call| {
            let (name, kind_name) = names
                .get(&call.connector_id)
                .cloned()
                .unwrap_or_else(|| (call.connector_id.clone(), String::new()));
            ActivityRow {
                connector_id: call.connector_id,
                name,
                kind_name,
                tool: call.tool,
                ok: call.ok,
                at: call.at,
                detail: call.detail,
            }
        })
        .collect();
    Ok(overview)
}

/// What the add / edit dialog sends.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorDraft {
    pub id: Option<String>,
    pub kind: String,
    pub name: String,
    #[serde(default)]
    pub config: serde_json::Map<String, Value>,
}

#[tauri::command]
pub async fn connector_save(
    state: State<'_, CatalogState>,
    draft: ConnectorDraft,
) -> Result<ConnectorOverview, String> {
    let kind = connector::descriptor(&draft.kind)
        .ok_or_else(|| format!("不支持的连接器类型：{}", draft.kind))?;
    let name = draft.name.trim().to_string();
    if name.is_empty() {
        return Err("请给这个连接起个名字".to_string());
    }
    let now = anvil_catalog::clock::now_rfc3339();
    state.with_service(|service| {
        let store = service.store();
        let existing = match &draft.id {
            Some(id) => store.connector(id).map_err(|error| error.to_wire())?,
            None => None,
        };
        // Editing keeps the type it was created with: a Notion row cannot turn
        // into a mail row and keep its stored secrets.
        if let Some(existing) = &existing {
            if !existing.kind.eq_ignore_ascii_case(kind.id) {
                return Err("连接器类型不能修改；请新建一个再删除旧的".to_string());
            }
        }
        let saved_config = existing.as_ref().map(|row| &row.config);
        kind.validate(&draft.config, saved_config)
            .map_err(|error| error.to_wire())?;
        let config = kind.normalize(&draft.config, saved_config);
        let id = draft.id.clone().unwrap_or_else(|| next_id(store));
        let previous_state = existing
            .as_ref()
            .map_or_else(|| "declared".to_string(), |row| row.state.clone());
        let row = ConnectorRow {
            id,
            kind: kind.id.to_string(),
            name,
            config,
            enabled: existing.as_ref().map_or(true, |row| row.enabled),
            // A changed credential must be verified again; unchanged settings
            // keep the state the last probe earned.
            state: previous_state,
            probed_at: existing.as_ref().and_then(|row| row.probed_at.clone()),
            last_error: existing.as_ref().and_then(|row| row.last_error.clone()),
            created_at: existing
                .as_ref()
                .map_or_else(|| now.clone(), |row| row.created_at.clone()),
            updated_at: now.clone(),
        };
        let changed_secret = existing.as_ref().is_some_and(|old| {
            kind.fields
                .iter()
                .filter(|field| field.secret)
                .any(|field| old.config.get(field.key) != row.config.get(field.key))
        });
        store.save_connector(&row).map_err(|error| error.to_wire())?;
        if changed_secret || existing.is_none() {
            store
                .set_connector_state(&row.id, "declared", None, None)
                .map_err(|error| error.to_wire())?;
        }
        Ok(())
    })?;
    connectors_overview(state, None).await
}

#[tauri::command]
pub async fn connector_delete(
    state: State<'_, CatalogState>,
    id: String,
) -> Result<ConnectorOverview, String> {
    state.with_service(|service| {
        service
            .store()
            .delete_connector(&id)
            .map_err(|error| error.to_wire())
    })?;
    connectors_overview(state, None).await
}

#[tauri::command]
pub async fn connector_toggle(
    state: State<'_, CatalogState>,
    id: String,
    enabled: bool,
) -> Result<ConnectorOverview, String> {
    let now = anvil_catalog::clock::now_rfc3339();
    state.with_service(|service| {
        service
            .store()
            .set_connector_enabled(&id, enabled, &now)
            .map_err(|error| error.to_wire())
    })?;
    connectors_overview(state, None).await
}

/// Probe one connector for real: credentials are used, not just checked for
/// being non-empty.
#[tauri::command]
pub async fn connector_probe(
    state: State<'_, CatalogState>,
    id: String,
) -> Result<ProbeOutcome, String> {
    let row = state.with_service(|service| {
        service
            .store()
            .connector(&id)
            .map_err(|error| error.to_wire())
    })?;
    let Some(row) = row else {
        return Err(format!("没有这个连接器：{id}"));
    };
    let kind = connector::descriptor(&row.kind)
        .ok_or_else(|| format!("不支持的连接器类型：{}", row.kind))?;
    // curl blocks for as long as the server takes; keep it off the UI thread.
    let probe_row = row.clone();
    let outcome = tauri::async_runtime::spawn_blocking(move || {
        let config = Config::new(&probe_row.config);
        match kind.id {
            "MAIL" => mail::probe(&config),
            "NOTION" => notion::probe(&config),
            "DINGTALK_DOC" => dingtalk::probe(&config),
            other => ProbeOutcome::failed(format!("这个版本还不能测试 {other}")),
        }
    })
    .await
    .map_err(|error| format!("测试任务失败：{error}"))?;

    let now = anvil_catalog::clock::now_rfc3339();
    state.with_service(|service| {
        service
            .store()
            .set_connector_state(
                &id,
                if outcome.ok { "connected" } else { "failed" },
                Some(&now),
                if outcome.ok {
                    None
                } else {
                    Some(outcome.message.as_str())
                },
            )
            .map_err(|error| error.to_wire())
    })?;
    Ok(outcome)
}

/// Run one connector tool. Called from the bridge, on a request thread.
pub fn call_tool(app: &AppHandle, connector_id: &str, tool: &str, params: &Value) -> Value {
    let Some(state) = app.try_state::<CatalogState>() else {
        return json!({ "ok": false, "error": "catalog is not ready" });
    };
    let row = match state.with_service(|service| {
        service
            .store()
            .connector(connector_id)
            .map_err(|error| error.to_wire())
    }) {
        Ok(Some(row)) => row,
        Ok(None) => {
            return json!({ "ok": false, "error": format!("没有这个连接器：{connector_id}") });
        }
        Err(error) => return json!({ "ok": false, "error": error }),
    };
    if !row.enabled {
        return json!({ "ok": false, "error": "这个连接器已停用" });
    }
    let Some(kind) = connector::descriptor(&row.kind) else {
        return json!({ "ok": false, "error": format!("未知类型：{}", row.kind) });
    };
    if !kind.tools.iter().any(|(name, _)| *name == tool) {
        let names: Vec<&str> = kind.tools.iter().map(|(name, _)| *name).collect();
        return json!({
            "ok": false,
            "error": format!("{tool} 不属于这个连接器（可用：{}）", names.join(", ")),
        });
    }

    let started = Instant::now();
    let config = Config::new(&row.config);
    let result = match kind.id {
        "MAIL" => mail::call(&config, tool, params),
        "NOTION" => notion::call(&config, tool, params),
        "DINGTALK_DOC" => dingtalk::call(&config, tool, params),
        other => Err(format!("这个版本还不能调用 {other}")),
    };
    let elapsed = started.elapsed().as_millis() as i64;
    let now = anvil_catalog::clock::now_rfc3339();
    let (ok, detail) = match &result {
        Ok(value) => (
            true,
            value
                .get("text")
                .and_then(Value::as_str)
                .map(|text| text.chars().take(200).collect::<String>()),
        ),
        Err(error) => (false, Some(error.chars().take(200).collect::<String>())),
    };
    if let Err(error) = state.with_service(|service| {
        service
            .store()
            .record_connector_call(&row.id, tool, ok, detail.as_deref(), &now, elapsed)
            .map(|_| ())
            .map_err(|error| error.to_wire())
    }) {
        eprintln!("anvil: connector call log: {error}");
    }
    match result {
        Ok(value) => json!({ "ok": true, "value": value }),
        Err(error) => json!({ "ok": false, "error": error }),
    }
}

/// `GET /connectors`: what an agent can call, without any credential material.
pub fn list_for_agent(app: &AppHandle) -> Value {
    let Some(state) = app.try_state::<CatalogState>() else {
        return json!({ "ok": false, "error": "catalog is not ready" });
    };
    match state.with_service(|service| {
        service
            .store()
            .connectors()
            .map_err(|error| error.to_wire())
    }) {
        Ok(rows) => {
            let list: Vec<Value> = rows
                .into_iter()
                .filter(|row| row.enabled)
                .filter_map(|row| {
                    let kind = connector::descriptor(&row.kind)?;
                    Some(json!({
                        "id": row.id,
                        "name": row.name,
                        "type": kind.id,
                        "state": row.state,
                        "tools": kind.tools.iter().map(|(name, description)| {
                            json!({ "name": name, "description": description })
                        }).collect::<Vec<_>>(),
                    }))
                })
                .collect();
            json!({ "ok": true, "connectors": list })
        }
        Err(error) => json!({ "ok": false, "error": error }),
    }
}

/// A field's control type, exposed for the page's form renderer.
pub fn field_kind(field: &ConnectorField) -> &'static str {
    match field.kind {
        FieldKind::Text => "text",
        FieldKind::Password => "password",
        FieldKind::Number => "number",
        FieldKind::Select => "select",
        FieldKind::Tags => "tags",
    }
}

/// Select options are serialised with the descriptor; this keeps the helper
/// referenced so a future field type cannot silently lose its options.
pub fn field_options(field: &ConnectorField) -> &'static [FieldOption] {
    field.options
}

/// The type descriptor, for callers that only have an id.
pub fn descriptor(id: &str) -> Option<ConnectorType> {
    connector::descriptor(id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tool_names_mark_writes() {
        assert!(is_write_tool("notion_create_page"));
        assert!(is_write_tool("mail_send"));
        assert!(!is_write_tool("mail_list"));
    }

    #[test]
    fn overview_counts_each_state_once() {
        let rows = vec![
            ConnectorRow {
                id: "cn-1".into(),
                kind: "MAIL".into(),
                name: "work".into(),
                config: serde_json::Map::new(),
                enabled: true,
                state: "connected".into(),
                probed_at: Some("2026-11-22T00:00:00Z".into()),
                last_error: None,
                created_at: "2026-11-21T00:00:00Z".into(),
                updated_at: "2026-11-21T00:00:00Z".into(),
            },
            ConnectorRow {
                id: "cn-2".into(),
                kind: "NOTION".into(),
                name: "notes".into(),
                config: serde_json::Map::new(),
                enabled: true,
                state: "failed".into(),
                probed_at: None,
                last_error: Some("HTTP 401".into()),
                created_at: "2026-11-21T00:00:00Z".into(),
                updated_at: "2026-11-21T00:00:00Z".into(),
            },
        ];
        let stats = vec![ConnectorCallStat {
            connector_id: "cn-1".into(),
            tool: "mail_list".into(),
            calls: 3,
            failures: 1,
            last_at: "2026-11-22T01:00:00Z".into(),
        }];
        let overview = build_overview(rows, stats, "2026-11-15T00:00:00Z");
        assert_eq!(overview.stats.total, 2);
        assert_eq!(overview.stats.failed, 1);
        assert_eq!(overview.stats.calls, 3);
        assert_eq!(overview.connectors[0].calls, 3);
        assert_eq!(overview.connectors[0].last_call_at.as_deref(), Some("2026-11-22T01:00:00Z"));
        // A stored token never travels to the page.
        assert!(overview.connectors[0]
            .config
            .values()
            .all(|value| value.as_str() != Some("secret")));
    }

    #[test]
    fn defaults_survive_an_empty_config() {
        let config = Config::from_pairs(&[]);
        assert_eq!(config.number("imapPort", 993), 993);
        assert!(config.flag("allowWrite", true));
    }
}
