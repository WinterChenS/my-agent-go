package app

// 本文件是 F06 的绑定层：托管 Pi 的安装 / 取消 / 回滚（PI_INS01–03、PI_UPG01–03）。
//
// 三条纪律：
//   - UI 只能给**版本与目标**，绝不给 URL 或任意路径：下载地址与哈希只来自随二进制
//     下发的受信清单（internal/piinstall/manifest.json）；
//   - 进度事件（myagent:pi-install）只承载阶段；权威状态一律由返回值给出
//     （installing / rollback_available / failed / ready_*），UI 不得从进度推断状态；
//   - 失败必须带结构化错误码与失败阶段，不静默回退、不假装成功。
//
// 事件名沿用 myagent: 前缀：U00 负责 Anvil 改名（届时统一改成 anvil:），
// 这里不提前改名，避免前端与宿主两边不一致。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/piinstall"
	"my-agent-go/internal/runtime/pi"
)

// PiInstallEventName 是安装进度事件类型。
const PiInstallEventName = "myagent:pi-install"

// PiInstallResult 是一次安装/回滚/取消的权威结果。
//
// 字段与 internal/piinstall.Result 一一对应；State 是 F08 的 CompatibilityState
// （字符串形式），ManagedRoot 说明这次操作实际用了哪个托管根（证据要对得上）。
type PiInstallResult struct {
	InstallID         string `json:"installId,omitempty"`
	State             string `json:"state"`
	ManagedRoot       string `json:"managedRoot"`
	Version           string `json:"version,omitempty"`
	TargetID          string `json:"targetId,omitempty"`
	Executable        string `json:"executable,omitempty"`
	ExecutableSHA256  string `json:"executableSha256,omitempty"`
	InstallationID    string `json:"installationId,omitempty"`
	PreviousVersion   string `json:"previousVersion,omitempty"`
	RollbackAvailable bool   `json:"rollbackAvailable"`
	AutoRolledBack    bool   `json:"autoRolledBack,omitempty"`
	FailedStage       string `json:"failedStage,omitempty"`
	Error             string `json:"error,omitempty"`
	// EventName 让前端不必硬编码事件名（U00 改名时只改一处）。
	EventName string `json:"eventName"`
}

// piInstallProgressEvent 是进度事件的 payload（只有阶段，没有结论）。
type piInstallProgressEvent struct {
	InstallID string `json:"installId,omitempty"`
	Stage     string `json:"stage"`
}

// piInstallService 返回安装服务单例（懒建）。
//
// 生产路径不注入任何东西：托管根取 pi.ManagedRoot()，清单取嵌入清单，
// HTTP 客户端与探针都用默认实现。测试通过 piInstallOverride 覆盖这些依赖。
func (d *Desktop) piInstallService() (*piinstall.InstallService, error) {
	d.piInstallMu.Lock()
	defer d.piInstallMu.Unlock()
	if d.piInstallSvc != nil {
		return d.piInstallSvc, nil
	}
	manifest, err := piinstall.LoadEmbeddedManifest()
	if err != nil {
		return nil, err
	}
	root := pi.ManagedRoot()
	if root == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing,
			"无法确定托管 Pi 根目录（MYAGENT_PI_HOME 与平台数据目录都不可用）", false)
	}
	svc := &piinstall.InstallService{ManagedRoot: root, Manifest: manifest}
	if d.piInstallOverride != nil {
		d.piInstallOverride(svc)
	}
	d.piInstallSvc = svc
	return svc, nil
}

// InstallPi 一键安装指定版本的托管 Pi。
//
// 同步执行并返回权威结果：Wails 绑定层负责把它放到后台协程；进度通过事件发出。
func (d *Desktop) InstallPi(version string) PiInstallResult {
	svc, err := d.piInstallService()
	if err != nil {
		return piInstallFailedResult("", "", err)
	}
	target := pi.HostTarget()
	out, installErr := svc.Install(context.Background(),
		piinstall.InstallRequest{Version: version, TargetID: target},
		d.piInstallProgress())
	return piInstallResultFrom(out, installErr)
}

// CancelPiInstall 取消正在进行的安装。幂等：不在跑就返回 nil。
func (d *Desktop) CancelPiInstall(installID string) error {
	svc, err := d.piInstallService()
	if err != nil {
		return err
	}
	return svc.Cancel(installID)
}

// RollbackPi 把托管 Pi 回滚到唯一的已验证旧版。
//
// installationID 必须是当前 active 的托管安装 ID（来自 DiscoverPi 的候选 ID）：
// 不认识的 ID 报 NOT_FOUND，绝不"回滚到别的什么东西"。
func (d *Desktop) RollbackPi(installationID string) PiInstallResult {
	svc, err := d.piInstallService()
	if err != nil {
		return piInstallFailedResult("", "", err)
	}
	out, rbErr := svc.Rollback(context.Background(), installationID)
	return piInstallResultFrom(out, rbErr)
}

// piInstallProgress 把阶段进度转成事件（只发阶段，不发结论）。
func (d *Desktop) piInstallProgress() func(piinstall.Stage) {
	return func(stage piinstall.Stage) {
		d.mu.Lock()
		emitter := d.emitter
		d.mu.Unlock()
		if emitter == nil {
			return
		}
		payload, err := json.Marshal(piInstallProgressEvent{
			InstallID: d.activePiInstallID(),
			Stage:     string(stage),
		})
		if err != nil {
			// payload 是固定结构，编码失败说明代码坏了；不要发一个假事件出去。
			return
		}
		emitter(contracts.Event{
			SchemaVersion: contracts.EventSchemaVersion,
			EventID:       fmt.Sprintf("%s-%s-%d", PiInstallEventName, stage, time.Now().UnixNano()),
			Type:          PiInstallEventName,
			OccurredAt:    time.Now().UTC(),
			Payload:       payload,
		})
	}
}

// activePiInstallID 返回正在进行的安装 ID（没有则空串），只用于事件关联。
func (d *Desktop) activePiInstallID() string {
	d.piInstallMu.Lock()
	svc := d.piInstallSvc
	d.piInstallMu.Unlock()
	if svc == nil {
		return ""
	}
	return svc.ActiveInstallID()
}

// piInstallResultFrom 把安装层结果映射成绑定层结果。
func piInstallResultFrom(out piinstall.Result, err error) PiInstallResult {
	res := PiInstallResult{
		InstallID:         out.InstallID,
		State:             string(out.State),
		ManagedRoot:       out.ManagedRoot,
		Version:           out.Version,
		TargetID:          out.TargetID,
		Executable:        out.Executable,
		ExecutableSHA256:  out.ExecutableSHA256,
		InstallationID:    out.InstallationID,
		PreviousVersion:   out.PreviousVersion,
		RollbackAvailable: out.RollbackAvailable,
		AutoRolledBack:    out.AutoRolledBack,
		FailedStage:       string(out.FailedStage),
		EventName:         PiInstallEventName,
	}
	if err != nil {
		res.Error = err.Error()
		if res.State == "" {
			res.State = string(pi.StateFailed)
		}
	}
	return res
}

// piInstallFailedResult 构造"连服务都没建起来"的失败结果。
func piInstallFailedResult(managedRoot, version string, err error) PiInstallResult {
	res := PiInstallResult{
		State:       string(pi.StateFailed),
		ManagedRoot: managedRoot,
		Version:     version,
		EventName:   PiInstallEventName,
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}
