// Package pi 负责本地 Pi 定位、能力探测与版本兼容性裁定（F02）。
//
// 设计纪律（来自 AGENTS.md / 基线 §8）：
//   - Locate 只做文件发现，绝不把用户字符串拼成通用 shell 命令。所有执行都通过
//     BuildLaunch 以 argv 切片形式交给 exec.Command，路径作为独立（必要时加引号）参数。
//   - Probe 在隔离 env（HOME/USERPROFILE/APPDATA/PI_CODING_AGENT_DIR）下运行 --version，
//     不启动 agent loop、不请求真实模型、不拉起用户 MCP。
//   - 版本策略：只有经过认证（certified）的精确版本才允许标 Ready；未知/未认证版本进入
//     unverified（映射基线 UnsupportedVersion），绝不静默承诺可用（基线 §3 / §8）。
package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"my-agent-go/internal/contracts"
)

// Origin 标明 Pi 安装来源。
type Origin string

const (
	// OriginExternal 表示在 PATH / 常见位置 / 用户指定路径发现的外部安装。
	OriginExternal Origin = "external"
	// OriginManaged 表示由本应用安装/管理的隔离副本（如 .tools/pi/vX.Y.Z）。
	OriginManaged Origin = "managed"
)

// PiInstallation 描述一个被发现/指定的 Pi 可执行体。
// 注意：Executable 始终是 Pi 本体（.exe/.cmd/.ps1/.sh）的真实路径；
// 进程启动所需的包装（cmd /c、powershell -File 等）由 BuildLaunch 按扩展名推导，
// 不在此处写死成 shell 字符串。
type PiInstallation struct {
	// ID 稳定标识，形如 "<source>:<abspath>"，承载“来源”以便多版本共存时 UI 显示。
	ID string `json:"id"`
	// Executable 是 Pi 本体的绝对路径。
	Executable string `json:"executable"`
	// Args 是调用 Pi 时始终附加的固定参数（如指向隔离 agentDir 的 flag）。
	Args []string `json:"args,omitempty"`
	// Version 探测得到的版本；Locate 阶段可能为空，由 Probe 填充。
	Version string `json:"version,omitempty"`
	// Origin 来源 external/managed。
	Origin Origin `json:"origin"`
	// Architecture 架构，如 x86_64/arm64；脚本型 shim 记 "any"；探测不到时为 "unknown"。
	Architecture string `json:"architecture"`
	// Source 是发现来源（用户指定/PATH/常见位置/托管），用于"多版本显示来源"。
	Source Source `json:"source,omitempty"`
	// Runnable 表示能否被直接启动；.ps1 受执行策略约束时为 false，不得假装可用。
	Runnable bool `json:"runnable"`
	// Note 记录该安装件的诊断信息（例如仅有 .ps1 因而不可直接执行）。
	Note string `json:"note,omitempty"`
}

// Source 记录安装件的发现来源。基线 §8 要求多版本时显示来源、禁止静默切换。
type Source string

const (
	SourceUserSpecified Source = "user-specified"
	SourceSavedPath     Source = "saved-path"
	SourcePATH          Source = "path"
	SourceCommon        Source = "common-location"
	SourceManaged       Source = "managed"
)

// ProbeState 沿用基线 §8 的发现/探测状态机。
type ProbeState string

const (
	StateNotFound           ProbeState = "NotFound"
	StateDependencyMissing  ProbeState = "DependencyMissing"
	StateUnsupportedVersion ProbeState = "UnsupportedVersion"
	StateProbeFailed        ProbeState = "ProbeFailed"
	StateReady              ProbeState = "Ready"
)

// Capability 描述探测到的一项能力。
type Capability struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

// ProbeResult 是探测某 Pi 安装的结果。
type ProbeResult struct {
	State               ProbeState   `json:"state"`
	Capabilities        []Capability `json:"capabilities,omitempty"`
	Version             string       `json:"version"`
	ProtocolFixtureHash string       `json:"protocolFixtureHash,omitempty"`
	Diagnostics         []string     `json:"diagnostics,omitempty"`
}

// VersionPolicy 描述哪些版本被认证为可直接使用。
// 基线 §3：Pi 初始认证版本 v1.5.0，必须读取该 tag 的 RPC/扩展资料；未知版本进入
// unverified，不静默承诺可用。增加支持版本需要跑同一 Pi 契约套件。
type VersionPolicy struct {
	// Certified 是被认证、可标 Ready 的精确版本集合。
	Certified map[string]bool
	// Minimum 是允许的最低版本（仅用于诊断提示）。
	Minimum string
	// PreCertifiedHashes 是"已认证版本 → 允许的可执行体 sha256"白名单（F08）。
	//
	// 它随发行包下发，默认为空——空表意味着**任何**本机可执行体都不能标
	// ready_verified（只能 ready_compatible）。禁止在运行期把"本机跑通过一次"
	// 的哈希写进来：那是自证，不是认证。
	PreCertifiedHashes map[string]map[string]bool
}

