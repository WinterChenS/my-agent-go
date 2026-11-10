package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runNativeAgentsCase opens the normal and sample agents entries, probes all
// manifest viewports, switches a detail tab, and opens/closes a dialog with
// focus return before proving the window is gone.
func runNativeAgentsCase(r automationRunner, sha, target string, hooks *nativeHooks) (out nativeOutcome) {
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
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-agents")
	s.webviewDir = filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-agents")
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
		return s.blocked("reset agents data directory: %v", err)
	}
	if err := resetDirectory(s.webviewDir); err != nil {
		return s.blocked("reset agents WebView data directory: %v", err)
	}
	if !s.run(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + s.dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + s.webviewDir, "--create"}},
	) {
		s.markBlocked()
		return out
	}

	probeCount := 0

	// Normal entry: empty list/detail, no fixture agents.
	s.initialURL = "/agents"
	if !s.launchThemeClient(1440, 900, "launch-agents-normal") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.waitText("agents-normal-title", "Agent 管理", false, 10) {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(false) {
		return s.failCleanup()
	}
	for _, fixture := range []string{"迁移工程师", "数据分析师", "claude-sonnet-4.5"} {
		count, ok := s.textCount(fixture, "agents-normal-fixture-"+fixture)
		if !ok {
			return s.failCleanup()
		}
		if count != 0 {
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, "normal agents rendered sample fixture "+fixture)
			return s.failCleanup()
		}
	}
	if !s.screenshot("native/agents-normal-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < 1 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the normal agents page")
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
	if !s.screenshot("native/agents-normal-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	// Sample entry: populated list/detail, tab and dialog operations.
	s.initialURL = "/__sample/agents"
	if !s.launchThemeClient(1440, 900, "launch-agents-sample") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(true) {
		return s.failCleanup()
	}
	if !s.waitText("agents-sample-title", "迁移工程师", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/agents-sample-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+3 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the sample agents page")
		return s.failCleanup()
	}

	runsTab, ok := s.findRef("agents-runs-tab", "", "运行记录", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "agents-click-runs-tab", Command: "agent-desktop", Args: []string{"click", runsTab}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitText("agents-runs-pane", "API 迁移评估：v1 → v2", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/agents-sample-runs-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}

	newAgent, ok := s.findRef("agents-new", "button", "新建 Agent", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "agents-open-new", Command: "agent-desktop", Args: []string{"click", newAgent}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("agents-new-dialog", "window", "新建 Agent", 1, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/agents-new-dialog-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "agents-close-new", Command: "self", Args: []string{"key", "--title=Anvil", "--combo=escape"}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("agents-new-dialog-escape", "window", "新建 Agent", 0, 10) {
		return s.failCleanup()
	}
	if !s.waitFocused(newAgent, "agents-new") {
		return s.failCleanup()
	}

	// Compact sample: detail view then back to the list.
	if !s.resize(s.windowIDValue, 390, 844, true) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+4 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the compact sample agents page")
		return s.failCleanup()
	}
	basicTab, ok := s.findRef("agents-basic-tab", "", "基础配置", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "agents-compact-basic-tab", Command: "agent-desktop", Args: []string{"click", basicTab}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitText("agents-compact-basic", "Agent 运行时", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/agents-sample-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	back, ok := s.findRef("agents-back", "button", "返回 Agent 列表", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "agents-back-to-list", Command: "agent-desktop", Args: []string{"click", back}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitText("agents-compact-list", "主 Agent · 4", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/agents-list-390x844.png", s.windowIDValue) {
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
		out.Limitations = append(out.Limitations, "application log was not produced; agents evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}
