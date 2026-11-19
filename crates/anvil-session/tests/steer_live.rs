//! Real-runtime evidence that a streaming turn can be steered.
//!
//! The unit tests prove the command shape. This one proves the behaviour the
//! composer depends on: a prompt starts a run, a second message written into
//! the same live pipe reaches the model *during* that run, and the ledger gets
//! a `user_steer` row for it.
//!
//! It needs `pi` on `PATH`, its configured provider and two cheap model turns,
//! so it is ignored by the default suite:
//!
//! `cargo test -p anvil-session --test steer_live -- --ignored --nocapture`

use std::sync::{Arc, Mutex};
use std::time::Duration;

use anvil_session::adapter::{AdapterEvent, Cancel, SessionAdapter, Turn};
use anvil_session::adapters::{find_on_path, pi::PiAdapter};
use anvil_session::model::{EventKind, OpenedSession, SessionContext};

#[test]
#[ignore = "requires pi on PATH, its configured provider, and model turns"]
fn a_message_written_into_the_live_run_reaches_the_model() {
    let Some(exe) = find_on_path("pi") else {
        panic!("pi is not on PATH");
    };
    let root = std::env::temp_dir().join(format!("anvil-steer-live-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&root);
    let work = root.join("work");
    std::fs::create_dir_all(&work).expect("work dir");

    let adapter = Arc::new(PiAdapter::new(Some(exe)));
    let context = SessionContext {
        state_dir: work.clone(),
        run_timeout: None,
    };
    let opened = OpenedSession {
        runtime_id: "pi".into(),
        native_id: format!("steer-{}", std::process::id()),
        model: String::new(),
        cwd: work.display().to_string(),
        allow_tools: true,
        ..OpenedSession::default()
    };

    let seen: Arc<Mutex<Vec<AdapterEvent>>> = Arc::new(Mutex::new(Vec::new()));
    let sink_seen = seen.clone();
    let run_adapter = adapter.clone();
    let run_opened = opened.clone();
    let runner = std::thread::spawn(move || {
        let cancel = Cancel::new();
        let mut sink = |event: AdapterEvent| {
            sink_seen.lock().expect("events").push(event);
        };
        run_adapter.send_turn(
            &context,
            &run_opened,
            &Turn::text("Use bash to run `sleep 8`, then answer with the single word DONE."),
            &cancel,
            &mut sink,
        )
    });

    // Long enough for the child to start and begin its tool call, short enough
    // that the run is certainly still going.
    std::thread::sleep(Duration::from_secs(4));
    let accepted = adapter
        .steer(&opened, "When you answer, also print the word PINEAPPLE on its own line.")
        .expect("steer");
    assert!(accepted, "there was no live run to steer");

    let outcome = runner.join().expect("run thread").expect("turn");
    assert_eq!(outcome.status, anvil_session::model::RunStatus::Completed);

    let events = seen.lock().expect("events");
    let steer = events
        .iter()
        .find(|event| event.kind == EventKind::UserSteer)
        .expect("the ledger needs a row for a message sent mid-run");
    assert!(
        steer
            .payload
            .get("text")
            .and_then(|value| value.as_str())
            .unwrap_or("")
            .contains("PINEAPPLE"),
        "{:?}",
        steer.payload
    );
    assert!(
        outcome.text.contains("PINEAPPLE"),
        "the model never saw the steer: {:?}",
        outcome.text
    );

    let _ = std::fs::remove_dir_all(&root);
}
