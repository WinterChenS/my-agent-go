package pi

// 本文件实现 F08 的 Pi 发现与选择（规格 §8.1 / 计划 F08 Step 1–5、7）。
//
// 与旧 Locate 的关系：Locate 只回答"哪里有 Pi"；Discover 还要回答"这个候选是
// 谁（preferred/saved/managed/path/common）、哪个东西真的会被执行（launcher 还是
// Node）、指纹是多少、按版本策略该给什么产品状态"。两者互不影响，Locate 保留。
//
// 硬约束（AGENTS.md / 规格 §8.1）：
//   - 发现是只读操作：不写任何文件、不改 PATH、不创建目录、不碰用户真实 Pi 配置。
//   - 版本探测可注入（VersionProbe），测试不依赖本机是否装了 Pi。
//   - 绝不把用户字符串拼成 shell 命令；需要 Node 时只做文件解析，执行交给既有 BuildLaunch。
//   - 多候选全部列出、顺序确定；选定后不静默切换。
//   - 未经登记的可执行体哈希**永远**不会变成 ready_verified。

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"my-agent-go/internal/contracts"
)

// CompatibilityState 是产品对外暴露的发现/兼容状态（规格 §8.1）。
// 它与低层的 ProbeState 不冲突：ProbeState 描述"这次探测发生了什么"，
// CompatibilityState 描述"UI 该给用户看什么、允不允许用"。
type CompatibilityState string

const (
	StateDiscovering CompatibilityState = "discovering"
	// StateReadyVerified 表示版本已认证 **且** 可执行体哈希在随发行包下发的
	// 预认证清单里（并已通过契约探测）。本层不产生"本地跑通就认证"。
	StateReadyVerified   CompatibilityState = "ready_verified"
	StateReadyCompatible CompatibilityState = "ready_compatible"
	// StateUpgradeAvailable 是真实候选，但低于最低支持版本：只能提示升级，不可选中。
	StateUpgradeAvailable CompatibilityState = "upgrade_available"
	StateIncompatible     CompatibilityState = "incompatible"
	StateMissing          CompatibilityState = "missing"
	// installing / rollback_available 由 F06 提供，这里只定义常量，不产生它们。
	StateInstalling        CompatibilityState = "installing"
	StateRollbackAvailable CompatibilityState = "rollback_available"
	StateFailed            CompatibilityState = "failed"
)

// 候选来源优先级词表（规格 §8.1）。注意这些**不是** PiInstallation.Origin：
// 后者只有 external/managed 两种含义，继续用于"这是谁装的"。
const (
	CandidateOriginPreferred = "preferred"
	CandidateOriginSaved     = "saved"
	CandidateOriginManaged   = "managed"
	CandidateOriginPATH      = "path"
	CandidateOriginCommon    = "common"
)

// 发行形态。
const (
	DistributionStandalone = "standalone"
	DistributionNpm        = "npm"
)

// PiCandidate 是一个被发现（或被判定为缺失）的 Pi 候选。
type PiCandidate struct {
	Installation PiInstallation `json:"installation"`
	// Origin 是优先级词表：preferred|saved|managed|path|common。
	Origin string `json:"origin"`
	// Distribution 是 standalone|npm；npm 表示启动器只是 Node shim。
	Distribution string `json:"distribution"`
	// ExecutableHash 是启动器的 sha256。
	ExecutableHash string `json:"executableHash"`
	// LauncherPath 是用户/PATH 命名的那个路径（按发现时的绝对路径记录）。
	LauncherPath string `json:"launcherPath"`
	// RuntimeExecutable 是真正被执行的二进制（native → launcher 自身；shim → Node）。
	RuntimeExecutable string `json:"runtimeExecutable"`
	// RuntimeHash 是 RuntimeExecutable 的 sha256。
	RuntimeHash   string             `json:"runtimeHash"`
	Compatibility CompatibilityState `json:"compatibility"`
	Diagnostics   []string           `json:"diagnostics,omitempty"`
}

// VersionProbe 是版本探测注入点（测试用）。nil 表示使用真实 Probe。
type VersionProbe func(ctx context.Context, inst PiInstallation) (version string, diagnostics []string)

