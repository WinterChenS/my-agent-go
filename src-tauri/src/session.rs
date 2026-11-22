//! Session commands: the chat page's data path.
//!
//! Two SQLite connections, on purpose:
//!
//! * the **writer** owns the `SessionService` and is held for the whole of a
//!   run, because `send` needs `&mut self`;
//! * the **reader** answers list/detail/event queries from a second connection.
//!
//! The ledger is WAL, so a reader sees committed rows while a run is still
//! writing. Without the second connection the UI could not stream a token: the
//! only handle to the database would be locked by the very call it is waiting
//! on.
//!
//! The UI polls the ledger instead of relying on pushed IPC events. The ledger
//! is the source of truth anyway, and a poll that arrives late still shows every
//! token, whereas a dropped event would silently lose text.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, MutexGuard};

use anvil_session::audit::{self, AuditReport};
use anvil_session::export::{ExportFormat, ExportOptions, ExportReport};
use anvil_session::rail::RailView;
use anvil_session::{
    ApprovalDecision, Approvals, Cancels, CompactReport, ContextEntry, ContextRef, ContextUsage,
    Message, Steers,
    ProjectSummary, RunEvent, SessionContext, SessionDetail, SessionInput, SessionPage,
    SessionQuery, SessionService, SessionSpec, SessionSummary, Store,
};
use serde::Serialize;
use tauri::{AppHandle, Manager, State};

/// Session state shared by the commands.
pub struct SessionState {
    database: PathBuf,
    state_dir: PathBuf,
    /// The catalog holds the agent's persona. The ledger must not depend on the
    /// runtime inventory, so this is a path the host opens only when a session
    /// is created, not a shared service object.
    catalog_database: PathBuf,
    writer: Mutex<Option<SessionService>>,
    reader: Mutex<Option<Store>>,
    cancels: Mutex<Option<Cancels>>,
    steers: Mutex<Option<Steers>>,
    approvals: Mutex<Option<Approvals>>,
    init_error: Mutex<Option<String>>,
    /// Set once the connections exist. Reads and the cancel flag check this
    /// instead of the writer lock, which a running turn holds end to end.
    ready: AtomicBool,
    /// Serialises the one-time initialisation without touching the writer lock.
    init: Mutex<()>,
}

impl SessionState {
    /// Build the state; both connections open on first use, so window creation
    /// never waits for SQLite.
    pub fn new(database: PathBuf, state_dir: PathBuf, catalog_database: PathBuf) -> Self {
        Self {
            database,
            state_dir,
            catalog_database,
            writer: Mutex::new(None),
            reader: Mutex::new(None),
            cancels: Mutex::new(None),
            steers: Mutex::new(None),
            approvals: Mutex::new(None),
            init_error: Mutex::new(None),
            ready: AtomicBool::new(false),
            init: Mutex::new(()),
        }
    }

    /// Path of the ledger (for the startup log line).
    pub fn database(&self) -> &Path {
        &self.database
    }

    /// The persona catalog, read alongside the ledger by the workbench view.
    pub fn catalog_database(&self) -> &Path {
        &self.catalog_database
    }

    /// Where exports land: beside the ledger, so both live on the data drive
    /// and both are covered by the same backup.
    fn export_dir(&self) -> PathBuf {
        self.database
            .parent()
            .map(|dir| dir.join("exports"))
            .unwrap_or_else(|| PathBuf::from("exports"))
    }

    fn fail(&self, message: String) -> String {
        if let Ok(mut slot) = self.init_error.lock() {
            *slot = Some(message.clone());
        }
        message
    }

    fn ensure(&self) -> Result<(), String> {
        if let Some(error) = self.init_error.lock().ok().and_then(|slot| slot.clone()) {
            return Err(error);
        }
        if self.ready.load(Ordering::Acquire) {
            return Ok(());
        }
        // Initialisation needs the writer, but only until the service exists.
        // Taking the writer lock on *every* call made a running turn block the
        // history list, the session detail read and the stop button: `send`
        // holds that lock for the whole turn, which is minutes.
        let _guard = self
            .init
            .lock()
            .map_err(|_| "session: init lock poisoned".to_string())?;
        if self.ready.load(Ordering::Acquire) {
            return Ok(());
        }
        let mut writer = self
            .writer
            .lock()
            .map_err(|_| "session: state lock poisoned".to_string())?;
        if writer.is_some() {
            self.ready.store(true, Ordering::Release);
            return Ok(());
        }

        let store = Store::open(&self.database).map_err(|error| {
            self.fail(format!(
                "session: open {}: {error}",
                self.database.display()
            ))
        })?;
        let reader = Store::open(&self.database)
            .map_err(|error| self.fail(format!("session: open reader: {error}")))?;
        let mut service = SessionService::new(
            store,
            anvil_session::default_registry(),
            SessionContext {
                state_dir: self.state_dir.clone(),
                // No deadline: a turn ends when the runtime finishes, fails,
                // or the user stops it. A fixed budget cut long tool-using
                // turns mid-answer with "did not finish within 300s".
                run_timeout: None,
            },
        );
        // A run interrupted by the previous process is `failed`, not `running`:
        // otherwise the first send after a restart would be refused forever.
        let recovered = service
            .recover()
            .map_err(|error| self.fail(format!("session: recover: {error}")))?;
        if recovered > 0 {
            eprintln!("anvil: session: {recovered} interrupted run(s) marked failed");
        }
        let cancels = service.cancels();
        let steers = service.steers();
        let approvals = service.approvals();
        if let Ok(mut slot) = self.reader.lock() {
            *slot = Some(reader);
        }
        if let Ok(mut slot) = self.cancels.lock() {
            *slot = Some(cancels);
        }
        if let Ok(mut slot) = self.steers.lock() {
            *slot = Some(steers);
        }
        if let Ok(mut slot) = self.approvals.lock() {
            *slot = Some(approvals);
        }
        *writer = Some(service);
        self.ready.store(true, Ordering::Release);
        Ok(())
    }

