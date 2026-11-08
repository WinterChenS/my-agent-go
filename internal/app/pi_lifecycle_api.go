package app

// 本文件是 F08-A 的绑定层：Pi 候选发现与选择（PI_DISC01/02）。
//
// 三条纪律：
//   - 发现是只读的：不改 PATH、不写设置、不创建目录、不动用户真实 Pi 配置；
//   - 请求字段来源显式：settings.piPath 是用户指定，settings.piSavedPath 是
//     “上一次选定的 Pi”（F07 持久化），PATH 取进程环境的一份拷贝，
//     托管根由 pi.ManagedRoot() 给出（F06 才负责填充）；
//   - 选择绝不静默回退：不认识的 id、不可用的候选都报结构化错误。
//
// “选定”与“运行”是两件事：这里只维护发现来源与 persisted 的 saved 路径，
// 运行路径仍由 resolveInstallation 决定（settings.piPath → active.json → 缺失），
// saved 不是第三优先级。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/piinstall"
	"my-agent-go/internal/runtime/pi"
)

// piDiscoveryRequest 组装一次发现请求（生产环境的真实字段来源）。
func (d *Desktop) piDiscoveryRequest() pi.DiscoveryRequest {
	s := d.Settings()
	req := pi.DiscoveryRequest{
		Preferred: s.PiPath,
		// 上次选定的 Pi（F07）：失效时由 F08 报成 missing-path 候选与诊断，
		// 不静默消失、也不自动换成 PATH 上的另一个。
		Saved:           s.PiSavedPath,
		ManagedRoot:     managedDiscoveryRoot(pi.ManagedRoot()),
		PathEnv:         splitPathEntries(os.Getenv("PATH")),
		CommonLocations: pi.CommonLocations(),
		Policy:          pi.DefaultPolicy,
	}
	// 测试注入点：只覆盖"发现环境"（PATH/常见位置/探针），不改变字段来源本身。
	// 生产路径恒为 nil，避免 UI 看到的环境和真实环境不一致。
	d.mu.Lock()
	override := d.piRequestOverride
	d.mu.Unlock()
	if override != nil {
		req = override(req)
	}
	return req
}

// DiscoverPi 探测本机所有 Pi 候选（PI_DISC01/02）。只读，不改 PATH、不改外部文件。
//
// 这是**完整**发现：每个可用候选都会跑六项隔离契约探测（spawn 真实 Pi，单个候选
// 本机实测约 10s）。因此它只适合用户显式点“验证契约”的动作，不适合挂载时调用；
// 挂载/列表走 DiscoverPiFast。
//
// FIX2：这个动作**总是**重新探测（不读缓存），并把结论写回运行前闸门用的同一份
// 缓存（<appRoot>/pi-contract-cache.json）。于是“验证完立刻发消息”不会再等 10s。
func (d *Desktop) DiscoverPi() (pi.DiscoveryResult, error) {
	res, err := pi.Discover(context.Background(), d.piDiscoveryRequest())
	if err != nil {
		return res, err
	}
	res.Diagnostics = append(res.Diagnostics, d.recordPiDiscoveryContractResults(res)...)
	return res, nil
}

// DiscoverPiFast 是挂载与列表用的快速发现：只做 --version 版本探测 + 路径/指纹解析，
// 不 spawn 任何契约探测子进程。
//
// 承诺：候选的真实路径、来源、版本、发行形式、指纹与“能不能启动”的判定。
// 不承诺：这个二进制会不会说我们的协议。因此结果里 Verification=version_only、
// 每个候选的 ContractVerified=false，状态最高 ready_compatible；要升级到
// contract_probed 必须由用户显式跑 DiscoverPi（设置页“验证契约”）。
func (d *Desktop) DiscoverPiFast() (pi.DiscoveryResult, error) {
	return pi.DiscoverFast(context.Background(), d.piDiscoveryRequest())
}