// DefaultPolicy 当前仅认证 1.5.0。1.4.0（PATH 上现有）属已知但 unverified，不可标 Ready。
//
// PreCertifiedHashes 由 init 从嵌入的签名清单填充（见 precert.go）。今天发行包里
// 没有清单，所以它是空表：任何本机可执行体都到不了 ready_verified。
var DefaultPolicy = VersionPolicy{
	Certified: map[string]bool{"1.5.0": true},
	Minimum:   "1.5.0",
}

// embeddedPreCertified 是嵌入清单的装载结果，保留错误以便诊断。
var (
	embeddedPreCertifiedHashes map[string]map[string]bool
	embeddedPreCertifiedErr    error
)

func init() {
	// fail closed：清单缺失/非法 → 空哈希表 + 保留错误。绝不"猜一个"哈希放行。
	embeddedPreCertifiedHashes, embeddedPreCertifiedErr = LoadEmbeddedPreCertified()
	DefaultPolicy.PreCertifiedHashes = embeddedPreCertifiedHashes
}

// EmbeddedPreCertifiedStatus 报告随包下发的预认证哈希清单及其装载结果。
//
// 返回的表是只读快照：调用方不得写回 DefaultPolicy（那等于运行期自证认证）。
// 没有清单时 err 为 ErrNoPreCertifiedManifest，hashes 为空——这是预期状态。
func EmbeddedPreCertifiedStatus() (map[string]map[string]bool, error) {
	return embeddedPreCertifiedHashes, embeddedPreCertifiedErr
}

// IsCertified 报告版本是否经认证可直接使用。
func (p VersionPolicy) IsCertified(v string) bool {
	if v == "" {
		return false
	}
	return p.Certified[v]
}

// LaunchPolicy 控制 .ps1 启动时的执行策略包装，便于测试“执行策略禁止”的负例。
// 生产默认 true（按 AGENTS.md 用 -ExecutionPolicy Bypass）。
var PSBypassPolicy = true

// BuildLaunch 依据扩展名把 PiInstallation 解析为 (exe, args) 切片，绝不拼接 shell 字符串。
// 路径作为独立参数传入 exec.Command；含空格的路径由 Go 运行时自动加引号，不会注入。
//
//   - .ps1 → powershell.exe -NoProfile [-ExecutionPolicy Bypass] -File <path>
//   - .cmd/.bat → cmd.exe /c <path>
//   - 其他（.exe/.sh/无扩展）→ 直接执行 <path>
func BuildLaunch(inst PiInstallation, extraArgs ...string) (string, []string, error) {
	lower := strings.ToLower(inst.Executable)
	switch {
	case strings.HasSuffix(lower, ".ps1"):
		exe := "powershell.exe"
		args := []string{"-NoProfile"}
		if PSBypassPolicy {
			args = append(args, "-ExecutionPolicy", "Bypass")
		}
		args = append(args, "-File", inst.Executable)
		args = append(args, inst.Args...)
		args = append(args, extraArgs...)
		return exe, args, nil
	case strings.HasSuffix(lower, ".cmd"), strings.HasSuffix(lower, ".bat"):
		// .cmd/.bat 必须经 cmd.exe /c 启动；路径作为独立参数，不进入 shell 字符串。
		args := []string{"/c", inst.Executable}
		args = append(args, inst.Args...)
		args = append(args, extraArgs...)
		return "cmd.exe", args, nil
	default:
		args := append([]string{}, inst.Args...)
		args = append(args, extraArgs...)
		return inst.Executable, args, nil
	}
}

// classifyVersion 依据版本策略把“探测到的版本”映射为最终状态。
// 只有 certified 版本且探测本身成功才标 Ready；其余一律 UnsupportedVersion（unverified），
// 并附带诊断说明，绝不静默承诺可用。
func classifyVersion(policy VersionPolicy, version string) (ProbeState, []string) {
	if version == "" {
		return StateUnsupportedVersion, []string{"无法解析 Pi 版本（unverified）"}
	}
	if policy.IsCertified(version) {
		return StateReady, nil
	}
	diag := fmt.Sprintf("版本 %s 未经认证（基线仅认证 %v）；标 unverified，不自动运行",
		version, keysOf(policy.Certified))
	return StateUnsupportedVersion, []string{diag}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// protocolFixtureHash 计算某版本对应协议基线的哈希（来自 testdata/<version>/）。
// 路径通过 runtime.Caller 相对本源文件定位，测试与生产一致。目录缺失返回空串。
func protocolFixtureHash(version string) string {
	if version == "" {
		return ""
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Join(filepath.Dir(thisFile), "testdata", version)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dirSHA256(dir)
}

// dirSHA256 计算目录内（递归）文件的稳定哈希。相同内容→相同哈希，便于门禁比对。
func dirSHA256(dir string) string {
	h := sha256.New()
	var files []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		files = append(files, p)
		return nil
	})
	sort.Strings(files)
	for _, p := range files {
		rel, _ := filepath.Rel(dir, p)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		b, err := os.ReadFile(p)
		if err != nil {
			h.Write([]byte("ERR"))
			continue
		}
		h.Write(b)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// notFoundError 构造 Locate 的 NotFound 错误（沿用统一错误码）。
func notFoundError(msg string) error {
	return contracts.NewAppError(contracts.CodeNotFound, msg, false)
}
