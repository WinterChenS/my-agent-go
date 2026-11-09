//go:build !windows

package acceptance

// agentDesktopACLArgs is not applicable off Windows; the D:-only write rule and
// the private-file DACL check belong to the Windows rebuild environment.
func agentDesktopACLArgs(string) ([]string, error) { return nil, nil }