// ProbeFunc 是 F08-B 契约探测的注入点。F08-A 只留缝，不实现探测。
//
// verified=true 只允许来自真正校验过签名清单的契约探测；本层不会因为
// "本机能跑 --version" 就把候选升成 ready_verified。
type ProbeFunc func(ctx context.Context, cand PiCandidate) (verified bool, failed []string)

// DiscoveryRequest 描述一次发现请求。全部输入都是显式传入的，
// 这样测试不会因为宿主机的真实 PATH 而得到不同结论。
type DiscoveryRequest struct {
	Preferred       string   // settings.piPath / 用户指定
	Saved           string   // 上次选中的路径（saved-path）
	ManagedRoot     string   // 托管安装根（F06 负责填充，这里只读）
	PathEnv         []string // 要扫描的 PATH 条目，按顺序
	CommonLocations []string // 平台常见位置
	Policy          VersionPolicy
	VersionProbe    VersionProbe
	ProbeFunc       ProbeFunc
}

// DiscoveryResult 是发现结果：所有候选 + 选中项 + 总体状态。
type DiscoveryResult struct {
	State       CompatibilityState `json:"state"`
	Selected    *PiCandidate       `json:"selected,omitempty"`
	Candidates  []PiCandidate      `json:"candidates"`
	Diagnostics []string           `json:"diagnostics,omitempty"`
}

// Discover 收集所有来源的候选并按来源优先级排序，然后逐条判定兼容状态。
//
// 返回值只在本层能确定的范围内承诺：没有候选 → missing；有候选但都不可用 →
// incompatible；否则 State 等于被选中候选的 Compatibility。
func Discover(ctx context.Context, req DiscoveryRequest) (DiscoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	policy := req.Policy
	if len(policy.Certified) == 0 {
		policy = DefaultPolicy
	}
	vprobe := req.VersionProbe
	if vprobe == nil {
		vprobe = defaultVersionProbe
	}

	res := DiscoveryResult{Candidates: []PiCandidate{}}

	// 1) 收集候选（按来源优先级顺序，先到先占，保证"同一真实路径保留最高优先级来源"）。
	seenLauncher := map[string]bool{}
	add := func(c PiCandidate) {
		key := strings.ToLower(filepath.Clean(c.LauncherPath))
		if seenLauncher[key] {
			return
		}
		seenLauncher[key] = true
		res.Candidates = append(res.Candidates, c)
	}
	for _, src := range discoverySources(req) {
		files, missing := scanDiscoveryEntry(src)
		if missing {
			// 失效的用户指定/保存路径必须作为候选出现（UI 要显示"这条坏了"），
			// 但永远不可选。
			c := newCandidate(src.path, src.origin)
			c.Compatibility = StateMissing
			c.Diagnostics = append(c.Diagnostics,
				"kind:\"missing-path\" "+src.origin+" 路径不存在，已失效: "+c.LauncherPath)
			c.Installation.Runnable = false
			add(c)
			continue
		}
		for _, f := range files {
			add(newCandidate(f, src.origin))
		}
	}

	// 2) 顺序确定：来源优先级 → 路径（Windows 大小写不敏感）。
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		ri, rj := originRank(res.Candidates[i].Origin), originRank(res.Candidates[j].Origin)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(res.Candidates[i].LauncherPath) < strings.ToLower(res.Candidates[j].LauncherPath)
	})

	// 3) 逐条解析真实路径 / 运行时 / 指纹 / 版本 → 兼容状态。
	for i := range res.Candidates {
		res.Candidates[i] = resolveCandidate(ctx, res.Candidates[i], req, policy, vprobe)
	}

	// 4) 按**解析后的真实路径**去重（符号链接/多来源指向同一文件时只留最高优先级）。
	res.Candidates = dedupeByRealPath(res.Candidates)

	// 5) 选中：ready_verified 优先，其次 ready_compatible，组内按优先级顺序。
	res.Selected = firstUsable(res.Candidates)
	switch {
	case res.Selected != nil:
		res.State = res.Selected.Compatibility
	case len(res.Candidates) > 0:
		res.State = StateIncompatible
		res.Diagnostics = append(res.Diagnostics,
			"发现候选但均不可用；不自动切换到未验证的运行时，请用户显式选择或升级")
	default:
		res.State = StateMissing
		res.Diagnostics = append(res.Diagnostics, "未发现任何 Pi 候选")
	}
	return res, nil
}

