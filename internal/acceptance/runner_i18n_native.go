package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runNativeI18nCase exercises the real language switch: default Chinese,
// English navigation/breadcrumb/placeholder/accessibility/title/lang, a return
// to Chinese, both viewports, and English persistence across a restart.
func runNativeI18nCase(r automationRunner, sha, target string, hooks *nativeHooks) (out nativeOutcome) {
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
	s.dataDir = filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target+"-i18n")
	s.webviewDir = filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target+"-i18n")
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
		return s.blocked("reset i18n data directory: %v", err)
	}
	if err := resetDirectory(s.webviewDir); err != nil {
		return s.blocked("reset i18n WebView data directory: %v", err)
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

	if !s.launchI18nClient(390, 844, "launch-i18n-390", "zh-CN") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.navigate(*settingsRoute, "i18n-zh") {
		return s.failCleanup()
	}
	if !s.assertChineseUI("zh") {
		return s.failCleanup()
	}
	if !s.screenshot("native/i18n-zh-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}

	// Chinese -> English through the actual language radio control.
	if !s.clickLanguageOption(4, "i18n-en-390", "en-US") {
		return s.failCleanup()
	}
	if !s.assertEnglishUI("en") {
		return s.failCleanup()
	}
	if !s.screenshot("native/i18n-en-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}

	// Back to Chinese for the second half of the viewport pair.
	if !s.clickLanguageOption(3, "i18n-zh-back", "zh-CN") {
		return s.failCleanup()
	}
	if !s.waitTitle("设置 · Anvil", 10*time.Second) {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "Chinese document title was not restored")
		return s.failCleanup()
	}

	if !s.resize(s.windowIDValue, 1440, 900, true) {
		return s.failCleanup()
	}
	if !s.screenshot("native/i18n-zh-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}
	if !s.clickLanguageOption(4, "i18n-en-wide", "en-US") {
		return s.failCleanup()
	}
	if !s.assertEnglishUI("en-wide") {
		return s.failCleanup()
	}
	if !s.screenshot("native/i18n-en-1440x900.png", s.windowIDValue) {
		return s.failCleanup()
	}

	// English must survive a restart, and the preferences file must say so.
	if !s.closeThemeClient() {
		return s.failCleanup()
	}
	if !s.launchI18nClient(390, 844, "relaunch-i18n-en", "en-US") {
		return s.failCleanup()
	}
	if !s.activate() {
		return s.failCleanup()
	}
	if !s.assertEnglishHome("restart") {
		return s.failCleanup()
	}
	if !s.screenshot("native/i18n-restart-en-390x844.png", s.windowIDValue) {
		return s.failCleanup()
	}
	prefBody, err := os.ReadFile(filepath.Join(s.dataDir, "preferences.json"))
	if err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("read preferences.json after restart: %v", err))
		return s.failCleanup()
	}
	language, ok := parseLanguagePreference(prefBody)
	if !ok || language != "en-US" {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("preferences.json language = %q", language))
		return s.failCleanup()
	}
	prefPath := filepath.Join(nativeDir, "preferences.json")
	if err := os.WriteFile(prefPath, prefBody, 0o644); err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("copy preferences.json: %v", err))
		return s.failCleanup()
	}
	if artifact, err := hashArtifact(s.runner.caseDir, "native/preferences.json"); err == nil {
		out.Artifacts = append(out.Artifacts, artifact)
	}

	if !s.closeThemeClient() {
		return s.failCleanup()
	}
	if artifact, ok := copyAppLog(s.runner, s.dataDir); ok {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "application log was not produced; i18n evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}

func (s *shellNative) launchI18nClient(width, height int, label, expectedLanguage string) bool {
	before := len(s.i18nEntries())
	if !s.launchThemeClient(width, height, label) {
		return false
	}
	entry, ok := s.waitLanguageSince(before, expectedLanguage, 20*time.Second)
	if !ok {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("frontend did not report language %s after launch", expectedLanguage))
		return false
	}
	s.lastLaunchLanguage = entry
	return true
}

func (s *shellNative) i18nEntries() []languageLogEntry {
	return parseI18nLogs(readFileBestEffort(s.appLog))
}

