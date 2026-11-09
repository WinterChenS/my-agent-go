//go:build !windows

package main

import "fmt"

func sendKeyCombo(windowTitle, combo string) error {
	return fmt.Errorf("key injection is Windows-only (window %q, combo %q)", windowTitle, combo)
}
