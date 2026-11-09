//go:build windows

package acceptance

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// agentDesktopACLArgs returns the icacls arguments that remove inherited ACEs
// and grant the state directory only to the current user, SYSTEM and
// Administrators. SIDs are used so the command is locale-independent.
func agentDesktopACLArgs(home string) ([]string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("open current process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current user SID: %w", err)
	}
	sid := user.User.Sid.String()
	return []string{
		home,
		"/inheritance:r",
		"/grant:r",
		"*" + sid + ":(OI)(CI)F",
		"*S-1-5-18:(OI)(CI)F",     // NT AUTHORITY\SYSTEM
		"*S-1-5-32-544:(OI)(CI)F", // BUILTIN\Administrators
		"/T",
		"/Q",
	}, nil
}
