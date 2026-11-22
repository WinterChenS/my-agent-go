//! Catalog and runtime integration for Anvil (phase 1).
//!
//! * `model` — the data types shared with the host and the UI;
//! * `ids` — stable registration identity from a normalised path;
//! * `store` — SQLite persistence (one transaction per scan, snapshots kept on
//!   failure);
//! * `skills` / `mcp` — runtime-independent inventory readers, with redaction
//!   and a trusted-command check for the explicit MCP probe;
//! * `secrets` — DPAPI-encrypted credentials, never written in plaintext;
//! * `runtime` — the adapter contract, the registry, the scan/sync service and
//!   the four shipped adapters (Pi, Codex, Claude Code, OpenCode).

pub mod clock;
pub mod connector;
pub mod error;
pub mod ids;
pub mod mcp;
pub mod model;
pub mod runtime;
pub mod secrets;
pub mod skills;
pub mod store;

pub use error::{Error, Result};
pub use model::*;
pub use runtime::service::CatalogService;
pub use store::Store;
