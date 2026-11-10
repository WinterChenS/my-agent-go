package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// shellNative runs the R0-01 real desktop case: launch the Wails client at the
// compact viewport, navigate every route through the UI, resize through the
// manifest viewport matrix while reading the real layout probe from the app
// log, then prove the window closed.
type shellNative struct {
	runner             automationRunner
	exec               desktopStepRunner
	out                *nativeOutcome
	dataDir            string
	webviewDir         string
	appLog             string
	windowIDValue      string
	window             windowBounds
	lastLaunchTheme    themeLogEntry
	lastLaunchLanguage languageLogEntry
	processInstance    string
	windowPID          int
	initialURL         string
}

func runNativeShellCase(r automationRunner, sha, target string, hooks *nativeHooks) nativeOutcome {
	out := nativeOutcome{}
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
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-shell")
	webviewDir := filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-shell")
	for _, dir := range []string{s.dataDir, webviewDir} {
		if err := resetDirectory(dir); err != nil {
			return s.blocked("prepare %q on D:: %v", dir, err)
		}
	}
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
	if !s.run(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + s.dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + webviewDir, "--create"}},
	) {
		s.markBlocked()
		return out
	}

	exe := filepath.Join(r.repo, ".tmp", "bin", "anvil.exe")
	if _, err := os.Stat(exe); err != nil {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("built client %q is missing: %v", exe, err))
		return out
	}
	dist := filepath.Join(r.repo, "frontend", "dist")
	launchArgs := []string{
		"launch", exe, "--no-attach",
		"--env", "ANVIL_DATA_DIR=" + s.dataDir,
		"--env", "ANVIL_WEBVIEW_DATA_DIR=" + webviewDir,
		"--env", "ANVIL_FRONTEND_DIST=" + dist,
		"--env", "ANVIL_WINDOW_WIDTH=390",
		"--env", "ANVIL_WINDOW_HEIGHT=844",
		"--timeout", "30000",
	}
	if !s.run(StepSpec{
		Name:                 "launch-390x844",
		Command:              "agent-desktop",
		Args:                 launchArgs,
		Timeout:              90 * time.Second,
		ExpectOutputContains: `"title":"Anvil"`,
	}) {
		if out.Status == StatusFail {
			return s.failCleanup()
		}
		return out
	}
	if !s.run(
		StepSpec{Name: "wait-window", Command: "agent-desktop", Args: []string{"wait", "--window", "Anvil", "--app", "anvil.exe", "--timeout", "30000"}, Timeout: 60 * time.Second},
		StepSpec{Name: "list-windows", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second},
	) {
		if out.Status == StatusFail {
			return s.failCleanup()
		}
		return out
	}
	windowsBody, _ := executor.read("list-windows")
	window, ok := parseFirstWindow(windowsBody)
	if !ok || window.Title != "Anvil" {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("Anvil window is missing or has the wrong title: %+v", window))
		return s.failCleanup()
	}
	if !near(window.Width, 390) || !near(window.Height, 844) {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("launched window bounds = %.0fx%.0f, want 390x844", window.Width, window.Height))
		return s.failCleanup()
	}
	s.windowIDValue = window.ID
	s.window = window
	if opts, found := lastWindowOptions(s.appLog); !found || opts.Width != 390 || opts.Height != 844 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("application log does not record window-options requested=390x844 (found %+v: %v)", opts, found))
		return s.failCleanup()
	}

	probeCount := s.waitProbe(0, 10*time.Second)
	if probeCount < 1 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "layout probe was not written to the application log after launch")
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}

	routes, err := readShellRoutes(r.repo)
	if err != nil {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, err.Error())
		return s.failCleanup()
	}
	for _, route := range routes {
		if !s.navigate(route, "narrow") {
			return s.failCleanup()
		}
	}
	if !s.screenshot("native/screenshot-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.resize(s.windowIDValue, 360, 800, true) {
		return s.failCleanup()
	}
	if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
		probeCount = next
	} else {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "layout probe did not record the 360x800 resize")
		return s.failCleanup()
	}
	lastRoute := routes[len(routes)-1]
	if lastRoute.Chrome == "shell" && lastRoute.BreadcrumbParent != "" {
		if !s.visibleStaticText("current-parent-360x800", lastRoute.BreadcrumbParent, 10) {
			return s.failCleanup()
		}
	}
	if !s.screenshot("native/screenshot-360x800.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.goBack("narrow", "/schedules") {
		return s.failCleanup()
	}

	if !s.resize(s.windowIDValue, 1440, 900, true) {
		return s.failCleanup()
	}
	if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
		probeCount = next
	} else {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "layout probe did not record the 1440x900 resize")
		return s.failCleanup()
	}
	for _, route := range routes {
		if !s.navigate(route, "wide") {
			return s.failCleanup()
		}
	}
	if !s.screenshot("native/screenshot-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}

	viewports, err := readManifestViewports(r.repo)
	if err != nil {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, err.Error())
		return s.failCleanup()
	}
	for _, viewport := range viewports {
		if !s.resize(s.windowIDValue, viewport.Width, viewport.Height, false) {
			return s.failCleanup()
		}
		if next := s.waitProbe(probeCount, 10*time.Second); next > probeCount {
			probeCount = next
		} else {
			s.out.Status = StatusFail
			s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("layout probe did not record viewport %dx%d", viewport.Width, viewport.Height))
			return s.failCleanup()
		}
	}

	if !s.run(
		StepSpec{Name: "close-app", Command: "agent-desktop", Args: []string{"close-app", "anvil.exe"}, Timeout: 60 * time.Second},
		StepSpec{Name: "list-windows-after-close", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second, ExpectOutputContains: `"data":[]`},
	) {
		return out
	}

	appLog, err := os.ReadFile(s.appLog)
	if err != nil {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("read application log: %v", err))
		return out
	}
	probes := parseLayoutProbes(string(appLog))
	if len(probes) < 10 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("application log has %d layout probes, want at least 10", len(probes)))
		return out
	}
	unique := map[string]bool{}
	for _, probe := range probes {
		unique[probe.Viewport] = true
		if probe.Overflow {
			s.out.Status = StatusFail
			s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("horizontal overflow at viewport %s: scrollWidth=%d clientWidth=%d", probe.Viewport, probe.ScrollWidth, probe.ClientWidth))
			return out
		}
	}
	if len(unique) < 8 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("layout probe recorded only %d distinct viewports", len(unique)))
		return out
	}
	if artifact, ok := copyAppLog(r, s.dataDir); ok {
		s.out.Artifacts = append(s.out.Artifacts, artifact)
	} else {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "application log was not produced; layout evidence is incomplete")
		return out
	}

	s.out.Status = StatusPass
	return out
}

