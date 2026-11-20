//! The adapter contract.
//!
//! An adapter owns one runtime: it opens a session, streams one run, and can be
//! cancelled. It never assigns sequence numbers — the service does that, so the
//! ledger stays ordered even when an adapter emits events out of order or from
//! another thread.

use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use serde::Serialize;
use serde_json::Value;

use crate::error::{Error, Result};
use crate::model::{
    AdapterFailure, EventKind, OpenedSession, RunStatus, SessionCapabilities, SessionContext,
    SessionSpec,
};

/// How full a runtime's context window is right now.
///
/// Window size comes from the runtime's model; used tokens come from the
/// session itself. Both are optional: a runtime that reports neither simply
/// leaves the indicator without a number rather than inventing one.
#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextUsage {
    pub tokens: Option<u64>,
    pub context_window: u64,
    pub percent: Option<f64>,
}

/// One event as the adapter observed it, before the service assigns a `seq`.
#[derive(Debug, Clone)]
pub struct AdapterEvent {
    pub kind: EventKind,
    pub payload: Value,
}

impl AdapterEvent {
    #[must_use]
    pub fn new(kind: EventKind, payload: Value) -> Self {
        Self { kind, payload }
    }

    /// Convenience for the common text delta.
    #[must_use]
    pub fn text_delta(delta: &str) -> Self {
        Self::new(EventKind::TextDelta, serde_json::json!({ "delta": delta }))
    }
}

/// Cooperative cancellation.
///
/// A runtime call is a blocking read loop, so the host flips this flag and the
/// adapter stops at the next chunk. That is a real stop, not a detached thread.
#[derive(Debug, Clone, Default)]
pub struct Cancel(Arc<AtomicBool>);

impl Cancel {
    #[must_use]
    pub fn new() -> Self {
        Self(Arc::new(AtomicBool::new(false)))
    }

    pub fn cancel(&self) {
        self.0.store(true, Ordering::SeqCst);
    }

    #[must_use]
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::SeqCst)
    }
}

/// What one run produced overall.
#[derive(Debug, Clone)]
pub struct RunOutcome {
    pub status: RunStatus,
    /// Final assistant text, as the runtime reported it.
    pub text: String,
    pub error: Option<String>,
    pub usage: Option<Value>,
    /// Set when the runtime only minted its identifier during the run.
    ///
    /// Codex does not write a rollout for a thread that never ran a turn, so a
    /// thread created at open time cannot be resumed; the identifier therefore
    /// arrives with the first run and the service stores it then.
    pub native_id: Option<String>,
}

impl RunOutcome {
    #[must_use]
    pub fn completed(text: &str, usage: Option<Value>) -> Self {
        Self {
            status: RunStatus::Completed,
            text: text.to_string(),
            error: None,
            usage,
            native_id: None,
        }
    }

    #[must_use]
    pub fn aborted(text: &str) -> Self {
        Self {
            status: RunStatus::Aborted,
            text: text.to_string(),
            error: None,
            usage: None,
            native_id: None,
        }
    }

    #[must_use]
    pub fn failed(error: &str) -> Self {
        Self {
            status: RunStatus::Failed,
            text: String::new(),
            error: Some(error.to_string()),
            usage: None,
            native_id: None,
        }
    }

    /// Record the identifier the runtime minted during this run.
    #[must_use]
    pub fn with_native_id(mut self, native_id: &str) -> Self {
        if !native_id.trim().is_empty() {
            self.native_id = Some(native_id.to_string());
        }
        self
    }
}

/// One turn as the user composed it.
///
/// Images are paths, not bytes: every runtime that accepts them wants a file
/// path (pi reads `@path` itself and turns the file into an image content
/// block), and a path keeps the adapter free of image decoding.
#[derive(Debug, Clone, Copy)]
pub struct Turn<'a> {
    pub prompt: &'a str,
    pub images: &'a [PathBuf],
}

impl<'a> Turn<'a> {
    #[must_use]
    pub fn text(prompt: &'a str) -> Self {
        Self {
            prompt,
            images: &[],
        }
    }
}

/// One runtime, as this build sees it.
pub trait SessionAdapter: Send + Sync {
    /// Stable id, the same string the catalog uses (`pi`, `codex`, …).
    fn id(&self) -> &str;

