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
use crate::runtime::adapter::RuntimeAdapter;
use crate::runtime::bundled::BundledPi;
use crate::runtime::registry::Registry;

/// Runtimes whose adapter ships in this build but is not registered.
///
/// A list rather than a comment so a test can prove they stay off: a parked
/// runtime that quietly came back would re-open the branches this build does
/// not test.
pub const PARKED: [&str; 3] = ["codex", "claude", "opencode"];

/// Registry with the adapters this build actually drives.
///
/// The pi adapter here discovers an installation on the machine. The desktop
/// host uses [`default_registry_with_pi`] to hand in the bundled runtime
/// instead (ADR 0009).
pub fn default_registry() -> Result<Registry> {
    default_registry_with_pi(Arc::new(pi::PiAdapter::system()))
}

/// Same registry, with the pi adapter supplied by the caller.
///
/// The host builds it around the runtime that ships in the installer; tests
/// build it around a stub or a scratch bundle. Nothing else in the registry
/// changes.
pub fn default_registry_with_pi(pi: Arc<dyn RuntimeAdapter>) -> Result<Registry> {
    let mut registry = Registry::new();
    registry.register(pi)?;
    // Parked on purpose (see the module docs). Re-enabling one is this line:
    //
    //     registry.register(Arc::new(codex::CodexAdapter))?;
    Ok(registry)
}

/// The registry for one process, given what startup found about the bundled
/// runtime.
///
/// This is the single place that decides where pi comes from, and the middle
/// arm is the one that used to be missing: when the installer's tree failed to
/// load, the host handed nothing in and the registry fell through to
/// `PiAdapter::system()`, i.e. to whatever `pi` was on the machine's `PATH`.
/// That is the fallback ADR 0009 removes — a damaged bundle offers no runtime at
/// all, and says why.
///
/// `(None, None)` is not a damaged install: it is a host with no install
/// resources to check (a development run, a test), which keeps discovering the
/// machine's runtime.
pub fn registry_for_pi(
    bundle: Option<Arc<BundledPi>>,
    failure: Option<&str>,
) -> Result<Registry> {
    match (bundle, failure) {
        (Some(bundle), _) => default_registry_with_pi(Arc::new(pi::PiAdapter::with_bundle(bundle))),
        (None, Some(reason)) => {
            default_registry_with_pi(Arc::new(pi::PiAdapter::unavailable(reason.to_string())))
        }
        (None, None) => default_registry(),
    }
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
        assert_eq!(
            adapter.unavailable_reason(),
            None,
            "a system adapter is merely empty, not unavailable"
        );
    }

    /// The middle arm of [`registry_for_pi`]: a failed load is a reason, not a
    /// licence to go looking for the machine's pi.
    #[test]
    fn a_failed_load_registers_an_unavailable_pi() {
        let registry = registry_for_pi(None, Some("the bundled Pi runtime is missing"))
            .expect("registry");
        let adapter = registry.get("pi").expect("pi is still registered");
        assert_eq!(
            adapter.unavailable_reason(),
            Some("the bundled Pi runtime is missing")
        );
        let candidates = adapter
            .discover(&crate::runtime::adapter::DiscoveryContext {
                home: std::path::PathBuf::from("/nonexistent"),
                timeout: std::time::Duration::from_secs(1),
            })
            .expect("discover");
        assert!(candidates.is_empty(), "{candidates:?}");
    }
}
