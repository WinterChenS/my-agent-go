//! Notion connector: search, read and create pages over the REST API.
//!
//! Notion versions its API through a request header. `2026-03-11` is the one
//! the reference implementation pinned after the data-source split, so the same
//! value is used here: a connector written against an older header would start
//! returning empty search results without any error.

use serde_json::{Value, json};

use super::http::{self, HttpRequest};
use super::{Config, ProbeOutcome};

const BASE: &str = "https://api.notion.com";
const VERSION: &str = "2026-03-11";

fn api(settings: &Config, method: &str, path: &str) -> HttpRequest {
    HttpRequest::new(method, format!("{BASE}{path}"))
        .header("Authorization", format!("Bearer {}", settings.get("token")))
        .header("Notion-Version", VERSION)
        .header("Content-Type", "application/json")
        .timeout(30)
}

fn call_json(request: HttpRequest, what: &str) -> Result<Value, String> {
    let response = http::send(&request)?;
    http::json_body(&response, what)
}

/// 401 / 403 from Notion almost always means "the page was never shared with
/// the integration", which the raw message does not say.
fn explain(error: String) -> String {
    if error.contains("HTTP 401") {
        return format!("{error}（令牌无效或已撤销）");
    }
    if error.contains("HTTP 403") || error.contains("HTTP 404") {
        return format!("{error}（页面没有授权给这个连接：在页面右上角「…」→「添加连接」里选中它）");
    }
    error
}

/// Verify the token and report what it can see.
pub fn probe(settings: &Config) -> ProbeOutcome {
    let mut steps = Vec::new();
    let me = match call_json(api(settings, "GET", "/v1/users/me"), "读取令牌信息") {
        Ok(value) => value,
        Err(error) => return ProbeOutcome::failed(explain(error)),
    };
    let kind = me.get("type").and_then(Value::as_str).unwrap_or("bot");
    let name = me.get("name").and_then(Value::as_str).unwrap_or("");
    let owner = if me
        .get("owner")
        .and_then(|owner| owner.get("type"))
        .and_then(Value::as_str)
        == Some("person")
    {
        "个人访问令牌（以本人权限工作）"
    } else {
        "集成令牌（需要把页面添加给这个连接）"
    };
    steps.push(format!(
        "令牌有效：{} · {owner}",
        if name.is_empty() { kind } else { name }
    ));

    let search = call_json(
        api(settings, "POST", "/v1/search").body(json!({ "page_size": 10 }).to_string()),
        "搜索页面",
    );
    match search {
        Ok(value) => {
            let count = value
                .get("results")
                .and_then(Value::as_array)
                .map_or(0, Vec::len);
            steps.push(format!("可访问的页面 / 数据库（前 10 条）中拿到 {count} 项"));
            if count == 0 {
                steps.push(
                    "一条都搜不到通常是没授权：集成令牌要在页面「…」→「添加连接」里选中".to_string(),
                );
            }
        }
        Err(error) => return ProbeOutcome::failed(explain(error)),
    }
    ProbeOutcome::ok("连接正常", steps)
}

/// Dispatch one tool call. `params` is the agent's JSON argument object.
pub fn call(settings: &Config, tool: &str, params: &Value) -> Result<Value, String> {
    match tool {
        "notion_search" => search(settings, params),
        "notion_read_page" => read_page(settings, params),
        "notion_create_page" => create_page(settings, params),
        other => Err(format!("未知的 Notion 工具：{other}")),
    }
}

fn arg<'a>(params: &'a Value, key: &str) -> &'a str {
    params.get(key).and_then(Value::as_str).unwrap_or("").trim()
}

/// Accept a bare id or a pasted Notion link (`...-<32 hex>`).
pub fn normalize_id(raw: &str) -> String {
    let trimmed = raw.trim().trim_end_matches('/');
    if let Some(index) = trimmed.rfind('-') {
        let tail = &trimmed[index + 1..];
        if tail.len() >= 32 && tail.chars().all(|c| c.is_ascii_hexdigit()) {
            return tail[..32].to_string();
        }
    }
    if trimmed.len() >= 32 {
        let candidate: String = trimmed.chars().filter(char::is_ascii_hexdigit).collect();
        let head: String = candidate.chars().take(32).collect();
        if head.len() == 32 {
            return head;
        }
    }
    trimmed.replace('-', "")
}

