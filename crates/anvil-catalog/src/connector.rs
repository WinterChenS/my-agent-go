//! Connectors: third-party services an agent acts in on the user's behalf.
//!
//! A connector is **not** an MCP server. MCP is a protocol a runtime speaks to a
//! tool host; a connector is a service with its own credentials and its own API
//! (mail over IMAP/SMTP, Notion over HTTPS, DingTalk docs over HTTPS). Both end
//! up as tools the agent can call, but they are configured, probed and audited
//! differently, so they live in different tables and different pages.
//!
//! This module owns the part that is pure data: the type catalogue (fields,
//! secrets, capabilities) and the row shape. The network calls live in the host,
//! because only the host has a process runner.

use serde::{Deserialize, Serialize};

use crate::error::{Error, Result};

/// What kind of control the settings form draws for a field.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum FieldKind {
    Text,
    Password,
    Number,
    Select,
    Tags,
}

/// One option of a `select` field: the stored value and its label.
#[derive(Debug, Clone, Copy, Serialize)]
pub struct FieldOption {
    pub value: &'static str,
    pub label: &'static str,
}

/// One configuration field of a connector type.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorField {
    pub key: &'static str,
    pub label: &'static str,
    /// Long-form hint under the input.
    pub hint: &'static str,
    pub placeholder: &'static str,
    pub kind: FieldKind,
    /// Stored value never leaves the host; the UI only learns it is set.
    pub secret: bool,
    pub required: bool,
    pub options: &'static [FieldOption],
}

impl ConnectorField {
    const fn text(
        key: &'static str,
        label: &'static str,
        hint: &'static str,
        placeholder: &'static str,
        required: bool,
    ) -> Self {
        Self {
            key,
            label,
            hint,
            placeholder,
            kind: FieldKind::Text,
            secret: false,
            required,
            options: &[],
        }
    }

    const fn password(
        key: &'static str,
        label: &'static str,
        hint: &'static str,
        required: bool,
    ) -> Self {
        Self {
            key,
            label,
            hint,
            placeholder: "",
            kind: FieldKind::Password,
            secret: true,
            required,
            options: &[],
        }
    }

    const fn number(
        key: &'static str,
        label: &'static str,
        hint: &'static str,
        placeholder: &'static str,
        required: bool,
    ) -> Self {
        Self {
            key,
            label,
            hint,
            placeholder,
            kind: FieldKind::Number,
            secret: false,
            required,
            options: &[],
        }
    }

    const fn select(
        key: &'static str,
        label: &'static str,
        hint: &'static str,
        options: &'static [FieldOption],
    ) -> Self {
        Self {
            key,
            label,
            hint,
            placeholder: "",
            kind: FieldKind::Select,
            secret: false,
            required: false,
            options,
        }
    }

    const fn tags(key: &'static str, label: &'static str, hint: &'static str) -> Self {
        Self {
            key,
            label,
            hint,
            placeholder: "",
            kind: FieldKind::Tags,
            secret: false,
            required: false,
            options: &[],
        }
    }
}

/// A connector type: what the page can add, and which fields it asks for.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorType {
    pub id: &'static str,
    pub name: &'static str,
    pub description: &'static str,
    /// Capability tags rendered as chips: read / search / write / send / attachment.
    pub capabilities: &'static [&'static str],
    pub fields: &'static [ConnectorField],
    /// Tools this type exposes to the agent, with a one-line description.
    pub tools: &'static [(&'static str, &'static str)],
}

