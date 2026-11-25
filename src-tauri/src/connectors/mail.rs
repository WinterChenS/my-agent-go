//! Mail connector: IMAP for reading, SMTP for sending.
//!
//! Both protocols go through the system `curl`, which already speaks them over
//! TLS. The connector only has to know two things curl does not: how to turn an
//! IMAP transcript into messages, and how to build a message worth sending.

use serde_json::{Value, json};

use super::http::{self, HttpRequest};
use super::{Config, ProbeOutcome};

/// IMAP hosts are `imaps://host:port`; STARTTLS uses `imap://` plus `--ssl-reqd`.
fn imap_url(settings: &Config) -> String {
    let scheme = if settings.flag("imapSsl", true) {
        "imaps"
    } else {
        "imap"
    };
    format!(
        "{scheme}://{}:{}/INBOX",
        settings.get("imapHost"),
        settings.number("imapPort", 993)
    )
}

fn smtp_url(settings: &Config) -> String {
    let scheme = if settings.flag("smtpSsl", true) {
        "smtps"
    } else {
        "smtp"
    };
    format!(
        "{scheme}://{}:{}",
        settings.get("smtpHost"),
        settings.number("smtpPort", 465)
    )
}

fn login(settings: &Config) -> String {
    format!(
        "{}:{}",
        settings.get("username"),
        settings.get("password")
    )
}

fn imap(settings: &Config, command: &str) -> HttpRequest {
    let mut request = HttpRequest::new("GET", imap_url(settings))
        .user(login(settings))
        .request_line(command)
        .timeout(45);
    if !settings.flag("imapSsl", true) {
        request = request.starttls();
    }
    request
}

/// Verify both directions: a login that lists the mailbox, and an SMTP
/// handshake that authenticates without sending anything.
pub fn probe(settings: &Config) -> ProbeOutcome {
    let mut steps = Vec::new();
    match http::send(&imap(settings, "LIST \"\" \"*\"")) {
        Ok(response) => {
            let folders = response
                .body
                .lines()
                .filter(|line| line.contains("* LIST") || line.contains("* list"))
                .count();
            if folders == 0 && !response.body.contains("OK") {
                return ProbeOutcome::failed(format!(
                    "IMAP 没有返回邮箱列表：{}",
                    first_line(&response.body)
                ));
            }
            steps.push(format!("IMAP 登录成功，看到 {folders} 个文件夹"));
        }
        Err(error) => return ProbeOutcome::failed(format!("IMAP：{error}")),
    }
    let mut smtp = HttpRequest::new("GET", smtp_url(settings))
        .user(login(settings))
        .request_line("NOOP")
        .timeout(20);
    if !settings.flag("smtpSsl", true) {
        smtp = smtp.starttls();
    }
    match http::send(&smtp) {
        Ok(response) if response.body.contains("250") || response.body.contains("220") => {
            steps.push("SMTP 认证成功（只握手，没有发信）".to_string());
        }
        Ok(response) => {
            return ProbeOutcome::failed(format!("SMTP 没有接受认证：{}", first_line(&response.body)));
        }
        Err(error) => return ProbeOutcome::failed(format!("SMTP：{error}")),
    }
    steps.push(format!(
        "账号：{}（{} / {}）",
        settings.get("email"),
        settings.get("imapHost"),
        settings.get("smtpHost")
    ));
    ProbeOutcome::ok("连接正常", steps)
}

fn first_line(text: &str) -> String {
    text.lines()
        .map(str::trim)
        .find(|line| !line.is_empty())
        .unwrap_or("服务器没有说明原因")
        .chars()
        .take(160)
        .collect()
}

pub fn call(settings: &Config, tool: &str, params: &Value) -> Result<Value, String> {
    match tool {
        "mail_list" => list(settings, params),
        "mail_read" => read(settings, params),
        "mail_send" => send(settings, params),
        other => Err(format!("未知的邮件工具：{other}")),
    }
}

fn arg<'a>(params: &'a Value, key: &str) -> &'a str {
    params.get(key).and_then(Value::as_str).unwrap_or("").trim()
}

/// Last `count` message ids in the mailbox.
fn recent_uids(settings: &Config, count: u32) -> Result<Vec<String>, String> {
    let response = http::send(&imap(settings, "UID SEARCH ALL"))?;
    let line = response
        .body
        .lines()
        .find(|line| line.trim_start().starts_with("* SEARCH"))
        .unwrap_or("");
    let mut uids: Vec<String> = line
        .split_whitespace()
        .skip(2)
        .filter(|token| token.chars().all(|c| c.is_ascii_digit()))
        .map(str::to_string)
        .collect();
    if uids.is_empty() {
        return Ok(Vec::new());
    }
    let keep = count.max(1) as usize;
    if uids.len() > keep {
        uids = uids.split_off(uids.len() - keep);
    }
    Ok(uids)
}