// SelectPi 在已发现候选里选择指定 installationID；不在候选里或不可用则报错，绝不静默回退。
//
// 选中后会把这个候选的可执行体路径写进 settings.piSavedPath（既有配置 API：
// persistSettings），使选择跨重启保留。三条约束：
//   - 只有选择成功才写盘：失败的选择必须让 settings.json 字节不变；
//   - 走 persistPiSelection 而不是 persistSettings/SaveSettings：选 Pi 不能把
//     正在跑的 run 连根拔掉，新选择与其它配置一样在下一个 run 生效；
//   - 写盘失败就是没生效：返回错误，并且不把这次 Selected 当作已落地结果交出去。
func (d *Desktop) SelectPi(installationID string) (pi.DiscoveryResult, error) {
	// 选择用快速发现：点一下“选用”不该再等每个候选的六项契约探测（本机实测 10s+）。
	// 契约验证由设置页的显式“验证契约”动作负责，不在选择路径上：选择只回答
	// “下次跑的时候用哪个二进制”，不假装验证过它的协议。
	res, err := d.DiscoverPiFast()
	if err != nil {
		return res, err
	}
	sel, err := pi.Select(context.Background(), res, installationID)
	if err != nil {
		return sel, err
	}
	if sel.Selected == nil {
		return sel, contracts.NewAppError(contracts.CodeInternal,
			"选择成功但没有选中项，拒绝持久化: "+installationID, false)
	}

	// 存**解析后的真实路径**（Installation.Executable），与发现层的真实路径
	// 去重口径一致：存启动器别名会让下次发现多出一条同源候选。
	if err := d.persistPiSelection(sel.Selected.Installation.Executable); err != nil {
		// 落盘失败：内存设置已在 persistSettings 里保持不动，这里也不把
		// 新 Selected 交出去——否则调用方会以为选择已经生效。
		return res, fmt.Errorf("保存选中的 Pi 路径失败: %w", err)
	}
	return sel, nil
}

// persistPiSelection 只把"上次选定的 Pi"写进 settings.json，不提交整份内存快照。
//
// 为什么不能直接复用 persistSettings(d.Settings())：内存里的 Settings 已经过
// applyEnvOverrides，把整份快照写回磁盘等于把这一次进程的环境变量（MYAGENT_PI_PATH
// 等）永久钉进用户配置。选一个 Pi 是一次点击动作，不该顺带固化一组用户没输入过的值；
// 固化之后即使环境变量消失，配置里也留着那条路径，而 resolveInstallation 对失效的
// piPath 是**不静默回退**的，用户会突然打不开任何 Pi。
//
// 所以这里以磁盘上的文档为基线（没有文件就是默认值），只改这一个字段。
// 环境覆盖仍在 loadSettings 时应用，优先级没变：本次进程内 piPath 还是覆盖值，
// 只是不再被写进文件。
func (d *Desktop) persistPiSelection(path string) error {
	base := defaultSettings(d.root)
	if raw, err := os.ReadFile(d.settingsPath()); err == nil {
		if err := json.Unmarshal(raw, &base); err != nil {
			// 解析失败不静默覆盖用户文件：宁可报错，也不要拿默认值把
			// 一份用户手改坏了的配置抹掉。
			return fmt.Errorf("解析 settings.json 失败，未写入选择: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取 settings.json 失败: %w", err)
	}
	base.PiSavedPath = path

	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(d.settingsPath(), raw, 0o600); err != nil {
		return fmt.Errorf("写 settings.json 失败: %w", err)
	}

	// 内存里也只同步这一个字段：其它字段仍是本次进程的实际生效值（含环境覆盖），
	// 下次发现请求读到的 saved 必须与磁盘一致。
	d.mu.Lock()
	d.settings.PiSavedPath = path
	d.mu.Unlock()
	return nil
}

// managedDiscoveryRoot 返回交给 F08 发现的托管根。
//
// F06 的安装布局是 <managed>/versions/<version>-<target>/pi(.exe)，而 F08 的
// discoverySources 只扫托管根及它的**一层**子目录：把 <managed> 直接传过去，
// 它只会看到 versions/（里面没有启动器）与 active.json，托管安装对应用不可见。
// 所以有 versions/ 时就把这一层交给它；没有则保持托管根（空目录/旧布局行为不变）。
//
// 注意：发现层的托管候选按**目录名**排序（1.5.0 排在 1.6.0 前面），名字顺序不是
// 版本顺序。因此“当前版本是哪个”不由排序回答，而由 <managed>/active.json 回答
// （见 resolveInstallation）。发现结果只用于列出候选与来源。
func managedDiscoveryRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	versions := filepath.Join(root, piinstall.VersionsDirName)
	if info, err := os.Stat(versions); err == nil && info.IsDir() {
		return versions
	}
	return root
}

