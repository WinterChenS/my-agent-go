//! Codex adapter.
//!
//! Codex keeps `~/.codex/config.toml` (model, MCP servers), a cached model
//! catalogue in `~/.codex/models_cache.json`, credentials in
//! `~/.codex/auth.json` and skills under `~/.codex/skills`. The `app-server`
//! subcommand is the protocol surface a session will use later, so the probe
//! performs a real JSON-RPC `initialize` against it instead of only reading
//! `--version`.

use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

use crate::error::{Error, Result};
use crate::model::{
    Capabilities, CapabilityState, InventoryKind, McpRecord, ModelRecord, RuntimeCandidate,
    RuntimeRegistration, RuntimeStatus,
};
use crate::runtime::adapter::{
    DiscoveryContext, Inventory, ProbeContext, ProbeReport, RuntimeAdapter,
};
use crate::runtime::adapters::common;
use crate::runtime::version::Version;
use crate::{mcp, skills};

/// Minimum Codex version this build speaks to.
const MIN_VERSION: Version = Version {
    major: 0,
    minor: 100,
    patch: 0,
};

pub struct CodexAdapter;

impl CodexAdapter {
    fn config_dir(home: &Path) -> PathBuf {
        home.join(".codex")
    }

    fn config_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("config.toml")
    }

    fn auth_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("auth.json")
    }

    fn models_file(home: &Path) -> PathBuf {
        Self::config_dir(home).join("models_cache.json")
    }

    fn skills_root(home: &Path) -> PathBuf {
        Self::config_dir(home).join("skills")
    }

    fn install_candidates() -> Vec<PathBuf> {
        let mut paths = Vec::new();
        if let Some(prefix) = common::shared_prefix() {
            paths.push(common::shared_shim(&prefix, "codex"));
        }
        paths
    }

    fn read_models(home: &Path) -> Result<Vec<ModelRecord>> {
        let Some(value) = common::read_json(&Self::models_file(home), 16 << 20) else {
            return Ok(Vec::new());
        };
        let now = crate::clock::now_rfc3339();
        let mut models = Vec::new();
        let Some(items) = value.get("models").and_then(|value| value.as_array()) else {
            return Ok(Vec::new());
        };
        for item in items {
            let Some(slug) = item.get("slug").and_then(|value| value.as_str()) else {
                continue;
            };
            if models
                .iter()
                .any(|model: &ModelRecord| model.model_id == slug)
            {
                continue;
            }
            let display_name = item
                .get("display_name")
                .and_then(|value| value.as_str())
                .unwrap_or(slug);
            models.push(ModelRecord {
                enabled: false,
                runtime_id: "codex".to_string(),
                model_id: slug.to_string(),
                display_name: display_name.to_string(),
                origin: "discovered".to_string(),
                protocol: Some("openai-responses".to_string()),
                endpoint: None,
                verified: false,
                verified_at: None,
                is_default: false,
                updated_at: now.clone(),
                has_credential: false,
            });
        }
        Ok(models)
    }

    fn read_mcp(home: &Path) -> Result<Vec<McpRecord>> {
        let Some(text) = common::read_text(&Self::config_file(home), 4 << 20) else {
            return Ok(Vec::new());
        };
        mcp::from_codex_toml("codex", &text, "~/.codex/config.toml")
    }
}

/// One-shot JSON-RPC `initialize` against `codex app-server`.
///
/// Returns the `userAgent`/`codexHome` summary the server reports. The child is
/// killed on every path.
pub fn app_server_handshake(executable: &Path, timeout: Duration) -> Result<String> {
    let mut command_line = Command::new(executable);
    command_line
        .arg("app-server")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    crate::runtime::process::hide_console(&mut command_line);
    let mut child = command_line
        .spawn()
        .map_err(|error| Error::Io(format!("spawn app-server: {error}")))?;

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
        let initialize = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"anvil","version":"0.1.0"}}}"#;
        let _ = stdin.write_all(initialize.as_bytes());
        let _ = stdin.write_all(b"\n");
        let _ = stdin.flush();
    }

    let deadline = Instant::now() + timeout;
    let mut reply = None;
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
                if value.get("id").and_then(|id| id.as_i64()) != Some(1) {
                    continue;
                }
                if let Some(error) = value.get("error") {
                    reply = Some(Err(Error::Io(format!("initialize failed: {error}"))));
                } else if let Some(result) = value.get("result") {
                    let user_agent = result
                        .get("userAgent")
                        .and_then(|value| value.as_str())
                        .unwrap_or("unknown");
                    let codex_home = result
                        .get("codexHome")
                        .and_then(|value| value.as_str())
                        .unwrap_or("unknown");
                    reply = Some(Ok(format!("{user_agent} · {codex_home}")));
                }
                break;
            }
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                reply = Some(Err(Error::Io(
                    "app-server closed stdout before answering initialize".into(),
                )));
                break;
            }
        }
    }

    let _ = child.kill();
    let _ = child.wait();
    reply.unwrap_or_else(|| {
        Err(Error::Timeout(
            "app-server did not answer initialize".into(),
        ))
    })
}

