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
    SessionCapabilities, SessionContext, SessionPage, SessionQuery, SessionService, SessionSpec,
    SessionStatus, Store,
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
            run_timeout: Some(timeout),
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
        tools: Vec::new(),
        ..Default::default()
    }
}

fn list(service: &SessionService, include_archived: bool) -> SessionResult<SessionPage> {
    service.page(&SessionQuery {
        include_archived,
        ..SessionQuery::default()
    })
}

#[test]
fn unknown_runtime_is_rejected_before_any_row_is_written() {
    let (adapter, _) = Scripted::new("pi", Behaviour::Echo);
    let mut service = service_with(Duration::from_secs(2), vec![adapter]);
    let error = service.open(&spec("ghost")).expect_err("unknown runtime");
    assert_eq!(error.code(), "not_found");
    assert!(error.to_string().contains("pi"), "{error}");
    let page = list(&service, true).expect("page");
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
    let page = list(&service, false).expect("page");
    assert_eq!(page.sessions[0].title, "Renamed");
    assert!(page.sessions[0].pinned);

    service.set_archived(&session.id, true).expect("archive");
    let visible = list(&service, false).expect("page");
    assert!(visible.sessions.is_empty());
    let all = list(&service, true).expect("page");
    assert_eq!(all.sessions.len(), 1);
    assert_eq!(all.sessions[0].status, SessionStatus::Archived);

    service.delete(&session.id).expect("delete");
    let after = list(&service, true).expect("page");
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

/// A service on a real file, so the test can close it and open it again the way
/// a restarted application does.
fn service_on(path: &std::path::Path, adapters: Vec<Arc<dyn SessionAdapter>>) -> SessionService {
    let mut registry = Registry::new();
    for adapter in adapters {
        registry.register(adapter).expect("register adapter");
    }
    SessionService::new(
        Store::open(path).expect("open ledger"),
        registry,
        SessionContext {
            state_dir: path.with_file_name("state"),
            run_timeout: Some(Duration::from_secs(2)),
        },
    )
}

/// Scratch database under the workspace `.tmp`, not under the crate.
///
/// A relative path would land in `crates/anvil-session/` because a test binary
/// runs with the crate root as its working directory, and leave an untracked
/// directory behind every run.
fn scratch(name: &str) -> std::path::PathBuf {
    let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join(".tmp")
        .join("test")
        .join("session-contract")
        .join(name)
        .join("sessions.sqlite");
    let _ = std::fs::remove_dir_all(dir.parent().expect("parent"));
    dir
}

#[test]
fn history_survives_a_restart_and_keeps_projects_apart() {
    let path = scratch("restart");
    let (adapter, _) = Scripted::new("pi", Behaviour::Echo);

    let project_a = {
        let mut service = service_on(&path, vec![adapter.clone()]);
        let mut in_a = spec("pi");
        in_a.cwd = "D://work//alpha".into();
        let first = service.open(&in_a).expect("open a");
        service.send(&first.id, "alpha needle").expect("send a");
        let mut in_b = spec("pi");
        in_b.cwd = "D://work//beta".into();
        let second = service.open(&in_b).expect("open b");
        service.send(&second.id, "beta body").expect("send b");
        service.rename(&second.id, "Beta session").expect("rename");
        service.set_pinned(&second.id, true).expect("pin");
        // Simulate a process that died mid-run: the row says running, no run
        // will ever finish.
        service
            .store_mut()
            .set_status(&first.id, SessionStatus::Running, "2026-11-13T00:00:00Z")
            .expect("mark running");
        (first.id, second.id)
    };

    // Close, reopen: the same file, a new process.
    let mut service = service_on(&path, vec![adapter]);
    assert_eq!(
        service.recover().expect("recover"),
        0,
        "no run row was left running"
    );

    let all = list(&service, true).expect("page");
    assert_eq!(all.total, 2);
    assert_eq!(all.sessions[0].id, project_a.1, "pinned row comes first");
    assert_eq!(all.sessions[0].title, "Beta session");

    let alpha = service
        .page(&SessionQuery {
            project_path: Some("D://work//alpha".into()),
            ..SessionQuery::default()
        })
        .expect("filter");
    assert_eq!(alpha.sessions.len(), 1);
    assert_eq!(alpha.sessions[0].id, project_a.0);

    let found = service
        .page(&SessionQuery {
            search: Some("beta body".into()),
            ..SessionQuery::default()
        })
        .expect("search");
    assert_eq!(found.sessions.len(), 1);

    // A session that says "running" but has no run behind it would block its
    // next send forever; the reopen repaired it.
    service
        .send(&project_a.0, "after restart")
        .expect("send after restart");
    let detail = service.detail(&project_a.0).expect("detail");
    assert_eq!(detail.messages.len(), 4);

    let projects = service.projects().expect("projects");
    assert_eq!(projects.len(), 2, "one entry per directory, no duplicates");
    assert_eq!(
        projects
            .iter()
            .map(|project| project.name.clone())
            .collect::<Vec<_>>(),
        vec!["alpha".to_string(), "beta".to_string()]
    );
}

#[test]
fn paging_walks_every_session_exactly_once_across_a_restart() {
    let path = scratch("paging");
    let (adapter, _) = Scripted::new("pi", Behaviour::Echo);
    let mut expected = Vec::new();
    {
        let mut service = service_on(&path, vec![adapter.clone()]);
        for index in 0..7 {
            let mut each = spec("pi");
            each.cwd = "D://work//paging".into();
            let session = service.open(&each).expect("open");
            service
                .send(&session.id, &format!("turn {index}"))
                .expect("send");
            expected.push(session.id);
        }
        // Renaming rewrites `updated_at` with the same clock as the last send,
        // so the ordering key stays total only because `id` breaks the tie.
        service.rename(&expected[2], "third").expect("rename");
    }

    let service = service_on(&path, vec![adapter]);
    let mut seen: Vec<String> = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let page = service
            .page(&SessionQuery {
                limit: Some(3),
                cursor: cursor.clone(),
                ..SessionQuery::default()
            })
            .expect("page");
        assert!(page.sessions.len() <= 3);
        seen.extend(page.sessions.iter().map(|session| session.id.clone()));
        match page.next_cursor {
            Some(next) => cursor = Some(next),
            None => break,
        }
    }
    seen.sort();
    let mut wanted = expected.clone();
    wanted.sort();
    assert_eq!(seen, wanted, "every session appears exactly once");
}
