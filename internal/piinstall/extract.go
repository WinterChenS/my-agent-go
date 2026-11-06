package piinstall

// 本文件实现 F06 的归档解包（计划 F06 Step 3 的最后一段）。
//
// 纪律（PI_INS01 的"归档边界"）：
//   - 绝对路径、`..`、盘符路径、逃出解压目录的路径一律拒绝；
//   - 符号链接/硬链接一律拒绝（不尝试"判断它是否指向外面"——一律不写链接更简单、
//     更安全，Pi 的发行包本来就不含链接）；
//   - 设备/FIFO 等非常规条目一律拒绝；
//   - 重复路径（大小写不敏感，因为 Windows 上它们就是同一个文件）一律拒绝；
//   - 总解压字节数、文件数都有上界；
//   - tar 条目保留执行位（Pi 必须在没有 Node 的环境里直接可执行）；
//   - 解出来的包必须真的含目标平台的可执行体，且架构与目标一致。

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
)

// 默认配额：官方 Pi 包解压后约 130MB / 200 多个文件，这里留足余量但仍然有界。
const (
	defaultMaxArchiveBytes = int64(1) << 30 // 1 GiB
	defaultMaxArchiveFiles = 8192
)

// ExtractLimits 是解包配额。
type ExtractLimits struct {
	MaxTotalBytes int64
	MaxFiles      int
}

// DefaultExtractLimits 返回默认配额。
func DefaultExtractLimits() ExtractLimits {
	return ExtractLimits{MaxTotalBytes: defaultMaxArchiveBytes, MaxFiles: defaultMaxArchiveFiles}
}

// Extracted 是解包结果。
type Extracted struct {
	Dir              string `json:"dir"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executableSha256"`
	Files            int    `json:"files"`
	TotalBytes       int64  `json:"totalBytes"`
}

// ExpectedExecutable 返回某目标平台 Pi 的可执行体文件名。
func ExpectedExecutable(targetID string) (string, error) {
	switch {
	case strings.HasPrefix(targetID, "win-"):
		return "pi.exe", nil
	case strings.HasPrefix(targetID, "mac-"), strings.HasPrefix(targetID, "linux-"):
		return "pi", nil
	}
	return "", contracts.NewAppError(contracts.CodeUnknownTarget, "未知目标: "+targetID, false)
}

