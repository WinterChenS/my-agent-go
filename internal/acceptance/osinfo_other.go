//go:build !windows

package acceptance

import "runtime"

// currentOSInfo reports the host used by this evidence run. The D:-only rule
// and Windows build reporting do not apply on this platform.
func currentOSInfo() OSInfo {
	return OSInfo{
		Name:    runtime.GOOS,
		Version: runtime.GOOS,
		Build:   "n/a",
		Arch:    runtime.GOARCH,
	}
}