impl ConnectorType {
    /// Keys that must be present before the connector can be saved.
    #[must_use]
    pub fn required_keys(&self) -> Vec<&'static str> {
        self.fields
            .iter()
            .filter(|field| field.required)
            .map(|field| field.key)
            .collect()
    }

    /// Validate one config object. `saved` is the stored config, so a blank
    /// secret on an edit ("leave unchanged") still passes.
    pub fn validate(
        &self,
        config: &serde_json::Map<String, serde_json::Value>,
        saved: Option<&serde_json::Map<String, serde_json::Value>>,
    ) -> Result<()> {
        for field in self.fields {
            let value = config
                .get(field.key)
                .and_then(|value| value.as_str())
                .unwrap_or("")
                .trim()
                .to_string();
            let kept = saved.is_some_and(|saved| {
                saved
                    .get(field.key)
                    .and_then(|value| value.as_str())
                    .is_some_and(|value| !value.trim().is_empty())
            });
            if field.required && value.is_empty() && !(field.secret && kept) {
                return Err(Error::Rejected(format!("{} 必填", field.label)));
            }
            if field.kind == FieldKind::Number && !value.is_empty() {
                if value.parse::<u16>().is_err() {
                    return Err(Error::Rejected(format!("{} 必须是数字", field.label)));
                }
            }
            if !field.options.is_empty()
                && !value.is_empty()
                && !field
                    .options
                    .iter()
                    .any(|option| option.value.eq_ignore_ascii_case(&value))
            {
                return Err(Error::Rejected(format!(
                    "{} 只能是 {}",
                    field.label,
                    field
                        .options
                        .iter()
                        .map(|option| option.value)
                        .collect::<Vec<_>>()
                        .join(" / ")
                )));
            }
        }
        Ok(())
    }

    /// Keep only declared keys, trim strings, and carry saved secrets forward
    /// when the form left them blank. Unknown keys are dropped so a stale client
    /// cannot smuggle settings the type does not have.
    pub fn normalize(
        &self,
        config: &serde_json::Map<String, serde_json::Value>,
        saved: Option<&serde_json::Map<String, serde_json::Value>>,
    ) -> serde_json::Map<String, serde_json::Value> {
        let mut out = serde_json::Map::new();
        for field in self.fields {
            let value = config
                .get(field.key)
                .and_then(|value| value.as_str())
                .map(str::trim)
                .unwrap_or("");
            if value.is_empty() && field.secret {
                if let Some(saved) = saved
                    .and_then(|saved| saved.get(field.key))
                    .and_then(|value| value.as_str())
                    .filter(|value| !value.trim().is_empty())
                {
                    out.insert(
                        field.key.to_string(),
                        serde_json::Value::String(saved.to_string()),
                    );
                    continue;
                }
            }
            out.insert(
                field.key.to_string(),
                serde_json::Value::String(value.to_string()),
            );
        }
        out
    }

    /// Config with every secret replaced by a boolean flag, for the UI.
    #[must_use]
    pub fn public_config(
        &self,
        config: &serde_json::Map<String, serde_json::Value>,
    ) -> (serde_json::Map<String, serde_json::Value>, Vec<String>) {
        let mut out = serde_json::Map::new();
        let mut secrets_set = Vec::new();
        for field in self.fields {
            let value = config
                .get(field.key)
                .and_then(|value| value.as_str())
                .unwrap_or("")
                .to_string();
            if field.secret {
                if !value.trim().is_empty() {
                    secrets_set.push(field.key.to_string());
                }
                out.insert(
                    field.key.to_string(),
                    serde_json::Value::String(String::new()),
                );
            } else {
                out.insert(field.key.to_string(), serde_json::Value::String(value));
            }
        }
        (out, secrets_set)
    }
}

const BOOL: &[FieldOption] = &[
    FieldOption {
        value: "true",
        label: "是",
    },
    FieldOption {
        value: "false",
        label: "否",
    },
];

const SEND_GUARD: &[FieldOption] = &[
    FieldOption {
        value: "true",
        label: "允许",
    },
    FieldOption {
        value: "false",
        label: "禁止（只能读）",
    },
];

const MAIL_FIELDS: &[ConnectorField] = &[
    ConnectorField::text(
        "email",
        "邮箱地址",
        "要连接的邮箱账号；发信时也用它作为发件人",
        "me@company.com",
        true,
    ),
    ConnectorField::text("displayName", "发件人显示名", "留空则用邮箱地址", "", false),
    ConnectorField::text(
        "imapHost",
        "IMAP 服务器",
        "收信服务器，例如 imap.qq.com",
        "imap.qq.com",
        true,
    ),
    ConnectorField::number(
        "imapPort",
        "IMAP 端口",
        "SSL 一般是 993；STARTTLS 是 143",
        "993",
        true,
    ),
    ConnectorField::select(
        "imapSsl",
        "IMAP 使用 SSL",
        "993 端口选「是」；143 端口选「否」（自动 STARTTLS）",
        BOOL,
    ),
    ConnectorField::text(
        "smtpHost",
        "SMTP 服务器",
        "发信服务器，例如 smtp.qq.com",
        "smtp.qq.com",
        true,
    ),
    ConnectorField::number(
        "smtpPort",
        "SMTP 端口",
        "SSL 一般 465；STARTTLS 是 587",
        "465",
        true,
    ),
    ConnectorField::select(
        "smtpSsl",
        "SMTP 使用 SSL",
        "465 端口选「是」；587 端口选「否」（自动 STARTTLS）",
        BOOL,
    ),
    ConnectorField::text(
        "username",
        "登录账号",
        "通常就是邮箱地址（企业邮箱可能不同）",
        "me@company.com",
        true,
    ),
    ConnectorField::password(
        "password",
        "密码 / 授权码",
        "QQ / 163 / Gmail 需要「授权码」而不是登录密码；修改时留空表示不改",
        true,
    ),
    ConnectorField::select(
        "allowSend",
        "允许发信",
        "选「否」后 Agent 只能读、不能发",
        SEND_GUARD,
    ),
    ConnectorField::tags(
        "allowedRecipients",
        "发信白名单（可选）",
        "留空 = 不限制收件人；可填邮箱地址，或用 @company.com 允许整个域",
    ),
];