// Extract 解包 archivePath 到 destDir，并要求解出 expectedExecutable。
func Extract(archivePath, destDir, expectedExecutable string, limits ExtractLimits) (Extracted, error) {
	var zero Extracted
	if limits.MaxTotalBytes <= 0 || limits.MaxFiles <= 0 {
		limits = DefaultExtractLimits()
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return zero, fmt.Errorf("piinstall: 创建解压目录失败: %w", err)
	}
	kind, err := sniffArchive(archivePath)
	if err != nil {
		return zero, err
	}
	var files int
	var total int64
	seen := map[string]bool{}

	writeEntry := func(name string, mode os.FileMode, body io.Reader, declaredSize int64) error {
		target, err := secureJoin(destDir, name)
		if err != nil {
			return err
		}
		key := strings.ToLower(filepath.ToSlash(target))
		if seen[key] {
			return verificationError("归档里有重复路径（大小写不敏感）: " + name)
		}
		seen[key] = true
		files++
		if files > limits.MaxFiles {
			return verificationError(fmt.Sprintf("归档文件数超过上限 %d", limits.MaxFiles))
		}
		if declaredSize > 0 {
			total += declaredSize
			if total > limits.MaxTotalBytes {
				return verificationError(fmt.Sprintf("归档解压后总字节数超过上限 %d", limits.MaxTotalBytes))
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("piinstall: 创建目录失败: %w", err)
		}
		perm := mode.Perm()
		if perm == 0 {
			perm = 0o644
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
		if err != nil {
			return fmt.Errorf("piinstall: 创建文件 %s 失败: %w", name, err)
		}
		// 每个条目最多读 declaredSize+1 字节：超出声明就失败，不无限读。
		limit := int64(1) << 40
		if declaredSize >= 0 {
			limit = declaredSize + 1
		}
		written, copyErr := io.Copy(f, io.LimitReader(body, limit))
		closeErr := f.Close()
		if copyErr != nil {
			return fmt.Errorf("piinstall: 写文件 %s 失败: %w", name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("piinstall: 关闭文件 %s 失败: %w", name, closeErr)
		}
		if declaredSize >= 0 && written != declaredSize {
			return verificationError(fmt.Sprintf("归档条目 %s 声明 %d 字节、实际 %d", name, declaredSize, written))
		}
		return nil
	}

	mkdirEntry := func(name string, mode os.FileMode) error {
		target, err := secureJoin(destDir, name)
		if err != nil {
			return err
		}
		perm := mode.Perm()
		if perm == 0 {
			perm = 0o755
		}
		if err := os.MkdirAll(target, perm); err != nil {
			return fmt.Errorf("piinstall: 创建目录 %s 失败: %w", name, err)
		}
		return nil
	}

	switch kind {
	case archiveZip:
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return zero, verificationError("zip 打不开: " + err.Error())
		}
		defer func() { _ = zr.Close() }()
		for _, f := range zr.File {
			name := f.Name
			mode := f.Mode()
			if err := rejectUnsafeEntry(name, mode, f.FileInfo().IsDir()); err != nil {
				return zero, err
			}
			if f.FileInfo().IsDir() {
				if err := mkdirEntry(name, mode); err != nil {
					return zero, err
				}
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return zero, verificationError("读取 zip 条目失败: " + err.Error())
			}
			err = writeEntry(name, mode, rc, int64(f.UncompressedSize64))
			_ = rc.Close()
			if err != nil {
				return zero, err
			}
		}
	case archiveTarGz:
		f, err := os.Open(archivePath)
		if err != nil {
			return zero, fmt.Errorf("piinstall: 打开归档失败: %w", err)
		}
		defer func() { _ = f.Close() }()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return zero, verificationError("tar.gz 打不开: " + err.Error())
		}
		defer func() { _ = gz.Close() }()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return zero, verificationError("读取 tar 条目失败: " + err.Error())
			}
			mode := os.FileMode(hdr.Mode)
			switch hdr.Typeflag {
			case tar.TypeDir:
				if err := rejectUnsafeEntry(hdr.Name, mode|os.ModeDir, true); err != nil {
					return zero, err
				}
				if err := mkdirEntry(hdr.Name, mode); err != nil {
					return zero, err
				}
			case tar.TypeReg:
				if err := rejectUnsafeEntry(hdr.Name, mode, false); err != nil {
					return zero, err
				}
				if err := writeEntry(hdr.Name, mode, tr, hdr.Size); err != nil {
					return zero, err
				}
			default:
				return zero, verificationError(fmt.Sprintf(
					"归档里有不允许的条目类型 %q（只接受常规文件与目录）: %s", string(hdr.Typeflag), hdr.Name))
			}
		}
	}

	exePath := filepath.Join(destDir, expectedExecutable)
	st, err := os.Stat(exePath)
	if err != nil {
		return zero, verificationError("归档里没有目标平台的可执行体 " + expectedExecutable + ": " + err.Error())
	}
	if !st.Mode().IsRegular() {
		return zero, verificationError("目标可执行体不是常规文件: " + exePath)
	}
	// 这里只要求"可执行体存在且是常规文件"：格式/架构校验需要目标 ID，
	// 由调用方在 extract 阶段用 ValidateExecutableForTarget(exe, target) 完成。
	sum, err := hashFileContent(exePath)
	if err != nil {
		return zero, fmt.Errorf("piinstall: 计算可执行体哈希失败: %w", err)
	}
	return Extracted{Dir: destDir, Executable: exePath, ExecutableSHA256: sum, Files: files, TotalBytes: total}, nil
}

// rejectUnsafeEntry 拒绝越界路径、链接、非常规条目。
func rejectUnsafeEntry(name string, mode os.FileMode, isDir bool) error {
	if name == "" {
		return verificationError("归档里有空路径条目")
	}
	if strings.ContainsRune(name, 0) {
		return verificationError("归档条目路径含 NUL: " + name)
	}
	// 反斜杠在 Windows 上是分隔符；统一成 / 再判断，避免 "..\\escape" 绕过。
	norm := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(norm, "/") {
		return verificationError("归档条目是绝对路径: " + name)
	}
	if len(norm) >= 2 && norm[1] == ':' {
		return verificationError("归档条目带盘符: " + name)
	}
	for _, part := range strings.Split(norm, "/") {
		if part == ".." {
			return verificationError("归档条目越界（含 ..）: " + name)
		}
	}
	if mode&os.ModeSymlink != 0 {
		return verificationError("归档里有符号链接（一律拒绝）: " + name)
	}
	if !isDir && !mode.IsRegular() && mode != 0 {
		return verificationError(fmt.Sprintf("归档条目不是常规文件（mode=%s）: %s", mode, name))
	}
	return nil
}

