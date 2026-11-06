//go:build !windows

package session

// 非 Windows 平台没有目录联接：用目录符号链接覆盖同一类"路径经链接指向别处"
// 的攻击面。这里返回错误而不是静默成功——被误用时应当立刻炸掉，
// 而不是给出一个假的"已覆盖"。

import "os"

func makeAttachmentDirLink(t interface {
	Helper()
	Fatalf(string, ...any)
}, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("创建目录符号链接失败（环境不支持则该用例不能算通过）: %v", err)
	}
}
