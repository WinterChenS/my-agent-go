//! Print one catalog scan as JSON.
//!
//! ```text
//! cargo run -p anvil-catalog --example catalog_report -- --data-dir <dir> [--home <dir>]
//! ```
//!
//! Used by the R1 acceptance cases: it runs the same service the app runs, with
//! the real adapters, and writes nothing outside `--data-dir`.

use std::path::PathBuf;
use std::process::ExitCode;

use anvil_catalog::runtime::adapters::common::home;
use anvil_catalog::{CatalogService, Store};

fn main() -> ExitCode {
    let mut data_dir: Option<PathBuf> = None;
    let mut home_dir: Option<PathBuf> = None;
    let mut args = std::env::args().skip(1);
    while let Some(argument) = args.next() {
        match argument.as_str() {
            "--data-dir" => data_dir = args.next().map(PathBuf::from),
            "--home" => home_dir = args.next().map(PathBuf::from),
            "--help" | "-h" => {
                println!("catalog_report --data-dir <dir> [--home <dir>]");
                return ExitCode::SUCCESS;
            }
            other => {
                eprintln!("catalog_report: unknown argument {other:?}");
                return ExitCode::from(2);
            }
        }
    }
    let Some(data_dir) = data_dir else {
        eprintln!("catalog_report: --data-dir is required");
        return ExitCode::from(2);
    };

    let home_dir = home_dir.unwrap_or_else(home);
    let database = data_dir.join("catalog.sqlite");
    let store = match Store::open(&database) {
        Ok(store) => store,
        Err(error) => {
            eprintln!("catalog_report: open {}: {error}", database.display());
            return ExitCode::from(2);
        }
    };
    let mut service = match CatalogService::new(store, home_dir.clone()) {
        Ok(service) => service,
        Err(error) => {
            eprintln!("catalog_report: registry: {error}");
            return ExitCode::from(2);
        }
    };

    let scan = match service.scan() {
        Ok(scan) => scan,
        Err(error) => {
            eprintln!("catalog_report: scan: {error}");
            return ExitCode::from(2);
        }
    };
    for runtime_id in service.registry().ids() {
        if let Err(error) = service.sync_runtime(&runtime_id) {
            eprintln!("catalog_report: sync {runtime_id}: {error}");
        }
    }
    match service.snapshot() {
        Ok(snapshot) => {
            let report = serde_json::json!({
                "home": home_dir.to_string_lossy(),
                "database": database.to_string_lossy(),
                "scan": {
                    "registrations": scan.registrations,
                    "scannedAt": scan.scanned_at,
                    "failures": scan.failures,
                },
                "snapshot": snapshot,
            });
            println!(
                "{}",
                serde_json::to_string_pretty(&report).unwrap_or_default()
            );
            ExitCode::SUCCESS
        }
        Err(error) => {
            eprintln!("catalog_report: snapshot: {error}");
            ExitCode::from(2)
        }
    }
}
