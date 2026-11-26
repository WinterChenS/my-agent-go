//! Compact one real session and print what the ledger holds afterwards.
//!
//! ```text
//! cargo run -p anvil-session --example compact_probe -- \
//!     --database .tmp/compact-probe/sessions.sqlite \
//!     --state-dir .tmp/compact-probe/state \
//!     --session pi-1795415535864-0 --keep-last 2
//! ```
//!
//! Exists because compaction is the one session operation that cannot be
//! proven by a unit test against the echo adapter: whether pi itself accepts
//! the command, and what it says when it refuses, only shows up against a real
//! runtime. Run it against a *copy* of the ledger and a copy of the pi
//! transcript, never the live ones.

use std::path::PathBuf;
use std::process::ExitCode;

use anvil_session::{default_registry, SessionContext, SessionService, Store};

fn main() -> ExitCode {
    let mut database: Option<PathBuf> = None;
    let mut state_dir: Option<PathBuf> = None;
    let mut session = String::new();
    let mut keep_last = 2usize;

    let mut args = std::env::args().skip(1);
    while let Some(flag) = args.next() {
        let mut value = || args.next().map(PathBuf::from);
        match flag.as_str() {
            "--database" => database = value(),
            "--state-dir" => state_dir = value(),
            "--session" => session = args.next().unwrap_or_default(),
            "--keep-last" => {
                keep_last = args
                    .next()
                    .and_then(|raw| raw.parse().ok())
                    .unwrap_or(2)
            }
            other => {
                eprintln!("compact_probe: unknown argument {other:?}");
                return ExitCode::from(2);
            }
        }
    }
    let (Some(database), Some(state_dir)) = (database, state_dir) else {
        eprintln!("compact_probe: --database and --state-dir are required");
        return ExitCode::from(2);
    };
    if session.is_empty() {
        eprintln!("compact_probe: --session is required");
        return ExitCode::from(2);
    }
    match run(database, state_dir, &session, keep_last) {
        Ok(()) => ExitCode::SUCCESS,
        Err(message) => {
            eprintln!("compact_probe: {message}");
            ExitCode::from(1)
        }
    }
}

fn run(database: PathBuf, state_dir: PathBuf, session: &str, keep_last: usize) -> Result<(), String> {
    let store = Store::open(&database).map_err(|error| error.to_wire().to_string())?;
    let mut service = SessionService::new(
        store,
        default_registry(),
        SessionContext {
            state_dir,
            run_timeout: None,
        },
    );
    // Same as the host on startup: a run the previous process left behind is
    // `failed`, not `running`, or every command is refused as "in flight".
    let recovered = service.recover().map_err(|error| error.to_wire().to_string())?;
    if recovered > 0 {
        println!("recovered: {recovered} interrupted run(s)");
    }
    let before = service.detail(session).map_err(|error| error.to_wire().to_string())?;
    println!("before: {} message(s)", before.messages.len());

    let report = service
        .compact(session, keep_last, None)
        .map_err(|error| error.to_wire().to_string())?;
    println!("report: {report:#?}");

    let after = service.detail(session).map_err(|error| error.to_wire().to_string())?;
    println!("after: {} message(s)", after.messages.len());
    for message in &after.messages {
        let meta = message
            .meta
            .as_ref()
            .map(|value| value.to_string())
            .unwrap_or_default();
        let text: String = message.text.chars().take(70).collect();
        println!("  [{}] {} | {} | {}", message.seq, message.role.as_str(), text, meta);
    }
    Ok(())
}
