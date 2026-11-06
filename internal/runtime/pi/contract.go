package pi

// 本文件实现 F08-B 的 Pi 契约探测（PI_COMP01–04）。
//
// 它回答的问题只有一个：**这个可执行体真的能按我们依赖的协议跑完整回合吗**。
// 六项检查各失败各的、各具名各的，任何一项失败都返回 incompatible 并把失败项写进
// FailedChecks——不允许"部分通过就放行"，也不允许把超时当成通过。
//
// 纪律：
//   - 一切探测都在隔离 AgentDir/WorkspaceDir 里跑：绝不读写用户真实 Pi 配置。
//   - 只证明协议行为；不证明"这是官方 Pi"。版本/哈希认证由 PreCertifiedHashes 决定。
//   - 协议形状以真实捕获（internal/runtime/pi/testdata/v1.5.0/*.jsonl）为准：
//     握手用捕获里真实存在的 get_commands，扩展/MCP 能力面看它的 commands 记录，
//     完成信号只认 agent_settled 且必须带布尔 aborted。
//   - 探测必须以 `--mode rpc` 启动（见 launchArgs）：不带它 Pi 进 text 模式，
//     六项检查全部超时，真实可用的 Pi 会被判 incompatible。
//   - FixtureHash 只证明 testdata 未漂移，**不**认证本机可执行体。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
)

const (
	defaultContractTimeout      = 60 * time.Second
	defaultContractCheckTimeout = 10 * time.Second
	// contractProbeEnvironment 声明探测确实发生在隔离 agentDir 里。
	contractProbeEnvironment = "isolated-agentdir"

	// contractProbeExtensionDir 是隔离 agentDir 下存放探测扩展的子目录。
	//
	// 放在 agentDir 而不是工作区：工作区是 Pi 的 cwd，往里面写文件会改变
	// "探测看到的工作区"（以及任何按工作区快照做的断言）。
	contractProbeExtensionDir = "anvil-contract-probe"
	// contractProbeExtensionName 是探测自己加载的薄扩展文件名。
	//
	// 为什么探测必须显式加载一个绝对路径的扩展：产品运行路径就是这样加载
	// pi-extensions/harness-policy.ts（执行前授权）。这条链路断了，产品就只剩
	// "模型自觉"这一层保护，必须判 incompatible，而不是等用户第一次运行才发现。
	contractProbeExtensionName = "anvil-contract-probe.ts"
	// contractProbeCommand 是该扩展注册的命令名，也是扩展 hook 面的观察值。
	contractProbeCommand = "anvil-contract-probe"
)

// contractProbeExtensionSource 是运行时写进隔离目录的薄扩展。
//
// 只注册一条命令：不订阅事件、不碰网络、不改任何行为——探测只回答
// "绝对路径扩展能不能被加载并把命令注册出来"。刻意不写 import：独立包与
// npm 形式都能直接加载它，不依赖宿主 Node 的模块解析。
const contractProbeExtensionSource = `// Anvil 契约探测扩展（运行时生成，只存在于隔离临时目录）
export default function (pi) {
	pi.registerCommand("` + contractProbeCommand + `", {
		description: "Anvil contract probe extension (isolation check only)",
		handler: async () => {},
	});
}
`

// ContractChecks 是六项契约检查的名字与报告顺序。
var ContractChecks = []string{"rpc", "settled", "mcp", "extension", "abort", "cleanup"}

// WorkerStarter 是启动 worker 的注入点（生产用 StartWorker，测试注入 fakeworker）。
type WorkerStarter func(ctx context.Context, spec WorkerSpec) (Worker, error)

// ContractOptions 描述一次契约探测的隔离位置与时限。
type ContractOptions struct {
	// AgentDir 是隔离的 PI_CODING_AGENT_DIR；为空直接拒绝（不能碰用户真实配置）。
	AgentDir string
	// WorkspaceDir 是 worker 的工作目录；为空时退回 AgentDir。
	WorkspaceDir string
	// Timeout 是整次探测的总时限，默认 60s。
	Timeout time.Duration
	// CheckTimeout 是单项检查的时限，默认 10s。超时是失败，不是通过。
	CheckTimeout time.Duration
	// Isolation 是探测使用的隔离参数（项目级可执行资源一律切断）。
	//
	// 调用方只能加东西（ExtraExtensions/Skills/PolicyExtension）；HarnessMCP 一律
	// 按 true 处理：mcp 检查要求内置 MCP 扩展真的可达，--no-mcp 下它必然不可达，
	// 那说明的是参数配错，不是被测 Pi 不兼容。
	Isolation IsolationSpec
	// Provider/Model 是探测回合使用的模型。
	//
	// 真实 Pi 在没有任何可用模型时会直接拒绝 prompt（response success=false），
	// rpc/settled/abort 三项必然失败。为空表示不传——探测照样跑，但真实二进制的
	// 结论会是 incompatible（fail closed），绝不因为"没配模型"就跳过那三项。
	Provider string
	Model    string
	// WorkerStarter 默认 StartWorker。
	WorkerStarter WorkerStarter
}

