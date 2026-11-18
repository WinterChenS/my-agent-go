//! The built-in adapter set.
//!
//! This module is the only place that names concrete runtimes. Adding a runtime
//! means adding a module here plus one `register` line; nothing else in the
//! shared code changes.
//!
//! ## Only pi is wired in this build
//!
//! The codex, claude and opencode adapters are still here and still compile, but
//! none of them is registered: the product is being finished around pi, and
//! offering four half-checked runtimes made every surface — discovery, models,
//! agents, the runtime page, chat — carry branches nobody was testing end to
//! end. See [`PARKED`].
//!
//! A parked adapter is disconnected, not deleted. Re-enabling one is putting its
//! `register` line back, and everything downstream follows: discovery reports
//! it, the wizard lists it, the models page grows its tab, and the rows an older
//! build left in `catalog.sqlite` are re-discovered. Nothing else needs editing —
//! [`crate::store::Store::forget_unregistered`] has been keeping those rows out
//! of the database in the meantime.

pub mod claude;
pub mod codex;
pub mod common;
pub mod opencode;
pub mod pi;

use std::sync::Arc;

use crate::error::Result;
use crate::runtime::registry::Registry;

/// Runtimes whose adapter ships in this build but is not registered.
///
/// A list rather than a comment so a test can prove they stay off: a parked
/// runtime that quietly came back would re-open the branches this build does
/// not test.
pub const PARKED: [&str; 3] = ["codex", "claude", "opencode"];

/// Registry with the adapters this build actually drives.
pub fn default_registry() -> Result<Registry> {
    let mut registry = Registry::new();
    registry.register(Arc::new(pi::PiAdapter))?;
    // Parked on purpose (see the module docs). Re-enabling one is this line:
    //
    //     registry.register(Arc::new(codex::CodexAdapter))?;
    Ok(registry)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_pi_is_wired() {
        let registry = default_registry().expect("registry");
        assert_eq!(registry.ids(), vec!["pi".to_string()]);
        for id in PARKED {
            assert!(
                registry.get(id).is_none(),
                "{id} is parked and must not be registered"
            );
        }
        let adapter = registry.get("pi").expect("pi adapter");
        assert!(!adapter.display_name().is_empty());
    }
}
