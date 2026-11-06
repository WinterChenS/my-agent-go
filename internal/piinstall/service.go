package piinstall

// 本文件实现 F06 的安装事务（计划 F06 Step 5 / 规格 §8.2）。
//
// 事务顺序是固定的：download → verify → extract → probe → activate。
// 每一步失败都只有一个结果：**active 指针与旧版本哈希不变，staging 被删掉，
// 半成品不会被 F08 Discover 当成可用**。没有"部分成功"。
//
// 状态口径（前端不得从进度推断）：
//   - installing：事务进行中；
//   - failed：事务失败，FailedStage 说明停在哪一步；
//   - rollback_available：成功激活且磁盘上确实还有一个哈希仍对得上的旧版；
//   - 其余是 F08 的 ready_* 状态（由 Discover 给出）。
//
// 并发：同一时刻只允许一个安装事务；第二个请求返回结构化 CONFIG_CONFLICT。
// 取消是幂等的：对不在跑的 installID 调 Cancel 返回 nil。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// Stage 是安装事务的阶段名。
type Stage string

const (
	StageDownload Stage = "download"
	StageVerify   Stage = "verify"
	StageExtract  Stage = "extract"
	StageProbe    Stage = "probe"
	StageActivate Stage = "activate"
	// StageValidate 表示"在进入下载前就被校验闸门拒绝"（未知目标、清单缺条目、
	// sizeBytes 未测量等）。它不是安装事务里的一步，但失败结果必须说清楚停在哪。
	StageValidate Stage = "validate"
)

// InstallStages 是阶段顺序（证据/展示用）。
var InstallStages = []Stage{StageDownload, StageVerify, StageExtract, StageProbe, StageActivate}

// ProbeFunc 是 F08 契约探测的注入缝（与 pi.ProbeFunc 同一类型）。
type ProbeFunc = pi.ProbeFunc

// InstallRequest 是一次安装请求。只有版本与目标：UI 不能给 URL 或任意路径。
type InstallRequest struct {
	Version  string `json:"version"`
	TargetID string `json:"targetId"`
}

// Result 是一次安装/回滚的权威结果。
type Result struct {
	InstallID        string                `json:"installId"`
	State            pi.CompatibilityState `json:"state"`
	ManagedRoot      string                `json:"managedRoot"`
	Version          string                `json:"version,omitempty"`
	TargetID         string                `json:"targetId,omitempty"`
	Executable       string                `json:"executable,omitempty"`
	ExecutableSHA256 string                `json:"executableSha256,omitempty"`
	// InstallationID 是当前 active 托管安装的 F08 口径 ID（UI 回滚时原样传回）。
	InstallationID    string `json:"installationId,omitempty"`
	PreviousVersion   string `json:"previousVersion,omitempty"`
	RollbackAvailable bool   `json:"rollbackAvailable"`
	// AutoRolledBack 表示"激活后自检失败 → 自动回滚到原 active"。
	AutoRolledBack bool   `json:"autoRolledBack,omitempty"`
	Stage          Stage  `json:"stage,omitempty"`
	FailedStage    Stage  `json:"failedStage,omitempty"`
	Error          string `json:"error,omitempty"`
}

// InstallService 是托管 Pi 的安装器。
type InstallService struct {
	ManagedRoot string
	Manifest    Manifest
	HTTPClient  *http.Client
	Probe       ProbeFunc
	Now         func() time.Time

	// 以下是测试/发行注入缝，生产留空即取安全默认值。
	HostAllowlist []string
	MaxRedirects  int
	PublicKey     ed25519.PublicKey
	ExtractLimits ExtractLimits
	// VersionProbe 只在测试里注入（生产用 F08 默认的真实 --version 探针）。
	VersionProbe pi.VersionProbe

	mu      sync.Mutex
	running map[string]context.CancelFunc
	order   []string
}

type runningInstall struct {
	id     string
	cancel context.CancelFunc
}

func (s *InstallService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *InstallService) limits() ExtractLimits {
	if s.ExtractLimits.MaxTotalBytes > 0 && s.ExtractLimits.MaxFiles > 0 {
		return s.ExtractLimits
	}
	return DefaultExtractLimits()
}

// probeFunc 返回实际使用的 probe：注入优先，否则用真实可执行体探针。
func (s *InstallService) probeFunc(expectedVersion string) ProbeFunc {
	if s.Probe != nil {
		return s.Probe
	}
	return executableProbe(expectedVersion)
}