fn search(settings: &Config, params: &Value) -> Result<Value, String> {
    let query = arg(params, "query");
    let mut body = json!({ "page_size": 10 });
    if !query.is_empty() {
        body["query"] = json!(query);
    }
    let value = call_json(
        api(settings, "POST", "/v1/search").body(body.to_string()),
        "搜索页面",
    )
    .map_err(explain)?;
    let items: Vec<Value> = value
        .get("results")
        .and_then(Value::as_array)
        .map(|rows| {
            rows.iter()
                .map(|row| {
                    json!({
                        "id": row.get("id").and_then(Value::as_str).unwrap_or(""),
                        "type": row.get("object").and_then(Value::as_str).unwrap_or(""),
                        "title": title_of(row),
                        "url": row.get("url").and_then(Value::as_str).unwrap_or(""),
                        "lastEdited": row.get("last_edited_time").and_then(Value::as_str).unwrap_or(""),
                    })
                })
                .collect()
        })
        .unwrap_or_default();
    Ok(json!({
        "count": items.len(),
        "results": items,
        "text": items.iter().map(|item| {
            format!("{} · {} · {}", item["title"].as_str().unwrap_or("(无标题)"), item["type"].as_str().unwrap_or(""), item["id"].as_str().unwrap_or(""))
        }).collect::<Vec<_>>().join("\n"),
    }))
}

fn title_of(row: &Value) -> String {
    if let Some(title) = row
        .get("title")
        .and_then(Value::as_array)
        .and_then(|parts| parts.first())
        .and_then(|part| part.get("plain_text"))
        .and_then(Value::as_str)
    {
        return title.to_string();
    }
    // Pages keep their title in properties; database rows in `name`.
    if let Some(name) = row.get("name").and_then(Value::as_str) {
        return name.to_string();
    }
    if let Some(properties) = row.get("properties").and_then(Value::as_object) {
        for (key, value) in properties {
            if key == "title" || value.get("type").and_then(Value::as_str) == Some("title") {
                if let Some(parts) = value.get("title").and_then(Value::as_array) {
                    let text: String = parts
                        .iter()
                        .filter_map(|part| part.get("plain_text").and_then(Value::as_str))
                        .collect();
                    if !text.is_empty() {
                        return text;
                    }
                }
            }
        }
    }
    String::new()
}

fn read_page(settings: &Config, params: &Value) -> Result<Value, String> {
    let raw = arg(params, "page_id");
    if raw.is_empty() {
        return Err("缺少 page_id（可先用 notion_search 找）".to_string());
    }
    let page_id = normalize_id(raw);
    let page = call_json(
        api(settings, "GET", &format!("/v1/pages/{page_id}")),
        "读取页面",
    )
    .map_err(explain)?;
    let title = title_of(&page);
    let limit = settings.number("maxBlocksPerPage", 200).max(1);

    let mut blocks: Vec<String> = Vec::new();
    let mut cursor: Option<String> = None;
    let mut truncated = false;
    loop {
        let mut path = format!(
            "/v1/blocks/{page_id}/children?page_size={}",
            limit.min(100)
        );
        if let Some(next) = &cursor {
            path.push_str(&format!("&start_cursor={next}"));
        }
        let value = call_json(api(settings, "GET", &path), "读取页面正文").map_err(explain)?;
        for block in value
            .get("results")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
        {
            if blocks.len() >= limit as usize {
                truncated = true;
                break;
            }
            let text = block_text(block);
            if !text.is_empty() {
                blocks.push(text);
            }
        }
        if truncated || !value.get("has_more").and_then(Value::as_bool).unwrap_or(false) {
            break;
        }
        cursor = value
            .get("next_cursor")
            .and_then(Value::as_str)
            .map(str::to_string);
        if cursor.is_none() {
            break;
        }
    }
    let text = format!(
        "# {}\n\n{}{}",
        if title.is_empty() { "(无标题)" } else { &title },
        blocks.join("\n"),
        if truncated { "\n…（已截断）" } else { "" }
    );
    Ok(json!({ "id": page_id, "title": title, "blocks": blocks, "truncated": truncated, "text": text }))
}

