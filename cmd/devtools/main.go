// Command devtools is the single entry point for verification and gates.
//
// Exit codes follow docs/adr/0001-devtools-exit-code-fidelity.md:
// 0 pass, 1 assertion failure (valid evidence reports FAIL), 2 incomplete
// input, missing evidence, configuration error or BLOCKED/NOT_RUN.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"my-agent-go/internal/acceptance"
	"my-agent-go/internal/preflight"
)

const usage = `usage:
  devtools verify --case=<id> --target=<target> [--evidence=<abs path>] [--matrix=<path>] [--sha=<sha>]
  devtools gate --id=<gate> --target=<target> --evidence=<abs path> [--matrix=<path>] [--sha=<sha>]
  devtools preflight --path=<path> [--create]
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return acceptance.ExitIncomplete
	}
	switch args[0] {
	case "gate":
		return runGate(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "preflight":
		return runPreflight(args[1:])
	case "version":
		fmt.Println("devtools (Anvil rebuild)")
		return acceptance.ExitPass
	case "-h", "--help", "help":
		fmt.Print(usage)
		return acceptance.ExitPass
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return acceptance.ExitIncomplete
	}
}

type gateReport struct {
	Gate     string            `json:"gate"`
	Target   string            `json:"target"`
	GitSHA   string            `json:"gitSHA"`
	ExitCode int               `json:"exitCode"`
	Passed   []string          `json:"passed"`
	Statuses map[string]string `json:"statuses"`
	Problems []string          `json:"problems"`
}

func runGate(args []string) int {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	id := fs.String("id", "", "gate id, e.g. G0")
	target := fs.String("target", "", "target id, e.g. win-x64")
	evidence := fs.String("evidence", "", "absolute evidence root on D:")
	matrix := fs.String("matrix", "", "matrix path (default tests/acceptance/rebuild.json)")
	sha := fs.String("sha", "", "commit SHA (default: git HEAD)")
	if err := fs.Parse(args); err != nil {
		return acceptance.ExitIncomplete
	}

	repo, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate: %v\n", err)
		return acceptance.ExitIncomplete
	}
	matrixPath := *matrix
	if matrixPath == "" {
		matrixPath = filepath.Join(repo, "tests", "acceptance", "rebuild.json")
	}
	commit := strings.TrimSpace(*sha)
	if commit == "" {
		commit, err = gitHEAD(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gate: %v\n", err)
			return acceptance.ExitIncomplete
		}
	}

	out := acceptance.EvaluateGate(acceptance.GateOptions{
		MatrixPath:   matrixPath,
		EvidenceRoot: *evidence,
		GateID:       *id,
		Target:       *target,
		GitSHA:       commit,
	})
	report := gateReport{
		Gate:     *id,
		Target:   *target,
		GitSHA:   commit,
		ExitCode: out.Code,
		Passed:   out.Passed,
		Statuses: out.Statuses,
		Problems: out.Problems,
	}
	if report.Passed == nil {
		report.Passed = []string{}
	}
	if report.Statuses == nil {
		report.Statuses = map[string]string{}
	}
	if report.Problems == nil {
		report.Problems = []string{}
	}
	writeJSON(os.Stdout, report)
	if out.Code != acceptance.ExitPass {
		fmt.Fprintf(os.Stderr, "gate %s/%s: exit %d (%d problem(s))\n", *id, *target, out.Code, len(out.Problems))
	}
	return out.Code
}

type verifyReport struct {
	CaseID   string   `json:"caseId"`
	Target   string   `json:"target"`
	GitSHA   string   `json:"gitSHA"`
	ExitCode int      `json:"exitCode"`
	Status   string   `json:"status"`
	CaseDir  string   `json:"caseDir"`
	Problems []string `json:"problems"`
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	caseID := fs.String("case", "", "case id, e.g. R0-00-build-gate")
	target := fs.String("target", "", "target id, e.g. win-x64")
	evidence := fs.String("evidence", "", "absolute evidence root (default ANVIL_EVIDENCE_ROOT)")
	matrix := fs.String("matrix", "", "matrix path (default tests/acceptance/rebuild.json)")
	sha := fs.String("sha", "", "commit SHA (default: git HEAD)")
	if err := fs.Parse(args); err != nil {
		return acceptance.ExitIncomplete
	}

	repo, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return acceptance.ExitIncomplete
	}
	matrixPath := *matrix
	if matrixPath == "" {
		matrixPath = filepath.Join(repo, "tests", "acceptance", "rebuild.json")
	}
	evidenceRoot := strings.TrimSpace(*evidence)
	if evidenceRoot == "" {
		evidenceRoot = strings.TrimSpace(os.Getenv("ANVIL_EVIDENCE_ROOT"))
	}
	if evidenceRoot == "" {
		fmt.Fprintln(os.Stderr, "verify: evidence root is required (pass --evidence or set ANVIL_EVIDENCE_ROOT)")
		return acceptance.ExitIncomplete
	}
	commit := strings.TrimSpace(*sha)
	if commit == "" {
		commit, err = gitHEAD(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify: %v\n", err)
			return acceptance.ExitIncomplete
		}
		// Evidence must describe the committed tree, not a work in progress.
		dirty, dirtyErr := gitDirty(repo)
		if dirtyErr != nil {
			fmt.Fprintf(os.Stderr, "verify: %v\n", dirtyErr)
			return acceptance.ExitIncomplete
		}
		if dirty {
			fmt.Fprintln(os.Stderr, "verify: working tree has tracked changes; commit first so evidence binds to the git SHA")
			return acceptance.ExitIncomplete
		}
	}

	out := acceptance.RunCase(acceptance.VerifyOptions{
		RepoRoot:     repo,
		MatrixPath:   matrixPath,
		EvidenceRoot: evidenceRoot,
		CaseID:       *caseID,
		Target:       *target,
		GitSHA:       commit,
	})
	report := verifyReport{
		CaseID:   *caseID,
		Target:   *target,
		GitSHA:   commit,
		ExitCode: out.Code,
		Status:   out.Status,
		CaseDir:  out.CaseDir,
		Problems: out.Problems,
	}
	if report.Problems == nil {
		report.Problems = []string{}
	}
	writeJSON(os.Stdout, report)
	if out.Code != acceptance.ExitPass {
		fmt.Fprintf(os.Stderr, "verify %s/%s: exit %d status %s\n", *caseID, *target, out.Code, out.Status)
	}
	return out.Code
}

type preflightReport struct {
	Path     string `json:"path"`
	Existing string `json:"existing"`
	Resolved string `json:"resolved"`
	Volume   string `json:"volume"`
	Created  bool   `json:"created"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