const NOTION_FIELDS: &[ConnectorField] = &[
    ConnectorField::password(
        "token",
        "集成令牌 / 个人访问令牌",
        "Notion → Settings → Connections 创建集成令牌（需把页面「添加连接」给它），修改时留空表示不改",
        true,
    ),
    ConnectorField::text(
        "workspaceName",
        "工作区名称（备注）",
        "仅用于界面显示",
        "",
        false,
    ),
    ConnectorField::text(
        "defaultPageId",
        "默认写入页面（可选）",
        "新建页面时的默认父页面；可粘贴完整链接",
        "",
        false,
    ),
    ConnectorField::number(
        "maxBlocksPerPage",
        "单页最多读取多少块",
        "默认 200；超大页面会截断，防止一次读爆上下文",
        "200",
        false,
    ),
    ConnectorField::select("allowWrite", "允许写入", "关闭后 Agent 只能读", SEND_GUARD),
    ConnectorField::tags(
        "allowedPageIds",
        "页面白名单（可选）",
        "留空 = 不限；填了就只允许读写这些页面",
    ),
];

const DINGTALK_FIELDS: &[ConnectorField] = &[
    ConnectorField::password(
        "appKey",
        "AppKey",
        "钉钉开放平台 → 应用 → 凭证与基础信息；应用需要文档相关权限",
        true,
    ),
    ConnectorField::password(
        "appSecret",
        "AppSecret",
        "与 AppKey 同一个位置；修改时留空表示不改",
        true,
    ),
    ConnectorField::text(
        "operatorId",
        "操作人 unionId",
        "以谁的钉钉身份操作，决定能看哪些文档",
        "",
        true,
    ),
    ConnectorField::text(
        "defaultWorkspaceId",
        "默认知识库（可选）",
        "新建文档时的默认落点；可粘贴知识库链接",
        "",
        false,
    ),
    ConnectorField::number(
        "maxBlocks",
        "单篇最多读取多少块",
        "默认 300；超大文档会截断",
        "300",
        false,
    ),
    ConnectorField::select("allowWrite", "允许写入", "关闭后只能读", SEND_GUARD),
];

/// Every connector type this build can configure.
#[must_use]
pub fn types() -> Vec<ConnectorType> {
    vec![
        ConnectorType {
            id: "MAIL",
            name: "邮件",
            description: "让 Agent 读取、搜索你的邮箱并代你发信（IMAP 收信 + SMTP 发信）",
            capabilities: &["read", "search", "send", "attachment"],
            fields: MAIL_FIELDS,
            tools: &[
                ("mail_list", "列出最近收到的邮件（发件人、主题、时间、uid）"),
                ("mail_read", "按 uid 读取一封邮件的正文"),
                ("mail_send", "以你的身份发送一封纯文本邮件"),
            ],
        },
        ConnectorType {
            id: "NOTION",
            name: "Notion",
            description: "让 Agent 搜索、读取、新建你的 Notion 页面（按你的权限访问）",
            capabilities: &["read", "search", "write"],
            fields: NOTION_FIELDS,
            tools: &[
                ("notion_search", "搜索可访问的页面与数据库"),
                ("notion_read_page", "读取一个页面的标题与正文块"),
                ("notion_create_page", "在指定父页面下新建一页（可选带正文）"),
            ],
        },
        ConnectorType {
            id: "DINGTALK_DOC",
            name: "钉钉文档",
            description: "让 Agent 搜索、读取你的钉钉文档 / 知识库（以你的钉钉身份操作）",
            capabilities: &["read", "search"],
            fields: DINGTALK_FIELDS,
            tools: &[
                ("dingtalk_search_docs", "按关键词搜索文档"),
                ("dingtalk_read_doc", "按 docKey 读取文档正文块"),
            ],
        },
    ]
}

/// Look up a type by id (case-insensitive).
#[must_use]
pub fn descriptor(id: &str) -> Option<ConnectorType> {
    types()
        .into_iter()
        .find(|kind| kind.id.eq_ignore_ascii_case(id.trim()))
}

