//go:build windows

package artifact

// Windows 目录联接（junction）构造器，仅供 C07 产物测试使用。
//
// 与 internal/session 的 C06c 夹具同源：internal/policy 里的同名构造器未导出，
// 而本任务的边界不允许改那个包。这里复制的是**测试夹具**，不是产品逻辑——
// 产品侧识别链接仍然走 internal/policy/pathscope.go 的解析。
//
// 不用 cmd.exe / mklink：测试不该依赖外部 shell，也不该把待测路径拼进命令行。

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

const (
	artifactFsctlSetReparsePoint = 0x000900A4
	artifactIOTagMountPoint      = 0xA0000003
)

// makeDirLink 在 link 处创建一个指向 target 的目录链接（Windows：目录联接）。
func makeDirLink(t *testing.T, link, target string) {
	t.Helper()
	if err := artifactJunction(link, target); err != nil {
		t.Fatalf("创建目录联接失败（环境不支持则该用例不能算通过）: %v", err)
	}
}

func artifactJunction(link, target string) error {
	if err := os.Mkdir(link, 0o755); err != nil {
		return fmt.Errorf("创建链接目录: %w", err)
	}
	buf, err := artifactMountPointReparse(`\??\`+target, target)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("打开链接目录: %w", err)
	}
	defer windows.CloseHandle(h)
	var returned uint32
	if err := windows.DeviceIoControl(h, artifactFsctlSetReparsePoint,
		&buf[0], uint32(len(buf)), nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("写入 reparse point: %w", err)
	}
	return nil
}

// artifactMountPointReparse 按 REPARSE_DATA_BUFFER 布局拼出 MOUNT_POINT 缓冲区。
// 长度字段不能把结尾 NUL 算进去（算进去内核回 ERROR_INVALID_REPARSE_DATA）。
func artifactMountPointReparse(substitute, print string) ([]byte, error) {
	sub, err := windows.UTF16FromString(substitute)
	if err != nil {
		return nil, fmt.Errorf("编码替代名: %w", err)
	}
	pr, err := windows.UTF16FromString(print)
	if err != nil {
		return nil, fmt.Errorf("编码显示名: %w", err)
	}
	subBytes := len(sub) * 2
	prBytes := len(pr) * 2
	dataLen := 8 + subBytes + prBytes
	buf := make([]byte, 8+dataLen)
	binary.LittleEndian.PutUint32(buf[0:], artifactIOTagMountPoint)
	binary.LittleEndian.PutUint16(buf[4:], uint16(dataLen))
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint16(buf[8:], 0)
	binary.LittleEndian.PutUint16(buf[10:], uint16(subBytes-2))
	binary.LittleEndian.PutUint16(buf[12:], uint16(subBytes))
	binary.LittleEndian.PutUint16(buf[14:], uint16(prBytes-2))
	off := 16
	for _, u := range sub {
		binary.LittleEndian.PutUint16(buf[off:], u)
		off += 2
	}
	for _, u := range pr {
		binary.LittleEndian.PutUint16(buf[off:], u)
		off += 2
	}
	return buf, nil
}