// splitPathEntries 把 PATH 拆成条目列表（保留顺序，丢掉空条目）。
func splitPathEntries(pathEnv string) []string {
	out := make([]string, 0, 8)
	for _, e := range strings.Split(pathEnv, string(os.PathListSeparator)) {
		if strings.TrimSpace(e) == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ── FIX2：运行前的 Pi 契约验证（带缓存） ────────────────────────────────────
//
// 背景（2026-11-08 实测）：resolveInstallation 只校验“路径存在 + 托管指针哈希”，
// 六项契约探测只在设置页显式“验证契约”时跑。于是一个版本合法、协议却坏掉的 Pi
// 在 readiness 里显示可用，开跑之后才失败——用户看到“连不上/没权限”，没有可行动
// 的错误码，也不知道去哪里修。
//
// 闸门就放在运行时构建路径上（ensureRuntime 调 resolveVerifiedInstallation）：
//   - 解析出安装件后，按 (可执行体身份, sha256, 最低支持版本) 查缓存；
//   - 命中且 TTL 内 → 直接放行，**不 spawn** 任何探测（常见路径不额外变慢）；
//   - 未命中 → 在临时隔离 agentDir/workspace 里跑完整六项探测（有上限超时）；
//   - 结论写回内存 + <appRoot>/pi-contract-cache.json，重启不必重探；
//   - 失败也缓存，但 TTL 更短；显式“验证契约”（DiscoverPi）总是重探并刷新同一份缓存。
//
// 为什么键里必须有 sha256：路径不变但二进制被换掉（升级、手改、半截下载）是最常见
// 的失效场景；只按路径缓存会把一个从未验证过的二进制当成已验证。

const (
	// piContractCacheFileName 是持久化缓存文件名（生产在 <appRoot> 下）。
	piContractCacheFileName = "pi-contract-cache.json"
	// piContractCacheVersion 是缓存文档格式版本；不认识的版本当作空缓存。
	piContractCacheVersion = 1
	// piContractSuccessTTL 是“通过”结论的有效期：二进制没变时一天最多探测一次。
	piContractSuccessTTL = 24 * time.Hour
	// piContractFailureTTL 是“未通过”结论的有效期。
	//
	// 为什么要短：失败结论会拦住 run，用户可能刚装好/刚修好就来重试；
	// 太长会让人以为“明明修好了还是不行”，太短则每次点发送都白等一轮探测。
	piContractFailureTTL = 2 * time.Minute
	// piContractProbeTimeout / piContractProbeCheckTimeout 是闸门探测的时限。
	//
	// 与 F08-B 的默认值一致：整次 60s（六项各 10s）。超时算失败，不算通过——
	// 一个会卡死的 Pi 本来就不能拿来跑 run。
	piContractProbeTimeout      = 60 * time.Second
	piContractProbeCheckTimeout = 10 * time.Second
)

// 缓存条目的 State 取值。只记“契约行不行”，不重复 F08 的版本/指纹认证结论。
const (
	piContractStateCompatible   = "compatible"
	piContractStateIncompatible = "incompatible"
)

// piContractCacheEntry 是一条契约验证结论。
//
// 键是 (Path, SHA256, MinimumVersion) 三元组；Version 只用于展示与诊断
// （用户指定路径时 resolveInstallation 不跑 --version，版本号不可靠，因此不参与键）。
type piContractCacheEntry struct {
	Path           string    `json:"path"`
	SHA256         string    `json:"sha256"`
	Version        string    `json:"version"`
	MinimumVersion string    `json:"minimumVersion"`
	State          string    `json:"state"`
	CheckedAt      time.Time `json:"checkedAt"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	FailedChecks   []string  `json:"failedChecks,omitempty"`
}

// piContractCacheDocument 是缓存文件的顶层结构。
type piContractCacheDocument struct {
	Version int                             `json:"version"`
	Entries map[string]piContractCacheEntry `json:"entries"`
}

// resolveVerifiedInstallation 是 FIX2 的闸门：解析安装件 **并** 验证它的契约。
//
// 调用点只有运行时构建（ensureRuntime）：run 真正要 spawn Pi 的那一刻。
// 已有运行时、配置签名没变时不会走到这里，所以“同一条消息反复发”不会反复验证。
func (d *Desktop) resolveVerifiedInstallation(ctx context.Context, s Settings) (pi.PiInstallation, error) {
	inst, err := d.resolveInstallation(s)
	if err != nil {
		return inst, err
	}
	if err := d.assertPiContract(ctx, inst); err != nil {
		return inst, err
	}
	return inst, nil
}

// assertPiContract 用缓存（或一次探测）确认这个安装件的契约可用。
// 失败返回 COMPATIBILITY 结构化错误，点名失败项并给出修复动作；
// 调用方负责“不启动 run”（本层不碰 run，只拒绝）。
func (d *Desktop) assertPiContract(ctx context.Context, inst pi.PiInstallation) error {
	entry, err := d.verifiedPiContract(ctx, inst, false)
	if err != nil {
		return err
	}
	if entry.State == piContractStateCompatible {
		return nil
	}
	return piContractFailureError(entry)
}

// verifiedPiContract 返回这个安装件的契约结论：命中就用缓存，未命中就跑一次探测。
//
// refresh=true 时跳过缓存读取（显式“验证契约”语义），但结论仍然写回缓存。
func (d *Desktop) verifiedPiContract(ctx context.Context, inst pi.PiInstallation, refresh bool) (piContractCacheEntry, error) {
	skeleton, key, err := d.piContractKeyFor(inst)
	if err != nil {
		return skeleton, err
	}
	if !refresh {
		if entry, ok := d.cachedPiContract(key); ok {
			return entry, nil
		}
	}

	report, err := d.probePiContract(ctx, inst)
	if err != nil {
		// 探测根本无法进行（隔离目录建不出来、上下文取消）：这是环境问题，
		// 不是对被测 Pi 的结论，因此**不写缓存**——修好后立刻可以重试。
		var appErr *contracts.AppError
		if errors.As(err, &appErr) {
			return skeleton, appErr
		}
		return skeleton, contracts.NewAppError(contracts.CodeCompatibility,
			"Pi 契约验证无法完成，这次 run 没有启动: "+err.Error(), true)
	}

	entry := skeleton
	entry.CheckedAt = d.piContractNow()
	entry.Capabilities = append([]string(nil), report.Capabilities...)
	if report.State == pi.StateReadyCompatible && len(report.FailedChecks) == 0 {
		entry.State = piContractStateCompatible
	} else {
		entry.State = piContractStateIncompatible
		entry.FailedChecks = append([]string(nil), report.FailedChecks...)
		if len(entry.FailedChecks) == 0 {
			entry.FailedChecks = []string{"六项契约检查未全部通过（state=" + string(report.State) + "）"}
		}
	}
	if err := d.storePiContractEntry(key, entry); err != nil {
		// 缓存写不进去不影响本次结论（fail closed 的结论仍在返回值里），
		// 但下一次会重探：留痕，而不是假装缓存生效了。
		d.setLastErr("契约验证结论未能写入缓存（下一次 run 前会重新探测）：" + err.Error())
	}
	return entry, nil
}

// piContractKeyFor 算出 (可执行体身份, sha256, 最低版本) 与对应的缓存键。
//
// 失败（可执行体不可读）不是“没验证过”，而是没法验证：返回结构化错误，不放行。
func (d *Desktop) piContractKeyFor(inst pi.PiInstallation) (piContractCacheEntry, string, error) {
	identity, fingerprint, err := piInstallationFingerprint(inst)
	if err != nil {
		return piContractCacheEntry{}, "", contracts.NewAppError(contracts.CodeVerificationFailed,
			"无法计算 Pi 可执行体指纹，这个安装件不能进入契约验证: "+err.Error(), false)
	}
	minimum := piContractMinimumVersion()
	skeleton := piContractCacheEntry{
		Path:           identity,
		SHA256:         fingerprint,
		Version:        inst.Version,
		MinimumVersion: minimum,
	}
	return skeleton, piContractCacheKey(identity, fingerprint, minimum), nil
}

func piContractCacheKey(identity, fingerprint, minimumVersion string) string {
	return identity + "|" + fingerprint + "|" + minimumVersion
}

// piContractMinimumVersion 取契约结论所依据的最低支持版本（来自 F08 的版本策略）。
//
// 它进缓存键：最低版本抬高了（例如 1.5.0 → 1.6.0），旧结论就不再适用，必须重探。
func piContractMinimumVersion() string {
	if v := strings.TrimSpace(pi.DefaultPolicy.Minimum); v != "" {
		return v
	}
	return "1.5.0"
}

// piInstallationFingerprint 返回可执行体身份与合并 sha256。
//
// 身份 = 真正被执行的路径 + argv 里的文件参数（npm 形式是 node + 一个 .js 入口，
// 只看 node 会让“换了 pi 的 .js”看起来没变）。合并哈希覆盖这些文件的内容，
// 任何一个变了整个指纹就变，缓存自然失效。
func piInstallationFingerprint(inst pi.PiInstallation) (string, string, error) {
	targets := make([]string, 0, 1+len(inst.Args))
	targets = append(targets, inst.Executable)
	for _, a := range inst.Args {
		if filepath.IsAbs(a) {
			targets = append(targets, a)
		}
	}

	identity := piInstallationIdentity(inst)
	h := sha256.New()
	hashed := 0
	seen := map[string]bool{}
	for _, target := range targets {
		// 路径归一必须与身份一致（含符号链接解析与 Windows 大小写）：否则同一个文件
		// 因为“短名/长名”两种写法定出两个指纹，缓存永远不命中。
		path := piCanonicalExecutablePath(target)
		if seen[path] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		digest, err := piinstall.HashFileContent(path)
		if err != nil {
			return "", "", fmt.Errorf("读取 %s 失败: %w", path, err)
		}
		seen[path] = true
		hashed++
		_, _ = io.WriteString(h, path)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, digest)
		_, _ = io.WriteString(h, "\n")
	}
	if hashed == 0 {
		return "", "", fmt.Errorf("可执行体不存在或不可读: %s", inst.Executable)
	}
	return identity, hex.EncodeToString(h.Sum(nil)), nil
}

// piInstallationIdentity 是缓存键里的“可执行体身份”（做符号链接解析与大小写归一）。
func piInstallationIdentity(inst pi.PiInstallation) string {
	id := piCanonicalExecutablePath(inst.Executable)
	args := make([]string, 0, len(inst.Args))
	for _, a := range inst.Args {
		if !filepath.IsAbs(a) {
			continue
		}
		args = append(args, piCanonicalExecutablePath(a))
	}
	if len(args) > 0 {
		id += " " + strings.Join(args, " ")
	}
	return id
}

// piCanonicalExecutablePath 把一个路径归一成“同一个文件只有一种写法”。
//
// Windows 会把同一路径写成短名（ADMINI~1）或长名，符号链接/大小写同理；
// 缓存键与指纹都必须走这一个函数，否则“明明没换二进制却永远不命中”。
func piCanonicalExecutablePath(path string) string {
	p := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// probePiContract 跑一次真实探测（或测试注入的替身）。
//
// 隔离纪律与 F08-B 一致：临时目录、独立 agentDir/workspace、探测自己起一个只绑回环
// 的 stub provider（不借用户的模型配置与密钥）。
func (d *Desktop) probePiContract(ctx context.Context, inst pi.PiInstallation) (pi.ContractReport, error) {
	d.mu.Lock()
	injected := d.piContractProbe
	d.mu.Unlock()
	if injected != nil {
		return injected(ctx, inst)
	}
	root, err := os.MkdirTemp("", "anvil-pi-contract-gate-")
	if err != nil {
		return pi.ContractReport{}, contracts.NewAppError(contracts.CodeCompatibility,
			"无法创建契约验证的隔离目录，这次 run 没有启动: "+err.Error(), true)
	}
	defer func() { _ = os.RemoveAll(root) }()
	return pi.ContractProbe(ctx, inst, pi.ContractOptions{
		AgentDir:     filepath.Join(root, "agent"),
		WorkspaceDir: filepath.Join(root, "workspace"),
		Timeout:      piContractProbeTimeout,
		CheckTimeout: piContractProbeCheckTimeout,
	})
}

// cachedPiContract 读缓存；过期/缺失都算未命中。
func (d *Desktop) cachedPiContract(key string) (piContractCacheEntry, bool) {
	now := d.piContractNow()
	d.piContractMu.Lock()
	defer d.piContractMu.Unlock()
	d.loadPiContractCacheLocked()
	entry, ok := d.piContractCache[key]
	if !ok {
		return piContractCacheEntry{}, false
	}
	if !piContractEntryFresh(entry, now) {
		return piContractCacheEntry{}, false
	}
	return entry, true
}

// piContractEntryFresh 按结论类型判 TTL：通过 24h、未通过 2min。
func piContractEntryFresh(entry piContractCacheEntry, now time.Time) bool {
	if entry.CheckedAt.IsZero() {
		return false
	}
	ttl := piContractSuccessTTL
	if entry.State != piContractStateCompatible {
		ttl = piContractFailureTTL
	}
	age := now.Sub(entry.CheckedAt)
	// age < 0（时钟回拨）也按过期处理：宁可重探，也不能拿一条“来自未来”的结论放行。
	return age >= 0 && age < ttl
}

// storePiContractEntry 写内存缓存并落盘。
func (d *Desktop) storePiContractEntry(key string, entry piContractCacheEntry) error {
	d.piContractMu.Lock()
	defer d.piContractMu.Unlock()
	d.loadPiContractCacheLocked()
	d.piContractCache[key] = entry
	return d.persistPiContractCacheLocked()
}

// piContractCachePath 是持久化缓存位置（<appRoot>/pi-contract-cache.json）。
func (d *Desktop) piContractCachePath() string {
	return filepath.Join(d.root, piContractCacheFileName)
}

// loadPiContractCacheLocked 懒加载磁盘缓存（调用方必须持有 piContractMu）。
//
// 读不到/读坏了都当作空缓存：缓存只是加速，不是放行依据；
// 真正放行永远依赖“有一条新鲜且 compatible 的结论”，而不是“文件存在”。
func (d *Desktop) loadPiContractCacheLocked() {
	if d.piContractLoaded {
		return
	}
	d.piContractLoaded = true
	d.piContractCache = map[string]piContractCacheEntry{}
	raw, err := os.ReadFile(d.piContractCachePath())
	if err != nil {
		return
	}
	var doc piContractCacheDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	if doc.Version != piContractCacheVersion || doc.Entries == nil {
		return
	}
	d.piContractCache = doc.Entries
}

// persistPiContractCacheLocked 原子写回缓存（调用方必须持有 piContractMu）。
func (d *Desktop) persistPiContractCacheLocked() error {
	doc := piContractCacheDocument{Version: piContractCacheVersion, Entries: d.piContractCache}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := d.piContractCachePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// piContractNow 返回当前时间（测试可注入时钟以验证 TTL）。
func (d *Desktop) piContractNow() time.Time {
	d.mu.Lock()
	clock := d.piContractClock
	d.mu.Unlock()
	if clock != nil {
		return clock()
	}
	return time.Now()
}

// recordPiDiscoveryContractResults 把一次完整发现的契约结论写回运行闸门的缓存。
//
// 为什么共享：用户点“验证契约”看到通过，紧接着发消息却又等 10s 重探，等于让
// 一次显式验证白做。共享的代价是必须承认粒度差——发现层的 ProbeFunc 只回答
// verified 与失败项，拿不到逐项 capabilities，所以从这里写入的条目 Capabilities 为空。
//
// 只记录**真的跑过探测**的候选：通过 = ContractVerified；失败 = 诊断里有
// contract-probe-failed（classifyCompatibility 只在探测真的跑完并失败时写它）。
// 低于最低版本、2.x、missing 这些根本没探测过，写进缓存等于冒充。
//
// 返回诊断：缓存写失败不改变“验证已通过”这个事实，但必须说出来（下一次 run 会重探）。
func (d *Desktop) recordPiDiscoveryContractResults(res pi.DiscoveryResult) []string {
	var diags []string
	for _, cand := range res.Candidates {
		if !cand.Installation.Runnable || strings.TrimSpace(cand.Installation.Executable) == "" {
			continue
		}
		var state string
		switch {
		case cand.ContractVerified:
			state = piContractStateCompatible
		case len(contractFailureDiagnostics(cand.Diagnostics)) > 0:
			state = piContractStateIncompatible
		default:
			// 没有探测证据（未探测/低于基线/2.x/missing）：不冒充“已探测过”。
			continue
		}
		skeleton, key, err := d.piContractKeyFor(cand.Installation)
		if err != nil {
			diags = append(diags, "kind:\"contract-cache-write-failed\" 验证结论未能写进缓存（下一次 run 前会重新探测）: "+err.Error())
			continue
		}
		skeleton.State = state
		skeleton.CheckedAt = d.piContractNow()
		if cand.ContractVerified {
			skeleton.Capabilities = append([]string(nil), pi.ContractChecks...)
		}
		if state == piContractStateIncompatible {
			skeleton.FailedChecks = contractFailureDiagnostics(cand.Diagnostics)
		}
		if err := d.storePiContractEntry(key, skeleton); err != nil {
			diags = append(diags, "kind:\"contract-cache-write-failed\" 验证结论未能写进缓存（下一次 run 前会重新探测）: "+err.Error())
		}
	}
	return diags
}

// contractFailureDiagnostics 从发现诊断里挑出“探测失败”的那几条，作为失败项原文。
func contractFailureDiagnostics(diags []string) []string {
	out := make([]string, 0, 2)
	for _, d := range diags {
		if strings.Contains(d, "contract-probe-failed") {
			out = append(out, d)
		}
	}
	return out
}

// piContractFailureError 把一条“未通过”的结论说成可行动的结构化错误。
//
// 三件事必须在错误里同时出现，否则用户还是只能猜：
//  1. 哪个可执行体、什么版本（同一台机器上可能有好几个 Pi）；
//  2. 哪几项契约没过（不是“连不上”这种无法行动的结论）；
//  3. 下一步动作（设置 → Pi → 验证），带机器可读的 params.action。
func piContractFailureError(entry piContractCacheEntry) error {
	failed := strings.Join(entry.FailedChecks, "；")
	if strings.TrimSpace(failed) == "" {
		failed = "六项契约检查未全部通过"
	}
	msg := fmt.Sprintf("Pi 契约验证未通过，这次 run 没有启动：%s（可执行体 %s，版本 %s）。"+
		"修复：打开 设置 → Pi → 验证（重新探测并刷新结论），或换用受支持的官方独立包（>= %s）后重试",
		failed, entry.Path, entry.Version, entry.MinimumVersion)
	return contracts.NewAppError(contracts.CodeCompatibility, msg, false).
		WithParams(map[string]string{
			"action":       "settings.pi.verify",
			"path":         entry.Path,
			"version":      entry.Version,
			"failedChecks": strings.Join(entry.FailedChecks, ","),
		}).
		WithOperation("run.start")
}
