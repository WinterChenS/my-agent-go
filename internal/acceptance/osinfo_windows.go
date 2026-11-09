//go:build windows

package acceptance

import (
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const windowsCurrentVersion = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`

func registryString(path, name string) string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer key.Close()
	if value, _, err := key.GetStringValue(name); err == nil {
		return strings.TrimSpace(value)
	}
	if value, _, err := key.GetIntegerValue(name); err == nil {
		return fmt.Sprintf("%d", value)
	}
	return ""
}

// currentOSInfo reports the Windows edition, kernel version, build/UBR and
// architecture used by this evidence run.
func currentOSInfo() OSInfo {
	product := registryString(windowsCurrentVersion, "ProductName")
	display := registryString(windowsCurrentVersion, "DisplayVersion")
	build := registryString(windowsCurrentVersion, "CurrentBuildNumber")
	ubr := registryString(windowsCurrentVersion, "UBR")

	name := product
	if display != "" {
		name = strings.TrimSpace(product + " " + display)
	}
	version := ""
	if build != "" {
		version = "10.0." + build
	}
	fullBuild := build
	if ubr != "" {
		fullBuild = build + "." + ubr
	}
	return OSInfo{
		Name:    name,
		Version: version,
		Build:   fullBuild,
		Arch:    runtime.GOARCH,
	}
}
