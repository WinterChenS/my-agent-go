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

/// Today's date as `YYYY-MM-DD`, for persona placeholders.
#[must_use]
pub fn today() -> String {
    let stamp = now_rfc3339();
    stamp.split('T').next().unwrap_or_default().to_string()
}

/// `YYYY-MM-DD` to `(year, month, day)`, tolerating a trailing timestamp.
#[must_use]
pub fn parse_ymd(stamp: &str) -> Option<(i64, i64, i64)> {
    let mut parts = stamp.split('-');
    let year = parts.next()?.parse().ok()?;
    let month = parts.next()?.parse().ok()?;
    let day = parts.next()?.get(..2)?.parse().ok()?;
    Some((year, month, day))
}

/// Howard Hinnant's `days_from_civil`: days since 1970-01-01 for a civil date.
#[must_use]
pub fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let y = year - if month <= 2 { 1 } else { 0 };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = month + if month > 2 { -3 } else { 9 };
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// Move a `YYYY-MM-DD` date by whole days. Calendar arithmetic, not a timer.
#[must_use]
pub fn shift_date(date: &str, delta: i64) -> Option<String> {
    let (year, month, day) = parse_ymd(date)?;
    let (year, month, day) = civil_from_days(days_from_civil(year, month, day) + delta);
    Some(format!("{year:04}-{month:02}-{day:02}"))
}

/// `0` = Sunday, matching JavaScript's `Date#getDay`.
#[must_use]
pub fn weekday(date: &str) -> Option<i64> {
    let (year, month, day) = parse_ymd(date)?;
    Some((days_from_civil(year, month, day) + 4).rem_euclid(7))
}

/// Today's date in the viewer's timezone, derived from the same UTC clock the
/// ledger writes. The UI passes `Date#getTimezoneOffset` negated.
#[must_use]
pub fn local_today(offset_minutes: i64) -> String {
    let days = (unix_now() + offset_minutes.saturating_mul(60)).div_euclid(86_400);
    let (year, month, day) = civil_from_days(days);
    format!("{year:04}-{month:02}-{day:02}")
}

/// The viewer's local date for one UTC ledger stamp, e.g.
/// `2026-11-21T16:30:00Z` with `+480` becomes `2026-11-22`.
///
/// Ledger timestamps are UTC to the second. Doing this on the host keeps one
/// definition of "today" for the stats, the heatmap and the session rows; if
/// React guessed it, a UTC evening run would land on both of two days.
#[must_use]
pub fn local_date(stamp: &str, offset_minutes: i64) -> Option<String> {
    if stamp.len() < 19 {
        return None;
    }
    let (year, month, day) = parse_ymd(stamp)?;
    let hh: i64 = stamp.get(11..13)?.parse().ok()?;
    let mm: i64 = stamp.get(14..16)?.parse().ok()?;
    let ss: i64 = stamp.get(17..19)?.parse().ok()?;
    let seconds = days_from_civil(year, month, day) * 86_400
        + hh * 3_600
        + mm * 60
        + ss
        + offset_minutes.saturating_mul(60);
    let (year, month, day) = civil_from_days(seconds.div_euclid(86_400));
    Some(format!("{year:04}-{month:02}-{day:02}"))
}

/// Howard Hinnant's `civil_from_days`: days since 1970-01-01 to a civil date.
#[must_use]
pub fn civil_from_days(days: i64) -> (i64, u32, u32) {
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

    #[test]
    fn shifts_dates_across_month_and_leap_day() {
        assert_eq!(shift_date("2026-11-01", -1).as_deref(), Some("2026-10-31"));
        assert_eq!(shift_date("2024-03-01", -1).as_deref(), Some("2024-02-29"));
        assert_eq!(
            shift_date("2026-11-21", -371).as_deref(),
            Some("2025-11-15")
        );
    }

    #[test]
    fn local_date_uses_the_viewer_offset_not_the_utc_date() {
        assert_eq!(
            local_date("2026-11-21T16:30:00Z", 480).as_deref(),
            Some("2026-11-22")
        );
        assert_eq!(
            local_date("2026-11-21T16:30:00Z", 0).as_deref(),
            Some("2026-11-21")
        );
        assert_eq!(
            local_date("2026-11-22T05:00:00Z", -300).as_deref(),
            Some("2026-11-22")
        );
        assert_eq!(local_date("bad", 0), None);
    }

    #[test]
    fn weekday_matches_javascript_get_day() {
        // 1970-01-01 was a Thursday (4); 2026-11-21 is a Saturday (6).
        assert_eq!(weekday("1970-01-01"), Some(4));
        assert_eq!(weekday("2026-11-21"), Some(6));
    }
}
