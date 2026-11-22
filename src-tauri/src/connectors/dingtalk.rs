//! DingTalk docs connector.
//!
//! Two things make DingTalk different from Notion:
//!
//! * the credential is an **enterprise app** (appKey/appSecret) exchanged for a
//!   two-hour access token, not a user token;
//! * almost every doc endpoint takes an `operatorId` — *whose* DingTalk
//!   identity is acting — and that person's own permissions decide what is
//!   visible.

use serde_json::{Value, json};
use std::sync::Mutex;
use std::time::{Duration, Instant};

use super::http::{self, HttpRequest};
use super::{Config, ProbeOutcome};

const BASE: &str = "https://api.dingtalk.com";

/// One token per appKey, shared across probes and calls in this process.
fn token_cache() -> &'static Mutex<Vec<(String, String, Instant)>> {
    static CACHE: std::sync::OnceLock<Mutex<Vec<(String, String, Instant)>>> =
        std::sync::OnceLock::new();
    CACHE.get_or_init(|| Mutex::new(Vec::new()))
}

fn access_token(settings: &Config) -> Result<String, String> {
    let app_key = settings.get("appKey");
    let app_secret = settings.get("appSecret");
    if app_key.is_empty() || app_secret.is_empty() {
        return Err("缺少 AppKey / AppSecret".to_string());
    }
    let cache = token_cache();
    if let Ok(guard) = cache.lock() {
        if let Some((_, token, _)) = guard.iter().find(|(key, _, expires)| {
            key == &app_key && *expires > Instant::now() + Duration::from_secs(30)
        }) {
            return Ok(token.clone());
        }
    }
    let body = json!({ "appKey": app_key, "appSecret": app_secret }).to_string();
    let response = http::send(
        &HttpRequest::new("POST", format!("{BASE}/v1.0/oauth2/accessToken"))
            .header("Content-Type", "application/json")
            .body(body)
            .timeout(20),
    )?;
    let value: Value = http::json_body(&response, "获取钉钉 accessToken")?;
    let token = value
        .get("accessToken")
        .and_then(Value::as_str)
        .ok_or_else(|| "钉钉没有返回 accessToken（检查 AppKey / AppSecret）".to_string())?
        .to_string();
    let expire = value
        .get("expireIn")
        .and_then(Value::as_u64)
        .unwrap_or(7200)
        .saturating_sub(120);
    if let Ok(mut guard) = cache.lock() {
        guard.retain(|(key, _, _)| key != &app_key);
        guard.push((
            app_key.clone(),
            token.clone(),
            Instant::now() + Duration::from_secs(expire),
        ));
    }
    Ok(token)
}

fn get(settings: &Config, path_and_query: &str) -> Result<Value, String> {
    let token = access_token(settings)?;
    let response = http::send(
        &HttpRequest::new("GET", format!("{BASE}{path_and_query}"))
            .header("x-acs-dingtalk-access-token", token)
            .timeout(30),
    )?;
    http::json_body(&response, "钉钉文档接口")
}

/// Exchange the app credentials for a token and confirm the operator can see
/// at least the doc search endpoint.
pub fn probe(settings: &Config) -> ProbeOutcome {
    let mut steps = Vec::new();
    if let Err(error) = access_token(settings) {
        return ProbeOutcome::failed(error);
    }
    steps.push("AppKey / AppSecret 有效，已换取 accessToken".to_string());
    let operator = settings.get("operatorId");
    if operator.is_empty() {
        return ProbeOutcome::failed("还没有填操作人 unionId".to_string());
    }
    let query = format!("/v1.0/doc/docs?operatorId={}&maxResults=1", encode(&operator));
    match get(settings, &query) {
        Ok(value) => {
            let count = documents(&value).len();
            steps.push(format!("以操作人身份调用文档搜索成功（返回 {count} 条）"));
            if count == 0 {
                steps.push("搜索没有结果：确认操作人对目标文档 / 知识库有权限".to_string());
            }
        }
        Err(error) => return ProbeOutcome::failed(format!("文档搜索失败：{error}")),
    }
    ProbeOutcome::ok("连接正常", steps)
}

fn encode(input: &str) -> String {
    let mut out = String::with_capacity(input.len());
    for byte in input.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(byte as char)
            }
            _ => out.push_str(&format!("%{byte:02X}")),
        }
    }
    out
}

/// DingTalk wraps lists differently per endpoint; find the array.
fn documents(value: &Value) -> Vec<Value> {
    for key in ["documents", "docs", "result", "data", "items"] {
        if let Some(rows) = value.get(key).and_then(Value::as_array) {
            return rows.clone();
        }
        if let Some(nested) = value.get(key).and_then(|inner| inner.get("documents")) {
            if let Some(rows) = nested.as_array() {
                return rows.clone();
            }
        }
    }
    Vec::new()
}

fn arg<'a>(params: &'a Value, key: &str) -> &'a str {
    params.get(key).and_then(Value::as_str).unwrap_or("").trim()
}

