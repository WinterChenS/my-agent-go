package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const appsUseLightThemeKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`

type registryEvidence struct {
	Original        int  `json:"original"`
	SystemFlip      int  `json:"systemFlip"`
	ManualLightFlip int  `json:"manualLightFlip"`
	ManualDarkFlip  int  `json:"manualDarkFlip"`
	Restored        int  `json:"restored"`
	RestoredOK      bool `json:"restoredOK"`
}

// runNativeThemeCase exercises the real theme pipeline: the settings control,
// dark/light/system rendering, real HKCU AppsUseLightTheme flips while in
// system mode (follows) and manual mode (ignores), and restart persistence.
func runNativeThemeCase(r automationRunner, sha, target string, hooks *nativeHooks) (out nativeOutcome) {
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
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-theme")
	s.webviewDir = filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-theme")
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
		return s.blocked("reset theme data directory: %v", err)
	}
	if err := resetDirectory(s.webviewDir); err != nil {
		return s.blocked("reset theme WebView data directory: %v", err)
	}
	if !s.run(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + s.dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + s.webviewDir, "--create"}},
	) {
		s.markBlocked()
		return out
	}

	routes, err := readShellRoutes(r.repo)
	if err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, err.Error())
		return out
	}
	var settingsRoute *shellRoute
	for i := range routes {
		if routes[i].ID == "settings" {
			settingsRoute = &routes[i]
		}
	}
	if settingsRoute == nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "settings route is missing from routes.json")
		return out
	}

	// First launch: fresh data directory, system mode.
	if !s.launchThemeClient(390, 844, "launch-theme-390") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.navigate(*settingsRoute, "theme") {
		return s.failCleanup()
	}
	systemEntry := s.lastLaunchTheme
	if systemEntry.Mode != "system" {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("initial theme was not system: %+v", systemEntry))
		return s.failCleanup()
	}
	if !s.screenshot("native/theme-system-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}

	// Light and dark at the compact viewport.
	if _, ok := s.clickThemeOption(1, "theme-light-390", "light"); !ok {
		return s.failCleanup()
	}
	if !s.screenshot("native/theme-light-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if _, ok := s.clickThemeOption(2, "theme-dark-390", "dark"); !ok {
		return s.failCleanup()
	}
	if !s.screenshot("native/theme-dark-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}

	// Registry backup and real system-follow test.
	original, ok := s.registryQuery("registry-query-original")
	if !ok {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "could not read AppsUseLightTheme; system-follow evidence unavailable")
		return s.failCleanup()
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = s.registrySet(original, "registry-restore-final")
		if !restored {
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, "failed to restore AppsUseLightTheme to its original value")
		}
	}
	defer restore()

	if _, ok := s.clickThemeOption(0, "theme-system-flip", "system"); !ok {
		return s.failCleanup()
	}
	systemFlip := 1 - original
	if !s.registrySet(systemFlip, "registry-flip-system") {
		return s.failCleanup()
	}
	expectedFlip := "light"
	if systemFlip == 0 {
		expectedFlip = "dark"
	}
	if _, ok := s.waitThemeFor("system", expectedFlip, 15*time.Second); !ok {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("system mode did not follow AppsUseLightTheme=%d", systemFlip))
		return s.failCleanup()
	}
	if !s.registrySet(original, "registry-restore-system") {
		return s.failCleanup()
	}
	expectedOriginal := "light"
	if original == 0 {
		expectedOriginal = "dark"
	}
	if _, ok := s.waitThemeFor("system", expectedOriginal, 15*time.Second); !ok {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "system mode did not return to the original Windows theme")
		return s.failCleanup()
	}

	// Manual light ignores a system flip to dark.
	if _, ok := s.clickThemeOption(1, "theme-light-manual", "light"); !ok {
		return s.failCleanup()
	}
	if !s.registrySet(0, "registry-flip-manual-light") {
		return s.failCleanup()
	}
	time.Sleep(2500 * time.Millisecond)
	if entry, ok := s.latestTheme(); !ok || entry.Mode != "light" || entry.Resolved != "light" {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("manual light followed a system flip: %+v", entry))
		return s.failCleanup()
	}
	if !s.registrySet(original, "registry-restore-manual-light") {
		return s.failCleanup()
	}

	// Manual dark ignores a system flip to light.
	if _, ok := s.clickThemeOption(2, "theme-dark-manual", "dark"); !ok {
		return s.failCleanup()
	}
	if !s.registrySet(1, "registry-flip-manual-dark") {
		return s.failCleanup()
	}
	time.Sleep(2500 * time.Millisecond)
	if entry, ok := s.latestTheme(); !ok || entry.Mode != "dark" || entry.Resolved != "dark" {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("manual dark followed a system flip: %+v", entry))
		return s.failCleanup()
	}
	if !s.registrySet(original, "registry-restore-manual-dark") {
		return s.failCleanup()
	}

	// Wide screenshots for all three modes.
	if !s.resize(s.windowIDValue, 1440, 900, true) {
		return s.failCleanup()
	}
	if _, ok := s.clickThemeOption(0, "theme-system-wide", "system"); !ok {
		return s.failCleanup()
	}
	if !s.screenshot("native/theme-system-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if _, ok := s.clickThemeOption(1, "theme-light-wide", "light"); !ok {
		return s.failCleanup()
	}
	if !s.screenshot("native/theme-light-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if _, ok := s.clickThemeOption(2, "theme-dark-wide", "dark"); !ok {
		return s.failCleanup()
	}
	if !s.screenshot("native/theme-dark-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}

	// Restart persistence: dark survives a relaunch.
	if !s.closeThemeClient() {
		return s.failCleanup()
	}
	if !s.launchThemeClient(390, 844, "relaunch-dark-390") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	restartDark := s.lastLaunchTheme
	if restartDark.Mode != "dark" || restartDark.Resolved != "dark" {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("dark theme did not survive a restart: %+v", restartDark))
		return s.failCleanup()
	}
	if !s.screenshot("native/theme-restart-dark-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.navigate(*settingsRoute, "theme-restart") {
		return s.failCleanup()
	}
	if _, ok := s.clickThemeOption(0, "theme-system-restart", "system"); !ok {
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	// Restart persistence: system survives another relaunch.
	if !s.launchThemeClient(390, 844, "relaunch-system-390") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if entry := s.lastLaunchTheme; entry.Mode != "system" || entry.Resolved != expectedOriginal {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("system theme did not survive a restart: %+v", entry))
		return s.failCleanup()
	}
	if !s.closeThemeClient() {
		return s.failCleanup()
	}

	// Restore the user's Windows theme explicitly before writing evidence.
	if !s.registrySet(original, "registry-restore-explicit") {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "failed to restore AppsUseLightTheme")
		return s.failCleanup()
	}
	restoredValue, restoredOK := s.registryQuery("registry-query-restored")
	restored = restoredOK && restoredValue == original
	registry := registryEvidence{
		Original:        original,
		SystemFlip:      systemFlip,
		ManualLightFlip: 0,
		ManualDarkFlip:  1,
		Restored:        restoredValue,
		RestoredOK:      restored,
	}
	if artifact, err := writeJSONArtifact(s.runner.caseDir, "native/registry.json", registry); err == nil {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("write registry evidence: %v", err))
	}

	if !restored {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("AppsUseLightTheme restore mismatch: original=%d restored=%d", original, restoredValue))
	}

	if artifact, ok := copyAppLog(s.runner, s.dataDir); ok {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "application log was not produced; theme evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}

func (s *shellNative) launchThemeClient(width, height int, label string) bool {
	exe := filepath.Join(s.runner.repo, ".tmp", "bin", "anvil.exe")
	if _, err := os.Stat(exe); err != nil {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("built client %q is missing: %v", exe, err))
		return false
	}
	dist := filepath.Join(s.runner.repo, "frontend", "dist")
	before := len(s.themeEntries())
	args := []string{
		"launch", exe, "--no-attach",
		"--env", "ANVIL_DATA_DIR=" + s.dataDir,
		"--env", "ANVIL_WEBVIEW_DATA_DIR=" + s.webviewDir,
		"--env", "ANVIL_FRONTEND_DIST=" + dist,
		"--env", "ANVIL_WINDOW_WIDTH=" + strconv.Itoa(width),
		"--env", "ANVIL_WINDOW_HEIGHT=" + strconv.Itoa(height),
		"--timeout", "30000",
	}
	if s.initialURL != "" {
		args = append(args, "--env", "ANVIL_INITIAL_URL="+s.initialURL)
	}
	if !s.run(StepSpec{
		Name:                 label,
		Command:              "agent-desktop",
		Args:                 args,
		Timeout:              90 * time.Second,
		ExpectOutputContains: `"title":"Anvil"`,
	}) {
		return false
	}
	if !s.run(
		StepSpec{Name: label + "-wait-window", Command: "agent-desktop", Args: []string{"wait", "--window", "Anvil", "--app", "anvil.exe", "--timeout", "30000"}, Timeout: 60 * time.Second},
		StepSpec{Name: label + "-list-windows", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second},
	) {
		return false
	}
	body, _ := s.exec.read(label + "-list-windows")
	window, ok := parseFirstWindow(body)
	if !ok || window.Title != "Anvil" || !near(window.Width, float64(width)) || !near(window.Height, float64(height)) {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("theme window bounds = %+v, want %dx%d", window, width, height))
		return false
	}
	s.windowIDValue = window.ID
	s.window = window
	entry, ok := s.waitTheme(before, 20*time.Second)
	if !ok {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "frontend did not report a theme after launch")
		return false
	}
	s.lastLaunchTheme = entry
	return true
}

func (s *shellNative) closeThemeClient() bool {
	if !s.run(
		StepSpec{Name: "theme-close-app", Command: "agent-desktop", Args: []string{"close-app", "anvil.exe"}, Timeout: 60 * time.Second},
		StepSpec{Name: "theme-list-windows-after-close", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second, ExpectOutputContains: `"data":[]`},
	) {
		return false
	}
	return true
}

func (s *shellNative) themeEntries() []themeLogEntry {
	body, err := os.ReadFile(s.appLog)
	if err != nil {
		return nil
	}
	return parseThemeLogs(string(body))
}

func (s *shellNative) latestTheme() (themeLogEntry, bool) {
	return latestThemeLog(readFileBestEffort(s.appLog))
}

func (s *shellNative) waitTheme(after int, timeout time.Duration) (themeLogEntry, bool) {
	deadline := time.Now().Add(timeout)
	for {
		entries := s.themeEntries()
		if len(entries) > after {
			return entries[len(entries)-1], true
		}
		if time.Now().After(deadline) {
			return themeLogEntry{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *shellNative) waitThemeFor(mode, resolved string, timeout time.Duration) (themeLogEntry, bool) {
	deadline := time.Now().Add(timeout)
	for {
		entries := s.themeEntries()
		if len(entries) > 0 {
			last := entries[len(entries)-1]
			if last.Mode == mode && last.Resolved == resolved {
				return last, true
			}
		}
		if time.Now().After(deadline) {
			return themeLogEntry{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func readFileBestEffort(path string) string {
	body, _ := os.ReadFile(path)
	return string(body)
}

func (s *shellNative) clickThemeOption(index int, logID, expectedMode string) (themeLogEntry, bool) {
	ref, ok := s.findRadio(logID, index)
	if !ok {
		return themeLogEntry{}, false
	}
	bounds, ok := s.elementBounds(logID, ref)
	if !ok {
		return themeLogEntry{}, false
	}
	if !s.run(StepSpec{
		Name:    "focus-theme-window-" + logID,
		Command: "agent-desktop",
		Args:    []string{"focus-window", "--window-id", s.windowIDValue},
		Timeout: 60 * time.Second,
	}) {
		return themeLogEntry{}, false
	}
	x := int(bounds.X + bounds.Width/2)
	y := int(bounds.Y + bounds.Height/2)
	if !s.run(StepSpec{
		Name:    "click-theme-option-" + logID,
		Command: "agent-desktop",
		Args:    []string{"--headed", "mouse-click", "--xy", fmt.Sprintf("%d,%d", x, y)},
		Timeout: 60 * time.Second,
	}) {
		return themeLogEntry{}, false
	}
	resolved := expectedMode
	if expectedMode == "system" {
		resolved = ""
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries := s.themeEntries()
		if len(entries) > 0 {
			last := entries[len(entries)-1]
			if last.Mode == expectedMode && (resolved == "" || last.Resolved == resolved) {
				return last, true
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("theme option %d did not produce mode=%s", index, expectedMode))
	return themeLogEntry{}, false
}

func (s *shellNative) findRadio(logID string, index int) (string, bool) {
	for attempt := 1; attempt <= 20; attempt++ {
		stepName := fmt.Sprintf("find-radio-%s-%02d", logID, attempt)
		if !s.run(StepSpec{
			Name:    stepName,
			Command: "agent-desktop",
			Args:    []string{"find", "--role", "radiobutton", "--app", "anvil.exe", "--timeout-ms", "15000"},
			Timeout: 60 * time.Second,
		}) {
			return "", false
		}
		body, _ := s.exec.read(stepName)
		matches := parseFindMatches(body)
		if len(matches) >= 3 {
			if matches[index].RefID == "" {
				return "", false
			}
			return matches[index].RefID, true
		}
		time.Sleep(400 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, "theme radiogroup did not expose three options")
	return "", false
}

func (s *shellNative) elementBounds(logID, ref string) (elementBounds, bool) {
	stepName := "bounds-" + logID
	if !s.run(StepSpec{
		Name:    stepName,
		Command: "agent-desktop",
		Args:    []string{"get", ref, "--property", "bounds"},
		Timeout: 60 * time.Second,
	}) {
		return elementBounds{}, false
	}
	body, _ := s.exec.read(stepName)
	bounds, ok := parseElementBounds(body)
	if !ok {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "theme option had no usable bounds")
	}
	return bounds, ok
}

func (s *shellNative) registryQuery(name string) (int, bool) {
	if !s.run(StepSpec{
		Name:    name,
		Command: "reg",
		Args:    []string{"query", appsUseLightThemeKey, "/v", "AppsUseLightTheme"},
		Timeout: 60 * time.Second,
	}) {
		return 0, false
	}
	body, _ := s.exec.read(name)
	value, ok := parseRegAppsUseLightTheme(string(body))
	if !ok {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "could not parse AppsUseLightTheme from reg query")
	}
	return value, ok
}

func (s *shellNative) registrySet(value int, name string) bool {
	return s.run(StepSpec{
		Name:    name,
		Command: "reg",
		Args: []string{
			"add", appsUseLightThemeKey, "/v", "AppsUseLightTheme", "/t", "REG_DWORD",
			"/d", strconv.Itoa(value), "/f",
		},
		Timeout: 60 * time.Second,
	})
}
