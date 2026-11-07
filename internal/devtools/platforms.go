package devtools

// 平台最低 OS 要求（GOV05）。
//
// 为什么最低要求必须来自机器可读的 build/platforms.json 而不是代码常量：
// 门禁、集成 runner、原生 runner 都要用同一份口径。写两处必然会分叉——
// 一处调低阈值让本机绿，另一处还在拦，结论就说不清是谁对。
//
// ADR 0005（2026-11-06）把本轮验收平台收敛为 win-x64 / Windows 10 22H2（build 19045）：
// 本机即可产出验收级证据；其余五个目标 active=false，它们的必需位在 gate 报告里
// 记为 deferred（不计缺失、也不计通过），见 activeTargets/ResolveRequiredEvidence。

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"my-agent-go/internal/contracts"
)

// platformsRelPath 是平台配置相对仓库根的路径。
const platformsRelPath = "build/platforms.json"

// PlatformTarget 是 build/platforms.json 里单个目标的机器可读部分。
// 只取门禁判定需要的字段：其它字段（package/status/reason）是描述性的。
type PlatformTarget struct {
	TargetID string `json:"targetId"`
	GOOS     string `json:"goos"`
	GOARCH   string `json:"goarch"`
	// MinOSVersion 是目标最低 OS 版本（点分数字：Windows "11"、macOS "14"、Linux "24.04"）。
	MinOSVersion string `json:"minOSVersion"`
	// MinOSBuild 是目标最低 OS build 号；0 表示该平台不按 build 判定。
	MinOSBuild int `json:"minOSBuild"`
	// Active 表示该目标本轮是否验收（ADR 0005）。缺省视为 true：
	// 老配置没有这个字段时行为不变，不会因为漏写一个字段悄悄停掉一个目标。
	Active *bool `json:"active,omitempty"`
	// DeferredBy 记录停用依据（例如 "ADR 0005"），用于 gate 报告点名。
	DeferredBy string `json:"deferredBy,omitempty"`
}

// IsActive 报告该目标本轮是否验收（缺省 true）。
func (t PlatformTarget) IsActive() bool {
	return t.Active == nil || *t.Active
}

// Platforms 是 build/platforms.json 的解析结果。
type Platforms struct {
	SchemaVersion string           `json:"schemaVersion"`
	Targets       []PlatformTarget `json:"targets"`
	// path 记录来源文件，便于错误信息指出到底是哪份配置有问题。
	path string
}

// LoadPlatforms 读取并校验平台配置。
//
// 任一目标缺 minOSVersion 都返回错误：门禁不能「读不到最低要求就默认放行」。
// 这与 GOV01/GOV02 的口径一致——配置缺失一律 exit 2，不是通过。
func LoadPlatforms(path string) (*Platforms, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取平台配置失败 %s: %w", path, err)
	}
	var p Platforms
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("解析平台配置失败 %s: %w", path, err)
	}
	if len(p.Targets) == 0 {
		return nil, fmt.Errorf("平台配置 %s 没有任何 target", path)
	}
	for i, t := range p.Targets {
		switch {
		case strings.TrimSpace(t.TargetID) == "":
			return nil, fmt.Errorf("平台配置 %s 第 %d 个 target 缺 targetId", path, i+1)
		case strings.TrimSpace(t.GOOS) == "" || strings.TrimSpace(t.GOARCH) == "":
			return nil, fmt.Errorf("平台配置 %s 的 target %s 缺 goos/goarch", path, t.TargetID)
		case strings.TrimSpace(t.MinOSVersion) == "":
			return nil, fmt.Errorf("平台配置 %s 的 target %s 缺 minOSVersion：门禁不能默认放行最低 OS 版本", path, t.TargetID)
		case t.MinOSBuild < 0:
			return nil, fmt.Errorf("平台配置 %s 的 target %s 的 minOSBuild=%d 非法", path, t.TargetID, t.MinOSBuild)
		}
		if _, err := parseOSVersion(t.MinOSVersion); err != nil {
			return nil, fmt.Errorf("平台配置 %s 的 target %s 的 minOSVersion 不可解析: %v", path, t.TargetID, err)
		}
		p.Targets[i] = t
	}
	p.path = path
	return &p, nil
}

