//go:build windows

package main

import "golang.org/x/sys/windows/registry"

const personalizeKey = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`

// readSystemDark reads the Windows app theme. AppsUseLightTheme=0 means dark.
func readSystemDark() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, personalizeKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return value == 0
}