    fn capabilities(&self) -> SessionCapabilities;

    /// Whether `send_turn` can hand images to the runtime.
    ///
    /// Default `false`: a runtime that was never taught the attachment channel
    /// must refuse images loudly instead of silently dropping them from a turn
    /// the user believes carried a picture.
    fn supports_images(&self) -> bool {
        false
    }

    /// Start or resume a session and return the runtime's own identifier.
    fn open(&self, context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession>;

    /// Run one turn, emitting events as they arrive.
    ///
    /// `sink` is called from the adapter's thread; it must not panic and must
    /// not block longer than a channel send.
    fn send(
        &self,
        context: &SessionContext,
        opened: &OpenedSession,
        prompt: &str,
        cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> Result<RunOutcome>;

    /// Run one turn with its attachments.
    ///
    /// Default: attachments cannot be represented, so the prompt travels alone.
    /// Only adapters that opt in through `supports_images` override this.
    fn send_turn(
        &self,
        context: &SessionContext,
        opened: &OpenedSession,
        turn: &Turn<'_>,
        cancel: &Cancel,
        sink: &mut dyn FnMut(AdapterEvent),
    ) -> Result<RunOutcome> {
        self.send(context, opened, turn.prompt, cancel, sink)
    }

    /// Read the current context-window occupancy of a session, if the runtime
    /// can report it. Default: no data; the page must say so, not guess.
    fn context_usage(
        &self,
        _context: &SessionContext,
        _opened: &OpenedSession,
    ) -> Result<Option<ContextUsage>> {
        Ok(None)
    }

    /// The registry of questions this runtime is waiting on.
    ///
    /// A runtime that can ask the user something holds a registry so the page
    /// can answer through another command while the run is blocked. Runtimes
    /// with no such channel return `None`.
    fn extension_uis(&self) -> Option<crate::ui::ExtensionUis> {
        None
    }

    /// Hand the user's answer to a question the runtime is waiting on.
    ///
    /// `native_id` is the runtime's own session id: the answer travels back
    /// through the same live pipe the run reads from.
    fn answer_ui(
        &self,
        _native_id: &str,
        _request_id: &str,
        _answer: &crate::model::UiAnswer,
    ) -> Result<()> {
        Err(Error::Failed("this runtime cannot ask the user anything".into()))
    }

    /// Stop a run started by `send`. Default: rely on `Cancel`.
    fn abort(&self, _opened: &OpenedSession) -> Result<()> {
        Ok(())
    }

    /// Hand a message to a run that is already streaming.
    ///
    /// A steer is not a second turn: the runtime keeps working and picks the
    /// text up at its next step. `Ok(false)` means there was no live run to
    /// hand it to — either the adapter has no such channel or the turn already
    /// ended — so the caller can put the text back in front of the user instead
    /// of pretending it was delivered.
    fn steer(&self, _opened: &OpenedSession, _text: &str) -> Result<bool> {
        Ok(false)
    }
}

/// Adapter registry. Duplicate or empty ids are refused at registration time so
/// a misconfigured build fails at start-up rather than at the first message.
#[derive(Default)]
pub struct Registry {
    adapters: BTreeMap<String, Arc<dyn SessionAdapter>>,
}

impl Registry {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    pub fn register(&mut self, adapter: Arc<dyn SessionAdapter>) -> Result<()> {
        let id = adapter.id().trim().to_string();
        if id.is_empty() {
            return Err(Error::Rejected("adapter id must not be empty".into()));
        }
        if self.adapters.contains_key(&id) {
            return Err(Error::Rejected(format!(
                "adapter {id:?} is already registered"
            )));
        }
        self.adapters.insert(id, adapter);
        Ok(())
    }

    #[must_use]
    pub fn get(&self, runtime_id: &str) -> Option<Arc<dyn SessionAdapter>> {
        self.adapters.get(runtime_id).cloned()
    }

    #[must_use]
    pub fn ids(&self) -> Vec<String> {
        self.adapters.keys().cloned().collect()
    }

    #[must_use]
    pub fn len(&self) -> usize {
        self.adapters.len()
    }

    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.adapters.is_empty()
    }

    #[must_use]
    pub fn capabilities(&self, runtime_id: &str) -> Option<SessionCapabilities> {
        self.adapters
            .get(runtime_id)
            .map(|adapter| adapter.capabilities())
    }

    /// Resolve an id or explain precisely what is missing.
    pub fn require(&self, runtime_id: &str) -> Result<Arc<dyn SessionAdapter>> {
        self.get(runtime_id).ok_or_else(|| {
            Error::NotFound(format!(
                "no session adapter for runtime {runtime_id:?} (have: {})",
                if self.adapters.is_empty() {
                    "none".to_string()
                } else {
                    self.ids().join(", ")
                }
            ))
        })
    }

    /// Failures for every registered adapter that cannot meet `minimum`.
    #[must_use]
    pub fn capability_failures(&self, minimum: SessionCapabilities) -> Vec<AdapterFailure> {
        let mut failures = Vec::new();
        for (id, adapter) in &self.adapters {
            let offered = adapter.capabilities();
            if minimum.streaming && !offered.streaming {
                failures.push(AdapterFailure::new(
                    id,
                    "no_streaming",
                    "runtime cannot stream text",
                ));
            }
            if minimum.abort && !offered.abort {
                failures.push(AdapterFailure::new(
                    id,
                    "no_abort",
                    "runtime cannot stop a run",
                ));
            }
            if minimum.resume && !offered.resume {
                failures.push(AdapterFailure::new(
                    id,
                    "no_resume",
                    "runtime cannot resume a session",
                ));
            }
        }
        failures
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::SessionCapabilities;

    struct Stub {
        id: String,
        caps: SessionCapabilities,
    }

    impl SessionAdapter for Stub {
        fn id(&self) -> &str {
            &self.id
        }

        fn capabilities(&self) -> SessionCapabilities {
            self.caps
        }

        fn open(&self, _context: &SessionContext, spec: &SessionSpec) -> Result<OpenedSession> {
            Ok(OpenedSession {
                runtime_id: spec.runtime_id.clone(),
                native_id: "native-1".into(),
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
            _cancel: &Cancel,
            _sink: &mut dyn FnMut(AdapterEvent),
        ) -> Result<RunOutcome> {
            Ok(RunOutcome::completed("ok", None))
        }
    }

    fn stub(id: &str) -> Arc<dyn SessionAdapter> {
        Arc::new(Stub {
            id: id.to_string(),
            caps: SessionCapabilities::full(),
        })
    }

    #[test]
    fn duplicate_and_empty_ids_are_refused() {
        let mut registry = Registry::new();
        registry.register(stub("pi")).expect("first");
        assert_eq!(
            registry.register(stub("pi")).expect_err("duplicate").code(),
            "rejected"
        );
        assert_eq!(
            registry.register(stub("  ")).expect_err("empty").code(),
            "rejected"
        );
        assert_eq!(registry.len(), 1);
    }

    #[test]
    fn unknown_runtime_is_not_found_and_names_the_known_ones() {
        let mut registry = Registry::new();
        registry.register(stub("pi")).expect("register");
        let error = registry
            .require("nope")
            .err()
            .expect("unknown runtime must fail");
        assert_eq!(error.code(), "not_found");
        assert!(error.to_string().contains("pi"), "{error}");
    }

    #[test]
    fn capability_gaps_are_reported_per_adapter() {
        let mut registry = Registry::new();
        registry
            .register(Arc::new(Stub {
                id: "half".into(),
                caps: SessionCapabilities {
                    streaming: true,
                    abort: false,
                    resume: false,
                    tools: false,
                    approvals: false,
                    compact: false,
                    fork: false,
                    usage: false,
                },
            }))
            .expect("register");
        let failures = registry.capability_failures(SessionCapabilities::full());
        assert_eq!(failures.len(), 2, "{failures:?}");
        assert!(failures.iter().any(|failure| failure.code == "no_abort"));
        assert!(failures.iter().any(|failure| failure.code == "no_resume"));
    }

    #[test]
    fn cancel_is_shared_between_clones() {
        let cancel = Cancel::new();
        let clone = cancel.clone();
        assert!(!clone.is_cancelled());
        cancel.cancel();
        assert!(clone.is_cancelled());
    }
}
