//go:build !windows

package artifact

// 非 Windows 平台没有目录联接：用目录符号链接覆盖同一类"路径经链接指向别处"
// 的攻击面。创建失败直接 Fatal（环境不支持则该用例不能算通过），不返回假的成功。

import (
	"os"
	"testing"
)

// makeDirLink 在 link 处创建一个指向 target 的目录符号链接。
func makeDirLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("创建目录符号链接失败（环境不支持则该用例不能算通过）: %v", err)
	}
}
