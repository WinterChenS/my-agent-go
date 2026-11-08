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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
func (d *Desktop) DiscoverPi() (pi.DiscoveryResult, error) {
	return pi.Discover(context.Background(), d.piDiscoveryRequest())
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
