//! Orchestration: session lifecycle on top of the adapter registry.
//!
//! `open` creates the index row first, then asks the runtime; `send` opens a run
//! row, forwards adapter events into the ledger as they arrive, and closes the
//! run with a terminal status even when the adapter fails. A crash therefore
//! leaves "everything that arrived", never a half-written message.

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use crate::adapter::{AdapterEvent, Cancel, Registry};
use crate::clock::{now_rfc3339, unix_millis};
use crate::error::{Error, Result};
use crate::model::{
    CompactReport, Message, OpenedSession, ProjectSummary, Role, Run, RunEvent, RunStatus,
    SessionContext, SessionDetail, SessionPage, SessionQuery, SessionSpec, SessionStatus,
    SessionSummary,
};
use crate::store::{NewSession, Store};

static SESSION_COUNTER: AtomicU64 = AtomicU64::new(0);

/// Runs are created in the same millisecond when a case sends twice in a row;
/// the counter keeps their ids unique.
static RUN_COUNTER: AtomicU64 = AtomicU64::new(0);

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

/// The session facade the host commands call.
pub struct SessionService {
    store: Store,
    registry: Registry,
    context: SessionContext,
    cancels: Cancels,
}

impl SessionService {
    #[must_use]
    pub fn new(store: Store, registry: Registry, context: SessionContext) -> Self {
        Self {
            store,
            registry,
            context,
            cancels: Cancels::new(),
        }
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
            project_path: &spec.cwd,
            parent_id: "",
            branch_at: 0,
            at: &now,
        })?;

        let opened = match adapter.open(&self.context, spec) {
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
            }),
        )?;
        self.store.session(&id)
    }

    /// Run one turn. Blocking: the host calls this from a blocking task.
    pub fn send(&mut self, session_id: &str, prompt: &str) -> Result<SendOutcome> {
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
        if prompt.trim().is_empty() {
            return Err(Error::Rejected("an empty prompt cannot be sent".into()));
        }

        let adapter = self.registry.require(&summary.runtime_id)?;
        let cancel = self.cancels.register(session_id);

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
        let adapter_prompt = if seed.is_empty() {
            prompt.to_string()
        } else {
            format!("{seed}\n\n{prompt}")
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
            None,
        )?;
        self.store.append_event(
            session_id,
            &run_id,
            crate::model::EventKind::TurnStarted,
            &serde_json::json!({ "prompt": prompt, "seeded": !seed.is_empty() }),
        )?;

        let opened = OpenedSession {
            runtime_id: summary.runtime_id.clone(),
            native_id: summary.native_id.clone(),
            model: summary.model.clone(),
        };

        let mut event_count = 0usize;
        let mut deltas = String::new();
        let mut sink = |event: AdapterEvent| {
            event_count += 1;
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

        let outcome = adapter.send(&self.context, &opened, &adapter_prompt, &cancel, &mut sink);
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
            &serde_json::json!({ "status": status.as_str(), "error": error }),
        )?;
        self.cancels.forget(session_id);

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
        Ok(self.cancels.cancel(session_id))
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
        self.store.create_session(&NewSession {
            id: &id,
            runtime_id: &source.runtime_id,
            title: &title,
            model: &source.model,
            project_path: &source.project_path,
            parent_id: &source.id,
            branch_at,
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
            allow_tools: false,
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
                run_timeout: std::time::Duration::from_secs(5),
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
        }
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