// Target 返回某目标的最低要求声明。
func (p *Platforms) Target(id string) (PlatformTarget, bool) {
	if p == nil {
		return PlatformTarget{}, false
	}
	for _, t := range p.Targets {
		if t.TargetID == id {
			return t, true
		}
	}
	return PlatformTarget{}, false
}

// LoadPlatformsFromRepo 从仓库根加载 build/platforms.json。
// 用 repoFile 解析仓库根：go test 与命令行的 cwd 都可能是子目录。
func LoadPlatformsFromRepo() (*Platforms, error) {
	path, err := repoFile(platformsRelPath)
	if err != nil {
		return nil, err
	}
	return LoadPlatforms(path)
}

// parseOSVersion 把 "24.04" / "14.5" / "11" 解析成数字分量。
// 非数字（含空分量、负数）一律判为不可解析：宁可判死也不猜。
func parseOSVersion(s string) ([]int, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, fmt.Errorf("OS 版本为空")
	}
	parts := strings.Split(trimmed, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("OS 版本 %q 含空的版本分量", s)
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("OS 版本 %q 不可解析（分量 %q 不是非负整数）", s, part)
		}
		out = append(out, n)
	}
	return out, nil
}

// compareOSVersion 按数字分量比较两个版本（major→minor→patch），
// 缺的分量按 0 处理（"11" 与 "11.0" 等价）。不可解析时返回 error。
func compareOSVersion(got, min string) (int, error) {
	g, err := parseOSVersion(got)
	if err != nil {
		return 0, err
	}
	m, err := parseOSVersion(min)
	if err != nil {
		return 0, err
	}
	n := len(g)
	if len(m) > n {
		n = len(m)
	}
	at := func(v []int, i int) int {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	for i := 0; i < n; i++ {
		switch {
		case at(g, i) < at(m, i):
			return -1, nil
		case at(g, i) > at(m, i):
			return 1, nil
		}
	}
	return 0, nil
}

// BelowMinimum 判断宿主环境是否低于该目标的最低要求。
//
// 门禁与 runner 的警告共用这一条判定：两处口径分叉时，runner 会说「能过」
// 而门禁说「不过」，人就没法判断到底哪边错了。返回 (false, "") 表示满足。
func (t PlatformTarget) BelowMinimum(env contracts.Environment) (bool, string) {
	if t.MinOSBuild > 0 {
		raw := strings.TrimSpace(env.OSBuild)
		build, err := strconv.Atoi(raw)
		if err != nil || build < 0 {
			return true, fmt.Sprintf("宿主 OS build %q 无法解析，不能证明达到目标最低 build %d", env.OSBuild, t.MinOSBuild)
		}
		if build < t.MinOSBuild {
			return true, fmt.Sprintf("宿主 OS build %d 低于目标最低 build %d", build, t.MinOSBuild)
		}
	}
	cmp, err := compareOSVersion(env.OSVersion, t.MinOSVersion)
	if err != nil {
		return true, fmt.Sprintf("宿主 OS 版本 %q 无法解析: %v", env.OSVersion, err)
	}
	if cmp < 0 {
		return true, fmt.Sprintf("宿主 OS 版本 %s 低于目标最低版本 %s", env.OSVersion, t.MinOSVersion)
	}
	return false, ""
}

// warnIfBelowMinimum 在 runner 输出里显式说明「这份证据会被门禁拒绝」。
//
// 不跳过 case：证据仍然是合法的开发证据，只是不能当验收结论。
// 这里刻意不返回错误：环境偏差不是 runner 的配置错误，是机器不符合基线。
func warnIfBelowMinimum(env contracts.Environment, target string) {
	plats, err := LoadPlatformsFromRepo()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[警告] 无法加载 %s，不能判断宿主是否达到目标最低要求: %v\n", platformsRelPath, err)
		return
	}
	tm, ok := plats.Target(target)
	if !ok {
		fmt.Fprintf(os.Stderr, "[警告] %s 缺 target %s 的最低 OS 要求，宿主是否达标未知\n", platformsRelPath, target)
		return
	}
	if below, why := tm.BelowMinimum(env); below {
		fmt.Printf("[警告] %s；目标 %s 最低要求 osVersion=%s osBuild=%d。\n", why, target, tm.MinOSVersion, tm.MinOSBuild)
		fmt.Println("       该证据会被 GOV05 门禁拒绝，只能作为开发证据，不能作为验收结论。")
	}
}