// executableProbe 是生产/真实包的探针：在隔离 env 下跑 Pi 自己的 --version。
//
// 为什么不是六项契约探测：pi.ContractProbe 启动 worker 时不带 --mode rpc
// （F08 冻结，2026-11-05 实测六个检查全部 RPC_TIMEOUT），对真实可执行体无法成立。
// 六项契约探测的可注入缝（ProbeFunc）保留给上层；本包不假装跑过它。
func executableProbe(expectedVersion string) ProbeFunc {
	return func(ctx context.Context, cand pi.PiCandidate) (bool, []string) {
		res, err := pi.Probe(ctx, cand.Installation)
		if err != nil {
			return false, []string{"kind:\"probe-failed\" 无法执行候选可执行体: " + err.Error()}
		}
		switch res.State {
		case pi.StateNotFound, pi.StateProbeFailed, pi.StateDependencyMissing:
			return false, []string{"kind:\"probe-failed\" 候选可执行体探测失败: " +
				strings.Join(res.Diagnostics, "; ")}
		}
		if res.Version == "" {
			return false, []string{"kind:\"probe-failed\" 候选可执行体没有自报版本"}
		}
		if expectedVersion != "" && res.Version != expectedVersion {
			return false, []string{fmt.Sprintf(
				"kind:\"probe-version-mismatch\" 候选自报版本 %s，清单要求 %s", res.Version, expectedVersion)}
		}
		return true, []string{"kind:\"probe-passed\" 隔离 env 下 --version 通过: " + res.Version}
	}
}