/// One block's plain text, with a prefix that keeps the document readable.
fn block_text(block: &Value) -> String {
    let kind = block.get("type").and_then(Value::as_str).unwrap_or("");
    let payload = block.get(kind).unwrap_or(&Value::Null);
    let rich = payload
        .get("rich_text")
        .or_else(|| payload.get("text"))
        .and_then(Value::as_array);
    let text: String = rich
        .map(|parts| {
            parts
                .iter()
                .filter_map(|part| part.get("plain_text").and_then(Value::as_str))
                .collect()
        })
        .unwrap_or_default();
    if text.is_empty() {
        return match kind {
            "divider" => "---".to_string(),
            "child_page" => format!(
                "[子页面] {}",
                payload.get("title").and_then(Value::as_str).unwrap_or("")
            ),
            "image" => "[图片]".to_string(),
            _ => String::new(),
        };
    }
    match kind {
        "heading_1" => format!("# {text}"),
        "heading_2" => format!("## {text}"),
        "heading_3" => format!("### {text}"),
        "bulleted_list_item" => format!("- {text}"),
        "numbered_list_item" => format!("1. {text}"),
        "to_do" => format!(
            "- [{}] {text}",
            if payload
                .get("checked")
                .and_then(Value::as_bool)
                .unwrap_or(false)
            {
                "x"
            } else {
                " "
            }
        ),
        "quote" => format!("> {text}"),
        "code" => format!(
            "```{}\n{text}\n```",
            payload
                .get("language")
                .and_then(Value::as_str)
                .unwrap_or("")
        ),
        _ => text,
    }
}

fn create_page(settings: &Config, params: &Value) -> Result<Value, String> {
    if !settings.flag("allowWrite", true) {
        return Err("这个连接器已关闭写入：在设置里允许写入后再试".to_string());
    }
    let title = arg(params, "title");
    if title.is_empty() {
        return Err("缺少 title".to_string());
    }
    let parent_raw = {
        let given = arg(params, "parent_id");
        if given.is_empty() {
            settings.get("defaultPageId")
        } else {
            given.to_string()
        }
    };
    if parent_raw.is_empty() {
        return Err("缺少 parent_id（也可以在连接器设置里填默认写入页面）".to_string());
    }
    let parent = normalize_id(&parent_raw);
    let mut children = Vec::new();
    for line in arg(params, "content").lines() {
        let line = line.trim_end();
        if line.is_empty() {
            continue;
        }
        children.push(json!({
            "object": "block",
            "type": "paragraph",
            "paragraph": { "rich_text": [{ "type": "text", "text": { "content": line } }] }
        }));
    }
    let mut body = json!({
        "parent": { "page_id": parent },
        "properties": {
            "title": [{ "type": "text", "text": { "content": title } }]
        }
    });
    if !children.is_empty() {
        body["children"] = Value::Array(children);
    }
    let value = call_json(
        api(settings, "POST", "/v1/pages").body(body.to_string()),
        "新建页面",
    )
    .map_err(explain)?;
    let id = value.get("id").and_then(Value::as_str).unwrap_or("");
    Ok(json!({
        "id": id,
        "url": value.get("url").and_then(Value::as_str).unwrap_or(""),
        "text": format!("已新建页面「{title}」（{id}）"),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pasted_links_become_ids() {
        assert_eq!(
            normalize_id("https://www.notion.so/Design-notes-0123456789abcdef0123456789abcdef"),
            "0123456789abcdef0123456789abcdef"
        );
        assert_eq!(normalize_id("0123456789abcdef0123456789abcdef"), "0123456789abcdef0123456789abcdef");
    }

    #[test]
    fn blocks_render_with_structure() {
        let heading = json!({
            "type": "heading_2",
            "heading_2": { "rich_text": [{ "plain_text": "Summary" }] }
        });
        assert_eq!(block_text(&heading), "## Summary");
        let todo = json!({
            "type": "to_do",
            "to_do": { "checked": true, "rich_text": [{ "plain_text": "ship" }] }
        });
        assert_eq!(block_text(&todo), "- [x] ship");
    }

    #[test]
    fn writes_are_refused_when_disabled() {
        let settings = Config::from_pairs(&[("token", "t"), ("allowWrite", "false")]);
        let error = create_page(&settings, &json!({ "title": "x", "parent_id": "y" })).unwrap_err();
        assert!(error.contains("关闭写入"));
    }
}
