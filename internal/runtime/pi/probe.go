package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ProbeIsolationDir 指定 probe 子进程的隔离目录（HOME/USERPROFILE/APPDATA/
// PI_CODING_AGENT_DIR 均重定向至此），用于保证不触碰用户真实配置。
// 为空时 Probe 会自行创建临时隔离目录。
var ProbeIsolationDir string

// probeTimeout 是单次 --version 探测的硬超时；超过即视为异常，绝不进入 agent loop。
const probeTimeout = 20 * time.Second

// versionRe 提取输出中的语义化版本号。
var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Probe 探测一个 Pi 安装：在隔离 env 下运行 --version，按版本策略裁定最终状态。
//
// 纪律（PI02 / 基线 §8）：
//   - 只运行 --version，不启动 agent loop、不请求真实模型、不拉起用户 MCP。
//   - 子进程 HOME/USERPROFILE/APPDATA/PI_CODING_AGENT_DIR 全部重定向到隔离目录，
//     因此即使用户 HOME 指向真实配置，也不会被改动。
//   - .ps1 必须经由 powershell -File 启动；若执行策略禁止导致无法运行，返回明确诊断
//     “不可直接执行”，绝不假装可用。
func Probe(ctx context.Context, installation PiInstallation) (ProbeResult, error) {
	res := ProbeResult{}

	// 安装本体不存在：直接 NotFound，不尝试执行。
	if installation.Executable == "" {
		res.State = StateNotFound
		res.Diagnostics = append(res.Diagnostics, "安装路径为空")
		return res, nil
	}
	if _, err := os.Stat(installation.Executable); err != nil {
		res.State = StateNotFound
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("安装本体不存在: %s", installation.Executable))
		return res, nil
	}

	// 构建隔离 env：把用户级配置根全部重定向到隔离目录。
	iso := ProbeIsolationDir
	if iso == "" {
		dir, err := os.MkdirTemp("", "pi-probe-iso-")
		if err != nil {
			res.State = StateProbeFailed
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("无法创建隔离目录: %v", err))
			return res, err
		}
		iso = dir
	}
	env := overrideEnv(os.Environ(),
		"PI_CODING_AGENT_DIR", iso,
		"HOME", iso,
		"USERPROFILE", iso,
		"APPDATA", iso,
	)

	exe, args, berr := BuildLaunch(installation, "--version")
	if berr != nil {
		res.State = StateProbeFailed
		res.Diagnostics = append(res.Diagnostics, berr.Error())
		return res, berr
	}

	runCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, exe, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// 启动失败（如 .ps1 需要 powershell 但缺失，或无法执行）。
	if runErr != nil {
		if isExitError(runErr) {
			// 进程退出非 0：通常是 .ps1 执行策略禁止或 pi 报错。
			if strings.EqualFold(filepath.Ext(installation.Executable), ".ps1") {
				res.State = StateProbeFailed
				res.Version = ""
				res.Diagnostics = append(res.Diagnostics,
					"ps1 不可直接执行：执行策略禁止或未包装 powershell -File（stderr: "+firstLine(stderr.String())+"）")
				return res, nil
			}
			res.State = StateProbeFailed
			res.Diagnostics = append(res.Diagnostics, "探测命令非零退出: "+firstLine(stderr.String()))
			return res, nil
		}
		// 无法启动（二进制缺失等）。
		if strings.EqualFold(filepath.Ext(installation.Executable), ".ps1") {
			res.State = StateProbeFailed
			res.Diagnostics = append(res.Diagnostics, "ps1 不可直接执行（未能启动 powershell）: "+runErr.Error())
			return res, nil
		}
		res.State = StateDependencyMissing
		res.Diagnostics = append(res.Diagnostics, "依赖缺失或无法启动: "+runErr.Error())
		return res, nil
	}

	version := parseVersion(stdout.String())
	res.Version = version

	state, diag := classifyVersion(DefaultPolicy, version)
	res.State = state
	res.Diagnostics = append(res.Diagnostics, diag...)

	if state == StateReady {
		res.ProtocolFixtureHash = protocolFixtureHash(version)
		res.Capabilities = loadCapabilities(version)
	}
	return res, nil
}

// parseVersion 从 --version 输出中提取首个语义化版本号。
func parseVersion(out string) string {
	m := versionRe.FindString(strings.TrimSpace(out))
	return m
}

// isExitError 判断错误是否源于进程非零退出（而非无法启动）。
func isExitError(err error) bool {
	var ee *exec.ExitError
	return asExitError(err, &ee)
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// firstLine 取输出首行（去尾随换行），用于诊断摘要。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// overrideEnv 返回覆盖了指定键值（已存在则替换，不存在则追加）的环境切片副本。
func overrideEnv(base []string, kv ...string) []string {
	if len(kv)%2 != 0 {
		panic("overrideEnv: 奇数参数")
	}
	// 拷贝并去掉将被覆盖的键。
	out := make([]string, 0, len(base)+len(kv)/2)
	lowerSet := map[string]bool{}
	for i := 0; i < len(kv); i += 2 {
		lowerSet[strings.ToLower(kv[i])] = true
	}
	for _, e := range base {
		eq := strings.IndexByte(e, '=')
		if eq > 0 && lowerSet[strings.ToLower(e[:eq])] {
			continue
		}
		out = append(out, e)
	}
	for i := 0; i < len(kv); i += 2 {
		out = append(out, kv[i]+"="+kv[i+1])
	}
	return out
}

// loadCapabilities 从 testdata/<version>/capabilities.json 读取真实捕获得到的能力清单。
// 文件不存在（未捕获协议基线）时返回空，绝不编造能力。
func loadCapabilities(version string) []Capability {
	if version == "" {
		return nil
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil
	}
	p := filepath.Join(filepath.Dir(thisFile), "testdata", version, "capabilities.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var caps []Capability
	if err := json.Unmarshal(b, &caps); err != nil {
		return nil
	}
	return caps
}
