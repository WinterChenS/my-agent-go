package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runNativeMcpCase opens normal/sample MCP, probes all manifest viewports,
// scrolls a tool list, opens/closes the add-server dialog, and proves the
// compact card layout before closing.
func runNativeMcpCase(r automationRunner, sha, target string, hooks *nativeHooks) (out nativeOutcome) {
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
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-mcp")
	s.webviewDir = filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-mcp")
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
		return s.blocked("reset MCP data directory: %v", err)
	}
	if err := resetDirectory(s.webviewDir); err != nil {
		return s.blocked("reset MCP WebView data directory: %v", err)
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
	s.initialURL = "/mcp"
	if !s.launchThemeClient(1440, 900, "launch-mcp-normal") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.waitText("mcp-normal-title", "MCP 服务", true, 10) {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(false) {
		return s.failCleanup()
	}
	for _, fixture := range []string{"filesystem", "postgres-analytics", "19", "216"} {
		count, ok := s.textCount(fixture, "mcp-normal-fixture-"+fixture)
		if !ok {
			return s.failCleanup()
		}
		if count != 0 {
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, "normal MCP rendered sample fixture "+fixture)
			return s.failCleanup()
		}
	}
	if !s.screenshot("native/mcp-normal-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < 1 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the normal MCP page")
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
	if !s.screenshot("native/mcp-normal-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	// Sample entry.
	s.initialURL = "/__sample/mcp"
	if !s.launchThemeClient(1440, 900, "launch-mcp-sample") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(true) {
		return s.failCleanup()
	}
	if !s.waitText("mcp-sample-title", "filesystem", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/mcp-sample-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+3 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the sample MCP page")
		return s.failCleanup()
	}

	tool, ok := s.findRef("mcp-tool-anchor", "button", "filesystem 工具清单", 15)
	if !ok {
		return s.failCleanup()
	}
	bounds, ok := s.elementBounds("mcp-tools-bounds", tool)
	if !ok {
		return s.failCleanup()
	}
	x := int(bounds.X + bounds.Width/2)
	y := int(bounds.Y + bounds.Height/2)
	if !s.run(StepSpec{Name: "mcp-scroll-tools", Command: "agent-desktop", Args: []string{"--headed", "mouse-wheel", "--x", fmt.Sprint(x), "--y", fmt.Sprint(y), "--dy", "-3"}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.screenshot("native/mcp-tools-scrolled-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}

	add, ok := s.findRef("mcp-add", "button", "添加服务", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "mcp-open-add", Command: "agent-desktop", Args: []string{"click", add}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("mcp-add-dialog", "window", "添加 MCP 服务", 1, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/mcp-add-dialog-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "mcp-close-add", Command: "self", Args: []string{"key", "--title=Anvil", "--combo=escape"}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("mcp-add-escape", "window", "添加 MCP 服务", 0, 10) {
		return s.failCleanup()
	}
	if !s.waitFocused(add, "mcp-add") {
		return s.failCleanup()
	}

	// Compact sample: one-column cards.
	if !s.resize(s.windowIDValue, 390, 844, true) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+4 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the compact sample MCP page")
		return s.failCleanup()
	}
	if !s.screenshot("native/mcp-sample-390x844.png", s.windowIDValue) {
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
		out.Limitations = append(out.Limitations, "application log was not produced; MCP evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}

func (s *shellNative) findTextRef(logID, text string, attempts int) (string, bool) {
	for attempt := 1; attempt <= attempts; attempt++ {
		stepName := fmt.Sprintf("find-text-%s-%02d", logID, attempt)
		if !s.run(StepSpec{Name: stepName, Command: "agent-desktop", Args: []string{"find", "--text", text, "--first", "--app", "anvil.exe", "--timeout-ms", "15000"}, Timeout: 60 * time.Second}) {
			return "", false
		}
		body, _ := s.exec.read(stepName)
		if ref, ok := parseFindRef(body); ok {
			return ref, true
		}
		time.Sleep(400 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("could not find text %q in the accessibility tree after %d attempts", text, attempts))
	return "", false
}
