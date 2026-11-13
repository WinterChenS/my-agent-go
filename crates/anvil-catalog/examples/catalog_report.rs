//! Scan the machine, optionally run one catalog action, and check expectations.
//!
//! ```text
//! catalog_report --data-dir <dir> [--home <dir>] [--no-sync]
//!                [--action scan|add-model|verify|set-default|probe-mcp]
//!                [--runtime pi] [--model id] [--display name]
//!                [--protocol openai] [--endpoint url] [--credential-env VAR]
//!                [--name server]
//!                [--expect runtime:pi:status=available] ...
//! ```
//!
//! The R1 acceptance cases run this binary: it drives the same service the app
//! uses, writes only under `--data-dir`, and turns a wrong fact into exit 1.
//! `--expect` exists so the case plan stays declarative and the evidence file
//! records exactly which facts were required.

use std::path::PathBuf;
use std::process::ExitCode;

use anvil_catalog::runtime::adapters::common::home;
use anvil_catalog::{CatalogService, Store};

struct Options {
    data_dir: PathBuf,
    home: PathBuf,
    sync: bool,
    action: String,
    runtime: Option<String>,
    model: Option<String>,
    display: Option<String>,
    protocol: Option<String>,
    endpoint: Option<String>,
    credential_env: Option<String>,
    name: Option<String>,
    expect: Vec<String>,
}

impl Options {
    fn require(&self, flag: &str) -> Result<String, String> {
        let value = match flag {
            "--runtime" => self.runtime.clone(),
            "--model" => self.model.clone(),
            "--name" => self.name.clone(),
            other => return Err(format!("{other} is not a value flag")),
        };
        value.ok_or_else(|| format!("catalog_report: {flag} is required for --action"))
    }
}

fn parse() -> Result<Options, String> {
    let mut data_dir = None;
    let mut home_dir = None;
    let mut sync = true;
    let mut action = "scan".to_string();
    let mut runtime = None;
    let mut model = None;
    let mut display = None;
    let mut protocol = None;
    let mut endpoint = None;
    let mut credential_env = None;
    let mut name = None;
    let mut expect = Vec::new();

    let mut args = std::env::args().skip(1);
    while let Some(argument) = args.next() {
        let mut value = || {
            args.next()
                .ok_or_else(|| format!("catalog_report: {argument} needs a value"))
        };
        match argument.as_str() {
            "--data-dir" => data_dir = Some(PathBuf::from(value()?)),
            "--home" => home_dir = Some(PathBuf::from(value()?)),
            "--no-sync" => sync = false,
            "--action" => action = value()?,
            "--runtime" => runtime = Some(value()?),
            "--model" => model = Some(value()?),
            "--display" => display = Some(value()?),
            "--protocol" => protocol = Some(value()?),
            "--endpoint" => endpoint = Some(value()?),
            "--credential-env" => credential_env = Some(value()?),
            "--name" => name = Some(value()?),
            "--expect" => expect.push(value()?),
            "--help" | "-h" => {
                println!("catalog_report --data-dir <dir> [--action …] [--expect …]");
                std::process::exit(0);
            }
            other => return Err(format!("catalog_report: unknown argument {other:?}")),
        }
    }
    Ok(Options {
        data_dir: data_dir.ok_or("catalog_report: --data-dir is required")?,
        home: home_dir.unwrap_or_else(home),
        sync,
        action,
        runtime,
        model,
        display,
        protocol,
        endpoint,
        credential_env,
        name,
        expect,
    })
}

