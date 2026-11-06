package pi

// F08-D：契约探测的**启动形状**。
//
// 缺陷背景（2026-11-06 实测）：contractProber.start 不给 WorkerSpec.Args，
// 于是真实 pi 以默认 text 模式启动；stdin 里的 JSON 命令不被当作 RPC 处理，
// 六项检查全部超时——一个完全可用的 Pi 被判 incompatible。
//
// 本文件只测"交给 worker 的 argv 长什么样"，不启动真实 Pi：
// 真实二进制的端到端探测见 contract_realpi_test.go。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureStarter 捕获每次启动的 WorkerSpec，并立刻返回错误让检查快速失败。
//
// 返回错误而不是假 worker：本用例只关心 argv；探测的六项检查会各自启动一次，
// 因此还能顺带确认"每项检查都拿到同一份 argv"。
func captureStarter() (WorkerStarter, func() []WorkerSpec) {
	var mu sync.Mutex
	var specs []WorkerSpec
	starter := func(_ context.Context, spec WorkerSpec) (Worker, error) {
		mu.Lock()
		specs = append(specs, spec)
		mu.Unlock()
		return nil, errors.New("capture-only: 不真的启动 worker")
	}
	return starter, func() []WorkerSpec {
		mu.Lock()
		defer mu.Unlock()
		return append([]WorkerSpec(nil), specs...)
	}
}

func TestContractProbeLaunchesWorkerInRPCMode(t *testing.T) {
	starter, captured := captureStarter()
	inst := contractInstallation(t, "1.5.0")
	opts := contractOptions(t, starter)
	opts.Timeout = 5 * time.Second
	opts.CheckTimeout = time.Second

	report, err := ContractProbe(context.Background(), inst, opts)
	if err != nil {
		t.Fatalf("探测本身不应报错（启动失败要写进报告）: %v", err)
	}
	// fail closed：一项都跑不起来时必须是 incompatible，不是"没测就放行"。
	if report.State != StateIncompatible {
		t.Fatalf("六项检查全部启动失败时状态 = %q，期望 incompatible", report.State)
	}

	specs := captured()
	if len(specs) != len(ContractChecks) {
		t.Fatalf("启动次数 = %d，期望 %d（每项检查各启动一次）", len(specs), len(ContractChecks))
	}

	for i, spec := range specs {
		args := spec.Args
		if len(args) < 2 || args[0] != "--mode" || args[1] != "rpc" {
			t.Fatalf("第 %d 次启动的 argv 前两位 = %v，必须是 [--mode rpc]：不带它 Pi 进 text 模式，RPC 命令永远没有响应", i+1, args)
		}

		// 隔离参数必须由产品自己的校验器判定完整：这里不重写一份判据。
		if err := ValidateArgs(args, IsolationSpec{HarnessMCP: true}); err != nil {
			t.Fatalf("第 %d 次启动的隔离参数不完整: %v（argv=%v）", i+1, err, args)
		}
		if !containsPair(args, "-e", "builtin:mcp") {
			t.Fatalf("第 %d 次启动缺少 `-e builtin:mcp`：-ne 会连带关掉内置 MCP，mcp 检查必然失败（argv=%v）", i+1, args)
		}

		// 会话必须落在隔离 agentDir 下，否则 Pi 会把会话写到自己的默认位置。
		wantSessionDir := filepath.Join(opts.AgentDir, "sessions")
		if !containsPair(args, "--session-dir", wantSessionDir) {
			t.Fatalf("第 %d 次启动的 --session-dir 不是 %q（argv=%v）", i+1, wantSessionDir, args)
		}

		// 探测自己的薄扩展：必须是绝对路径、位于隔离目录内、且启动时真的存在。
		ext := extensionArgPath(args)
		if ext == "" {
			t.Fatalf("第 %d 次启动没有显式加载探测扩展（argv=%v）：扩展 hook 面无法证明可达", i+1, args)
		}
		if !filepath.IsAbs(ext) {
			t.Fatalf("探测扩展必须是绝对路径，实际 %q", ext)
		}
		if !pathWithin(ext, opts.AgentDir) {
			t.Fatalf("探测扩展 %q 不在隔离 agentDir %q 内", ext, opts.AgentDir)
		}
		body, readErr := os.ReadFile(ext)
		if readErr != nil {
			t.Fatalf("探测扩展在启动时必须真实存在（%s）: %v", ext, readErr)
		}
		if !strings.Contains(string(body), contractProbeCommand) {
			t.Fatalf("探测扩展内容里没有注册命令 %q：%s", contractProbeCommand, string(body))
		}

		// 每次启动都要带上本次检查的名字，诊断/负例依赖它区分 worker。
		if !strings.Contains(strings.Join(spec.Env, "\n"), "ANVIL_CONTRACT_CHECK=") {
			t.Fatalf("第 %d 次启动缺少 ANVIL_CONTRACT_CHECK 标记: %v", i+1, spec.Env)
		}
	}
}