func (s *shellNative) blocked(format string, args ...any) nativeOutcome {
	s.out.Status = StatusBlocked
	s.out.Limitations = append(s.out.Limitations, withReverify(fmt.Sprintf(format, args...)))
	return *s.out
}

func (s *shellNative) markBlocked() {
	s.out.Status = StatusBlocked
	s.out.Limitations = []string{withReverify(strings.Join(s.out.Limitations, "; "))}
}

func (s *shellNative) run(specs ...StepSpec) bool {
	for _, spec := range specs {
		cmd, artifact, failure := s.exec.run(spec)
		s.out.Commands = append(s.out.Commands, cmd)
		if artifact.Path != "" {
			s.out.Artifacts = append(s.out.Artifacts, artifact)
		}
		if failure == "" {
			continue
		}
		body, _ := s.exec.read(spec.Name)
		if blocked, _ := isBlockedDesktopFailure(spec.Command, failure, body); blocked {
			s.out.Status = StatusBlocked
			s.out.Limitations = append(s.out.Limitations, withReverify(desktopFailureDetail(spec.Command, failure, body)))
			return false
		}
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, failure)
		return false
	}
	return true
}

func (s *shellNative) failCleanup() nativeOutcome {
	if s.out.Status == StatusFail {
		cmd, artifact, failure := s.exec.run(StepSpec{
			Name:    "cleanup-close-app",
			Command: "agent-desktop",
			Args:    []string{"close-app", "anvil.exe", "--force"},
			Timeout: 60 * time.Second,
		})
		s.out.Commands = append(s.out.Commands, cmd)
		if artifact.Path != "" {
			s.out.Artifacts = append(s.out.Artifacts, artifact)
		}
		if failure != "" && len(s.out.Limitations) == 0 {
			s.out.Limitations = append(s.out.Limitations, failure)
		}
	}
	return *s.out
}

