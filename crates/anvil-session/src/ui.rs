//! Questions the runtime asks the user, and the answers that unblock it.
//!
//! A runtime that runs headless still has to ask the human something: pi's
//! extensions reach for `ctx.ui`, which in RPC mode arrives as an
//! `extension_ui_request` on stdout and blocks until the client writes an
//! `extension_ui_response` back. Dropping the request leaves the turn waiting
//! forever, so the request has to reach the page and the answer has to come
//! back the same way.
//!
//! The registry is a separate clonable handle, exactly like [`Approvals`]: the
//! runtime blocks on it inside the adapter's thread, and the page answers
//! through another command, so nothing may hold the service lock while the
//! question is open.

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Condvar, Mutex, OnceLock};
use std::time::{Duration, Instant};

use crate::adapter::Cancel;
use crate::model::{UiAnswer, UiRequest};

/// The one registry every runtime and the host share.
///
/// The registry has to outlive the object that created it: the runtime blocks
/// on it inside the adapter, and the page answers it through a command that
/// must not wait for the run to finish. A process-level handle is the only
/// place both can meet without one of them holding a lock the other needs.
static REGISTRY: OnceLock<ExtensionUis> = OnceLock::new();

/// The shared registry of open questions.
#[must_use]
pub fn extension_uis() -> ExtensionUis {
    REGISTRY.get_or_init(ExtensionUis::new).clone()
}

/// Open questions, addressable while a run is blocked on them.
#[derive(Clone, Default)]
pub struct ExtensionUis {
    inner: Arc<Mutex<BTreeMap<String, Arc<Pending>>>>,
}

struct Pending {
    /// The request as the runtime phrased it. The session is stamped in once
    /// the host knows which conversation the question belongs to.
    request: Mutex<UiRequest>,
    answer: Mutex<Option<UiAnswer>>,
    signal: Condvar,
}

/// Keeps the generated ids unique when several runtimes ask at once.
static UI_COUNTER: AtomicU64 = AtomicU64::new(0);

impl ExtensionUis {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Ask the user and wait for the answer.
    ///
    /// The runtime is blocked while this waits, so the wait ends as soon as the
    /// page answers, the runtime's own timeout expires, or the run is
    /// cancelled. A timeout is a dismissal, never a yes.
    pub fn ask(
        &self,
        request: UiRequest,
        timeout: Option<Duration>,
        cancel: &Cancel,
    ) -> UiAnswer {
        let id = if request.id.trim().is_empty() {
            format!(
                "{}-{}",
                request.session_id,
                UI_COUNTER.fetch_add(1, Ordering::SeqCst)
            )
        } else {
            request.id.clone()
        };
        let mut request = request;
        request.id = id.clone();
        let pending = Arc::new(Pending {
            request: Mutex::new(request.clone()),
            answer: Mutex::new(None),
            signal: Condvar::new(),
        });
        if let Ok(mut inner) = self.inner.lock() {
            inner.insert(id.clone(), pending.clone());
        }

        let deadline = timeout.map(|budget| Instant::now() + budget);
        let mut slot = pending.answer.lock().expect("extension ui lock");
        while slot.is_none() {
            if cancel.is_cancelled() {
                break;
            }
            let now = Instant::now();
            if let Some(deadline) = deadline
                && now >= deadline
            {
                break;
            }
            let wait = deadline
                .map(|deadline| deadline.saturating_duration_since(now))
                .unwrap_or(Duration::from_millis(200));
            // A short wait keeps the cancellation check alive without spinning.
            let (guard, _) = pending
                .signal
                .wait_timeout(slot, wait.min(Duration::from_millis(200)))
                .expect("extension ui wait");
            slot = guard;
        }
        let answer = slot.clone().unwrap_or_else(UiAnswer::cancelled);
        drop(slot);
        if let Ok(mut inner) = self.inner.lock() {
            inner.remove(&id);
        }
        answer
    }

    /// Record which conversation a question belongs to.
    ///
    /// The runtime only knows its own ids; the host knows the conversation, and
    /// an answer must not be able to cross over into another one.
    pub fn stamp(&self, request_id: &str, session_id: &str) {
        let Ok(inner) = self.inner.lock() else { return };
        let Some(pending) = inner.get(request_id) else {
            return;
        };
        if let Ok(mut request) = pending.request.lock() {
            request.session_id = session_id.to_string();
        }
    }

