package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runNativeChatCase opens the normal and isolated sample chat entries at the
// wide and compact viewports, exercises the rail tabs and drawers, a dialog
// with Escape/focus return, and reads the real layout probe from the app log.
func runNativeChatCase(r automationRunner, sha, target string, hooks *nativeHooks) (out nativeOutcome) {
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
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-chat")
	s.webviewDir = filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-chat")
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
		return s.blocked("reset chat data directory: %v", err)
	}
	if err := resetDirectory(s.webviewDir); err != nil {
		return s.blocked("reset chat WebView data directory: %v", err)
	}
	if !s.run(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + s.dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + s.webviewDir, "--create"}},
	) {
		s.markBlocked()
		return out
	}

	probeCount := 0

	// Normal entry: empty state, disabled composer, no fixture conversation.
	s.initialURL = "/chat"
	if !s.launchThemeClient(1440, 900, "launch-chat-normal") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.waitText("chat-normal-title", "尚未接入会话", false, 10) {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(false) {
		return s.failCleanup()
	}
	for _, fixture := range []string{"API 迁移评估", "迁移工程师", "陈羽", "claude-sonnet-4.5"} {
		if count, ok := s.textCount(fixture, "chat-normal-fixture-"+fixture); !ok || count != 0 {
			if !ok {
				return s.failCleanup()
			}
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, "normal chat rendered sample fixture "+fixture)
			return s.failCleanup()
		}
	}
	if !s.screenshot("native/chat-normal-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < 1 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the normal chat")
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
	if !s.screenshot("native/chat-normal-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	// Sample entry: fixture conversation, rail operations and dialogs.
	s.initialURL = "/__sample/chat"
	if !s.launchThemeClient(1440, 900, "launch-chat-sample") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.assertSampleBanner(true) {
		return s.failCleanup()
	}
	if !s.waitText("chat-sample-title", "API 迁移评估", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/chat-sample-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+3 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the sample chat")
		return s.failCleanup()
	}

	artifactsTab, ok := s.findRef("chat-artifacts-tab", "", "输出物 3", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "chat-click-artifacts-tab", Command: "agent-desktop", Args: []string{"click", artifactsTab}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitText("chat-artifacts-pane", "本次会话产出 · 3", false, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/chat-sample-rail-artifacts-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}

	more, ok := s.findRef("chat-more", "button", "更多操作", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "chat-open-thread-menu", Command: "agent-desktop", Args: []string{"click", more}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("chat-thread-menu", "window", "会话操作", 1, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/chat-thread-menu-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "chat-close-thread-menu", Command: "self", Args: []string{"key", "--title=Anvil", "--combo=escape"}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("chat-thread-menu-escape", "window", "会话操作", 0, 10) {
		return s.failCleanup()
	}
	if !s.waitFocused(more, "chat-more") {
		return s.failCleanup()
	}

	// Compact sample: rail becomes a drawer; open, screenshot and close it.
	if !s.resize(s.windowIDValue, 390, 844, true) {
		return s.failCleanup()
	}
	if probeCount = s.waitProbe(probeCount, 10*time.Second); probeCount < len(viewports)+4 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "layout probe missing for the compact sample chat")
		return s.failCleanup()
	}
	if !s.screenshot("native/chat-sample-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	railToggle, ok := s.findRef("chat-rail-expand", "", "右侧面板", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.clickRefPhysical(railToggle, "chat-rail-toggle") {
		return s.failCleanup()
	}
	if !s.waitExpanded(railToggle, true, "chat-rail-open") {
		return s.failCleanup()
	}
	if !s.screenshot("native/chat-rail-drawer-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	railClose, ok := s.findRef("chat-rail-close", "", "关闭面板", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.clickRefPhysical(railClose, "chat-rail-close") {
		return s.failCleanup()
	}
	if !s.waitExpanded(railToggle, false, "chat-rail-closed") {
		return s.failCleanup()
	}
	if !s.screenshot("native/chat-rail-closed-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}

	// Compact history drawer: open, screenshot, Escape and focus return.
	historyToggle, ok := s.findRef("chat-history-open", "", "历史会话", 15)
	if !ok {
		return s.failCleanup()
	}
	if !s.clickRefPhysical(historyToggle, "chat-history-toggle") {
		return s.failCleanup()
	}
	if !s.waitRoleCount("chat-history-drawer", "window", "历史会话", 1, 10) {
		return s.failCleanup()
	}
	if !s.screenshot("native/chat-history-drawer-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.run(StepSpec{Name: "chat-close-history-drawer", Command: "self", Args: []string{"key", "--title=Anvil", "--combo=escape"}, Timeout: 60 * time.Second}) {
		return s.failCleanup()
	}
	if !s.waitRoleCount("chat-history-drawer-escape", "window", "历史会话", 0, 10) {
		return s.failCleanup()
	}
	if !s.waitFocused(historyToggle, "chat-history") {
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
		out.Limitations = append(out.Limitations, "application log was not produced; chat evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}

func (s *shellNative) waitExpanded(ref string, want bool, label string) bool {
	for attempt := 1; attempt <= 10; attempt++ {
		statusBefore := s.out.Status
		limitationsBefore := len(s.out.Limitations)
		ok := s.run(StepSpec{
			Name:                 fmt.Sprintf("expanded-%s-%02d", label, attempt),
			Command:              "agent-desktop",
			Args:                 []string{"is", ref, "--property", "expanded"},
			Timeout:              30 * time.Second,
			ExpectOutputContains: fmt.Sprintf(`"result":%t`, want),
		})
		if ok {
			return true
		}
		if s.out.Status == StatusBlocked {
			return false
		}
		s.out.Status = statusBefore
		if len(s.out.Limitations) > limitationsBefore {
			s.out.Limitations = s.out.Limitations[:limitationsBefore]
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("rail toggle did not report expanded=%t", want))
	return false
}

func (s *shellNative) waitRoleCount(logID, role, name string, want, attempts int) bool {
	for attempt := 1; attempt <= attempts; attempt++ {
		stepName := fmt.Sprintf("role-count-%s-%02d", logID, attempt)
		if !s.run(StepSpec{
			Name:    stepName,
			Command: "agent-desktop",
			Args:    []string{"find", "--role", role, "--name", name, "--exact", "--count", "--app", "anvil.exe", "--timeout-ms", "15000"},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		body, _ := s.exec.read(stepName)
		if parseFindCount(body) == want {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("role %s %q count did not become %d", role, name, want))
	return false
}

func (s *shellNative) clickRefPhysical(ref, logID string) bool {
	bounds, ok := s.elementBounds(logID, ref)
	if !ok {
		return false
	}
	statusBefore := s.out.Status
	limitationsBefore := len(s.out.Limitations)
	if !s.focusWindowRetry(logID) {
		// A background window may refuse foreground activation; fall back to
		// the semantic click path for ordinary buttons.
		s.out.Status = statusBefore
		if len(s.out.Limitations) > limitationsBefore {
			s.out.Limitations = s.out.Limitations[:limitationsBefore]
		}
		return s.run(StepSpec{Name: "semantic-click-" + logID, Command: "agent-desktop", Args: []string{"click", ref}, Timeout: 60 * time.Second})
	}
	x := int(bounds.X + bounds.Width/2)
	y := int(bounds.Y + bounds.Height/2)
	return s.run(StepSpec{
		Name: "physical-click-" + logID, Command: "agent-desktop",
		Args:    []string{"--headed", "mouse-click", "--xy", fmt.Sprintf("%d,%d", x, y)},
		Timeout: 60 * time.Second,
	})
}

func (s *shellNative) focusWindowRetry(label string) bool {
	for attempt := 1; attempt <= 3; attempt++ {
		statusBefore := s.out.Status
		limitationsBefore := len(s.out.Limitations)
		ok := s.run(StepSpec{
			Name:    fmt.Sprintf("focus-%s-%02d", label, attempt),
			Command: "agent-desktop",
			Args:    []string{"focus-window", "--window-id", s.windowIDValue},
			Timeout: 60 * time.Second,
		})
		if ok {
			return true
		}
		if s.out.Status == StatusBlocked {
			return false
		}
		s.out.Status = statusBefore
		if len(s.out.Limitations) > limitationsBefore {
			s.out.Limitations = s.out.Limitations[:limitationsBefore]
		}
		time.Sleep(250 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, "could not focus the Anvil window after 3 attempts")
	return false
}