func (s *shellNative) findRef(logID, role, name string, attempts int) (string, bool) {
	for attempt := 1; attempt <= attempts; attempt++ {
		stepName := fmt.Sprintf("find-%s-%02d", logID, attempt)
		args := []string{"find", "--name", name, "--exact", "--first", "--app", "anvil.exe", "--timeout-ms", "15000"}
		if role != "" {
			args = append(args, "--role", role)
		}
		if !s.run(StepSpec{Name: stepName, Command: "agent-desktop", Args: args, Timeout: 60 * time.Second}) {
			return "", false
		}
		body, _ := s.exec.read(stepName)
		if ref, ok := parseFindRef(body); ok {
			return ref, true
		}
		time.Sleep(400 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("could not find %s %q in the accessibility tree after %d attempts", role, name, attempts))
	return "", false
}

func (s *shellNative) waitText(logID, text string, exact bool, attempts int) bool {
	for attempt := 1; attempt <= attempts; attempt++ {
		stepName := fmt.Sprintf("marker-%s-%02d", logID, attempt)
		if !s.run(StepSpec{
			Name:    stepName,
			Command: "agent-desktop",
			Args:    []string{"find", "--text", text, "--app", "anvil.exe", "--timeout-ms", "15000"},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		body, _ := s.exec.read(stepName)
		if exact {
			for _, name := range parseFindNames(body) {
				if name == text {
					return true
				}
			}
		} else if parseFindCount(body) > 0 {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("route marker %q did not appear after %d attempts", text, attempts))
	return false
}

func (s *shellNative) activate() bool {
	for attempt := 1; attempt <= 30; attempt++ {
		stepName := fmt.Sprintf("activate-%02d", attempt)
		if !s.run(StepSpec{
			Name:    stepName,
			Command: "agent-desktop",
			Args:    []string{"find", "--role", "link", "--count", "--app", "anvil.exe", "--timeout-ms", "15000"},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		body, _ := s.exec.read(stepName)
		if parseFindCount(body) >= 4 {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, "the WebView accessibility tree did not expose the shell navigation")
	return false
}

func (s *shellNative) navigate(route shellRoute, label string) bool {
	var ref string
	var ok bool
	if route.Group == "tools" {
		trigger, found := s.findRef("tools-trigger-"+label, "menubutton", "工具与自动化", 20)
		if !found {
			return false
		}
		if !s.run(StepSpec{
			Name:    "expand-tools-" + label,
			Command: "agent-desktop",
			Args:    []string{"expand", trigger},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		ref, ok = s.findRef(route.ID+"-"+label, "menuitem", route.NavLabel, 20)
	} else {
		ref, ok = s.findRef(route.ID+"-"+label, "link", route.NavLabel, 20)
	}
	if !ok {
		return false
	}
	if !s.run(StepSpec{
		Name:    "click-" + route.ID + "-" + label,
		Command: "agent-desktop",
		Args:    []string{"click", ref},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	if !s.waitText(route.ID+"-"+label, route.Marker, route.Path == "/", 20) {
		return false
	}
	if route.Chrome == "shell" && route.BreadcrumbParent != "" {
		if !s.waitText(route.ID+"-crumb-"+label, route.BreadcrumbParent+" / "+route.Title, false, 10) {
			return false
		}
		// Aria-label is not enough: require the visible parent static text to
		// have real bounds at this viewport (this is the F7 narrow assertion).
		if !s.visibleStaticText(route.ID+"-parent-"+label, route.BreadcrumbParent, 10) {
			return false
		}
	}
	return s.run(StepSpec{
		Name:    "snapshot-" + route.ID + "-" + label,
		Command: "agent-desktop",
		Args:    []string{"snapshot", "--app", "anvil.exe", "--window-id", s.windowIDValue, "--timeout-ms", "30000", "--compact"},
		Timeout: 90 * time.Second,
	})
}

func (s *shellNative) visibleStaticText(logID, name string, attempts int) bool {
	for attempt := 1; attempt <= attempts; attempt++ {
		stepName := fmt.Sprintf("visible-%s-%02d", logID, attempt)
		if !s.run(StepSpec{
			Name:    stepName,
			Command: "agent-desktop",
			Args:    []string{"find", "--text", name, "--app", "anvil.exe", "--timeout-ms", "15000"},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		body, _ := s.exec.read(stepName)
		for _, match := range parseFindMatches(body) {
			if match.Name == name && match.Role == "statictext" && match.Bounds.Width > 0 && match.Bounds.Height > 0 {
				return true
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("visible breadcrumb parent %q had no non-zero statictext bounds after %d attempts", name, attempts))
	return false
}

func (s *shellNative) goBack(label, marker string) bool {
	// The prototype has no shared back button, so focus the web content and
	// inject the platform history shortcut Alt+Left (devtools key uses
	// SendInput; agent-desktop press cannot verify WebView2 child focus).
	// Retry the focus+key pair: the first physical click can race the window
	// resize/focus transition on narrow viewports.
	x := int(s.window.X + s.window.Width*0.6)
	y := int(s.window.Y + s.window.Height*0.6)
	for attempt := 1; attempt <= 3; attempt++ {
		// Bring Anvil to the foreground first: absolute mouse clicks otherwise
		// land on whatever window happens to be on top after a long run.
		if !s.run(StepSpec{
			Name:    fmt.Sprintf("focus-window-back-%s-%02d", label, attempt),
			Command: "agent-desktop",
			Args:    []string{"focus-window", "--window-id", s.windowIDValue},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		if !s.run(StepSpec{
			Name:    fmt.Sprintf("focus-page-%s-%02d", label, attempt),
			Command: "agent-desktop",
			Args:    []string{"--headed", "mouse-click", "--xy", fmt.Sprintf("%d,%d", x, y)},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		if !s.run(StepSpec{
			Name:    fmt.Sprintf("history-back-%s-%02d", label, attempt),
			Command: "self",
			Args:    []string{"key", "--title=Anvil", "--combo=alt+left"},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		if s.markerPresent(fmt.Sprintf("back-%s-%02d", label, attempt), marker, 6) {
			return true
		}
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("history back to %q did not take effect after 3 focus+Alt+Left attempts", marker))
	return false
}

func (s *shellNative) markerPresent(logID, text string, attempts int) bool {
	for attempt := 1; attempt <= attempts; attempt++ {
		stepName := fmt.Sprintf("marker-%s-%02d", logID, attempt)
		if !s.run(StepSpec{
			Name:    stepName,
			Command: "agent-desktop",
			Args:    []string{"find", "--text", text, "--app", "anvil.exe", "--timeout-ms", "15000"},
			Timeout: 60 * time.Second,
		}) {
			return false
		}
		body, _ := s.exec.read(stepName)
		if parseFindCount(body) > 0 {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

func (s *shellNative) screenshot(rel string, windowID string) bool {
	path := filepath.Join(s.runner.caseDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("create screenshot directory: %v", err))
		return false
	}
	if !s.run(StepSpec{
		Name:    "screenshot-" + strings.ReplaceAll(rel, "/", "-"),
		Command: "agent-desktop",
		Args:    []string{"screenshot", "--app", "anvil.exe", "--window-id", windowID, path},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() < 1024 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("screenshot %q missing or too small", rel))
		return false
	}
	artifact, err := hashArtifact(s.runner.caseDir, rel)
	if err != nil {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("hash screenshot %q: %v", rel, err))
		return false
	}
	s.out.Artifacts = append(s.out.Artifacts, artifact)
	return true
}

func (s *shellNative) resize(windowID string, width, height int, strict bool) bool {
	resizeSpec := StepSpec{
		Name:    fmt.Sprintf("resize-%dx%d", width, height),
		Command: "agent-desktop",
		Args:    []string{"resize-window", "--app", "anvil.exe", "--window-id", windowID, "--width", fmt.Sprint(width), "--height", fmt.Sprint(height)},
		Timeout: 60 * time.Second,
	}
	cmd, artifact, failure := s.exec.run(resizeSpec)
	s.out.Commands = append(s.out.Commands, cmd)
	if artifact.Path != "" {
		s.out.Artifacts = append(s.out.Artifacts, artifact)
	}
	if failure != "" {
		body, _ := s.exec.read(resizeSpec.Name)
		if resizeLandedAfterTimeout(body) {
			s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("resize-window %dx%d reported TIMEOUT after applying; bounds and layout probe confirm the resize", width, height))
		} else {
			if blocked, _ := isBlockedDesktopFailure(resizeSpec.Command, failure, body); blocked {
				s.out.Status = StatusBlocked
				s.out.Limitations = append(s.out.Limitations, withReverify(desktopFailureDetail(resizeSpec.Command, failure, body)))
				return false
			}
			s.out.Status = StatusFail
			s.out.Limitations = append(s.out.Limitations, failure)
			return false
		}
	}
	stepName := fmt.Sprintf("list-after-resize-%dx%d", width, height)
	if !s.run(StepSpec{
		Name:    stepName,
		Command: "agent-desktop",
		Args:    []string{"list-windows", "--app", "anvil.exe"},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	body, _ := s.exec.read(stepName)
	window, ok := parseFirstWindow(body)
	widthOK := ok && near(window.Width, float64(width))
	heightOK := ok && near(window.Height, float64(height))
	if !widthOK || (strict && !heightOK) {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("resize to %dx%d reported bounds %+v", width, height, window))
		return false
	}
	if ok {
		s.window = window
	}
	return true
}

// resizeLandedAfterTimeout recognises the agent-desktop timestamp race where
// the semantic resize was delivered and applied but its post-action
// verification timed out (observed for windows taller than the work area).
func resizeLandedAfterTimeout(body []byte) bool {
	type disposition struct {
		Delivery string `json:"delivery"`
	}
	var envelope struct {
		Error *struct {
			Code        string      `json:"code"`
			Disposition disposition `json:"disposition"`
		} `json:"error"`
		Disposition disposition `json:"disposition"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil || envelope.Error == nil {
		return false
	}
	delivery := envelope.Disposition.Delivery
	if delivery == "" {
		delivery = envelope.Error.Disposition.Delivery
	}
	return envelope.Error.Code == "TIMEOUT" && delivery == "delivered_unverified"
}

func (s *shellNative) waitProbe(after int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		if body, err := os.ReadFile(s.appLog); err == nil {
			if count := len(parseLayoutProbes(string(body))); count > after {
				return count
			}
		}
		if time.Now().After(deadline) {
			return after
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func lastWindowOptions(appLog string) (windowGeometry, bool) {
	body, err := os.ReadFile(appLog)
	if err != nil {
		return windowGeometry{}, false
	}
	var latest windowGeometry
	found := false
	for _, line := range strings.Split(string(body), "\n") {
		if opts, ok := parseWindowOptions(line); ok {
			latest = opts
			found = true
		}
	}
	return latest, found
}

func near(actual, want float64) bool {
	delta := actual - want
	if delta < 0 {
		delta = -delta
	}
	return delta <= 8
}
