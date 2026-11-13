//! Errors carry a stable `code()` so the host and the UI can branch on the
//! class of failure instead of matching on message text.

use std::fmt::{Display, Formatter};

pub type Result<T> = std::result::Result<T, Error>;

#[derive(Debug)]
pub enum Error {
    /// SQLite or filesystem failure.
    Io(String),
    /// A row or runtime the caller named does not exist.
    NotFound(String),
    /// The caller asked for something the contract forbids.
    Rejected(String),
    /// The runtime did not answer inside the budget.
    Timeout(String),
    /// The runtime answered, but the run failed.
    Failed(String),
}

impl Error {
    /// Stable identifier for the wire and for tests.
    #[must_use]
    pub fn code(&self) -> &'static str {
        match self {
            Self::Io(_) => "io",
            Self::NotFound(_) => "not_found",
            Self::Rejected(_) => "rejected",
            Self::Timeout(_) => "timeout",
            Self::Failed(_) => "failed",
        }
    }

    /// The shape the host sends to the UI.
    #[must_use]
    pub fn to_wire(&self) -> serde_json::Value {
        serde_json::json!({ "code": self.code(), "message": self.detail() })
    }

    fn detail(&self) -> &str {
        match self {
            Self::Io(detail)
            | Self::NotFound(detail)
            | Self::Rejected(detail)
            | Self::Timeout(detail)
            | Self::Failed(detail) => detail,
        }
    }
}

impl Display for Error {
    fn fmt(&self, formatter: &mut Formatter<'_>) -> std::fmt::Result {
        write!(formatter, "{}: {}", self.code(), self.detail())
    }
}

impl std::error::Error for Error {}

impl From<rusqlite::Error> for Error {
    fn from(error: rusqlite::Error) -> Self {
        Self::Io(format!("sqlite: {error}"))
    }
}

impl From<std::io::Error> for Error {
    fn from(error: std::io::Error) -> Self {
        Self::Io(format!("io: {error}"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn codes_are_stable() {
        assert_eq!(Error::Io(String::new()).code(), "io");
        assert_eq!(Error::NotFound(String::new()).code(), "not_found");
        assert_eq!(Error::Rejected(String::new()).code(), "rejected");
        assert_eq!(Error::Timeout(String::new()).code(), "timeout");
        assert_eq!(Error::Failed(String::new()).code(), "failed");
    }

    #[test]
    fn wire_shape_carries_code_and_message() {
        let wire = Error::Rejected("nope".into()).to_wire();
        assert_eq!(wire["code"], "rejected");
        assert_eq!(wire["message"], "nope");
    }
}
