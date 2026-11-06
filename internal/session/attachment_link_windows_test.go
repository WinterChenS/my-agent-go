//go:build windows

package session

// Windows 目录联接（junction）构造器，仅供 C06c 附件测试使用。
//
// 为什么在测试里再写一份：internal/policy 里的同名构造器未导出，而 C06c 不允许
// 改 internal/policy。这里复制的是**测试夹具**，不是产品逻辑；一旦产品侧需要
// 识别联接，用的仍然是 internal/policy/pathscope.go 那一份。
//
// 不走 cmd.exe / mklink：测试不应依赖外部 shell，也不该把待测路径拼进命令行。
// 直接用 DeviceIoControl(FSCTL_SET_REPARSE_POINT) 写 reparse point。

import (
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const (
	attachmentFsctlSetReparsePoint = 0x000900A4
	attachmentIOTagMountPoint      = 0xA0000003
)

// makeAttachmentDirLink 在 link 处创建指向 target 的目录链接。
//
// Windows 上造目录联接（junction）；其它平台在另一份 build-tag 文件里造目录符号链接。
func makeAttachmentDirLink(t interface {
	Helper()
	Fatalf(string, ...any)
}, link, target string) {
	t.Helper()
	if err := attachmentJunction(link, target); err != nil {
		t.Fatalf("创建目录联接失败（环境不支持则该用例不能算通过）: %v", err)
	}
}

func attachmentJunction(link, target string) error {
	if err := os.Mkdir(link, 0o755); err != nil {
		return fmt.Errorf("创建链接目录: %w", err)
	}
	buf, err := attachmentMountPointReparse(`\??\`+target, target)
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
	if err := windows.DeviceIoControl(h, attachmentFsctlSetReparsePoint,
		&buf[0], uint32(len(buf)), nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("写入 reparse point: %w", err)
	}
	return nil
}

// attachmentMountPointReparse 按 REPARSE_DATA_BUFFER 布局拼出 MOUNT_POINT 缓冲区。
// 长度字段不能把结尾 NUL 算进去（算进去内核回 ERROR_INVALID_REPARSE_DATA）。
func attachmentMountPointReparse(substitute, print string) ([]byte, error) {
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
	binary.LittleEndian.PutUint32(buf[0:], attachmentIOTagMountPoint)
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