// Select 在已发现候选里选择指定 installationID。
//
// 绝不静默回退：ID 不在候选里 → NotFound；候选不可用（missing/upgrade_available/
// incompatible…）→ Unsupported。选择只改结果里的 Selected/State，不写任何配置
// （F07 目前没有"已保存 Pi 路径"字段，报告里已声明）。
func Select(ctx context.Context, result DiscoveryResult, installationID string) (DiscoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for i := range result.Candidates {
		c := result.Candidates[i]
		if c.Installation.ID != installationID {
			continue
		}
		if !selectable(c.Compatibility) {
			return result, contracts.NewAppError(contracts.CodeUnsupported,
				fmt.Sprintf("候选 %s 的状态是 %s，不可选择", installationID, c.Compatibility), false)
		}
		chosen := c
		result.Selected = &chosen
		result.State = c.Compatibility
		return result, nil
	}
	return result, contracts.NewAppError(contracts.CodeNotFound,
		"候选列表里没有这个 installationID: "+installationID, false)
}

// ── 内部实现 ────────────────────────────────────────────────────────────────

type discoverySource struct {
	origin string
	path   string
}

// discoverySources 按规格顺序给出全部来源：preferred → saved → managed → PATH → common。
func discoverySources(req DiscoveryRequest) []discoverySource {
	var out []discoverySource
	add := func(origin, path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		out = append(out, discoverySource{origin: origin, path: path})
	}
	add(CandidateOriginPreferred, req.Preferred)
	add(CandidateOriginSaved, req.Saved)
	if root := strings.TrimSpace(req.ManagedRoot); root != "" {
		add(CandidateOriginManaged, root)
		// 托管根下按版本目录布局（F06）：逐个子目录扫描，名字排序保证确定性。
		if entries, err := os.ReadDir(root); err == nil {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			for _, n := range names {
				add(CandidateOriginManaged, filepath.Join(root, n))
			}
		}
	}
	for _, p := range req.PathEnv {
		add(CandidateOriginPATH, p)
	}
	for _, p := range req.CommonLocations {
		add(CandidateOriginCommon, p)
	}
	return out
}

// scanDiscoveryEntry 在一个来源路径（文件或目录）里找启动器。
// missing=true 只对用户指定/保存路径有意义：那是"你让我看的这条坏了"。
func scanDiscoveryEntry(src discoverySource) (files []string, missing bool) {
	info, err := os.Stat(src.path)
	if err != nil {
		userPinned := src.origin == CandidateOriginPreferred || src.origin == CandidateOriginSaved
		return nil, userPinned
	}
	if !info.IsDir() {
		return []string{src.path}, false
	}
	for _, name := range candidateNames() {
		p := filepath.Join(src.path, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return []string{p}, false
		}
	}
	return nil, false
}

// candidateNames 是目录里依次尝试的启动器名字。
// .ps1/.js 也列进来：它们不可按 native 方式执行，但必须作为候选出现并被诊断，
// 不能静默消失。
func candidateNames() []string {
	if isWindows() {
		return []string{"pi.exe", "pi.cmd", "pi.bat", "pi.ps1"}
	}
	return []string{"pi", "pi.js", "pi.ps1"}
}

// newCandidate 构造未解析的候选骨架（只填路径与来源）。
func newCandidate(launcher, origin string) PiCandidate {
	abs := launcher
	if a, err := filepath.Abs(launcher); err == nil {
		abs = a
	}
	src := originSource(origin)
	return PiCandidate{
		Origin:       origin,
		LauncherPath: abs,
		Installation: PiInstallation{
			ID:         installationID(abs),
			Executable: abs,
			Source:     src,
			Origin:     originFor(src),
		},
	}
}

