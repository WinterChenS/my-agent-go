//! Chat session contract and event ledger for Anvil (phase 2).
//!
//! * `model` — the wire types shared with the host and the UI;
//! * `store` — SQLite index (sessions, messages, runs, events);
//! * `context` — `@` references: confinement, budgets and prompt folding;
//! * `rail` — the right rail: execution, artifacts, citations, local preview;
//! * `persona` — renders an agent's saved system prompt for one session;
//! * `export` — HTML/Markdown/JSON transcripts, confined and atomic;
//! * `adapter` — the `SessionAdapter` contract and its registry;
//! * `adapters` — the two shipped adapters (Pi, Codex);
//! * `service` — open/send/abort/resume orchestration on top of the registry;
//! * `workbench` — the home page's counts and activity grid, from the same rows.
//! * `schedule` — cron triggers and the runs they produced, in the same ledger.
//!
//! The ledger is append-only: a run writes events in `seq` order inside one
//! transaction, and a session's message list is derived from those events, so a
//! crashed run still leaves everything that actually arrived.

pub mod adapter;
pub mod audit;
pub mod adapters;
pub mod clock;
pub mod context;
pub mod error;
pub mod export;
pub mod extensions;
pub mod memory;
pub mod model;
pub mod persona;
pub mod preview;
pub mod rail;
pub mod schedule;
pub mod service;
pub mod store;
pub mod subagent;
pub mod subagents;
pub mod ui;
pub mod workbench;

pub use adapter::{CompactOutcome, ContextUsage, Registry, SessionAdapter};
pub use adapters::{default_registry, default_registry_with_pi};
pub use error::{Error, Result};
pub use model::*;
pub use service::{Approvals, Cancels, SendOutcome, SessionService, Steers};
pub use store::Store;
