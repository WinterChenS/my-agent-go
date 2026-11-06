package piinstall

// PI_INS01–03 / PI_UPG01–03 的落地测试。
//
// 每条 case 都写 external-observation 需要的四个 artifact（见 observation_test.go），
// 断言一律落在磁盘/进程表/active.json 上，不看"服务自己说成功了"。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

const (
	insVersion = "1.5.0"
	// 三个版本都必须在 DefaultPolicy.Minimum（1.5.0）以上：低于最低支持版本会被
	// F08 判为 upgrade_available，那是"该升级"而不是"装坏了"，不该拿来演回滚。
	upgV0 = "1.5.0"
	upgV1 = "1.6.0"
	upgV2 = "1.7.0"
)

func hostTarget(t *testing.T) string {
	t.Helper()
	target := pi.HostTarget()
	if target == "" {
		t.Fatalf("宿主不是六目标之一（%s/%s），本包无法在未知目标上取证", runtime.GOOS, runtime.GOARCH)
	}
	return target
}

// ── PI_INS01 ────────────────────────────────────────────────────────────────

// TestInstallRejectsInvalidPackageBeforeProbe 是 PI_INS01 的核心：
// 清单/HTTPS/重定向/size/hash/签名/归档边界/目标架构任何一项不合格，都必须在
// **probe 阶段之前**失败，且 probe 一次都不许被调用。
func TestInstallRejectsInvalidPackageBeforeProbe(t *testing.T) {
	target := hostTarget(t)
	piName := expectedExecutableFor(target)

	cases := []struct {
		name     string
		version  string
		targetID string
		setup    func(h *harness, entry ManifestEntry)
		wantCode string
		// wantStage 是必须停下的阶段：证明"在 probe 之前就被挡住"的具体位置。
		wantStage Stage
	}{
		{
			name: "哈希不符", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				h.setEntry(entry.Version, entry.TargetID, func(e *ManifestEntry) {
					e.SHA256 = strings.Repeat("0", 64)
				})
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageDownload,
		},
		{
			name: "明文 HTTP URL", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				h.setEntry(entry.Version, entry.TargetID, func(e *ManifestEntry) {
					e.URL = strings.Replace(e.URL, "https://", "http://", 1)
				})
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageDownload,
		},
		{
			name: "重定向到非白名单主机", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				h.mu.Lock()
				h.redir["/"+entry.AssetName] = "https://evil.example/" + entry.AssetName
				h.mu.Unlock()
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageDownload,
		},
		{
			name: "size 与清单不符", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				h.setEntry(entry.Version, entry.TargetID, func(e *ManifestEntry) { e.SizeBytes = 1 })
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageDownload,
		},
		{
			name: "清单条目缺少 sizeBytes", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				h.setEntry(entry.Version, entry.TargetID, func(e *ManifestEntry) { e.SizeBytes = 0 })
			},
			wantCode:  contracts.CodeUnsupported,
			wantStage: StageValidate,
		},
		{
			name: "zip 相对越界", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				body := zipBytes(t, map[string][]byte{piName: []byte("x"), "../escape": []byte("x")})
				h.replacePackageBody(entry.Version, entry.TargetID, body)
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageExtract,
		},
		{
			name: "zip 绝对路径", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				body := zipBytes(t, map[string][]byte{piName: []byte("x"), "/absolute": []byte("x")})
				h.replacePackageBody(entry.Version, entry.TargetID, body)
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageExtract,
		},
		{
			name: "zip 符号链接越界", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				body := zipBytesWithModes(t, []zipEntrySpec{
					{Name: piName, Body: []byte("x"), Mode: 0o755},
					{Name: "link-outside", Body: []byte("/etc/passwd"), Mode: os.ModeSymlink | 0o777},
				})
				h.replacePackageBody(entry.Version, entry.TargetID, body)
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageExtract,
		},
		{
			name: "目标架构不符", version: insVersion, targetID: target,
			setup: func(h *harness, entry ManifestEntry) {
				// 顶层有 pi.exe，但内容不是目标平台的可执行体。
				body := zipBytes(t, map[string][]byte{piName: []byte("not a real executable")})
				h.replacePackageBody(entry.Version, entry.TargetID, body)
			},
			wantCode:  contracts.CodeVerificationFailed,
			wantStage: StageExtract,
		},
		{
			name: "未知目标", version: insVersion, targetID: "solaris-x64",
			setup:     func(h *harness, entry ManifestEntry) {},
			wantCode:  contracts.CodeUnknownTarget,
			wantStage: StageValidate,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			entry := h.addPackage(insVersion, target)
			tc.setup(h, entry)

			res, err, rec := h.install(tc.version, tc.targetID)
			if err == nil {
				t.Fatalf("非法安装必须失败，实际成功: %+v", res)
			}
			if code := appErrCode(err); code != tc.wantCode {
				t.Fatalf("错误码 = %q，期望 %q（err=%v）", code, tc.wantCode, err)
			}
			// 最关键的一条：probe 一次都不许被调用。
			if got := h.probe.count(); got != 0 {
				t.Fatalf("校验失败时 probe 必须一次都不调用，实际调用 %d 次（%v）", got, h.probe.recordedPaths())
			}
			if p := h.active(); p != nil {
				t.Fatalf("校验失败不得写 active 指针: %+v", p)
			}
			assertNoStaging(t, h.Root)
			if disc := h.discoverManaged(); disc.State == pi.StateReadyCompatible || disc.State == pi.StateReadyVerified {
				t.Fatalf("半成品不得被发现为可用: %+v", disc)
			}
			if res.State != pi.StateFailed {
				t.Errorf("失败结果的 state = %q，期望 %q", res.State, pi.StateFailed)
			}
			if res.FailedStage != tc.wantStage {
				t.Errorf("失败阶段 = %q，期望 %q（这条场景必须停在该阶段才算真的测到了）",
					res.FailedStage, tc.wantStage)
			}

			writeInstallObservation(t, h.Root, map[string]any{
				"case":          "PI_INS01",
				"scenario":      tc.name,
				"state":         string(res.State),
				"errorCode":     appErrCode(err),
				"error":         err.Error(),
				"failedStage":   string(res.FailedStage),
				"probeCalls":    h.probe.count(),
				"stages":        summarizeStages(rec),
				"activeVersion": activeVersion(h),
				"managedRoot":   h.Root,
				"realProbe":     "未调用（校验先失败）",
			}, rec, captureExternalSnapshot(t, ""))
		})
	}
}

