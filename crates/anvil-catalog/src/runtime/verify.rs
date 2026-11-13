//! Real model verification over an OpenAI-compatible endpoint.
//!
//! The request goes through the operating system's `curl` so the host needs no
//! TLS stack of its own. The credential is written to the child's stdin as a
//! curl config, never to its command line, and never to a log.

use std::io::Write;
use std::process::{Command, Stdio};
use std::time::Duration;

use crate::error::{Error, Result};
use crate::runtime::adapter::ModelVerification;

/// POST a one-token chat request and classify the answer.
pub fn openai_chat(
    endpoint: &str,
    model_id: &str,
    credential: Option<&str>,
    timeout: Duration,
) -> Result<ModelVerification> {
    let base = endpoint.trim_end_matches('/');
    if !(base.starts_with("http://") || base.starts_with("https://")) {
        return Err(Error::Rejected(format!(
            "endpoint {endpoint:?} is not an http(s) URL"
        )));
    }
    let url = format!("{base}/chat/completions");
    let body = serde_json::json!({
        "model": model_id,
        "messages": [{"role": "user", "content": "ping"}],
        "max_tokens": 1,
    })
    .to_string();

    let mut config = String::new();
    config.push_str(&format!("url = \"{}\"\n", escape_config(&url)));
    config.push_str("header = \"Content-Type: application/json\"\n");
    if let Some(credential) = credential {
        config.push_str(&format!(
            "header = \"Authorization: Bearer {}\"\n",
            escape_config(credential)
        ));
    }
    config.push_str(&format!("data = \"{}\"\n", escape_config(&body)));
    // A loopback endpoint must not go through an HTTP proxy: the host export in
    // some environments points at a proxy that cannot reach 127.0.0.1.
    if is_loopback(&url) {
        config.push_str("noproxy = \"localhost,127.0.0.1,::1\"\n");
    }
    // The body is not needed; only the status code, which curl appends to the
    // output stream.
    config.push_str(&format!(
        "output = \"{}\"\n",
        if cfg!(windows) { "NUL" } else { "/dev/null" }
    ));
    config.push_str("silent\n");
    config.push_str("show-error\n");
    config.push_str("write-out = \"%{http_code}\"\n");
    config.push_str(&format!("max-time = {}\n", timeout.as_secs().max(1)));

    let mut child = Command::new(curl_program())
        .arg("--config")
        .arg("-")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| Error::Io(format!("spawn curl: {error}")))?;
    if let Some(stdin) = child.stdin.as_mut() {
        stdin
            .write_all(config.as_bytes())
            .map_err(|error| Error::Io(format!("write curl config: {error}")))?;
    }
    drop(child.stdin.take());

    let output = child
        .wait_with_output()
        .map_err(|error| Error::Io(format!("wait curl: {error}")))?;
    let stdout = String::from_utf8_lossy(&output.stdout);
    // `write-out` appends the code directly after any remaining output, so the
    // status is the trailing run of digits.
    let trimmed = stdout.trim();
    let digits: String = trimmed
        .chars()
        .rev()
        .take_while(char::is_ascii_digit)
        .collect::<String>()
        .chars()
        .rev()
        .collect();
    let status: u16 = digits.parse().unwrap_or(0);

    Ok(match status {
        200..=299 => ModelVerification {
            ok: true,
            detail: Some(format!("HTTP {status}")),
        },
        401 | 403 => ModelVerification {
            ok: false,
            detail: Some(format!("credential rejected (HTTP {status})")),
        },
        404 => ModelVerification {
            ok: false,
            detail: Some("endpoint or model not found (HTTP 404)".to_string()),
        },
        0 => ModelVerification {
            ok: false,
            detail: Some("no HTTP status; the endpoint did not answer".to_string()),
        },
        other => ModelVerification {
            ok: false,
            detail: Some(format!("upstream answered HTTP {other}")),
        },
    })
}

fn curl_program() -> String {
    if cfg!(windows) {
        "curl.exe".to_string()
    } else {
        "curl".to_string()
    }
}

/// Whether the request targets the local machine.
fn is_loopback(url: &str) -> bool {
    let rest = url
        .trim_start_matches("http://")
        .trim_start_matches("https://");
    let authority = rest.split('/').next().unwrap_or("");
    let authority = match authority.rsplit_once('@') {
        Some((_userinfo, host)) => host,
        None => authority,
    };
    let host = match authority.strip_prefix('[') {
        // IPv6 literal: `[::1]` or `[::1]:8080`.
        Some(bracketed) => bracketed.split(']').next().unwrap_or(""),
        None => match authority.rsplit_once(':') {
            Some((host, _port)) => host,
            None => authority,
        },
    };
    matches!(host, "localhost" | "127.0.0.1" | "::1")
}

/// curl config values are quoted; only quotes and backslashes need escaping.
fn escape_config(value: &str) -> String {
    value.replace('\\', "\\\\").replace('"', "\\\"")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_non_http_endpoints() {
        let error = openai_chat("ftp://example.invalid", "m", None, Duration::from_secs(1))
            .expect_err("rejected");
        assert_eq!(error.code(), "rejected");
    }

    #[test]
    fn unreachable_endpoint_is_unverified_not_a_crash() {
        // Port 9 (discard) is closed on the loopback, so curl returns no status.
        let result = openai_chat(
            "http://127.0.0.1:9",
            "demo",
            Some("sk-test"),
            Duration::from_secs(2),
        );
        match result {
            Ok(verification) => assert!(!verification.ok),
            Err(error) => assert!(matches!(error, Error::Io(_))),
        }
    }

    #[test]
    fn detects_loopback_endpoints() {
        assert!(is_loopback("http://127.0.0.1:3425/v1"));
        assert!(is_loopback("http://localhost:8080/v1"));
        assert!(is_loopback("https://[::1]/v1"));
        assert!(!is_loopback("https://api.example.com/v1"));
    }

    #[test]
    fn escapes_quotes_for_curl_config() {
        assert_eq!(escape_config(r#"a"b\c"#), r#"a\"b\\c"#);
    }
}
