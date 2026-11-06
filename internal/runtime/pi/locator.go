package pi

// 本文件实现 Pi 安装件定位（执行基线 §8）。
//
// 硬约束：
//   - 发现优先级：用户指定 → 已保存路径 → PATH → 常见位置 → 托管目录。
//   - 多个有效版本必须分别列出并显示来源，**选定后禁止静默切换**（本函数只负责列出，
//     不替调用方做选择）。
//   - 绝不把用户字符串拼接成 shell 命令；一律按 argv 直接执行。
//     实测 Windows 下 Go 可直接执行 .cmd shim，因此版本探测不需要任何 shell。
//   - .ps1 shim 受 PowerShell 执行策略约束（本机实测禁止），记为不可直接执行，不假装可用。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

// versionProbeTimeout 是单次版本探测的上限。只查版本，不进入 agent loop。
const versionProbeTimeout = 5 * time.Second

// semverPattern 从输出里提取形如 1.5.0 的版本号。
var semverPattern = regexp.MustCompile(`\b(\d+\.\d+\.\d+)\b`)

// isWindows 判断当前平台。
func isWindows() bool { return runtime.GOOS == "windows" }

// hostArch 返回宿主架构，用于原生可执行文件。
func hostArch() string { return runtime.GOARCH }

// executableNames 返回当前平台允许的 Pi 可执行文件名（按优先级）。
func executableNames() []string {
	if isWindows() {
		return []string{"pi.exe", "pi.cmd", "pi.bat"}
	}
	return []string{"pi"}
}

// Locate 定位本机所有可用的 Pi 安装件。
//
// preferred 为"用户指定或已保存路径"（可为文件、目录或空）。若 preferred 非空但无效，
// 仍返回其余发现结果，同时返回 NotFound 错误，便于 UI 提示"已保存路径失效"并让用户重选。
// 无任何 Pi 时返回空列表与 nil 错误（NotFound 是探测状态，不是调用失败）。
func Locate(ctx context.Context, preferred string) ([]PiInstallation, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	var (
		out     []PiInstallation
		seen    = map[string]bool{}
		prefErr error
	)
	add := func(inst PiInstallation) {
		key := strings.ToLower(inst.Executable)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, inst)
	}

	// 1) 用户指定 / 已保存路径（最高优先级）
	if strings.TrimSpace(preferred) != "" {
		inst, found, err := inspectCandidate(ctx, preferred, SourceUserSpecified)
		switch {
		case err != nil:
			prefErr = err
		case found:
			add(inst)
		default:
			prefErr = notFoundError("用户指定/已保存的 Pi 路径不可用: " + preferred)
		}
	}

	// 2) PATH
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if inst, found, _ := inspectCandidate(ctx, dir, SourcePATH); found || inst.Executable != "" {
			add(inst)
		}
	}

	// 3) 常见位置（只列已知目录，不做全盘扫描）
	for _, dir := range commonLocations() {
		if inst, found, _ := inspectCandidate(ctx, dir, SourceCommon); found || inst.Executable != "" {
			add(inst)
		}
	}

	// 4) 托管位置（客户端自己安装的副本，可升级/回滚）
	for _, dir := range managedLocations() {
		if inst, found, _ := inspectCandidate(ctx, dir, SourceManaged); found || inst.Executable != "" {
			add(inst)
		}
	}

	if len(out) == 0 && prefErr == nil {
		return nil, nil
	}
	return out, prefErr
}

// CommonLocations 返回各平台常见的 Pi 安装位置（供 F08 Discover 复用）。
func CommonLocations() []string { return commonLocations() }

// ManagedRoot 返回托管 Pi 根目录（供 F08 Discover 复用；F06 负责实际填充）。
func ManagedRoot() string { return managedRoot() }

// commonLocations 返回各平台常见的 Pi 安装位置。
func commonLocations() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".pi", "bin"),
		)
	}
	if isWindows() {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs,
				filepath.Join(local, "pi-node", "current"),
				filepath.Join(local, "Programs", "pi"),
			)
		}
		if appData := os.Getenv("APPDATA"); appData != "" {
			dirs = append(dirs, filepath.Join(appData, "npm"))
		}
	} else {
		dirs = append(dirs, "/usr/local/bin", "/usr/bin")
	}
	return dirs
}