// ContractReport 是一次完整契约探测的结论（PI_COMP01–04）。
type ContractReport struct {
	Version string `json:"version"`
	// State 只可能是 ready_compatible（六项全过）或 incompatible（任一项失败）。
	// ready_verified 由 Discover 结合版本/哈希清单判定，本层不产生。
	State        CompatibilityState `json:"state"`
	Capabilities []string           `json:"capabilities"` // rpc, settled, mcp, extension, abort, cleanup
	FailedChecks []string           `json:"failedChecks,omitempty"`
	// FixtureHash 是已捕获 fixture 的哈希（只证明 testdata 未漂移）。
	FixtureHash string `json:"fixtureHash"`
	// Environment 例如 "isolated-agentdir"。
	Environment string `json:"environment"`
	// Observed 记录通过时实际观察到的能力来源（例如 MCP 来自哪条命令）。
	Observed []string `json:"observed,omitempty"`
}

// ContractProbe 在隔离 agentDir 里跑完整契约探测。
//
// 任何一项失败都返回 StateIncompatible 并把失败项写进 FailedChecks；
// 不允许"部分通过就放行"。error 只用于"探测根本无法进行"（例如没给 agentDir），
// 检查失败一律在报告里。
func ContractProbe(ctx context.Context, inst PiInstallation, opts ContractOptions) (ContractReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := ContractReport{
		Version:      inst.Version,
		State:        StateIncompatible,
		Capabilities: []string{},
		FixtureHash:  contractFixtureHash(inst.Version),
		Environment:  contractProbeEnvironment,
	}
	if strings.TrimSpace(opts.AgentDir) == "" {
		return report, contracts.NewAppError(contracts.CodeCompatibility,
			"契约探测必须指定隔离 agentDir（拒绝在用户真实配置目录里跑）", false)
	}
	opts = normalizeContractOptions(opts)
	if err := os.MkdirAll(opts.AgentDir, 0o755); err != nil {
		return report, contracts.NewAppError(contracts.CodeCompatibility,
			"无法创建隔离 agentDir: "+err.Error(), false)
	}
	if err := os.MkdirAll(opts.WorkspaceDir, 0o755); err != nil {
		return report, contracts.NewAppError(contracts.CodeCompatibility,
			"无法创建隔离工作目录: "+err.Error(), false)
	}
	if err := ctx.Err(); err != nil {
		return report, contracts.NewAppError(contracts.CodeRpcCancelled,
			"契约探测开始前上下文已取消: "+err.Error(), false)
	}

	probeCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	iso := opts.probeIsolation()
	// 隔离参数在这里就校验：少了必需 flag 是编程错误，不能变成"探测失败"
	// 让人以为是被测 Pi 的问题。
	if err := ValidateArgs(iso.Args(), iso); err != nil {
		return report, err
	}
	extPath, err := writeContractProbeExtension(opts.AgentDir)
	if err != nil {
		return report, err
	}
	iso.ExtraExtensions = append(iso.ExtraExtensions, extPath)

	// 探测回合必须有一个**它自己的**模型，不能借用用户的模型配置：
	//   - 用户可能一个模型都没配（新装机器），真实 Pi 会回
	//     "No API key found for the selected model."，rpc/settled/abort 三项必然失败，
	//     于是一个完全可用的 Pi 被判 incompatible（2026-11-06 实测）；
	//   - 就算配了，探测也不该拿用户的密钥去发真实请求：探测只回答协议问题。
	// 所以生产路径（Provider/Model 都没给）由探测自己在隔离 agentDir 里起一个
	// 只绑回环的 stub provider 并写 models.json；探测结束（含失败/取消）立即关闭。
	//
	// 调用方给了 Provider/Model 的场景（测试夹具、自定义端点）：models.json 归调用方
	// 所有，这里不写、也不覆盖。两者只给一个属于配置错误——那样 models.json 该由谁
	// 写就说不清了，静默挑一边会让"探测通过"取决于运气。
	providerSet := strings.TrimSpace(opts.Provider) != ""
	modelSet := strings.TrimSpace(opts.Model) != ""
	if providerSet != modelSet {
		return report, contracts.NewAppError(contracts.CodeConfigMissing,
			"契约探测的 Provider/Model 必须同时给出或同时留空（当前 provider="+opts.Provider+" model="+opts.Model+"）", false)
	}
	if !providerSet {
		stub, err := startContractStubProvider(opts.AgentDir)
		if err != nil {
			return report, err
		}
		defer stub.Close()
		opts.Provider = stub.ProviderID
		opts.Model = stub.ModelID
	}

	p := &contractProber{inst: inst, opts: opts, iso: iso}
	checks := []struct {
		name string
		run  func(context.Context) (string, error)
	}{
		{"rpc", p.checkRPC},
		{"settled", p.checkSettled},
		{"mcp", p.checkMCP},
		{"extension", p.checkExtension},
		{"abort", p.checkAbort},
		{"cleanup", p.checkCleanup},
	}
	for _, c := range checks {
		p.check = c.name
		checkCtx, checkCancel := context.WithTimeout(probeCtx, opts.CheckTimeout)
		observed, err := c.run(checkCtx)
		checkCancel()
		if err != nil {
			report.FailedChecks = append(report.FailedChecks, c.name+": "+err.Error())
			continue
		}
		report.Capabilities = append(report.Capabilities, c.name)
		if observed != "" {
			report.Observed = append(report.Observed, observed)
		}
	}
	if len(report.FailedChecks) == 0 {
		report.State = StateReadyCompatible
	}
	return report, nil
}

