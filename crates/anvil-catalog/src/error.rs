//! Error type shared by the catalog, the registry and the Tauri commands.

use std::fmt;

/// Catalog failure. Every variant carries a message the UI can show as-is and
/// the acceptance runner can match on.
#[derive(Debug)]
pub enum Error {
    /// SQLite failure (query, constraint, migration).
    Store(String),
    /// Filesystem failure while scanning or writing.
    Io(String),
    /// A payload did not match the expected schema.
    Format(String),
    /// The caller asked for something the catalog refuses (duplicate id,
    /// unknown capability, missing registration).
    Rejected(String),
    /// The requested row does not exist.
    NotFound(String),
    /// An operation exceeded its deadline.
    Timeout(String),
}

impl Error {
    /// Short machine-readable prefix used by the CLI and the UI.
    pub fn code(&self) -> &'static str {
        match self {
            Error::Store(_) => "store",
            Error::Io(_) => "io",
            Error::Format(_) => "format",
            Error::Rejected(_) => "rejected",
            Error::NotFound(_) => "not_found",
            Error::Timeout(_) => "timeout",
        }
    }

    pub fn message(&self) -> &str {
        match self {
            Error::Store(text)
            | Error::Io(text)
            | Error::Format(text)
            | Error::Rejected(text)
            | Error::NotFound(text)
            | Error::Timeout(text) => text,
        }
    }

    /// `code: message`, the shape the Tauri commands return.
    pub fn to_wire(&self) -> String {
        format!("{}: {}", self.code(), self.message())
    }
}

impl fmt::Display for Error {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(formatter, "{}: {}", self.code(), self.message())
    }
}

impl std::error::Error for Error {}

impl From<rusqlite::Error> for Error {
    fn from(error: rusqlite::Error) -> Self {
        Error::Store(error.to_string())
    }
}

impl From<std::io::Error> for Error {
    fn from(error: std::io::Error) -> Self {
        Error::Io(error.to_string())
    }
}

impl From<serde_json::Error> for Error {
    fn from(error: serde_json::Error) -> Self {
        Error::Format(error.to_string())
    }
}

/// Crate result alias.
pub type Result<T> = std::result::Result<T, Error>;
