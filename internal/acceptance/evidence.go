package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Evidence kinds declared by the matrix and copied into result.json.
const (
	EvidenceAutomation    = "automation"
	EvidenceNativeDesktop = "native-desktop"
	EvidenceRealRuntime   = "real-runtime"
)

// Result statuses. Only PASS advances a gate; FAIL is a valid assertion
// failure (exit 1); BLOCKED and NOT_RUN keep the gate closed (exit 2).
const (
	StatusPass    = "PASS"
	StatusFail    = "FAIL"
	StatusBlocked = "BLOCKED"
	StatusNotRun  = "NOT_RUN"
)

// OSInfo records the machine an evidence run was produced on.
type OSInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Build   string `json:"build"`
	Arch    string `json:"arch"`
}

// Command is one command actually executed by a case.
type Command struct {
	Command  string `json:"command"`
	WorkDir  string `json:"workDir"`
	ExitCode int    `json:"exitCode"`
}

// Artifact is a file produced by a case, relative to the case directory.
type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Result is the evidence document stored at
// <evidence>/<gitSHA>/<target>/<caseId>/result.json.
type Result struct {
	SchemaVersion  int               `json:"schemaVersion"`
	CaseID         string            `json:"caseId"`
	GitSHA         string            `json:"gitSHA"`
	Target         string            `json:"target"`
	Status         string            `json:"status"`
	EvidenceKind   string            `json:"evidenceKind"`
	OS             OSInfo            `json:"os"`
	ToolVersions   map[string]string `json:"toolVersions"`
	RuntimeVersion string            `json:"runtimeVersion"`
	Commands       []Command         `json:"commands"`
	Artifacts      []Artifact        `json:"artifacts"`
	Limitations    []string          `json:"limitations"`
}

var requiredResultFields = []string{
	"schemaVersion", "caseId", "gitSHA", "target", "status", "evidenceKind",
	"os", "toolVersions", "runtimeVersion", "commands", "artifacts", "limitations",
}

func loadResult(path string) (Result, map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, nil, fmt.Errorf("missing result.json")
		}
		return Result{}, nil, fmt.Errorf("read result.json: %w", err)
	}
	if len(data) > 1<<20 {
		return Result{}, nil, fmt.Errorf("result.json exceeds 1 MiB")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Result{}, nil, fmt.Errorf("invalid result.json: %w", err)
	}
	for _, field := range requiredResultFields {
		if _, ok := raw[field]; !ok {
			return Result{}, nil, fmt.Errorf("result.json is missing field %q", field)
		}
	}
	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		return Result{}, nil, fmt.Errorf("invalid result.json: %w", err)
	}
	return res, raw, nil
}

func validateCommands(raw map[string]json.RawMessage) error {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw["commands"], &entries); err != nil {
		return fmt.Errorf("commands must be an array of objects")
	}
	if len(entries) == 0 {
		return fmt.Errorf("commands must not be empty")
	}
	for i, entry := range entries {
		for _, key := range []string{"command", "workDir", "exitCode"} {
			if _, ok := entry[key]; !ok {
				return fmt.Errorf("commands[%d] is missing field %q", i, key)
			}
		}
	}
	return nil
}

func validateArtifactFields(raw map[string]json.RawMessage) error {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw["artifacts"], &entries); err != nil {
		return fmt.Errorf("artifacts must be an array of objects")
	}
	for i, entry := range entries {
		for _, key := range []string{"path", "sha256"} {
			if _, ok := entry[key]; !ok {
				return fmt.Errorf("artifacts[%d] is missing field %q", i, key)
			}
		}
	}
	return nil
}

// validateMatrixCase checks a single case declaration.
func validateMatrixCase(c Case) error {
	if err := validateIdentifier(c.ID, "case id"); err != nil {
		return err
	}
	if err := validateIdentifier(c.Task, "task id"); err != nil {
		return err
	}
	if !validEvidenceKind(c.EvidenceKind) {
		return fmt.Errorf("case %q has invalid evidenceKind %q (want automation, native-desktop or real-runtime)", c.ID, c.EvidenceKind)
	}
	return nil
}

func validEvidenceKind(kind string) bool {
	switch kind {
	case EvidenceAutomation, EvidenceNativeDesktop, EvidenceRealRuntime:
		return true
	default:
		return false
	}
}

func validStatus(status string) bool {
	switch status {
	case StatusPass, StatusFail, StatusBlocked, StatusNotRun:
		return true
	default:
		return false
	}
}

func validateIdentifier(value, what string) error {
	if value == "" || len(value) > 128 {
		return fmt.Errorf("invalid %s %q", what, value)
	}
	for i, r := range value {
		allowed := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !allowed || (i == 0 && (r == '.' || r == '_' || r == '-')) {
			return fmt.Errorf("invalid %s %q (letters, digits, dot, underscore and dash only)", what, value)
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func isHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func normalizeSHA(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