// managedLocations 返回托管根目录下的各版本目录。
func managedLocations() []string {
	root := managedRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	return dirs
}

// managedRoot 是托管 Pi 根目录，可用 MYAGENT_PI_HOME 覆盖（测试注入隔离目录）。
func managedRoot() string {
	if v := os.Getenv("MYAGENT_PI_HOME"); v != "" {
		return v
	}
	if isWindows() {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "MyAgent", "pi")
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "myagent", "pi")
}

// inspectCandidate 检查一个候选路径（文件或目录）。
func inspectCandidate(ctx context.Context, candidate string, source Source) (PiInstallation, bool, error) {
	info, err := os.Stat(candidate)
	if err != nil {
		if source == SourceUserSpecified || source == SourceSavedPath {
			return PiInstallation{}, false, notFoundError("路径不可访问: " + candidate)
		}
		return PiInstallation{}, false, nil
	}

	if info.IsDir() {
		for _, name := range executableNames() {
			p := filepath.Join(candidate, name)
			if st, serr := os.Stat(p); serr == nil && !st.IsDir() {
				return buildInstallation(ctx, p, source), true, nil
			}
		}
		// 只有 .ps1：给出明确诊断，不静默忽略、不假装可用。
		if isWindows() {
			p := filepath.Join(candidate, "pi.ps1")
			if st, serr := os.Stat(p); serr == nil && !st.IsDir() {
				return PiInstallation{
					ID:           installationID(p),
					Executable:   p,
					Origin:       originFor(source),
					Architecture: "any",
					Source:       source,
					Runnable:     false,
					Note:         "仅有 PowerShell shim（pi.ps1），受执行策略约束，不可直接执行",
				}, false, nil
			}
		}
		return PiInstallation{}, false, nil
	}

	return buildInstallation(ctx, candidate, source), true, nil
}

// buildInstallation 探测版本并构造安装件记录。
func buildInstallation(ctx context.Context, path string, source Source) PiInstallation {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	real := abs
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		real = r
	}

	arch := hostArch()
	ext := strings.ToLower(filepath.Ext(real))
	if ext == ".cmd" || ext == ".bat" || ext == ".ps1" {
		// 脚本型 shim 不绑定具体架构。
		arch = "any"
	}

	version, probeErr := probeVersion(ctx, real)
	note := ""
	if probeErr != nil {
		note = probeErr.Error()
	}

	return PiInstallation{
		ID:           installationID(real),
		Executable:   real,
		Version:      version,
		Origin:       originFor(source),
		Architecture: arch,
		Source:       source,
		Runnable:     true,
		Note:         note,
	}
}

// originFor 依据来源给出归属：托管目录视为 managed，其余为 external。
func originFor(source Source) Origin {
	if source == SourceManaged {
		return OriginManaged
	}
	return OriginExternal
}

// probeVersion 直接执行可执行文件查询版本，不经任何 shell。
func probeVersion(ctx context.Context, exe string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()

	out, err := runVersionCommand(cctx, exe, "--version")
	if err != nil {
		// 有的版本只认 -v。
		out, err = runVersionCommand(cctx, exe, "-v")
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return "", contracts.NewAppError(contracts.CodeDependencyMissing, "Pi 版本探测超时: "+exe, true)
			}
			return "", contracts.NewAppError(contracts.CodeDependencyMissing, "Pi 无法执行或缺少运行时依赖: "+err.Error(), true)
		}
	}
	if m := semverPattern.FindStringSubmatch(out); len(m) == 2 {
		return m[1], nil
	}
	return "", contracts.NewAppError(contracts.CodeDependencyMissing, "Pi 版本输出无法识别: "+strings.TrimSpace(out), true)
}

// runVersionCommand 直接执行（argv 形式，不拼 shell 字符串）。
func runVersionCommand(ctx context.Context, exe string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// installationID 由规范化路径派生稳定 ID。
func installationID(real string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(real)))
	return "pi-" + hex.EncodeToString(sum[:6])
}

// IsNotFound 判断错误是否为 NotFound 类。
func IsNotFound(err error) bool {
	var appErr *contracts.AppError
	if errors.As(err, &appErr) {
		return appErr.Code == contracts.CodeNotFound
	}
	return false
}