// secureJoin 把归档条目名安全地拼到 dest 下，并再次确认没有逃出 dest。
func secureJoin(dest, name string) (string, error) {
	norm := strings.ReplaceAll(name, `\`, "/")
	clean := filepath.Clean(filepath.Join(dest, filepath.FromSlash(norm)))
	rel, err := filepath.Rel(dest, clean)
	if err != nil {
		return "", verificationError("归档条目路径无法解析: " + name)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", verificationError("归档条目逃出解压目录: " + name)
	}
	return clean, nil
}

type archiveKind int

const (
	archiveUnknown archiveKind = iota
	archiveZip
	archiveTarGz
)

// sniffArchive 用魔数判断格式（不看扩展名：扩展名是元数据，可以被改）。
func sniffArchive(path string) (archiveKind, error) {
	f, err := os.Open(path)
	if err != nil {
		return archiveUnknown, fmt.Errorf("piinstall: 打开归档失败: %w", err)
	}
	defer func() { _ = f.Close() }()
	var head [4]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && err != io.ErrUnexpectedEOF {
		return archiveUnknown, verificationError("读取归档头失败: " + err.Error())
	}
	if n >= 4 && head[0] == 'P' && head[1] == 'K' && (head[2] == 3 || head[2] == 5 || head[2] == 7) {
		return archiveZip, nil
	}
	if n >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		return archiveTarGz, nil
	}
	return archiveUnknown, verificationError("既不是 zip 也不是 tar.gz（按魔数判断）")
}

// ValidateExecutableForTarget 校验可执行体与目标平台/架构一致。
//
// targetID 为空时只要求"是已知的可执行格式"（ELF/Mach-O/PE），供解包阶段的
// 最低判断使用；给出 targetID 时还要求操作系统与机器码匹配。
func ValidateExecutableForTarget(path, targetID string) error {
	f, err := os.Open(path)
	if err != nil {
		return verificationError("可执行体不可读: " + err.Error())
	}
	defer func() { _ = f.Close() }()
	var head [4]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return verificationError("可执行体太短，不像任何发行包里的二进制: " + err.Error())
	}

	kind := ""
	switch {
	case head[0] == 'M' && head[1] == 'Z':
		kind = "windows"
	case head[0] == 0x7f && head[1] == 'E' && head[2] == 'L' && head[3] == 'F':
		kind = "linux"
	case (head[0] == 0xcf && head[1] == 0xfa && head[2] == 0xed && head[3] == 0xfe) ||
		(head[0] == 0xfe && head[1] == 0xed && head[2] == 0xfa && head[3] == 0xcf):
		kind = "mac"
	default:
		return verificationError("不是已知的可执行格式（PE/ELF/Mach-O）: " + path)
	}
	if targetID == "" {
		return nil
	}
	wantOS := ""
	switch {
	case strings.HasPrefix(targetID, "win-"):
		wantOS = "windows"
	case strings.HasPrefix(targetID, "linux-"):
		wantOS = "linux"
	case strings.HasPrefix(targetID, "mac-"):
		wantOS = "mac"
	default:
		return contracts.NewAppError(contracts.CodeUnknownTarget, "未知目标: "+targetID, false)
	}
	if kind != wantOS {
		return verificationError(fmt.Sprintf("可执行体格式 %s 与目标 %s 不符", kind, targetID))
	}
	wantArch := ""
	switch {
	case strings.HasSuffix(targetID, "-x64"):
		wantArch = "amd64"
	case strings.HasSuffix(targetID, "-arm64"):
		wantArch = "arm64"
	}
	switch kind {
	case "windows":
		pf, err := pe.Open(path)
		if err != nil {
			return verificationError("PE 头解析失败: " + err.Error())
		}
		defer func() { _ = pf.Close() }()
		got := map[uint16]string{0x8664: "amd64", 0xAA64: "arm64", 0x014c: "386"}[pf.Machine]
		if got == "" {
			return verificationError(fmt.Sprintf("未知的 PE 机器码 0x%04x", pf.Machine))
		}
		if wantArch != "" && got != wantArch {
			return verificationError(fmt.Sprintf("PE 架构 %s 与目标 %s 不符", got, targetID))
		}
	case "linux":
		// e_machine 在 ELF 头偏移 18（小端）。
		var buf [2]byte
		if _, err := f.ReadAt(buf[:], 18); err != nil {
			return verificationError("ELF 头不完整: " + err.Error())
		}
		got := map[uint16]string{62: "amd64", 183: "arm64"}[binary.LittleEndian.Uint16(buf[:])]
		if got == "" {
			return verificationError(fmt.Sprintf("未知的 ELF 机器码 0x%04x", binary.LittleEndian.Uint16(buf[:])))
		}
		if wantArch != "" && got != wantArch {
			return verificationError(fmt.Sprintf("ELF 架构 %s 与目标 %s 不符", got, targetID))
		}
	case "mac":
		var buf [4]byte
		if _, err := f.ReadAt(buf[:], 4); err != nil {
			return verificationError("Mach-O 头不完整: " + err.Error())
		}
		got := map[uint32]string{0x01000007: "amd64", 0x0100000C: "arm64"}[binary.LittleEndian.Uint32(buf[:])]
		if got == "" {
			return verificationError(fmt.Sprintf("未知的 Mach-O CPU 类型 0x%08x", binary.LittleEndian.Uint32(buf[:])))
		}
		if wantArch != "" && got != wantArch {
			return verificationError(fmt.Sprintf("Mach-O 架构 %s 与目标 %s 不符", got, targetID))
		}
	}
	return nil
}