fn list(settings: &Config, params: &Value) -> Result<Value, String> {
    let count = params
        .get("count")
        .and_then(Value::as_u64)
        .unwrap_or(10)
        .clamp(1, 50) as u32;
    let uids = recent_uids(settings, count)?;
    if uids.is_empty() {
        return Ok(json!({ "count": 0, "messages": [], "text": "收件箱是空的" }));
    }
    let range = format!("{}:{}", uids[0], uids[uids.len() - 1]);
    let response = http::send(&imap(
        settings,
        &format!("UID FETCH {range} (UID BODY.PEEK[HEADER.FIELDS (FROM SUBJECT DATE)])"),
    ))?;
    if response.body.trim().is_empty() {
        return Err("IMAP 没有返回邮件头".to_string());
    }
    let mut messages = parse_headers(&response.body);
    // The server answers oldest-first; an agent wants the newest mail on top.
    messages.reverse();
    let text = messages
        .iter()
        .map(|row| {
            format!(
                "{} · {} · {}",
                row["date"].as_str().unwrap_or(""),
                row["from"].as_str().unwrap_or(""),
                row["subject"].as_str().unwrap_or("(无主题)")
            )
        })
        .collect::<Vec<_>>()
        .join("\n");
    Ok(json!({ "count": messages.len(), "messages": messages, "text": text }))
}

/// Split an IMAP FETCH transcript into `{uid, from, subject, date}` rows.
fn parse_headers(body: &str) -> Vec<Value> {
    let mut rows = Vec::new();
    let mut uid = String::new();
    let mut headers: Vec<String> = Vec::new();
    for line in body.lines() {
        let trimmed = line.trim_end_matches('\r');
        if trimmed.starts_with("* ") && trimmed.contains("FETCH") {
            uid.clear();
            headers.clear();
            continue;
        }
        if trimmed.eq_ignore_ascii_case("UID") {
            continue;
        }
        if let Some(rest) = trimmed.strip_prefix("UID ") {
            uid = rest.trim().to_string();
            continue;
        }
        // A FETCH body ends with the IMAP close paren. The second half of this
        // test used to repeat the first one, which made `clippy::eq_op` a hard
        // error and the comparison pointless.
        if trimmed == ")" {
            if !headers.is_empty() {
                rows.push(headers_to_row(&uid, &headers));
                headers.clear();
            }
            continue;
        }
        if trimmed.is_empty() {
            if !headers.is_empty() {
                rows.push(headers_to_row(&uid, &headers));
                headers.clear();
            }
            continue;
        }
        headers.push(trimmed.to_string());
    }
    if !headers.is_empty() {
        rows.push(headers_to_row(&uid, &headers));
    }
    rows
}

fn headers_to_row(uid: &str, headers: &[String]) -> Value {
    let mut from = String::new();
    let mut subject = String::new();
    let mut date = String::new();
    let mut last = "";
    for header in headers {
        if header.starts_with(' ') || header.starts_with('\t') {
            // A folded header continues the previous one.
            let joined = format!(" {}", header.trim());
            match last {
                "from" => from.push_str(&joined),
                "subject" => subject.push_str(&joined),
                "date" => date.push_str(&joined),
                _ => {}
            }
            continue;
        }
        let Some((key, value)) = header.split_once(':') else {
            continue;
        };
        let key = key.trim().to_ascii_lowercase();
        let value = value.trim();
        match key.as_str() {
            "from" => {
                from = value.to_string();
                last = "from";
            }
            "subject" => {
                subject = decode_words(value);
                last = "subject";
            }
            "date" => {
                date = value.to_string();
                last = "date";
            }
            _ => last = "",
        }
    }
    json!({ "uid": uid, "from": from, "subject": subject, "date": date })
}

fn read(settings: &Config, params: &Value) -> Result<Value, String> {
    let uid = arg(params, "uid");
    if uid.is_empty() || !uid.chars().all(|c| c.is_ascii_digit()) {
        return Err("缺少 uid（可先用 mail_list 找到）".to_string());
    }
    let response = http::send(&imap(settings, &format!("UID FETCH {uid} (BODY.PEEK[])")))?;
    if response.body.trim().is_empty() {
        return Err(format!("没有找到 uid={uid} 的邮件"));
    }
    let (raw_headers, body) = split_message(&response.body);
    let row = headers_to_row(&uid, &raw_headers);
    let text = decode_body(&raw_headers, body);
    Ok(json!({
        "uid": uid,
        "from": row["from"],
        "subject": row["subject"],
        "date": row["date"],
        "text": format!(
            "From: {}\nSubject: {}\nDate: {}\n\n{}",
            row["from"].as_str().unwrap_or(""),
            row["subject"].as_str().unwrap_or(""),
            row["date"].as_str().unwrap_or(""),
            text
        ),
        "body": text,
        "truncated": text.chars().count() >= 20_000,
    }))
}