fn main() -> ExitCode {
    let options = match parse() {
        Ok(options) => options,
        Err(error) => {
            eprintln!("{error}");
            return ExitCode::from(2);
        }
    };
    match run(&options) {
        Ok((report, failures)) => {
            // The report is printed even when an expectation failed, so the
            // evidence file shows what the machine actually looked like.
            println!(
                "{}",
                serde_json::to_string_pretty(&report).unwrap_or_default()
            );
            if failures.is_empty() {
                ExitCode::SUCCESS
            } else {
                for failure in &failures {
                    eprintln!("expectation failed: {failure}");
                }
                ExitCode::from(1)
            }
        }
        Err(error) => {
            eprintln!("catalog_report: {error}");
            ExitCode::from(2)
        }
    }
}

fn run(options: &Options) -> Result<(serde_json::Value, Vec<String>), String> {
    let database = options.data_dir.join("catalog.sqlite");
    let store = Store::open(&database).map_err(|error| format!("open database: {error}"))?;
    let mut service = CatalogService::new(store, options.home.clone())
        .map_err(|error| format!("registry: {error}"))?;

    let scan = service.scan().map_err(|error| error.to_wire())?;
    if options.sync {
        for runtime_id in service.registry().ids() {
            service
                .sync_runtime(&runtime_id)
                .map_err(|error| error.to_wire())?;
        }
    }

    let mut action_result = serde_json::Value::Null;
    match options.action.as_str() {
        "scan" => {}
        "add-model" => {
            let runtime = options.require("--runtime")?;
            let model = options.require("--model")?;
            let credential = match &options.credential_env {
                Some(variable) => Some(
                    std::env::var(variable)
                        .map_err(|_| format!("credential env {variable} is not set"))?,
                ),
                None => None,
            };
            service
                .add_custom_model(
                    &runtime,
                    &model,
                    options.display.as_deref().unwrap_or(&model),
                    options.protocol.as_deref(),
                    options.endpoint.as_deref(),
                    credential.as_deref(),
                )
                .map_err(|error| error.to_wire())?;
            action_result = serde_json::json!({ "added": model, "runtime": runtime });
        }
        "verify" => {
            let runtime = options.require("--runtime")?;
            let model = options.require("--model")?;
            let verification = service
                .verify_model(&runtime, &model)
                .map_err(|error| error.to_wire())?;
            action_result = serde_json::json!({
                "ok": verification.ok,
                "detail": verification.detail,
            });
        }
        "set-default" => {
            let runtime = options.require("--runtime")?;
            let model = options.require("--model")?;
            service
                .set_default_model(&runtime, &model)
                .map_err(|error| error.to_wire())?;
            action_result = serde_json::json!({ "default": model, "runtime": runtime });
        }
        "probe-mcp" => {
            let runtime = options.require("--runtime")?;
            let name = options.require("--name")?;
            let probe = service
                .probe_mcp(&runtime, &name)
                .map_err(|error| error.to_wire())?;
            action_result = serde_json::json!({
                "ok": probe.ok,
                "tools": probe.tools,
                "detail": probe.detail,
            });
        }
        other => return Err(format!("unknown --action {other:?}")),
    }

    let snapshot = service.snapshot().map_err(|error| error.to_wire())?;
    let report = serde_json::json!({
        "home": options.home.to_string_lossy(),
        "database": database.to_string_lossy(),
        "scan": {
            "registrations": scan.registrations,
            "scannedAt": scan.scanned_at,
            "failures": scan.failures,
        },
        "action": action_result,
        "snapshot": snapshot,
    });

    let failures = check_expectations(&options.expect, &report);
    Ok((report, failures))
}

