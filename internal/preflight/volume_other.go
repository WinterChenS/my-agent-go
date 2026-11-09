//go:build !windows

package preflight

import "path/filepath"

// resolveExisting is the POSIX implementation: symlinks are fully resolved by
// filepath.EvalSymlinks.
func resolveExisting(path string) (string, error) { return filepath.EvalSymlinks(path) }

// volumeAllowed is platform-agnostic off Windows; the D:-only rule belongs to
// the Windows rebuild environment.
func volumeAllowed(string) bool { return true }
