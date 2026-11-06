//go:build darwin

package devtools

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"my-agent-go/internal/contracts"
)

// hostEnvironment 用 sw_vers 读真实 macOS 版本。
//
// 用 argv 而不是 shell 拼接：不需要引号规则，也不会把参数解释成命令。
func hostEnvironment(display string) (contracts.Environment, error) {
	version, err := swVers("-productVersion")
	if err != nil {
		return contracts.Environment{}, err
	}
	build, err := swVers("-buildVersion")
	if err != nil {
		return contracts.Environment{}, err
	}
	return contracts.Environment{
		OS:        runtime.GOOS,
		OSVersion: version,
		OSBuild:   build,
		Arch:      runtime.GOARCH,
		Display:   display,
	}, nil
}

// swVers 执行 `sw_vers <flag>` 并返回裁剪后的输出；空输出视为探测失败。
func swVers(flag string) (string, error) {
	out, err := exec.Command("sw_vers", flag).Output()
	if err != nil {
		return "", fmt.Errorf("执行 sw_vers %s 失败: %w", flag, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", fmt.Errorf("sw_vers %s 输出为空，无法确定宿主 macOS 版本", flag)
	}
	return value, nil
}