    fn with_writer<T>(
        &self,
        action: impl FnOnce(&mut SessionService) -> Result<T, String>,
    ) -> Result<T, String> {
        self.ensure()?;
        let mut guard: MutexGuard<'_, Option<SessionService>> = self
            .writer
            .lock()
            .map_err(|_| "session: state lock poisoned".to_string())?;
        let service = guard
            .as_mut()
            .ok_or_else(|| "session: service is not initialised".to_string())?;
        action(service)
    }

    pub(crate) fn with_reader<T>(
        &self,
        action: impl FnOnce(&Store) -> Result<T, String>,
    ) -> Result<T, String> {
        self.ensure()?;
        let guard = self
            .reader
            .lock()
            .map_err(|_| "session: reader lock poisoned".to_string())?;
        let store = guard
            .as_ref()
            .ok_or_else(|| "session: reader is not initialised".to_string())?;
        action(store)
    }

    /// The saved agent and the skill paths it mounts.
    ///
    /// Skill names are what the page stores; pi needs the actual files, and
    /// those live in the catalog's skill inventory, so resolution happens here
    /// (on the machine that will run the process) rather than in React.
    fn agent_config(
        &self,
        agent_id: &str,
    ) -> Result<(anvil_catalog::model::AgentRecord, Vec<String>), String> {
        let store = anvil_catalog::Store::open(&self.catalog_database).map_err(|error| {
            format!(
                "agent: open catalog {}: {error}",
                self.catalog_database.display()
            )
        })?;
        let agent = store
            .agent(agent_id)
            .map_err(|error| format!("agent: read catalog: {error}"))?
            .ok_or_else(|| format!("agent {agent_id} is not in the catalog"))?;
        let wanted: std::collections::HashSet<&str> =
            agent.skills.iter().map(String::as_str).collect();
        let skills = store
            .skills()
            .map_err(|error| format!("agent: read skills: {error}"))?
            .into_iter()
            .filter(|skill| {
                skill.runtime_id == agent.runtime_id && wanted.contains(skill.name.as_str())
            })
            .map(|skill| skill.path)
            .collect();
        Ok((agent, skills))
    }

    /// The name, runtime and model a saved agent runs as.
    ///
    /// The schedule table stores these beside the id so a row still explains
    /// itself after the agent is renamed; this is the single place that reads
    /// them from the catalog, so the two can never disagree at creation time.
    pub(crate) fn agent_target(&self, agent_id: &str) -> Result<(String, String, String), String> {
        let (agent, _) = self.agent_config(agent_id)?;
        Ok((agent.name, agent.runtime_id, agent.model_id))
    }

    /// Open a session for a schedule: the agent's persona, its own directory.
    ///
    /// A scheduled run has no composer, so it must not inherit a project from
    /// whatever the user last had open; the service gives it an isolated
    /// directory under the workspace root when `cwd` is empty.
    pub(crate) fn open_scheduled(
        &self,
        agent_id: &str,
        title: &str,
    ) -> Result<SessionSummary, String> {
        let (agent, skills) = self.agent_config(agent_id)?;
        let memory = if agent.memory {
            self.memory_prompt(agent_id)?
        } else {
            String::new()
        };
        let spec = spec_for_agent(
            agent,
            skills,
            memory,
            String::new(),
            Some(title.to_string()),
        );
        self.with_writer(|service| {
            service
                .open(&spec)
                .map_err(|error| error.to_wire().to_string())
        })
    }

    /// Cross-session memory is a short block of the agent's own past conclusions.
    /// Truncation is by characters, never by byte index into UTF-8.
    fn memory_prompt(&self, agent_id: &str) -> Result<String, String> {
        let entries = self.with_reader(|store| {
            store
                .agent_memory(agent_id, 3)
                .map_err(|error| error.to_wire().to_string())
        })?;
        if entries.is_empty() {
            return Ok(String::new());
        }
        let mut text =
            String::from("以下是此 Agent 近期会话留下的结论摘要；仅供回忆，未经本次会话复核：\n");
        for (title, preview) in entries {
            let short: String = preview.chars().take(240).collect();
            let name: String = title.chars().take(80).collect();
            text.push_str("- ");
            text.push_str(&name);
            text.push('：');
            text.push_str(&short);
            text.push('\n');
        }
        Ok(text)
    }

