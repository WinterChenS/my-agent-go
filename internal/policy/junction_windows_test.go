//go:build windows

package policy

import (
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const (
	// FSCTL_SET_REPARSE_POINT：给目录写入一个 reparse point（目录联接的底层机制）。
	fsctlSetReparsePoint = 0x000900A4
	// IO_REPARSE_TAG_MOUNT_POINT：目录联接（junction）的 tag，与符号链接是两个不同的 tag。
	ioReparseTagMountPoint = 0xA0000003
)

// makeJunction 在 link 处创建指向 target 的目录联接。
//
// 直接用 DeviceIoControl(FSCTL_SET_REPARSE_POINT) 写 reparse point，
// 不走 cmd.exe / mklink：测试不应依赖外部 shell，也不该把待测路径拼进命令行。
//
// 之所以要单独造联接：Windows 上"目录联接"和"符号链接"是两种不同的 reparse tag，
// 解析实现很容易只覆盖其中一种。只测 symlink 就宣称覆盖了越界，是自欺。
func makeJunction(link, target string) error {
	if err := os.Mkdir(link, 0o755); err != nil {
		return fmt.Errorf("创建链接目录: %w", err)
	}
	buf, err := mountPointReparse(`\??\`+target, target)
	if err != nil {
		return err
	}

	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	// FILE_FLAG_OPEN_REPARSE_POINT：打开链接自身而不是跟随它；
	// FILE_FLAG_BACKUP_SEMANTICS：允许对目录取句柄。
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("打开链接目录: %w", err)
	}
	defer windows.CloseHandle(h)

	var returned uint32
	if err := windows.DeviceIoControl(h, fsctlSetReparsePoint,
		&buf[0], uint32(len(buf)), nil, 0, &returned, nil); err != nil {
		return fmt.Errorf("写入 reparse point: %w", err)
	}
	return nil
}

// mountPointReparse 按 REPARSE_DATA_BUFFER 布局拼出 MOUNT_POINT 缓冲区。
//
// 布局（全部小端）：
//
//	0  ULONG  ReparseTag
//	4  USHORT ReparseDataLength  (= 8 + 路径字节数，含 NUL)
//	6  USHORT Reserved
//	8  USHORT SubstituteNameOffset
//	10 USHORT SubstituteNameLength   ← 不含结尾 NUL
//	12 USHORT PrintNameOffset        ← 指向 PrintName 起点（跨过 SubstituteName 的 NUL）
//	14 USHORT PrintNameLength        ← 不含结尾 NUL
//	16 WCHAR  PathBuffer[]           (SubstituteName + NUL + PrintName + NUL)
//
// 实测结论：长度字段若把结尾 NUL 算进去，内核直接回
// ERROR_INVALID_REPARSE_DATA（"The data present in the reparse point buffer is invalid"）。
// 这不是文档随手能查到的细节，是靠 TestScratchJunction 逐变体试出来的。
func mountPointReparse(substitute, print string) ([]byte, error) {
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

	binary.LittleEndian.PutUint32(buf[0:], ioReparseTagMountPoint)
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