// ── PI_INS03 ────────────────────────────────────────────────────────────────

// TestInstallFailuresPreserveActiveAndLeaveNoHalfInstall 是 PI_INS03：
// 取消、断网、校验/解压/probe 失败时，active 与旧版本 hash 不变，staging 消失，
// 半成品不被 Discover 报告为可用。
func TestInstallFailuresPreserveActiveAndLeaveNoHalfInstall(t *testing.T) {
	target := hostTarget(t)

	cases := []struct {
		name  string
		setup func(h *harness, slowGate chan struct{}) (act func() error)
	}{
		{
			name: "下载中途取消",
			setup: func(h *harness, gate chan struct{}) func() error {
				return func() error {
					// 等 staging 里出现半成品，再通过服务 API 取消。
					deadline := time.Now().Add(20 * time.Second)
					for time.Now().Before(deadline) {
						if id := h.svc.ActiveInstallID(); id != "" {
							if hasStaging(h.Root) {
								return h.svc.Cancel(id)
							}
						}
						time.Sleep(20 * time.Millisecond)
					}
					return errTimeout("等不到下载中的安装")
				}
			},
		},
		{
			name: "网络失败",
			setup: func(h *harness, gate chan struct{}) func() error {
				return func() error { return nil } // 由 install 直接触发 500
			},
		},
		{
			name: "哈希不符",
			setup: func(h *harness, gate chan struct{}) func() error {
				return func() error { return nil }
			},
		},
		{
			name: "解压失败",
			setup: func(h *harness, gate chan struct{}) func() error {
				return func() error { return nil }
			},
		},
		{
			name: "probe 失败",
			setup: func(h *harness, gate chan struct{}) func() error {
				return func() error { return nil }
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.addPackage(upgV1, target)
			h.mustInstall(upgV1, target)
			before := activeBytes(t, h.Root)
			prevBefore := previousFingerprint(h)

			// 准备 v2 的失败场景。
			var gate chan struct{}
			switch tc.name {
			case "下载中途取消":
				gate = make(chan struct{})
				body := h.defaultArchive(target)
				asset := "pi-" + upgV2 + "-" + target + ".zip"
				h.mu.Lock()
				h.bodies["/"+asset] = body
				h.slow["/"+asset] = gate
				h.mu.Unlock()
				h.entries = append(h.entries, entryFor(upgV2, target, asset, h.srv.URL+"/"+asset, body))
				h.rebuild()
			case "网络失败":
				entry := h.addPackage(upgV2, target)
				h.mu.Lock()
				h.status["/"+entry.AssetName] = 500
				h.mu.Unlock()
			case "哈希不符":
				entry := h.addPackage(upgV2, target)
				h.setEntry(entry.Version, entry.TargetID, func(e *ManifestEntry) { e.SHA256 = strings.Repeat("a", 64) })
			case "解压失败":
				h.addPackageBody(upgV2, target, []byte("this is not an archive at all"))
			case "probe 失败":
				h.addPackage(upgV2, target)
				h.probe.set(false, "kind:\"contract-probe-failed\" 注入的 probe 失败")
			}

			action := tc.setup(h, gate)
			rec := &stageRecorder{}
			type outcome struct {
				res Result
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				res, err := h.svc.Install(context.Background(), InstallRequest{Version: upgV2, TargetID: target}, rec.progress())
				done <- outcome{res, err}
			}()

			if err := action(); err != nil {
				t.Fatalf("触发失败场景时出错: %v", err)
			}
			var got outcome
			select {
			case got = <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("安装没有在时限内结束")
			}
			if gate != nil {
				close(gate)
			}
			if got.err == nil {
				t.Fatalf("失败场景必须返回错误，实际成功: %+v", got.res)
			}
			if got.res.State != pi.StateFailed {
				t.Errorf("state = %q，期望 %q", got.res.State, pi.StateFailed)
			}

			// active 与旧版本 hash 不变（逐字节比较 active.json + 可执行体哈希）。
			if after := activeBytes(t, h.Root); string(after) != string(before) {
				t.Fatalf("失败后 active 指针被改动:\nbefore=%s\nafter=%s", before, after)
			}
			if prevAfter := previousFingerprint(h); prevAfter != prevBefore {
				t.Fatalf("失败后 previous 版本指纹被改动: before=%s after=%s", prevBefore, prevAfter)
			}
			assertNoStaging(t, h.Root)
			for _, cand := range h.discoverManaged().Candidates {
				if cand.Installation.Version == upgV2 && (cand.Compatibility == pi.StateReadyCompatible || cand.Compatibility == pi.StateReadyVerified) {
					t.Fatalf("半成品 %s 被发现为可用: %+v", upgV2, cand)
				}
			}

			writeInstallObservation(t, h.Root, map[string]any{
				"case":            "PI_INS03",
				"scenario":        tc.name,
				"state":           string(got.res.State),
				"errorCode":       appErrCode(got.err),
				"error":           got.err.Error(),
				"failedStage":     string(got.res.FailedStage),
				"stages":          summarizeStages(rec),
				"activeVersion":   activeVersion(h),
				"activeBytesSame": string(activeBytes(t, h.Root)) == string(before),
				"stagingGone":     !hasStaging(h.Root),
			}, rec, captureExternalSnapshot(t, ""))
		})
	}
}

