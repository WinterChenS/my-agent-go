package devtools

// 宿主环境探测（GOV05）。
//
// 证据必须记录**真实**宿主环境：runner 自己声明的目标值不是身份，是意图。
// 探测失败时调用方必须退出 2（配置缺失），不允许写猜测值——一份带假 OS 版本
// 的证据比没有证据更糟，它会让门禁的最低系统判定变成装饰。

import (
	"my-agent-go/internal/contracts"
)

// HostEnvironment 返回真实宿主环境；display 用调用方归一化后的值。
//
// 各平台实现见 hostenv_<goos>.go：windows 用 RtlGetVersion，darwin 用 sw_vers，
// linux 读 /etc/os-release，其余平台返回不支持错误。
func HostEnvironment(display string) (contracts.Environment, error) {
	return hostEnvironment(display)
}
