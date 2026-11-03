//go:build !windows

package policy

import "fmt"

// makeJunction 在非 Windows 平台没有对应语义。
//
// 返回错误而不是静默成功：调用方（junction 越界用例）在非 Windows 上会先跳过，
// 若哪天误用，这里会立刻报出来而不是给出一个假的"已覆盖"。
func makeJunction(link, target string) error {
	return fmt.Errorf("目录联接是 Windows 特有机制，本平台无法构造：%s -> %s", link, target)
}
