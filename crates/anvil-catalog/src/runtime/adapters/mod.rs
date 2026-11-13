//! The built-in adapter set.
//!
//! This module is the only place that names concrete runtimes. Adding a runtime
//! means adding a module here plus one `register` line; nothing else in the
//! shared code changes.

pub mod claude;
pub mod codex;
pub mod common;
pub mod opencode;
pub mod pi;

use std::sync::Arc;

use crate::error::Result;
use crate::runtime::registry::Registry;

/// Registry with every shipped adapter.
pub fn default_registry() -> Result<Registry> {
    let mut registry = Registry::new();
    registry.register(Arc::new(pi::PiAdapter))?;
    registry.register(Arc::new(codex::CodexAdapter))?;
    registry.register(Arc::new(claude::ClaudeAdapter))?;
    registry.register(Arc::new(opencode::OpenCodeAdapter))?;
    Ok(registry)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_registry_has_four_adapters() {
        let registry = default_registry().expect("registry");
        assert_eq!(registry.ids(), vec!["claude", "codex", "opencode", "pi"]);
        for id in registry.ids() {
            let adapter = registry.get(&id).expect("adapter");
            assert!(!adapter.display_name().is_empty());
        }
    }
}
