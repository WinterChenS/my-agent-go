//! Connectors: the integrations this machine actually has, read from MCP.
//!
//! A connector in this app is an MCP server. That is not a downgrade of the
//! prototype's mail/IM/doc cards — it is the honest mapping: the runtime reaches
//! those services through declared MCP servers, and the catalog already knows
//! each one's transport, endpoint and probe state. A card therefore shows what
//! exists (a server, its probed tools, its calls in the ledger) and nothing else:
//! a server nobody has probed reports zero actions rather than a guessed twelve.
//!
//! Calls are matched by tool name (`mcp__<server>__<tool>`, or a probed tool name
//! seen verbatim), so every count comes from real `tool_call` rows.

use std::collections::{BTreeMap, BTreeSet};

use rusqlite::{Connection, OptionalExtension, params};
use serde::Serialize;
use serde_json::Value;
use tauri::State;

use anvil_catalog::runtime::adapter::McpProbe;

use crate::session::SessionState;

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorAction {
    pub tool: String,
    pub calls: u64,
    pub failures: u64,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Connector {
    pub runtime_id: String,
    pub name: String,
    pub transport: String,
    /// `command args…` or the URL; secrets are never part of it.
    pub endpoint: String,
    pub state: String,
    pub probed_at: Option<String>,
    pub tools: Vec<String>,
    pub calls: u64,
    pub failures: u64,
    pub last_used: Option<String>,
    pub sessions: u64,
    pub actions: Vec<ConnectorAction>,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorStats {
    pub total: u64,
    pub connected: u64,
    pub actions: u64,
    pub week_calls: u64,
    pub week_failures: u64,
    /// `None` when nothing was called: "—", not 100%.
    pub success_rate: Option<f64>,
    pub last_probe: Option<String>,
    /// At least one server speaks a transport this build cannot probe.
    pub remote_unprobeable: bool,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorActivity {
    pub at: String,
    pub connector: String,
    pub tool: String,
    pub ok: bool,
    pub session_id: String,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorView {
    pub connectors: Vec<Connector>,
    pub stats: ConnectorStats,
    pub activity: Vec<ConnectorActivity>,
    pub ready: bool,
}

#[tauri::command]
pub async fn connectors_overview(
    state: State<'_, SessionState>,
    utc_offset_minutes: Option<i64>,
) -> Result<ConnectorView, String> {
    let offset = utc_offset_minutes.unwrap_or(0).clamp(-840, 840);
    let catalog_path = state.catalog_database().to_path_buf();
    // Opening a missing catalog would create one; "not installed" must stay
    // distinguishable from "installed and empty".
    let Some(catalog) = catalog_path
        .exists()
        .then(|| anvil_catalog::Store::open(&catalog_path).ok())
        .flatten()
    else {
        return Ok(ConnectorView::default());
    };
    state.with_reader(|store| {
        view(&catalog, store.raw(), offset).map_err(|error| error.to_wire().to_string())
    })
}

/// Probe one connector. The state and tool list it writes are what the card
/// shows on the next read, so verifying is a real observation, not a toast.
#[tauri::command]
pub async fn connector_verify(
    state: State<'_, crate::catalog::CatalogState>,
    runtime_id: String,
    name: String,
) -> Result<McpProbe, String> {
    state.with_service(|service| {
        service
            .probe_mcp(&runtime_id, &name)
            .map_err(|error| error.to_wire())
    })
}

fn view(
    catalog: &anvil_catalog::Store,
    connection: &Connection,
    offset_minutes: i64,
) -> anvil_session::Result<ConnectorView> {
    let Ok(servers) = catalog.mcp() else {
        return Ok(ConnectorView::default());
    };
    let since = anvil_session::clock::rfc3339(
        anvil_session::clock::unix_now() - 7 * 86_400 - offset_minutes * 60,
    );
    let calls = week_calls(connection, &since)?;
    let mut connectors: Vec<Connector> = Vec::new();
    let mut stats = ConnectorStats::default();
    for server in &servers {
        let mut row = Connector {
            runtime_id: server.runtime_id.clone(),
            name: server.name.clone(),
            transport: server.transport.clone(),
            endpoint: endpoint_of(server),
            state: server.state.clone(),
            probed_at: server.probed_at.clone(),
            tools: server.tools.clone(),
            ..Connector::default()
        };
        let mut actions: BTreeMap<String, ConnectorAction> = row
            .tools
            .iter()
            .map(|tool| {
                (
                    tool.clone(),
                    ConnectorAction {
                        tool: tool.clone(),
                        ..ConnectorAction::default()
                    },
                )
            })
            .collect();
        let mut sessions: BTreeSet<&str> = BTreeSet::new();
        for call in &calls {
            if !belongs(&call.tool, &server.name, &server.tools) {
                continue;
            }
            row.calls += 1;
            if !call.ok {
                row.failures += 1;
            }
            if row
                .last_used
                .as_deref()
                .map_or(true, |last| call.at.as_str() > last)
            {
                row.last_used = Some(call.at.clone());
            }
            sessions.insert(call.session_id.as_str());
            let entry = actions
                .entry(call.tool.clone())
                .or_insert_with(|| ConnectorAction {
                    tool: call.tool.clone(),
                    ..ConnectorAction::default()
                });
            entry.calls += 1;
            if !call.ok {
                entry.failures += 1;
            }
        }
        row.sessions = sessions.len() as u64;
        row.actions = actions.into_values().collect();
        row.actions.sort_by(|left, right| {
            right
                .calls
                .cmp(&left.calls)
                .then_with(|| left.tool.cmp(&right.tool))
        });
        stats.total += 1;
        if row.state == "connected" {
            stats.connected += 1;
        }
        stats.actions += row.tools.len() as u64;
        stats.week_calls += row.calls;
        stats.week_failures += row.failures;
        if row.state == "unsupported_transport" {
            stats.remote_unprobeable = true;
        }
        if let Some(at) = &row.probed_at
            && stats
                .last_probe
                .as_deref()
                .map_or(true, |last| at.as_str() > last)
        {
            stats.last_probe = Some(at.clone());
        }
        connectors.push(row);
    }
    connectors.sort_by(|left, right| {
        right
            .calls
            .cmp(&left.calls)
            .then_with(|| left.name.cmp(&right.name))
    });
    stats.success_rate = (stats.week_calls > 0).then(|| {
        (stats.week_calls - stats.week_failures) as f64 * 1000.0 / stats.week_calls as f64 / 10.0
    });
    // The activity feed is every matched call, newest first.
    let activity = calls
        .iter()
        .rev()
        .filter_map(|call| {
            let connector = connectors
                .iter()
                .find(|row| belongs(&call.tool, &row.name, &row.tools))?;
            Some(ConnectorActivity {
                at: call.at.clone(),
                connector: connector.name.clone(),
                tool: call.tool.clone(),
                ok: call.ok,
                session_id: call.session_id.clone(),
            })
        })
        .take(8)
        .collect();
    Ok(ConnectorView {
        connectors,
        stats,
        activity,
        ready: true,
    })
}

fn endpoint_of(server: &anvil_catalog::model::McpRecord) -> String {
    if server.transport == "stdio" {
        let mut parts = vec![server.command.clone().unwrap_or_default()];
        parts.extend(server.args.iter().cloned());
        return parts.join(" ");
    }
    server.url.clone().unwrap_or_default()
}

struct CallRow {
    at: String,
    tool: String,
    ok: bool,
    session_id: String,
}

/// Tool calls in the window, each paired with its own result.
///
/// A call and its result are two events in one run; the result repeats the tool
/// name, which is how the pair is found.
fn week_calls(connection: &Connection, since: &str) -> anvil_session::Result<Vec<CallRow>> {
    let mut statement = connection.prepare(
        "SELECT session_id, run_id, at, payload FROM events
         WHERE kind = 'tool_call' AND at >= ?1 ORDER BY at",
    )?;
    let rows = statement.query_map(params![since], |row| {
        Ok((
            row.get::<_, String>(0)?,
            row.get::<_, String>(1)?,
            row.get::<_, String>(2)?,
            row.get::<_, String>(3)?,
        ))
    })?;
    let mut result_statement = connection.prepare(
        "SELECT payload FROM events
         WHERE session_id = ?1 AND run_id = ?2 AND kind = 'tool_result'
           AND json_extract(payload, '$.tool') = ?3
         ORDER BY seq DESC LIMIT 1",
    )?;
    let mut calls = Vec::new();
    for row in rows {
        let (session_id, run_id, at, payload) = row?;
        let value: Value = serde_json::from_str(&payload).unwrap_or_default();
        let tool = value
            .get("tool")
            .or_else(|| value.get("name"))
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
        if tool.is_empty() {
            continue;
        }
        let outcome: Option<String> = result_statement
            .query_row(params![session_id, run_id, tool], |row| row.get(0))
            .optional()?;
        let ok = outcome
            .and_then(|payload| serde_json::from_str::<Value>(&payload).ok())
            .and_then(|value| value.get("ok").and_then(Value::as_bool))
            .unwrap_or(true);
        calls.push(CallRow {
            at,
            tool,
            ok,
            session_id,
        });
    }
    Ok(calls)
}

/// Whether a tool name belongs to a server.
///
/// pi names an MCP tool `mcp__<server>__<tool>`, and a probed tool may also be
/// reported verbatim. Both checks are exact prefixes/equalities, so a server
/// called `box` does not swallow `box-admin`.
fn belongs(tool: &str, server: &str, probed: &[String]) -> bool {
    if probed.iter().any(|name| name == tool) {
        return true;
    }
    tool.starts_with(&format!("mcp__{server}__"))
        || tool.starts_with(&format!("{server}__"))
        || tool.starts_with(&format!("mcp_{server}_"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tool_names_match_their_server_only() {
        let probed = vec!["search".to_string()];
        assert!(belongs("mcp__mail__send", "mail", &probed));
        assert!(belongs("mail__send", "mail", &probed));
        assert!(belongs("search", "mail", &probed));
        assert!(!belongs("mcp__mailbox__send", "mail", &probed));
        assert!(!belongs("mailbox__send", "mail", &probed));
        assert!(!belongs("bash", "mail", &probed));
    }
}
