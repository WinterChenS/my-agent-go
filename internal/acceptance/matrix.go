// Package acceptance loads tests/acceptance/rebuild.json and evaluates stage
// gates against recorded evidence.
//
// A gate only passes when the matrix is valid, every required task has
// registered cases, and every required case has complete, internally
// consistent evidence bound to the gate commit and target. Missing or invalid
// input returns exit code 2; valid evidence that reports FAIL returns 1;
// everything passing returns 0.
package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"my-agent-go/internal/preflight"
)

// Exit codes shared by the devtools CLI, the PowerShell wrapper and the gate.
const (
	ExitPass       = 0
	ExitAssertion  = 1
	ExitIncomplete = 2
)

// MatrixSchemaVersion is the only matrix schema this build understands.
const MatrixSchemaVersion = 1

// ResultSchemaVersion is the only evidence schema this build understands.
const ResultSchemaVersion = 1

// Matrix is the acceptance matrix document.
type Matrix struct {
	SchemaVersion int    `json:"schemaVersion"`
	Gates         []Gate `json:"gates"`
}

// Gate declares one stage gate, its targets, the tasks it requires and the
// cases registered so far.
type Gate struct {
	ID            string   `json:"id"`
	Targets       []string `json:"targets"`
	RequiredTasks []string `json:"requiredTasks"`
	Cases         []Case   `json:"cases"`
}

// Case is one registered acceptance case.
type Case struct {
	ID           string `json:"id"`
	Task         string `json:"task"`
	EvidenceKind string `json:"evidenceKind"`
	Required     bool   `json:"required"`
}

// GateOptions selects the gate evaluation inputs.
type GateOptions struct {
	MatrixPath   string
	EvidenceRoot string
	GateID       string
	Target       string
	GitSHA       string

	// Resolver resolves evidence paths to their real volume. Nil means
	// preflight.OSSystem (the production behavior).
	Resolver preflight.Resolver
}

// GateOutcome is the result of evaluating a gate.
type GateOutcome struct {
	Code     int
	Problems []string
	Statuses map[string]string
	Passed   []string
}

// LoadMatrix reads and validates the matrix document.
func LoadMatrix(path string) (Matrix, error) {
	if strings.TrimSpace(path) == "" {
		return Matrix{}, fmt.Errorf("matrix path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Matrix{}, fmt.Errorf("read matrix: %w", err)
	}
	if len(data) > 4<<20 {
		return Matrix{}, fmt.Errorf("matrix %q exceeds 4 MiB", path)
	}
	var m Matrix
	if err := json.Unmarshal(data, &m); err != nil {
		return Matrix{}, fmt.Errorf("parse matrix: %w", err)
	}
	if m.SchemaVersion != MatrixSchemaVersion {
		return Matrix{}, fmt.Errorf("matrix schemaVersion = %d, want %d", m.SchemaVersion, MatrixSchemaVersion)
	}
	if len(m.Gates) == 0 {
		return Matrix{}, fmt.Errorf("matrix declares no gates")
	}
	seenGates := map[string]bool{}
	for _, gate := range m.Gates {
		if strings.TrimSpace(gate.ID) == "" {
			return Matrix{}, fmt.Errorf("matrix contains a gate without an id")
		}
		if seenGates[gate.ID] {
			return Matrix{}, fmt.Errorf("matrix declares duplicate gate %q", gate.ID)
		}
		seenGates[gate.ID] = true
		if len(gate.Targets) == 0 {
			return Matrix{}, fmt.Errorf("gate %q declares no targets", gate.ID)
		}
		if len(gate.RequiredTasks) == 0 {
			return Matrix{}, fmt.Errorf("gate %q declares no required tasks", gate.ID)
		}
		for _, target := range gate.Targets {
			if err := validateIdentifier(target, "target"); err != nil {
				return Matrix{}, fmt.Errorf("gate %q: %w", gate.ID, err)
			}
		}
		for _, task := range gate.RequiredTasks {
			if err := validateIdentifier(task, "task id"); err != nil {
				return Matrix{}, fmt.Errorf("gate %q: %w", gate.ID, err)
			}
		}
	}
	return m, nil
}