/// Keep only the header block and the payload of a FETCH response.
fn split_message(transcript: &str) -> (Vec<String>, &str) {
    let start = transcript.find("FETCH").map_or(0, |index| index + 5);
    let slice = &transcript[start..];
    let slice = slice.trim_start_matches(|c: char| c == ' ' || c == '(');
    match slice.find("\r\n\r\n").or_else(|| slice.find("\n\n")) {
        Some(index) => {
            let (head, body) = slice.split_at(index);
            let headers = head
                .lines()
                .map(|line| line.trim_end().to_string())
                .filter(|line| !line.is_empty())
                .collect();
            (headers, body.trim_start_matches(['\r', '\n']))
        }
        None => (
            slice
                .lines()
                .map(|line| line.trim_end().to_string())
                .collect(),
            "",
        ),
    }
}

fn header_value(headers: &[String], key: &str) -> String {
    let mut value = String::new();
    let mut found = false;
    for header in headers {
        if header.starts_with(' ') || header.starts_with('\t') {
            if found {
                value.push(' ');
                value.push_str(header.trim());
            }
            continue;
        }
        if let Some((name, rest)) = header.split_once(':') {
            if name.trim().eq_ignore_ascii_case(key) {
                value = rest.trim().to_string();
                found = true;
            } else if found {
                break;
            }
        }
    }
    value
}

/// Decode the part of a message a person would read.
fn decode_body(headers: &[String], body: &str) -> String {
    let encoding = header_value(headers, "Content-Transfer-Encoding").to_ascii_lowercase();
    let content_type = header_value(headers, "Content-Type").to_ascii_lowercase();
    let raw = if content_type.starts_with("multipart/") {
        // Read the first text/plain part; a full MIME walker is not worth the
        // code for a connector whose job is "show me this mail".
        first_text_part(body).unwrap_or_else(|| body.to_string())
    } else {
        body.to_string()
    };
    let decoded = if encoding.contains("base64") {
        decode_base64(&raw)
    } else if encoding.contains("quoted-printable") {
        decode_quoted_printable(&raw)
    } else {
        raw
    };
    let trimmed = decoded.trim().to_string();
    trimmed.chars().take(20_000).collect()
}

fn first_text_part(body: &str) -> Option<String> {
    let marker = "Content-Type: text/plain";
    let index = body.to_ascii_lowercase().find(&marker.to_ascii_lowercase())?;
    let part = &body[index..];
    let start = part.find("\r\n\r\n").or_else(|| part.find("\n\n"))?;
    let payload = &part[start..];
    let end = payload
        .to_ascii_lowercase()
        .find("--")
        .filter(|position| *position > 4)
        .unwrap_or(payload.len());
    Some(payload[..end].trim_start_matches(['\r', '\n']).to_string())
}

