//! Contract tests for the session layer.
//!
//! Every scenario here uses an injected adapter: the point is the contract, not
//! a runtime. Live behaviour is covered by the `R2-01-*-live` acceptance cases.

use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use anvil_session::adapter::{AdapterEvent, Cancel, RunOutcome, SessionAdapter};
use anvil_session::{
    EventKind, Message, OpenedSession, Registry, Result as SessionResult, Role, RunStatus,
    SessionCapabilities, SessionContext, SessionService, SessionSpec, SessionStatus, Store,
};

struct Scripted {
    id: &'static str,
    caps: SessionCapabilities,
    behaviour: Behaviour,
    sends: Arc<AtomicUsize>,
}

enum Behaviour {
    Echo,
    /// Emits one delta, then reports a failure.
    FailAfterDelta,
    /// Reports a timeout the way a real adapter does: it owns its deadline and
    /// returns rather than blocking the caller forever.
    Timeout,
    /// Blocks until cancelled, then reports aborted.
    SleepUntilCancel,
}

impl Scripted {
    fn new(id: &'static str, behaviour: Behaviour) -> (Arc<Self>, Arc<AtomicUsize>) {
        let sends = Arc::new(AtomicUsize::new(0));
        (
            Arc::new(Self {
                id,
                caps: SessionCapabilities::full(),
                behaviour,
                sends: sends.clone(),
            }),
            sends,
        )
    }
}

impl SessionAdapter for Scripted {
    fn id(&self) -> &str {
        self.id
    }

    fn capabilities(&self) -> SessionCapabilities {
        self.caps
    }

    fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> SessionResult<OpenedSession> {
        Ok(OpenedSession {
            runtime_id: spec.runtime_id.clone(),
            native_id: format!("{}-native", self.id),
            model: spec.model.clone(),
        })
    }