    /// Answer one open question.
    ///
    /// The session is checked: an id from another session must not be
    /// answerable from this one, or a stale card could answer a question the
    /// user never saw.
    pub fn resolve(
        &self,
        session_id: &str,
        request_id: &str,
        answer: UiAnswer,
    ) -> Result<(), String> {
        let pending = self
            .inner
            .lock()
            .map_err(|_| "extension ui registry poisoned".to_string())?
            .get(request_id)
            .cloned()
            .ok_or_else(|| format!("question {request_id:?} is not open"))?;
        let owner = pending
            .request
            .lock()
            .map(|request| request.session_id.clone())
            .unwrap_or_default();
        if !owner.is_empty() && owner != session_id {
            return Err(format!(
                "question {request_id:?} belongs to session {owner:?}, not {session_id:?}"
            ));
        }
        let mut slot = pending
            .answer
            .lock()
            .map_err(|_| "extension ui answer poisoned".to_string())?;
        if slot.is_some() {
            return Err(format!("question {request_id:?} was already answered"));
        }
        *slot = Some(answer);
        pending.signal.notify_all();
        Ok(())
    }

    /// Dismiss every open question of one session.
    pub fn cancel_session(&self, session_id: &str) {
        if let Ok(inner) = self.inner.lock() {
            let open: Vec<String> = inner
                .iter()
                .filter(|(_, pending)| {
                    pending
                        .request
                        .lock()
                        .map(|request| request.session_id == session_id)
                        .unwrap_or(false)
                })
                .map(|(id, _)| id.clone())
                .collect();
            for id in open {
                if let Some(pending) = inner.get(&id).cloned() {
                    let mut slot = pending.answer.lock().expect("extension ui lock");
                    if slot.is_none() {
                        *slot = Some(UiAnswer::cancelled());
                    }
                    drop(slot);
                    pending.signal.notify_all();
                }
            }
        }
    }

    /// The questions that are still open, for a session or for all of them.
    #[must_use]
    pub fn pending(&self, session_id: Option<&str>) -> Vec<UiRequest> {
        let Ok(inner) = self.inner.lock() else {
            return Vec::new();
        };
        inner
            .values()
            .filter_map(|pending| pending.request.lock().ok().map(|request| request.clone()))
            .filter(|request| session_id.is_none_or(|id| request.session_id == id))
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::UiRequest;

    fn request(id: &str, session: &str) -> UiRequest {
        UiRequest {
            id: id.to_string(),
            session_id: session.to_string(),
            run_id: "r1".to_string(),
            method: "confirm".to_string(),
            title: "允许吗".to_string(),
            ..UiRequest::default()
        }
    }

    #[test]
    fn an_answer_unblocks_the_waiter() {
        let uis = ExtensionUis::new();
        let cancel = Cancel::new();
        let handle = {
            let uis = uis.clone();
            std::thread::spawn(move || {
                uis.ask(request("q1", "s1"), None, &cancel)
            })
        };
        // The question is open until it is answered.
        for _ in 0..50 {
            if !uis.pending(Some("s1")).is_empty() {
                break;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        assert_eq!(uis.pending(Some("s1")).len(), 1);
        uis.resolve("s1", "q1", UiAnswer {
            confirmed: Some(true),
            ..UiAnswer::default()
        })
        .expect("answer");
        let answer = handle.join().expect("waiter");
        assert_eq!(answer.confirmed, Some(true));
        assert!(uis.pending(Some("s1")).is_empty());
    }

    #[test]
    fn a_question_of_another_session_cannot_be_answered() {
        let uis = ExtensionUis::new();
        let cancel = Cancel::new();
        let handle = {
            let uis = uis.clone();
            std::thread::spawn(move || uis.ask(request("q1", "s1"), None, &cancel))
        };
        for _ in 0..50 {
            if !uis.pending(Some("s1")).is_empty() {
                break;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        assert!(uis.resolve("s2", "q1", UiAnswer::default()).is_err());
        uis.resolve("s1", "q1", UiAnswer::default()).expect("answer");
        handle.join().expect("waiter");
    }

    #[test]
    fn a_timeout_dismisses_the_question() {
        let uis = ExtensionUis::new();
        let cancel = Cancel::new();
        let answer = uis.ask(
            request("q1", "s1"),
            Some(Duration::from_millis(50)),
            &cancel,
        );
        assert!(answer.cancelled);
        assert_eq!(answer.confirmed, Some(false));
    }

    #[test]
    fn a_cancelled_run_dismisses_the_question() {
        let uis = ExtensionUis::new();
        let cancel = Cancel::new();
        cancel.cancel();
        let answer = uis.ask(request("q1", "s1"), None, &cancel);
        assert!(answer.cancelled);
    }
}