// TestContractProbePassesProviderAndModel 验证探测回合的模型参数真的进了 argv。
//
// 为什么单测这一条：真实 Pi 在没有任何可用模型时直接拒绝 prompt（success=false），
// rpc/settled/abort 三项必然失败。模型参数没传下去，"探测失败"就会被误读成
// "这个 Pi 不兼容"。
func TestContractProbePassesProviderAndModel(t *testing.T) {
	starter, captured := captureStarter()
	inst := contractInstallation(t, "1.5.0")
	opts := contractOptions(t, starter)
	opts.Timeout = 5 * time.Second
	opts.CheckTimeout = time.Second
	opts.Provider = "fixture"
	opts.Model = "fixture-model"

	if _, err := ContractProbe(context.Background(), inst, opts); err != nil {
		t.Fatalf("探测本身不应报错: %v", err)
	}
	specs := captured()
	if len(specs) == 0 {
		t.Fatal("没有捕获到任何启动")
	}
	for i, spec := range specs {
		if !containsPair(spec.Args, "--provider", "fixture") {
			t.Fatalf("第 %d 次启动缺少 --provider fixture（argv=%v）", i+1, spec.Args)
		}
		if !containsPair(spec.Args, "--model", "fixture-model") {
			t.Fatalf("第 %d 次启动缺少 --model fixture-model（argv=%v）", i+1, spec.Args)
		}
	}

	// 不传模型时不得凭空补一个：探测不能假装有模型可用。
	plain, plainCapture := captureStarter()
	opts2 := contractOptions(t, plain)
	opts2.Timeout = 5 * time.Second
	opts2.CheckTimeout = time.Second
	if _, err := ContractProbe(context.Background(), inst, opts2); err != nil {
		t.Fatalf("探测本身不应报错: %v", err)
	}
	for i, spec := range plainCapture() {
		if containsArg(spec.Args, "--provider") || containsArg(spec.Args, "--model") {
			t.Fatalf("未指定模型时第 %d 次启动不得带 --provider/--model（argv=%v）", i+1, spec.Args)
		}
	}
}

// TestContractProbeForcesHarnessMCPIsolation 确认 mcp 检查所需的 `-e builtin:mcp`
// 不是调用方可选项：--no-mcp 下 mcp 能力面必然不可达，那不是被测 Pi 的问题。
func TestContractProbeForcesHarnessMCPIsolation(t *testing.T) {
	starter, captured := captureStarter()
	inst := contractInstallation(t, "1.5.0")
	opts := contractOptions(t, starter)
	// 调用方显式关掉 HarnessMCP：探测仍必须按 mcp 检查的要求把它打开。
	opts.Isolation = IsolationSpec{HarnessMCP: false}

	if _, err := ContractProbe(context.Background(), inst, opts); err != nil {
		t.Fatalf("探测本身不应报错: %v", err)
	}
	for i, spec := range captured() {
		if !containsPair(spec.Args, "-e", "builtin:mcp") {
			t.Fatalf("调用方关掉 HarnessMCP 时第 %d 次启动仍必须带 `-e builtin:mcp`（argv=%v）：--no-mcp 下 mcp 检查必然失败", i+1, spec.Args)
		}
		if containsArg(spec.Args, "--no-mcp") {
			t.Fatalf("探测不得带 --no-mcp（argv=%v）", spec.Args)
		}
	}
}

// ── 小工具 ──────────────────────────────────────────────────────────────────

// extensionArgPath 返回 argv 里除 builtin:* 之外的第一个 -e 值。
func extensionArgPath(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-e" && args[i] != "--extension" {
			continue
		}
		value := args[i+1]
		if strings.HasPrefix(value, "builtin:") {
			continue
		}
		return value
	}
	return ""
}

// pathWithin 报告 path 是否位于 dir 之内（按路径分量比较，不做字符串前缀比较）。
func pathWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
