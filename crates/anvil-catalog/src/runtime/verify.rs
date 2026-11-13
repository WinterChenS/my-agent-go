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
    let status: u16 = stdout
        .trim()
        .rsplit('\n')
        .next()
        .unwrap_or("")
        .trim()
        .parse()
        .unwrap_or(0);

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
    fn escapes_quotes_for_curl_config() {
        assert_eq!(escape_config(r#"a"b\c"#), r#"a\"b\\c"#);
    }
}
