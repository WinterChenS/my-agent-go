//! Semantic-version parsing for `--version` output.
//!
//! CLIs print `1.5.0`, `codex-cli 0.153.0`, `opencode 1.18.23`; the adapters only
//! need the first `major.minor.patch` triple and an ordering.

/// A three-part version.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Version {
    pub major: u64,
    pub minor: u64,
    pub patch: u64,
}

impl Version {
    /// Parse a version from the first `N.N.N` run in `text`.
    pub fn find(text: &str) -> Option<Self> {
        let bytes = text.as_bytes();
        let mut index = 0;
        while index < bytes.len() {
            if !bytes[index].is_ascii_digit() {
                index += 1;
                continue;
            }
            // A version must start at a token boundary, so `2024-11-05` in a date
            // is not picked up ahead of the real version.
            let boundary =
                index == 0 || !bytes[index - 1].is_ascii_alphanumeric() && bytes[index - 1] != b'.';
            if !boundary {
                index += 1;
                continue;
            }
            if let Some((version, consumed)) = parse_at(&bytes[index..]) {
                return Some(version).filter(|_| consumed > 0);
            }
            index += 1;
        }
        None
    }

    pub fn at_least(&self, minimum: Version) -> bool {
        *self >= minimum
    }
}

fn parse_at(bytes: &[u8]) -> Option<(Version, usize)> {
    let mut cursor = 0;
    let major = read_number(bytes, &mut cursor)?;
    if bytes.get(cursor) != Some(&b'.') {
        return None;
    }
    cursor += 1;
    let minor = read_number(bytes, &mut cursor)?;
    if bytes.get(cursor) != Some(&b'.') {
        return None;
    }
    cursor += 1;
    let patch = read_number(bytes, &mut cursor)?;
    Some((
        Version {
            major,
            minor,
            patch,
        },
        cursor,
    ))
}

fn read_number(bytes: &[u8], cursor: &mut usize) -> Option<u64> {
    let start = *cursor;
    while bytes.get(*cursor).is_some_and(u8::is_ascii_digit) {
        *cursor += 1;
    }
    if *cursor == start {
        return None;
    }
    std::str::from_utf8(&bytes[start..*cursor])
        .ok()?
        .parse()
        .ok()
}

impl std::fmt::Display for Version {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(formatter, "{}.{}.{}", self.major, self.minor, self.patch)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_plain_and_prefixed_output() {
        assert_eq!(
            Version::find("1.5.0"),
            Some(Version {
                major: 1,
                minor: 5,
                patch: 0
            })
        );
        assert_eq!(
            Version::find("codex-cli 0.153.0"),
            Some(Version {
                major: 0,
                minor: 153,
                patch: 0
            })
        );
        assert_eq!(
            Version::find("opencode 1.18.23\n"),
            Some(Version {
                major: 1,
                minor: 18,
                patch: 23
            })
        );
    }

    #[test]
    fn ignores_dates_and_partial_versions() {
        assert_eq!(Version::find("built 2026-11-13"), None);
        assert_eq!(Version::find("version 1.5"), None);
        assert_eq!(Version::find("no numbers"), None);
    }

    #[test]
    fn orders_versions() {
        let old = Version {
            major: 0,
            minor: 99,
            patch: 9,
        };
        let new = Version {
            major: 0,
            minor: 153,
            patch: 0,
        };
        assert!(new.at_least(old));
        assert!(!old.at_least(new));
        assert_eq!(new.to_string(), "0.153.0");
    }
}