/// One configured connector, as stored.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorRow {
    pub id: String,
    /// Type id: `MAIL` / `NOTION` / `DINGTALK_DOC`.
    pub kind: String,
    pub name: String,
    /// Stored settings, secrets included. Never serialise this to the UI as-is.
    #[serde(skip)]
    pub config: serde_json::Map<String, serde_json::Value>,
    pub enabled: bool,
    pub state: String,
    pub probed_at: Option<String>,
    pub last_error: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

/// Calls and failures of one connector tool over the report window.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorCallStat {
    pub connector_id: String,
    pub tool: String,
    pub calls: u64,
    pub failures: u64,
    pub last_at: String,
}

/// One recorded call, for the activity list.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConnectorCall {
    pub connector_id: String,
    pub tool: String,
    pub ok: bool,
    pub detail: Option<String>,
    pub at: String,
    pub duration_ms: i64,
}

/// Render a stored value for a log line: secrets never appear.#[must_use]
pub fn redact(kind: &ConnectorType, config: &serde_json::Map<String, serde_json::Value>) -> String {
    let mut parts = Vec::new();
    for field in kind.fields {
        let value = config
            .get(field.key)
            .and_then(|value| value.as_str())
            .unwrap_or("");
        if value.is_empty() {
            continue;
        }
        parts.push(if field.secret {
            format!("{}={}", field.key, "***")
        } else {
            format!("{}={value}", field.key)
        });
    }
    parts.join(", ")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(pairs: &[(&str, &str)]) -> serde_json::Map<String, serde_json::Value> {
        pairs
            .iter()
            .map(|(key, value)| {
                (
                    (*key).to_string(),
                    serde_json::Value::String((*value).into()),
                )
            })
            .collect()
    }

    #[test]
    fn every_type_declares_tools_and_required_keys() {
        for kind in types() {
            assert!(!kind.tools.is_empty(), "{} has no tools", kind.id);
            assert!(
                !kind.required_keys().is_empty(),
                "{} has no required",
                kind.id
            );
            assert!(descriptor(kind.id).is_some());
        }
        assert!(descriptor("nope").is_none());
    }

    #[test]
    fn required_fields_are_enforced_but_a_kept_secret_passes() {
        let mail = descriptor("MAIL").expect("mail");
        let err = mail
            .validate(&config(&[("email", "a@b.c")]), None)
            .unwrap_err();
        assert!(err.message().contains("IMAP"), "{}", err.message());

        let saved = config(&[("password", "stored")]);
        let draft = config(&[
            ("email", "a@b.c"),
            ("imapHost", "imap.b.c"),
            ("imapPort", "993"),
            ("smtpHost", "smtp.b.c"),
            ("smtpPort", "465"),
            ("username", "a@b.c"),
        ]);
        assert!(mail.validate(&draft, Some(&saved)).is_ok());
        let normalized = mail.normalize(&draft, Some(&saved));
        assert_eq!(
            normalized.get("password").and_then(|v| v.as_str()),
            Some("stored")
        );
    }

    #[test]
    fn select_values_and_numbers_are_checked() {
        let mail = descriptor("MAIL").expect("mail");
        let mut draft = config(&[
            ("email", "a@b.c"),
            ("imapHost", "imap.b.c"),
            ("imapPort", "not-a-port"),
            ("smtpHost", "smtp.b.c"),
            ("smtpPort", "465"),
            ("username", "a@b.c"),
            ("password", "x"),
        ]);
        let err = mail.validate(&draft, None).unwrap_err();
        assert!(err.message().contains("数字"), "{}", err.message());
        draft.insert("imapPort".into(), serde_json::Value::String("993".into()));
        draft.insert("imapSsl".into(), serde_json::Value::String("maybe".into()));
        let err = mail.validate(&draft, None).unwrap_err();
        assert!(err.message().contains("只能是"), "{}", err.message());
    }

    #[test]
    fn secrets_are_blanked_and_flags_reported() {
        let notion = descriptor("NOTION").expect("notion");
        let (public, set) = notion.public_config(&config(&[("token", "secret-token")]));
        assert_eq!(
            public.get("token").and_then(|v| v.as_str()),
            Some(""),
            "the token must not travel to the UI"
        );
        assert_eq!(set, vec!["token".to_string()]);
    }

    #[test]
    fn redaction_never_prints_a_secret() {
        let mail = descriptor("MAIL").expect("mail");
        let text = redact(
            &mail,
            &config(&[("email", "a@b.c"), ("password", "hunter2")]),
        );
        assert!(text.contains("password=***"));
        assert!(!text.contains("hunter2"));
    }
}
