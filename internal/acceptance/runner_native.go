package acceptance

// nativeOutcome is the result of the real desktop (agent-desktop) case.
type nativeOutcome struct {
	Status      string
	Commands    []Command
	Artifacts   []Artifact
	Limitations []string
}

// runNativeWindowCase launches the built Wails client, observes it through
// agent-desktop and closes it again. The implementation lives in
// runner_native.go so the automation runner stays independent of desktop
// automation details.
func runNativeWindowCase(r automationRunner, sha, target string) nativeOutcome {
	return nativeRun(r, sha, target)
}