// defaultContractProbe 是生产路径的 ProbeFunc：在临时隔离目录里跑完整契约探测。
//
// 只在 Discover 既没注入 VersionProbe 也没注入 ProbeFunc 时使用（见 Discover）。
// 目录是临时创建的，探测结束即删除：发现/探测不写用户真实配置。
func defaultContractProbe(ctx context.Context, cand PiCandidate) (bool, []string) {
	root, err := os.MkdirTemp("", "anvil-pi-contract-")
	if err != nil {
		return false, []string{"kind:\"contract-probe-failed\" 无法创建隔离探测目录: " + err.Error()}
	}
	defer func() { _ = os.RemoveAll(root) }()

	report, err := ContractProbe(ctx, cand.Installation, ContractOptions{
		AgentDir:     filepath.Join(root, "agent"),
		WorkspaceDir: filepath.Join(root, "workspace"),
	})
	if err != nil {
		return false, []string{"kind:\"contract-probe-failed\" 隔离契约探测无法执行: " + err.Error()}
	}
	if report.State == StateReadyCompatible {
		return true, []string{
			"kind:\"contract-probe-passed\" 隔离契约探测通过: " + strings.Join(report.Capabilities, ","),
		}
	}
	return false, []string{
		"kind:\"contract-probe-failed\" 隔离契约探测未通过: " + strings.Join(report.FailedChecks, "; "),
	}
}

// ── 内部实现 ────────────────────────────────────────────────────────────────

type contractProber struct {
	inst PiInstallation
	opts ContractOptions
	// iso 是实际要交给 Pi 的隔离参数（含探测自己写入的扩展路径）。
	iso IsolationSpec
	// check 是当前进行检查的名字；只用于标注子进程环境与诊断，不改变行为。
	check string
}

// probeIsolation 返回探测用的隔离参数。
//
// HarnessMCP 强制为 true：mcp 检查要观察的就是内置 MCP 扩展注册的命令，
// `--no-mcp` 下这条命令根本不存在——那不是被测 Pi 的缺陷。
func (o ContractOptions) probeIsolation() IsolationSpec {
	iso := o.Isolation
	iso.HarnessMCP = true
	return iso
}

// writeContractProbeExtension 在隔离 agentDir 下写出探测扩展，返回其绝对路径。
//
// 只写隔离目录：绝不写用户真实配置目录（AgentDir 为空时调用方已经在更前面被拒）。
func writeContractProbeExtension(agentDir string) (string, error) {
	dir := filepath.Join(agentDir, contractProbeExtensionDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", contracts.NewAppError(contracts.CodeCompatibility,
			"无法创建探测扩展目录: "+err.Error(), false)
	}
	path := filepath.Join(dir, contractProbeExtensionName)
	if err := os.WriteFile(path, []byte(contractProbeExtensionSource), 0o644); err != nil {
		return "", contracts.NewAppError(contracts.CodeCompatibility,
			"无法写入探测扩展: "+err.Error(), false)
	}
	return path, nil
}

