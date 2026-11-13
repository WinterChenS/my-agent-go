//! Chat session contract and event ledger for Anvil (phase 2).
//!
//! * `model` — the wire types shared with the host and the UI;
//! * `store` — SQLite index (sessions, messages, runs, events);
//! * `context` — `@` references: confinement, budgets and prompt folding;
//! * `adapter` — the `SessionAdapter` contract and its registry;
//! * `adapters` — the two shipped adapters (Pi, Codex);
//! * `service` — open/send/abort/resume orchestration on top of the registry.
//!
//! The ledger is append-only: a run writes events in `seq` order inside one
//! transaction, and a session's message list is derived from those events, so a
//! crashed run still leaves everything that actually arrived.

pub mod adapter;
pub mod adapters;
pub mod clock;
pub mod context;
pub mod error;
pub mod model;
pub mod service;
pub mod store;

pub use adapter::{Registry, SessionAdapter};
pub use adapters::default_registry;
pub use error::{Error, Result};
pub use model::*;
pub use service::{Approvals, Cancels, SendOutcome, SessionService};
pub use store::Store;
