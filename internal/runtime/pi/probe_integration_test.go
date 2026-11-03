package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hashFile 计算文件 sha256（oracle：用户配置 marker 前后必须一致）。
func hashFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 marker 失败: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// makeVersionShim 创建一个只响应 --version 的 .cmd fixture（不触碰模型/MCP）。
func makeVersionShim(t *testing.T, dir, version string) PiInstallation {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, "pi.cmd")
	content := "@echo off\r\nif \"%1\"==\"--version\" echo " + version + "\r\n"
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return PiInstallation{
		ID:         "fixture:" + p,
		Executable: p,
		Origin:     OriginExternal,
	}
}

// TestProbeHasNoUserConfigSideEffects 验证 PI02：隔离 agentDir 下 probe，
// 不访问真实 provider、不启动用户 MCP、用户配置 marker 哈希不变，且 probe 在限定时间内返回
// （未进入 agent loop / 未发起模型请求）。
func TestProbeHasNoUserConfigSideEffects(t *testing.T) {
	// 模拟真实用户 HOME，并在其中放一个 marker 文件（代表既有用户配置）。
	userHome := t.TempDir()
	markerDir := filepath.Join(userHome, ".pi", "agent")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatalf("mkdir marker: %v", err)
	}
	markerPath := filepath.Join(markerDir, "config.marker")
	if err := os.WriteFile(markerPath, []byte("user-config-sentinel-"+time.Now().Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	before := hashFile(t, markerPath)

	// 独立 agentDir：probe 的子进程配置必须落在这里，而不是真实用户 HOME。
	agentDir := t.TempDir()
	oldIso := ProbeIsolationDir
	ProbeIsolationDir = agentDir
	defer func() { ProbeIsolationDir = oldIso }()

	// 把进程级 HOME/USERPROFILE/APPDATA 指向 userHome，模拟“这是用户真实 home”。
	oldHome := os.Getenv("HOME")
	oldUP := os.Getenv("USERPROFILE")
	oldAD := os.Getenv("APPDATA")
	os.Setenv("HOME", userHome)
	os.Setenv("USERPROFILE", userHome)
	os.Setenv("APPDATA", userHome)
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("USERPROFILE", oldUP)
		os.Setenv("APPDATA", oldAD)
	}()

	inst := makeVersionShim(t, filepath.Join(t.TempDir(), "bin"), "1.5.0")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := Probe(ctx, inst)
	if err != nil {
		t.Fatalf("Probe 报错: %v（state=%s）", err, res.State)
	}
	// probe 不得进入 agent loop：必须在超时前完成（fixture 仅响应 --version）。
	if res.State != StateReady {
		t.Fatalf("认证版本 1.5.0 fixture 应 Ready，实际 state=%s diag=%v", res.State, res.Diagnostics)
	}
	if res.Version != "1.5.0" {
		t.Fatalf("版本应解析为 1.5.0，实际 %q", res.Version)
	}

	// oracle 1：marker 哈希不变。
	after := hashFile(t, markerPath)
	if after != before {
		t.Fatalf("用户配置 marker 哈希发生变化（probe 产生副作用）：before=%s after=%s", before, after)
	}
	// oracle 2：用户 HOME 下除 marker 外不得出现新文件。
	entries, _ := os.ReadDir(userHome)
	for _, e := range entries {
		if e.IsDir() && e.Name() == ".pi" {
			sub, _ := os.ReadDir(markerDir)
			for _, f := range sub {
				if f.Name() != "config.marker" {
					t.Fatalf("用户 HOME 下出现意外文件: %s（probe 疑似写入用户配置）", f.Name())
				}
			}
			continue
		}
		t.Fatalf("用户 HOME 根目录出现意外条目: %s（probe 疑似写入用户配置）", e.Name())
	}
	// oracle 3：agentDir 应为 probe 实际隔离目录（证明配置被重定向到隔离区）。
	if _, statErr := os.Stat(agentDir); statErr != nil {
		t.Fatalf("隔离 agentDir 未被创建/使用: %v", statErr)
	}
}

// TestProbePs1NotDirectlyExecutable 验证：.ps1 在执行策略禁止（不带 -ExecutionPolicy Bypass）
// 时，Probe 返回“不可直接执行”的明确诊断，而不是假装可用。
func TestProbePs1NotDirectlyExecutable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pi.ps1")
	// 一个正常内容；重点在于执行策略禁止时它无法运行。
	content := "param()\r\nif ($args -contains '--version') { Write-Output '1.5.0' }\r\n"
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatalf("write ps1: %v", err)
	}
	inst := PiInstallation{ID: "fixture:" + p, Executable: p, Origin: OriginExternal}

	oldBypass := PSBypassPolicy
	PSBypassPolicy = false // 模拟执行策略禁止（不带 Bypass）
	defer func() { PSBypassPolicy = oldBypass }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := Probe(ctx, inst)
	// 这里期望“无法执行”，但不应 panic；状态应为 ProbeFailed 且诊断说明不可直接执行。
	if err == nil && res.State == StateReady {
		t.Fatalf("执行策略禁止时 .ps1 不应标 Ready；state=%s", res.State)
	}
	if res.State != StateProbeFailed {
		t.Fatalf("执行策略禁止应判 ProbeFailed，实际 state=%s err=%v", res.State, err)
	}
	joined := strings.Join(res.Diagnostics, " ") + " " + errString(err)
	if !strings.Contains(joined, "不可直接执行") && !strings.Contains(joined, "执行策略") {
		t.Fatalf("诊断未说明不可直接执行/执行策略禁止；diag=%v err=%v", res.Diagnostics, err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
