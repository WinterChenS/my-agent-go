//! Orchestration: session lifecycle on top of the adapter registry.
//!
//! `open` creates the index row first, then asks the runtime; `send` opens a run
//! row, forwards adapter events into the ledger as they arrive, and closes the
//! run with a terminal status even when the adapter fails. A crash therefore
//! leaves "everything that arrived", never a half-written message.

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use std::path::PathBuf;
use std::sync::Condvar;

use crate::adapter::{AdapterEvent, Cancel, ContextUsage, Registry, SessionAdapter, Turn};
use crate::clock::{now_rfc3339, unix_millis};
use crate::context::{self, Prepared, Step};
use crate::error::{Error, Result};
use crate::model::{
    ApprovalDecision, ApprovalRequest, CompactReport, EventKind, Message, OpenedSession,
    ProjectSummary, Role, Run, RunEvent, RunStatus, SessionContext, SessionDetail, SessionInput,
    SessionPage, SessionQuery, SessionSpec, SessionStatus, SessionSummary,
};
use crate::store::{NewSession, Store};

static SESSION_COUNTER: AtomicU64 = AtomicU64::new(0);

/// Runs are created in the same millisecond when a case sends twice in a row;
/// the counter keeps their ids unique.
static RUN_COUNTER: AtomicU64 = AtomicU64::new(0);

/// Approval ids must not collide when two sessions ask in the same millisecond.
static APPROVAL_COUNTER: AtomicU64 = AtomicU64::new(0);

/// What one approval question says, separate from who is asking.
pub struct Question<'a> {
    pub kind: &'a str,
    pub title: &'a str,
    pub detail: &'a str,
}

/// Pending approval requests, addressable from the host while a run is blocked.
///
/// A run that waits for consent cannot hold the service lock: the UI has to be
/// able to answer through another command. The registry is therefore a separate
/// clonable handle, exactly like [`Cancels`], and the run only touches it to
/// register a question and wait.
#[derive(Clone, Default)]
pub struct Approvals {
    inner: Arc<Mutex<BTreeMap<String, Arc<Pending>>>>,
}

struct Pending {
    request: ApprovalRequest,
    decision: Mutex<Option<ApprovalDecision>>,
    signal: Condvar,
}

impl Approvals {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Register a question, publish it through `emit`, and wait for an answer.
    ///
    /// A timeout is a denial, not an approval: an unattended window must never
    /// widen what the model can read.
    fn ask(
        &self,
        session_id: &str,
        run_id: &str,
        question: Question<'_>,
        timeout: std::time::Duration,
        emit: &mut dyn FnMut(AdapterEvent),
    ) -> ApprovalDecision {
        let id = format!(
            "{session_id}:a{}-{}",
            unix_millis(),
            APPROVAL_COUNTER.fetch_add(1, Ordering::SeqCst)
        );
        let request = ApprovalRequest {
            id: id.clone(),
            session_id: session_id.to_string(),
            run_id: run_id.to_string(),
            kind: question.kind.to_string(),
            title: question.title.to_string(),
            detail: question.detail.to_string(),
            created_at: now_rfc3339(),
        };
        let pending = Arc::new(Pending {
            request: request.clone(),
            decision: Mutex::new(None),
            signal: Condvar::new(),
        });
        if let Ok(mut inner) = self.inner.lock() {
            inner.insert(id.clone(), pending.clone());
        }
        emit(AdapterEvent::new(
            EventKind::ApprovalRequest,
            serde_json::to_value(&request).unwrap_or(serde_json::Value::Null),
        ));

        let deadline = std::time::Instant::now() + timeout;
        let mut decision = pending.decision.lock().expect("approval lock");
        let mut timed_out = false;
        while decision.is_none() {
            let now = std::time::Instant::now();
            if now >= deadline {
                timed_out = true;
                break;
            }
            let (guard, _) = pending
                .signal
                .wait_timeout(decision, deadline - now)
                .expect("approval wait");
            decision = guard;
        }
        let answer = decision.unwrap_or(ApprovalDecision::Deny);
        drop(decision);
        if let Ok(mut inner) = self.inner.lock() {
            inner.remove(&id);
        }
        emit(AdapterEvent::new(
            EventKind::ApprovalResolved,
            serde_json::json!({
                "id": id,
                "decision": answer.as_str(),
                "timedOut": timed_out,
            }),
        ));
        answer
    }

    /// Answer one pending request.
    ///
    /// `session_id` is required and checked: an id from another session must not
    /// be answerable from this one, or a stale card could approve a path the
    /// user never saw.
    pub fn resolve(
        &self,
        session_id: &str,
        approval_id: &str,
        decision: ApprovalDecision,
    ) -> Result<()> {
        let pending = self
            .inner
            .lock()
            .map_err(|_| Error::Failed("approval registry poisoned".into()))?
            .get(approval_id)
            .cloned()
            .ok_or_else(|| Error::NotFound(format!("approval {approval_id:?}")))?;
        if pending.request.session_id != session_id {
            return Err(Error::Rejected(format!(
                "approval {approval_id:?} belongs to session {:?}, not {session_id:?}",
                pending.request.session_id
            )));
        }
        let mut slot = pending
            .decision
            .lock()
            .map_err(|_| Error::Failed("approval decision poisoned".into()))?;
        if slot.is_some() {
            return Err(Error::Rejected(format!(
                "approval {approval_id:?} was already answered"
            )));
        }
        *slot = Some(decision);
        pending.signal.notify_all();
        Ok(())
    }

    /// Answer every pending request of one session with `decision`.
    ///
    /// Called when a run is stopped: a card that is already on screen must not
    /// stay answerable for a run that no longer exists.
    pub fn resolve_session(&self, session_id: &str, decision: ApprovalDecision) -> usize {
        let pending: Vec<Arc<Pending>> = self
            .inner
            .lock()
            .map(|inner| {
                inner
                    .values()
                    .filter(|pending| pending.request.session_id == session_id)
                    .cloned()
                    .collect()
            })
            .unwrap_or_default();
        let mut answered = 0;
        for request in pending {
            if let Ok(mut slot) = request.decision.lock()
                && slot.is_none()
            {
                *slot = Some(decision);
                request.signal.notify_all();
                answered += 1;
            }
        }
        answered
    }

    /// Ids currently waiting, for diagnostics and tests.
    #[must_use]
    pub fn pending(&self) -> Vec<String> {
        self.inner
            .lock()
            .map(|inner| inner.keys().cloned().collect())
            .unwrap_or_default()
    }
}

/// Result of one `send`.
#[derive(Debug, Clone)]
pub struct SendOutcome {
    pub run: Run,
    pub message: Option<Message>,
    pub event_count: usize,
}

/// Cancellation handles, clonable on their own.
///
/// `send` needs `&mut self`, so the host cannot call `abort` through the same
/// handle while a run is in flight. The host keeps a clone of this registry
/// outside the service lock and flips the flag from another thread; the run
/// stops at the adapter's next chunk.
#[derive(Clone, Default)]
pub struct Cancels {
    inner: Arc<Mutex<BTreeMap<String, Cancel>>>,
}

impl Cancels {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Register (and replace) the handle for one session, returning a clone the
    /// run keeps.
    pub fn register(&self, session_id: &str) -> Cancel {
        let cancel = Cancel::new();
        if let Ok(mut inner) = self.inner.lock() {
            inner.insert(session_id.to_string(), cancel.clone());
        }
        cancel
    }

    /// Flip the flag for one session. `false` means nothing was running.
    #[must_use]
    pub fn cancel(&self, session_id: &str) -> bool {
        match self.inner.lock() {
            Ok(inner) => match inner.get(session_id) {
                Some(cancel) => {
                    cancel.cancel();
                    true
                }
                None => false,
            },
            Err(_) => false,
        }
    }

    pub fn forget(&self, session_id: &str) {
        if let Ok(mut inner) = self.inner.lock() {
            inner.remove(session_id);
        }
    }

    #[must_use]
    pub fn is_running(&self, session_id: &str) -> bool {
        self.inner
            .lock()
            .map(|inner| inner.contains_key(session_id))
            .unwrap_or(false)
    }
}

/// Live turns, reachable without the service lock.
///
/// `send` holds `&mut self` for the whole turn — minutes — so a steer routed
/// through the service could never arrive while the user needs it. The host
/// keeps a clone of this registry, exactly like [`Cancels`], and writes into the
/// adapter's own pipe from another thread.
/// What one live turn needs in order to accept a steer: the adapter that owns
/// the pipe, and the session it opened.
type SteerTarget = (Arc<dyn SessionAdapter>, OpenedSession);

