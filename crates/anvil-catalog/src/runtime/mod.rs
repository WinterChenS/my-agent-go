//! Runtime discovery, registration and resource sync.
//!
//! `adapter` is the extension contract, `registry` rejects duplicate ids,
//! `service` owns sequencing and persistence, `adapters/` holds the shipped
//! implementations.

pub mod adapter;
pub mod adapters;
pub mod bundled;
pub mod node_bootstrap;
pub mod pi_import;
pub mod process;
pub mod registry;
pub mod service;
pub mod verify;
pub mod version;