// Install 执行一次安装事务。
//
// progress 只承载阶段进度；权威状态一律看返回的 Result。
func (s *InstallService) Install(ctx context.Context, req InstallRequest, progress func(Stage)) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	version := strings.TrimPrefix(strings.TrimSpace(req.Version), "v")
	res := Result{ManagedRoot: s.ManagedRoot, State: pi.StateInstalling, Version: version}
	emit := func(st Stage) {
		if progress != nil {
			progress(st)
		}
	}

	target, err := hostTargetOrDefault(req.TargetID)
	if err != nil {
		return failResult(res, StageValidate, err)
	}
	res.TargetID = target
	if version == "" {
		return failResult(res, StageValidate, contracts.NewAppError(contracts.CodeConfigMissing,
			"安装请求必须指定版本", false))
	}
	entry, err := s.Manifest.Entry(version, target)
	if err != nil {
		return failResult(res, StageValidate, err)
	}

	installID, err := newInstallID()
	if err != nil {
		return failResult(res, StageValidate, err)
	}
	res.InstallID = installID

	runCtx, cancel := context.WithCancel(ctx)
	if err := s.begin(installID, cancel); err != nil {
		cancel()
		return failResult(res, StageValidate, err)
	}
	defer func() {
		cancel()
		s.finish(installID)
	}()

	root := s.ManagedRoot
	if strings.TrimSpace(root) == "" {
		return failResult(res, StageValidate, contracts.NewAppError(contracts.CodeConfigMissing,
			"未配置托管 Pi 根目录", false))
	}
	staging := filepath.Join(root, stagingDirName, installID)
	tree := filepath.Join(staging, "tree")
	if err := EnsureInsideManagedRoot(root, staging); err != nil {
		return failResult(res, StageValidate, err)
	}
	// 事务结束（成功或失败）都要清掉 staging：成功时树已经被 rename 走了。
	defer func() { _ = os.RemoveAll(staging) }()

	// ── download ──
	emit(StageDownload)
	downloader := &Downloader{
		Client:       s.HTTPClient,
		AllowedHosts: s.HostAllowlist,
		PublicKey:    s.PublicKey,
		MaxRedirects: s.MaxRedirects,
	}
	staged, err := downloader.Fetch(runCtx, entry, staging)
	if err != nil {
		return failResult(res, StageDownload, err)
	}
	res.Stage = StageDownload

	// ── verify（从磁盘重算，证明盘上字节就是清单字节）──
	emit(StageVerify)
	verified, err := VerifyStagedFile(entry, staged.Path, s.PublicKey)
	if err != nil {
		return failResult(res, StageVerify, err)
	}
	res.Stage = StageVerify

	// ── extract ──
	emit(StageExtract)
	expectedExe, err := ExpectedExecutable(target)
	if err != nil {
		return failResult(res, StageExtract, err)
	}
	extracted, err := Extract(verified.Path, tree, expectedExe, s.limits())
	if err != nil {
		return failResult(res, StageExtract, err)
	}
	if err := ValidateExecutableForTarget(extracted.Executable, target); err != nil {
		return failResult(res, StageExtract, err)
	}
	res.Stage = StageExtract

	// ── probe（对 **staging** 里的可执行体，激活前）──
	emit(StageProbe)
	candidate := pi.PiCandidate{
		Installation: pi.PiInstallation{
			ID:           installID,
			Executable:   extracted.Executable,
			Version:      version,
			Origin:       pi.OriginManaged,
			Architecture: hostArchForTarget(target),
			Source:       pi.SourceManaged,
			Runnable:     true,
		},
		Origin:            pi.CandidateOriginManaged,
		Distribution:      pi.DistributionStandalone,
		ExecutableHash:    extracted.ExecutableSHA256,
		LauncherPath:      extracted.Executable,
		RuntimeExecutable: extracted.Executable,
		RuntimeHash:       extracted.ExecutableSHA256,
	}
	ok, failedChecks := s.probeFunc(version)(runCtx, candidate)
	if !ok {
		return failResult(res, StageProbe, verificationError(
			"staging 可执行体未通过 probe（激活前失败，active 不变）: "+fmtErrs(failedChecks)))
	}
	if err := runCtx.Err(); err != nil {
		return failResult(res, StageProbe, contracts.NewAppError(contracts.CodeRpcCancelled,
			"安装在 probe 之后被取消: "+err.Error(), true))
	}
	res.Stage = StageProbe

	// ── activate ──
	emit(StageActivate)
	old, err := ReadActive(root)
	if err != nil {
		return failResult(res, StageActivate, err)
	}
	versionDir := filepath.Join(root, versionsDirName, versionDirName(version, target))
	if err := EnsureInsideManagedRoot(root, versionDir); err != nil {
		return failResult(res, StageActivate, err)
	}
	if err := os.MkdirAll(filepath.Join(root, versionsDirName), 0o700); err != nil {
		return failResult(res, StageActivate, fmt.Errorf("piinstall: 创建版本目录失败: %w", err))
	}
	// 同一版本重复安装：哈希一致就复用（不删可能正在被 worker 使用的文件）。
	if existing, statErr := os.Stat(versionDir); statErr == nil && existing.IsDir() {
		exePath := filepath.Join(versionDir, expectedExe)
		if sum, hashErr := hashFileContent(exePath); hashErr == nil && strings.EqualFold(sum, extracted.ExecutableSHA256) {
			_ = os.RemoveAll(tree)
		} else if rmErr := os.RemoveAll(versionDir); rmErr != nil {
			return failResult(res, StageActivate, contracts.NewAppError(contracts.CodeConfigConflict,
				"已有同名版本目录且无法替换（可能正被运行中的 worker 使用）: "+rmErr.Error(), false))
		}
	}
	if _, err := os.Stat(versionDir); err != nil {
		if err := os.Rename(tree, versionDir); err != nil {
			return failResult(res, StageActivate, fmt.Errorf("piinstall: 提升版本目录失败: %w", err))
		}
	}
	activatedExe := filepath.Join(versionDir, expectedExe)
	pointer := ActivePointer{
		SchemaVersion:    ActiveSchemaVersion,
		Version:          version,
		TargetID:         target,
		VersionDir:       filepath.ToSlash(filepath.Join(versionsDirName, versionDirName(version, target))),
		Executable:       expectedExe,
		ExecutableSHA256: extracted.ExecutableSHA256,
		InstalledAt:      s.now(),
		InstallationID:   installationIDFor(activatedExe),
	}
	// 只保留一个已验证旧版：旧 active 只有在可执行体哈希仍对得上时才记成 previous。
	pointer.Previous = previousFromActive(root, old)
	if err := WriteActive(root, pointer); err != nil {
		return failResult(res, StageActivate, err)
	}

	// 激活后自检：F08 Discover 必须能发现、选中这个可执行体，且它自报的版本
	// 必须就是清单里的版本（否则"装上了别的东西"也算失败）。
	disc, discErr := s.discover(runCtx, activatedExe)
	if discErr != nil || !selectedIs(disc, activatedExe) || !selectedVersionIs(disc, version) {
		// 自动回滚到原 active（不留一个"切过去了但用不了"的状态）。
		restoreErr := s.restoreActive(root, old)
		_ = os.RemoveAll(versionDir)
		res.AutoRolledBack = restoreErr == nil
		if restoreErr != nil {
			return failResult(res, StageActivate, contracts.NewAppError(contracts.CodeControlUncertain,
				"激活后自检失败，且恢复原 active 也失败: "+restoreErr.Error(), true))
		}
		reason := "激活后 F08 Discover 未能确认新版本可用（发现/选中/自报版本任一不符）"
		if discErr != nil {
			reason += ": " + discErr.Error()
		} else if disc.Selected != nil {
			reason += fmt.Sprintf(": 选中 %s（状态 %s，自报版本 %s）",
				disc.Selected.Installation.Executable, disc.Selected.Compatibility,
				disc.Selected.Installation.Version)
		}
		// 自动回滚成功后，结果必须描述**机器现在的真实状态**，而不是一个含糊的 failed：
		// 旧版还在跑、旧版的旧版还在，就仍然是 rollback_available。
		res.State = s.currentState(runCtx, activeExeOf(root, old))
		res.RollbackAvailable = false
		if old != nil && old.Previous != nil {
			res.State = pi.StateRollbackAvailable
			res.RollbackAvailable = true
			res.PreviousVersion = old.Previous.Version
		}
		res.FailedStage = StageActivate
		res.Error = reason + "；已自动回滚到原 active"
		return res, verificationError(res.Error)
	}

	res.Stage = StageActivate
	res.InstallationID = pointer.InstallationID
	res.Executable = activatedExe
	res.ExecutableSHA256 = extracted.ExecutableSHA256
	res.State = disc.State
	res.RollbackAvailable = false
	if pointer.Previous != nil {
		res.PreviousVersion = pointer.Previous.Version
		res.State = pi.StateRollbackAvailable
		res.RollbackAvailable = true
	}
	return res, nil
}

