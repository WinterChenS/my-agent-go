package app

// 审批档位的绑定层入口（规格 docs/specs/2026-11-08-permission-mode-full-access.md）。
//
// 这一层只做三件事：
//   1. 把档位持久化进 settings.json（沿用既有 CAS 凭据，不新开第二套写入口）；
//   2. 提交 run 时把**当前**档位交给 RunService（在那里进 ExecutionBinding 快照）；
//   3. 不因切换档位而重建运行时——重建会关掉正在跑的 run，与"运行中切换只影响
//      下一条 run"的规格直接冲突。
//
// 用户可见的开关（编辑框下方的"⚠ 允许完全访问"）由后续前端任务接这个入口。

import (
	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// SetPermissionMode 持久化审批档位。
//
// 与 SaveSettings 的关键差别：**不** resetRuntime()。
//   - 档位在 run 受理时被读一次并冻结进 ExecutionBinding，运行中的 run 不重新解析；
//   - 因此切换开关不需要（也不允许）打断正在跑的 run。
//
// CAS：先用磁盘字节折算凭据，再带凭据写回。设置文件还有别的写者（SaveSettings /
// SaveSettingsWithRevision / 选 Pi / 用户手工编辑），"只改一个字段"的写者
// 一样要受 CAS 约束，否则它就成了绕过冲突检测的后门。
func (d *Desktop) SetPermissionMode(mode string) SettingsResult {
	normalized, ok := config.NormalizePermissionMode(mode)
	if !ok {
		err := contracts.NewAppError(contracts.CodeInvalidInput,
			"未知的权限档位: "+mode+"（只能是 ask 或 full_access）", false).
			WithOperation("settings.permissionMode")
		return SettingsResult{Settings: d.Settings(), Error: toCallError(err)}
	}

	raw, err := d.readSettingsFile()
	if err != nil {
		return SettingsResult{Settings: d.Settings(), Error: toCallError(err)}
	}
	expected := settingsRevisionOf(raw)

	d.mu.Lock()
	next := d.settings
	d.mu.Unlock()
	next.PermissionMode = normalized

	written, err := d.saveSettings(next, &expected)
	if err != nil {
		return SettingsResult{Settings: d.Settings(), Revision: written, Error: toCallError(err)}
	}
	// 内存里的生效值由 saveSettings 按"刚落盘的文档 + 环境变量"重新解析，
	// 所以这里读回来的是真实生效的那一份，不是我们以为写进去的那一份。
	return SettingsResult{Settings: d.Settings(), Revision: written}
}
