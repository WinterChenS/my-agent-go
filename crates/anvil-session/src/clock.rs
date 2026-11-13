//! Unix time helpers.
//!
//! The catalog crate has the same two functions; they are duplicated here on
//! purpose so this crate does not depend on the catalog (the session ledger
//! must stay usable without a runtime inventory).

use std::time::{SystemTime, UNIX_EPOCH};

/// Seconds since the Unix epoch, saturating at 0 for clocks before 1970.
#[must_use]
pub fn unix_now() -> i64 {
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(duration) => i64::try_from(duration.as_secs()).unwrap_or(i64::MAX),
        Err(_) => 0,
    }
}

/// Milliseconds since the Unix epoch, used to order events inside a run.
#[must_use]
pub fn unix_millis() -> i64 {
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(duration) => i64::try_from(duration.as_millis()).unwrap_or(i64::MAX),
        Err(_) => 0,
    }
}

/// RFC 3339 in UTC, e.g. `2026-11-13T05:27:23Z`.
#[must_use]
pub fn rfc3339(epoch_seconds: i64) -> String {
    let days = epoch_seconds.div_euclid(86_400);
    let seconds_of_day = epoch_seconds.rem_euclid(86_400);
    let (year, month, day) = civil_from_days(days);
    let hour = seconds_of_day / 3_600;
    let minute = (seconds_of_day % 3_600) / 60;
    let second = seconds_of_day % 60;
    format!("{year:04}-{month:02}-{day:02}T{hour:02}:{minute:02}:{second:02}Z")
}

/// Current time as RFC 3339.
#[must_use]
pub fn now_rfc3339() -> String {
    rfc3339(unix_now())
}

/// Howard Hinnant's `civil_from_days`: days since 1970-01-01 to a civil date.
fn civil_from_days(days: i64) -> (i64, u32, u32) {
    let shifted = days + 719_468;
    let era = if shifted >= 0 {
        shifted / 146_097
    } else {
        (shifted - 146_096) / 146_097
    };
    let day_of_era = shifted - era * 146_097;
    let year_of_era =
        (day_of_era - day_of_era / 1_460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let year = year_of_era + era * 400;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let month_piece = (5 * day_of_year + 2) / 153;
    let day = u32::try_from(day_of_year - (153 * month_piece + 2) / 5 + 1).unwrap_or(1);
    let month = u32::try_from(if month_piece < 10 {
        month_piece + 3
    } else {
        month_piece - 9
    })
    .unwrap_or(1);
    (if month <= 2 { year + 1 } else { year }, month, day)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn formats_known_instants() {
        assert_eq!(rfc3339(0), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339(1_700_000_000), "2023-11-14T22:13:20Z");
    }

    #[test]
    fn handles_leap_day_and_negative_days() {
        assert_eq!(rfc3339(1_709_164_800), "2024-02-29T00:00:00Z");
        assert_eq!(rfc3339(-86_400), "1969-12-31T00:00:00Z");
    }
}
