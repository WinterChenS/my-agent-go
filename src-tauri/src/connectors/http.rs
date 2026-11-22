//! The HTTP/IMAP/SMTP transport connectors use.
//!
//! Anvil links no TLS stack of its own: every connector speaks through the
//! system `curl`, which is present on Windows 10+ and ships with macOS/Linux.
//! That keeps the binary small and the certificates in the OS store, which is
//! what a desktop app should trust anyway.
//!
//! Credentials are passed through curl's config file on **stdin** (`-K -`), not
//! on the command line: an argument list is visible to every process on the
//! machine, a pipe is not.

use std::io::Write;
use std::process::{Command, Stdio};
use std::time::Instant;

/// What one request produced.
pub struct HttpResponse {
    pub status: u16,
    /// Response body (or the IMAP/SMTP transcript for those protocols).
    pub body: String,
}

/// One request. Built by the connector modules, executed by [`send`].
pub struct HttpRequest {
    pub method: String,
    pub url: String,
    pub headers: Vec<(String, String)>,
    pub body: Option<String>,
    /// `user:password`, handed to curl's `--user`.
    pub user: Option<String>,
    /// A protocol command instead of an HTTP verb: an IMAP command such as
    /// `UID SEARCH ALL`, or `NOOP` on SMTP.
    pub request_line: Option<String>,
    /// SMTP takes the message as `--upload-file`, not as a request body.
    pub upload: bool,
    /// SMTP envelope: who it is from and who receives it.
    pub mail_from: Option<String>,
    pub mail_rcpt: Vec<String>,
    /// IMAP/SMTP on 143/587 need an explicit STARTTLS upgrade.
    pub starttls: bool,
    pub timeout_secs: u64,
}

impl HttpRequest {
    pub fn new(method: &str, url: impl Into<String>) -> Self {
        Self {
            method: method.to_string(),
            url: url.into(),
            headers: Vec::new(),
            body: None,
            user: None,
            request_line: None,
            upload: false,
            mail_from: None,
            mail_rcpt: Vec::new(),
            starttls: false,
            timeout_secs: 30,
        }
    }

    pub fn header(mut self, key: &str, value: impl Into<String>) -> Self {
        self.headers.push((key.to_string(), value.into()));
        self
    }

    pub fn body(mut self, body: impl Into<String>) -> Self {
        self.body = Some(body.into());
        self
    }

    pub fn user(mut self, user: impl Into<String>) -> Self {
        self.user = Some(user.into());
        self
    }

    pub fn request_line(mut self, line: impl Into<String>) -> Self {
        self.request_line = Some(line.into());
        self
    }

    pub fn timeout(mut self, secs: u64) -> Self {
        self.timeout_secs = secs;
        self
    }

    /// Hand the body to `--upload-file` (SMTP) instead of `--data-binary`.
    pub fn upload(mut self) -> Self {
        self.upload = true;
        self
    }

    pub fn envelope(mut self, from: impl Into<String>, rcpt: Vec<String>) -> Self {
        self.mail_from = Some(from.into());
        self.mail_rcpt = rcpt;
        self
    }

    pub fn starttls(mut self) -> Self {
        self.starttls = true;
        self
    }
}

/// Quote one value for curl's config grammar.
fn quote(value: &str) -> String {
    let escaped = value.replace('\\', "\\\\").replace('"', "\\\"");
    format!("\"{escaped}\"")
}

/// The config text curl reads from stdin. Pure so it can be asserted.
fn config_text(request: &HttpRequest, body_file: Option<&str>) -> String {
    let mut lines = vec![
        "silent".to_string(),
        "show-error".to_string(),
        // A connector that points at http:// where https:// was meant should
        // follow the redirect rather than fail with a confusing empty body.
        "location".to_string(),
        format!("max-time = {}", request.timeout_secs.max(1)),
        format!("url = {}", quote(&request.url)),
        "write-out = \"\\n%{http_code}\"".to_string(),
    ];
    let verb = request
        .request_line
        .clone()
        .unwrap_or_else(|| request.method.clone());
    if !verb.eq_ignore_ascii_case("GET") || request.request_line.is_some() {
        lines.push(format!("request = {}", quote(&verb)));
    }
    for (key, value) in &request.headers {
        lines.push(format!("header = {}", quote(&format!("{key}: {value}"))));
    }
    if let Some(user) = &request.user {
        lines.push(format!("user = {}", quote(user)));
    }
    if request.starttls {
        lines.push("ssl-reqd".to_string());
    }
    if let Some(from) = &request.mail_from {
        lines.push(format!("mail-from = {}", quote(from)));
    }
    for rcpt in &request.mail_rcpt {
        lines.push(format!("mail-rcpt = {}", quote(rcpt)));
    }
    if let Some(path) = body_file {
        let key = if request.upload {
            "upload-file"
        } else {
            "data-binary"
        };
        lines.push(format!("{key} = {}", quote(&format!("@{path}"))));
    }
    lines.join("\n")
}