// ActiveInstallID 返回当前正在进行的安装 ID（没有则空串）。
func (s *InstallService) ActiveInstallID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) == 0 {
		return ""
	}
	return s.order[0]
}

// Cancel 取消指定安装。幂等：不在跑就返回 nil。
func (s *InstallService) Cancel(installID string) error {
	s.mu.Lock()
	cancel, ok := s.running[installID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	cancel()
	return nil
}

// Rollback 把 active 切回唯一的已验证旧版。
//
// 幂等：没有可回滚旧版时返回当前状态且不报错（重复点"回滚"不会来回翻转）；
// installationID 必须是当前 active 的 ID，否则 NotFound（不静默回滚别人的安装）。
func (s *InstallService) Rollback(ctx context.Context, installationID string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root := s.ManagedRoot
	res := Result{ManagedRoot: root, InstallID: newInstallIDQuiet()}
	current, err := ReadActive(root)
	if err != nil {
		res.State = pi.StateFailed
		return res, err
	}
	if current == nil {
		res.State = pi.StateMissing
		return res, contracts.NewAppError(contracts.CodeNotFound, "没有 active 的托管 Pi，无法回滚", false)
	}
	res.Version = current.Version
	res.TargetID = current.TargetID
	res.InstallationID = current.InstallationID
	if id := strings.TrimSpace(installationID); id != "" && id != current.InstallationID {
		res.State = pi.StateFailed
		return res, contracts.NewAppError(contracts.CodeNotFound,
			fmt.Sprintf("installationID %q 不是当前 active 的托管安装（当前 %q）", id, current.InstallationID), false)
	}
	exe, exeErr := ActiveExecutable(root, current)
	if exeErr == nil {
		res.Executable = exe
		res.ExecutableSHA256 = current.ExecutableSHA256
	}

	prev, prevExe, prevErr := RollbackTarget(root, current)
	if prevErr != nil {
		// 没有可回滚旧版：幂等返回当前状态。
		res.State = s.currentState(ctx, exe)
		res.RollbackAvailable = false
		if current.Previous == nil {
			return res, nil
		}
		// previous 记录在但已失效（哈希不符/目录没了）：如实报错，不静默"成功"。
		res.State = pi.StateFailed
		return res, prevErr
	}

	restored := ActivePointer{
		SchemaVersion:    ActiveSchemaVersion,
		Version:          prev.Version,
		TargetID:         prev.TargetID,
		VersionDir:       prev.VersionDir,
		Executable:       prev.Executable,
		ExecutableSHA256: prev.ExecutableSHA256,
		InstalledAt:      s.now(),
		InstallationID:   installationIDFor(prevExe),
	}
	if err := WriteActive(root, restored); err != nil {
		res.State = pi.StateFailed
		return res, err
	}
	res.Version = restored.Version
	res.TargetID = restored.TargetID
	res.InstallationID = restored.InstallationID
	res.Executable = prevExe
	res.ExecutableSHA256 = restored.ExecutableSHA256
	res.RollbackAvailable = false
	res.State = s.currentState(ctx, prevExe)
	return res, nil
}

// previousFromActive 把当前 active 转成"可回滚旧版"记录；哈希对不上就返回 nil。
//
// 注意：这里不能复用 RollbackTarget——那个函数读的是 active.Previous，
// 而升级时的"旧版"就是 active 本身。
func previousFromActive(managedRoot string, p *ActivePointer) *PreviousVersion {
	if p == nil {
		return nil
	}
	if _, err := ActiveExecutable(managedRoot, p); err != nil {
		return nil
	}
	return &PreviousVersion{
		Version:          p.Version,
		TargetID:         p.TargetID,
		VersionDir:       p.VersionDir,
		Executable:       p.Executable,
		ExecutableSHA256: p.ExecutableSHA256,
		InstalledAt:      p.InstalledAt,
	}
}

// currentState 用 F08 Discover 给出当前 active 的权威状态（发现不到就 failed）。
func (s *InstallService) currentState(ctx context.Context, exe string) pi.CompatibilityState {
	if exe == "" {
		return pi.StateFailed
	}
	disc, err := s.discover(ctx, exe)
	if err != nil || !selectedIs(disc, exe) {
		return pi.StateFailed
	}
	return disc.State
}

// discover 用 F08 的发现逻辑扫托管版本目录。
func (s *InstallService) discover(ctx context.Context, preferred string) (pi.DiscoveryResult, error) {
	return pi.Discover(ctx, pi.DiscoveryRequest{
		Preferred:    preferred,
		ManagedRoot:  filepath.Join(s.ManagedRoot, versionsDirName),
		Policy:       pi.DefaultPolicy,
		VersionProbe: s.VersionProbe,
		ProbeFunc:    s.probeFunc(""),
	})
}

// restoreActive 把 active 指针恢复成 old（old 为 nil 时删除指针）。
func (s *InstallService) restoreActive(root string, old *ActivePointer) error {
	if old == nil {
		err := os.Remove(filepath.Join(root, ActiveFileName))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return WriteActive(root, *old)
}

// begin 抢占唯一的安装槽位。
func (s *InstallService) begin(installID string, cancel context.CancelFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		s.running = map[string]context.CancelFunc{}
	}
	if len(s.running) > 0 {
		runningID := s.order[0]
		return contracts.NewAppError(contracts.CodeConfigConflict,
			fmt.Sprintf("已有安装在进行（%s），不允许并发安装", runningID), false)
	}
	s.running[installID] = cancel
	s.order = append(s.order, installID)
	return nil
}

// finish 释放槽位。
func (s *InstallService) finish(installID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, installID)
	for i, id := range s.order {
		if id == installID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// activeExeOf 返回指针指向的可执行体（读不到就空串）。
func activeExeOf(managedRoot string, p *ActivePointer) string {
	if p == nil {
		return ""
	}
	exe, err := ActiveExecutable(managedRoot, p)
	if err != nil {
		return ""
	}
	return exe
}

// selectedVersionIs 判断被选中候选自报的版本是否等于期望版本。
func selectedVersionIs(disc pi.DiscoveryResult, version string) bool {
	if disc.Selected == nil {
		return false
	}
	return strings.TrimPrefix(strings.TrimSpace(disc.Selected.Installation.Version), "v") == version
}

// selectedIs 判断 Discover 选中的是不是指定可执行体。
func selectedIs(disc pi.DiscoveryResult, exe string) bool {
	if disc.Selected == nil {
		return false
	}
	if disc.Selected.Compatibility != pi.StateReadyCompatible && disc.Selected.Compatibility != pi.StateReadyVerified {
		return false
	}
	return samePath(disc.Selected.Installation.Executable, exe)
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil {
		a = ra
	}
	if errB == nil {
		b = rb
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// installationIDFor 与 F08 同口径的安装件 ID（pi-<hash>）。
//
// 口径必须与 internal/runtime/pi 的 installationID 一致：先 EvalSymlinks 再小写，
// 取 sha256 前 6 字节的十六进制。否则 UI 拿到的 ID 在回滚时对不上。
func installationIDFor(exe string) string {
	real := exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		real = r
	}
	sum := hashString(strings.ToLower(real))
	return "pi-" + sum[:12]
}

func newInstallID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成 installID 失败: "+err.Error(), true)
	}
	return "pii-" + hex.EncodeToString(b[:]), nil
}

func newInstallIDQuiet() string {
	id, err := newInstallID()
	if err != nil {
		return ""
	}
	return id
}

// failResult 把结果标成 failed 并带上失败阶段。
func failResult(res Result, stage Stage, err error) (Result, error) {
	res.State = pi.StateFailed
	res.FailedStage = stage
	if err != nil {
		res.Error = err.Error()
	}
	return res, err
}
