//! MCP declaration import, redaction and an explicit connection probe.
//!
//! Import is read-only: it parses declarations from a runtime's config and keeps
//! the shape (command, args, url) plus the *names* of environment variables and
//! headers. Values are dropped before they reach the store, so a synced snapshot
//! can never leak a token. Connecting is a separate, explicit action that first
//! checks the command is trusted.

use std::io::{BufRead, BufReader, Write};
use std::path::{Component, Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

use crate::error::{Error, Result};
use crate::model::McpRecord;

/// Everything an importer knows about one declaration.
struct Draft<'a> {
    runtime_id: &'a str,
    name: &'a str,
    transport: &'a str,
    command: Option<String>,
    args: Vec<String>,
    url: Option<String>,
    env_keys: Vec<String>,
    header_keys: Vec<String>,
    source: &'a str,
}

impl Draft<'_> {
    /// Build the record with every secret stripped.
    fn build(self) -> McpRecord {
        let mut env_keys = self.env_keys;
        env_keys.sort();
        env_keys.dedup();
        let mut header_keys = self.header_keys;
        header_keys.sort();
        header_keys.dedup();
        McpRecord {
            runtime_id: self.runtime_id.to_string(),
            name: self.name.to_string(),
            transport: self.transport.to_string(),
            command: self.command,
            args: self.args,
            url: self.url,
            env_keys,
            header_keys,
            source: self.source.to_string(),
            state: "declared".to_string(),
            probed_at: None,
            tools: Vec::new(),
        }
    }
}

fn keys_of(map: Option<&serde_json::Map<String, serde_json::Value>>) -> Vec<String> {
    map.map(|map| map.keys().cloned().collect())
        .unwrap_or_default()
}

/// `~/.codex/config.toml` `[mcp_servers.<name>]`.
pub fn from_codex_toml(runtime_id: &str, text: &str, source: &str) -> Result<Vec<McpRecord>> {
    let value: toml::Value = toml::from_str(text)
        .map_err(|error| Error::Format(format!("codex config.toml: {error}")))?;
    let Some(servers) = value
        .get("mcp_servers")
        .and_then(|servers| servers.as_table())
    else {
        return Ok(Vec::new());
    };
    let mut out = Vec::new();
    for (name, entry) in servers {
        let table = entry.as_table();
        let command = table
            .and_then(|table| table.get("command"))
            .and_then(|value| value.as_str())
            .map(str::to_string);
        let args: Vec<String> = table
            .and_then(|table| table.get("args"))
            .and_then(|value| value.as_array())
            .map(|items| {
                items
                    .iter()
                    .filter_map(|item| item.as_str().map(str::to_string))
                    .collect()
            })
            .unwrap_or_default();
        let url = table
            .and_then(|table| table.get("url"))
            .and_then(|value| value.as_str())
            .map(str::to_string);
        let env_keys: Vec<String> = table
            .and_then(|table| table.get("env"))
            .and_then(|value| value.as_table())
            .map(|table| table.keys().cloned().collect())
            .unwrap_or_default();
        let transport = if url.is_some() { "http" } else { "stdio" };
        out.push(
            Draft {
                runtime_id,
                name,
                transport,
                command,
                args,
                url,
                env_keys,
                header_keys: Vec::new(),
                source,
            }
            .build(),
        );
    }
    Ok(out)
}

/// Claude's `mcpServers` object (`~/.claude.json`, `<project>/.mcp.json`).
pub fn from_claude_json(runtime_id: &str, text: &str, source: &str) -> Result<Vec<McpRecord>> {
    let value: serde_json::Value = serde_json::from_str(text)
        .map_err(|error| Error::Format(format!("claude mcp config: {error}")))?;
    let Some(servers) = value
        .get("mcpServers")
        .and_then(|servers| servers.as_object())
    else {
        return Ok(Vec::new());
    };
    let mut out = Vec::new();
    for (name, entry) in servers {
        let object = entry.as_object();
        let command = object
            .and_then(|object| object.get("command"))
            .and_then(|value| value.as_str())
            .map(str::to_string);
        let args: Vec<String> = object
            .and_then(|object| object.get("args"))
            .and_then(|value| value.as_array())
            .map(|items| {
                items
                    .iter()
                    .filter_map(|item| item.as_str().map(str::to_string))
                    .collect()
            })
            .unwrap_or_default();
        let url = object
            .and_then(|object| object.get("url"))
            .and_then(|value| value.as_str())
            .map(str::to_string);
        let env_keys = keys_of(
            object
                .and_then(|object| object.get("env"))
                .and_then(|value| value.as_object()),
        );
        let header_keys = keys_of(
            object
                .and_then(|object| object.get("headers"))
                .and_then(|value| value.as_object()),
        );
        let declared_type = object
            .and_then(|object| object.get("type"))
            .and_then(|value| value.as_str())
            .unwrap_or("");
        let transport = if url.is_some() || declared_type == "http" || declared_type == "sse" {
            "http"
        } else {
            "stdio"
        };
        out.push(
            Draft {
                runtime_id,
                name,
                transport,
                command,
                args,
                url,
                env_keys,
                header_keys,
                source,
            }
            .build(),
        );
    }
    Ok(out)
}