/// Run one request. Blocks for up to `timeout_secs`.
pub fn send(request: &HttpRequest) -> Result<HttpResponse, String> {
    let mut temp: Option<std::path::PathBuf> = None;
    if let Some(body) = &request.body {
        let name = format!(
            "anvil-connector-{}-{}.body",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|value| value.as_nanos())
                .unwrap_or_default()
        );
        let path = std::env::temp_dir().join(name);
        std::fs::write(&path, body).map_err(|error| format!("写临时请求体失败：{error}"))?;
        temp = Some(path);
    }
    let body_file = temp.as_ref().map(|path| path.to_string_lossy().into_owned());
    let config = config_text(request, body_file.as_deref());

    let curl = std::env::var("ANVIL_CURL").unwrap_or_else(|_| "curl".to_string());
    let started = Instant::now();
    let mut child = Command::new(&curl)
        .args(["-q", "-sS", "-K", "-"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| format!("无法运行 curl（{curl}）：{error}"))?;
    if let Some(stdin) = child.stdin.as_mut() {
        stdin
            .write_all(config.as_bytes())
            .map_err(|error| format!("无法把请求交给 curl：{error}"))?;
    }
    let output = child
        .wait_with_output()
        .map_err(|error| format!("curl 没有返回：{error}"))?;
    if let Some(path) = temp {
        let _ = std::fs::remove_file(path);
    }
    let _ = started.elapsed();
    let mut stdout = String::from_utf8_lossy(&output.stdout).into_owned();
    let stderr = String::from_utf8_lossy(&output.stderr).trim().to_string();
    if !output.status.success() {
        return Err(human_error(&stderr, output.status.code()));
    }
    // `write-out` appends the status on its own line; IMAP/SMTP answer `000`.
    let mut status = 0u16;
    if let Some(index) = stdout.rfind('\n') {
        let tail = stdout[index + 1..].trim().to_string();
        if let Ok(parsed) = tail.parse::<u16>() {
            status = parsed;
            stdout.truncate(index);
        }
    }
    Ok(HttpResponse {
        status,
        body: stdout,
    })
}

/// Turn curl's exit code into a sentence a person can act on.
fn human_error(stderr: &str, code: Option<i32>) -> String {
    let detail = if stderr.is_empty() {
        "没有错误输出".to_string()
    } else {
        stderr.to_string()
    };
    let hint = match code {
        Some(6) => "（域名解析失败，检查服务器地址）",
        Some(7) => "（连不上服务器，检查地址、端口和网络）",
        Some(28) => "（连接超时）",
        Some(35) => "（TLS 握手失败，检查端口是否对应该加密方式）",
        Some(60) => "（证书校验失败）",
        Some(67) => "（账号或密码 / 授权码不正确）",
        Some(64 | 65 | 55) => "（服务器拒绝了这次请求）",
        _ => "",
    };
    format!("{detail}{hint}")
}

/// Read a `message` / `error` field out of a JSON error body.
pub fn api_error(status: u16, body: &str) -> String {
    let snippet = body.trim();
    if let Ok(value) = serde_json::from_str::<serde_json::Value>(snippet) {
        for key in ["message", "error_description", "error", "errmsg"] {
            if let Some(text) = value.get(key).and_then(|value| value.as_str()) {
                return format!("HTTP {status}：{text}");
            }
            if let Some(nested) = value.get(key).and_then(|value| value.get("message")) {
                if let Some(text) = nested.as_str() {
                    return format!("HTTP {status}：{text}");
                }
            }
        }
    }
    let trimmed: String = snippet.chars().take(200).collect();
    if trimmed.is_empty() {
        format!("HTTP {status}")
    } else {
        format!("HTTP {status}：{trimmed}")
    }
}

/// Parse a JSON body, turning a non-JSON answer into a readable error.
pub fn json_body<T: serde::de::DeserializeOwned>(
    response: &HttpResponse,
    what: &str,
) -> Result<T, String> {
    if response.status >= 400 {
        return Err(api_error(response.status, &response.body));
    }
    serde_json::from_str(&response.body)
        .map_err(|error| format!("{what} 返回的不是预期 JSON：{error}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn config_quotes_every_value_and_keeps_user_off_argv() {
        let request = HttpRequest::new("POST", "https://api.notion.com/v1/search")
            .header("Authorization", "Bearer abc")
            .body("{}")
            .user("me@example.com:p@ss\"word");
        let text = config_text(&request, Some("C:/tmp/body.json"));
        assert!(text.contains("request = \"POST\""));
        assert!(text.contains("header = \"Authorization: Bearer abc\""));
        assert!(text.contains("user = \"me@example.com:p@ss\\\"word\""));
        assert!(text.contains("data-binary = \"@C:/tmp/body.json\""));
        assert!(text.contains("write-out"));
    }

    #[test]
    fn a_get_carries_no_request_line() {
        let request = HttpRequest::new("GET", "https://example.com");
        let text = config_text(&request, None);
        assert!(!text.contains("request ="), "{text}");
    }

    #[test]
    fn an_imap_command_becomes_the_request_line() {
        let request = HttpRequest::new("GET", "imaps://imap.example.com:993/INBOX")
            .request_line("UID SEARCH ALL");
        let text = config_text(&request, None);
        assert!(text.contains("request = \"UID SEARCH ALL\""));
    }

    #[test]
    fn curl_codes_become_actionable_sentences() {
        let text = human_error("Failed to connect", Some(7));
        assert!(text.contains("连不上服务器"));
    }

    #[test]
    fn api_errors_prefer_the_service_message() {
        assert!(api_error(401, r#"{"message":"API token is invalid."}"#).contains("token is invalid"));
        assert!(api_error(500, "boom").contains("boom"));
    }
}
