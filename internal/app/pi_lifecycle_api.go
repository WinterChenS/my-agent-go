package app

// 本文件是 F08-A 的绑定层：Pi 候选发现与选择（PI_DISC01/02）。
//
// 三条纪律：
//   - 发现是只读的：不改 PATH、不写设置、不创建目录、不动用户真实 Pi 配置；
//   - 请求字段来源显式：settings.piPath 是用户指定，PATH 取进程环境的一份拷贝，
//     托管根由 pi.ManagedRoot() 给出（F06 才负责填充）；
//   - 选择绝不静默回退：不认识的 id、不可用的候选都报结构化错误。
//
// 已知缺口（记录在 F08-A 报告里）：F07 目前没有"已保存的 Pi 路径"字段，
// 因此 Saved 传空串；等配置里出现该字段时在这里接上，不新造文件格式。

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"my-agent-go/internal/piinstall"
	"my-agent-go/internal/runtime/pi"
)

// piDiscoveryRequest 组装一次发现请求（生产环境的真实字段来源）。
func (d *Desktop) piDiscoveryRequest() pi.DiscoveryRequest {
	s := d.Settings()
	req := pi.DiscoveryRequest{
		Preferred: s.PiPath,
		// Saved 为空：配置里还没有"上次选中的 Pi 路径"字段（见文件头说明）。
		Saved:           "",
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
func (d *Desktop) DiscoverPi() (pi.DiscoveryResult, error) {
	return pi.Discover(context.Background(), d.piDiscoveryRequest())
}

// SelectPi 在已发现候选里选择指定 installationID；不在候选里或不可用则报错，绝不静默回退。
func (d *Desktop) SelectPi(installationID string) (pi.DiscoveryResult, error) {
	res, err := d.DiscoverPi()
	if err != nil {
		return res, err
	}
	return pi.Select(context.Background(), res, installationID)
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
