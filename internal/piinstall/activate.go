package piinstall

// 本文件实现 F06 的激活与回滚（计划 F06 Step 5）。
//
// 布局：
//
//	<managed>/versions/<version>-<target>/pi(.exe)
//	<managed>/active.json              ← 唯一权威指针，原子替换
//	<managed>/staging/<installID>/      ← 下载/解压中间产物，失败即删
//
// 纪律：
//   - active.json 先写临时文件再 os.Rename（同目录内 rename 是原子的）；
//   - 只保留**一个**已验证旧版：previous 只有在目录还在、可执行体哈希仍等于
//     激活时记录的哈希时才算"可回滚"；
//   - 任何写入都必须落在托管根内：外部安装（Origin=external）永不被改写。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

const (
	// ActiveFileName 是权威指针文件名。
	ActiveFileName = "active.json"
	// ActiveSchemaVersion 是指针格式版本。
	ActiveSchemaVersion = 1
	// versionsDirName 是托管版本目录。
	versionsDirName = "versions"
	// stagingDirName 是下载/解压中间目录。
	stagingDirName = "staging"
)

// ActivePointer 是托管 Pi 的权威指针。
type ActivePointer struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
	TargetID      string `json:"targetId"`
	// VersionDir 是相对托管根的版本目录（versions/<version>-<target>）。
	VersionDir string `json:"versionDir"`
	// Executable 是版本目录内的可执行体相对路径。
	Executable string `json:"executable"`
	// ExecutableSHA256 是激活当时实测的可执行体哈希。
	ExecutableSHA256 string    `json:"executableSha256"`
	InstalledAt      time.Time `json:"installedAt"`
	// InstallationID 是 F08 口径的安装件 ID（pi-<hash>），供 UI 回滚时引用。
	InstallationID string `json:"installationId"`
	// Previous 是最多一个已验证旧版。
	Previous *PreviousVersion `json:"previous,omitempty"`
}

// PreviousVersion 是可回滚的旧版本记录。
type PreviousVersion struct {
	Version          string    `json:"version"`
	TargetID         string    `json:"targetId"`
	VersionDir       string    `json:"versionDir"`
	Executable       string    `json:"executable"`
	ExecutableSHA256 string    `json:"executableSha256"`
	InstalledAt      time.Time `json:"installedAt"`
}

// ReadActive 读回指针；文件不存在返回 (nil, nil)。
func ReadActive(managedRoot string) (*ActivePointer, error) {
	raw, err := os.ReadFile(filepath.Join(managedRoot, ActiveFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("piinstall: 读 active.json 失败: %w", err)
	}
	var p ActivePointer
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, contracts.NewAppError(contracts.CodeVerificationFailed,
			"active.json 不是合法 JSON: "+err.Error(), false)
	}
	if p.SchemaVersion != ActiveSchemaVersion {
		return nil, contracts.NewAppError(contracts.CodeVerificationFailed,
			fmt.Sprintf("active.json schemaVersion=%d，期望 %d", p.SchemaVersion, ActiveSchemaVersion), false)
	}
	return &p, nil
}

// WriteActive 原子替换指针（写临时文件 + rename）。
func WriteActive(managedRoot string, p ActivePointer) error {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = ActiveSchemaVersion
	}
	if p.SchemaVersion != ActiveSchemaVersion {
		return contracts.NewAppError(contracts.CodeVerificationFailed,
			fmt.Sprintf("拒绝写出 schemaVersion=%d 的 active.json", p.SchemaVersion), false)
	}
	if err := EnsureInsideManagedRoot(managedRoot, filepath.Join(managedRoot, filepath.FromSlash(p.VersionDir))); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("piinstall: 序列化 active.json 失败: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(managedRoot, 0o700); err != nil {
		return fmt.Errorf("piinstall: 创建托管根失败: %w", err)
	}
	tmp := filepath.Join(managedRoot, ActiveFileName+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("piinstall: 创建临时指针失败: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("piinstall: 写临时指针失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("piinstall: 同步临时指针失败: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("piinstall: 关闭临时指针失败: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(managedRoot, ActiveFileName)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("piinstall: 替换 active.json 失败: %w", err)
	}
	return nil
}

// ActiveExecutable 返回当前 active 可执行体的绝对路径，并核对哈希。
func ActiveExecutable(managedRoot string, p *ActivePointer) (string, error) {
	if p == nil {
		return "", contracts.NewAppError(contracts.CodeNotFound, "没有 active 的托管 Pi", false)
	}
	dir := filepath.Join(managedRoot, filepath.FromSlash(p.VersionDir))
	if err := EnsureInsideManagedRoot(managedRoot, dir); err != nil {
		return "", err
	}
	exe := filepath.Join(dir, filepath.FromSlash(p.Executable))
	if err := EnsureInsideManagedRoot(managedRoot, exe); err != nil {
		return "", err
	}
	sum, err := hashFileContent(exe)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeNotFound, "active 可执行体不可读: "+err.Error(), false)
	}
	if p.ExecutableSHA256 != "" && !strings.EqualFold(sum, p.ExecutableSHA256) {
		return "", contracts.NewAppError(contracts.CodeVerificationFailed,
			fmt.Sprintf("active 可执行体哈希 %s 与记录的 %s 不一致", sum, p.ExecutableSHA256), false)
	}
	return exe, nil
}