/// OpenCode's `opencode.json` `mcp` object (`type: local|remote`).
pub fn from_opencode_json(runtime_id: &str, text: &str, source: &str) -> Result<Vec<McpRecord>> {
    let value: serde_json::Value = serde_json::from_str(text)
        .map_err(|error| Error::Format(format!("opencode config: {error}")))?;
    let Some(servers) = value.get("mcp").and_then(|servers| servers.as_object()) else {
        return Ok(Vec::new());
    };
    let mut out = Vec::new();
    for (name, entry) in servers {
        let object = entry.as_object();
        let command_array: Vec<String> = object
            .and_then(|object| object.get("command"))
            .and_then(|value| value.as_array())
            .map(|items| {
                items
                    .iter()
                    .filter_map(|item| item.as_str().map(str::to_string))
                    .collect()
            })
            .unwrap_or_default();
        let (command, args) = match command_array.split_first() {
            Some((first, rest)) => (Some(first.clone()), rest.to_vec()),
            None => (None, Vec::new()),
        };
        let url = object
            .and_then(|object| object.get("url"))
            .and_then(|value| value.as_str())
            .map(str::to_string);
        let env_keys = keys_of(
            object
                .and_then(|object| object.get("environment"))
                .and_then(|value| value.as_object()),
        );
        let header_keys = keys_of(
            object
                .and_then(|object| object.get("headers"))
                .and_then(|value| value.as_object()),
        );
        let declared = object
            .and_then(|object| object.get("type"))
            .and_then(|value| value.as_str())
            .unwrap_or("");
        let transport = if declared == "remote" || url.is_some() {
            "http"
        } else {
            "stdio"
        };
        out.push(
            Draft {
                runtime_id,
                name,
                transport,
                command,
                args,
                url,
                env_keys,
                header_keys,
                source,
            }
            .build(),
        );
    }
    Ok(out)
}

/// Pi's `mcp.json` (`mcpServers`).
pub fn from_pi_json(runtime_id: &str, text: &str, source: &str) -> Result<Vec<McpRecord>> {
    from_claude_json(runtime_id, text, source)
}

/// Where a runtime reads its own MCP declarations from.
fn config_path(runtime_id: &str, home: &Path) -> Result<PathBuf> {
    match runtime_id {
        "pi" => Ok(home.join(".pi").join("agent").join("mcp.json")),
        "claude" => Ok(home.join(".claude.json")),
        "codex" => Ok(home.join(".codex").join("config.toml")),
        "opencode" => Ok(home.join(".config").join("opencode").join("opencode.json")),
        other => Err(Error::Rejected(format!("{other} does not support MCP declarations"))),
    }
}

fn replace_file(path: &Path, text: &str) -> Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|error| Error::Io(format!("create {}: {error}", parent.display())))?;
    }
    let temp = path.with_extension("anvil-tmp");
    std::fs::write(&temp, text)
        .map_err(|error| Error::Io(format!("write {}: {error}", temp.display())))?;
    std::fs::rename(&temp, path)
        .map_err(|error| Error::Io(format!("replace {}: {error}", path.display())))?;
    Ok(())
}

fn read_or_empty(path: &Path) -> Result<String> {
    match std::fs::read_to_string(path) {
        Ok(text) if text.trim().is_empty() => Ok(String::new()),
        Ok(text) => Ok(text),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(String::new()),
        Err(error) => Err(Error::Io(format!("read {}: {error}", path.display()))),
    }
}

