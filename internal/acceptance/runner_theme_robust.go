package acceptance

import (
	"fmt"
	"os"
	"time"
)

// clickThemeOptionOnce performs one complete identity → bounds → click →
// expected-log sequence. Failure details are returned so the caller can retry
// and record raw evidence.
func (s *shellNative) clickThemeOptionOnce(index int, logID, expectedMode string) (themeLogEntry, bool, string, elementBounds, []string) {
	stepLogs := []string{
		"find-radio-" + logID + "-01",
		"bounds-" + logID,
		"focus-theme-window-" + logID,
		"click-theme-option-" + logID,
	}
	ref, ok := s.findRadio(logID, index)
	if !ok {
		return themeLogEntry{}, false, "", elementBounds{}, stepLogs
	}
	bounds, ok := s.elementBounds(logID, ref)
	if !ok {
		return themeLogEntry{}, false, "", elementBounds{}, stepLogs
	}
	if !s.run(StepSpec{
		Name:    "focus-theme-window-" + logID,
		Command: "agent-desktop",
		Args:    []string{"focus-window", "--window-id", s.windowIDValue},
		Timeout: 60 * time.Second,
	}) {
		return themeLogEntry{}, false, "", bounds, stepLogs
	}
	x := int(bounds.X + bounds.Width/2)
	y := int(bounds.Y + bounds.Height/2)
	coords := fmt.Sprintf("%d,%d", x, y)
	if !s.run(StepSpec{
		Name:    "click-theme-option-" + logID,
		Command: "agent-desktop",
		Args:    []string{"--headed", "mouse-click", "--xy", coords},
		Timeout: 60 * time.Second,
	}) {
		return themeLogEntry{}, false, coords, bounds, stepLogs
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
				return last, true, coords, bounds, stepLogs
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return themeLogEntry{}, false, coords, bounds, stepLogs
}

// ensureNoAnvilClient closes a stale Anvil process before a launch so the
// window identity and bounds always belong to the new run.
func (s *shellNative) ensureNoAnvilClient(label string) bool {
	listName := "preclean-" + label + "-list"
	if !s.run(StepSpec{
		Name:    listName,
		Command: "agent-desktop",
		Args:    []string{"list-windows", "--app", "anvil.exe"},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	body, _ := s.exec.read(listName)
	windows, ok := parseWindows(body)
	if !ok {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "could not parse the pre-launch window list")
		return false
	}
	if len(windows) == 0 {
		return true
	}
	if !s.run(StepSpec{
		Name:    "preclean-" + label + "-close",
		Command: "agent-desktop",
		Args:    []string{"close-app", "anvil.exe", "--force"},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	time.Sleep(300 * time.Millisecond)
	return s.run(StepSpec{
		Name:                 "preclean-" + label + "-empty",
		Command:              "agent-desktop",
		Args:                 []string{"list-windows", "--app", "anvil.exe"},
		Timeout:              60 * time.Second,
		ExpectOutputContains: `"data":[]`,
	})
}

// verifyWindowIdentity confirms the window still belongs to the process this
// case launched, then refreshes the bounds used for the next click.
func (s *shellNative) verifyWindowIdentity(label string) bool {
	stepName := "window-identity-" + label
	if !s.run(StepSpec{
		Name:    stepName,
		Command: "agent-desktop",
		Args:    []string{"list-windows", "--app", "anvil.exe"},
		Timeout: 60 * time.Second,
	}) {
		return false
	}
	body, _ := s.exec.read(stepName)
	windows, ok := parseWindows(body)
	if !ok || len(windows) == 0 {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations, "no Anvil window while verifying the active process")
		return false
	}
	window := windows[0]
	if window.ProcessInstance != s.processInstance || window.PID != s.windowPID {
		s.out.Status = StatusFail
		s.out.Limitations = append(s.out.Limitations,
			fmt.Sprintf("window identity changed: expected pid=%d instance=%s, got pid=%d instance=%s",
				s.windowPID, s.processInstance, window.PID, window.ProcessInstance))
		return false
	}
	s.window = window
	s.windowIDValue = window.ID
	return true
}

// recordThemeClickFailure keeps the raw diagnosis for an unsuccessful click.
func (s *shellNative) recordThemeClickFailure(attemptID string, index int, expectedMode, coords string, bounds elementBounds, stepLogs []string) {
	appLog, _ := os.ReadFile(s.appLog)
	logs := map[string]string{}
	for _, name := range stepLogs {
		if body, err := s.exec.read(name); err == nil && len(body) > 0 {
			logs[name] = lastLines(string(body), 80)
		}
	}
	payload := map[string]any{
		"attempt":          attemptID,
		"optionIndex":      index,
		"expectedMode":     expectedMode,
		"processInstance":  s.processInstance,
		"pid":              s.windowPID,
		"windowBounds":     s.window,
		"elementBounds":    bounds,
		"clickCoordinates": coords,
		"appLogTail":       lastLines(string(appLog), 40),
		"agentDesktopLogs": logs,
	}
	if artifact, err := writeJSONArtifact(s.runner.caseDir, "native/theme-failure-"+attemptID+".json", payload); err == nil {
		s.out.Artifacts = append(s.out.Artifacts, artifact)
	}
}
