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
func nativeRun(r automationRunner, sha, target string, hooks *nativeHooks) nativeOutcome {
	out := nativeOutcome{}
	var exec desktopStepRunner = r
	if hooks != nil && hooks.desktop != nil {
		exec = hooks.desktop
	}

	blocked := func(format string, args ...any) nativeOutcome {
		out.Status = StatusBlocked
		out.Limitations = append(out.Limitations, withReverify(fmt.Sprintf(format, args...)))
		return out
	}
	markBlocked := func() {
		out.Status = StatusBlocked
		out.Limitations = []string{withReverify(strings.Join(out.Limitations, "; "))}
	}

	// run executes steps in order and classifies the first failure. Desktop
	// environment failures (DACL, permissions, internal CLI errors) become
	// BLOCKED; product assertions (no window, wrong title, missing marker,
	// small screenshot) stay FAIL.
	run := func(specs ...StepSpec) bool {
		for _, spec := range specs {
			cmd, artifact, failure := exec.run(spec)
			out.Commands = append(out.Commands, cmd)
			if artifact.Path != "" {
				out.Artifacts = append(out.Artifacts, artifact)
			}
			if failure == "" {
				continue
			}
			body, _ := exec.read(spec.Name)
			if blocked, _ := isBlockedDesktopFailure(spec.Command, failure, body); blocked {
				out.Status = StatusBlocked
				out.Limitations = append(out.Limitations, withReverify(desktopFailureDetail(spec.Command, failure, body)))
				return false
			}
			out.Status = StatusFail
			out.Limitations = append(out.Limitations, failure)
			return false
		}
		return true
	}

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

	// agent-desktop keeps private session state. On Windows the default
	// inherited ACL grants untrusted principals, which makes private-file
	// checks fail later; tighten it before any read or write.
	home := agentDesktopHome(r.repo)
	var (
		prepCommands  []Command
		prepArtifacts []Artifact
		prepFailure   string
	)
	if hooks != nil && hooks.prepareHome != nil {
		prepCommands, prepArtifacts, prepFailure = hooks.prepareHome(exec, r.repo, home)
	} else {
		prepCommands, prepArtifacts, prepFailure = prepareAgentDesktopHome(exec, r.repo, home)
	}
	out.Commands = append(out.Commands, prepCommands...)
	out.Artifacts = append(out.Artifacts, prepArtifacts...)
	if prepFailure != "" {
		return blocked("%s", prepFailure)
	}

	// Desktop automation must be available and permitted before anything is
	// launched. A failing `agent-desktop status` is always environmental.
	if !run(StepSpec{
		Name:    "agent-desktop-status",
		Command: "agent-desktop",
		Args:    []string{"status"},
		Timeout: 60 * time.Second,
	}) {
		markBlocked()
		return out
	}
	statusBody, _ := exec.read("agent-desktop-status")
	if err := checkAgentDesktopStatus(statusBody); err != nil {
		return blocked("%v", err)
	}

	if !run(
		StepSpec{Name: "npm-build", Command: "npm", Args: []string{"run", "build"}, Dir: "frontend", Timeout: 10 * time.Minute},
		StepSpec{Name: "go-build", Command: "go", Args: []string{"build", "-o", filepath.Join(r.repo, ".tmp", "bin", "anvil.exe"), "./cmd/anvil"}, Timeout: 20 * time.Minute},
	) {
		return out
	}
	if !run(
		StepSpec{Name: "path-data", Command: "self", Args: []string{"preflight", "--path=" + dataDir, "--create"}},
		StepSpec{Name: "path-webview", Command: "self", Args: []string{"preflight", "--path=" + webviewDir, "--create"}},
	) {
		// The D:-only rule is an environment constraint.
		markBlocked()
		return out
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
	if !run(StepSpec{
		Name:                 "launch",
		Command:              "agent-desktop",
		Args:                 launchArgs,
		Timeout:              90 * time.Second,
		ExpectOutputContains: `"title":"Anvil"`,
	}) {
		if out.Status == StatusFail {
			return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
		}
		return out
	}

	if !run(
		StepSpec{Name: "wait-window", Command: "agent-desktop", Args: []string{"wait", "--window", "Anvil", "--app", "anvil.exe", "--timeout", "30000"}, Timeout: 60 * time.Second},
		StepSpec{Name: "list-windows", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second},
	) {
		if out.Status == StatusFail {
			return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
		}
		return out
	}

	windowsBody, _ := exec.read("list-windows")
	windowID, windowTitle, err := firstWindow(windowsBody)
	if err != nil {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, err.Error())
		return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
	}
	if windowTitle != "Anvil" {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("window title = %q, want Anvil", windowTitle))
		return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
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
	if !run(snapshot) {
		if out.Status == StatusFail {
			return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
		}
		return out
	}

	// WebView2 exposes its document tree only after the first accessibility
	// walk; poll the marker so a cold Chromium tree is not mistaken for a
	// missing React bundle.
	markerFound := false
	for attempt := 1; attempt <= 15; attempt++ {
		name := fmt.Sprintf("find-build-marker-%02d", attempt)
		cmd, artifact, failure := exec.run(StepSpec{
			Name:                 name,
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
		body, _ := exec.read(name)
		if blocked, _ := isBlockedDesktopFailure("agent-desktop", failure, body); blocked {
			out.Status = StatusBlocked
			out.Limitations = append(out.Limitations, withReverify(desktopFailureDetail("agent-desktop", failure, body)))
			return out
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !markerFound {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, "frontend build marker was not found in the accessibility tree after 15 attempts")
		return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
	}

	screenshotPath := filepath.Join(nativeDir, "screenshot.png")
	screenshot := StepSpec{
		Name:    "screenshot",
		Command: "agent-desktop",
		Args:    []string{"screenshot", "--app", "anvil.exe", "--window-id", windowID, screenshotPath},
		Timeout: 60 * time.Second,
	}
	if !run(screenshot) {
		if out.Status == StatusFail {
			return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
		}
		return out
	}
	info, statErr := os.Stat(screenshotPath)
	if statErr != nil || info.Size() < 1024 {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("screenshot %q missing or too small (size=%d, err=%v)", screenshotPath, infoSize(info), statErr))
		return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
	}
	if artifact, err := hashArtifact(r.caseDir, "native/screenshot.png"); err == nil {
		out.Artifacts = append(out.Artifacts, artifact)
	} else {
		out.Status = StatusFail
		out.Limitations = append(out.Limitations, fmt.Sprintf("hash screenshot: %v", err))
		return nativeFailWithCleanup(exec, out, r.caseDir, dataDir)
	}

	if !run(
		StepSpec{Name: "close-app", Command: "agent-desktop", Args: []string{"close-app", "anvil.exe"}, Timeout: 60 * time.Second},
		StepSpec{Name: "list-windows-after-close", Command: "agent-desktop", Args: []string{"list-windows", "--app", "anvil.exe"}, Timeout: 60 * time.Second, ExpectOutputContains: `"data":[]`},
	) {
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
// command and returns the outcome as FAIL.
func nativeFailWithCleanup(exec desktopStepRunner, out nativeOutcome, caseDir, dataDir string) nativeOutcome {
	if artifact, ok := copyAppLog(automationRunner{caseDir: caseDir}, dataDir); ok {
		out.Artifacts = append(out.Artifacts, artifact)
	}
	cmd, artifact, failure := exec.run(StepSpec{
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

func agentDesktopHome(repo string) string {
	return filepath.Join(repo, ".tmp", "tooling", "agent-desktop-home")
}

// agentDesktopHomePreparationSteps describes how the state directory is made
// safe before agent-desktop runs. Path validation comes first; on Windows the
// ACL step removes inherited ACEs and grants only the current user, SYSTEM and
// Administrators.
func agentDesktopHomePreparationSteps(home string) ([]StepSpec, error) {
	steps := []StepSpec{{
		Name:    "path-agent-desktop-home",
		Command: "self",
		Args:    []string{"preflight", "--path=" + home, "--create"},
	}}
	args, err := agentDesktopACLArgs(home)
	if err != nil {
		return nil, err
	}
	if args != nil {
		steps = append(steps, StepSpec{
			Name:    "agent-desktop-home-acl",
			Command: "icacls",
			Args:    args,
			Timeout: 2 * time.Minute,
		})
	}
	return steps, nil
}

// prepareAgentDesktopHome makes the agent-desktop state directory exist on D:
// and tightens its ACL. agent-desktop refuses to use a private state file whose
// DACL grants an untrusted principal, which a fresh worktree inherits from
// .tmp; without this step the native case cannot pass on a clean clone.
func prepareAgentDesktopHome(exec desktopStepRunner, repo, home string) ([]Command, []Artifact, string) {
	var (
		commands  []Command
		artifacts []Artifact
	)
	steps, err := agentDesktopHomePreparationSteps(home)
	if err != nil {
		return commands, artifacts, "build agent-desktop ACL command: " + err.Error()
	}
	for _, step := range steps {
		cmd, artifact, failure := exec.run(step)
		if artifact.Path != "" {
			artifacts = append(artifacts, artifact)
		}
		commands = append(commands, cmd)
		if failure == "" {
			continue
		}
		body, _ := exec.read(step.Name)
		if step.Command == "icacls" {
			return commands, artifacts, "agent-desktop home ACL normalisation failed: " + desktopFailureDetail("icacls", failure, body)
		}
		return commands, artifacts, "agent-desktop home is not writable on D:: " + desktopFailureDetail("self", failure, body)
	}
	return commands, artifacts, ""
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
func firstWindow(body []byte) (id, title string, err error) {
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

// isBlockedDesktopFailure classifies a failed step.
func isBlockedDesktopFailure(command, failure string, body []byte) (bool, string) {
	if command == "icacls" {
		return true, strings.TrimSpace(failure)
	}
	return classifyDesktopFailure(failure, body)
}
