package diagnostics

// 诊断包写入的路径守卫（与 C06e 会话导出、C07 产物导出同一组规则）。
//
// 为什么这里还有一份：internal/policy 的 lexicalReject 与逐段链接判定没有导出，
// 而 C07 的边界不允许改 policy。规则与 policy/pathscope.go、C06c 附件、
// C07 产物导出同源；三处若将来分叉，以 policy 的版本为准并在对应任务里收敛。
//
// 这里守的不是"用户输入"（导出目录是产品的目录），而是**写文件的位置**：
// 目标必须解析到导出根内的普通文件，且途中任何一段都不是符号链接/目录联接。

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"my-agent-go/internal/contracts"
)

// lexicalRejectPath 做纯词法拒绝，必须在解析之前执行。
func lexicalRejectPath(raw string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, "//") {
		return contracts.NewAppError(contracts.CodeUnauthorized, "不允许 UNC/设备路径: "+raw, false)
	}
	if len(raw) >= 2 && raw[1] == ':' {
		if len(raw) == 2 || (raw[2] != '\\' && raw[2] != '/') {
			return contracts.NewAppError(contracts.CodeUnauthorized, "不允许驱动器相对路径: "+raw, false)
		}
		for _, seg := range strings.Split(raw[2:], `\`) {
			if strings.Contains(seg, ":") {
				return contracts.NewAppError(contracts.CodeUnauthorized, "不允许备用数据流路径: "+raw, false)
			}
		}
	}
	return nil
}

// hasDotDotSegment 判断路径里是否出现 `..` 段（Clean 之前判断）。
func hasDotDotSegment(raw string) bool {
	for _, seg := range strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// escapesRoot 判断相对路径是否指向根之外。
func escapesRoot(rel string) bool {
	if rel == "" {
		return false
	}
	if filepath.IsAbs(rel) {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// linkBelowRoot 返回 root/rel 这条路径上第一个符号链接/目录联接组件；没有则返回空。
//
// 只从**根之下**开始逐段 Lstat：macOS 的 /var 本身是符号链接，从卷根走会把
// 每个正常文件都判成链接；Windows 的临时目录可能带 8.3 短名，字符串比较会误判。
// 目录联接在 Lstat 里可能没有 ModeSymlink，但 Readlink 一定能读出目标，
// 所以对每个非 symlink 条目也试探一次 Readlink（与 policy 同一判据）。
func linkBelowRoot(realRoot, rel string) (string, error) {
	if rel == "" || rel == "." {
		return "", nil
	}
	cur := realRoot
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == "." || seg == ".." || seg == "" {
			continue
		}
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", nil
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return cur, nil
		}
		if target, rerr := os.Readlink(cur); rerr == nil && target != "" {
			return cur, nil
		}
	}
	return "", nil
}