/// Write one server declaration into the runtime's own config file, preserving
/// every other entry. Secrets stay in that file; the catalog only ever imports
/// their names.
#[allow(clippy::too_many_arguments)]
pub fn write_config(
    runtime_id: &str,
    home: &Path,
    name: &str,
    transport: &str,
    command: Option<&str>,
    args: &[String],
    url: Option<&str>,
    env: &[(String, String)],
) -> Result<()> {
    if name.trim().is_empty()
        || !name
            .chars()
            .all(|ch| ch.is_ascii_alphanumeric() || ch == '-' || ch == '_')
    {
        return Err(Error::Rejected("server name may only use letters, digits, - and _".into()));
    }
    let remote = transport != "stdio";
    if remote && url.map(|value| value.trim().is_empty()).unwrap_or(true) {
        return Err(Error::Rejected("remote servers need a URL".into()));
    }
    if !remote && command.map(|value| value.trim().is_empty()).unwrap_or(true) {
        return Err(Error::Rejected("stdio servers need a command".into()));
    }
    let path = config_path(runtime_id, home)?;
    match runtime_id {
        "codex" => {
            let mut root: toml::Value = if read_or_empty(&path)?.trim().is_empty() {
                toml::Value::Table(toml::map::Map::new())
            } else {
                toml::from_str(&read_or_empty(&path)?)
                    .map_err(|error| Error::Format(format!("codex config.toml: {error}")))?
            };
            let servers = root
                .as_table_mut()
                .ok_or_else(|| Error::Format("codex config.toml is not a table".into()))?
                .entry("mcp_servers")
                .or_insert_with(|| toml::Value::Table(toml::map::Map::new()));
            let table = servers
                .as_table_mut()
                .ok_or_else(|| Error::Format("mcp_servers is not a table".into()))?;
            let mut entry = toml::map::Map::new();
            if remote {
                entry.insert("url".into(), toml::Value::String(url.unwrap().to_string()));
            } else {
                entry.insert(
                    "command".into(),
                    toml::Value::String(command.unwrap().to_string()),
                );
                entry.insert(
                    "args".into(),
                    toml::Value::Array(args.iter().cloned().map(toml::Value::String).collect()),
                );
                if !env.is_empty() {
                    let env_table: toml::map::Map<_, _> = env
                        .iter()
                        .map(|(key, value)| (key.clone(), toml::Value::String(value.clone())))
                        .collect();
                    entry.insert("env".into(), toml::Value::Table(env_table));
                }
            }
            table.insert(name.to_string(), toml::Value::Table(entry));
            replace_file(&path, &toml::to_string_pretty(&root)
                .map_err(|error| Error::Format(format!("codex config.toml: {error}")))?)
        }
        "opencode" => {
            let mut root: serde_json::Value = if read_or_empty(&path)?.trim().is_empty() {
                serde_json::json!({})
            } else {
                serde_json::from_str(&read_or_empty(&path)?)
                    .map_err(|error| Error::Format(format!("opencode.json: {error}")))?
            };
            let servers = root
                .as_object_mut()
                .ok_or_else(|| Error::Format("opencode.json is not an object".into()))?
                .entry("mcp".to_string())
                .or_insert_with(|| serde_json::json!({}));
            let table = servers
                .as_object_mut()
                .ok_or_else(|| Error::Format("opencode mcp is not an object".into()))?;
            let mut entry = serde_json::Map::new();
            if remote {
                entry.insert("type".into(), serde_json::json!("remote"));
                entry.insert("url".into(), serde_json::json!(url.unwrap()));
            } else {
                entry.insert("type".into(), serde_json::json!("local"));
                let mut command_line: Vec<String> = vec![command.unwrap().to_string()];
                command_line.extend(args.iter().cloned());
                entry.insert("command".into(), serde_json::json!(command_line));
                if !env.is_empty() {
                    let values: serde_json::Map<_, _> = env
                        .iter()
                        .map(|(key, value)| (key.clone(), serde_json::json!(value)))
                        .collect();
                    entry.insert("environment".into(), serde_json::Value::Object(values));
                }
            }
            table.insert(name.to_string(), serde_json::Value::Object(entry));
            replace_file(
                &path,
                &serde_json::to_string_pretty(&root)
                    .map_err(|error| Error::Format(format!("opencode.json: {error}")))?,
            )
        }
        _ => {
            let mut root: serde_json::Value = if read_or_empty(&path)?.trim().is_empty() {
                serde_json::json!({})
            } else {
                serde_json::from_str(&read_or_empty(&path)?)
                    .map_err(|error| Error::Format(format!("mcp config: {error}")))?
            };
            let servers = root
                .as_object_mut()
                .ok_or_else(|| Error::Format("mcp config is not an object".into()))?
                .entry("mcpServers".to_string())
                .or_insert_with(|| serde_json::json!({}));
            let table = servers
                .as_object_mut()
                .ok_or_else(|| Error::Format("mcpServers is not an object".into()))?;
            let mut entry = serde_json::Map::new();
            if remote {
                entry.insert("url".into(), serde_json::json!(url.unwrap()));
            } else {
                entry.insert("command".into(), serde_json::json!(command.unwrap()));
                if !args.is_empty() {
                    entry.insert("args".into(), serde_json::json!(args));
                }
                if !env.is_empty() {
                    let values: serde_json::Map<_, _> = env
                        .iter()
                        .map(|(key, value)| (key.clone(), serde_json::json!(value)))
                        .collect();
                    entry.insert("env".into(), serde_json::Value::Object(values));
                }
            }
            table.insert(name.to_string(), serde_json::Value::Object(entry));
            replace_file(
                &path,
                &serde_json::to_string_pretty(&root)
                    .map_err(|error| Error::Format(format!("mcp config: {error}")))?,
            )
        }
    }
}

