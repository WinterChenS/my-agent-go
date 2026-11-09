package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/preflight"
)

// nativeRun is the real desktop case: build and launch the Wails client, read
// the window through agent-desktop, capture a screenshot, close it and prove
// the process is gone. Missing desktop prerequisites are BLOCKED, not FAIL.
func nativeRun(r automationRunner, sha, target string) nativeOutcome {
	var (
		out   nativeOutcome
		steps = func(specs ...StepSpec) bool {
			for _, spec := range specs {
				cmd, artifact, failure := r.runStep(spec)
				out.Commands = append(out.Commands, cmd)
				if artifact.Path != "" {
					out.Artifacts = append(out.Artifacts, artifact)
				}
				if failure != "" {
					out.Limitations = append(out.Limitations, failure)
					return false
				}
			}
			return true
		}
		blocked = func(format string, args ...any) nativeOutcome {
			out.Status = StatusBlocked
			out.Limitations = append(out.Limitations, fmt.Sprintf(format, args...))
			return out
		}
	)

	nativeDir := filepath.Join(r.caseDir, "native")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		return blocked("create native evidence directory: %v", err)
	}

	shortSHA := sha
	if len(shortSHA) > 12 {
		shortSHA = shortSHA[:12]
	}
	dataDir := filepath.Join(r.repo, ".tmp", "test", "anvil-data", shortSHA+"-"+target)
	webviewDir := filepath.Join(r.repo, ".tmp", "test", "webview2", shortSHA+"-"+target)
	for _, dir := range []string{dataDir, webviewDir} {
		if err := resetDirectory(dir); err != nil {
			return blocked("prepare %q on D:: %v", dir, err)
		}
	}

	// Desktop automation must be available and permitted before anything is
	// launched; otherwise the case is BLOCKED, not a product failure.
	if !steps(StepSpec{
		Name:    "agent-desktop-status",
		Command: "agent-desktop",
		Args:    []string{"status"},
		Timeout: 60 * time.Second,
	}) {
		out.Status = StatusBlocked
		return out
	}
	statusBody, _ := os.ReadFile(filepath.Join(r.caseDir, "logs", "agent-desktop-status.log"))
	if err := checkAgentDesktopStatus(statusBody); err != nil {
		return blocked("%v", err)
	}

	if !steps(
		StepSpec{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
		StepSpec{Name: "go-build", Command: "go", Args: []string{"build", "-o", filepath.Join(r.repo, ".tmp", "bin", "anvil.exe"), "./cmd/anvil"}, Timeout: 20 * time.Minute},
	) {
		out.Status = StatusFail
		return out
	}
	if !steps(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + webviewDir, "--create"}},
	) {
		return blocked("native data directories are not writable on D:: %s", strings.Join(out.Limitations, "; "))
	}

	exe := filepath.Join(r.repo, ".tmp", "bin", "anvil.exe")
	if _, err := os.Stat(exe); err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("built client %q is missing: %v", exe, err))
		return out
	}
	dist := filepath.Join(r.repo, "frontend", "dist")
	launchArgs := []string{
		"launch", exe, "--no-attach",
		"--env", "ANVIL_DATA_DIR=" + dataDir,
		"--env", "ANVIL_WEBVIEW_DATA_DIR=" + webviewDir,
		"--env", "ANVIL_FRONTEND_DIST=" + dist,
		"--timeout", "30000",
	}
	if !steps(StepSpec{
		Name:                 "launch",
		Command:              "agent-desktop",
		Args:                 launchArgs,
		Timeout:              90 * time.Second,
		ExpectOutputContains: `"title":"Anvil"`,
	}) {
		return nativeFailWithCleanup(r, out, dataDir)
	}

	if !steps(
		StepSpec{Name: "wait-window", Command: "agent-desktop", Args: []string{"wait", "--window", "Anvil", "--app", "anvil.exe", "--timeout", "30000"}, Timeout: 60 * time.Second},
		StepSpec{Name: "list-windows", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second},
	) {
		return nativeFailWithCleanup(r, out, dataDir)
	}

	windowID, windowTitle, err := firstWindow(filepath.Join(r.caseDir, "logs", "list-windows.log"))
	if err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, err.Error())
		return nativeFailWithCleanup(r, out, dataDir)
	}
	if windowTitle != "Anvil" {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("window title = %q, want Anvil", windowTitle))
		return nativeFailWithCleanup(r, out, dataDir)
	}
	if artifact, err := copyArtifact(r.caseDir, "logs/list-windows.log", "native/windows.json"); err == nil {
		out.Artifacts = append(out.Artifacts, artifact)
	}

	snapshot := StepSpec{
		Name:                 "snapshot",
		Command:              "agent-desktop",
		Args:                 []string{"snapshot", "--app", "anvil.exe", "--window-id", windowID, "--timeout-ms", "30000", "--compact"},
		Timeout:              90 * time.Second,
		ExpectOutputContains: `"complete":true`,
	}
	if !steps(snapshot) {
		return nativeFailWithCleanup(r, out, dataDir)
	}

	// WebView2 exposes its document tree only after the first accessibility
	// walk; poll the marker so a cold Chromium tree is not mistaken for a
	// missing React bundle.
	markerFound := false
	for attempt := 1; attempt <= 15; attempt++ {
		cmd, artifact, failure := r.runStep(StepSpec{
			Name:                 fmt.Sprintf("find-build-marker-%02d", attempt),
			Command:              "agent-desktop",
			Args:                 []string{"find", "--text", "anvil-rebuild-shell", "--app", "anvil.exe"},
			Timeout:              60 * time.Second,
			ExpectOutputContains: `"name":"anvil-rebuild-shell"`,
		})
		out.Commands = append(out.Commands, cmd)
		if artifact.Path != "" {
			out.Artifacts = append(out.Artifacts, artifact)
		}
		if failure == "" {
			markerFound = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !markerFound {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "frontend build marker was not found in the accessibility tree after 15 attempts")
		return nativeFailWithCleanup(r, out, dataDir)
	}

	screenshotPath := filepath.Join(nativeDir, "screenshot.png")
	screenshot := StepSpec{
		Name:    "screenshot",
		Command: "agent-desktop",
		Args:    []string{"screenshot", "--app", "anvil.exe", "--window-id", windowID, screenshotPath},
		Timeout: 60 * time.Second,
	}
	if !steps(screenshot) {
		return nativeFailWithCleanup(r, out, dataDir)
	}
	info, statErr := os.Stat(screenshotPath)
	if statErr != nil || info.Size() < 1024 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("screenshot %q missing or too small (size=%d, err=%v)", screenshotPath, infoSize(info), statErr))
		return nativeFailWithCleanup(r, out, dataDir)
	}
	if artifact, err := hashArtifact(r.caseDir, "native/screenshot.png"); err == nil {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("hash screenshot: %v", err))
		return nativeFailWithCleanup(r, out, dataDir)
	}

	closed := steps(
		StepSpec{Name: "close-app", Command: "agent-desktop", Args: []string{"close-app", "anvil.exe"}, Timeout: 60 * time.Second},
		StepSpec{Name: "list-windows-after-close", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second, ExpectOutputContains: `"data":[]`},
	)
	if !closed {
		out.Status = StatusFail
		return out
	}

	if artifact, ok := copyAppLog(r, dataDir); ok {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "application log was not produced; startup evidence is incomplete")
		return out
	}

	out.Status = StatusPass
	return out
}