// ── PI_UPG01 ────────────────────────────────────────────────────────────────

// TestUpgradeKeepsRunningWorkerOnOldPath 是 PI_UPG01：
// 升级原子切换 active；已经跑起来的 worker 仍执行旧路径；新起的进程才用新版本。
func TestUpgradeKeepsRunningWorkerOnOldPath(t *testing.T) {
	target := hostTarget(t)
	h := newHarness(t)
	h.addPackage(upgV1, target)
	h.mustInstall(upgV1, target)

	activeV1 := h.active()
	if activeV1 == nil || activeV1.Version != upgV1 {
		t.Fatalf("v1 安装后 active 不对: %+v", activeV1)
	}
	exeV1, err := ActiveExecutable(h.Root, activeV1)
	if err != nil {
		t.Fatalf("解析 v1 可执行体失败: %v", err)
	}
	workdir := t.TempDir()
	worker := startLongLived(t, exeV1, workdir)
	pid := worker.Process.Pid
	imageBefore := processImagePath(t, pid)
	if !strings.Contains(strings.ToLower(imageBefore), strings.ToLower(upgV1)) {
		t.Fatalf("活跃 worker 的 image path 不含 v1 版本目录: %s", imageBefore)
	}

	// 升级到 v2。
	h.addPackage(upgV2, target)
	res, err, rec := h.install(upgV2, target)
	if err != nil {
		t.Fatalf("升级到 v2 失败: %v（阶段=%v）", err, rec.list())
	}
	if res.State != pi.StateRollbackAvailable {
		t.Errorf("升级成功后 state = %q，期望 %q（有可回滚旧版）", res.State, pi.StateRollbackAvailable)
	}

	activeV2 := h.active()
	if activeV2 == nil || activeV2.Version != upgV2 {
		t.Fatalf("升级后 active 应指向 v2，实际 %+v", activeV2)
	}
	if activeV2.Previous == nil || activeV2.Previous.Version != upgV1 {
		t.Fatalf("升级后必须保留恰好一个已验证旧版（v1），实际 %+v", activeV2.Previous)
	}
	// 原子替换：active 指针目录里不允许残留临时文件。
	if _, err := os.Stat(filepath.Join(h.Root, ActiveFileName+".tmp")); err == nil {
		t.Fatal("active.json.tmp 残留：替换不是原子的")
	}

	// 活跃 worker 仍在跑，且它的 image path 没有变。
	imageAfter := processImagePath(t, pid)
	if imageAfter != imageBefore {
		t.Fatalf("升级后活跃 worker 的 image path 变了：before=%s after=%s", imageBefore, imageAfter)
	}
	if !strings.Contains(strings.ToLower(imageAfter), strings.ToLower(upgV1)) {
		t.Fatalf("活跃 worker 不再执行 v1 路径: %s", imageAfter)
	}

	// 新起的进程用 v2。
	exeV2, err := ActiveExecutable(h.Root, activeV2)
	if err != nil {
		t.Fatalf("解析 v2 可执行体失败: %v", err)
	}
	worker2 := startLongLived(t, exeV2, workdir)
	imageNew := processImagePath(t, worker2.Process.Pid)
	if !strings.Contains(strings.ToLower(imageNew), strings.ToLower(upgV2)) {
		t.Fatalf("新起的进程没有使用 v2: %s", imageNew)
	}
	// F08 发现：托管根里两个版本都在，且旧版仍可用（回滚才有意义）。
	// 当前版本由 Preferred（应用传 active.json 的可执行体）表达：F08 的托管候选
	// 按目录名排序，不传 Preferred 时先命中 1.5.0 目录——那是 F08 的既定语义，
	// F06 不改它，而是保证产品路径永远带上 Preferred。
	disc := h.discoverManagedWith(exeV2)
	if disc.Selected == nil || disc.Selected.Installation.Version != upgV2 {
		t.Fatalf("带 Preferred 的 Discover 选中的不是 v2: %+v", disc.Selected)
	}
	seenV1 := false
	for _, c := range disc.Candidates {
		if c.Installation.Version == upgV1 &&
			(c.Compatibility == pi.StateReadyCompatible || c.Compatibility == pi.StateReadyVerified) {
			seenV1 = true
		}
	}
	if !seenV1 {
		t.Fatalf("升级后旧版必须仍是可用候选（否则无从回滚）: %+v", disc.Candidates)
	}

	writeInstallObservation(t, h.Root, map[string]any{
		"case":              "PI_UPG01",
		"state":             string(res.State),
		"activeVersion":     activeV2.Version,
		"previousVersion":   activeV2.Previous.Version,
		"runningWorkerPid":  pid,
		"workerImageBefore": imageBefore,
		"workerImageAfter":  imageAfter,
		"newProcessImage":   imageNew,
		"stages":            summarizeStages(rec),
	}, rec, captureExternalSnapshot(t, ""))
}