/// Probe a declaration the reader has typed but not saved yet.
pub fn probe_draft(
    transport: &str,
    command: Option<&str>,
    args: &[String],
    timeout: Duration,
) -> Result<Vec<String>> {
    if transport != "stdio" {
        return Err(Error::Rejected(
            "remote MCP connections are not probed by this build".into(),
        ));
    }
    let command = command
        .filter(|value| !value.trim().is_empty())
        .ok_or_else(|| Error::Rejected("stdio servers need a command".into()))?;
    probe_stdio(command, args, timeout)
}

/// Whether a declared command may be executed by an explicit probe.
///
/// Anything that leaves the local filesystem, contains parent traversal, or does
/// not resolve to a real file is refused; a bare program name is resolved on
/// `PATH` first.
pub fn resolve_trusted(command: &str) -> std::result::Result<PathBuf, String> {
    let trimmed = command.trim();
    if trimmed.is_empty() {
        return Err("command is empty".into());
    }
    if trimmed.starts_with(r"\\") || trimmed.starts_with("//") {
        return Err("network (UNC) paths are not trusted".into());
    }
    let path = Path::new(trimmed);
    if path
        .components()
        .any(|component| component == Component::ParentDir)
    {
        return Err("paths containing '..' are not trusted".into());
    }
    if path.is_absolute() {
        return if path.is_file() {
            Ok(path.to_path_buf())
        } else {
            Err(format!("{} is not a file", path.display()))
        };
    }
    crate::runtime::process::which(trimmed)
        .ok_or_else(|| format!("{trimmed:?} is neither absolute nor on PATH"))
}

