//go:build linux

package devtools

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"

	"my-agent-go/internal/contracts"
)

// os-release 里 VERSION_ID 的值是发行版版本（如 Ubuntu 的 "24.04"）。
// 为什么不用 uname -r：内核版本与发行版版本是两件事，最低验收系统是按发行版
// 版本声明的（Ubuntu 24.04），内核号无法直接比较。
const osReleasePath = "/etc/os-release"

// hostEnvironment 读 /etc/os-release 的 VERSION_ID。
// Linux 不按 build 号判定，OSBuild 留空。
func hostEnvironment(display string) (contracts.Environment, error) {
	version, err := osReleaseVersionID(osReleasePath)
	if err != nil {
		return contracts.Environment{}, err
	}
	return contracts.Environment{
		OS:        runtime.GOOS,
		OSVersion: version,
		OSBuild:   "",
		Arch:      runtime.GOARCH,
		Display:   display,
	}, nil
}

// osReleaseVersionID 解析 os-release 的 VERSION_ID，去掉引号。
// 读不到或读到空值都返回错误：调用方必须 exit 2，而不是写一个空版本冒充通过。
func osReleaseVersionID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败，无法确定宿主发行版版本: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "VERSION_ID=") {
			continue
		}
		value := strings.TrimPrefix(line, "VERSION_ID=")
		value = strings.Trim(value, `"'`)
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("%s 的 VERSION_ID 为空，无法确定宿主发行版版本", path)
		}
		return value, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	return "", fmt.Errorf("%s 没有 VERSION_ID，无法确定宿主发行版版本", path)
}
