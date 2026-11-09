package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"my-agent-go/internal/preflight"
)

// EvaluateGate checks matrix and evidence completeness, then decides the gate.
func EvaluateGate(opts GateOptions) GateOutcome {
	out := GateOutcome{Code: ExitIncomplete, Statuses: map[string]string{}}
	problem := func(format string, args ...any) {
		out.Problems = append(out.Problems, fmt.Sprintf(format, args...))
	}

	m, err := LoadMatrix(opts.MatrixPath)
	if err != nil {
		problem("%v", err)
		return out
	}

	if strings.TrimSpace(opts.GateID) == "" {
		problem("gate id is empty")
		return out
	}
	var gate *Gate
	for i := range m.Gates {
		if m.Gates[i].ID == opts.GateID {
			gate = &m.Gates[i]
			break
		}
	}
	if gate == nil {
		problem("gate %q is not registered in the matrix", opts.GateID)
		return out
	}
	if strings.TrimSpace(opts.Target) == "" {
		problem("target is empty")
		return out
	}
	if err := validateIdentifier(opts.Target, "target"); err != nil {
		problem("%v", err)
		return out
	}
	if !containsString(gate.Targets, opts.Target) {
		problem("target %q is not declared for gate %q (declared: %s)",
			opts.Target, gate.ID, strings.Join(gate.Targets, ", "))
		return out
	}

	sha := normalizeSHA(opts.GitSHA)
	if !isHexSHA(sha) {
		problem("gate commit %q is not a full 40-character git SHA", opts.GitSHA)
		return out
	}

	root, err := checkEvidenceRoot(opts)
	if err != nil {
		problem("%v", err)
		return out
	}

	// Every case declaration must be safe to turn into a path segment, and no
	// case may be registered twice.
	declared := make(map[string]Case, len(gate.Cases))
	for _, c := range gate.Cases {
		if err := validateMatrixCase(c); err != nil {
			problem("%v", err)
			return out
		}
		if _, dup := declared[c.ID]; dup {
			problem("duplicate case id %q in gate %q", c.ID, gate.ID)
			return out
		}
		declared[c.ID] = c
	}

	// A required task without registered cases stays pending: the gate cannot
	// pass just because the task was left out of the matrix.
	taskHasCases := make(map[string]bool, len(gate.RequiredTasks))
	for _, c := range gate.Cases {
		taskHasCases[c.Task] = true
	}
	for _, task := range gate.RequiredTasks {
		if !taskHasCases[task] {
			problem("required task %q has no registered cases", task)
		}
	}
	required := make([]Case, 0, len(gate.Cases))
	for _, c := range gate.Cases {
		if c.Required {
			required = append(required, c)
		}
	}
	if len(required) == 0 {
		problem("gate %q has no required cases", gate.ID)
		return out
	}

	hasFail := false
	for _, c := range required {
		status, err := evaluateCase(root, sha, opts.Target, c, opts.Resolver)
		if err != nil {
			problem("case %q: %v", c.ID, err)
			out.Statuses[c.ID] = "INVALID"
			continue
		}
		out.Statuses[c.ID] = status
		switch status {
		case StatusPass:
			out.Passed = append(out.Passed, c.ID)
		case StatusFail:
			hasFail = true
		case StatusBlocked, StatusNotRun:
			problem("case %q is %s: gate stays closed", c.ID, status)
		}
	}

	if len(out.Problems) > 0 {
		return out
	}
	if hasFail {
		out.Code = ExitAssertion
		return out
	}
	out.Code = ExitPass
	return out
}

func checkEvidenceRoot(opts GateOptions) (string, error) {
	if strings.TrimSpace(opts.EvidenceRoot) == "" {
		return "", fmt.Errorf("evidence root is empty")
	}
	if !filepath.IsAbs(opts.EvidenceRoot) {
		return "", fmt.Errorf("evidence root %q must be an absolute D: path", opts.EvidenceRoot)
	}
	resolver := opts.Resolver
	if resolver == nil {
		resolver = preflight.OSSystem{}
	}
	res, err := preflight.CheckPath(opts.EvidenceRoot, resolver)
	if err != nil {
		return "", fmt.Errorf("evidence root: %w", err)
	}
	info, statErr := os.Stat(res.Resolved)
	if statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("evidence root %q does not exist or is not a directory", res.Resolved)
	}
	return res.Resolved, nil
}

