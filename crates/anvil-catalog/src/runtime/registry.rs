//! Adapter registry.
//!
//! Registration is explicit and duplicate ids are rejected, which is what the
//! extension contract asks for: an unknown adapter can be injected by a test (or
//! by a future runtime) and the rest of the pipeline treats it like any other.

use std::collections::BTreeMap;
use std::sync::Arc;

use crate::error::{Error, Result};
use crate::model::Capabilities;
use crate::runtime::adapter::RuntimeAdapter;

/// Ordered set of adapters keyed by runtime id.
#[derive(Default)]
pub struct Registry {
    adapters: BTreeMap<String, Arc<dyn RuntimeAdapter>>,
}

impl Registry {
    pub fn new() -> Self {
        Self::default()
    }

    /// Register an adapter. The same id twice is a programming error and is
    /// surfaced instead of silently replacing the first adapter.
    pub fn register(&mut self, adapter: Arc<dyn RuntimeAdapter>) -> Result<()> {
        let id = adapter.id().to_string();
        if id.trim().is_empty() {
            return Err(Error::Rejected("adapter id must not be empty".into()));
        }
        if self.adapters.contains_key(&id) {
            return Err(Error::Rejected(format!(
                "runtime id {id:?} is already registered"
            )));
        }
        self.adapters.insert(id, adapter);
        Ok(())
    }

    pub fn get(&self, id: &str) -> Option<Arc<dyn RuntimeAdapter>> {
        self.adapters.get(id).cloned()
    }

    pub fn contains(&self, id: &str) -> bool {
        self.adapters.contains_key(id)
    }

    /// Ids in a stable order.
    pub fn ids(&self) -> Vec<String> {
        self.adapters.keys().cloned().collect()
    }

    /// Adapters in a stable order.
    pub fn all(&self) -> Vec<Arc<dyn RuntimeAdapter>> {
        self.adapters.values().cloned().collect()
    }

    pub fn capabilities(&self, id: &str) -> Option<Capabilities> {
        self.adapters.get(id).map(|adapter| adapter.capabilities())
    }

    pub fn is_empty(&self) -> bool {
        self.adapters.is_empty()
    }

    pub fn len(&self) -> usize {
        self.adapters.len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{Capabilities, RuntimeCandidate};
    use crate::runtime::adapter::{DiscoveryContext, ProbeContext, ProbeReport};

    struct Stub {
        id: &'static str,
    }

    impl RuntimeAdapter for Stub {
        fn id(&self) -> &'static str {
            self.id
        }
        fn display_name(&self) -> &'static str {
            "Stub"
        }
        fn capabilities(&self) -> Capabilities {
            Capabilities::none()
        }
        fn discover(&self, _context: &DiscoveryContext) -> Result<Vec<RuntimeCandidate>> {
            Ok(Vec::new())
        }
        fn probe(
            &self,
            _context: &ProbeContext,
            _candidate: &RuntimeCandidate,
        ) -> Result<ProbeReport> {
            Err(Error::Rejected("stub".into()))
        }
    }

    #[test]
    fn duplicate_ids_are_rejected() {
        let mut registry = Registry::new();
        registry
            .register(Arc::new(Stub { id: "alpha" }))
            .expect("first");
        let error = registry
            .register(Arc::new(Stub { id: "alpha" }))
            .expect_err("duplicate");
        assert_eq!(error.code(), "rejected");
        assert!(error.message().contains("already registered"));
        assert_eq!(registry.len(), 1);
    }

    #[test]
    fn unknown_ids_are_registrable_and_listed_in_order() {
        let mut registry = Registry::new();
        registry
            .register(Arc::new(Stub { id: "zeta" }))
            .expect("zeta");
        registry
            .register(Arc::new(Stub { id: "alpha" }))
            .expect("alpha");
        assert_eq!(
            registry.ids(),
            vec!["alpha".to_string(), "zeta".to_string()]
        );
        assert!(registry.get("alpha").is_some());
        assert!(registry.get("missing").is_none());
    }

    #[test]
    fn empty_ids_are_rejected() {
        let mut registry = Registry::new();
        let error = registry
            .register(Arc::new(Stub { id: "  " }))
            .expect_err("empty");
        assert_eq!(error.code(), "rejected");
    }
}
