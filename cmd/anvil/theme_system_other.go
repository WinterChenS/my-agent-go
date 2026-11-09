//go:build !windows

package main

// readSystemDark is only used to seed the initial HTML/window background. The
// frontend matchMedia query remains authoritative off Windows.
func readSystemDark() bool { return false }