// ── PI_UPG02 ────────────────────────────────────────────────────────────────

// TestFailedUpgradeRollsBackAndRollbackIsIdempotent 是 PI_UPG02：
// probe 失败 → active 不变；切换后自检失败 → 自动回滚；UI 回滚可用且幂等。
func TestFailedUpgradeRollsBackAndRollbackIsIdempotent(t *testing.T) {
	target := hostTarget(t)

	t.Run("probe 失败时 active 不变", func(t *testing.T) {
		h := newHarness(t)
		h.addPackage(upgV0, target)
		h.mustInstall(upgV0, target)
		h.addPackage(upgV1, target)
		h.mustInstall(upgV1, target)
		before := activeBytes(t, h.Root)

		h.addPackage(upgV2, target)
		h.probe.set(false, "kind:\"contract-probe-failed\" 注入的 probe 失败")
		res, err, rec := h.install(upgV2, target)
		if err == nil {
			t.Fatal("probe 失败必须让升级失败")
		}
		if res.State != pi.StateFailed || res.FailedStage != StageProbe {
			t.Fatalf("失败结果 = %+v，期望 state=failed failedStage=probe", res)
		}
		if after := activeBytes(t, h.Root); string(after) != string(before) {
			t.Fatalf("probe 失败后 active 被改动:\nbefore=%s\nafter=%s", before, after)
		}
		if p := h.active(); p == nil || p.Version != upgV1 || p.Previous == nil || p.Previous.Version != upgV0 {
			t.Fatalf("probe 失败后应保留 v1 + 一个旧版 v0，实际 %+v", p)
		}
		writeInstallObservation(t, h.Root, map[string]any{
			"case": "PI_UPG02", "scenario": "probe 失败时 active 不变",
			"state": string(res.State), "errorCode": appErrCode(err), "error": err.Error(),
			"failedStage": string(res.FailedStage), "stages": summarizeStages(rec),
			"activeVersion": activeVersion(h), "rollbackAvailable": res.RollbackAvailable,
		}, rec, captureExternalSnapshot(t, ""))
	})

	t.Run("切换后自检失败自动回滚且回滚幂等", func(t *testing.T) {
		h := newHarness(t)
		h.addPackage(upgV0, target)
		h.mustInstall(upgV0, target)
		h.addPackage(upgV1, target)
		h.mustInstall(upgV1, target)

		// v2 切换后自检：版本探针报错版本，Discover 判定不可用 → 自动回滚。
		h.versionP = func(ctx context.Context, inst pi.PiInstallation) (string, []string) {
			v, diags := managedVersionProbe(ctx, inst)
			if v == upgV2 {
				return "9.9.9", nil
			}
			return v, diags
		}
		h.rebuild()
		h.addPackage(upgV2, target)
		res, installErr, rec := h.install(upgV2, target)
		if installErr == nil {
			t.Fatal("切换后自检失败必须让升级失败")
		}
		if !res.AutoRolledBack {
			t.Fatalf("必须标记自动回滚: %+v", res)
		}
		p := h.active()
		if p == nil || p.Version != upgV1 {
			t.Fatalf("自动回滚后 active 应回到 v1，实际 %+v", p)
		}
		if p.Previous == nil || p.Previous.Version != upgV0 {
			t.Fatalf("自动回滚后必须保留恰好一个旧版 v0，实际 %+v", p.Previous)
		}
		if res.State != pi.StateRollbackAvailable || !res.RollbackAvailable {
			t.Fatalf("自动回滚后应报告 rollback_available: %+v", res)
		}

		// UI 回滚：回到 v0。
		rb, err := h.svc.Rollback(context.Background(), p.InstallationID)
		if err != nil {
			t.Fatalf("回滚失败: %v", err)
		}
		if rb.Version != upgV0 {
			t.Fatalf("回滚后版本 = %s，期望 %s", rb.Version, upgV0)
		}
		afterRollback := h.active()
		if afterRollback == nil || afterRollback.Version != upgV0 {
			t.Fatalf("回滚后 active 应指向 v0，实际 %+v", afterRollback)
		}
		if afterRollback.Previous != nil {
			t.Fatalf("回滚后不应再有 previous（避免来回翻转），实际 %+v", afterRollback.Previous)
		}
		// 幂等：第二次回滚不报错、不改变状态。
		rb2, err := h.svc.Rollback(context.Background(), afterRollback.InstallationID)
		if err != nil {
			t.Fatalf("重复回滚必须幂等（不报错）: %v", err)
		}
		if rb2.Version != upgV0 || rb2.RollbackAvailable {
			t.Fatalf("重复回滚改变了状态: %+v", rb2)
		}
		if p2 := h.active(); p2 == nil || p2.Version != upgV0 || p2.Previous != nil {
			t.Fatalf("重复回滚改动了 active: %+v", p2)
		}
		// 回滚到一个不存在的 installationID：结构化 NotFound，不静默成功。
		if _, err := h.svc.Rollback(context.Background(), "pi-does-not-exist"); err == nil {
			t.Fatal("未知 installationID 的回滚必须报错")
		} else if code := appErrCode(err); code != contracts.CodeNotFound {
			t.Fatalf("未知 installationID 的错误码 = %q，期望 %q", code, contracts.CodeNotFound)
		}

		writeInstallObservation(t, h.Root, map[string]any{
			"case": "PI_UPG02", "scenario": "切换后自检失败自动回滚且回滚幂等",
			"state": string(res.State), "errorCode": appErrCode(installErr), "error": installErr.Error(),
			"failedStage": string(res.FailedStage), "stages": summarizeStages(rec),
			"autoRolledBack": res.AutoRolledBack, "rollbackAvailable": res.RollbackAvailable,
			"activeVersion": activeVersion(h),
		}, rec, captureExternalSnapshot(t, ""))
	})
}

