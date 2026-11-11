//! `R0-08-native-mcp`: the MCP server list in both entries, the exposed tool
//! rows with real bounds, the add-server dialog and the compact cards.
//!
//! Ported from `internal/acceptance/runner_mcp_native.go`.

use std::time::Duration;

use crate::plans::StepSpec;
use crate::runner::AutomationRunner;

use super::NativeOutcome;
use super::pages::{self, PageSpec};
use super::session::Session;

const SPEC: PageSpec<'static> = PageSpec {
    normal_url: "/mcp",
    normal_marker: "MCP 服务",
    normal_exact: true,
    fixtures: &["filesystem", "postgres-analytics", "19", "216"],
    normal_shot: "native/mcp-normal-1440x900.png",
    normal_compact_shot: "native/mcp-normal-390x844.png",
    sample_url: "/__sample/mcp",
    sample_marker: "filesystem",
    sample_shot: "native/mcp-sample-1440x900.png",
    tag: "mcp",
};

pub(crate) fn run(runner: &AutomationRunner, sha: &str, target: &str) -> NativeOutcome {
    let mut session = Session::new(runner, sha, target, "mcp");
    if session.ensure_native_dir().is_ok() {
        run_case(&mut session);
    }
    if session.out.status.is_empty() {
        session.pass()
    } else {
        session.fail_cleanup()
    }
}

fn run_case(session: &mut Session<'_>) {
    if !session.reset_data_dirs("mcp")
        || !session.prepare_home()
        || !session.require_status()
        || !session.build_client()
        || !session.check_paths()
    {
        return;
    }
    let Some((mut probes, viewports)) = pages::normal_entry(session, &SPEC) else {
        return;
    };
    if !pages::sample_entry(session, &SPEC, &mut probes, &viewports) {
        return;
    }

    // Every exposed tool row must be a real, non-zero rectangle. The rows are
    // plain text nodes (no ref), so the rectangle comes from the `find` reply
    // rather than from `get --property bounds`.
    for tool in ["read_file", "list_directory", "search_files", "write_file"] {
        if session
            .text_node_bounds(&format!("mcp-tool-{tool}"), tool, 15)
            .is_none()
        {
            return;
        }
    }
    if !session.screenshot("native/mcp-filesystem-tools-1440x900.png") {
        return;
    }

    let Some(add) = session.find_ref("mcp-add", Some("button"), "添加服务", 15) else {
        return;
    };
    let click = StepSpec::owned(
        "mcp-open-add",
        "agent-desktop",
        vec!["click".to_string(), add.clone()],
    )
    .timeout(60);
    if !session.run(&[click]) {
        return;
    }
    if !session.wait_role_count("mcp-add-dialog", "window", "添加 MCP 服务", 1, 10)
        || !session.screenshot("native/mcp-add-dialog-1440x900.png")
    {
        return;
    }
    if !session.send_key("mcp-close-add", "escape") {
        return;
    }
    if !session.wait_role_count("mcp-add-escape", "window", "添加 MCP 服务", 0, 20)
        || !session.wait_focused(&add, "mcp-add")
    {
        return;
    }

    // Compact sample: one-column cards.
    if !session.resize(390, 844, true) {
        return;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 4 {
        session.fail("layout probe missing for the compact sample MCP page");
        return;
    }
    probes = next;
    if !session.screenshot("native/mcp-sample-390x844.png") {
        return;
    }
    if !session.close_client("mcp-compact") {
        return;
    }
    let _ = probes;
    if !session.assert_probes(viewports.len() + 4, viewports.len()) {
        return;
    }
    session.collect_app_log();
}