/// Connect to a stdio server, run `initialize` + `tools/list`, then stop it.
///
/// The child is killed on every path, including errors, so a broken server can
/// never outlive the probe.
pub fn probe_stdio(command: &str, args: &[String], timeout: Duration) -> Result<Vec<String>> {
    let executable = resolve_trusted(command).map_err(Error::Rejected)?;
    let mut child = Command::new(&executable)
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|error| Error::Io(format!("spawn {}: {error}", executable.display())))?;

    let stdout = child.stdout.take();
    let (sender, receiver) = mpsc::channel::<String>();
    if let Some(stdout) = stdout {
        std::thread::spawn(move || {
            for line in BufReader::new(stdout)
                .lines()
                .map_while(std::result::Result::ok)
            {
                if sender.send(line).is_err() {
                    break;
                }
            }
        });
    }

    if let Some(stdin) = child.stdin.as_mut() {
        let initialize = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"anvil","version":"0.1.0"}}}"#;
        let initialized = r#"{"jsonrpc":"2.0","method":"notifications/initialized"}"#;
        let list = r#"{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}"#;
        let mut write = |text: &str| {
            let _ = stdin.write_all(text.as_bytes());
            let _ = stdin.write_all(b"\n");
            let _ = stdin.flush();
        };
        write(initialize);
        write(initialized);
        write(list);
    }

    let deadline = Instant::now() + timeout;
    let mut tools = Vec::new();
    let mut error = None;
    while Instant::now() < deadline {
        match receiver.recv_timeout(Duration::from_millis(100)) {
            Ok(line) => {
                let trimmed = line.trim();
                if !trimmed.starts_with('{') {
                    continue;
                }
                let Ok(value) = serde_json::from_str::<serde_json::Value>(trimmed) else {
                    continue;
                };
                if value.get("id").and_then(|id| id.as_i64()) == Some(2) {
                    if let Some(message) = value.get("error") {
                        error = Some(format!("tools/list failed: {message}"));
                    } else if let Some(items) = value
                        .get("result")
                        .and_then(|result| result.get("tools"))
                        .and_then(|tools| tools.as_array())
                    {
                        tools = items
                            .iter()
                            .filter_map(|tool| tool.get("name").and_then(|name| name.as_str()))
                            .map(str::to_string)
                            .collect();
                    }
                    break;
                }
            }
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                error = Some("server closed stdout before answering tools/list".into());
                break;
            }
        }
    }

    let _ = child.kill();
    let _ = child.wait();

    match error {
        Some(error) => Err(Error::Io(error)),
        None if tools.is_empty() => Err(Error::Timeout(
            "server did not answer tools/list before the deadline".into(),
        )),
        None => Ok(tools),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn codex_import_keeps_env_names_only() {
        let text = r#"
[mcp_servers.demo]
command = "C:\\Tools\\demo.exe"
args = ["--serve"]
startup_timeout_sec = 30
[mcp_servers.demo.env]
DEMO_TOKEN = "super-secret"
DEMO_REGION = "eu"
"#;
        let records = from_codex_toml("codex", text, "codex config.toml").expect("parse");
        assert_eq!(records.len(), 1);
        let server = &records[0];
        assert_eq!(server.name, "demo");
        assert_eq!(server.transport, "stdio");
        assert_eq!(server.env_keys, vec!["DEMO_REGION", "DEMO_TOKEN"]);
        assert!(
            !serde_json::to_string(server)
                .expect("json")
                .contains("super-secret")
        );
    }

    #[test]
    fn claude_import_strips_header_values() {
        let text = r#"{
            "mcpServers": {
                "remote": {
                    "type": "http",
                    "url": "https://example.invalid/mcp",
                    "headers": {"Authorization": "Bearer secret-token", "X-Tenant": "acme"}
                }
            }
        }"#;
        let records = from_claude_json("claude", text, "claude.json").expect("parse");
        assert_eq!(records[0].transport, "http");
        assert_eq!(records[0].header_keys, vec!["Authorization", "X-Tenant"]);
        let json = serde_json::to_string(&records).expect("json");
        assert!(!json.contains("secret-token"));
        assert!(!json.contains("acme"));
    }

    #[test]
    fn opencode_import_splits_command_array() {
        let text = r#"{
            "mcp": {
                "demo": {"type": "local", "command": ["demo", "mcp"], "environment": {"DEMO_KEY": "secret-value"}}
            }
        }"#;
        let records = from_opencode_json("opencode", text, "opencode.json").expect("parse");
        assert_eq!(records[0].command.as_deref(), Some("demo"));
        assert_eq!(records[0].args, vec!["mcp"]);
        assert_eq!(records[0].env_keys, vec!["DEMO_KEY"]);
        assert!(
            !serde_json::to_string(&records)
                .expect("json")
                .contains("secret-value")
        );
    }

    #[test]
    fn untrusted_commands_are_refused() {
        assert!(resolve_trusted(r"\\server\share\evil.exe").is_err());
        assert!(resolve_trusted(r"C:\Tools\..\..\evil.exe").is_err());
        assert!(resolve_trusted("definitely-not-a-command-anvil").is_err());
        assert!(resolve_trusted("").is_err());
    }

    #[test]
    fn trusted_bare_name_resolves_on_path() {
        let name = if cfg!(windows) { "cmd" } else { "sh" };
        assert!(resolve_trusted(name).is_ok());
    }

    #[test]
    fn empty_config_yields_empty_inventory() {
        assert!(
            from_codex_toml("codex", "model = \"x\"\n", "s")
                .expect("parse")
                .is_empty()
        );
        assert!(
            from_claude_json("claude", "{}", "s")
                .expect("parse")
                .is_empty()
        );
        assert!(
            from_opencode_json("opencode", "{}", "s")
                .expect("parse")
                .is_empty()
        );
    }

    #[test]
    fn malformed_config_is_a_format_error() {
        let error = from_claude_json("claude", "{not json", "s").expect_err("format");
        assert_eq!(error.code(), "format");
    }
}
