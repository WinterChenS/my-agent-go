package acceptance

import (
	"encoding/json"
	"fmt"
	"strings"
)

// blockedDesktopCodes are agent-desktop error codes that describe the test
// environment (permissions, private state files, CLI internals), not the
// product under test.
var blockedDesktopCodes = map[string]bool{
	"INTERNAL":       true,
	"PERM_DENIED":    true,
	"POLICY_DENIED":  true,
	"ACCESS_DENIED":  true,
	"E_ACCESSDENIED": true,
}

// classifyDesktopFailure decides whether an agent-desktop failure is an
// environment problem (BLOCKED) or a product assertion failure (FAIL).
func classifyDesktopFailure(failure string, body []byte) (bool, string) {
	failure = strings.TrimSpace(failure)
	lowerFailure := strings.ToLower(failure)
	if strings.Contains(lowerFailure, "look up") || strings.Contains(lowerFailure, "executable file not found") {
		// The CLI itself is missing or unlaunchable.
		return true, failure
	}

	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(lastJSONObject(body), &envelope); err == nil && envelope.Error != nil {
		code := strings.ToUpper(strings.TrimSpace(envelope.Error.Code))
		message := strings.TrimSpace(envelope.Error.Message)
		detail := strings.TrimSpace(envelope.Error.Code + ": " + message)
		if blockedDesktopCodes[code] || environmentPhrase(message) {
			return true, detail
		}
	}
	return false, ""
}

func environmentPhrase(message string) bool {
	lower := strings.ToLower(message)
	for _, phrase := range []string{
		"dacl",
		"untrusted principal",
		"access is denied",
		"permission denied",
		"permission_denied",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// desktopFailureDetail keeps the original agent-desktop or tool error in
// evidence. For tool output that is not JSON (icacls) the log tail is used.
func desktopFailureDetail(command, failure string, body []byte) string {
	if _, detail := classifyDesktopFailure(failure, body); detail != "" {
		return detail
	}
	if command == "icacls" {
		if tail := lastLines(string(body), 5); tail != "" {
			return fmt.Sprintf("%s: %s", strings.TrimSpace(failure), tail)
		}
	}
	return strings.TrimSpace(failure)
}

// reverifyCondition tells the operator what unblocks a BLOCKED native case.
const reverifyCondition = "re-verify after repairing the reported environment condition"

// withReverify appends the re-verification condition exactly once.
func withReverify(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return reverifyCondition
	}
	if strings.HasSuffix(detail, reverifyCondition) {
		return detail
	}
	return detail + "; " + reverifyCondition
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
