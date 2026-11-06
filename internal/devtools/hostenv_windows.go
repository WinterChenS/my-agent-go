//go:build windows

package devtools

import (
	"fmt"
	"runtime"
	"strconv"

	"golang.org/x/sys/windows"

	"my-agent-go/internal/contracts"
)

// hostEnvironment 用 RtlGetVersion 读真实 Windows 版本。
//
// 为什么不用 GetVersionEx / runtime 的 manifest 版本：GetVersionEx 受应用兼容性
// 清单影响，会谎报 6.2；RtlGetVersion 返回内核真实版本号。最低验收系统是按
// build 号判定的，拿不到真实 build 就无法判定。
//
// OSVersion 口径：build >= 22000 记为 "11"，否则 "10"。微软没有发布过
// build 22000 以下的 Windows 11，所以这是单调可靠的判据。
func hostEnvironment(display string) (contracts.Environment, error) {
	info := windows.RtlGetVersion()
	if info == nil || info.BuildNumber == 0 {
		return contracts.Environment{}, fmt.Errorf("RtlGetVersion 未返回有效 build 号，无法确定宿主 Windows 版本")
	}
	osVersion := "10"
	if info.BuildNumber >= 22000 {
		osVersion = "11"
	}
	return contracts.Environment{
		OS:        runtime.GOOS,
		OSVersion: osVersion,
		OSBuild:   strconv.FormatUint(uint64(info.BuildNumber), 10),
		Arch:      runtime.GOARCH,
		Display:   display,
	}, nil
}
