//go:build !windows && !darwin && !linux

package devtools

import (
	"fmt"
	"runtime"

	"my-agent-go/internal/contracts"
)

// hostEnvironment 在不支持的宿主上返回错误。
// 六个目标之外的平台（freebsd 等）没有最低系统声明，探测出来的版本也无从比较；
// 这时必须失败退出 2，而不是写一份没人能判定的环境身份。
func hostEnvironment(display string) (contracts.Environment, error) {
	return contracts.Environment{}, fmt.Errorf("不支持的宿主平台 %s/%s：无法探测最低 OS 版本", runtime.GOOS, runtime.GOARCH)
}