// launchArgs 是交给 worker 的完整 argv，形状与 internal/run 的启动路径一致：
//
//	--mode rpc（必须最先）→ 隔离参数 → --session-dir → --provider/--model
//
// 为什么 `--mode rpc` 不能省：不带它 Pi 进默认的 text 模式，stdin 里的 JSON
// 命令不会被当作 RPC 处理，get_commands 永远没有响应，六项检查全部超时——
// 于是一个完全可用的 Pi 被判 incompatible。internal/run 早就知道这一点，
// 探测漏了它，于是"探测能跑"这件事只在 fixture worker 上成立。
//
// 隔离参数一律由 IsolationSpec.Args() 构造，这里不手写第二份：两份 argv 一旦
// 分叉，就会出现"探测在一种形状上跑、运行在另一种形状上跑"，而证据看不出来。
func (p *contractProber) launchArgs() []string {
	args := []string{"--mode", "rpc"}
	args = append(args, p.iso.Args()...)
	// 会话必须落在隔离 agentDir 下：否则 Pi 会把会话写进自己的默认位置。
	args = append(args, "--session-dir", filepath.Join(p.opts.AgentDir, "sessions"))
	if provider := strings.TrimSpace(p.opts.Provider); provider != "" {
		args = append(args, "--provider", provider)
	}
	if model := strings.TrimSpace(p.opts.Model); model != "" {
		args = append(args, "--model", model)
	}
	return args
}

func normalizeContractOptions(opts ContractOptions) ContractOptions {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultContractTimeout
	}
	if opts.CheckTimeout <= 0 {
		opts.CheckTimeout = defaultContractCheckTimeout
	}
	if opts.CheckTimeout > opts.Timeout {
		opts.CheckTimeout = opts.Timeout
	}
	if strings.TrimSpace(opts.WorkspaceDir) == "" {
		opts.WorkspaceDir = opts.AgentDir
	}
	if opts.WorkerStarter == nil {
		opts.WorkerStarter = StartWorker
	}
	return opts
}

func (p *contractProber) start(ctx context.Context) (Worker, error) {
	w, err := p.opts.WorkerStarter(ctx, WorkerSpec{
		Installation: p.inst,
		WorkingDir:   p.opts.WorkspaceDir,
		AgentDir:     p.opts.AgentDir,
		Env: []string{
			"ANVIL_CONTRACT_CHECK=" + p.check,
			// 探测不允许自动联网（模型目录刷新、更新检查等）。PI_OFFLINE 是 Pi 自己
			// 支持的开关（docs/environment-variables.md），用环境变量而不是 --offline 参数：
			// 参数在不同 1.x 上可能不认，env 不会因为解析失败而把好 Pi 判成不兼容。
			"PI_OFFLINE=1",
		},
		Args: p.launchArgs(),
	})
	if err != nil {
		return nil, fmt.Errorf("启动隔离 worker 失败: %w", err)
	}
	if w == nil {
		return nil, fmt.Errorf("启动隔离 worker 失败: starter 返回 nil")
	}
	return w, nil
}

// closeBudget 只是给 Close 一个"总得结束"的兜底；真正的时限在 WorkerSpec 里。
func (p *contractProber) closeBudget() time.Duration {
	return p.opts.CheckTimeout + 10*time.Second
}

// closeQuietly 关闭 worker 但不覆盖已记录的失败。
func (p *contractProber) closeQuietly(w Worker) {
	ctx, cancel := context.WithTimeout(context.Background(), p.closeBudget())
	defer cancel()
	_ = w.Close(ctx)
}

// checkRPC 验证最小 RPC 往返：握手命令 + prompt 都能拿到合规响应。
func (p *contractProber) checkRPC(ctx context.Context) (string, error) {
	w, err := p.start(ctx)
	if err != nil {
		return "", err
	}
	defer p.closeQuietly(w)

	raw, err := w.Call(ctx, json.RawMessage(`{"type":"get_commands"}`))
	if err != nil {
		return "", fmt.Errorf("握手命令 get_commands 无响应: %w", err)
	}
	if problem := responseProblem(raw); problem != "" {
		return "", fmt.Errorf("握手响应不合规: %s（原文 %s）", problem, clip(string(raw), 300))
	}
	promptRaw, err := w.Call(ctx, promptCommand())
	if err != nil {
		return "", fmt.Errorf("prompt 无响应: %w", err)
	}
	if problem := responseProblem(promptRaw); problem != "" {
		return "", fmt.Errorf("prompt 响应不合规: %s（原文 %s）", problem, clip(string(promptRaw), 300))
	}
	return "rpc:get_commands+prompt", nil
}