// ── PI_UPG03 ────────────────────────────────────────────────────────────────

// TestUpgradeWithExternalPiNeverTouchesIt 是 PI_UPG03：
// 外部 Pi 从不被覆盖/卸载；升级创建托管版本；回滚不改变外部 hash；PATH 不变。
func TestUpgradeWithExternalPiNeverTouchesIt(t *testing.T) {
	target := hostTarget(t)
	external, externalHash := writeExternalPi(t)
	externalBefore, err := snapshotRoot(filepath.Dir(external))
	if err != nil {
		t.Fatalf("快照外部 Pi 目录失败: %v", err)
	}
	pathBefore := os.Getenv("PATH")

	h := newHarness(t)
	h.addPackage(upgV1, target)
	first := h.mustInstall(upgV1, target)
	h.addPackage(upgV2, target)
	upgraded := h.mustInstall(upgV2, target)

	// 托管安装只写在托管根里。
	if !strings.HasPrefix(first.Executable, h.Root) || !strings.HasPrefix(upgraded.Executable, h.Root) {
		t.Fatalf("托管安装必须落在托管根内: %s / %s（root=%s）", first.Executable, upgraded.Executable, h.Root)
	}
	if strings.HasPrefix(first.Executable, filepath.Dir(external)) {
		t.Fatalf("托管安装写进了外部 Pi 目录: %s", first.Executable)
	}
	// 外部 Pi 哈希与目录快照不变。
	if sum, err := HashFileContent(external); err != nil || sum != externalHash {
		t.Fatalf("外部 Pi 被改写: hash=%s err=%v", sum, err)
	}
	externalAfter, err := snapshotRoot(filepath.Dir(external))
	if err != nil {
		t.Fatalf("快照外部 Pi 目录失败: %v", err)
	}
	if externalAfter.SnapshotHash != externalBefore.SnapshotHash {
		t.Fatalf("外部 Pi 目录在升级期间发生变化: before=%s after=%s",
			externalBefore.SnapshotHash, externalAfter.SnapshotHash)
	}
	// PATH 不变。
	if after := os.Getenv("PATH"); after != pathBefore {
		t.Fatal("安装/升级改变了 PATH")
	}

	// 回滚同样不碰外部 Pi。
	p := h.active()
	if p == nil || p.Version != upgV2 || p.Previous == nil {
		t.Fatalf("升级后 active 形态不对: %+v", p)
	}
	if _, err := h.svc.Rollback(context.Background(), p.InstallationID); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if sum, err := HashFileContent(external); err != nil || sum != externalHash {
		t.Fatalf("回滚改写了外部 Pi: hash=%s err=%v", sum, err)
	}
	externalAfterRollback, err := snapshotRoot(filepath.Dir(external))
	if err != nil {
		t.Fatalf("快照外部 Pi 目录失败: %v", err)
	}
	if externalAfterRollback.SnapshotHash != externalBefore.SnapshotHash {
		t.Fatal("回滚改变了外部 Pi 目录")
	}
	if after := os.Getenv("PATH"); after != pathBefore {
		t.Fatal("回滚改变了 PATH")
	}

	writeInstallObservation(t, h.Root, map[string]any{
		"case":                 "PI_UPG03",
		"externalPath":         external,
		"externalHash":         externalHash,
		"externalTreeBefore":   externalBefore.SnapshotHash,
		"externalTreeAfter":    externalAfterRollback.SnapshotHash,
		"managedExecutable":    upgraded.Executable,
		"activeVersion":        activeVersion(h),
		"pathUnchanged":        os.Getenv("PATH") == pathBefore,
		"managedInsideRoot":    strings.HasPrefix(upgraded.Executable, h.Root),
		"externalNeverTouched": true,
	}, &stageRecorder{}, captureExternalSnapshot(t, external))
}