#[derive(Clone, Default)]
pub struct Steers {
    inner: Arc<Mutex<BTreeMap<String, SteerTarget>>>,
}

impl Steers {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    pub fn register(&self, session_id: &str, adapter: Arc<dyn SessionAdapter>, opened: &OpenedSession) {
        if let Ok(mut inner) = self.inner.lock() {
            inner.insert(session_id.to_string(), (adapter, opened.clone()));
        }
    }

    /// Hand `text` to the run that is streaming. `Ok(false)` means nothing was
    /// running, or the runtime has no channel for it.
    pub fn steer(&self, session_id: &str, text: &str) -> Result<bool> {
        // Cloned out before the call: writing to a pipe is IO, and holding the
        // registry lock across it would make a second steer wait on the first.
        let target = match self.inner.lock() {
            Ok(inner) => inner.get(session_id).cloned(),
            Err(_) => None,
        };

        let Some((adapter, opened)) = target else {
            return Ok(false);
        };
        adapter.steer(&opened, text)
    }

    pub fn forget(&self, session_id: &str) {
        if let Ok(mut inner) = self.inner.lock() {
            inner.remove(session_id);
        }
    }

    #[must_use]
    pub fn is_live(&self, session_id: &str) -> bool {
        self.inner
            .lock()
            .map(|inner| inner.contains_key(session_id))
            .unwrap_or(false)
    }
}

/// The session facade the host commands call.
pub struct SessionService {
    store: Store,
    registry: Registry,
    context: SessionContext,
    cancels: Cancels,
    steers: Steers,
    approvals: Approvals,
    /// Roots a `skill` reference must live under. Empty means "no skills can be
    /// attached", which is the honest default: the crate does not know where the
    /// catalog put them.
    skill_roots: Vec<PathBuf>,
    /// How long an approval may stay unanswered before it is denied.
    approval_timeout: std::time::Duration,
}

impl SessionService {
    #[must_use]
    pub fn new(store: Store, registry: Registry, context: SessionContext) -> Self {
        Self {
            store,
            registry,
            context,
            cancels: Cancels::new(),
            steers: Steers::new(),
            approvals: Approvals::new(),
            skill_roots: Vec::new(),
            approval_timeout: std::time::Duration::from_secs(120),
        }
    }

    /// Roots a `skill` reference may point into, usually the catalog's skill
    /// directories.
    #[must_use]
    pub fn with_skill_roots(mut self, roots: Vec<PathBuf>) -> Self {
        self.skill_roots = roots;
        self
    }

    /// Refresh the directories an `@skill` reference may resolve inside.
    ///
    /// The catalog is scanned independently of the session ledger, so the
    /// roots can change while a session is open. The host calls this before
    /// each turn; without it every skill the composer attached would be
    /// refused by the context planner as "outside a configured root".
    pub fn set_skill_roots(&mut self, roots: Vec<PathBuf>) {
        self.skill_roots = roots;
    }

    /// Override the approval deadline (tests use a short one).
    #[must_use]
    pub fn with_approval_timeout(mut self, timeout: std::time::Duration) -> Self {
        self.approval_timeout = timeout;
        self
    }

    /// Pending approval questions, clonable for the host's answer path.
    #[must_use]
    pub fn approvals(&self) -> Approvals {
        self.approvals.clone()
    }

    #[must_use]
    pub fn registry(&self) -> &Registry {
        &self.registry
    }

    #[must_use]
    pub fn store(&self) -> &Store {
        &self.store
    }

    /// Mutable access for the host's recovery paths and for tests that need to
    /// recreate a state the ledger can only reach after a crash.
    #[doc(hidden)]
    pub fn store_mut(&mut self) -> &mut Store {
        &mut self.store
    }

    /// Cancellation handles; clone this before sending on another thread.
    #[must_use]
    pub fn cancels(&self) -> Cancels {
        self.cancels.clone()
    }

    /// The runtime's own estimate of how full this session's context is.
    ///
    /// A blank conversation has no session file to measure, so the page uses
    /// the model's advertised window instead; once a run has written its
    /// transcript this returns the real number.
    pub fn context_usage(&self, session_id: &str) -> Result<Option<ContextUsage>> {
        let summary = self.store.session(session_id)?;
        let adapter = self.registry.require(&summary.runtime_id)?;
        let opened = OpenedSession {
            runtime_id: summary.runtime_id,
            native_id: summary.native_id,
            model: summary.model,
            cwd: summary.project_path,
            allow_tools: summary.allow_tools,
            tools: summary.tools,
            ..OpenedSession::default()
        };
        adapter.context_usage(&self.context, &opened)
    }

    /// Steer handles; clone this before sending on another thread.
    pub fn steers(&self) -> Steers {
        self.steers.clone()
    }

    /// Mark runs left "running" by a previous process as failed.
    pub fn recover(&mut self) -> Result<usize> {
        self.store.fail_interrupted_runs(&now_rfc3339())
    }

    /// Create the index row and let the runtime confirm the session.
    ///
    /// When `spec.native_id` is set the existing native session is resumed and a
    /// new index row points at it; the two never share a row.
    pub fn open(&mut self, spec: &SessionSpec) -> Result<SessionSummary> {
        let adapter = self.registry.require(&spec.runtime_id)?;
        if spec.model.trim().is_empty() {
            return Err(Error::Rejected("a model must be named".into()));
        }

        let id = new_session_id(&spec.runtime_id);
        // A conversation with no explicit project runs inside its own isolated
        // directory: `<state_dir>/<session-id>`. One conversation's scratch files
        // and runtime transcript then never sit next to another's.
        let mut effective = spec.clone();
        if effective.cwd.trim().is_empty() {
            let dir = self.context.state_dir.join(&id);
            std::fs::create_dir_all(&dir).map_err(|error| {
                Error::Failed(format!("create session dir {}: {error}", dir.display()))
            })?;
            effective.cwd = dir.to_string_lossy().into_owned();
        }
        let title = spec
            .title
            .clone()
            .filter(|value| !value.trim().is_empty())
            .unwrap_or_else(|| default_title(&spec.model));
        let now = now_rfc3339();
        self.store.create_session(&NewSession {
            id: &id,
            runtime_id: &spec.runtime_id,
            title: &title,
            model: &spec.model,
            project_path: &effective.cwd,
            parent_id: "",
            branch_at: 0,
            allow_tools: spec.allow_tools,
            tools: &spec.tools,
            agent_id: &spec.agent_id,
            thinking: &spec.thinking,
            system_prompt: &spec.system_prompt,
            append_prompt: &spec.append_system_prompt,
            skills: &spec.skills,
            at: &now,
        })?;

        let opened = match adapter.open(&self.context, &effective) {
            Ok(opened) => opened,
            Err(error) => {
                // The row stays, marked failed: a session the user started that
                // the runtime refused is history, not a phantom.
                let _ = self
                    .store
                    .set_status(&id, SessionStatus::Failed, &now_rfc3339());
                return Err(error);
            }
        };
        self.store
            .set_native_id(&id, &opened.native_id, &now_rfc3339())?;
        self.store.append_event(
            &id,
            "",
            crate::model::EventKind::SessionStarted,
            &serde_json::json!({
                "runtimeId": opened.runtime_id,
                "nativeId": opened.native_id,
                "model": opened.model,
                // What the run will actually use, so the transcript can answer
                // "which agent and which thinking level produced this".
                "agentId": opened.agent_id,
                "thinking": opened.thinking,
                "systemPromptChars": opened.system_prompt.chars().count(),
                "skills": opened.skills.len(),
            }),
        )?;
        self.store.session(&id)
    }

    /// Run one turn. Blocking: the host calls this from a blocking task.
    pub fn send(&mut self, session_id: &str, prompt: &str) -> Result<SendOutcome> {
        self.send_with(
            session_id,
            &SessionInput {
                prompt: prompt.to_string(),
                context: Vec::new(),

                ..Default::default()
            },
        )
    }