    /// Answer a pending approval without touching the writer lock.
    fn resolve_approval(
        &self,
        session_id: &str,
        approval_id: &str,
        decision: ApprovalDecision,
    ) -> Result<(), String> {
        self.ensure()?;
        let guard = self
            .approvals
            .lock()
            .map_err(|_| "session: approval lock poisoned".to_string())?;
        guard
            .as_ref()
            .ok_or_else(|| "session: approvals are not initialised".to_string())?
            .resolve(session_id, approval_id, decision)
            .map_err(|error| error.to_wire().to_string())
    }

    /// The runtime's own context-window estimate for one session.
    fn context_usage(&self, session_id: &str) -> Result<Option<ContextUsage>, String> {
        self.with_writer(|service| {
            service
                .context_usage(session_id)
                .map_err(|error| error.to_wire().to_string())
        })
    }

    /// Write a message into the turn that is streaming right now.
    ///
    /// Deliberately outside the writer lock for the same reason as `abort`: the
    /// run holds that lock for its whole duration, so a steer queued behind it
    /// would arrive after the answer it was meant to change.
    fn steer(&self, session_id: &str, text: &str) -> Result<bool, String> {
        self.ensure()?;
        let guard = self
            .steers
            .lock()
            .map_err(|_| "session: steer lock poisoned".to_string())?;
        guard
            .as_ref()
            .ok_or_else(|| "session: steers are not initialised".to_string())?
            .steer(session_id, text)
            .map_err(|error| error.to_wire().to_string())
    }

    /// Flip the cancel flag for one session without touching the writer lock.
    fn abort(&self, session_id: &str) -> Result<bool, String> {
        self.ensure()?;
        let guard = self
            .cancels
            .lock()
            .map_err(|_| "session: cancel lock poisoned".to_string())?;
        Ok(guard
            .as_ref()
            .map(|cancels| cancels.cancel(session_id))
            .unwrap_or(false))
    }

    /// Directories the catalog currently lists as skill roots.
    ///
    /// A missing or not-yet-scanned catalog simply yields no roots: the chat
    /// still works, and an `@skill` reference is refused rather than silently
    /// reading an arbitrary path.
    fn skill_roots(&self) -> Vec<PathBuf> {
        let Ok(store) = anvil_catalog::Store::open(&self.catalog_database) else {
            return Vec::new();
        };
        let Ok(skills) = store.skills() else {
            return Vec::new();
        };
        let mut roots: Vec<PathBuf> = Vec::new();
        for skill in skills {
            let root = PathBuf::from(&skill.root);
            if !root.as_os_str().is_empty() && !roots.contains(&root) {
                roots.push(root);
            }
        }
        roots
    }

    pub(crate) fn send(
        &self,
        session_id: &str,
        text: &str,
        context: Vec<ContextRef>,
        permission: Option<String>,
        thinking: Option<String>,
    ) -> Result<SendReport, String> {
        let roots = self.skill_roots();
        self.with_writer(|service| {
            // Picked up on every turn so a skill installed after the window
            // opened is usable without restarting the app.
            service.set_skill_roots(roots);
            let outcome = service
                .send_with(
                    session_id,
                    &SessionInput {
                        prompt: text.to_string(),
                        context,
                        permission,
                        thinking,
                    },
                )
                .map_err(|error| error.to_wire().to_string())?;
            Ok(SendReport {
                run_id: outcome.run.id,
                status: outcome.run.status.as_str().to_string(),
                error: outcome.run.error,
                message: outcome.message,
                event_count: outcome.event_count,
            })
        })
    }
}

/// What one `session_send` produced.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SendReport {
    pub run_id: String,
    /// `completed`, `aborted` or `failed`.
    pub status: String,
    pub error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<Message>,
    pub event_count: usize,
}