// RollbackTarget 返回可回滚的旧版记录与它的可执行体路径。
//
// 只有"目录还在 + 可执行体哈希仍等于激活时记录的哈希"才算可回滚：
// 文件被换掉/被删掉的旧版不能当回滚目标（回滚到一个被篡改的二进制比不回滚更糟）。
func RollbackTarget(managedRoot string, p *ActivePointer) (*PreviousVersion, string, error) {
	if p == nil || p.Previous == nil {
		return nil, "", contracts.NewAppError(contracts.CodeUnsupported, "当前没有可回滚的旧版本", false)
	}
	prev := p.Previous
	dir := filepath.Join(managedRoot, filepath.FromSlash(prev.VersionDir))
	if err := EnsureInsideManagedRoot(managedRoot, dir); err != nil {
		return nil, "", err
	}
	exe := filepath.Join(dir, filepath.FromSlash(prev.Executable))
	if err := EnsureInsideManagedRoot(managedRoot, exe); err != nil {
		return nil, "", err
	}
	sum, err := hashFileContent(exe)
	if err != nil {
		return nil, "", contracts.NewAppError(contracts.CodeVerificationFailed,
			"旧版本可执行体不可读: "+err.Error(), false)
	}
	if !strings.EqualFold(sum, prev.ExecutableSHA256) {
		return nil, "", contracts.NewAppError(contracts.CodeVerificationFailed,
			fmt.Sprintf("旧版本可执行体哈希 %s 与激活时记录的 %s 不一致，拒绝回滚", sum, prev.ExecutableSHA256), false)
	}
	return prev, exe, nil
}

// EnsureInsideManagedRoot 拒绝任何落在托管根之外的路径。
//
// 这条守卫是"外部 Pi 不可变"的机械保证：激活、回滚、清理都只可能写托管根。
func EnsureInsideManagedRoot(managedRoot, target string) error {
	root, err := filepath.Abs(managedRoot)
	if err != nil {
		return fmt.Errorf("piinstall: 托管根无法绝对化: %w", err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("piinstall: 目标路径无法绝对化: %w", err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return contracts.NewAppError(contracts.CodeConfigConflict,
			"目标路径不在托管根内: "+abs, false)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return contracts.NewAppError(contracts.CodeConfigConflict,
			fmt.Sprintf("拒绝写托管根之外的路径: %s（托管根 %s）", abs, root), false)
	}
	return nil
}

// versionDirName 返回版本目录名 <version>-<target>。
func versionDirName(version, target string) string { return version + "-" + target }

// splitVersionDir 从 <version>-<target> 拆出版本。
func splitVersionDir(dir string) (string, bool) {
	for _, t := range TargetIDs {
		suffix := "-" + t
		if strings.HasSuffix(dir, suffix) {
			v := strings.TrimSuffix(dir, suffix)
			if versionPattern.MatchString(v) {
				return v, true
			}
		}
	}
	return "", false
}

// managedInstallation 把 active 指针转成 F08 的安装件记录（供 Discover/UI 复用）。
func managedInstallation(managedRoot string, p *ActivePointer, exe string) pi.PiInstallation {
	return pi.PiInstallation{
		ID:           p.InstallationID,
		Executable:   exe,
		Version:      p.Version,
		Origin:       pi.OriginManaged,
		Architecture: hostArchForTarget(p.TargetID),
		Source:       pi.SourceManaged,
		Runnable:     true,
	}
}

// hostArchForTarget 把目标 ID 映射成架构名（"any" 只在无法判断时使用）。
func hostArchForTarget(target string) string {
	switch {
	case strings.HasSuffix(target, "-x64"):
		return "amd64"
	case strings.HasSuffix(target, "-arm64"):
		return "arm64"
	}
	return "any"
}