    /// Run one turn with the context the user attached.
    ///
    /// References are resolved *before* the run row exists: a turn that was
    /// refused consent never happened, so it must not appear as a failed run.
    pub fn send_with(&mut self, session_id: &str, input: &SessionInput) -> Result<SendOutcome> {
        let summary = self.store.session(session_id)?;
        if summary.archived {
            return Err(Error::Rejected(format!(
                "session {session_id:?} is archived"
            )));
        }
        if summary.status == SessionStatus::Running {
            return Err(Error::Rejected(format!(
                "session {session_id:?} already has a run in flight"
            )));
        }
        let prompt = input.prompt.as_str();
        if prompt.trim().is_empty() {
            return Err(Error::Rejected("an empty prompt cannot be sent".into()));
        }
        // The first prompt is the session's name. "New chat · model" repeated
        // down the history list says nothing about what each conversation was,
        // so the ledger adopts the opening line once, and only while the title
        // is still the default the host invented.
        if summary.message_count == 0 && summary.title.trim_start().starts_with("New chat") {
            self.store
                .rename_session(session_id, &title_from_prompt(prompt), &now_rfc3339())?;
        }

        let adapter = self.registry.require(&summary.runtime_id)?;
        let cancel = self.cancels.register(session_id);
        // What this turn is allowed to do. Read at send time, not at open time:
        // the picker can change between turns, and a read-only turn must not
        // keep the shell a previous turn was allowed to use.
        let plan = crate::model::permission_plan(input.permission.as_deref());

        // ---- context references -----------------------------------------
        let mut references: Vec<Prepared> = Vec::new();
        if !input.context.is_empty() {
            let steps = context::plan(&summary.project_path, &self.skill_roots, &input.context)?;
            let approvals = self.approvals.clone();
            let timeout = self.approval_timeout;
            for step in steps {
                match step {
                    Step::Ready(prepared) => references.push(*prepared),
                    Step::Awaiting { reference, path } => {
                        // "full" means the user already trusted this workspace:
                        // reading outside it is the thing they asked for, so no
                        // card is raised. Every other mode keeps the question.
                        if plan.trusts_workspace {
                            references.push(context::allow(&reference, &path)?);
                            continue;
                        }
                        // A path outside the project is exactly what a stale
                        // card must not be able to approve, so the question is
                        // raised with the session's own identity.
                        let decision = approvals.ask(
                            session_id,
                            "",
                            Question {
                                kind: "read_outside_project",
                                title: "读取项目之外的路径",
                                detail: &path.to_string_lossy(),
                            },
                            timeout,
                            &mut |event| {
                                let _ = self.store.append_event(
                                    session_id,
                                    "",
                                    event.kind,
                                    &event.payload,
                                );
                            },
                        );
                        match decision {
                            ApprovalDecision::Allow => {
                                references.push(context::allow(&reference, &path)?);
                            }
                            ApprovalDecision::Deny => {
                                return Err(Error::Rejected(format!(
                                    "reading {} outside the project was denied",
                                    path.display()
                                )));
                            }
                            ApprovalDecision::Cancel => {
                                return Err(Error::Rejected(format!(
                                    "the approval for {} was closed without an answer",
                                    path.display()
                                )));
                            }
                        }
                    }
                }
            }
        }
        let images = context::attachments(&references);
        if !images.is_empty() && !adapter.supports_images() {
            // Dropping them quietly would answer a question about a picture
            // nobody looked at.
            return Err(Error::Rejected(format!(
                "the {} runtime cannot attach images; switch to a runtime that can, or remove {} from the composer",
                summary.runtime_id,
                images
                    .iter()
                    .map(|path| path.display().to_string())
                    .collect::<Vec<_>>()
                    .join(", ")
            )));
        }
        let context_meta = if references.is_empty() {
            None
        } else {
            Some(serde_json::json!({
                "context": references.iter().map(|entry| &entry.resolved).collect::<Vec<_>>(),
            }))
        };

        // A branch starts with the parent's transcript in our ledger but with an
        // empty runtime context. The first turn therefore carries that
        // transcript into the prompt once, so the branch can answer questions
        // about what happened before it. Later turns rely on the runtime's own
        // resumed context; sending it twice would double the token bill.
        let seed =
            if !summary.parent_id.trim().is_empty() && self.store.runs(session_id)?.is_empty() {
                branch_seed(&self.store.messages(session_id)?)
            } else {
                String::new()
            };
        let folded = context::fold(prompt, &references);
        let adapter_prompt = if seed.is_empty() {
            folded
        } else {
            format!("{seed}\n\n{folded}")
        };

        let run_id = format!(
            "{session_id}:r{}-{}",
            unix_millis(),
            RUN_COUNTER.fetch_add(1, Ordering::SeqCst)
        );
        let started_at = now_rfc3339();
        let run = Run {
            id: run_id.clone(),
            session_id: session_id.to_string(),
            status: RunStatus::Running,
            started_at: started_at.clone(),
            ended_at: None,
            error: None,
            prompt: prompt.to_string(),
        };
        self.store.start_run(&run)?;
        self.store
            .set_status(session_id, SessionStatus::Running, &started_at)?;

        // The user's own message lands before the runtime answers, so history is
        // readable even if the process dies during the turn.
        self.store.append_message(
            session_id,
            Some(&run_id),
            Role::User,
            prompt,
            &started_at,
            context_meta.as_ref(),
        )?;
        self.store.append_event(
            session_id,
            &run_id,
            crate::model::EventKind::TurnStarted,
            &serde_json::json!({
                "prompt": prompt,
                "seeded": !seed.is_empty(),
                "contextRefs": references.len(),
            }),
        )?;
        if !references.is_empty() {
            // The ledger keeps what was inlined, not the content itself: the
            // prompt is already in `runs.prompt`, and duplicating a file here
            // would double the size of every transcript.
            self.store.append_event(
                session_id,
                &run_id,
                crate::model::EventKind::ContextAttached,
                &serde_json::json!({
                    "refs": references.iter().map(|entry| &entry.resolved).collect::<Vec<_>>(),
                    "totalChars": references.iter().map(|entry| entry.resolved.chars).sum::<usize>(),
                }),
            )?;
        }

        // The persona comes from the session row, not from the caller: a turn
        // sent an hour later still runs as the agent it was opened with, even if
        // the page has since been edited to something else. The one field the
        // caller may override is the thinking level — the composer sets it per
        // question, and an unknown value falls back to the persona instead of
        // failing the turn.
        let persona = self.store.persona(session_id)?;
        let thinking = crate::model::thinking_level(input.thinking.as_deref())
            .map(str::to_string)
            .unwrap_or(persona.thinking);
        let opened = OpenedSession {
            runtime_id: summary.runtime_id.clone(),
            native_id: summary.native_id.clone(),
            model: summary.model.clone(),
            cwd: summary.project_path.clone(),
            allow_tools: summary.allow_tools,
            // A read-only turn gets the runtime's read-only tool set; every
            // other mode keeps whatever the session (or its agent) was opened
            // with, so a saved allowlist still wins over the picker.
            tools: if plan.read_only {
                crate::model::READ_ONLY_TOOLS
                    .iter()
                    .map(|name| (*name).to_string())
                    .collect()
            } else {
                summary.tools.clone()
            },
            agent_id: persona.agent_id,
            thinking,
            system_prompt: persona.system_prompt,
            append_system_prompt: persona.append_prompt,
            skills: persona.skills,
            project_trust: plan.project_trust,
        };

        // Registered before the first byte is read, so the page can steer the
        // run from the moment it starts.
        self.steers
            .register(session_id, adapter.clone(), &opened);

        let mut event_count = 0usize;
        let mut deltas = String::new();
        // Set once the adapter returned. Anything the runtime managed to emit
        // after that is late: writing it would append rows behind the terminal
        // `turn_completed` and make the transcript look like it kept talking
        // after the user stopped it.
        let closed = std::sync::atomic::AtomicBool::new(false);
        // A stop is a stop: text that the runtime was still producing when the
        // user pressed it must not keep streaming into the bubble.
        let stopwatch = cancel.clone();
        let mut late = 0usize;
        let mut sink = |event: AdapterEvent| {
            if closed.load(Ordering::SeqCst) {
                return;
            }
            if stopwatch.is_cancelled() && event.kind == EventKind::TextDelta {
                late += 1;
                return;
            }
            event_count += 1;
            // A steer is part of the record, not a transient instruction: the
            // reader reopening this conversation must see what was said while
            // the turn was still running. It is written before the event so the
            // two rows keep the order they happened in.
            if event.kind == crate::model::EventKind::UserSteer
                && let Some(text) = event.payload.get("text").and_then(|value| value.as_str())
                && !text.trim().is_empty()
            {
                let _ = self.store.append_message(
                    session_id,
                    Some(&run_id),
                    Role::User,
                    text,
                    &now_rfc3339(),
                    Some(&serde_json::json!({ "steer": true })),
                );
            }
            if event.kind == crate::model::EventKind::TextDelta
                && let Some(delta) = event.payload.get("delta").and_then(|value| value.as_str())
            {
                deltas.push_str(delta);
            }
            // Ledger writes are cheap (WAL) and keep the UI live; an error here
            // cannot be reported through the sink, so it is retried on flush.
            let _ = self
                .store
                .append_event(session_id, &run_id, event.kind, &event.payload);
        };

        let turn = Turn {
            prompt: &adapter_prompt,
            images: &images,
        };
        let outcome = adapter.send_turn(&self.context, &opened, &turn, &cancel, &mut sink);
        closed.store(true, Ordering::SeqCst);
        // Nothing can be steered after the turn ended; the next send registers
        // again. Keeping the entry would let a stray steer write into a pipe
        // nobody is reading.
        self.steers.forget(session_id);
        let ended_at = now_rfc3339();

        let (status, text, error, minted) = match outcome {
            Ok(outcome) => (
                outcome.status,
                outcome.text,
                outcome.error,
                outcome.native_id,
            ),
            Err(error) => (
                RunStatus::Failed,
                String::new(),
                Some(error.to_string()),
                None,
            ),
        };
        if let Some(native_id) = minted
            && summary.native_id.trim().is_empty()
        {
            self.store
                .set_native_id(session_id, &native_id, &ended_at)?;
        }
        let final_text = if text.is_empty() { deltas } else { text };

        let message = if final_text.trim().is_empty() {
            None
        } else {
            let seq = self.store.append_message(
                session_id,
                Some(&run_id),
                Role::Assistant,
                &final_text,
                &ended_at,
                None,
            )?;
            Some(Message {
                id: format!("{session_id}:m{seq}"),
                session_id: session_id.to_string(),
                run_id: Some(run_id.clone()),
                role: Role::Assistant,
                text: final_text.clone(),
                seq,
                created_at: ended_at.clone(),
                meta: None,
            })
        };

        self.store
            .finish_run(&run_id, status, &ended_at, error.as_deref())?;
        let session_status = match status {
            RunStatus::Completed => SessionStatus::Idle,
            RunStatus::Aborted => SessionStatus::Aborted,
            RunStatus::Failed | RunStatus::Running => SessionStatus::Failed,
        };
        self.store
            .set_status(session_id, session_status, &ended_at)?;
        self.store.append_event(
            session_id,
            &run_id,
            if status == RunStatus::Aborted {
                crate::model::EventKind::Aborted
            } else {
                crate::model::EventKind::TurnCompleted
            },
            &serde_json::json!({
                "status": status.as_str(),
                "error": error,
                "lateDeltas": late,
            }),
        )?;
        self.cancels.forget(session_id);
        // A card that outlived its run must not stay answerable.
        self.approvals
            .resolve_session(session_id, ApprovalDecision::Cancel);

        Ok(SendOutcome {
            run: Run {
                status,
                ended_at: Some(ended_at),
                error,
                ..run
            },
            message,
            event_count,
        })
    }