    fn send(
        &self,
        _context: &SessionContext,
        _opened: &OpenedSession,
        prompt: &str,
        cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> SessionResult<RunOutcome> {
        self.sends.fetch_add(1, Ordering::SeqCst);
        match self.behaviour {
            Behaviour::Echo => {
                sink(AdapterEvent::text_delta("echo:"));
                Ok(RunOutcome::completed(&format!("echo:{prompt}"), None))
            }
            Behaviour::FailAfterDelta => {
                sink(AdapterEvent::text_delta("partial"));
                Err(anvil_session::Error::Failed("runtime crashed".into()))
            }
            Behaviour::Timeout => Err(anvil_session::Error::Timeout(
                "runtime did not answer in time".into(),
            )),
            Behaviour::SleepUntilCancel => {
                let mut written = 0;
                while written < 100 {
                    if cancel.is_cancelled() {
                        return Ok(RunOutcome::aborted("partial"));
                    }
                    std::thread::sleep(Duration::from_millis(10));
                    written += 1;
                }
                Ok(RunOutcome::completed("finished anyway", None))
            }
        }
    }
}

fn service_with(timeout: Duration, adapters: Vec<Arc<dyn SessionAdapter>>) -> SessionService {
    let mut registry = Registry::new();
    for adapter in adapters {
        registry.register(adapter).expect("register adapter");
    }
    SessionService::new(
        Store::open_in_memory().expect("store"),
        registry,
        SessionContext {
            state_dir: std::path::PathBuf::from(".tmp/test/session-contract"),
            run_timeout: timeout,
        },
    )
}

fn spec(runtime_id: &str) -> SessionSpec {
    SessionSpec {
        runtime_id: runtime_id.to_string(),
        model: "test-model".into(),
        cwd: "D:\\project".into(),
        title: None,
        native_id: None,
        allow_tools: false,
    }
}

#[test]
fn unknown_runtime_is_rejected_before_any_row_is_written() {
    let (adapter, _) = Scripted::new("pi", Behaviour::Echo);
    let mut service = service_with(Duration::from_secs(2), vec![adapter]);
    let error = service.open(&spec("ghost")).expect_err("unknown runtime");
    assert_eq!(error.code(), "not_found");
    assert!(error.to_string().contains("pi"), "{error}");
    let page = service.page(None, true, None, 10, None).expect("page");
    assert_eq!(page.total, 0, "a refused open must not leave a row");
}

#[test]
fn a_crashing_adapter_keeps_what_arrived_and_fails_the_run() {
    let (adapter, _) = Scripted::new("pi", Behaviour::FailAfterDelta);
    let mut service = service_with(Duration::from_secs(2), vec![adapter]);
    let session = service.open(&spec("pi")).expect("open");
    let outcome = service.send(&session.id, "hello").expect("send");
    assert_eq!(outcome.run.status, RunStatus::Failed);
    assert!(outcome.run.error.as_deref().unwrap().contains("crashed"));

    let detail = service.detail(&session.id).expect("detail");
    assert_eq!(detail.session.status, SessionStatus::Failed);
    // The user message and the partial answer both survive.
    assert_eq!(detail.messages.len(), 2);
    assert_eq!(detail.messages[1].text, "partial");
    let events = service.events(&session.id).expect("events");
    assert!(
        events
            .iter()
            .any(|event| event.kind == EventKind::TextDelta),
        "the delta that arrived before the crash must be kept"
    );
}

#[test]
fn an_adapter_timeout_becomes_a_failed_run() {
    let (adapter, _) = Scripted::new("pi", Behaviour::Timeout);
    let mut service = service_with(Duration::from_millis(120), vec![adapter]);
    let session = service.open(&spec("pi")).expect("open");
    let outcome = service.send(&session.id, "hello").expect("send");
    assert_eq!(outcome.run.status, RunStatus::Failed);
    assert!(
        outcome
            .run
            .error
            .as_deref()
            .unwrap_or_default()
            .contains("timeout"),
        "{:?}",
        outcome.run.error
    );
    assert_eq!(
        service.detail(&session.id).expect("detail").session.status,
        SessionStatus::Failed
    );
}

#[test]
fn abort_stops_the_run_from_another_thread() {
    let (adapter, _) = Scripted::new("pi", Behaviour::SleepUntilCancel);
    let mut service = service_with(Duration::from_secs(30), vec![adapter]);
    let session = service.open(&spec("pi")).expect("open");

    // The host keeps the registry outside the service lock; that is the only
    // way to stop a run whose `send` holds `&mut self`.
    let cancels = service.cancels();
    let id = session.id.clone();

    // Scoped: the send borrows the service mutably, the main thread only touches
    // the cloned cancel registry, and the borrow ends when the scope returns.
    let (outcome, stopped) = std::thread::scope(|scope| {
        let handle = scope.spawn(|| service.send(&id, "long answer"));
        let mut stopped = false;
        for _ in 0..100 {
            std::thread::sleep(Duration::from_millis(20));
            if cancels.cancel(&id) {
                stopped = true;
                break;
            }
        }
        (handle.join().expect("join").expect("send"), stopped)
    });
    assert!(stopped, "the run must be visible in the cancel registry");
    assert_eq!(outcome.run.status, RunStatus::Aborted);
    assert!(outcome.run.ended_at.is_some());
    assert_eq!(
        service.detail(&session.id).expect("detail").session.status,
        SessionStatus::Aborted
    );
}

#[test]
fn a_session_left_running_by_a_crash_refuses_a_second_send() {
    let (adapter, sends) = Scripted::new("pi", Behaviour::Echo);
    let mut service = service_with(Duration::from_secs(5), vec![adapter]);
    let session = service.open(&spec("pi")).expect("open");

    // Recreate what a killed process leaves behind: a session marked running.
    service
        .store_mut()
        .set_status(
            &session.id,
            SessionStatus::Running,
            &anvil_session::clock::now_rfc3339(),
        )
        .expect("force running");

    let error = service.send(&session.id, "second").expect_err("refused");
    assert_eq!(error.code(), "rejected");
    assert!(error.to_string().contains("in flight"), "{error}");
    assert_eq!(
        sends.load(Ordering::SeqCst),
        0,
        "no adapter call must happen"
    );
}

#[test]
fn event_sequence_is_unique_and_monotonic_per_session() {
    let (adapter, _) = Scripted::new("pi", Behaviour::Echo);
    let mut service = service_with(Duration::from_secs(2), vec![adapter]);
    let session = service.open(&spec("pi")).expect("open");
    service.send(&session.id, "one").expect("send 1");
    service.send(&session.id, "two").expect("send 2");

    let events = service.events(&session.id).expect("events");
    let seqs: Vec<i64> = events.iter().map(|event| event.seq).collect();
    let mut unique = seqs.clone();
    unique.dedup();
    assert_eq!(unique.len(), seqs.len(), "seq must not repeat: {seqs:?}");
    let mut sorted = seqs.clone();
    sorted.sort_unstable();
    assert_eq!(seqs, sorted, "seq must be appended in order");
}

#[test]
fn messages_keep_order_and_roles_across_runs() {
    let (adapter, _) = Scripted::new("pi", Behaviour::Echo);
    let mut service = service_with(Duration::from_secs(2), vec![adapter]);
    let session = service.open(&spec("pi")).expect("open");
    service.send(&session.id, "one").expect("send 1");
    service.send(&session.id, "two").expect("send 2");

    let messages: Vec<Message> = service.detail(&session.id).expect("detail").messages;
    let roles: Vec<Role> = messages.iter().map(|message| message.role).collect();
    assert_eq!(
        roles,
        vec![Role::User, Role::Assistant, Role::User, Role::Assistant]
    );
    assert_eq!(messages[3].text, "echo:two");
}

#[test]
fn archive_rename_pin_and_delete_keep_the_index_consistent() {
    let (adapter, _) = Scripted::new("pi", Behaviour::Echo);
    let mut service = service_with(Duration::from_secs(2), vec![adapter]);
    let session = service.open(&spec("pi")).expect("open");

    service.rename(&session.id, "Renamed").expect("rename");
    service.set_pinned(&session.id, true).expect("pin");
    let page = service.page(None, false, None, 10, None).expect("page");
    assert_eq!(page.sessions[0].title, "Renamed");
    assert!(page.sessions[0].pinned);

    service.set_archived(&session.id, true).expect("archive");
    let visible = service.page(None, false, None, 10, None).expect("page");
    assert!(visible.sessions.is_empty());
    let all = service.page(None, true, None, 10, None).expect("page");
    assert_eq!(all.sessions.len(), 1);
    assert_eq!(all.sessions[0].status, SessionStatus::Archived);

    service.delete(&session.id).expect("delete");
    let after = service.page(None, true, None, 10, None).expect("page");
    assert_eq!(after.total, 0);
}

#[test]
fn capability_gaps_are_reported_for_every_adapter() {
    let (mut adapter, _) = Scripted::new("pi", Behaviour::Echo);
    Arc::get_mut(&mut adapter).expect("sole owner").caps = SessionCapabilities {
        streaming: true,
        abort: false,
        resume: false,
        tools: false,
        approvals: false,
        compact: false,
        fork: false,
        usage: false,
    };
    let mut registry = Registry::new();
    registry.register(adapter).expect("register");
    let failures = registry.capability_failures(SessionCapabilities::full());
    assert_eq!(failures.len(), 2);
    assert!(failures.iter().all(|failure| failure.runtime_id == "pi"));
}