func runPreflight(args []string) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	path := fs.String("path", "", "path to verify")
	create := fs.Bool("create", false, "create missing directories")
	if err := fs.Parse(args); err != nil {
		return acceptance.ExitIncomplete
	}
	if strings.TrimSpace(*path) == "" {
		fmt.Fprintln(os.Stderr, "preflight: --path is required")
		return acceptance.ExitIncomplete
	}
	report := preflightReport{Path: *path}
	var res preflight.Resolution
	var err error
	if *create {
		res, err = preflight.EnsureDir(*path, preflight.OSSystem{})
	} else {
		res, err = preflight.CheckPath(*path, preflight.OSSystem{})
	}
	report.Existing = res.Existing
	report.Resolved = res.Resolved
	report.Volume = res.Volume
	report.Created = *create
	if err != nil {
		report.Error = err.Error()
		writeJSON(os.Stdout, report)
		fmt.Fprintf(os.Stderr, "preflight: %v\n", err)
		return acceptance.ExitIncomplete
	}
	report.OK = true
	writeJSON(os.Stdout, report)
	return acceptance.ExitPass
}

func writeJSON(w *os.File, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find repo root (go.mod) above the working directory")
		}
		dir = parent
	}
}

func gitHEAD(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	sha := strings.TrimSpace(string(out))
	if len(sha) != 40 {
		return "", fmt.Errorf("git rev-parse HEAD returned %q", sha)
	}
	return sha, nil
}

func gitDirty(repo string) (bool, error) {
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}