    /// Ask the in-flight run to stop. Returns `Ok(false)` when nothing was
    /// running, so the caller can tell "stop sent" from "nothing to stop".
    pub fn abort(&self, session_id: &str) -> Result<bool> {
        let stopped = self.cancels.cancel(session_id);
        if stopped {
            self.approvals
                .resolve_session(session_id, ApprovalDecision::Cancel);
        }
        Ok(stopped)
    }

    /// Answer one approval card. `session_id` must own the request.
    pub fn resolve_approval(
        &self,
        session_id: &str,
        approval_id: &str,
        decision: ApprovalDecision,
    ) -> Result<()> {
        self.approvals.resolve(session_id, approval_id, decision)
    }

    /// The right rail, aggregated from this session's own ledger.
    ///
    /// Read-only by construction: it opens no runtime and writes nothing, so
    /// the panels can refresh while a turn is still streaming.
    pub fn rail(&self, session_id: &str) -> Result<crate::rail::RailView> {
        let summary = self.store.session(session_id)?;
        let messages = self.store.messages(session_id)?;
        let runs = self.store.runs(session_id)?;
        let events = self.store.events(session_id)?;
        Ok(crate::rail::build(
            &summary.project_path,
            &messages,
            &runs,
            &events,
        ))
    }

    /// Write one conversation to `root` in the requested format.
    ///
    /// The root is chosen by the caller (the host), never by the transcript, and
    /// the file name is derived here: an export cannot be talked into writing
    /// somewhere else by a session whose title looks like a path.
    pub fn export(
        &self,
        session_id: &str,
        root: &std::path::Path,
        options: &crate::export::ExportOptions,
    ) -> Result<crate::export::ExportReport> {
        let session = self.store.session(session_id)?;
        let messages = self.store.messages(session_id)?;
        let runs = self.store.runs(session_id)?;
        let events = self.store.events(session_id)?;
        let (file_name, content, stats) =
            crate::export::render(&session, &messages, &runs, &events, options);
        let path = crate::export::write(root, &file_name, &content, options.overwrite)?;
        Ok(crate::export::ExportReport {
            path: path.display().to_string(),
            root: root.display().to_string(),
            file_name,
            bytes: content.len() as u64,
            sha256: crate::export::digest(&content),
            format: options.format.as_str().to_string(),
            messages: stats.messages,
            runs: stats.runs,
            tool_rows: stats.tool_rows,
            tools_included: options.include_tools,
        })
    }

    /// Entries for the composer's `@` list, confined to the session's project.
    pub fn context_entries(
        &self,
        session_id: &str,
        query: &str,
        limit: usize,
    ) -> Result<Vec<crate::model::ContextEntry>> {
        let summary = self.store.session(session_id)?;
        context::entries(&summary.project_path, query, limit)
    }

    pub fn detail(&self, session_id: &str) -> Result<SessionDetail> {
        Ok(SessionDetail {
            session: self.store.session(session_id)?,
            messages: self.store.messages(session_id)?,
            runs: self.store.runs(session_id)?,
        })
    }

    pub fn events(&self, session_id: &str) -> Result<Vec<RunEvent>> {
        self.store.events(session_id)
    }

    pub fn page(&self, query: &SessionQuery) -> Result<SessionPage> {
        self.store.sessions(query)
    }

    /// Projects the history panel groups sessions by, most recently used first.
    pub fn projects(&self) -> Result<Vec<ProjectSummary>> {
        self.store.projects()
    }

    /// Branch a session at `upto_seq` (default: the last message).
    ///
    /// The new session is a real session on the same runtime: it gets its own
    /// index row and its own native conversation, and the transcript up to the
    /// branch point is copied into its ledger. The runtime is not forked, so the
    /// first turn replays the carried transcript as context (see `send`).
    pub fn fork(&mut self, session_id: &str, upto_seq: Option<i64>) -> Result<SessionSummary> {
        let source = self.store.session(session_id)?;
        if source.archived {
            return Err(Error::Rejected(format!(
                "session {session_id:?} is archived; unarchive it before branching"
            )));
        }
        let messages = self.store.messages(session_id)?;
        let upto = upto_seq.unwrap_or_else(|| {
            messages
                .last()
                .map(|message| message.seq)
                .unwrap_or_default()
        });
        let carried: Vec<Message> = messages
            .into_iter()
            .filter(|message| message.seq <= upto)
            .collect();
        if carried.is_empty() {
            return Err(Error::Rejected(format!(
                "session {session_id:?} has nothing to branch at seq {upto}"
            )));
        }
        let branch_at = carried
            .last()
            .map(|message| message.seq)
            .unwrap_or_default();

        let adapter = self.registry.require(&source.runtime_id)?;
        let id = new_session_id(&source.runtime_id);
        let now = now_rfc3339();
        let title = format!("{} · branch", source.title.trim());
        // A branch is the same agent on a diverged transcript: the persona is
        // copied with the messages, or the branch would answer as somebody else.
        let persona = self.store.persona(&source.id)?;
        self.store.create_session(&NewSession {
            id: &id,
            runtime_id: &source.runtime_id,
            title: &title,
            model: &source.model,
            project_path: &source.project_path,
            parent_id: &source.id,
            branch_at,
            allow_tools: source.allow_tools,
            tools: &source.tools,
            agent_id: &persona.agent_id,
            thinking: &persona.thinking,
            system_prompt: &persona.system_prompt,
            append_prompt: &persona.append_prompt,
            skills: &persona.skills,
            at: &now,
        })?;

        // A fresh native conversation: the branch must be able to diverge
        // without rewriting the parent's runtime history.
        let spec = SessionSpec {
            runtime_id: source.runtime_id.clone(),
            model: source.model.clone(),
            cwd: source.project_path.clone(),
            title: Some(title.clone()),
            native_id: None,
            allow_tools: source.allow_tools,
            tools: source.tools.clone(),
            agent_id: persona.agent_id,
            thinking: persona.thinking,
            system_prompt: persona.system_prompt,
            append_system_prompt: persona.append_prompt,
            skills: persona.skills,
        };
        match adapter.open(&self.context, &spec) {
            Ok(opened) => {
                self.store
                    .set_native_id(&id, &opened.native_id, &now_rfc3339())?;
            }
            Err(error) => {
                let _ = self
                    .store
                    .set_status(&id, SessionStatus::Failed, &now_rfc3339());
                return Err(error);
            }
        }

        let copied: Vec<Message> = carried
            .iter()
            .enumerate()
            .map(|(index, message)| Message {
                id: format!("{id}:m{}", index + 1),
                session_id: id.clone(),
                run_id: None,
                role: message.role,
                text: message.text.clone(),
                seq: (index + 1) as i64,
                created_at: message.created_at.clone(),
                meta: Some(serde_json::json!({
                    "carriedFrom": source.id,
                    "carriedSeq": message.seq,
                })),
            })
            .collect();
        self.store.replace_messages(&id, &copied)?;
        self.store.append_event(
            &id,
            "",
            crate::model::EventKind::SessionStarted,
            &serde_json::json!({
                "runtimeId": source.runtime_id,
                "nativeId": self.store.session(&id)?.native_id,
                "model": source.model,
                "parentId": source.id,
                "branchAt": branch_at,
                "carried": copied.len(),
            }),
        )?;
        self.store.session(&id)
    }

