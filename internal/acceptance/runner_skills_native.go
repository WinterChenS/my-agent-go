package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runNativeSkillsCase opens normal/sample Skills, probes all manifest viewports,
// expands a workflow step, opens/closes the import dialog, and proves the
// compact list/detail switch before closing the window.
func runNativeSkillsCase(r automationRunner, sha, target string, hooks *nativeHooks) (out nativeOutcome) {
	var executor desktopStepRunner = r
	if hooks != nil && hooks.desktop != nil {
		executor = hooks.desktop
	}
	s := &shellNative{runner: r, exec: executor, out: &out}
	nativeDir := filepath.Join(r.caseDir, "native")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		return s.blocked("create native evidence directory: %v", err)
	}
	shortSHA := sha
	if len(shortSHA) > 12 {
		shortSHA = shortSHA[:12]
	}
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-skills")
	s.webviewDir = filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-skills")
	s.appLog = filepath.Join(s.dataDir, "logs", "anvil.log")

	home := agentDesktopHome(r.repo)
	var (
		prepCommands  []Command
		prepArtifacts []Artifact
		prepFailure   string
	)
	if hooks != nil && hooks.prepareHome != nil {
		prepCommands, prepArtifacts, prepFailure = hooks.prepareHome(executor, r.repo, home)
	} else {
		prepCommands, prepArtifacts, prepFailure = prepareAgentDesktopHome(executor, r.repo, home)
	}
	out.Commands = append(out.Commands, prepCommands...)
	out.Artifacts = append(out.Artifacts, prepArtifacts...)
	if prepFailure != "" {
		return s.blocked("%s", prepFailure)
	}

	if !s.run(StepSpec{Name: "agent-desktop-status", Command: "agent-desktop", Args: []string{"status"}, Timeout: 60 * time.Second}) {
		s.markBlocked()
		return out
	}
	statusBody, _ := executor.read("agent-desktop-status")
	if err := checkAgentDesktopStatus(statusBody); err != nil {
		return s.blocked("%v", err)
	}
	if !s.run(
		StepSpec{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
		StepSpec{Name: "go-build", Command: "go", Args: []string{"build", "-o", filepath.Join(r.repo, ".tmp", "bin", "anvil.exe"), "./cmd/anvil"}, Timeout: 20 * time.Minute},
	) {
		return out
	}
	if err := resetDirectory(s.dataDir); err != nil {
		return s.blocked("reset skills data directory: %v", err)
	}
	if err := resetDirectory(s.webviewDir); err != nil {
		return s.blocked("reset skills WebView data directory: %v", err)
	}
	if !s.run(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + s.dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + s.webviewDir, "--create"}},
	) {
		s.markBlocked()
		return out
	}

	probeCount := 0

	// Normal entry.
	s.initialURL = "/skills"
	if !s.launchThemeClient(1440, 900, "launch-skills-normal") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.waitText("skills-normal-title", "Skill", true, 10) {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(false) {
		return s.failCleanup()
	}
	for _, fixture := range []string{"代码审查", "v2.3", "97%"} {
		count, ok := s.textCount(fixture, "skills-normal-fixture-"+fixture)
		if !ok {
			return s.failCleanup()
		}
		if count != 0 {
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, "normal skills rendered sample fixture "+fixture)
			return s.failCleanup()
		}
	}
	if !s.screenshot("native/skills-normal-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < 1 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the normal skills page")
		return s.failCleanup()
	}
	viewports, err := readManifestViewports(r.repo)
	if err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, err.Error())
		return s.failCleanup()
	}
	for _, viewport := range viewports {
		if !s.resize(s.windowIDValue, viewport.Width, viewport.Height, false) {
			return s.failCleanup()
		}
		next := s.waitProbe(probeCount, 10*time.Second)
		if next <= probeCount {
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, fmt.Sprintf("layout probe missing at %dx%d", viewport.Width, viewport.Height))
			return s.failCleanup()
		}
		probeCount = next
	}
	if !s.resize(s.windowIDValue, 390, 844, true) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+2 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing at 390x844")
		return s.failCleanup()
	}
	if !s.screenshot("native/skills-normal-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	// Sample entry.
	s.initialURL = "/__sample/skills"
	if !s.launchThemeClient(1440, 900, "launch-skills-sample") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(true) {
		return s.failCleanup()
	}
	if !s.waitText("skills-sample-title", "代码审查", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/skills-sample-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+3 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the sample skills page")
		return s.failCleanup()
	}

	step, ok := s.findRef("skills-workflow-step", "", "展开或收起步骤", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "skills-expand-step", Command: "agent-desktop", Args: []string{"expand", step}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitExpanded(step, true, "skills-step") {
		return s.failCleanup()
	}
	if !s.screenshot("native/skills-workflow-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}

	importButton, ok := s.findRef("skills-import", "button", "导入 Skill", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "skills-open-import", Command: "agent-desktop", Args: []string{"click", importButton}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("skills-import-dialog", "window", "导入 Skill", 1, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/skills-import-dialog-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "skills-close-import", Command: "self", Args: []string{"key", "--title=Anvil", "--combo=escape"}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("skills-import-escape", "window", "导入 Skill", 0, 10) {
		return s.failCleanup()
	}
	if !s.waitFocused(importButton, "skills-import") {
		return s.failCleanup()
	}

	// Compact sample: detail then back to list.
	if !s.resize(s.windowIDValue, 390, 844, true) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+4 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the compact sample skills page")
		return s.failCleanup()
	}
	if !s.screenshot("native/skills-sample-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	back, ok := s.findRef("skills-back", "button", "返回 Skill 列表", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "skills-back-to-list", Command: "agent-desktop", Args: []string{"click", back}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitText("skills-compact-list", "已安装 9", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/skills-list-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	appLog, err := os.ReadFile(s.appLog)
	if err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("read application log: %v", err))
		return out
	}
	probes := parseLayoutProbes(string(appLog))
	if len(probes) < len(viewports)+4 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("only %d layout probes recorded", len(probes)))
		return out
	}
	unique := map[string]bool{}
	for _, probe := range probes {
		unique[probe.Viewport] = true
		if probe.Overflow {
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, fmt.Sprintf("horizontal overflow at viewport %s", probe.Viewport))
			return out
		}
	}
	if len(unique) < len(viewports) {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("only %d distinct viewports probed", len(unique)))
		return out
	}
	if artifact, ok := copyAppLog(s.runner, s.dataDir); ok {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "application log was not produced; skills evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}