// nativeFailWithCleanup closes any leftover process, records the cleanup
// command and returns a FAIL outcome.
func nativeFailWithCleanup(r automationRunner, out nativeOutcome, dataDir string) nativeOutcome {
	if artifact, ok := copyAppLog(r, dataDir); ok {
		out.Artifacts = append(out.Artifacts, artifact)
	}
	cmd, artifact, failure := r.runStep(StepSpec{
		Name:    "cleanup-close-app",
		Command: "agent-desktop",
		Args:    []string{"close-app", "anvil.exe", "--force"},
		Timeout: 60 * time.Second,
	})
	out.Commands = append(out.Commands, cmd)
	if artifact.Path != "" {
		out.Artifacts = append(out.Artifacts, artifact)
	}
	if failure != "" && len(out.Limitations) == 0 {
		out.Limitations = append(out.Limitations, failure)
	}
	out.Status = StatusFail
	return out
}

func infoSize(info os.FileInfo) int64 {
	if info == nil {
		return -1
	}
	return info.Size()
}

func copyArtifact(caseDir, rel, dstRel string) (Artifact, error) {
	source, err := os.ReadFile(filepath.Join(caseDir, filepath.FromSlash(rel)))
	if err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(caseDir, filepath.FromSlash(dstRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, err
	}
	if err := os.WriteFile(path, source, 0o644); err != nil {
		return Artifact{}, err
	}
	return hashArtifact(caseDir, dstRel)
}

func copyAppLog(r automationRunner, dataDir string) (Artifact, bool) {
	source := filepath.Join(dataDir, "logs", "anvil.log")
	body, err := os.ReadFile(source)
	if err != nil || len(body) == 0 {
		return Artifact{}, false
	}
	path := filepath.Join(r.caseDir, "native", "anvil.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, false
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return Artifact{}, false
	}
	artifact, err := hashArtifact(r.caseDir, "native/anvil.log")
	if err != nil {
		return Artifact{}, false
	}
	return artifact, true
}

// firstWindow parses `agent-desktop list-windows` output.
func firstWindow(logPath string) (id, title string, err error) {
	body, err := os.ReadFile(logPath)
	if err != nil {
		return "", "", fmt.Errorf("read list-windows output: %w", err)
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Visible   bool   `json:"visible"`
			Access    bool   `json:"accessible"`
			Minimized bool   `json:"minimized"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil {
		return "", "", fmt.Errorf("parse list-windows output: %w", err)
	}
	if !envelope.OK {
		return "", "", fmt.Errorf("list-windows did not report ok")
	}
	if len(envelope.Data) == 0 {
		return "", "", fmt.Errorf("no Anvil window was reported")
	}
	win := envelope.Data[0]
	if !win.Visible || win.Minimized || !win.Access {
		return "", "", fmt.Errorf("window %q visible=%v minimized=%v accessible=%v; want a visible, accessible window", win.Title, win.Visible, win.Minimized, win.Access)
	}
	return win.ID, win.Title, nil
}

// checkAgentDesktopStatus requires a usable accessibility surface.
func checkAgentDesktopStatus(body []byte) error {
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Platform    string `json:"platform"`
			Permissions struct {
				Accessibility struct {
					State string `json:"state"`
				} `json:"accessibility"`
			} `json:"permissions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err != nil {
		return fmt.Errorf("parse agent-desktop status: %w", err)
	}
	if !envelope.OK {
		return fmt.Errorf("agent-desktop status did not report ok")
	}
	if envelope.Data.Platform != "windows" {
		return fmt.Errorf("agent-desktop platform = %q, want windows", envelope.Data.Platform)
	}
	if envelope.Data.Permissions.Accessibility.State != "granted" {
		return fmt.Errorf("agent-desktop accessibility permission = %q, want granted", envelope.Data.Permissions.Accessibility.State)
	}
	return nil
}

// lastJSONObject tolerates application log lines before the JSON envelope
// (the launched client inherits agent-desktop's stdout).
func lastJSONObject(body []byte) []byte {
	text := strings.TrimSpace(string(body))
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		candidate := text[i:]
		var probe any
		dec := json.NewDecoder(strings.NewReader(candidate))
		if err := dec.Decode(&probe); err == nil {
			return []byte(candidate)
		}
	}
	return []byte(text)
}

func resetDirectory(path string) error {
	if _, err := preflight.CheckPath(path, preflight.OSSystem{}); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(path)
}