/// Evaluate `selector=value` expectations against the report.
///
/// Selectors:
/// * `runtime-count`, `models-count`, `skills-count`, `mcp-count`
/// * `skills-valid-min`, `skills-flagged-min`
/// * `runtime:<id>:status|version|rows`
/// * `runtime:<id>:capability:<name>`
/// * `runtime:<id>:models-total|skills-total|mcp-total`
fn check_expectations(expectations: &[String], report: &serde_json::Value) -> Vec<String> {
    let mut failures = Vec::new();
    for expectation in expectations {
        let Some((selector, want)) = expectation.split_once('=') else {
            failures.push(format!("{expectation:?} is not selector=value"));
            continue;
        };
        let actual = resolve(selector, report);
        // `*-min` selectors mean "at least", everything else is exact.
        if selector.ends_with("-min") {
            let want_number: u64 = match want.parse() {
                Ok(number) => number,
                Err(_) => {
                    failures.push(format!("{selector} needs a numeric bound, not {want:?}"));
                    continue;
                }
            };
            match actual.and_then(|value| value.parse::<u64>().ok()) {
                Some(value) if value >= want_number => {}
                Some(value) => {
                    failures.push(format!("{selector} = {value}, want at least {want_number}"))
                }
                None => failures.push(format!("{selector} is missing")),
            }
            continue;
        }
        match actual {
            Some(value) if value == want => {}
            Some(value) => failures.push(format!("{selector} = {value:?}, want {want:?}")),
            None => failures.push(format!("{selector} is missing (want {want:?})")),
        }
    }
    failures
}

fn resolve(selector: &str, report: &serde_json::Value) -> Option<String> {
    let snapshot = &report["snapshot"];
    let runtimes = snapshot["runtimes"].as_array().cloned().unwrap_or_default();
    let models = snapshot["models"].as_array().cloned().unwrap_or_default();
    let skills = snapshot["skills"].as_array().cloned().unwrap_or_default();
    let mcp = snapshot["mcp"].as_array().cloned().unwrap_or_default();

    match selector {
        "runtime-count" => return Some(runtimes.len().to_string()),
        "models-count" => return Some(models.len().to_string()),
        "skills-count" => return Some(skills.len().to_string()),
        "mcp-count" => return Some(mcp.len().to_string()),
        "skills-valid-min" => return Some(count_where(&skills, "state", "valid").to_string()),
        "skills-flagged-min" => {
            let flagged = skills
                .iter()
                .filter(|row| row["state"].as_str() != Some("valid"))
                .count();
            return Some(flagged.to_string());
        }
        _ => {}
    }

    let parts: Vec<&str> = selector.split(':').collect();
    if parts.first() == Some(&"action") && parts.len() >= 3 {
        let action = &report["action"];
        return match parts[1] {
            "ok" => action["ok"].as_bool().map(|ok| ok.to_string()),
            "detail-contains" => action["detail"]
                .as_str()
                .map(|detail| detail.contains(parts[2]).to_string()),
            "tools-count" => action["tools"]
                .as_array()
                .map(|tools| tools.len().to_string()),
            _ => None,
        };
    }
    if parts.first() != Some(&"runtime") || parts.len() < 3 {
        return None;
    }
    let runtime_id = parts[1];
    let rows: Vec<&serde_json::Value> = runtimes
        .iter()
        .filter(|row| row["runtimeId"].as_str() == Some(runtime_id))
        .collect();
    let counts = |items: &[serde_json::Value]| {
        items
            .iter()
            .filter(|row| row["runtimeId"].as_str() == Some(runtime_id))
            .count()
            .to_string()
    };
    match parts[2] {
        "rows" => Some(rows.len().to_string()),
        "status" => rows
            .first()
            .map(|row| row["status"].as_str().unwrap_or("").to_string()),
        "version" => rows
            .first()
            .map(|row| row["version"].as_str().unwrap_or("").to_string()),
        "capability" => {
            let name = parts.get(3)?;
            rows.first()
                .and_then(|row| row["capabilities"].get(*name))
                .and_then(|value| value.as_str())
                .map(str::to_string)
        }
        "models-total" => Some(counts(&models)),
        "skills-total" => Some(counts(&skills)),
        "mcp-total" => Some(counts(&mcp)),
        _ => None,
    }
}

fn count_where(items: &[serde_json::Value], field: &str, want: &str) -> usize {
    items
        .iter()
        .filter(|item| item[field].as_str() == Some(want))
        .count()
}