func (s *shellNative) waitLanguageSince(after int, expected string, timeout time.Duration) (languageLogEntry, bool) {
	deadline := time.Now().Add(timeout)
	for {
		entries := s.i18nEntries()
		if len(entries) > after {
			last := entries[len(entries)-1]
			if last.Language == expected {
				return last, true
			}
		}
		if time.Now().After(deadline) {
			return languageLogEntry{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *shellNative) waitLanguage(expected string, timeout time.Duration) (languageLogEntry, bool) {
	deadline := time.Now().Add(timeout)
	for {
		entries := s.i18nEntries()
		if len(entries) > 0 && entries[len(entries)-1].Language == expected {
			return entries[len(entries)-1], true
		}
		if time.Now().After(deadline) {
			return languageLogEntry{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *shellNative) latestTitle() (string, bool) {
	return parseTitleLog(readFileBestEffort(s.appLog))
}

func (s *shellNative) waitTitle(expected string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if title, ok := s.latestTitle(); ok && title == expected {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (s *shellNative) clickLanguageOption(index int, logID, expectedLanguage string) bool {
	ref, ok := s.findRadio(logID, index)
	if !ok {
		return false
	}
	bounds, ok := s.elementBounds(logID, ref)
	if !ok {
		return false
	}
	if !s.run(StepSpec{
		Name:    "focus-language-window-" + logID,
		Command: "agent-desktop",
		Args:    []string{"focus-window", "--window-id", s.windowIDValue},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	x := int(bounds.X + bounds.Width/2)
	y := int(bounds.Y + bounds.Height/2)
	if !s.run(StepSpec{
		Name:    "click-language-option-" + logID,
		Command: "agent-desktop",
		Args:    []string{"--headed", "mouse-click", "--xy", fmt.Sprintf("%d,%d", x, y)},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	if _, ok := s.waitLanguage(expectedLanguage, 10*time.Second); !ok {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("language option %d did not switch to %s", index, expectedLanguage))
		return false
	}
	return true
}

func (s *shellNative) assertChineseUI(logID string) bool {
	if s.lastLaunchLanguage.Language != "zh-CN" || s.lastLaunchLanguage.HTMLLang != "zh-CN" {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("initial language = %+v, want zh-CN", s.lastLaunchLanguage))
		return false
	}
	if !s.waitText(logID+"-nav", "工作台", false, 10) ||
		!s.waitText(logID+"-crumb", "系统 / 设置", false, 10) ||
		!s.waitText(logID+"-placeholder", "路由占位", false, 10) {
		return false
	}
	if _, ok := s.findRef(logID+"-workbench-link", "link", "工作台", 10); !ok {
		return false
	}
	if _, ok := s.findRef(logID+"-search", "button", "搜索", 10); !ok {
		return false
	}
	if !s.waitTitle("设置 · Anvil", 10*time.Second) {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "Chinese document title was not observed")
		return false
	}
	return true
}

func (s *shellNative) assertEnglishHome(logID string) bool {
	entry, ok := s.waitLanguage("en-US", 10*time.Second)
	if !ok || entry.HTMLLang != "en-US" {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("English language/html lang = %+v", entry))
		return false
	}
	if !s.waitText(logID+"-nav", "Dashboard", false, 10) {
		return false
	}
	if _, ok := s.findRef(logID+"-dashboard-link", "link", "Dashboard", 10); !ok {
		return false
	}
	if !s.waitTitle("Dashboard · Anvil", 10*time.Second) {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "English home document title was not observed")
		return false
	}
	return true
}

func (s *shellNative) assertEnglishUI(logID string) bool {
	entry, ok := s.waitLanguage("en-US", 10*time.Second)
	if !ok || entry.HTMLLang != "en-US" {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, fmt.Sprintf("English language/html lang = %+v", entry))
		return false
	}
	if !s.waitText(logID+"-nav", "Dashboard", false, 10) ||
		!s.waitText(logID+"-crumb", "System / Settings", false, 10) ||
		!s.waitText(logID+"-placeholder", "Route placeholder", false, 10) ||
		!s.waitText(logID+"-status", "Runtime not connected", false, 10) {
		return false
	}
	if _, ok := s.findRef(logID+"-dashboard-link", "link", "Dashboard", 10); !ok {
		return false
	}
	if _, ok := s.findRef(logID+"-search", "button", "Search", 10); !ok {
		return false
	}
	if !s.waitTitle("Settings · Anvil", 10*time.Second) {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "English document title was not observed")
		return false
	}
	return true
}
