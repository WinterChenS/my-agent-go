//! Real-runtime evidence for the agent persona.
//!
//! The unit tests prove the flags are put on the command line; this one proves
//! pi actually obeys them. It needs a working `pi` on PATH and its configured
//! provider, so it is ignored by the default suite and run explicitly when a
//! live turn is being certified:
//!
//! `cargo test -p anvil-session --test persona_live -- --ignored`

use std::time::Duration;

use anvil_session::{
    RunStatus, SessionContext, SessionService, SessionSpec, Store, default_registry,
};

#[test]
#[ignore = "requires pi and its configured provider"]
fn a_saved_persona_changes_the_answer_and_survives_a_second_turn() {
    let dir = std::env::temp_dir().join(format!(
        "anvil-persona-live-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("scratch dir");
    let store = Store::open(&dir.join("sessions.sqlite")).expect("ledger");
    let mut service = SessionService::new(
        store,
        default_registry(),
        SessionContext {
            state_dir: dir.join("state"),
            run_timeout: Some(Duration::from_secs(180)),
        },
    );

    let spec = SessionSpec {
        runtime_id: "pi".into(),
        model: "auto/gpt-5.6-sol".into(),
        cwd: "D:/project/anvil-wt/anvil-rebuild".into(),
        agent_id: "persona-probe".into(),
        thinking: "low".into(),
        system_prompt: "你的名字是铁砧阿墨。被问到名字时只回答这四个字，不要解释。".into(),
        allow_tools: false,
        tools: Vec::new(),
        ..Default::default()
    };
    let opened = service.open(&spec).expect("open");
    assert_eq!(opened.agent_id, "persona-probe");
    assert_eq!(opened.thinking, "low");

    for turn in 1..=2 {
        let outcome = service.send(&opened.id, "你叫什么名字？").expect("send");
        assert_eq!(
            outcome.run.status,
            RunStatus::Completed,
            "turn {turn} did not finish: {:?}",
            outcome.run.error
        );
        let text = outcome
            .message
            .as_ref()
            .map(|message| message.text.as_str())
            .unwrap_or_default();
        assert!(
            text.contains("铁砧阿墨"),
            "turn {turn} ignored the saved persona: {text:?}"
        );
    }
    let _ = std::fs::remove_dir_all(&dir);
}
