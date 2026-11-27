//! Pi provider accounts: the built-in providers, and the two files that say
//! whether each one is usable.
//!
//! Pi authenticates per provider, not per model. A credential lives either in
//! `~/.pi/agent/auth.json` (`{"<provider>": {"type": "api_key" | "oauth", …}}`)
//! or as an `apiKey` on a hand-written provider in `~/.pi/agent/models.json`.
//! The models page manages accounts here for the same reason pi does: a model
//! list without a credential is a list of models nobody can call.
//!
//! The catalogue below is the set of providers pi 1.6 ships, with the
//! authentication each one accepts. It is a copy on purpose: reading it out of
//! the installed bundle would mean loading a Node module from the app, and the
//! answer changes only when pi's own release notes say so. `pi auth check
//! --provider <id>` remains the authority on whether a credential resolves.

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};

use crate::error::{Error, Result};

/// One extra value a provider needs beside the key.
///
/// Cloudflare wants an account (and gateway) id, Azure a resource URL, Vertex a
/// project and location. Pi reads them from the credential's `env` object, so
/// the form writes them there instead of inventing a second store.
#[derive(Debug, Clone, Copy, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExtraField {
    /// Environment variable pi reads the value from.
    pub env: &'static str,
    /// What the field is, for the form label.
    pub label: &'static str,
    pub required: bool,
}

/// A provider pi ships with.
#[derive(Debug, Clone, Copy)]
pub struct ProviderSpec {
    pub id: &'static str,
    pub name: &'static str,
    pub base_url: Option<&'static str>,
    /// Default API protocol, used to prefill a custom provider.
    pub api: &'static str,
    /// Environment variable pi falls back to when no credential is stored.
    pub key_env: Option<&'static str>,
    /// False for providers authenticated only by ambient cloud credentials.
    pub supports_key: bool,
    /// Label of the OAuth option; `None` means pi cannot sign in to it.
    pub oauth_label: Option<&'static str>,
    /// The OAuth option is backed by a paid subscription.
    pub oauth_subscription: bool,
    pub extra: &'static [ExtraField],
}

const CF_ACCOUNT: ExtraField = ExtraField {
    env: "CLOUDFLARE_ACCOUNT_ID",
    label: "Account ID",
    required: true,
};
const CF_GATEWAY: ExtraField = ExtraField {
    env: "CLOUDFLARE_GATEWAY_ID",
    label: "Gateway ID",
    required: true,
};
const AZURE_BASE: ExtraField = ExtraField {
    env: "AZURE_OPENAI_BASE_URL",
    label: "Resource URL",
    required: false,
};
const GCP_PROJECT: ExtraField = ExtraField {
    env: "GOOGLE_CLOUD_PROJECT",
    label: "Project ID",
    required: false,
};
const GCP_LOCATION: ExtraField = ExtraField {
    env: "GOOGLE_CLOUD_LOCATION",
    label: "Location",
    required: false,
};
const AWS_REGION: ExtraField = ExtraField {
    env: "AWS_REGION",
    label: "Region",
    required: false,
};