// resolveCandidate 解析真实路径、运行时与指纹，并按版本策略给出兼容状态。
func resolveCandidate(ctx context.Context, c PiCandidate, req DiscoveryRequest, policy VersionPolicy, vprobe VersionProbe) PiCandidate {
	info, err := os.Stat(c.LauncherPath)
	if err != nil {
		c.Installation.Runnable = false
		c.Compatibility = StateMissing
		c.Diagnostics = append(c.Diagnostics, "kind:\"missing-path\" 候选路径不存在: "+c.LauncherPath)
		return c
	}
	if info.IsDir() {
		c.Installation.Runnable = false
		c.Compatibility = StateIncompatible
		c.Diagnostics = append(c.Diagnostics, "kind:\"invalid-path\" 候选是目录而不是启动器: "+c.LauncherPath)
		return c
	}

	real := c.LauncherPath
	if r, serr := filepath.EvalSymlinks(real); serr == nil {
		real = r
	}
	c.Installation.Executable = real
	c.Installation.ID = installationID(real)
	c.Installation.Architecture = hostArch()

	hash, herr := sha256File(real)
	if herr != nil {
		c.Installation.Runnable = false
		c.Compatibility = StateIncompatible
		c.Diagnostics = append(c.Diagnostics, "kind:\"unreadable-launcher\" 无法读取启动器: "+herr.Error())
		return c
	}
	c.ExecutableHash = hash

	// 启动器本身就落在微信开发者工具目录里：同样拒绝（按实际路径命名）。
	if wechatOrigin(real) {
		c.Installation.Runnable = false
		c.Compatibility = StateIncompatible
		c.Diagnostics = append(c.Diagnostics, wechatDiagnostic(real))
		return c
	}

	// PowerShell 启动器受执行策略约束：不假装可直接执行。
	if strings.EqualFold(filepath.Ext(real), ".ps1") {
		c.Distribution = DistributionNpm
		c.Installation.Architecture = "any"
		c.Installation.Runnable = false
		c.Installation.Note = "PowerShell shim（pi.ps1）不可直接执行：受执行策略约束"
		c.Compatibility = StateIncompatible
		c.Diagnostics = append(c.Diagnostics, "kind:\"ps1-not-directly-runnable\" 需要 powershell -File 包装，本层不承诺可用")
		return c
	}

	c.Distribution = DistributionStandalone
	if isScriptLauncher(real) {
		c.Distribution = DistributionNpm
		c.Installation.Architecture = "any"
	}

	// Unix 上"文件存在但没有执行位"是真实的不可用，不是可用。
	if !isWindows() && info.Mode().Perm()&0o111 == 0 {
		c.Installation.Runnable = false
		c.Compatibility = StateIncompatible
		c.Diagnostics = append(c.Diagnostics, "kind:\"not-executable\" 文件没有执行位: "+real)
		return c
	}

	if c.Distribution == DistributionNpm {
		runtimeExe, rerr := resolveRuntimeExecutable(real, req.PathEnv)
		if rerr != nil {
			c.Installation.Runnable = false
			c.Installation.Note = rerr.Error()
			c.Compatibility = StateIncompatible
			c.Diagnostics = append(c.Diagnostics, "kind:\"runtime-missing\" "+rerr.Error())
			return c
		}
		c.RuntimeExecutable = runtimeExe
		// 只认**解析到的运行时**才算数：launcher 在安全目录也可能指向微信的 Node。
		if wechatOrigin(runtimeExe) {
			c.Installation.Runnable = false
			c.Compatibility = StateIncompatible
			c.Diagnostics = append(c.Diagnostics, wechatDiagnostic(runtimeExe))
			return c
		}
		rh, rerr := sha256File(runtimeExe)
		if rerr != nil {
			c.Installation.Runnable = false
			c.Compatibility = StateIncompatible
			c.Diagnostics = append(c.Diagnostics, "kind:\"unreadable-runtime\" 无法读取运行时: "+rerr.Error())
			return c
		}
		c.RuntimeHash = rh
	} else {
		c.RuntimeExecutable = real
		c.RuntimeHash = hash
	}

	// 架构：只有能确定是 PE 且机器码与宿主不符时才判死；解析不出来就不猜。
	if c.Distribution == DistributionStandalone && isWindows() && strings.EqualFold(filepath.Ext(real), ".exe") {
		if mismatch, machine, ok := peArchMismatch(real); ok && mismatch {
			c.Installation.Runnable = false
			c.Compatibility = StateIncompatible
			c.Diagnostics = append(c.Diagnostics,
				fmt.Sprintf("kind:\"arch-mismatch\" 可执行体架构 %s 与宿主 %s 不符", machine, hostArch()))
			return c
		}
	}

	c.Installation.Runnable = true
	version, probeDiags := vprobe(ctx, c.Installation)
	c.Installation.Version = version
	if len(probeDiags) > 0 {
		c.Installation.Note = strings.Join(probeDiags, "; ")
	}
	compat, diags := classifyCompatibility(policy, version, c, probeDiags, req.ProbeFunc)
	c.Compatibility = compat
	c.Diagnostics = append(c.Diagnostics, diags...)
	return c
}

