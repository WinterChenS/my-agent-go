package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runNativeWorkbenchCase opens the normal and isolated sample workbench entries
// at the wide and compact viewports, checks the real layout probe, exercises the
// global search dialog with Escape, and closes the window.
func runNativeWorkbenchCase(r automationRunner, sha, target string, hooks *nativeHooks) (out nativeOutcome) {
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
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-workbench")
	s.webviewDir = filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-workbench")
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
		return s.blocked("reset workbench data directory: %v", err)
	}
	if err := resetDirectory(s.webviewDir); err != nil {
		return s.blocked("reset workbench WebView data directory: %v", err)
	}
	if !s.run(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + s.dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + s.webviewDir, "--create"}},
	) {
		s.markBlocked()
		return out
	}

	// Normal entry at the wide viewport.
	s.initialURL = ""
	if !s.launchThemeClient(1440, 900, "launch-workbench-normal") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(false) {
		return s.failCleanup()
	}
	if count, ok := s.textCount("1,489", "normal-fixture-check"); !ok || count != 0 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "normal workbench rendered sample fixture 1,489")
		return s.failCleanup()
	}
	if !s.screenshot("native/workbench-normal-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	probeCount := s.waitProbe(0, 10*time.Second)
	if probeCount < 1 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the normal workbench")
		return s.failCleanup()
	}

	// Search dialog: real click, Escape key, focus restored by the app.
	ref, ok := s.findRef("workbench-search", "button", "搜索", 10)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "open-global-search", Command: "agent-desktop", Args: []string{"click", ref}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitText("global-search-open", "快速跳转", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/workbench-search-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "close-global-search", Command: "self", Args: []string{"key", "--title=Anvil", "--combo=escape"}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitTextGone("global-search-escape", "快速跳转", 10) {
		return s.failCleanup()
	}
	if !s.waitFocused(ref, "workbench-search") {
		return s.failCleanup()
	}

	// Responsive spot checks plus the compact reference screenshot.
	if !s.resize(s.windowIDValue, 1366, 768, true) {
		return s.failCleanup()
	}
	if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
		probeCount = next
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing at 1366x768")
		return s.failCleanup()
	}
	if !s.resize(s.windowIDValue, 820, 1180, false) {
		return s.failCleanup()
	}
	if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
		probeCount = next
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing at 820x1180")
		return s.failCleanup()
	}
	if !s.resize(s.windowIDValue, 390, 844, true) {
		return s.failCleanup()
	}
	if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
		probeCount = next
	}
	if !s.screenshot("native/workbench-normal-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	// Isolated sample entry at both reference viewports.
	s.initialURL = "/__sample/workbench"
	if !s.launchThemeClient(1440, 900, "launch-workbench-sample") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(true) {
		return s.failCleanup()
	}
	if !s.waitText("sample-fixtures", "1,489", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/workbench-sample-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
		probeCount = next
	}
	if !s.resize(s.windowIDValue, 390, 844, true) {
		return s.failCleanup()
	}
	if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
		probeCount = next
	}
	if !s.screenshot("native/workbench-sample-390x844.png", s.windowIDValue) {
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
	if len(probes) < 6 {
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
	if len(unique) < 3 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("only %d distinct viewports probed", len(unique)))
		return out
	}
	if artifact, ok := copyAppLog(s.runner, s.dataDir); ok {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "application log was not produced; workbench evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}

func (s *shellNative) assertSampleBanner(expected bool) bool {
	count, ok := s.textCount("原型样例数据", "sample-banner-check")
	if !ok {
		return false
	}
	if expected && count == 0 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "sample banner was not rendered")
		return false
	}
	if !expected && count != 0 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "sample banner appeared in the normal entry")
		return false
	}
	return true
}

func (s *shellNative) textCount(text, logID string) (int, bool) {
	stepName := "text-count-" + logID
	if !s.run(StepSpec{
		Name:    stepName,
		Command: "agent-desktop",
		Args:    []string{"find", "--text", text, "--app", "anvil.exe", "--timeout-ms", "15000"},
		Timeout: 60 * time.Second,
	}) {
		return 0, false
	}
	body, _ := s.exec.read(stepName)
	return parseFindCount(body), true
}

func (s *shellNative) waitTextGone(logID, text string, attempts int) bool {
	for attempt := 1; attempt <= attempts; attempt++ {
		count, ok := s.textCount(text, logID+"-"+fmt.Sprint(attempt))
		if !ok {
			return false
		}
		if count == 0 {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("text %q did not disappear after Escape", text))
	return false
}
