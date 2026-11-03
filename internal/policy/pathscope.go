package policy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"my-agent-go/internal/contracts"
)

// PathGuard 是"许可针对的实际资源范围"守卫（SEC05）。
//
// 它只回答一个问题：这次调用的目标路径，把链接全部解析掉之后，是否真的落在
// 被授予的根目录内。判定基准是**真实路径**而不是用户写的字符串——否则
// `<root>/link/../..` 这类写法、指向根外的 junction、以及不同拼写指向同一文件
// 的情形都会让"许可范围"失去意义。
//
// 边界声明：这不是 OS 沙箱。bash 里的一条 `cd /` 不经过这里；基线也不要求本层
// 提供 shell 隔离（SPEC §6 明确"不宣称 shell OS 隔离"）。这里守的是文件资源范围，
// 以及"审批绑定的是哪个具体文件"。
type PathGuard struct {
	roots []string // 已解析的真实根路径
	// realPath 可替换，仅用于验证错误分支；生产恒为 resolveReal。
	realPath func(string) (string, error)
}

// PathDecision 是一次路径校验的结果。
type PathDecision struct {
	// Requested 是调用方给的原始字符串。
	Requested string `json:"requested"`
	// Real 是解析链接后的真实路径，即真正会被读写的资源。
	Real string `json:"real"`
	// Root 是命中的授权根（也是真实路径）。
	Root string `json:"root"`
}

// NewPathGuard 用一组根目录构造守卫。根本身必须是存在的目录。
//
// 根也走真实解析：调用方传进来的根若是一个链接，后续比较必须用它的真实路径，
// 否则"根内"的判定会与文件系统事实不一致。
func NewPathGuard(roots ...string) (*PathGuard, error) {
	if len(roots) == 0 {
		return nil, contracts.NewAppError(contracts.CodeInternal, "路径守卫至少需要一个根目录", false)
	}
	seen := make(map[string]bool, len(roots))
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "根目录无法绝对化: "+r+": "+err.Error(), false)
		}
		real, err := resolveReal(abs)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "根目录无法解析: "+r+": "+err.Error(), false)
		}
		fi, err := os.Stat(real)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "根目录不可用: "+r+": "+err.Error(), false)
		}
		if !fi.IsDir() {
			return nil, contracts.NewAppError(contracts.CodeInternal, "根目录不是目录: "+r, false)
		}
		key := foldKey(real)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, real)
	}
	if len(out) == 0 {
		return nil, contracts.NewAppError(contracts.CodeInternal, "路径守卫没有有效根目录", false)
	}
	return &PathGuard{roots: out, realPath: resolveReal}, nil
}

// Roots 返回已解析的根（只读副本），用于诊断与证据。
func (g *PathGuard) Roots() []string {
	out := make([]string, len(g.roots))
	copy(out, g.roots)
	return out
}

// Check 校验目标路径是否在某个根内，返回真实路径。
//
// 拒绝的情形（都返回 UNAUTHORIZED，且不产生任何副作用）：
//   - 空路径、含 NUL
//   - Windows 上的 UNC / 设备路径（\\server\share、\\?\C:\...、\\.\...）
//   - Windows 上的驱动器相对路径（C:foo，目标依赖进程 cwd，不可预测）
//   - Windows 上的备用数据流（file.txt:stream，写的是同一文件的隐藏流）
//   - 解析链接后落在所有根之外（含"目标不存在但父目录是根外链接"）
func (g *PathGuard) Check(target string) (PathDecision, error) {
	dec := PathDecision{Requested: target}
	if strings.TrimSpace(target) == "" {
		return dec, denyPath("路径为空")
	}
	if strings.ContainsRune(target, 0) {
		return dec, denyPath("路径包含 NUL 字节")
	}
	if err := lexicalReject(target); err != nil {
		return dec, err
	}

	abs, err := filepath.Abs(target)
	if err != nil {
		return dec, denyPath("路径无法绝对化: " + target)
	}
	dec.Real, err = g.realPath(abs)
	if err != nil {
		return dec, denyPath("路径无法解析（" + target + "）: " + err.Error())
	}

	for _, root := range g.roots {
		if withinRoot(dec.Real, root) {
			dec.Root = root
			return dec, nil
		}
	}
	return dec, denyPath("路径越界：" + dec.Real + " 不在任何授权根内")
}

// Trusted 判断目标是否在授权范围内，不返回细节，便于做布尔门禁。
func (g *PathGuard) Trusted(target string) bool {
	_, err := g.Check(target)
	return err == nil
}

func denyPath(msg string) error {
	return contracts.NewAppError(contracts.CodeUnauthorized, msg, false)
}