func evaluateCase(evidenceRoot, sha, target string, c Case, resolver preflight.Resolver) (string, error) {
	caseDir := filepath.Join(evidenceRoot, sha, target, c.ID)
	if err := ensureWithin(evidenceRoot, caseDir); err != nil {
		return "", fmt.Errorf("case directory escapes the evidence root")
	}

	res, raw, err := loadResult(filepath.Join(caseDir, "result.json"))
	if err != nil {
		return "", err
	}
	if res.SchemaVersion != ResultSchemaVersion {
		return "", fmt.Errorf("schemaVersion = %d, want %d", res.SchemaVersion, ResultSchemaVersion)
	}
	if res.CaseID != c.ID {
		return "", fmt.Errorf("caseId %q does not match registered case %q", res.CaseID, c.ID)
	}
	if normalizeSHA(res.GitSHA) != sha {
		return "", fmt.Errorf("gitSHA %q does not match gate commit %q", res.GitSHA, sha)
	}
	if res.Target != target {
		return "", fmt.Errorf("target %q does not match gate target %q", res.Target, target)
	}
	if res.EvidenceKind != c.EvidenceKind {
		return "", fmt.Errorf("evidenceKind %q does not match matrix evidenceKind %q", res.EvidenceKind, c.EvidenceKind)
	}
	if !validStatus(res.Status) {
		return "", fmt.Errorf("status %q is not one of PASS, FAIL, BLOCKED, NOT_RUN", res.Status)
	}
	if res.OS.Version == "" || res.OS.Build == "" || res.OS.Arch == "" {
		return "", fmt.Errorf("os.version, os.build and os.arch must be recorded")
	}
	if len(res.ToolVersions) == 0 {
		return "", fmt.Errorf("toolVersions must record the tools used by the case")
	}
	if err := validateCommands(raw); err != nil {
		return "", err
	}
	if err := validateArtifactFields(raw); err != nil {
		return "", err
	}
	if (res.Status == StatusPass || res.Status == StatusFail) && len(res.Artifacts) == 0 {
		return "", fmt.Errorf("artifacts must not be empty for %s evidence", res.Status)
	}
	if (res.Status == StatusBlocked || res.Status == StatusNotRun) && len(res.Limitations) == 0 {
		return "", fmt.Errorf("limitations must explain %s evidence", res.Status)
	}

	if resolver == nil {
		resolver = preflight.OSSystem{}
	}
	for _, artifact := range res.Artifacts {
		if err := validateArtifact(caseDir, artifact, resolver); err != nil {
			return "", err
		}
	}
	return res.Status, nil
}

func validateArtifact(caseDir string, a Artifact, resolver preflight.Resolver) error {
	if strings.TrimSpace(a.Path) == "" {
		return fmt.Errorf("artifact path is empty")
	}
	if filepath.IsAbs(a.Path) || filepath.VolumeName(a.Path) != "" {
		return fmt.Errorf("artifact %q must be a relative path inside the case directory", a.Path)
	}
	clean := filepath.Clean(filepath.FromSlash(a.Path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("artifact %q escapes the case directory", a.Path)
	}
	full := filepath.Join(caseDir, clean)
	if err := ensureWithin(caseDir, full); err != nil {
		return fmt.Errorf("artifact %q escapes the case directory", a.Path)
	}
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("artifact %q is missing: %v", a.Path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact %q is not a regular file", a.Path)
	}
	if !isSHA256(normalizeSHA(a.SHA256)) {
		return fmt.Errorf("artifact %q sha256 %q is not a 64-character hex digest", a.Path, a.SHA256)
	}

	// Resolve reparse points before accepting the file: a junction inside the
	// case directory must not hand the gate a file from somewhere else.
	resDir, err := resolver.Resolve(caseDir)
	if err != nil {
		return fmt.Errorf("resolve case directory: %w", err)
	}
	resArtifact, err := resolver.Resolve(full)
	if err != nil {
		return fmt.Errorf("resolve artifact %q: %w", a.Path, err)
	}
	if err := ensureWithin(resDir.Resolved, resArtifact.Resolved); err != nil {
		return fmt.Errorf("artifact %q escapes the case directory", a.Path)
	}

	actual, err := fileSHA256(full)
	if err != nil {
		return fmt.Errorf("hash artifact %q: %w", a.Path, err)
	}
	if !strings.EqualFold(actual, a.SHA256) {
		return fmt.Errorf("artifact %q sha256 mismatch: recorded %s, actual %s", a.Path, a.SHA256, actual)
	}
	return nil
}

func ensureWithin(base, target string) error {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%q is outside %q", target, base)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
