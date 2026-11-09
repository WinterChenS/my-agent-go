package acceptance

import "os"

// desktopStepRunner is the seam between the desktop case orchestration and
// command execution. Production uses automationRunner; tests inject a fake so
// environment classification is covered without a real window.
type desktopStepRunner interface {
	run(step StepSpec) (Command, Artifact, string)
	read(stepName string) ([]byte, error)
}

// nativeHooks lets tests replace desktop execution and the state-directory ACL
// preparation. Production leaves them nil.
type nativeHooks struct {
	desktop     desktopStepRunner
	prepareHome func(exec desktopStepRunner, repo, home string) ([]Command, []Artifact, string)
}

// nativeOutcome is the result of the real desktop (agent-desktop) case.
type nativeOutcome struct {
	Status      string
	Commands    []Command
	Artifacts   []Artifact
	Limitations []string
}

// runNativeWindowCase launches the built Wails client, observes it through
// agent-desktop and closes it again. The implementation lives in
// runner_native_desktop.go so the automation runner stays independent of
// desktop automation details.
func runNativeWindowCase(r automationRunner, sha, target string, hooks *nativeHooks) nativeOutcome {
	return nativeRun(r, sha, target, hooks)
}

func (r automationRunner) run(step StepSpec) (Command, Artifact, string) {
	return r.runStep(step)
}

func (r automationRunner) read(stepName string) ([]byte, error) {
	return os.ReadFile(r.logPath(stepName))
}