// lexicalReject 做纯词法层面的拒绝，必须在解析之前执行。
//
// 这些写法不是"另一个等价路径"，而是让解析结果与字面意思脱钩的构造：
// `\\?\` 前缀会关闭 Win32 的 `..` 规范化，UNC 可指向任意主机的共享。
func lexicalReject(raw string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	// UNC / 设备命名空间：\\server\share、\\?\C:\x、\\.\PhysicalDrive0。
	// 也覆盖正斜杠写法 //server/share（Win32 同样接受）。
	if strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, "//") {
		return denyPath("不允许 UNC/设备路径: " + raw)
	}
	// 驱动器相对路径：C:foo 依赖进程当前目录，解析结果不可预测。
	if len(raw) >= 2 && raw[1] == ':' {
		if len(raw) == 2 || (raw[2] != '\\' && raw[2] != '/') {
			return denyPath("不允许驱动器相对路径: " + raw)
		}
		// 备用数据流：C:\root\a.txt:hidden 写的是同一文件的隐藏流，
		// 但字面路径看起来仍在根内，必须单独拦。
		for _, seg := range strings.Split(raw[2:], `\`) {
			if strings.Contains(seg, ":") {
				return denyPath("不允许备用数据流路径: " + raw)
			}
		}
	}
	return nil
}

// withinRoot 判断 real 是否在 root 之内（含 root 自身）。
//
// 逐段比较而不是字符串前缀：`C:\work` 是 `C:\workspace` 的字符串前缀但并不是它的父目录。
// Windows 与默认大小写不敏感的 macOS 上按段忽略大小写比较。
func withinRoot(real, root string) bool {
	rseg, rvol := splitPath(real)
	rootseg, rootvol := splitPath(root)
	if foldKey(rvol) != foldKey(rootvol) {
		return false
	}
	if len(rseg) < len(rootseg) {
		return false
	}
	for i := range rootseg {
		if foldKey(rseg[i]) != foldKey(rootseg[i]) {
			return false
		}
	}
	return true
}

func splitPath(p string) (segments []string, volume string) {
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, vol)
	rest = strings.Trim(rest, `/\`)
	if rest == "" {
		return nil, vol
	}
	return strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }), vol
}

// foldKey 归一化用于比较的字符串：Windows 与 macOS 文件系统默认大小写不敏感。
func foldKey(s string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(s)
	}
	return s
}

// resolveReal 把路径解析成真实路径（跟随符号链接与目录联接）。
//
// 逐段处理而不是直接用 filepath.EvalSymlinks：目标可能尚不存在（写新文件），
// 而 EvalSymlinks 一旦遇到不存在的段就整体失败，回退到"最近存在的祖先"会把
// **悬空链接**当成普通文件名——写进去实际落在链接指向的根外位置。
// 这个漏洞是靠 TestPathGuardRejectsDanglingSymlinkOutside 抓出来的。
func resolveReal(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := resolveSegments(filepath.Clean(abs), 0)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// maxLinkDepth 限制链接展开层数，防止自指/环链把进程拖死。
const maxLinkDepth = 40

func resolveSegments(abs string, depth int) (string, error) {
	if depth > maxLinkDepth {
		return "", errors.New("符号链接层数过多（可能存在链接环）")
	}
	vol := filepath.VolumeName(abs)
	rest := strings.Trim(strings.TrimPrefix(abs, vol), `/\`)
	cur := vol + string(filepath.Separator)
	if vol == "" {
		cur = string(filepath.Separator)
	}
	if rest == "" {
		return cur, nil
	}
	segs := strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' })

	for i := 0; i < len(segs); i++ {
		next := filepath.Join(cur, segs[i])
		fi, err := os.Lstat(next)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
			// 从这里开始都不存在：字面拼回，继续由根比较判定归属。
			// 注意此处 cur 已经是"已解析的真实祖先"，所以根外链接不会在这里被洗白。
			return filepath.Join(append([]string{cur}, segs[i:]...)...), nil
		}
		// 不能只看 ModeSymlink：Windows 的目录联接（junction）在 Go 的 Lstat 里
		// 可能不报 ModeSymlink，但内核照样会把它解析到目标目录（穿透访问成功）。
		// 只信 Mode 会漏掉整类越界——这条是 TestPathGuardRejectsJunctionOutside 抓出来的。
		if linkTarget, isLink := readLinkIfLink(next, fi); isLink {
			linkTarget = stripNTPrefix(linkTarget)
			if !filepath.IsAbs(linkTarget) {
				linkTarget = filepath.Join(cur, linkTarget)
			}
			resolved, err := resolveSegments(filepath.Clean(linkTarget), depth+1)
			if err != nil {
				return "", err
			}
			cur = resolved
			continue
		}
		cur = next
	}
	return cur, nil
}

// readLinkIfLink 判断一个已存在的路径条目是否是链接（含 Windows 目录联接），
// 是则返回其目标。
//
// ModeSymlink 不足以判定：Windows 联接在 Go 的 Lstat 结果里可能只是一个普通目录
// （实测 mode 无 ModeSymlink），但内核仍会穿透到目标目录。因此对非 ModeSymlink 的
// 条目也试探一次 Readlink——链接必然成功，普通目录必定失败。
func readLinkIfLink(p string, fi os.FileInfo) (string, bool) {
	if fi.Mode()&os.ModeSymlink != 0 {
		if t, err := os.Readlink(p); err == nil && t != "" {
			return t, true
		}
		return "", false
	}
	if t, err := os.Readlink(p); err == nil && t != "" {
		return t, true
	}
	return "", false
}

// stripNTPrefix 去掉 Win32/NT 对象路径前缀。
// Readlink 在不同实现下可能给出 `\??\C:\x` 或 `\\?\C:\x`；不剥掉会被当成相对路径的怪段。
func stripNTPrefix(p string) string {
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		return `\\` + strings.TrimPrefix(p, `\\?\UNC\`)
	case strings.HasPrefix(p, `\\?\`):
		return strings.TrimPrefix(p, `\\?\`)
	case strings.HasPrefix(p, `\??\`):
		return strings.TrimPrefix(p, `\??\`)
	}
	return p
}