// checkSettled 驱动一个回合到 agent_settled，并要求负载带布尔 aborted。
func (p *contractProber) checkSettled(ctx context.Context) (string, error) {
	w, err := p.start(ctx)
	if err != nil {
		return "", err
	}
	defer p.closeQuietly(w)

	if _, err := w.Call(ctx, promptCommand()); err != nil {
		return "", fmt.Errorf("prompt 无响应: %w", err)
	}
	ev, err := waitForEvent(ctx, w, func(ev RawEvent) bool { return ev.Type == TypeAgentSettled })
	if err != nil {
		return "", err
	}
	aborted, problem := settledAborted(ev.Record)
	if problem != "" {
		return "", fmt.Errorf("agent_settled 负载不合规: %s（原文 %s）", problem, clip(string(ev.Record), 300))
	}
	return fmt.Sprintf("settled:agent_settled/aborted=%t", aborted), nil
}

// checkMCP 验证 MCP 能力面可达：get_commands 里必须有 MCP 命令。
// 记录观察到的命令名，便于证据回溯。
func (p *contractProber) checkMCP(ctx context.Context) (string, error) {
	w, err := p.start(ctx)
	if err != nil {
		return "", err
	}
	defer p.closeQuietly(w)

	cmds, err := fetchCommands(ctx, w)
	if err != nil {
		return "", err
	}
	for _, c := range cmds {
		if strings.EqualFold(c.Name, "mcp") || strings.Contains(strings.ToLower(c.Description), "mcp") {
			return "mcp:get_commands/" + c.Name, nil
		}
	}
	return "", fmt.Errorf("MCP 能力面不可达：get_commands 未返回 MCP 命令（共 %d 条命令）", len(cmds))
}

// checkExtension 验证扩展 hook 面可达：get_commands 里必须有扩展来源的命令。
func (p *contractProber) checkExtension(ctx context.Context) (string, error) {
	w, err := p.start(ctx)
	if err != nil {
		return "", err
	}
	defer p.closeQuietly(w)

	cmds, err := fetchCommands(ctx, w)
	if err != nil {
		return "", err
	}
	for _, c := range cmds {
		if strings.EqualFold(c.Source, "extension") || strings.EqualFold(c.Source, "skill") {
			return "extension:get_commands/" + c.Name, nil
		}
	}
	return "", fmt.Errorf("扩展 hook 面不可达：get_commands 未返回扩展来源命令（共 %d 条命令）", len(cmds))
}

// checkAbort 在回合进行中发 abort，要求 agent_settled{aborted:true}。
// 一直跑下去的 worker 必须失败（超时），不能因为"还活着"算通过。
func (p *contractProber) checkAbort(ctx context.Context) (string, error) {
	w, err := p.start(ctx)
	if err != nil {
		return "", err
	}
	defer p.closeQuietly(w)

	// prompt 不能在当前 goroutine 等响应：abort 必须在回合进行中发出。
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.Call(promptCtx, promptCommand())
	}()
	defer func() {
		cancelPrompt()
		<-done
	}()

	// 尽量等到回合真的开始（真实 Pi 与合规 worker 都发 agent_start）。
	// 等不到也必须继续发 abort：hang 模式的负例要求 abort 自己超时失败。
	startCtx, cancelStart := context.WithTimeout(ctx, p.opts.CheckTimeout/2)
	_, _ = waitForEvent(startCtx, w, func(ev RawEvent) bool { return ev.Type == "agent_start" })
	cancelStart()

	if _, err := w.Call(ctx, json.RawMessage(`{"type":"abort"}`)); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timeout: abort 命令在时限内无响应（%w）", err)
		}
		return "", fmt.Errorf("abort 命令失败: %w", err)
	}
	ev, err := waitForEvent(ctx, w, func(ev RawEvent) bool { return ev.Type == TypeAgentSettled })
	if err != nil {
		return "", err
	}
	aborted, problem := settledAborted(ev.Record)
	if problem != "" {
		return "", fmt.Errorf("agent_settled 负载不合规: %s（原文 %s）", problem, clip(string(ev.Record), 300))
	}
	if !aborted {
		return "", fmt.Errorf("abort 之后 agent_settled 没有标记 aborted:true（原文 %s）",
			clip(string(ev.Record), 300))
	}
	return "abort:agent_settled/aborted=true", nil
}