// ── 小工具 ──────────────────────────────────────────────────────────────────

type errTimeout string

func (e errTimeout) Error() string { return string(e) }

func activeBytes(t *testing.T, root string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ActiveFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return []byte{}
		}
		t.Fatalf("读 active.json 失败: %v", err)
	}
	return raw
}

func activeVersion(h *harness) string {
	p, err := ReadActive(h.Root)
	if err != nil || p == nil {
		return ""
	}
	return p.Version
}

// previousFingerprint 是 previous 版本目录的磁盘指纹（不存在记为 none）。
func previousFingerprint(h *harness) string {
	p, err := ReadActive(h.Root)
	if err != nil || p == nil || p.Previous == nil {
		return "none"
	}
	dir := filepath.Join(h.Root, filepath.FromSlash(p.Previous.VersionDir))
	snap, err := snapshotRoot(dir)
	if err != nil {
		return "error: " + err.Error()
	}
	return p.Previous.Version + "/" + p.Previous.ExecutableSHA256 + "/" + snap.SnapshotHash
}

func hasStaging(root string) bool {
	entries, err := os.ReadDir(filepath.Join(root, stagingDirName))
	if err != nil {
		return false
	}
	return len(entries) > 0
}

func assertNoStaging(t *testing.T, root string) {
	t.Helper()
	if hasStaging(root) {
		entries, _ := os.ReadDir(filepath.Join(root, stagingDirName))
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("失败路径留下了 staging 半成品: %v", names)
	}
}

var _ = sync.Mutex{}