pub fn call(settings: &Config, tool: &str, params: &Value) -> Result<Value, String> {
    match tool {
        "dingtalk_search_docs" => search(settings, params),
        "dingtalk_read_doc" => read_doc(settings, params),
        other => Err(format!("未知的钉钉工具：{other}")),
    }
}

fn search(settings: &Config, params: &Value) -> Result<Value, String> {
    let keyword = arg(params, "query");
    let mut query = format!(
        "/v1.0/doc/docs?operatorId={}&maxResults=10",
        encode(&settings.get("operatorId"))
    );
    if !keyword.is_empty() {
        query.push_str(&format!("&keyword={}", encode(keyword)));
    }
    let workspace = settings.get("defaultWorkspaceId");
    if !workspace.is_empty() {
        query.push_str(&format!("&workspaceId={}", encode(&workspace)));
    }
    let value = get(settings, &query)?;
    let items: Vec<Value> = documents(&value)
        .into_iter()
        .map(|row| {
            json!({
                "docKey": row.get("docKey").or_else(|| row.get("dentryUuid")).and_then(Value::as_str).unwrap_or(""),
                "name": row.get("name").or_else(|| row.get("title")).and_then(Value::as_str).unwrap_or(""),
                "workspaceId": row.get("workspaceId").and_then(Value::as_str).unwrap_or(""),
                "updatedAt": row.get("updatedTime").or_else(|| row.get("modifiedTime")).and_then(Value::as_str).unwrap_or(""),
            })
        })
        .collect();
    let text = items
        .iter()
        .map(|item| {
            format!(
                "{} · {}",
                item["name"].as_str().unwrap_or("(无标题)"),
                item["docKey"].as_str().unwrap_or("")
            )
        })
        .collect::<Vec<_>>()
        .join("\n");
    Ok(json!({ "count": items.len(), "documents": items, "text": text }))
}

fn read_doc(settings: &Config, params: &Value) -> Result<Value, String> {
    let doc_key = arg(params, "doc_key");
    if doc_key.is_empty() {
        return Err("缺少 doc_key（可先用 dingtalk_search_docs 找到）".to_string());
    }
    let limit = settings.number("maxBlocks", 300).clamp(1, 1000);
    let query = format!(
        "/v1.0/doc/suites/documents/{}/blocks?operatorId={}&maxResults={limit}",
        encode(doc_key),
        encode(&settings.get("operatorId"))
    );
    let value = get(settings, &query)?;
    let blocks = value
        .get("blocks")
        .and_then(Value::as_array)
        .cloned()
        .or_else(|| {
            value
                .get("result")
                .and_then(|result| result.get("blocks"))
                .and_then(Value::as_array)
                .cloned()
        })
        .unwrap_or_default();
    let mut lines: Vec<String> = Vec::new();
    for block in &blocks {
        if lines.len() >= limit as usize {
            break;
        }
        if let Some(text) = block_text(block) {
            lines.push(text);
        }
    }
    let truncated = blocks.len() > lines.len();
    let text = format!(
        "{}{}",
        lines.join("\n"),
        if truncated { "\n…（已截断）" } else { "" }
    );
    Ok(json!({
        "docKey": doc_key,
        "blocks": lines.len(),
        "truncated": truncated,
        "text": text,
    }))
}

/// Blocks carry plain text in different shapes; take whichever is present.
fn block_text(block: &Value) -> Option<String> {
    for key in ["text", "content", "plainText"] {
        if let Some(text) = block.get(key).and_then(Value::as_str) {
            let trimmed = text.trim();
            if !trimmed.is_empty() {
                return Some(trimmed.to_string());
            }
        }
        if let Some(nested) = block.get(key).and_then(|inner| inner.get("text")) {
            if let Some(text) = nested.as_str() {
                let trimmed = text.trim();
                if !trimmed.is_empty() {
                    return Some(trimmed.to_string());
                }
            }
        }
    }
    if let Some(rows) = block.get("richText").and_then(Value::as_array) {
        let joined: String = rows
            .iter()
            .filter_map(|row| {
                row.get("text")
                    .and_then(Value::as_str)
                    .or_else(|| row.get("content").and_then(Value::as_str))
            })
            .collect::<Vec<_>>()
            .join("");
        if !joined.trim().is_empty() {
            return Some(joined.trim().to_string());
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lists_are_found_under_any_wrapper() {
        assert_eq!(
            documents(&json!({ "documents": [{ "name": "a" }] })).len(),
            1
        );
        assert_eq!(
            documents(&json!({ "result": { "documents": [{ "name": "b" }] } })).len(),
            1
        );
        assert_eq!(documents(&json!({ "documents": [] })).len(), 0);
    }

    #[test]
    fn block_text_accepts_the_shapes_dingtalk_uses() {
        assert_eq!(block_text(&json!({ "text": " hello " })), Some("hello".into()));
        assert_eq!(
            block_text(&json!({ "richText": [{ "text": "a" }, { "text": "b" }] })),
            Some("ab".into())
        );
        assert_eq!(block_text(&json!({ "blockType": "divider" })), None);
    }
}