/// One page of the session list (pinned first, then most recently updated).
///
/// The filters travel as one object: the UI sends the same shape it stores, and
/// a caller cannot swap two optional strings by accident.
#[tauri::command]
pub async fn session_list(
    state: State<'_, SessionState>,
    query: Option<SessionQuery>,
) -> Result<SessionPage, String> {
    let query = query.unwrap_or_default();
    state.with_reader(|store| {
        store
            .sessions(&query)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Projects the history panel groups sessions by.
#[tauri::command]
pub async fn session_projects(
    state: State<'_, SessionState>,
) -> Result<Vec<ProjectSummary>, String> {
    state.with_reader(|store| {
        store
            .projects()
            .map_err(|error| error.to_wire().to_string())
    })
}

/// One session with its messages and runs.
#[tauri::command]
pub async fn session_detail(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<SessionDetail, String> {
    state.with_reader(|store| {
        let session = store
            .session(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        let messages = store
            .messages(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        let runs = store
            .runs(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        Ok(SessionDetail {
            session,
            messages,
            runs,
        })
    })
}

/// Events after `after_seq`, so the UI can poll incrementally.
#[tauri::command]
pub async fn session_events(
    state: State<'_, SessionState>,
    session_id: String,
    after_seq: Option<i64>,
) -> Result<Vec<RunEvent>, String> {
    let after = after_seq.unwrap_or(0);
    state.with_reader(|store| {
        let mut events = store
            .events(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        events.retain(|event| event.seq > after);
        Ok(events)
    })
}

/// Create a session on one runtime. Nothing is sent yet.
///
/// With `agentId` the agent's saved persona becomes the session's persona: the
/// runtime, model, thinking level, system prompt, tool allowlist and skills are
/// read from the catalog once and stored on the session row. Every later turn
/// uses that row, so editing the agent afterwards does not rewrite a running
/// conversation.
#[tauri::command]
pub async fn session_open(
    state: State<'_, SessionState>,
    runtime_id: String,
    model: String,
    cwd: Option<String>,
    title: Option<String>,
    agent_id: Option<String>,
) -> Result<SessionSummary, String> {
    let persona = match agent_id.as_deref().filter(|id| !id.trim().is_empty()) {
        Some(id) => {
            let (agent, skills) = state.agent_config(id)?;
            let memory = if agent.memory {
                state.memory_prompt(id)?
            } else {
                String::new()
            };
            Some(spec_for_agent(
                agent,
                skills,
                memory,
                cwd.clone().unwrap_or_default(),
                title.clone(),
            ))
        }
        None => None,
    };
    state.with_writer(|service| {
        let spec = persona.unwrap_or_else(|| {
            // No explicit project: the service gives the conversation its own
            // isolated directory under the workspace root.
            model_spec(runtime_id, model, cwd.unwrap_or_default(), title)
        });
        service
            .open(&spec)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// What every session is told about the app it runs inside.
///
/// The pane's browser is driven by a command, not by a tool call. A model that
/// has never been told that command exists reaches for a browser it does know —
/// an MCP tool, the system default — and the page opens where the user cannot
/// see it. So the app writes this part of the prompt itself, for every session.
const PANE_TOOLS: &str = r##"## 本应用的浏览器（右侧面板）

这个应用的右侧有一个真实浏览器面板，用户就看着它。用户让你“打开 / 看看 / 操作某个网页”时，用这条命令，它会在右侧面板里打开并让用户看到：

  node ~/.anvil/bin/anvil-browser.mjs navigate '{"url":"https://example.com"}'

其它子命令：text（页面文字）、snapshot（可点元素）、click '{"text":"登录"}'、fill '{"selector":"#q","value":"…"}'、press、scroll、screenshot '{"path":"out/shot.png"}'（存成图片并返回路径，可用来核对页面到底长什么样）、back / forward / reload。

不要用外部浏览器（start / open / explorer / xdg-open）、系统默认浏览器或云电脑去打开网址：那会发生在用户看不见的地方，而用户要的是在应用里看到这个页面。

## 定时任务

用户说「每天 / 每周 / 明早九点 / 每小时帮我……」这类要按时间重复执行的请求时，直接创建定时任务，不要只给建议、也不要用 schtasks / cron / 系统计划任务：

  node ~/.anvil/bin/anvil-schedule.mjs create '{"name":"每日站会纪要","cron":"0 9 * * 1-5","prompt":"汇总今天的会议记录，按项目分组提炼结论、待办与阻塞","agent":"文档助手"}'

cron 为五段式：分 时 日 月 周（例：`0 9 * * 1-5` 工作日 9:00，`30 16 * * *` 每天 16:30）。agent 填当前 Agent 的名字，省略则用默认 Agent。其它子命令：list（看已有任务，创建前先查重）、pause / resume / remove '{"id":"sch-…"}'。

创建成功后，用一句话告诉用户任务名称与下次执行时间；执行结果会出现在「定时任务」页。

## 连接器（邮箱 / Notion / 钉钉文档）

用户在「连接器」页配置好的服务，不在你的工具列表里，要走这条命令：

  node ~/.anvil/bin/anvil-connector.mjs list
  node ~/.anvil/bin/anvil-connector.mjs call '{"connector":"cn-…","tool":"mail_list","params":{"count":5}}'

先 list 看已连接的连接器和它们的工具名、参数，再 call。邮件用 mail_list / mail_read / mail_send，Notion 用 notion_search / notion_read_page / notion_create_page，钉钉文档用 dingtalk_search_docs / dingtalk_read_doc。

连接器不是 MCP：MCP 是工具服务端，连接器是用用户自己的账号访问一个外部服务。用户说「帮我查邮件 / 读 Notion 页面 / 搜钉钉文档」时用连接器；没有对应连接器就如实说明还没配置，不要编造内容。"##;

/// Append the app's own instructions after whatever the persona says.
fn with_pane_tools(prompt: String) -> String {
    if prompt.trim().is_empty() {
        PANE_TOOLS.to_string()
    } else {
        format!("{prompt}\n\n{PANE_TOOLS}")
    }
}

/// The spec used when the user picks a model instead of a saved Agent.
///
/// Behaving like the CLI means the built-in tools are available. Only these
/// seven are named so `ask_question` cannot stall a headless run waiting for an
/// answer this build cannot give; a saved Agent still overrides the allowlist.
fn model_spec(
    runtime_id: String,
    model: String,
    cwd: String,
    title: Option<String>,
) -> SessionSpec {
    SessionSpec {
        runtime_id,
        model,
        cwd,
        title,
        allow_tools: true,
        tools: anvil_catalog::model::DEFAULT_TOOLS
            .iter()
            .map(|name| (*name).to_string())
            .collect(),
        append_system_prompt: with_pane_tools(String::new()),
        ..Default::default()
    }
}

/// Turn a saved agent into the exact [`SessionSpec`] the runtime will see.
///
/// Pure on purpose: the host's part of "the configuration takes effect" is a
/// mapping, and a mapping can be tested without a Tauri app or a real window.
/// The catalog lookup and the memory digest are done by the caller.
fn spec_for_agent(
    agent: anvil_catalog::model::AgentRecord,
    skills: Vec<String>,
    memory: String,
    cwd: String,
    title: Option<String>,
) -> SessionSpec {
    let allow_tools = agent.allows_tools();
    let tools = agent.effective_tools();
    SessionSpec {
        runtime_id: agent.runtime_id,
        model: agent.model_id,
        cwd,
        title: title
            .filter(|value| !value.trim().is_empty())
            .or(Some(agent.name)),
        native_id: None,
        agent_id: agent.slug,
        thinking: agent.thinking,
        system_prompt: agent.system_prompt,
        append_system_prompt: with_pane_tools(memory),
        skills,
        allow_tools,
        tools,
    }
}

/// Run records of one agent, read from the ledger (never sample data).
#[tauri::command]
pub async fn agent_runs(
    state: State<'_, SessionState>,
    agent_id: String,
    limit: Option<usize>,
) -> Result<Vec<anvil_session::model::AgentRun>, String> {
    state.with_reader(|store| {
        store
            .agent_runs(&agent_id, limit.unwrap_or(20).clamp(1, 100))
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Run one turn. Blocking on purpose: it runs on a blocking thread, and the UI
/// streams the interim text by polling [`session_events`].
///
/// `thinking` is `off` | `minimal` | `low` | `medium` | `high` | `xhigh` | `max`;
/// omitting it keeps whatever the session was opened with.
#[tauri::command]
pub async fn session_send(
    app: AppHandle,
    session_id: String,
    text: String,
    context: Option<Vec<ContextRef>>,
    permission: Option<String>,
    thinking: Option<String>,
) -> Result<SendReport, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SessionState>();
        state.send(
            &session_id,
            &text,
            context.unwrap_or_default(),
            permission,
            thinking,
        )
    })
    .await
    .map_err(|error| format!("io: the run task ended unexpectedly: {error}"))?
}

/// Answer one approval card.
///
/// This deliberately does not take the writer lock: the run that is waiting for
/// the answer holds it for the whole turn, and a stop/approve path that queued
/// behind it could never arrive.
#[tauri::command]
pub async fn session_approve(
    state: State<'_, SessionState>,
    session_id: String,
    approval_id: String,
    decision: ApprovalDecision,
) -> Result<(), String> {
    state.resolve_approval(&session_id, &approval_id, decision)
}

/// Answer a question the runtime asked on its own.
///
/// A runtime that runs headless still has to ask the human something: pi's
/// extensions reach for `ctx.ui`, which in RPC mode blocks the run until the
/// answer is written back. The answer therefore travels through the shared
/// registry rather than the service: the run that is waiting for it holds the
/// writer lock for the whole turn, and an answer that queued behind it could
/// never arrive.
#[tauri::command]
pub async fn session_extension_ui(
    session_id: String,
    request_id: String,
    answer: anvil_session::model::UiAnswer,
) -> Result<(), String> {
    anvil_session::ui::extension_uis()
        .resolve(&session_id, &request_id, answer)
}

/// Entries for the composer's `@` list, confined to the session's project.
#[tauri::command]
pub async fn session_context_list(
    state: State<'_, SessionState>,
    session_id: String,
    query: Option<String>,
    limit: Option<u32>,
) -> Result<Vec<ContextEntry>, String> {
    state.with_reader(|store| {
        let session = store
            .session(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        anvil_session::context::entries(
            &session.project_path,
            query.as_deref().unwrap_or(""),
            limit.unwrap_or(30) as usize,
        )
        .map_err(|error| error.to_wire().to_string())
    })
}

/// The right rail: execution rows, produced files, citations and the page this
/// build can preview.
///
/// A read, deliberately: the panels refresh while a turn is streaming, and a
/// rail that needed the write lock would freeze the transcript it describes.
#[tauri::command]
pub async fn session_rail(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<RailView, String> {
    state.with_reader(|store| {
        let session = store
            .session(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        let messages = store
            .messages(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        let runs = store
            .runs(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        let events = store
            .events(&session_id)
            .map_err(|error| error.to_wire().to_string())?;
        Ok(anvil_session::rail::build(
            &session.project_path,
            &messages,
            &runs,
            &events,
        ))
    })
}

/// Write the conversation to disk, under the application's own export folder.
///
/// The UI never sends a path: it picks a format, and the host decides where the
/// bytes go. That is what keeps an export from becoming an arbitrary write.
#[tauri::command]
pub async fn session_export(
    state: State<'_, SessionState>,
    session_id: String,
    format: String,
    include_tools: Option<bool>,
    overwrite: Option<bool>,
) -> Result<ExportReport, String> {
    let format = ExportFormat::from_label(&format)
        .ok_or_else(|| format!("export: unknown format {format:?}"))?;
    let options = ExportOptions {
        format,
        include_tools: include_tools.unwrap_or(false),
        overwrite: overwrite.unwrap_or(false),
    };
    let root = state.export_dir();
    state.with_writer(|service| {
        service
            .export(&session_id, &root, &options)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Ask the in-flight run to stop. `false` means nothing was running.
#[tauri::command]
pub async fn session_abort(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<bool, String> {
    state.abort(&session_id)
}

/// Write a message into the turn that is streaming right now.
///
/// Answers `true` when the runtime accepted it. `false` means there was nothing
/// live to steer — the turn ended between the click and the write — and the page
/// puts the text back in the queue instead of losing it.
#[tauri::command]
pub async fn session_steer(
    state: State<'_, SessionState>,
    session_id: String,
    text: String,
) -> Result<bool, String> {
    state.steer(&session_id, &text)
}

/// How full this session's context window is, according to pi itself.
#[tauri::command]
pub async fn session_context(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<Option<ContextUsage>, String> {
    state.context_usage(&session_id)
}

/// Read one file of a session's project for the preview pane.
#[tauri::command]
pub async fn file_preview(
    state: State<'_, SessionState>,
    session_id: String,
    path: String,
) -> Result<anvil_session::preview::FilePreview, String> {
    state.with_writer(|service| {
        service
            .preview(&session_id, &path)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Rename a session.
#[tauri::command]
pub async fn session_rename(
    state: State<'_, SessionState>,
    session_id: String,
    title: String,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .rename(&session_id, &title)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Pin or unpin a session in the history list.
#[tauri::command]
pub async fn session_pin(
    state: State<'_, SessionState>,
    session_id: String,
    pinned: bool,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .set_pinned(&session_id, pinned)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Archive a session out of the default list, or bring it back.
#[tauri::command]
pub async fn session_archive(
    state: State<'_, SessionState>,
    session_id: String,
    archived: bool,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .set_archived(&session_id, archived)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Delete a session and everything derived from it.
#[tauri::command]
pub async fn session_delete(
    state: State<'_, SessionState>,
    session_id: String,
) -> Result<(), String> {
    state.with_writer(|service| {
        service
            .delete(&session_id)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Branch a session at one message. The branch is a new session on the same
/// runtime, carrying the transcript up to `upto_seq` (default: the last one).
#[tauri::command]
pub async fn session_fork(
    state: State<'_, SessionState>,
    session_id: String,
    upto_seq: Option<i64>,
) -> Result<SessionSummary, String> {
    state.with_writer(|service| {
        service
            .fork(&session_id, upto_seq)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Fold all but the last `keep_last` messages into one summary row.
#[tauri::command]
pub async fn session_compact(
    state: State<'_, SessionState>,
    session_id: String,
    keep_last: u32,
) -> Result<CompactReport, String> {
    state.with_writer(|service| {
        service
            .compact(&session_id, keep_last as usize)
            .map_err(|error| error.to_wire().to_string())
    })
}

/// Read the observability/audit view out of the session ledger.
///
/// Every number is derived from rows a runtime wrote: usage events, runs and
/// tool calls. The host does not estimate tokens or cost, and a runtime that
/// reported neither shows up as "not reported" rather than as a measured zero.
#[tauri::command]
pub async fn session_audit(
    state: State<'_, SessionState>,
    period: Option<String>,
) -> Result<AuditReport, String> {
    let label = period.unwrap_or_else(|| "7".to_string());
    let period = audit::parse_period(&label).map_err(|error| error.to_wire().to_string())?;
    let today = anvil_session::clock::today();
    state.with_reader(|store| {
        audit::report(store.raw(), period, &today).map_err(|error| error.to_wire().to_string())
    })
}

/// Write the execution log to a CSV beside the ledger and report where it went.
#[tauri::command]
pub async fn session_audit_export(
    state: State<'_, SessionState>,
    period: Option<String>,
    mask: Option<bool>,
) -> Result<AuditExportReport, String> {
    let label = period.unwrap_or_else(|| "30".to_string());
    let period = audit::parse_period(&label).map_err(|error| error.to_wire().to_string())?;
    let today = anvil_session::clock::today();
    let view = state.with_reader(|store| {
        audit::report(store.raw(), period, &today).map_err(|error| error.to_wire().to_string())
    })?;
    let csv = audit::executions_csv(&view.executions, mask.unwrap_or(true));
    let root = state.export_dir();
    std::fs::create_dir_all(&root).map_err(|error| format!("audit export dir: {error}"))?;
    let name = format!("audit-{}-{}.csv", view.since, view.today);
    let path = root.join(&name);
    let temp = root.join(format!("{name}.tmp"));
    std::fs::write(&temp, csv.as_bytes()).map_err(|error| format!("audit export write: {error}"))?;
    std::fs::rename(&temp, &path).map_err(|error| format!("audit export replace: {error}"))?;
    Ok(AuditExportReport {
        path: path.to_string_lossy().to_string(),
        rows: view.executions.len(),
    })
}

/// Where an audit CSV landed, and how many execution rows it holds.
#[derive(Debug, Clone, serde::Serialize)]
pub struct AuditExportReport {
    pub path: String,
    pub rows: usize,
}

// A conversation with no explicit project no longer inherits one shared
// directory: `Service::open` gives it `<state_dir>/<session-id>` (an isolated
// per-conversation folder under `~/.anvil/workspace`).

#[cfg(test)]
mod tests {
    use super::*;

    /// Picking a model (rather than a saved Agent) used to send `--no-tools`,
    /// so the runtime could not call anything at all.
    #[test]
    fn picking_a_model_keeps_the_builtin_tools_available() {
        let spec = model_spec("pi".into(), "auto/fast".into(), "D:/work".into(), None);
        assert!(spec.allow_tools);
        assert_eq!(
            spec.tools,
            anvil_catalog::model::DEFAULT_TOOLS
                .iter()
                .map(|name| (*name).to_string())
                .collect::<Vec<_>>()
        );
        assert!(!spec.tools.iter().any(|tool| tool == "ask_question"));
    }

    /// The host maps a saved agent to the exact spec the runtime receives.
    #[test]
    fn a_saved_agent_becomes_the_session_spec() {
        let mut agent = anvil_catalog::model::AgentRecord::default_persona();
        agent.slug = "migration-engineer".into();
        agent.name = "迁移工程师".into();
        agent.runtime_id = "pi".into();
        agent.model_id = "auto/fast".into();
        agent.thinking = "high".into();
        agent.system_prompt = "你是迁移工程师".into();
        agent.tools = vec!["read".into(), "grep".into()];
        agent.memory = true;

        let spec = spec_for_agent(
            agent,
            vec!["D:/skills/review.md".into()],
            "历史结论摘要".into(),
            "D:/work/api".into(),
            None,
        );
        assert_eq!(spec.agent_id, "migration-engineer");
        assert_eq!(spec.title.as_deref(), Some("迁移工程师"));
        assert_eq!(spec.thinking, "high");
        assert_eq!(spec.system_prompt, "你是迁移工程师");
        // The persona's digest stays first; the app's own instructions follow.
        assert!(spec.append_system_prompt.starts_with("历史结论摘要"));
        assert!(spec.append_system_prompt.contains("anvil-browser.mjs navigate"));
        assert_eq!(spec.skills, vec!["D:/skills/review.md"]);
        assert_eq!(spec.tools, vec!["read", "grep"]);
        assert!(spec.allow_tools);
    }

    /// Every session learns how to open a page the user can see. Without this
    /// the model picks a browser it already knows and the page opens elsewhere.
    #[test]
    fn every_session_is_told_about_the_pane_browser() {
        let spec = model_spec(
            "pi".into(),
            "claude-sonnet-4".into(),
            "D:/work/api".into(),
            None,
        );
        assert!(spec.append_system_prompt.contains("anvil-browser.mjs navigate"));

        let agent = anvil_catalog::model::AgentRecord::default_persona();
        let with_memory = spec_for_agent(
            agent,
            Vec::new(),
            "历史结论摘要".into(),
            "D:/work/api".into(),
            None,
        );
        // The persona's own text is not replaced; the app appends to it.
        assert!(with_memory.append_system_prompt.starts_with("历史结论摘要"));
        assert!(with_memory.append_system_prompt.contains("anvil-browser.mjs navigate"));
    }

    /// Read-only is the one permission mode that changes runtime arguments.
    #[test]
    fn readonly_permission_narrows_the_tool_allowlist() {
        let mut agent = anvil_catalog::model::AgentRecord::default_persona();
        agent.permission = "readonly".into();
        agent.tools = vec!["bash".into()];
        let spec = spec_for_agent(agent, vec![], String::new(), "D:/work".into(), None);
        assert_eq!(
            spec.tools,
            vec!["read", "grep", "find", "ls"],
            "bash must not survive a read-only agent"
        );
        assert!(spec.allow_tools);
    }

    fn state(name: &str) -> SessionState {
        let dir = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join(".tmp")
            .join("test")
            .join("host-session")
            .join(name);
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create scratch dir");
        SessionState::new(
            dir.join("sessions.sqlite"),
            dir.join("state"),
            dir.join("catalog.sqlite"),
        )
    }

    #[test]
    fn the_reader_sees_an_empty_ledger_before_anything_runs() {
        let state = state("empty");
        let page = state
            .with_reader(|store| {
                store
                    .sessions(&SessionQuery::default())
                    .map_err(|error| error.to_wire().to_string())
            })
            .expect("list");
        assert_eq!(page.total, 0);
        assert!(page.sessions.is_empty());
        let projects = state
            .with_reader(|store| {
                store
                    .projects()
                    .map_err(|error| error.to_wire().to_string())
            })
            .expect("projects");
        assert!(projects.is_empty());
    }

    #[test]
    fn abort_on_a_session_without_a_run_reports_nothing_to_stop() {
        let state = state("abort");
        state.ensure().expect("init");
        assert!(!state.abort("nope").expect("abort"));
    }

    #[test]
    fn a_running_turn_does_not_block_reads_or_the_stop_button() {
        let state = state("while-running");
        state.ensure().expect("init");
        // `send` keeps the writer guard for the whole turn. The page still has to
        // list sessions, read a detail and press stop during those minutes, so
        // neither may go through that guard.
        let writer = state
            .writer
            .lock()
            .expect("hold the writer like a run does");
        let (listed, list_rx) = std::sync::mpsc::channel();
        let (stopped, stop_rx) = std::sync::mpsc::channel();
        std::thread::scope(|scope| {
            scope.spawn(|| {
                let outcome = state
                    .with_reader(|store| {
                        store
                            .sessions(&SessionQuery::default())
                            .map_err(|error| error.to_wire().to_string())
                    })
                    .map(|page| page.total);
                let _ = listed.send(outcome);
            });
            scope.spawn(|| {
                let _ = stopped.send(state.abort("any-session"));
            });
            let list = list_rx.recv_timeout(std::time::Duration::from_secs(2));
            let stop = stop_rx.recv_timeout(std::time::Duration::from_secs(2));
            // Release before asserting: a regression would otherwise leave the
            // helper threads waiting for a guard this thread still owns.
            drop(writer);
            assert!(
                matches!(list, Ok(Ok(0))),
                "listing sessions waited for the running turn: {list:?}"
            );
            assert!(
                matches!(stop, Ok(Ok(false))),
                "stopping waited for the running turn: {stop:?}"
            );
        });
    }

    #[test]
    fn an_unknown_session_is_reported_as_not_found() {
        let state = state("unknown");
        let error = state
            .with_reader(|store| {
                store
                    .session("no-such-session")
                    .map_err(|error| error.to_wire().to_string())
            })
            .expect_err("missing session must not resolve");
        // The wire shape keeps the code, so the UI can tell "gone" from
        // "database broken" without matching on prose.
        assert!(error.contains("\"code\":\"not_found\""), "{error}");
    }
    /// The composer's `@skill` reference is refused unless the host has told
    /// the ledger which directories are skill roots. This proves the roots are
    /// read from the live catalog (and de-duplicated), not hardcoded or empty.
    #[test]
    fn skill_roots_are_read_from_the_catalog_before_each_turn() {
        let dir = std::env::temp_dir().join(format!(
            "anvil-skill-roots-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("scratch dir");
        let root = dir.join("skills");
        std::fs::create_dir_all(root.join("code-review")).expect("skill dir");
        std::fs::write(root.join("code-review").join("SKILL.md"), "# review\n")
            .expect("skill file");

        let catalog = dir.join("catalog.sqlite");
        let mut store = anvil_catalog::Store::open(&catalog).expect("catalog");
        let record = |name: &str| anvil_catalog::model::SkillRecord {
            runtime_id: "pi".to_string(),
            name: name.to_string(),
            description: String::new(),
            root: root.to_string_lossy().into_owned(),
            path: root.join(name).to_string_lossy().into_owned(),
            state: "valid".to_string(),
            reason: None,
            updated_at: "2026-11-15T00:00:00Z".to_string(),
        };
        store
            .replace_skills(
                "pi",
                &[record("code-review"), record("dashi-ppt")],
                "2026-11-15T00:00:00Z",
            )
            .expect("write skills");

        let state = SessionState::new(
            dir.join("sessions.sqlite"),
            dir.join("state"),
            catalog.clone(),
        );
        assert_eq!(
            state.skill_roots(),
            vec![root.clone()],
            "roots de-duplicated"
        );

        // A catalog that does not exist yet must not break sending a message.
        let empty = SessionState::new(
            dir.join("sessions.sqlite"),
            dir.join("state"),
            dir.join("missing.sqlite"),
        );
        assert!(empty.skill_roots().is_empty());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