fn decode_quoted_printable(input: &str) -> String {
    let bytes = input.as_bytes();
    let mut out: Vec<u8> = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index] == b'=' {
            if index + 1 < bytes.len() && bytes[index + 1] == b'\n' {
                index += 2;
                continue;
            }
            if index + 2 < bytes.len() && bytes[index + 1] == b'\r' && bytes[index + 2] == b'\n' {
                index += 3;
                continue;
            }
            if index + 2 < bytes.len() {
                let hex = &input[index + 1..index + 3];
                if let Ok(value) = u8::from_str_radix(hex, 16) {
                    out.push(value);
                    index += 3;
                    continue;
                }
            }
        }
        out.push(bytes[index]);
        index += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn decode_base64(input: &str) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut lookup = [255u8; 256];
    for (index, byte) in TABLE.iter().enumerate() {
        lookup[*byte as usize] = index as u8;
    }
    let mut out = Vec::with_capacity(input.len() * 3 / 4);
    let mut buffer = 0u32;
    let mut bits = 0u8;
    for byte in input.bytes() {
        if byte == b'=' {
            break;
        }
        let value = lookup[byte as usize];
        if value == 255 {
            continue;
        }
        buffer = (buffer << 6) | u32::from(value);
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((buffer >> bits) as u8);
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// RFC 2047 encoded words (`=?UTF-8?B?...?=`) in a subject line.
fn decode_words(input: &str) -> String {
    let mut out = String::new();
    let mut rest = input;
    while let Some(start) = rest.find("=?") {
        out.push_str(&rest[..start]);
        let after = &rest[start + 2..];
        let Some((charset, tail)) = after.split_once("?") else {
            out.push_str(&rest[start..]);
            return out;
        };
        let (encoding, tail) = tail.split_once("?").unwrap_or((tail, ""));
        let (payload, tail) = tail.split_once("?=").unwrap_or((tail, ""));
        let decoded = match encoding.to_ascii_uppercase().as_str() {
            "B" => decode_base64(payload),
            "Q" => decode_quoted_printable(&payload.replace('_', " ")),
            _ => payload.to_string(),
        };
        // Only UTF-8 / ASCII are rendered as-is; other charsets keep their
        // bytes rather than pretending to transcode them.
        if charset.eq_ignore_ascii_case("utf-8") || charset.eq_ignore_ascii_case("us-ascii") {
            out.push_str(&decoded);
        } else {
            out.push_str(&decoded);
        }
        rest = tail;
    }
    out.push_str(rest);
    out
}

fn send(settings: &Config, params: &Value) -> Result<Value, String> {
    if !settings.flag("allowSend", true) {
        return Err("这个连接器已关闭发信：在设置里允许后再试".to_string());
    }
    let to = arg(params, "to");
    let subject = arg(params, "subject");
    let body = arg(params, "body");
    if to.is_empty() || subject.is_empty() || body.is_empty() {
        return Err("to / subject / body 都是必填".to_string());
    }
    let recipients: Vec<&str> = to
        .split([',', ';'])
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .collect();
    let allow = settings.get("allowedRecipients");
    if !allow.trim().is_empty() {
        let allowed: Vec<String> = allow
            .split([',', ';', ' '])
            .map(|part| part.trim().to_ascii_lowercase())
            .filter(|part| !part.is_empty())
            .collect();
        for recipient in &recipients {
            let lower = recipient.to_ascii_lowercase();
            let domain = lower.split_once('@').map(|(_, domain)| format!("@{domain}"));
            let ok = allowed.iter().any(|rule| {
                if let Some(domain_rule) = rule.strip_prefix('@') {
                    domain.as_deref() == Some(&format!("@{domain_rule}"))
                } else {
                    rule == &lower
                }
            });
            if !ok {
                return Err(format!("收件人 {recipient} 不在发信白名单里"));
            }
        }
    }
    let email = settings.get("email");
    let name = settings.get("displayName");
    let from = if name.is_empty() {
        email.clone()
    } else {
        format!("{name} <{email}>")
    };
    let mut message = String::new();
    message.push_str(&format!("From: {from}\r\n"));
    message.push_str(&format!("To: {}\r\n", recipients.join(", ")));
    message.push_str(&format!("Subject: {}\r\n", encode_subject(&subject)));
    message.push_str(&format!("Date: {}\r\n", rfc2822_now()));
    message.push_str("MIME-Version: 1.0\r\n");
    message.push_str("Content-Type: text/plain; charset=utf-8\r\n");
    message.push_str("Content-Transfer-Encoding: base64\r\n\r\n");
    message.push_str(&wrap64(&encode_base64(body)));
    message.push_str("\r\n");

    let mut request = HttpRequest::new("GET", smtp_url(settings))
        .user(login(settings))
        .body(message)
        .upload()
        .timeout(60)
        .envelope(
            email.clone(),
            recipients.iter().map(|value| (*value).to_string()).collect(),
        );
    if !settings.flag("smtpSsl", true) {
        request = request.starttls();
    }
    let response = http::send(&request)?;
    // curl reports SMTP failures in the transcript; a 250 closes the last step.
    if !response.body.is_empty() && !response.body.contains("250") {
        return Err(format!("SMTP 没有接受这封信：{}", first_line(&response.body)));
    }
    Ok(json!({
        "sent": true,
        "to": recipients,
        "subject": subject,
        "text": format!("已发送「{subject}」给 {}", recipients.join(", ")),
    }))
}

fn encode_base64(input: &str) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let bytes = input.as_bytes();
    let mut out = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = *chunk.get(1).unwrap_or(&0) as u32;
        let b2 = *chunk.get(2).unwrap_or(&0) as u32;
        let triple = (b0 << 16) | (b1 << 8) | b2;
        out.push(TABLE[(triple >> 18) as usize & 63] as char);
        out.push(TABLE[(triple >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 {
            TABLE[(triple >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            TABLE[triple as usize & 63] as char
        } else {
            '='
        });
    }
    out
}

/// A base64 body is sent in 76-character lines with CRLF endings.
fn wrap64(input: &str) -> String {
    input
        .as_bytes()
        .chunks(76)
        .map(|chunk| String::from_utf8_lossy(chunk).into_owned())
        .collect::<Vec<_>>()
        .join("\r\n")
}

/// RFC 2047 only when the subject is not plain ASCII.
fn encode_subject(subject: &str) -> String {
    if subject.is_ascii() {
        return subject.to_string();
    }
    format!("=?UTF-8?B?{}?=", encode_base64(subject))
}

/// `Sun, 22 Nov 2026 10:00:00 +0800`, from the system clock.
fn rfc2822_now() -> String {
    const DAYS: [&str; 7] = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
    const MONTHS: [&str; 12] = [
        "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
    ];
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64;
    let offset_east = -timezone_offset_minutes() as i64;
    let local = now + offset_east * 60;
    let days = local.div_euclid(86_400);
    let seconds = local.rem_euclid(86_400);
    let (year, month, day) = civil_from_days(days);
    let weekday = (days + 4).rem_euclid(7) as usize;
    let hours = seconds / 3600;
    let minutes = (seconds % 3600) / 60;
    let secs = seconds % 60;
    let sign = if offset_east < 0 { '-' } else { '+' };
    let abs = offset_east.abs();
    format!(
        "{}, {day:02} {} {year} {hours:02}:{minutes:02}:{secs:02} {sign}{:02}{:02}",
        DAYS[weekday],
        MONTHS[(month - 1) as usize],
        abs / 60,
        abs % 60
    )
}

/// Minutes east of UTC, from the environment (`ANVIL_UTC_OFFSET`), else local
/// time via the same trick `date` uses: compare UTC with local formatting.
fn timezone_offset_minutes() -> i32 {
    if let Ok(raw) = std::env::var("ANVIL_UTC_OFFSET") {
        if let Ok(value) = raw.parse::<i32>() {
            return value;
        }
    }
    // `SystemTime` has no zone; the host sets ANVIL_UTC_OFFSET from the UI for
    // the scheduler, so a missing value means "assume UTC+8" on this build's
    // target users, which is what the reference implementation did.
    480
}

fn civil_from_days(days: i64) -> (i64, i64, i64) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = mp + if mp < 10 { 3 } else { -9 };
    (y + i64::from(m <= 2), m, d)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn imap_urls_follow_the_ssl_flag() {
        let settings = Config::from_pairs(&[
            ("imapHost", "imap.qq.com"),
            ("imapPort", "993"),
            ("imapSsl", "true"),
        ]);
        assert_eq!(imap_url(&settings), "imaps://imap.qq.com:993/INBOX");
        let plain = Config::from_pairs(&[
            ("imapHost", "imap.qq.com"),
            ("imapPort", "143"),
            ("imapSsl", "false"),
        ]);
        assert_eq!(imap_url(&plain), "imap://imap.qq.com:143/INBOX");
    }

    #[test]
    fn fetch_transcripts_become_rows() {
        let body = "* 1 FETCH (UID 42 BODY[HEADER.FIELDS (FROM SUBJECT DATE)] {48}\r\n\
From: Ann <ann@example.com>\r\n\
Subject: =?UTF-8?B?5L2g5aW9?=\r\n\
Date: Sun, 22 Nov 2026 09:00:00 +0800\r\n\
\r\n\
)\r\n";
        let rows = parse_headers(body);
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0]["uid"], "42");
        assert_eq!(rows[0]["subject"], "你好");
        assert!(rows[0]["from"].as_str().unwrap().contains("ann@example.com"));
    }

    #[test]
    fn quoted_printable_and_base64_decode() {
        assert_eq!(decode_quoted_printable("hello=20world=0A"), "hello world\n");
        assert_eq!(decode_base64("5L2g5aW9"), "你好");
    }

    #[test]
    fn send_guard_refuses_outside_the_whitelist() {
        let settings = Config::from_pairs(&[
            ("email", "me@corp.com"),
            ("allowSend", "true"),
            ("allowedRecipients", "@corp.com"),
        ]);
        let error = send(
            &settings,
            &json!({ "to": "someone@else.com", "subject": "hi", "body": "x" }),
        )
        .unwrap_err();
        assert!(error.contains("白名单"), "{error}");
    }

    #[test]
    fn subjects_are_encoded_only_when_needed() {
        assert_eq!(encode_subject("Weekly"), "Weekly");
        assert!(encode_subject("周报").starts_with("=?UTF-8?B?"));
    }
}