// classifyCompatibility 把"探测到的版本 + 可执行体指纹"映射为产品状态。
//
// 版本规则（F08-A，无契约探测）：
//   - 版本空（探测失败）→ incompatible，带探测诊断；
//   - 2.x / 无法解析主版本 → incompatible（2.x 尚无签名清单）；
//   - < 最低支持版本 → upgrade_available（真实候选，但不可选中）；
//   - 落在支持区间 → 只有"已认证版本 + 已登记哈希"才 ready_verified，
//     否则最高 ready_compatible。
//
// 关键纪律：PreCertifiedHashes 是随发行包下发的白名单，默认空。
// **未登记的可执行体哈希永远不可能变成 ready_verified**——本机跑通不是认证。
func classifyCompatibility(policy VersionPolicy, version string, cand PiCandidate, probeDiags []string, probe ProbeFunc) (CompatibilityState, []string) {
	if version == "" {
		diags := []string{"kind:\"version-probe-failed\" 版本探测失败，不得当作可用"}
		diags = append(diags, probeDiags...)
		return StateIncompatible, diags
	}
	ver, ok := parseSemver(version)
	if !ok {
		return StateIncompatible, []string{
			fmt.Sprintf("kind:\"unparsable-version\" 版本 %q 无法解析，按不兼容处理", version),
		}
	}
	if ver[0] >= 2 {
		return StateIncompatible, []string{
			fmt.Sprintf("kind:\"unsupported-major\" 版本 %s 属 2.x+，尚无签名清单，不静默放行", version),
		}
	}
	minVersion := minimumVersion(policy)
	if minVer, ok := parseSemver(minVersion); ok && versionLess(ver, minVer) {
		return StateUpgradeAvailable, []string{
			fmt.Sprintf("kind:\"upgrade-available\" 版本 %s 低于最低支持版本 %s；只能提示升级，不可选中", version, minVersion),
		}
	}

	verified := policy.IsCertified(version) && policy.PreCertifiedHashes[version][cand.ExecutableHash]
	diags := []string{}
	if !verified {
		diags = append(diags,
			"kind:\"contract-probe-pending\" 契约探测（F08-B）未执行或未通过；最高只能 ready_compatible")
	}
	if probe != nil {
		ok, failed := probe(context.Background(), cand)
		if ok {
			// 只有真正校验过签名清单的探测才允许升级到 ready_verified。
			verified = true
		}
		diags = append(diags, failed...)
	}
	if verified {
		return StateReadyVerified, diags
	}
	return StateReadyCompatible, diags
}

// minimumVersion 取策略里的最低支持版本，缺省 1.5.0（基线 §3）。
func minimumVersion(policy VersionPolicy) string {
	if v := strings.TrimSpace(policy.Minimum); v != "" {
		return v
	}
	return "1.5.0"
}

// parseSemver 解析 major.minor.patch。带后缀（-beta 等）视为无法解析：
// 基线只认证精确版本，宁可判不兼容也不猜。
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func versionLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// defaultVersionProbe 用真实 Probe（隔离 env 下运行 --version），
// 只取版本与失败诊断，不把"版本未认证"这类策略诊断混进发现层。
func defaultVersionProbe(ctx context.Context, inst PiInstallation) (string, []string) {
	res, err := Probe(ctx, inst)
	if err != nil {
		return "", []string{"kind:\"version-probe-failed\" " + err.Error()}
	}
	if res.Version == "" {
		diags := []string{"kind:\"version-probe-failed\" 无法解析 Pi 版本"}
		return "", append(diags, res.Diagnostics...)
	}
	return res.Version, nil
}