impl RuntimeAdapter for CodexAdapter {
    fn id(&self) -> &'static str {
        "codex"
    }

    fn display_name(&self) -> &'static str {
        "Codex"
    }

    fn capabilities(&self) -> Capabilities {
        Capabilities {
            models: CapabilityState::Supported,
            custom_models: CapabilityState::Unsupported,
            skills: CapabilityState::Supported,
            tools: CapabilityState::Unverified,
            mcp: CapabilityState::Supported,
            sessions: CapabilityState::Supported,
        }
    }

    fn discover(&self, _context: &DiscoveryContext) -> Result<Vec<RuntimeCandidate>> {
        Ok(common::discover_installs(
            "codex",
            "codex",
            &Self::install_candidates(),
        ))
    }

    fn probe(&self, context: &ProbeContext, candidate: &RuntimeCandidate) -> Result<ProbeReport> {
        let probe = common::version_probe(&candidate.executable, context.timeout)?;
        let config_file = Self::config_file(&context.home);
        let config_path = if config_file.is_file() {
            Some(config_file)
        } else if Self::config_dir(&context.home).is_dir() {
            Some(Self::config_dir(&context.home))
        } else {
            None
        };

        let mut detail = None;
        let mut status = match probe.version {
            None => RuntimeStatus::ProbeFailed,
            Some(version) if !version.at_least(MIN_VERSION) => RuntimeStatus::VersionUnsupported,
            Some(_) if !common::has_credentials(&Self::auth_file(&context.home)) => {
                RuntimeStatus::Unauthenticated
            }
            Some(_) => RuntimeStatus::Available,
        };

        if status == RuntimeStatus::Available {
            // The app-server handshake is what a session will use; a runtime that
            // passes --version but cannot speak it is not usable.
            match app_server_handshake(&candidate.executable, Duration::from_secs(10)) {
                Ok(summary) => detail = Some(format!("app-server: {summary}")),
                Err(error) => {
                    status = RuntimeStatus::ProbeFailed;
                    detail = Some(format!("app-server handshake failed: {error}"));
                }
            }
        }
        if status == RuntimeStatus::VersionUnsupported {
            detail = Some(format!(
                "version {} is below the supported {}.{}.{}",
                probe.version.expect("version"),
                MIN_VERSION.major,
                MIN_VERSION.minor,
                MIN_VERSION.patch
            ));
        }
        if status == RuntimeStatus::Unauthenticated {
            detail = Some("no credential in ~/.codex/auth.json".into());
        }
        if status == RuntimeStatus::ProbeFailed && detail.is_none() {
            // `--version` itself failed: name the file, its exit status and the
            // first line it printed.
            detail = Some(probe.failure_note(&candidate.executable, context.timeout));
        }

        Ok(ProbeReport {
            version: probe.version.map(|value| value.to_string()),
            status,
            config_path,
            detail,
            latency_ms: probe.elapsed_ms,
        })
    }

    fn sync(
        &self,
        context: &ProbeContext,
        registration: &RuntimeRegistration,
        kind: InventoryKind,
    ) -> Option<Result<Inventory>> {
        match kind {
            InventoryKind::Models => Some(Self::read_models(&context.home).map(Inventory::Models)),
            InventoryKind::Skills => {
                let roots = [skills::SkillRoot::new(
                    &registration.runtime_id,
                    "codex",
                    Self::skills_root(&context.home),
                )];
                Some(skills::scan(&roots, &crate::clock::now_rfc3339()).map(Inventory::Skills))
            }
            InventoryKind::Mcp => Some(Self::read_mcp(&context.home).map(Inventory::Mcp)),
            InventoryKind::Tools => None,
        }
    }
}

/// Skills root, exposed for tests.
pub fn skills_root(home: &Path) -> PathBuf {
    CodexAdapter::skills_root(home)
}
