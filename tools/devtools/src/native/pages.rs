//! Shared normal/sample/compact flow of the per-page native cases.
//!
//! Every page case follows the same shape: the normal entry must render the
//! real empty state (no sample fixtures), the isolated `/__sample/…` entry must
//! render the fixtures behind the sample banner, and the compact viewport must
//! stay free of horizontal overflow. Only the interactions in between differ,
//! so those stay in each case module.

use std::time::Duration;

use super::parse::ManifestViewport;
use super::session::Session;

/// What a page case must assert about its two entries.
pub(crate) struct PageSpec<'a> {
    /// Initial URL of the normal entry (`""` for the workbench root).
    pub normal_url: &'a str,
    /// Text that proves the normal page rendered.
    pub normal_marker: &'a str,
    /// Whether `normal_marker` must match a node name exactly.
    pub normal_exact: bool,
    /// Sample-only strings that must be absent from the normal entry.
    pub fixtures: &'a [&'a str],
    pub normal_shot: &'a str,
    pub normal_compact_shot: &'a str,
    pub sample_url: &'a str,
    pub sample_marker: &'a str,
    pub sample_shot: &'a str,
    /// Step-name prefix (`chat`, `agents`, …).
    pub tag: &'a str,
}

/// Open the normal entry at 1440×900, walk every manifest viewport, capture the
/// compact reference and close. Returns the probe count so far.
pub(crate) fn normal_entry(
    session: &mut Session<'_>,
    spec: &PageSpec<'_>,
) -> Option<(usize, Vec<ManifestViewport>)> {
    session.initial_url = spec.normal_url.to_string();
    let label = format!("launch-{}-normal", spec.tag);
    if !session.launch_client(1440, 900, &label) || !session.activate() {
        return None;
    }
    let title_log = format!("{}-normal-title", spec.tag);
    if !session.wait_text(&title_log, spec.normal_marker, spec.normal_exact, 10) {
        return None;
    }
    if !session.assert_sample_banner(false) {
        return None;
    }
    for fixture in spec.fixtures {
        let log_id = format!("{}-normal-fixture-{fixture}", spec.tag);
        let count = session.text_count(fixture, &log_id)?;
        if count != 0 {
            session.fail(&format!(
                "normal {} page rendered sample fixture {fixture}",
                spec.tag
            ));
            return None;
        }
    }
    if !session.screenshot(spec.normal_shot) {
        return None;
    }
    let mut probes = session.wait_probe(0, Duration::from_secs(10));
    if probes < 1 {
        session.fail(&format!(
            "layout probe missing for the normal {} page",
            spec.tag
        ));
        return None;
    }
    let viewports = match super::parse::read_manifest_viewports(&session.repo()) {
        Ok(viewports) => viewports,
        Err(err) => {
            session.fail(&err);
            return None;
        }
    };
    if !session.probe_manifest_viewports(&viewports, &mut probes) {
        return None;
    }
    if !session.resize(390, 844, true) {
        return None;
    }
    let next = session.wait_probe(probes, Duration::from_secs(10));
    if next < viewports.len() + 2 {
        session.fail(&format!(
            "layout probe missing at 390x844 on the {} page",
            spec.tag
        ));
        return None;
    }
    probes = next;
    if !session.screenshot(spec.normal_compact_shot) {
        return None;
    }
    if !session.close_client(&format!("{}-normal", spec.tag)) {
        return None;
    }
    Some((probes, viewports))
}

/// Open the isolated sample entry at 1440×900 and require the fixtures.
pub(crate) fn sample_entry(
    session: &mut Session<'_>,
    spec: &PageSpec<'_>,
    probes: &mut usize,
    viewports: &[ManifestViewport],
) -> bool {
    session.initial_url = spec.sample_url.to_string();
    let label = format!("launch-{}-sample", spec.tag);
    if !session.launch_client(1440, 900, &label) || !session.activate() {
        return false;
    }
    if !session.assert_sample_banner(true) {
        return false;
    }
    let marker_log = format!("{}-sample-title", spec.tag);
    if !session.wait_text(&marker_log, spec.sample_marker, false, 10) {
        return false;
    }
    if !session.screenshot(spec.sample_shot) {
        return false;
    }
    let next = session.wait_probe(*probes, Duration::from_secs(10));
    if next < viewports.len() + 3 {
        session.fail(&format!(
            "layout probe missing for the sample {} page",
            spec.tag
        ));
        return false;
    }
    *probes = next;
    true
}