// resolveRuntimeExecutable 解析 shim 真实使用的 Node。
//
// 顺序：launcher 同目录的 node.exe/node（npm 全局安装的常见布局）→ 同样的 PATH 顺序。
// 只做文件查找，不执行、不解析 shim 文本、不经 shell。
func resolveRuntimeExecutable(launcher string, pathEnv []string) (string, error) {
	if p := findNodeIn(filepath.Dir(launcher)); p != "" {
		return p, nil
	}
	for _, dir := range pathEnv {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if p := findNodeIn(dir); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("找不到启动该 shim 所需的 Node 运行时（已按同目录→PATH 顺序搜索）: %s", launcher)
}

func findNodeIn(dir string) string {
	names := []string{"node", "node.exe"}
	if isWindows() {
		names = []string{"node.exe", "node"}
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// isScriptLauncher 判断启动器是否只是脚本/Node shim（真实执行体不是它自己）。
func isScriptLauncher(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat", ".ps1", ".js":
		return true
	}
	return false
}

// wechatOrigin 判断路径是否落在微信开发者工具目录里。
func wechatOrigin(path string) bool {
	p := strings.ToLower(filepath.Clean(path))
	if strings.Contains(p, "微信web开发者工具") {
		return true
	}
	if strings.Contains(p, "wechatwebdevtools") {
		return true
	}
	return strings.Contains(p, "wechat") && strings.Contains(p, "devtools")
}

// wechatDiagnostic 是 PI_DISC02 要求的拒绝理由：kind + **实际解析到的路径**。
func wechatDiagnostic(path string) string {
	return "kind:\"unexpected-node-origin\" 解析到的运行时/启动器来自微信开发者工具目录（不是用户自己的 Node/Pi），拒绝使用: " + path
}

// peArchMismatch 读取 Windows PE 头判断机器码是否与宿主不符。
// 不是合法 PE（例如测试占位文件）时 ok=false：不猜、不因此判死。
func peArchMismatch(path string) (mismatch bool, machine string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, "", false
	}
	defer f.Close()
	var dos [64]byte
	if _, err := io.ReadFull(f, dos[:]); err != nil {
		return false, "", false
	}
	if dos[0] != 'M' || dos[1] != 'Z' {
		return false, "", false
	}
	off := int64(binary.LittleEndian.Uint32(dos[0x3C:0x40]))
	if off <= 0 || off > 1<<20 {
		return false, "", false
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return false, "", false
	}
	var sig [6]byte
	if _, err := io.ReadFull(f, sig[:]); err != nil {
		return false, "", false
	}
	if string(sig[:4]) != "PE\x00\x00" {
		return false, "", false
	}
	m := binary.LittleEndian.Uint16(sig[4:6])
	host, known := map[string]uint16{"amd64": 0x8664, "arm64": 0xAA64, "386": 0x014c}[runtime.GOARCH]
	if !known {
		return false, "", false
	}
	return m != host, fmt.Sprintf("0x%04x", m), true
}

// sha256File 以流式方式计算文件 sha256（不把大二进制读进内存）。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dedupeByRealPath 按解析后的真实路径去重，保留先出现（更高优先级）的那条。
func dedupeByRealPath(cands []PiCandidate) []PiCandidate {
	out := make([]PiCandidate, 0, len(cands))
	seen := map[string]bool{}
	for _, c := range cands {
		key := strings.ToLower(filepath.Clean(c.Installation.Executable))
		if c.Compatibility == StateMissing {
			key = strings.ToLower(filepath.Clean(c.LauncherPath))
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// firstUsable 返回第一个可选中候选：ready_verified 优先，其次 ready_compatible。
func firstUsable(cands []PiCandidate) *PiCandidate {
	for _, want := range []CompatibilityState{StateReadyVerified, StateReadyCompatible} {
		for i := range cands {
			if cands[i].Compatibility == want {
				chosen := cands[i]
				return &chosen
			}
		}
	}
	return nil
}

func selectable(state CompatibilityState) bool {
	return state == StateReadyVerified || state == StateReadyCompatible
}

// originRank 是来源优先级（数字越小越优先）。
func originRank(origin string) int {
	switch origin {
	case CandidateOriginPreferred:
		return 0
	case CandidateOriginSaved:
		return 1
	case CandidateOriginManaged:
		return 2
	case CandidateOriginPATH:
		return 3
	case CandidateOriginCommon:
		return 4
	}
	return 5
}

// originSource 把优先级词表映射到 PiInstallation.Source。
func originSource(origin string) Source {
	switch origin {
	case CandidateOriginPreferred:
		return SourceUserSpecified
	case CandidateOriginSaved:
		return SourceSavedPath
	case CandidateOriginManaged:
		return SourceManaged
	case CandidateOriginPATH:
		return SourcePATH
	case CandidateOriginCommon:
		return SourceCommon
	}
	return SourceCommon
}