// checkCleanup 关闭 worker 并要求真的关闭：Close 无 CleanupFailed 且主进程退出。
// 子孙清除本身由 worker.Close 判定（RPC05），这里不接受"清理报告的失败被吞掉"。
func (p *contractProber) checkCleanup(ctx context.Context) (string, error) {
	w, err := p.start(ctx)
	if err != nil {
		return "", err
	}
	// 先让 worker 真的跑起来：会派生子孙的 worker 在这一步拉起子孙。
	callCtx, cancelCall := context.WithTimeout(ctx, p.opts.CheckTimeout)
	_, _ = w.Call(callCtx, json.RawMessage(`{"type":"get_commands"}`))
	cancelCall()

	closeCtx, cancelClose := context.WithTimeout(context.Background(), p.closeBudget())
	defer cancelClose()
	closeErr := w.Close(closeCtx)
	defer p.closeQuietly(w)
	if closeErr != nil {
		return "", fmt.Errorf("清理未完成: %w", closeErr)
	}
	if _, known := w.ExitCode(); !known {
		return "", fmt.Errorf("清理后 worker 主进程仍在运行")
	}
	return "cleanup:closed", nil
}

// ── 协议解析小工具 ──────────────────────────────────────────────────────────

// contractFixtureHash 定位 <version> 的 fixture 哈希。
// testdata 目录名带 v 前缀（testdata/v1.5.0），而产品版本号不带，两种写法都试。
func contractFixtureHash(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return ""
	}
	if h := protocolFixtureHash(v); h != "" {
		return h
	}
	return protocolFixtureHash("v" + strings.TrimPrefix(v, "v"))
}

func promptCommand() json.RawMessage {
	return json.RawMessage(`{"type":"prompt","message":"contract probe: reply with ok"}`)
}

// responseProblem 检查一条 response 记录是否合规；返回空串表示合规。
func responseProblem(raw json.RawMessage) string {
	var h struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Success *bool  `json:"success"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return "不是合法 JSON 对象: " + err.Error()
	}
	if h.Type != TypeResponse {
		return "type 不是 response，实际 " + strings.TrimSpace(h.Type)
	}
	if strings.TrimSpace(h.ID) == "" {
		return "响应缺少 id"
	}
	if h.Success == nil {
		return "响应缺少 success 字段"
	}
	if !*h.Success {
		return "响应 success=false"
	}
	return ""
}

// settledAborted 取 agent_settled 里的布尔 aborted。
// 缺失/非布尔一律算失败：完成语义不能靠推断。
func settledAborted(record json.RawMessage) (bool, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record, &fields); err != nil {
		return false, "不是合法 JSON 对象: " + err.Error()
	}
	raw, ok := fields["aborted"]
	if !ok {
		return false, "缺少 aborted 字段"
	}
	var aborted bool
	if err := json.Unmarshal(raw, &aborted); err != nil {
		return false, "aborted 不是布尔值: " + string(raw)
	}
	return aborted, ""
}

type workerCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

// fetchCommands 发 get_commands 并解析 data.commands（真实捕获里的能力面）。
func fetchCommands(ctx context.Context, w Worker) ([]workerCommand, error) {
	raw, err := w.Call(ctx, json.RawMessage(`{"type":"get_commands"}`))
	if err != nil {
		return nil, fmt.Errorf("get_commands 无响应: %w", err)
	}
	if problem := responseProblem(raw); problem != "" {
		return nil, fmt.Errorf("get_commands 响应不合规: %s（原文 %s）", problem, clip(string(raw), 300))
	}
	var v struct {
		Data struct {
			Commands []workerCommand `json:"commands"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("get_commands 响应无法解析: %w", err)
	}
	return v.Data.Commands, nil
}

// waitForEvent 消费事件直到匹配或超时。超时返回带 timeout 字样的错误。
func waitForEvent(ctx context.Context, w Worker, match func(RawEvent) bool) (RawEvent, error) {
	for {
		select {
		case ev, ok := <-w.Events():
			if !ok {
				return RawEvent{}, fmt.Errorf("timeout: worker 事件流在期望记录到达前关闭")
			}
			if match(ev) {
				return ev, nil
			}
		case <-ctx.Done():
			return RawEvent{}, fmt.Errorf("timeout: 在时限内未等到期望记录（%v）", ctx.Err())
		}
	}
}