/// Pi 1.6's built-in providers, ordered as pi registers them.
///
/// Only providers pi can sign in to carry `oauth_label`; a provider with
/// neither OAuth nor `supports_key` (none today) would be ambient-only.
pub const PROVIDERS: &[ProviderSpec] = &[
    ProviderSpec {
        id: "amazon-bedrock",
        name: "Amazon Bedrock",
        base_url: None,
        api: "bedrock-converse-stream",
        key_env: Some("AWS_BEARER_TOKEN_BEDROCK"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[AWS_REGION],
    },
    ProviderSpec {
        id: "ant-ling",
        name: "Ant Ling",
        base_url: Some("https://api.ant-ling.com/v1"),
        api: "openai-completions",
        key_env: Some("ANT_LING_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "anthropic",
        name: "Anthropic",
        base_url: Some("https://api.anthropic.com"),
        api: "anthropic-messages",
        key_env: Some("ANTHROPIC_API_KEY"),
        supports_key: true,
        oauth_label: Some("Claude Pro/Max"),
        oauth_subscription: true,
        extra: &[],
    },
    ProviderSpec {
        id: "azure",
        name: "Azure OpenAI",
        base_url: None,
        api: "openai-completions",
        key_env: Some("AZURE_OPENAI_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[AZURE_BASE],
    },
    ProviderSpec {
        id: "baseten",
        name: "Baseten",
        base_url: Some("https://inference.baseten.co/v1"),
        api: "openai-completions",
        key_env: Some("BASETEN_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "cerebras",
        name: "Cerebras",
        base_url: Some("https://api.cerebras.ai/v1"),
        api: "openai-completions",
        key_env: Some("CEREBRAS_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "cloudflare-ai-gateway",
        name: "Cloudflare AI Gateway",
        base_url: None,
        api: "openai-completions",
        key_env: Some("CLOUDFLARE_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[CF_ACCOUNT, CF_GATEWAY],
    },
    ProviderSpec {
        id: "cloudflare-workers-ai",
        name: "Cloudflare Workers AI",
        base_url: None,
        api: "openai-completions",
        key_env: Some("CLOUDFLARE_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[CF_ACCOUNT],
    },
    ProviderSpec {
        id: "deepseek",
        name: "DeepSeek",
        base_url: Some("https://api.deepseek.com"),
        api: "openai-completions",
        key_env: Some("DEEPSEEK_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "fireworks",
        name: "Fireworks",
        base_url: Some("https://api.fireworks.ai/inference/v1"),
        api: "openai-completions",
        key_env: Some("FIREWORKS_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "github-copilot",
        name: "GitHub Copilot",
        base_url: None,
        api: "openai-completions",
        key_env: Some("COPILOT_GITHUB_TOKEN"),
        supports_key: true,
        oauth_label: Some("GitHub Copilot"),
        oauth_subscription: true,
        extra: &[],
    },
    ProviderSpec {
        id: "google",
        name: "Google Gemini",
        base_url: None,
        api: "google-generative-ai",
        key_env: Some("GEMINI_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "google-vertex",
        name: "Google Vertex AI",
        base_url: None,
        api: "google-vertex",
        key_env: Some("GOOGLE_CLOUD_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[GCP_PROJECT, GCP_LOCATION],
    },
    ProviderSpec {
        id: "groq",
        name: "Groq",
        base_url: Some("https://api.groq.com/openai/v1"),
        api: "openai-completions",
        key_env: Some("GROQ_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "huggingface",
        name: "Hugging Face",
        base_url: None,
        api: "openai-completions",
        key_env: Some("HF_TOKEN"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "kimi-coding",
        name: "Kimi For Coding",
        base_url: None,
        api: "openai-completions",
        key_env: Some("KIMI_API_KEY"),
        supports_key: true,
        oauth_label: Some("Kimi Code"),
        oauth_subscription: true,
        extra: &[],
    },
    ProviderSpec {
        id: "meta",
        name: "Meta",
        base_url: None,
        api: "openai-responses",
        key_env: Some("META_API_KEY"),
        supports_key: true,
        oauth_label: Some("Meta"),
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "minimax",
        name: "MiniMax",
        base_url: None,
        api: "anthropic-messages",
        key_env: Some("MINIMAX_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "minimax-cn",
        name: "MiniMax (China)",
        base_url: None,
        api: "anthropic-messages",
        key_env: Some("MINIMAX_CN_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "mistral",
        name: "Mistral",
        base_url: Some("https://api.mistral.ai"),
        api: "mistral-conversations",
        key_env: Some("MISTRAL_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "moonshotai",
        name: "Moonshot AI",
        base_url: None,
        api: "openai-completions",
        key_env: Some("MOONSHOT_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "moonshotai-cn",
        name: "Moonshot AI (China)",
        base_url: None,
        api: "openai-completions",
        key_env: Some("MOONSHOT_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "nvidia",
        name: "NVIDIA NIM",
        base_url: None,
        api: "openai-completions",
        key_env: Some("NVIDIA_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "openai",
        name: "OpenAI",
        base_url: Some("https://api.openai.com/v1"),
        api: "openai-responses",
        key_env: Some("OPENAI_API_KEY"),
        supports_key: true,
        oauth_label: Some("Sign in with ChatGPT"),
        oauth_subscription: true,
        extra: &[],
    },
    ProviderSpec {
        id: "openai-codex",
        name: "OpenAI Codex",
        base_url: None,
        api: "openai-codex-responses",
        key_env: None,
        supports_key: false,
        oauth_label: Some("ChatGPT Plus/Pro"),
        oauth_subscription: true,
        extra: &[],
    },
    ProviderSpec {
        id: "opencode",
        name: "OpenCode Zen",
        base_url: None,
        api: "openai-completions",
        key_env: Some("OPENCODE_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "opencode-go",
        name: "OpenCode Go",
        base_url: None,
        api: "openai-completions",
        key_env: Some("OPENCODE_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "openrouter",
        name: "OpenRouter",
        base_url: Some("https://openrouter.ai/api/v1"),
        api: "openai-completions",
        key_env: Some("OPENROUTER_API_KEY"),
        supports_key: true,
        oauth_label: Some("Sign in with OpenRouter"),
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "qwen-token-plan",
        name: "Qwen Token Plan Team",
        base_url: None,
        api: "openai-completions",
        key_env: Some("QWEN_TOKEN_PLAN_TEAM_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "qwen-token-plan-cn",
        name: "Qwen Token Plan Team (China)",
        base_url: None,
        api: "openai-completions",
        key_env: Some("QWEN_TOKEN_PLAN_TEAM_CN_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "qwen-token-plan-individual",
        name: "Qwen Token Plan Individual",
        base_url: None,
        api: "openai-completions",
        key_env: Some("QWEN_TOKEN_PLAN_INDIVIDUAL_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "qwen-token-plan-individual-cn",
        name: "Qwen Token Plan Individual (China)",
        base_url: None,
        api: "openai-completions",
        key_env: Some("QWEN_TOKEN_PLAN_INDIVIDUAL_CN_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "radius",
        name: "Radius",
        base_url: None,
        api: "pi-messages",
        key_env: Some("RADIUS_API_KEY"),
        supports_key: true,
        oauth_label: Some("Radius"),
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "together",
        name: "Together AI",
        base_url: Some("https://api.together.xyz/v1"),
        api: "openai-completions",
        key_env: Some("TOGETHER_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "typesafe",
        name: "TypeSafe",
        base_url: None,
        api: "openai-completions",
        key_env: Some("TYPESAFE_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "vercel-ai-gateway",
        name: "Vercel AI Gateway",
        base_url: None,
        api: "openai-completions",
        key_env: Some("AI_GATEWAY_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "xai",
        name: "xAI",
        base_url: Some("https://api.x.ai/v1"),
        api: "openai-responses",
        key_env: Some("XAI_API_KEY"),
        supports_key: true,
        oauth_label: Some("SuperGrok or X Premium"),
        oauth_subscription: true,
        extra: &[],
    },
    ProviderSpec {
        id: "xiaomi",
        name: "Xiaomi MiMo",
        base_url: None,
        api: "openai-completions",
        key_env: Some("XIAOMI_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "xiaomi-token-plan-ams",
        name: "Xiaomi MiMo Token Plan (Amsterdam)",
        base_url: None,
        api: "openai-completions",
        key_env: Some("XIAOMI_TOKEN_PLAN_AMS_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "xiaomi-token-plan-cn",
        name: "Xiaomi MiMo Token Plan (China)",
        base_url: None,
        api: "openai-completions",
        key_env: Some("XIAOMI_TOKEN_PLAN_CN_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "xiaomi-token-plan-sgp",
        name: "Xiaomi MiMo Token Plan (Singapore)",
        base_url: None,
        api: "openai-completions",
        key_env: Some("XIAOMI_TOKEN_PLAN_SGP_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "zai",
        name: "Z.AI Coding Plan",
        base_url: None,
        api: "openai-completions",
        key_env: Some("ZAI_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
    ProviderSpec {
        id: "zai-coding-cn",
        name: "Z.AI Coding Plan (China)",
        base_url: None,
        api: "openai-completions",
        key_env: Some("ZAI_CODING_CN_API_KEY"),
        supports_key: true,
        oauth_label: None,
        oauth_subscription: false,
        extra: &[],
    },
];

/// Look one spec up by id.
pub fn spec(id: &str) -> Option<&'static ProviderSpec> {
    PROVIDERS.iter().find(|provider| provider.id == id)
}

/// API protocols a custom provider may declare, in the order the form offers
/// them. Anything else pi supports is an extension-level concern.
pub const CUSTOM_APIS: &[&str] = &[
    "openai-completions",
    "openai-responses",
    "anthropic-messages",
    "google-generative-ai",
];

/// One provider as the models page sees it.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProviderAccount {
    pub id: String,
    pub name: String,
    pub base_url: Option<String>,
    pub api: String,
    pub key_env: Option<String>,
    pub supports_key: bool,
    pub oauth_label: Option<String>,
    pub oauth_subscription: bool,
    pub extra: Vec<ExtraField>,
    /// `oauth`, `api_key`, or `None` when nothing is stored for it.
    pub credential: Option<String>,
    /// `builtin` for pi's own providers, `custom` for a `models.json` entry.
    pub source: String,
    /// A `baseUrl` written in `models.json` for this provider (a proxy, say).
    pub base_url_override: Option<String>,
    /// Masked tail of the stored key, so the page can say which key is in use
    /// without ever shipping the key itself to the webview.
    pub key_hint: Option<String>,
    /// Models the `models.json` entry declares (custom providers only).
    pub models: Vec<String>,
}

/// The catalogue plus what is stored for it right now.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProviderSnapshot {
    pub accounts: Vec<ProviderAccount>,
    pub auth_path: String,
    pub models_path: String,
    /// Protocols a custom provider may use.
    pub custom_apis: Vec<String>,
    /// Files that exist but could not be read. A credential store that does not
    /// parse would otherwise look like "nothing is configured", and the next
    /// write would overwrite it.
    pub warnings: Vec<String>,
}

/// Pi's agent directory.
///
/// Precedence is the one the host applies at startup: the bundled runtime wins
/// (ADR 0009 — its `PI_CODING_AGENT_DIR` is the Anvil data directory), then an
/// explicit `PI_CODING_AGENT_DIR`, then `~/.pi/agent`.
pub fn config_dir(home: &Path) -> PathBuf {
    if let Some(bundle) = crate::runtime::bundled::active() {
        return bundle.agent_dir.clone();
    }
    if let Some(dir) = std::env::var_os("PI_CODING_AGENT_DIR")
        && !dir.is_empty()
    {
        return PathBuf::from(dir);
    }
    home.join(".pi").join("agent")
}

pub fn auth_file(home: &Path) -> PathBuf {
    config_dir(home).join("auth.json")
}

pub fn models_file(home: &Path) -> PathBuf {
    config_dir(home).join("models.json")
}

/// Read a JSON object file, or an empty one when it is missing.
///
/// A file that exists but does not parse is an error, not an empty object:
/// treating it as empty would let the next write replace a credential store the
/// user still needs — and the symptom would be every provider silently logging
/// out. `common::read_json` swallows parse errors, which is right for a read-only
/// inventory and wrong for a read-modify-write.
fn read_object(path: &Path, limit: usize) -> Result<Map<String, Value>> {
    let metadata = match std::fs::metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Map::new()),
        Err(error) => return Err(error.into()),
    };
    if metadata.len() as usize > limit {
        return Err(Error::Rejected(format!(
            "{} is larger than {limit} bytes; refusing to read it",
            path.display()
        )));
    }
    let text = std::fs::read_to_string(path)?;
    if text.trim().is_empty() {
        return Ok(Map::new());
    }
    match serde_json::from_str::<Value>(&text) {
        Ok(Value::Object(map)) => Ok(map),
        Ok(_) => Err(Error::Rejected(format!(
            "{} is not a JSON object; fix or remove it first",
            path.display()
        ))),
        Err(error) => Err(Error::Rejected(format!(
            "{} does not parse ({error}); fix or remove it first",
            path.display()
        ))),
    }
}

/// Write JSON the way pi does: two-space indent, trailing newline, and a
/// temp file renamed over the original so a crash cannot truncate a key store.
fn write_object(path: &Path, value: &Map<String, Value>) -> Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let mut text = serde_json::to_string_pretty(&Value::Object(value.clone()))
        .map_err(|error| Error::Rejected(format!("encode {}: {error}", path.display())))?;
    text.push('\n');
    let tmp = path.with_extension("json.anvil-tmp");
    std::fs::write(&tmp, text.as_bytes())?;
    restrict(&tmp)?;
    std::fs::rename(&tmp, path)?;
    restrict(path)?;
    Ok(())
}

/// Keep a credential file readable by its owner only (no-op on Windows, where
/// the file inherits the profile's ACL).
fn restrict(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mut permissions = std::fs::metadata(path)?.permissions();
        permissions.set_mode(0o600);
        std::fs::set_permissions(path, permissions)?;
    }
    #[cfg(not(unix))]
    {
        let _ = path;
    }
    Ok(())
}

/// Mask a key for display: provider keys are long enough that the last four
/// characters identify one without being usable.
fn key_hint(key: &str) -> String {
    let trimmed = key.trim();
    let tail: String = trimmed
        .chars()
        .rev()
        .take(4)
        .collect::<Vec<_>>()
        .into_iter()
        .rev()
        .collect();
    if trimmed.chars().count() <= 8 {
        "••••".to_string()
    } else {
        format!("••••{tail}")
    }
}

/// The whole catalogue with the credentials currently stored.
pub fn snapshot(home: &Path) -> ProviderSnapshot {
    let mut warnings = Vec::new();
    let auth = match read_object(&auth_file(home), 1 << 20) {
        Ok(map) => map,
        Err(error) => {
            warnings.push(error.to_string());
            Map::new()
        }
    };
    let models = match read_object(&models_file(home), 8 << 20) {
        Ok(map) => map,
        Err(error) => {
            warnings.push(error.to_string());
            Map::new()
        }
    };
    let configured = models
        .get("providers")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();

    let mut accounts: Vec<ProviderAccount> = PROVIDERS
        .iter()
        .map(|spec| {
            let stored = auth.get(spec.id);
            let credential = stored
                .and_then(|value| value.get("type"))
                .and_then(Value::as_str)
                .map(str::to_string);
            let hint = stored
                .and_then(|value| value.get("key"))
                .and_then(Value::as_str)
                .map(key_hint);
            let entry = configured.get(spec.id);
            ProviderAccount {
                id: spec.id.to_string(),
                name: spec.name.to_string(),
                base_url: spec.base_url.map(str::to_string),
                api: spec.api.to_string(),
                key_env: spec.key_env.map(str::to_string),
                supports_key: spec.supports_key,
                oauth_label: spec.oauth_label.map(str::to_string),
                oauth_subscription: spec.oauth_subscription,
                extra: spec.extra.to_vec(),
                credential,
                source: "builtin".to_string(),
                base_url_override: entry
                    .and_then(|value| value.get("baseUrl"))
                    .and_then(Value::as_str)
                    .map(str::to_string),
                key_hint: hint,
                models: Vec::new(),
            }
        })
        .collect();

    // A provider pi does not ship with is a `models.json` entry. Those are
    // accounts too: a custom endpoint is authenticated by the `apiKey` next to
    // it, and pi never records that in `auth.json`.
    for (id, entry) in &configured {
        if spec(id).is_some() {
            continue;
        }
        let key = entry
            .get("apiKey")
            .or_else(|| entry.get("api_key"))
            .and_then(Value::as_str);
        let models: Vec<String> = entry
            .get("models")
            .and_then(Value::as_array)
            .map(|items| {
                items
                    .iter()
                    .filter_map(|item| item.get("id").and_then(Value::as_str))
                    .map(str::to_string)
                    .collect()
            })
            .unwrap_or_default();
        accounts.push(ProviderAccount {
            id: id.clone(),
            name: entry
                .get("name")
                .and_then(Value::as_str)
                .map(str::to_string)
                .unwrap_or_else(|| id.clone()),
            base_url: entry
                .get("baseUrl")
                .and_then(Value::as_str)
                .map(str::to_string),
            api: entry
                .get("api")
                .and_then(Value::as_str)
                .unwrap_or("openai-completions")
                .to_string(),
            key_env: None,
            supports_key: true,
            oauth_label: None,
            oauth_subscription: false,
            extra: Vec::new(),
            credential: key.filter(|value| !value.trim().is_empty()).map(|_| "api_key".to_string()),
            source: "custom".to_string(),
            base_url_override: None,
            key_hint: key.map(key_hint),
            models,
        });
    }

    ProviderSnapshot {
        accounts,
        auth_path: auth_file(home).display().to_string(),
        models_path: models_file(home).display().to_string(),
        custom_apis: CUSTOM_APIS.iter().map(|api| api.to_string()).collect(),
        warnings,
    }
}

/// Store a provider credential exactly as pi's own sign-in flow returns it.
///
/// The models page writes API keys itself (the shape is two fields), but a
/// browser sign-in produces a token bundle only the flow itself can author —
/// refresh token, expiry, per-provider extras. Those arrive here as the flow's
/// JSON and are stored verbatim: reshaping them would break pi's refresh.
pub fn store_credential(home: &Path, provider_id: &str, credential: Value) -> Result<()> {
    let id = provider_id.trim();
    if id.is_empty() {
        return Err(Error::Rejected("provider id must not be empty".into()));
    }
    let Value::Object(credential) = credential else {
        return Err(Error::Rejected(
            "a credential must be a JSON object".into(),
        ));
    };
    match credential.get("type").and_then(Value::as_str) {
        Some("api_key") | Some("oauth") => {}
        _ => {
            return Err(Error::Rejected(
                "a credential must carry type \"api_key\" or \"oauth\"".into(),
            ))
        }
    }
    let mut auth = read_object(&auth_file(home), 1 << 20)?;
    auth.insert(id.to_string(), Value::Object(credential));
    write_object(&auth_file(home), &auth)
}

/// Store an API key for a provider, replacing whatever pi had for it.
///
/// This writes the same shape `pi auth login --api-key` writes, so the terminal
/// and the app share one credential: `{"type": "api_key", "key": …, "env": {…}}`.
pub fn set_key(home: &Path, provider_id: &str, key: &str, env: &[(String, String)]) -> Result<()> {
    let id = provider_id.trim();
    if id.is_empty() {
        return Err(Error::Rejected("provider id must not be empty".into()));
    }
    if key.trim().is_empty() {
        return Err(Error::Rejected("API key must not be empty".into()));
    }
    if id.starts_with('.') || id.contains(char::is_whitespace) {
        return Err(Error::Rejected(format!("provider id {id:?} is not usable")));
    }
    let mut auth = read_object(&auth_file(home), 1 << 20)?;
    let mut credential = Map::new();
    credential.insert("type".to_string(), Value::String("api_key".into()));
    credential.insert("key".to_string(), Value::String(key.trim().to_string()));
    let mut extra = Map::new();
    for (name, value) in env {
        let name = name.trim();
        let value = value.trim();
        if name.is_empty() || value.is_empty() {
            continue;
        }
        extra.insert(name.to_string(), Value::String(value.to_string()));
    }
    if !extra.is_empty() {
        credential.insert("env".to_string(), Value::Object(extra));
    }
    auth.insert(id.to_string(), Value::Object(credential));
    write_object(&auth_file(home), &auth)
}

/// Remove a provider's stored credential.
///
/// Environment variables and `models.json` are untouched, exactly like
/// `pi auth logout`: this only forgets what pi stored.
pub fn logout(home: &Path, provider_id: &str) -> Result<bool> {
    let mut auth = read_object(&auth_file(home), 1 << 20)?;
    let removed = auth.remove(provider_id.trim()).is_some();
    if removed {
        write_object(&auth_file(home), &auth)?;
    }
    Ok(removed)
}

/// One custom provider as the form submits it.
#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CustomProviderDraft {
    pub id: String,
    #[serde(default)]
    pub name: Option<String>,
    pub base_url: String,
    pub api: String,
    #[serde(default)]
    pub api_key: Option<String>,
    #[serde(default)]
    pub models: Vec<String>,
}

/// Create or replace a custom provider in `models.json`.
///
/// A provider pi already ships with is *not* replaced: it gets only its
/// `baseUrl` overridden, which is how pi routes a built-in provider through a
/// proxy without losing its model list or its authentication.
pub fn save_custom_provider(home: &Path, draft: &CustomProviderDraft) -> Result<()> {
    let id = draft.id.trim();
    if id.is_empty() {
        return Err(Error::Rejected("provider id must not be empty".into()));
    }
    if id.starts_with('.') || id.contains(char::is_whitespace) {
        return Err(Error::Rejected(format!(
            "provider id {id:?} must be a slug without spaces"
        )));
    }
    let base_url = draft.base_url.trim();
    let lowered = base_url.to_ascii_lowercase();
    if !(lowered.starts_with("https://") || lowered.starts_with("http://")) {
        return Err(Error::Rejected(format!(
            "endpoint {base_url:?} must be an http(s) URL"
        )));
    }
    let api = draft.api.trim();
    if api.is_empty() {
        return Err(Error::Rejected("API protocol must not be empty".into()));
    }
    let mut models = Vec::new();
    for model in &draft.models {
        let model = model.trim();
        if model.is_empty() || models.iter().any(|seen: &String| seen == model) {
            continue;
        }
        if model.contains(char::is_whitespace) {
            return Err(Error::Rejected(format!(
                "model id {model:?} must not contain whitespace"
            )));
        }
        models.push(model.to_string());
    }
    if models.is_empty() {
        return Err(Error::Rejected(
            "at least one model id is needed: pi lists providers through their models".into(),
        ));
    }

    let mut root = read_object(&models_file(home), 8 << 20)?;
    let providers = root
        .entry("providers".to_string())
        .or_insert_with(|| Value::Object(Map::new()));
    if !providers.is_object() {
        return Err(Error::Rejected(
            "models.json has a non-object \"providers\" entry; fix it first".into(),
        ));
    }
    let providers = providers.as_object_mut().expect("checked above");
    let is_builtin = spec(id).is_some();

    let entry = providers
        .entry(id.to_string())
        .or_insert_with(|| Value::Object(Map::new()));
    if !entry.is_object() {
        return Err(Error::Rejected(format!(
            "models.json provider {id:?} is not an object; fix it first"
        )));
    }
    let entry = entry.as_object_mut().expect("checked above");
    entry.insert("baseUrl".to_string(), Value::String(base_url.to_string()));

    if is_builtin {
        // Keep pi's models and auth: only the endpoint moves. Writing `models`
        // here would replace the built-in list with the one typed in the form.
        if let Some(name) = draft.name.as_deref().map(str::trim) {
            if !name.is_empty() {
                entry.insert("name".to_string(), Value::String(name.to_string()));
            }
        }
        return write_object(&models_file(home), &root);
    }

    entry.insert("api".to_string(), Value::String(api.to_string()));
    if let Some(key) = draft.api_key.as_deref().map(str::trim) {
        if !key.is_empty() {
            entry.insert("apiKey".to_string(), Value::String(key.to_string()));
        }
    }
    entry.insert(
        "models".to_string(),
        Value::Array(
            models
                .into_iter()
                .map(|model| json!({ "id": model }))
                .collect(),
        ),
    );
    write_object(&models_file(home), &root)
}

/// Remove a custom provider from `models.json`.
///
/// A built-in id is rejected: deleting pi's own provider entry would be a lie
/// about what pi supports. Use [`logout`] to drop its credential instead.
pub fn remove_custom_provider(home: &Path, provider_id: &str) -> Result<bool> {
    if spec(provider_id).is_some() {
        return Err(Error::Rejected(format!(
            "{provider_id:?} is a built-in provider; log out of it instead"
        )));
    }
    let mut root = read_object(&models_file(home), 8 << 20)?;
    let Some(providers) = root.get_mut("providers").and_then(Value::as_object_mut) else {
        return Ok(false);
    };
    let removed = providers.remove(provider_id).is_some();
    if removed {
        write_object(&models_file(home), &root)?;
    }
    Ok(removed)
}

/// Which providers pi would report as signed in, for callers that only need
/// the set of ids (the runtime status probe).
///
/// `home` is the *user* home; which agent directory that maps to is decided by
/// [`config_dir`]. A caller that already knows the agent directory (the bundled
/// runtime) uses [`signed_in_at`] so it cannot read a different store than the
/// pi it is about to start.
pub fn signed_in(home: &Path) -> Vec<String> {
    signed_in_at(&config_dir(home))
}

/// Same question, asked of one agent directory.
pub fn signed_in_at(agent_dir: &Path) -> Vec<String> {
    let mut ids: Vec<String> = read_object(&agent_dir.join("auth.json"), 1 << 20)
        .unwrap_or_default()
        .into_iter()
        .filter(|(_, value)| {
            value
                .get("type")
                .and_then(Value::as_str)
                .is_some_and(|kind| kind == "api_key" || kind == "oauth")
        })
        .map(|(id, _)| id)
        .collect();
    let configured = read_object(&agent_dir.join("models.json"), 8 << 20).unwrap_or_default();
    if let Some(providers) = configured.get("providers").and_then(Value::as_object) {
        for (id, entry) in providers {
            let has_key = entry
                .get("apiKey")
                .or_else(|| entry.get("api_key"))
                .and_then(Value::as_str)
                .is_some_and(|key| !key.trim().is_empty());
            if has_key && !ids.contains(id) {
                ids.push(id.clone());
            }
        }
    }
    ids.sort();
    ids
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "anvil-providers-{name}-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(dir.join(".pi/agent")).expect("scratch dir");
        dir
    }

    #[test]
    fn the_catalogue_covers_every_provider_pi_ships_without_duplicates() {
        assert_eq!(PROVIDERS.len(), 43, "pi 1.6 ships 43 chat providers");
        let mut ids: Vec<&str> = PROVIDERS.iter().map(|provider| provider.id).collect();
        ids.sort_unstable();
        ids.dedup();
        assert_eq!(ids.len(), PROVIDERS.len(), "duplicate provider id");
        for provider in PROVIDERS {
            assert!(!provider.name.is_empty(), "{} has no name", provider.id);
            assert!(
                provider.supports_key || provider.oauth_label.is_some(),
                "{} would be unusable: no key and no sign-in",
                provider.id
            );
        }
        let oauth: Vec<&str> = PROVIDERS
            .iter()
            .filter(|provider| provider.oauth_label.is_some())
            .map(|provider| provider.id)
            .collect();
        for expected in [
            "anthropic",
            "github-copilot",
            "openai",
            "openai-codex",
            "openrouter",
            "xai",
        ] {
            assert!(oauth.contains(&expected), "{expected} should sign in");
        }
    }

    #[test]
    fn a_stored_key_reads_back_as_an_api_key_account() {
        let home = scratch("set-key");
        set_key(&home, "deepseek", "sk-abcdefgh1234", &[]).expect("store key");

        let snapshot = snapshot(&home);
        let row = snapshot
            .accounts
            .iter()
            .find(|account| account.id == "deepseek")
            .expect("deepseek row");
        assert_eq!(row.credential.as_deref(), Some("api_key"));
        assert_eq!(row.source, "builtin");
        assert_eq!(row.key_hint.as_deref(), Some("••••1234"));
        assert!(row.supports_key);

        // The file pi reads must have pi's shape, not ours.
        let raw: Value =
            serde_json::from_str(&fs::read_to_string(auth_file(&home)).expect("read")).expect("json");
        assert_eq!(raw["deepseek"]["type"], "api_key");
        assert_eq!(raw["deepseek"]["key"], "sk-abcdefgh1234");
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn extra_fields_land_in_the_credential_env_where_pi_reads_them() {
        let home = scratch("extra-env");
        set_key(
            &home,
            "cloudflare-workers-ai",
            "cf-token-1234",
            &[
                ("CLOUDFLARE_ACCOUNT_ID".to_string(), "acc-1".to_string()),
                (" ".to_string(), "dropped".to_string()),
            ],
        )
        .expect("store key");
        let raw: Value =
            serde_json::from_str(&fs::read_to_string(auth_file(&home)).expect("read")).expect("json");
        assert_eq!(raw["cloudflare-workers-ai"]["env"]["CLOUDFLARE_ACCOUNT_ID"], "acc-1");
        assert_eq!(
            raw["cloudflare-workers-ai"]["env"]
                .as_object()
                .expect("env object")
                .len(),
            1,
            "an empty name must not be stored"
        );
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn a_second_key_does_not_erase_the_first() {
        let home = scratch("two-keys");
        set_key(&home, "deepseek", "sk-first0000", &[]).expect("first");
        set_key(&home, "groq", "gsk-second0000", &[]).expect("second");
        let snapshot = snapshot(&home);
        for id in ["deepseek", "groq"] {
            let row = snapshot
                .accounts
                .iter()
                .find(|account| account.id == id)
                .unwrap_or_else(|| panic!("{id} row"));
            assert!(row.credential.is_some(), "{id} lost its credential");
        }
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn logout_removes_only_that_provider() {
        let home = scratch("logout");
        set_key(&home, "deepseek", "sk-aaaa1111", &[]).expect("first");
        set_key(&home, "groq", "gsk-bbbb2222", &[]).expect("second");
        assert!(logout(&home, "deepseek").expect("logout"));
        let snapshot = snapshot(&home);
        let deepseek = snapshot
            .accounts
            .iter()
            .find(|account| account.id == "deepseek")
            .expect("row");
        let groq = snapshot
            .accounts
            .iter()
            .find(|account| account.id == "groq")
            .expect("row");
        assert!(deepseek.credential.is_none());
        assert_eq!(groq.credential.as_deref(), Some("api_key"));
        assert!(!logout(&home, "deepseek").expect("second logout"));
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn an_oauth_credential_is_reported_as_oauth_and_kept_verbatim() {
        let home = scratch("oauth");
        let auth = json!({
            "anthropic": {
                "type": "oauth",
                "refresh": "r",
                "access": "a",
                "expires": 1
            }
        });
        fs::write(auth_file(&home), auth.to_string()).expect("write auth");

        let snapshot = snapshot(&home);
        let row = snapshot
            .accounts
            .iter()
            .find(|account| account.id == "anthropic")
            .expect("row");
        assert_eq!(row.credential.as_deref(), Some("oauth"));
        assert!(row.key_hint.is_none(), "an OAuth token must not be echoed");

        // Storing a key for another provider must not touch the OAuth entry.
        set_key(&home, "groq", "gsk-cccc3333", &[]).expect("store");
        let raw: Value =
            serde_json::from_str(&fs::read_to_string(auth_file(&home)).expect("read")).expect("json");
        assert_eq!(raw["anthropic"]["type"], "oauth");
        assert_eq!(raw["anthropic"]["access"], "a");
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn a_custom_provider_is_written_in_pis_shape_and_listed_as_an_account() {
        let home = scratch("custom");
        save_custom_provider(
            &home,
            &CustomProviderDraft {
                id: "my-gateway".to_string(),
                name: Some("My Gateway".to_string()),
                base_url: "http://127.0.0.1:8080/v1".to_string(),
                api: "openai-completions".to_string(),
                api_key: Some("sk-local-9999".to_string()),
                models: vec!["qwen3-coder".to_string(), "qwen3-coder".to_string()],
            },
        )
        .expect("save custom");

        let raw: Value =
            serde_json::from_str(&fs::read_to_string(models_file(&home)).expect("read")).expect("json");
        assert_eq!(raw["providers"]["my-gateway"]["api"], "openai-completions");
        assert_eq!(
            raw["providers"]["my-gateway"]["baseUrl"],
            "http://127.0.0.1:8080/v1"
        );
        assert_eq!(
            raw["providers"]["my-gateway"]["models"]
                .as_array()
                .expect("models")
                .len(),
            1,
            "duplicate model ids collapse"
        );

        let snapshot = snapshot(&home);
        let row = snapshot
            .accounts
            .iter()
            .find(|account| account.id == "my-gateway")
            .expect("custom row");
        assert_eq!(row.source, "custom");
        assert_eq!(row.credential.as_deref(), Some("api_key"));
        assert_eq!(row.models, vec!["qwen3-coder".to_string()]);
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn a_builtin_provider_only_gets_its_base_url_overridden() {
        let home = scratch("builtin-override");
        fs::write(
            models_file(&home),
            json!({
                "providers": {
                    "anthropic": { "baseUrl": "https://proxy.example.com/v1" }
                }
            })
            .to_string(),
        )
        .expect("write models");

        save_custom_provider(
            &home,
            &CustomProviderDraft {
                id: "anthropic".to_string(),
                name: None,
                base_url: "https://proxy2.example.com/v1".to_string(),
                api: "openai-completions".to_string(),
                api_key: None,
                models: vec!["typo-model".to_string()],
            },
        )
        .expect("override");

        let raw: Value =
            serde_json::from_str(&fs::read_to_string(models_file(&home)).expect("read")).expect("json");
        let entry = &raw["providers"]["anthropic"];
        assert_eq!(entry["baseUrl"], "https://proxy2.example.com/v1");
        assert!(
            entry.get("models").is_none(),
            "a built-in provider's model list must not be replaced by the form's"
        );
        assert!(entry.get("api").is_none());

        let snapshot = snapshot(&home);
        let row = snapshot
            .accounts
            .iter()
            .find(|account| account.id == "anthropic")
            .expect("row");
        assert_eq!(row.source, "builtin");
        assert_eq!(
            row.base_url_override.as_deref(),
            Some("https://proxy2.example.com/v1")
        );
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn removing_a_custom_provider_keeps_the_others_and_refuses_builtins() {
        let home = scratch("remove-custom");
        for id in ["a-one", "b-two"] {
            save_custom_provider(
                &home,
                &CustomProviderDraft {
                    id: id.to_string(),
                    name: None,
                    base_url: "https://example.com/v1".to_string(),
                    api: "openai-completions".to_string(),
                    api_key: None,
                    models: vec!["m".to_string()],
                },
            )
            .expect("save");
        }
        assert!(remove_custom_provider(&home, "a-one").expect("remove"));
        let snapshot = snapshot(&home);
        assert!(snapshot.accounts.iter().all(|account| account.id != "a-one"));
        assert!(snapshot.accounts.iter().any(|account| account.id == "b-two"));
        let refused = remove_custom_provider(&home, "deepseek");
        assert!(refused.is_err(), "a built-in provider is not deletable");
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn invalid_input_is_rejected_before_anything_is_written() {
        let home = scratch("invalid");
        assert!(set_key(&home, "deepseek", "   ", &[]).is_err());
        assert!(set_key(&home, "", "sk-x", &[]).is_err());
        let bad_url = save_custom_provider(
            &home,
            &CustomProviderDraft {
                id: "nope".to_string(),
                name: None,
                base_url: "ftp://example.com".to_string(),
                api: "openai-completions".to_string(),
                api_key: None,
                models: vec!["m".to_string()],
            },
        );
        assert!(bad_url.is_err());
        let no_models = save_custom_provider(
            &home,
            &CustomProviderDraft {
                id: "nope".to_string(),
                name: None,
                base_url: "https://example.com/v1".to_string(),
                api: "openai-completions".to_string(),
                api_key: None,
                models: Vec::new(),
            },
        );
        assert!(no_models.is_err());
        assert!(
            !auth_file(&home).exists() && !models_file(&home).exists(),
            "a rejected write must not create a file"
        );
        let _ = fs::remove_dir_all(&home);
    }

    #[test]
    fn a_broken_auth_file_fails_loudly_instead_of_being_replaced() {
        let home = scratch("broken");
        fs::write(auth_file(&home), "{ not json").expect("write");
        let error = set_key(&home, "deepseek", "sk-x", &[]).expect_err("must fail");
        let text = error.to_string();
        assert!(
            text.contains("auth.json") || text.contains("not a JSON object"),
            "unexpected error: {text}"
        );
        assert_eq!(fs::read_to_string(auth_file(&home)).expect("read"), "{ not json");
        let _ = fs::remove_dir_all(&home);
    }
}
