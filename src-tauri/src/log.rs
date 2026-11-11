//! Host log at `<dataDir>/logs/anvil.log`.
//!
//! The line format is the one the retired Go host wrote
//! (`log.LstdFlags|log.LUTC`: `YYYY/MM/DD HH:MM:SS <message>` in UTC), because
//! the R0-RS03 native runner and the existing acceptance scripts read the
//! `theme-options:`, `i18n-options:`, `window-options:` and `starting:` prefixes
//! from this file. Do not change the prefix or timestamp format without
//! updating the runner.
//!
//! The file is opened with `FILE_SHARE_READ | FILE_SHARE_WRITE |
//! FILE_SHARE_DELETE`, matching Go's `os.OpenFile`. The runner reads the log
//! *while the client is running*; the default `std::fs` sharing mode (read-only
//! sharing) made every external read fail with "file in use" during the R0-RS02
//! native acceptance.

use std::fs::{File, OpenOptions};
use std::io::Write;
use std::os::windows::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

/// Go's `os.OpenFile` share mode: readers and writers may open the same file.
const FILE_SHARE_ALL: u32 = 0x0000_0007;

/// Append-only host log.
pub struct HostLog {
    path: PathBuf,
    file: Mutex<Option<File>>,
}

impl HostLog {
    /// Opens (creating if needed) `<log_dir>/anvil.log` for appending.
    pub fn open(log_dir: &Path) -> std::io::Result<Self> {
        std::fs::create_dir_all(log_dir)?;
        let path = log_dir.join("anvil.log");
        let file = OpenOptions::new()
            .create(true)
            .append(true)
            .share_mode(FILE_SHARE_ALL)
            .open(&path)?;
        Ok(Self {
            path,
            file: Mutex::new(Some(file)),
        })
    }

    /// Path of the log file, for evidence and diagnostics.
    #[must_use]
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Appends one UTC-timestamped line. Log failures are intentionally ignored:
    /// losing a diagnostic line must not break the UI.
    pub fn info(&self, message: &str) {
        let line = format!("{} {message}\n", utc_timestamp(SystemTime::now()));
        if let Ok(mut guard) = self.file.lock()
            && let Some(file) = guard.as_mut()
        {
            let _ = file.write_all(line.as_bytes());
            let _ = file.flush();
        }
    }
}

/// `YYYY/MM/DD HH:MM:SS` in UTC, matching the Go host's log flags.
#[must_use]
pub fn utc_timestamp(now: SystemTime) -> String {
    let seconds = now
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_secs())
        .unwrap_or(0);
    let days = (seconds / 86_400) as i64;
    let remainder = seconds % 86_400;
    let (year, month, day) = civil_from_days(days);
    format!(
        "{year:04}/{month:02}/{day:02} {:02}:{:02}:{:02}",
        remainder / 3600,
        (remainder % 3600) / 60,
        remainder % 60
    )
}

/// Howard Hinnant's `civil_from_days` (proleptic Gregorian, days since 1970-01-01).
fn civil_from_days(days_since_epoch: i64) -> (i64, u32, u32) {
    let z = days_since_epoch + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let day_of_era = z - era * 146_097;
    let year_of_era =
        (day_of_era - day_of_era / 1460 + day_of_era / 36_524 - day_of_era / 146_096) / 365;
    let year = year_of_era + era * 400;
    let day_of_year = day_of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let month_position = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * month_position + 2) / 5 + 1;
    let month = if month_position < 10 {
        month_position + 3
    } else {
        month_position - 9
    };
    let year = if month <= 2 { year + 1 } else { year };
    (year, month as u32, day as u32)
}