    /// Fold everything but the last `keep_last` messages into one summary row.
    ///
    /// This compacts **our ledger**, which is what the history panel and the
    /// export read. The runtime's own context is not touched — no runtime in
    /// this build exposes a context-rewrite call — so the next turn still sees
    /// the full native conversation. The event payload says exactly that.
    pub fn compact(&mut self, session_id: &str, keep_last: usize) -> Result<CompactReport> {
        let summary = self.store.session(session_id)?;
        if summary.status == SessionStatus::Running {
            return Err(Error::Rejected(format!(
                "session {session_id:?} has a run in flight"
            )));
        }
        let keep_last = keep_last.max(1);
        let messages = self.store.messages(session_id)?;
        if messages.len() <= keep_last {
            return Err(Error::Rejected(format!(
                "session {session_id:?} has {} message(s); nothing to compact with keep_last={keep_last}",
                messages.len()
            )));
        }
        let split = messages.len() - keep_last;
        let (head, tail) = messages.split_at(split);
        let now = now_rfc3339();
        let mut kept: Vec<Message> = Vec::with_capacity(tail.len() + 1);
        kept.push(Message {
            id: format!("{session_id}:m1"),
            session_id: session_id.to_string(),
            run_id: None,
            role: Role::System,
            text: compaction_summary(head),
            seq: 1,
            created_at: now.clone(),
            meta: Some(serde_json::json!({
                "compacted": true,
                "collapsed": head.len(),
                "nativeContext": "untouched",
            })),
        });
        for (index, message) in tail.iter().enumerate() {
            kept.push(Message {
                id: format!("{session_id}:m{}", index + 2),
                session_id: session_id.to_string(),
                run_id: message.run_id.clone(),
                role: message.role,
                text: message.text.clone(),
                seq: (index + 2) as i64,
                created_at: message.created_at.clone(),
                meta: message.meta.clone(),
            });
        }
        self.store.replace_messages(session_id, &kept)?;
        self.store.append_event(
            session_id,
            "",
            crate::model::EventKind::Compacted,
            &serde_json::json!({
                "collapsed": head.len(),
                "kept": tail.len(),
                "nativeContext": "untouched",
            }),
        )?;
        Ok(CompactReport {
            collapsed: head.len(),
            kept: tail.len(),
            summary_seq: 1,
        })
    }

    pub fn rename(&mut self, session_id: &str, title: &str) -> Result<()> {
        if title.trim().is_empty() {
            return Err(Error::Rejected("a session title must not be empty".into()));
        }
        self.store
            .rename_session(session_id, title.trim(), &now_rfc3339())
    }

    pub fn set_pinned(&mut self, session_id: &str, pinned: bool) -> Result<()> {
        self.store.set_pinned(session_id, pinned, &now_rfc3339())
    }

    pub fn set_archived(&mut self, session_id: &str, archived: bool) -> Result<()> {
        self.store
            .set_archived(session_id, archived, &now_rfc3339())
    }

    pub fn delete(&mut self, session_id: &str) -> Result<()> {
        self.store.delete_session(session_id)
    }
}

fn new_session_id(runtime_id: &str) -> String {
    let counter = SESSION_COUNTER.fetch_add(1, Ordering::SeqCst);
    format!("{runtime_id}-{}-{counter}", unix_millis())
}

/// How much of the parent transcript the first branch turn carries.
///
/// Bounded on purpose: an unbounded replay would spend the branch's whole
/// prompt budget on history the user may not even need.
const SEED_LIMIT: usize = 4_000;

/// Per-message cap inside the seed, so one huge answer cannot crowd out the
/// rest of the transcript.
const SEED_MESSAGE_LIMIT: usize = 400;

/// Render the parent transcript as context for a branch's first turn.
fn branch_seed(messages: &[Message]) -> String {
    if messages.is_empty() {
        return String::new();
    }
    let mut picked: Vec<String> = Vec::new();
    let mut budget = SEED_LIMIT;
    // Walk backwards: when the parent is longer than the budget, the messages
    // closest to the branch point are the ones that matter.
    for message in messages.iter().rev() {
        let speaker = match message.role {
            Role::User => "user",
            Role::Assistant => "assistant",
            Role::System => "system",
            Role::Tool => "tool",
        };
        let text: String = message.text.chars().take(SEED_MESSAGE_LIMIT).collect();
        let line = format!("{speaker}: {text}");
        if line.len() > budget {
            picked.push("… (earlier messages omitted)".to_string());
            break;
        }
        budget -= line.len();
        picked.push(line);
    }
    picked.reverse();
    format!(
        "The following is the earlier conversation this branch continues. \
         Treat it as context; do not repeat it.\n\n{}",
        picked.join("\n\n")
    )
}

/// One deterministic paragraph standing in for the messages it replaced.
fn compaction_summary(head: &[Message]) -> String {
    let first_user = head
        .iter()
        .find(|message| message.role == Role::User)
        .map(|message| clip(&message.text, 120))
        .unwrap_or_default();
    let last_assistant = head
        .iter()
        .rev()
        .find(|message| message.role == Role::Assistant)
        .map(|message| clip(&message.text, 160))
        .unwrap_or_default();
    format!(
        "Context compacted: {} earlier message(s) folded into this summary. \
         First request: {first_user}. Last reply: {last_assistant}. \
         The runtime still holds the full conversation; only this ledger was shortened.",
        head.len()
    )
}

fn clip(text: &str, limit: usize) -> String {
    let flat = text.split_whitespace().collect::<Vec<_>>().join(" ");
    if flat.chars().count() <= limit {
        flat
    } else {
        format!("{}…", flat.chars().take(limit).collect::<String>())
    }
}

fn default_title(model: &str) -> String {
    let trimmed = model.rsplit('/').next().unwrap_or(model);
    format!("New chat · {trimmed}")
}

/// A history-sized name taken from the opening prompt.
///
/// `clip` counts characters, not bytes, and flattens newlines: a byte slice
/// would panic in the middle of a CJK prompt, and a name with a line break
/// breaks the one-line row.
fn title_from_prompt(prompt: &str) -> String {
    let title = clip(prompt.trim(), 40);
    if title.is_empty() {
        "New chat".to_string()
    } else {
        title
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::adapter::{AdapterEvent, RunOutcome, SessionAdapter};
    use crate::model::{EventKind, SessionCapabilities};

    struct Echo {
        id: String,
    }

    impl SessionAdapter for Echo {
        fn id(&self) -> &str {
            &self.id
        }

        fn capabilities(&self) -> SessionCapabilities {
            SessionCapabilities::full()
        }

        fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession> {
            Ok(OpenedSession {
                runtime_id: spec.runtime_id.clone(),
                native_id: format!("native-{}", spec.model),
                model: spec.model.clone(),
                cwd: spec.cwd.clone(),
                allow_tools: spec.allow_tools,
                tools: spec.tools.clone(),
                ..Default::default()
            })
        }

        fn send(
            &self,
            _context: &SessionContext,
            _opened: &OpenedSession,
            prompt: &str,
            _cancel: &Cancel,
            sink: &mut dyn FnMut(AdapterEvent),
        ) -> Result<RunOutcome> {
            sink(AdapterEvent::text_delta("echo:"));
            sink(AdapterEvent::new(
                EventKind::Usage,
                serde_json::json!({ "totalTokens": 3 }),
            ));
            Ok(RunOutcome::completed(&format!("echo:{prompt}"), None))
        }
    }

    fn service() -> SessionService {
        let mut registry = Registry::new();
        registry
            .register(std::sync::Arc::new(Echo { id: "pi".into() }))
            .expect("register");
        SessionService::new(
            Store::open_in_memory().expect("store"),
            registry,
            SessionContext {
                state_dir: std::path::PathBuf::from(".tmp/test/session"),
                run_timeout: Some(std::time::Duration::from_secs(5)),
            },
        )
    }

    fn spec() -> SessionSpec {
        SessionSpec {
            runtime_id: "pi".into(),
            model: "auto/gpt-5.6-sol".into(),
            cwd: "D:\\project".into(),
            title: None,
            native_id: None,
            allow_tools: false,
            tools: Vec::new(),
            ..Default::default()
        }
    }

    /// Records the thinking level each turn actually ran with.
    struct ThinkingProbe {
        seen: std::sync::Arc<std::sync::Mutex<Vec<String>>>,
    }

    impl SessionAdapter for ThinkingProbe {
        fn id(&self) -> &str {
            "pi"
        }

        fn capabilities(&self) -> SessionCapabilities {
            SessionCapabilities::full()
        }

        fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession> {
            Ok(OpenedSession {
                runtime_id: spec.runtime_id.clone(),
                native_id: "native-probe".into(),
                model: spec.model.clone(),
                cwd: spec.cwd.clone(),
                allow_tools: spec.allow_tools,
                tools: spec.tools.clone(),
                thinking: spec.thinking.clone(),
                ..Default::default()
            })
        }

        fn send(
            &self,
            _context: &SessionContext,
            opened: &OpenedSession,
            prompt: &str,
            _cancel: &Cancel,
            _sink: &mut dyn FnMut(AdapterEvent),
        ) -> Result<RunOutcome> {
            self.seen
                .lock()
                .expect("probe")
                .push(opened.thinking.clone());
            Ok(RunOutcome::completed(&format!("echo:{prompt}"), None))
        }
    }

    #[test]
    fn the_composer_thinking_level_overrides_the_persona_for_one_turn() {
        let seen = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
        let mut registry = Registry::new();
        registry
            .register(std::sync::Arc::new(ThinkingProbe {
                seen: std::sync::Arc::clone(&seen),
            }))
            .expect("register");
        let mut service = SessionService::new(
            Store::open_in_memory().expect("store"),
            registry,
            SessionContext {
                state_dir: std::path::PathBuf::from(".tmp/test/session"),
                run_timeout: Some(std::time::Duration::from_secs(5)),
            },
        );
        let mut spec = spec();
        spec.thinking = "low".into();
        let session = service.open(&spec).expect("open");

        service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "one".into(),
                    thinking: Some("HIGH".into()),
                    ..Default::default()
                },
            )
            .expect("send");
        // A value the picker could not have produced must not take the turn
        // down: `pi` would exit on a usage error, so it falls back instead.
        service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "two".into(),
                    thinking: Some("ultra".into()),
                    ..Default::default()
                },
            )
            .expect("send");
        // Nothing picked keeps the session's own level.
        service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "three".into(),
                    ..Default::default()
                },
            )
            .expect("send");

        assert_eq!(
            *seen.lock().expect("probe"),
            vec!["high".to_string(), "low".into(), "low".into()]
        );
    }

    #[test]
    fn the_first_prompt_names_the_session() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        assert!(session.title.starts_with("New chat"));

        service
            .send(
                &session.id,
                "  迁移评估：v1 → v2 的兼容性\n第二行不要进标题  ",
            )
            .expect("send");
        let renamed = service.store.session(&session.id).expect("session");
        assert_eq!(renamed.title, "迁移评估：v1 → v2 的兼容性 第二行不要进标题");

        // A later turn must not rename the conversation the reader already
        // recognises.
        service.send(&session.id, "继续").expect("send again");
        assert_eq!(
            service.store.session(&session.id).expect("session").title,
            renamed.title
        );
    }

    #[test]
    fn a_custom_title_survives_the_first_prompt() {
        let mut service = service();
        let spec = SessionSpec {
            title: Some("我的会话".to_string()),
            ..spec()
        };
        let session = service.open(&spec).expect("open");
        service.send(&session.id, "hi").expect("send");
        assert_eq!(
            service.store.session(&session.id).expect("session").title,
            "我的会话"
        );
    }

    #[test]
    fn open_records_native_id_and_a_started_event() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        assert_eq!(session.native_id, "native-auto/gpt-5.6-sol");
        assert_eq!(session.status, SessionStatus::Idle);
        let events = service.events(&session.id).expect("events");
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].kind, EventKind::SessionStarted);
    }

    #[test]
    fn send_writes_user_and_assistant_messages_with_monotonic_seq() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        let outcome = service.send(&session.id, "hello").expect("send");
        assert_eq!(outcome.run.status, RunStatus::Completed);
        assert!(outcome.event_count >= 2);
        let detail = service.detail(&session.id).expect("detail");
        assert_eq!(detail.messages.len(), 2);
        assert_eq!(detail.messages[0].role, Role::User);
        assert_eq!(detail.messages[1].role, Role::Assistant);
        assert_eq!(detail.messages[1].text, "echo:hello");
        assert!(detail.messages[0].seq < detail.messages[1].seq);
        let events = service.events(&session.id).expect("events");
        let seqs: Vec<i64> = events.iter().map(|event| event.seq).collect();
        let mut sorted = seqs.clone();
        sorted.sort_unstable();
        assert_eq!(seqs, sorted, "event seq must be monotonic");
        assert_eq!(
            service
                .detail(&session.id)
                .expect("detail")
                .session
                .message_count,
            2
        );
    }

    #[test]
    fn send_refuses_archived_sessions_and_empty_prompts() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        assert_eq!(
            service.send(&session.id, "   ").expect_err("empty").code(),
            "rejected"
        );
        service.set_archived(&session.id, true).expect("archive");
        assert_eq!(
            service
                .send(&session.id, "hi")
                .expect_err("archived")
                .code(),
            "rejected"
        );
    }

    #[test]
    fn unknown_runtime_is_not_found_not_a_panic() {
        let mut service = service();
        let mut unknown = spec();
        unknown.runtime_id = "does-not-exist".into();
        let error = service.open(&unknown).expect_err("unknown");
        assert_eq!(error.code(), "not_found");
    }

    #[test]
    fn interrupted_runs_are_failed_on_recovery() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        service
            .store
            .start_run(&Run {
                id: "s1:r1".into(),
                session_id: session.id.clone(),
                status: RunStatus::Running,
                started_at: now_rfc3339(),
                ended_at: None,
                error: None,
                prompt: "stuck".into(),
            })
            .expect("start run");
        assert_eq!(service.recover().expect("recover"), 1);
        let detail = service.detail(&session.id).expect("detail");
        assert_eq!(detail.runs[0].status, RunStatus::Failed);
        assert!(detail.runs[0].ended_at.is_some());
    }

    #[test]
    fn abort_without_a_run_reports_nothing_to_stop() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        assert!(!service.abort(&session.id).expect("abort"));
    }

    #[test]
    fn page_orders_pinned_first_and_pages_without_overlap() {
        let mut service = service();
        let first = service.open(&spec()).expect("open");
        let second = service.open(&spec()).expect("open");
        service.set_pinned(&first.id, true).expect("pin");
        let page = service
            .page(&SessionQuery {
                limit: Some(1),
                ..SessionQuery::default()
            })
            .expect("page");
        assert_eq!(page.sessions.len(), 1);
        assert_eq!(page.sessions[0].id, first.id);
        assert_eq!(page.total, 2);
        let cursor = page.next_cursor.expect("cursor");
        let next = service
            .page(&SessionQuery {
                limit: Some(1),
                cursor: Some(cursor),
                ..SessionQuery::default()
            })
            .expect("page 2");
        assert_eq!(next.sessions.len(), 1);
        assert_eq!(next.sessions[0].id, second.id);
        assert!(next.next_cursor.is_none());
    }

    #[test]
    fn search_matches_title_and_message_text() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        service.send(&session.id, "unique-needle-42").expect("send");
        let by_body = service
            .page(&SessionQuery {
                search: Some("unique-needle-42".into()),
                ..SessionQuery::default()
            })
            .expect("search");
        assert_eq!(by_body.sessions.len(), 1);
        let miss = service
            .page(&SessionQuery {
                search: Some("no-such-text".into()),
                ..SessionQuery::default()
            })
            .expect("search");
        assert!(miss.sessions.is_empty());
    }

    /// A project directory with one readable file and one outside neighbour.
    fn project(name: &str) -> (std::path::PathBuf, std::path::PathBuf) {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("..")
            .join("..")
            .join(".tmp")
            .join("test")
            .join("service")
            .join(name);
        let _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(root.join("docs")).expect("create project");
        std::fs::write(root.join("docs/notes.md"), "pomegranate is the keyword").expect("write");
        let outside = root
            .parent()
            .expect("parent")
            .join(format!("{name}-outside.txt"));
        std::fs::write(&outside, "outside secret").expect("write outside");
        (root, outside)
    }

    struct Stopper;

    impl SessionAdapter for Stopper {
        fn id(&self) -> &str {
            "stopper"
        }

        fn capabilities(&self) -> SessionCapabilities {
            SessionCapabilities::full()
        }

        fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession> {
            Ok(OpenedSession {
                runtime_id: spec.runtime_id.clone(),
                native_id: "stopper-native".into(),
                model: spec.model.clone(),
                cwd: spec.cwd.clone(),
                allow_tools: spec.allow_tools,
                tools: spec.tools.clone(),
                ..Default::default()
            })
        }

        fn send(
            &self,
            _context: &SessionContext,
            _opened: &OpenedSession,
            _prompt: &str,
            cancel: &Cancel,
            sink: &mut dyn FnMut(AdapterEvent),
        ) -> Result<RunOutcome> {
            sink(AdapterEvent::text_delta("before"));
            cancel.cancel();
            // The runtime had already queued this chunk when the stop arrived.
            sink(AdapterEvent::text_delta("after"));
            Ok(RunOutcome::aborted("before"))
        }
    }

    fn answer_later(
        approvals: Approvals,
        owner: &str,
        decision: ApprovalDecision,
        answer_as: Option<&str>,
    ) -> std::thread::JoinHandle<std::result::Result<(), String>> {
        let owner = owner.to_string();
        let answer_as = answer_as.map(str::to_string);
        std::thread::spawn(move || {
            for _ in 0..300 {
                if let Some(id) = approvals.pending().into_iter().next() {
                    let session = answer_as.clone().unwrap_or(owner.clone());
                    let first = approvals.resolve(&session, &id, decision);
                    if answer_as.is_some() {
                        // Prove the mismatch is refused, then answer correctly.
                        let refused = first.is_err();
                        approvals
                            .resolve(&owner, &id, decision)
                            .map_err(|error| error.to_string())?;
                        return if refused {
                            Ok(())
                        } else {
                            Err("a foreign session answered the approval".into())
                        };
                    }
                    return first.map_err(|error| error.to_string());
                }
                std::thread::sleep(std::time::Duration::from_millis(10));
            }
            Err("the approval never appeared".into())
        })
    }

    #[test]
    fn attached_context_is_folded_into_the_prompt_the_runtime_receives() {
        let (root, _) = project("attach");
        let mut service = service();
        let mut spec = spec();
        spec.cwd = root.to_string_lossy().to_string();
        let session = service.open(&spec).expect("open");
        let outcome = service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "what is the keyword?".into(),
                    context: vec![crate::model::ContextRef {
                        kind: crate::model::ContextKind::File,
                        path: "docs/notes.md".into(),
                        label: None,
                    }],

                    ..Default::default()
                },
            )
            .expect("send");
        let reply = outcome.message.expect("message").text;
        assert!(reply.contains("pomegranate is the keyword"), "{reply}");
        assert!(
            reply.ends_with("what is the keyword?"),
            "user words stay last"
        );
        let events = service.events(&session.id).expect("events");
        let attached = events
            .iter()
            .find(|event| event.kind == EventKind::ContextAttached)
            .expect("context event");
        assert_eq!(attached.payload["refs"][0]["kind"], "file");
        assert_eq!(attached.payload["refs"][0]["outsideRoot"], false);
        let detail = service.detail(&session.id).expect("detail");
        assert!(
            detail.messages[0].meta.as_ref().unwrap()["context"][0]["chars"]
                .as_u64()
                .unwrap()
                > 0
        );
    }

    #[test]
    fn an_outside_path_is_denied_before_any_run_exists() {
        let (root, outside) = project("deny");
        let mut service = service();
        let mut spec = spec();
        spec.cwd = root.to_string_lossy().to_string();
        let session = service.open(&spec).expect("open");
        let handle = answer_later(
            service.approvals(),
            &session.id,
            ApprovalDecision::Deny,
            None,
        );
        let error = service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "read it".into(),
                    context: vec![crate::model::ContextRef {
                        kind: crate::model::ContextKind::File,
                        path: outside.to_string_lossy().to_string(),
                        label: None,
                    }],

                    ..Default::default()
                },
            )
            .expect_err("denied");
        assert_eq!(error.code(), "rejected");
        handle.join().expect("join").expect("answer");
        let detail = service.detail(&session.id).expect("detail");
        assert!(detail.runs.is_empty(), "a refused turn must not be a run");
        assert!(detail.messages.is_empty(), "nothing was sent");
        let events = service.events(&session.id).expect("events");
        let kinds: Vec<&str> = events.iter().map(|event| event.kind.as_str()).collect();
        assert_eq!(
            kinds,
            vec!["session_started", "approval_request", "approval_resolved"]
        );
        let resolved = events.last().expect("resolved");
        assert_eq!(resolved.payload["decision"], "deny");
    }

    #[test]
    fn an_allowed_outside_path_is_inlined_and_marked() {
        let (root, outside) = project("allow");
        let mut service = service();
        let mut spec = spec();
        spec.cwd = root.to_string_lossy().to_string();
        let session = service.open(&spec).expect("open");
        let handle = answer_later(
            service.approvals(),
            &session.id,
            ApprovalDecision::Allow,
            None,
        );
        let outcome = service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "summary?".into(),
                    context: vec![crate::model::ContextRef {
                        kind: crate::model::ContextKind::File,
                        path: outside.to_string_lossy().to_string(),
                        label: None,
                    }],

                    ..Default::default()
                },
            )
            .expect("send");
        assert!(
            outcome
                .message
                .expect("message")
                .text
                .contains("outside secret")
        );
        handle.join().expect("join").expect("answer");
        let events = service.events(&session.id).expect("events");
        let attached = events
            .iter()
            .find(|event| event.kind == EventKind::ContextAttached)
            .expect("attached");
        assert_eq!(attached.payload["refs"][0]["outsideRoot"], true);
    }

    #[test]
    fn an_approval_cannot_be_answered_from_another_session() {
        let (root, outside) = project("mismatch");
        let mut service = service();
        let mut spec = spec();
        spec.cwd = root.to_string_lossy().to_string();
        let session = service.open(&spec).expect("open");
        let handle = answer_later(
            service.approvals(),
            &session.id,
            ApprovalDecision::Allow,
            Some("some-other-session"),
        );
        let outcome = service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "read".into(),
                    context: vec![crate::model::ContextRef {
                        kind: crate::model::ContextKind::File,
                        path: outside.to_string_lossy().to_string(),
                        label: None,
                    }],

                    ..Default::default()
                },
            )
            .expect("the owner's own answer still allows it");
        assert_eq!(outcome.run.status, RunStatus::Completed);
        handle.join().expect("join").expect("mismatch refused");
    }

    #[test]
    fn a_cancelled_approval_sends_nothing() {
        let (root, outside) = project("cancel");
        let mut service = service();
        let mut spec = spec();
        spec.cwd = root.to_string_lossy().to_string();
        let session = service.open(&spec).expect("open");
        let handle = answer_later(
            service.approvals(),
            &session.id,
            ApprovalDecision::Cancel,
            None,
        );
        let error = service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "read".into(),
                    context: vec![crate::model::ContextRef {
                        kind: crate::model::ContextKind::File,
                        path: outside.to_string_lossy().to_string(),
                        label: None,
                    }],

                    ..Default::default()
                },
            )
            .expect_err("cancelled");
        assert_eq!(error.code(), "rejected");
        handle.join().expect("join").expect("answer");
        assert_eq!(service.detail(&session.id).expect("detail").runs.len(), 0);
    }

    /// "Full control · trust workspace": a path outside the project is read
    /// without a card, because that is exactly what the user asked for.
    #[test]
    fn trusting_the_workspace_reads_outside_paths_without_a_card() {
        let (root, outside) = project("trust");
        let mut service = service();
        let mut spec = spec();
        spec.cwd = root.to_string_lossy().to_string();
        let session = service.open(&spec).expect("open");
        // No `answer_later`: with "full" the turn must never wait on a question.
        let outcome = service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "summary?".into(),
                    context: vec![crate::model::ContextRef {
                        kind: crate::model::ContextKind::File,
                        path: outside.to_string_lossy().to_string(),
                        label: None,
                    }],
                    permission: Some("full".into()),

                    ..Default::default()
                },
            )
            .expect("send");
        assert_eq!(outcome.run.status, RunStatus::Completed);
        let events = service.events(&session.id).expect("events");
        let kinds: Vec<&str> = events.iter().map(|event| event.kind.as_str()).collect();
        assert!(
            !kinds.contains(&"approval_request"),
            "a trusted workspace must not raise a card: {kinds:?}"
        );
    }

    #[test]
    fn an_unanswered_approval_times_out_into_a_denial() {
        let (root, outside) = project("timeout");
        let mut registry = Registry::new();
        registry
            .register(std::sync::Arc::new(Echo { id: "pi".into() }))
            .expect("register");
        let mut service = SessionService::new(
            Store::open_in_memory().expect("store"),
            registry,
            SessionContext {
                state_dir: std::path::PathBuf::from(".tmp/test/session"),
                run_timeout: Some(std::time::Duration::from_secs(5)),
            },
        )
        .with_approval_timeout(std::time::Duration::from_millis(120));
        let mut spec = spec();
        spec.cwd = root.to_string_lossy().to_string();
        let session = service.open(&spec).expect("open");
        let error = service
            .send_with(
                &session.id,
                &SessionInput {
                    prompt: "read".into(),
                    context: vec![crate::model::ContextRef {
                        kind: crate::model::ContextKind::File,
                        path: outside.to_string_lossy().to_string(),
                        label: None,
                    }],

                    ..Default::default()
                },
            )
            .expect_err("timed out");
        assert_eq!(error.code(), "rejected");
        let events = service.events(&session.id).expect("events");
        let resolved = events
            .iter()
            .find(|event| event.kind == EventKind::ApprovalResolved)
            .expect("resolved");
        assert_eq!(resolved.payload["timedOut"], true);
        assert_eq!(resolved.payload["decision"], "deny");
    }

    #[test]
    fn text_produced_after_the_stop_is_not_streamed() {
        let mut registry = Registry::new();
        registry
            .register(std::sync::Arc::new(Stopper))
            .expect("register");
        let mut service = SessionService::new(
            Store::open_in_memory().expect("store"),
            registry,
            SessionContext {
                state_dir: std::path::PathBuf::from(".tmp/test/session"),
                run_timeout: Some(std::time::Duration::from_secs(5)),
            },
        );
        let mut spec = spec();
        spec.runtime_id = "stopper".into();
        let session = service.open(&spec).expect("open");
        let outcome = service.send(&session.id, "go").expect("send");
        assert_eq!(outcome.run.status, RunStatus::Aborted);
        let events = service.events(&session.id).expect("events");
        let deltas: Vec<&str> = events
            .iter()
            .filter(|event| event.kind == EventKind::TextDelta)
            .filter_map(|event| event.payload["delta"].as_str())
            .collect();
        assert_eq!(deltas, vec!["before"], "no token may land after the stop");
        let terminal = events.last().expect("terminal");
        assert_eq!(terminal.kind, EventKind::Aborted);
        assert_eq!(terminal.payload["lateDeltas"], 1);
    }

    #[test]
    fn a_branch_copies_the_transcript_and_carries_it_into_the_first_turn() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        service
            .send(&session.id, "remember the word pomegranate")
            .expect("send");

        let branch = service.fork(&session.id, None).expect("fork");
        assert_eq!(branch.parent_id, session.id);
        assert_eq!(branch.branch_at, 2);
        assert_eq!(branch.message_count, 2);
        assert!(!branch.native_id.trim().is_empty());
        assert_ne!(branch.id, session.id);

        let outcome = service.send(&branch.id, "what word?").expect("send");
        // The echo adapter returns what it was given, so the reply proves the
        // carried transcript reached the runtime.
        assert!(
            outcome
                .message
                .as_ref()
                .map(|message| message.text.contains("pomegranate"))
                .unwrap_or(false),
            "the branch's first prompt must carry the parent transcript"
        );
        let detail = service.detail(&branch.id).expect("detail");
        assert_eq!(detail.messages.len(), 4);
        assert_eq!(detail.messages[0].role, Role::User);
        assert_eq!(detail.messages[3].role, Role::Assistant);
        // The parent is untouched by the branch's traffic.
        assert_eq!(
            service.detail(&session.id).expect("parent").messages.len(),
            2
        );

        // Only the first turn carries the seed; a second turn relies on the
        // runtime's own resumed context.
        let second = service.send(&branch.id, "and again").expect("send");
        assert_eq!(
            second.message.map(|message| message.text),
            Some("echo:and again".to_string())
        );
    }

    #[test]
    fn compact_folds_old_messages_and_keeps_the_tail_verbatim() {
        let mut service = service();
        let session = service.open(&spec()).expect("open");
        service.send(&session.id, "first question").expect("send");
        service.send(&session.id, "second question").expect("send");
        service.send(&session.id, "third question").expect("send");

        let report = service.compact(&session.id, 2).expect("compact");
        assert_eq!(report.collapsed, 4);
        assert_eq!(report.kept, 2);
        let detail = service.detail(&session.id).expect("detail");
        assert_eq!(detail.messages.len(), 3);
        assert_eq!(detail.messages[0].role, Role::System);
        assert!(detail.messages[0].text.contains("4 earlier message(s)"));
        assert_eq!(detail.messages[1].text, "third question");
        assert_eq!(detail.messages[2].text, "echo:third question");
        let seqs: Vec<i64> = detail.messages.iter().map(|message| message.seq).collect();
        assert_eq!(seqs, vec![1, 2, 3]);
        let events = service.events(&session.id).expect("events");
        let compacted = events
            .iter()
            .find(|event| event.kind == EventKind::Compacted)
            .expect("compacted event");
        assert_eq!(compacted.payload["nativeContext"], "untouched");

        // Compacting again with nothing beyond the tail is refused rather than
        // silently producing an empty summary.
        assert_eq!(
            service
                .compact(&session.id, 5)
                .expect_err("nothing left")
                .code(),
            "rejected"
        );
    }

    #[test]
    fn projects_group_sessions_by_directory_and_retire_when_empty() {
        let mut service = service();
        service.open(&spec()).expect("open");
        let mut elsewhere = spec();
        elsewhere.cwd = "D:\\project\\anvil-wt".into();
        let second = service.open(&elsewhere).expect("open");
        let third = service.open(&elsewhere).expect("open");

        let projects = service.projects().expect("projects");
        assert_eq!(projects.len(), 2);
        let anvil = projects
            .iter()
            .find(|project| project.path == "D:\\project\\anvil-wt")
            .expect("anvil project");
        assert_eq!(anvil.name, "anvil-wt");
        assert_eq!(anvil.session_count, 2);

        service.delete(&second.id).expect("delete");
        let after = service.projects().expect("projects");
        let anvil = after
            .iter()
            .find(|project| project.path == "D:\\project\\anvil-wt")
            .expect("still has a session");
        assert_eq!(anvil.session_count, 1);
        service.delete(&third.id).expect("delete");
        assert!(
            service
                .projects()
                .expect("projects")
                .iter()
                .all(|project| project.path != "D:\\project\\anvil-wt"),
            "an empty project must not be listed"
        );
        assert!(
            service
                .projects()
                .expect("projects")
                .iter()
                .any(|project| project.path == "D:\\project"),
            "the other project survives"
        );
    }

    #[test]
    fn project_filter_never_mixes_two_directories() {
        let mut service = service();
        service.open(&spec()).expect("open");
        let mut elsewhere = spec();
        elsewhere.cwd = "D:\\project\\anvil-wt".into();
        let other = service.open(&elsewhere).expect("open");

        let filtered = service
            .page(&SessionQuery {
                project_path: Some("D:\\project\\anvil-wt".into()),
                ..SessionQuery::default()
            })
            .expect("page");
        assert_eq!(filtered.sessions.len(), 1);
        assert_eq!(filtered.sessions[0].id, other.id);
        assert_eq!(filtered.total, 1);
    }
}
