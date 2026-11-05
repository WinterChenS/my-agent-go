# Pi Lifecycle and Anvil Updater Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Anvil 自动发现并兼容探测本机 Pi，支持一键安装/升级/回滚，并安全地后台下载 Anvil 更新、在下次启动安装和失败回滚。

**Architecture:** `PiLifecycleService` 在现有 locator/probe 之上管理候选与托管版本，Pi 仍拥有 agent loop；`UpdateService` 只下载和验证，平台 updater helper 在主进程退出后的下一次启动前交换版本。两条生命周期各自事务化，不互相修改 active 指针。

**Tech Stack:** Go 1.27.3、Pi RPC、平台签名工具、HTTPS fixture server、Ed25519 签名、Wails v3 应用 API。

**Spec:** `docs/superpowers/specs/2026-11-05-anvil-v1-product-ui-lifecycle-design.md`。

## Global Constraints

- Pi 初始最低版本 1.5.0，默认候选范围 `>=1.5.0 <2.0.0`；范围内仍必须通过契约探测。
- 基础包和更新包不得捆绑 Pi、Node、npm、模型权重或独立 Chromium。
- 外部 Pi 永不被覆盖、删除或修改 PATH；托管 Pi 只写 Anvil 专用目录。
- Anvil 更新自动检查和下载，但不主动重启；只在用户下次正常启动时应用。
- 清单与包分别验签，信任根编译进当前应用；远端内容不能替换信任根。
- `internal/contracts`、`schemas`、`cmd/devtools` 与 case 索引由单一写者串行修改。
- 本计划的 case 在索引中声明 `oracleKind=external`。新增 runner 必须写 `kind=external-observation` 的 `oracle.json`：`source` + 结构化 `observation` + 目录内非空 artifact（active 指针快照、候选 hash、进程版本输出至少其一）。用 `test-process` 自证会让 G0/G2 判死。
- **本机 Pi 安全（来自 2026-11-04 用户指令，ADR 0002）**：F06 在开发机上只允许写临时托管根和本地下载 fixture；不得读写、移动或替换 `.tools/pi/`、`~/.pi`、真实 Anvil/MyAgent 数据目录和 PATH。每个 F06 测试在开始和结束时记录这些路径的 hash，不一致即判 FAIL。真实官方包安装只在干净目标机或虚拟机执行，不在当前开发机执行。
- **顺序**：F06 放在 G2 阶段后端功能（C08→C09→F09）之后、G2 收口之前。它不能排在全部 UI 之后：已批准规格把 PI_INS/PI_UPG 放在 G2，而 U02 经 C06 依赖 G2，排在 UI 后会形成 F06→U02→C06→G2→F06 环。若要严格按 2026-11-04 指令排到最后，需要用户决定把 PI_INS/PI_UPG 改回 G4。F09、C08、C09 不依赖 F06。

---

### Task F08: Pi Discovery and Compatibility Negotiation

**Files:**
- Modify: `internal/runtime/pi/locator.go`
- Modify: `internal/runtime/pi/probe.go`
- Modify: `internal/runtime/pi/compatibility.go`
- Create: `internal/runtime/pi/lifecycle.go`
- Create: `internal/runtime/pi/lifecycle_test.go`
- Modify: `internal/app/desktop.go`
- Create: `internal/app/pi_lifecycle_api.go`
- Create: `internal/app/pi_lifecycle_api_test.go`
- Modify: `tests/acceptance/cases.json`

**Interfaces:**
- Consumes: existing `PiInstallation`, real Pi probe fixtures, isolated `AgentDir` and `WorkerSpec`.
- Produces: `Discover(ctx, DiscoveryRequest) (DiscoveryResult, error)`, `Select(ctx, installationID string) error`, and `CompatibilityState` values `discovering`, `ready_verified`, `ready_compatible`, `upgrade_available`, `incompatible`, `missing`, `failed`.

- [ ] **Step 1: Write failing discovery-order and wrong-PATH tests**

```go
func TestDiscoverReturnsAllCandidatesAndSelectsDeterministically(t *testing.T) {
    got := discoverFixture(t, preferredPi, savedPi, managedPi, pathPi)
    assertCandidateOrigins(t, got.Candidates, "preferred", "saved", "managed", "path")
    if got.Selected.ID != preferredPi.ID { t.Fatalf("selected=%s", got.Selected.ID) }
}

func TestDiscoverRejectsWechatNodeShim(t *testing.T) {
    got := discoverWithPath(t, `C:\wechat-devtools;C:\Program Files\nodejs`)
    if got.Selected.Origin == "wechat-devtools" { t.Fatal("selected unmanaged shim") }
}
```

- [ ] **Step 2: Run focused tests and record the expected failure**

Run: `go test ./internal/runtime/pi -run 'TestDiscoverReturnsAllCandidates|TestDiscoverRejectsWechatNodeShim' -count=1 -v`

Expected: FAIL because `DiscoveryResult` and deterministic multi-candidate discovery do not exist.

- [ ] **Step 3: Implement candidate discovery without shell interpolation**

```go
type DiscoveryResult struct {
    State      CompatibilityState
    Selected   *PiInstallation
    Candidates []PiCandidate
    Diagnostics []Diagnostic
}

type PiCandidate struct {
    Installation PiInstallation
    Origin       string
    ExecutableHash string
    Compatibility CompatibilityState
}
```

Resolve executable paths directly, retain all valid candidates, hash the executable, and never execute a user-provided shell string.

- [ ] **Step 4: Write failing compatibility-range and capability-probe tests**

```go
func TestVersionInRangeStillRequiresContractProbe(t *testing.T) {
    result := probeSyntheticPi(t, "1.6.2", brokenSettledEventFixture)
    if result.State != Incompatible { t.Fatalf("state=%s", result.State) }
}

func TestUnknownPatchBecomesCompatibleOnlyAfterProbe(t *testing.T) {
    result := probeSyntheticPi(t, "1.6.2", compatibleFixture)
    if result.State != ReadyCompatible { t.Fatalf("state=%s", result.State) }
}
```

The probe must cover RPC handshake, final settled semantics, MCP, extension hook, abort, and process cleanup in an isolated agent directory.

- [ ] **Step 5: Implement compatibility negotiation and app bindings**

```go
type CompatibilityReport struct {
    Version string
    State CompatibilityState
    Capabilities CapabilitySet
    FailedChecks []string
    FixtureHash string
}
```

Treat 1.5.0 as pre-verified only after its existing fixture hash matches. Treat `>=1.5.0 <2.0.0` as probe candidates, not automatic success. Reject 2.x until a later signed compatibility manifest and contract suite are shipped.

- [ ] **Step 6: Run focused and regression verification**

Run: `go test ./internal/runtime/pi ./internal/app -run 'TestDiscover|TestVersionInRange|TestUnknownPatch|TestPiLifecycle' -count=1 -v`

Run: `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64`

Expected: focused tests PASS; integration reports the real candidate path, version, hash, origin, and probe state.

- [ ] **Step 7: Commit F08 independently**

```bash
git add internal/runtime/pi internal/app/pi_lifecycle_api.go internal/app/pi_lifecycle_api_test.go internal/app/desktop.go tests/acceptance/cases.json
git commit -m "F08: add Pi discovery and compatibility negotiation"
```

---

### Task F06: Managed Pi Install, Upgrade, and Rollback

**Dependencies:** F08、F09、F03、F07。F09 先完成共享启动/desktop 装配；F06 排在 G2 阶段后端功能之后。开发不依赖 G0 过闸；PI_INS/PI_UPG 的 PASS 仍需 G0→G1→G2 顺序。

**Files:**
- Create: `internal/piinstall/manifest.go`
- Create: `internal/piinstall/download.go`
- Create: `internal/piinstall/extract.go`
- Create: `internal/piinstall/activate.go`
- Create: `internal/piinstall/service.go`
- Create: `internal/piinstall/service_test.go`
- Create: `internal/piinstall/install_integration_test.go`
- Create: `internal/app/pi_install_api.go`
- Create: `internal/app/pi_install_api_test.go`
- Create: `build/pi-manifest.json`
- Modify: `internal/app/desktop.go`

**Interfaces:**
- Consumes: F08 compatibility probe, target ID, Anvil managed data root, signed Pi manifest.
- Produces: `Install(ctx, InstallRequest) (InstallID, error)`, `Cancel(ctx, InstallID) error`, `Rollback(ctx, installationID string) error`, and progress stages `download`, `verify`, `extract`, `probe`, `activate`.

- [ ] **Step 1: Write failing manifest, hash, redirect, and archive-boundary tests**

```go
func TestHashMismatchNeverActivates(t *testing.T) {
    old := seedActivePi(t)
    err := installFixture(t, manifestWithWrongHash())
    assertCode(t, err, contracts.CodeVerificationFailed)
    assertActiveHash(t, old.Hash)
}

func TestArchiveTraversalRejected(t *testing.T) {
    for _, name := range []string{"../escape", "/absolute", "link-outside"} {
        t.Run(name, func(t *testing.T) { assertArchiveRejected(t, name) })
    }
}
```

- [ ] **Step 2: Run tests and confirm they fail before implementation**

Run: `go test ./internal/piinstall -run 'TestHashMismatch|TestArchiveTraversal|TestUntrustedRedirect' -count=1 -v`

Expected: FAIL because the package and activation boundary do not exist.

- [ ] **Step 3: Implement signed manifest and staging download**

```go
type ManifestEntry struct {
    Version string `json:"version"`
    TargetID string `json:"targetId"`
    URL string `json:"url"`
    SizeBytes int64 `json:"sizeBytes"`
    SHA256 string `json:"sha256"`
    Signature string `json:"signature"`
}
```

Allow only official HTTPS hosts, revalidate redirects, stream to a staging file, verify length/hash/signature, then extract with path and link checks.

- [ ] **Step 4: Write cancellation, active-worker, and rollback tests**

```go
func TestInstallCancellationPreservesActive(t *testing.T) { /* cancel mid-download; old hash remains */ }
func TestActiveWorkerKeepsOriginalExecutable(t *testing.T) { /* upgrade active pointer; worker path unchanged */ }
func TestProbeFailureRollsBack(t *testing.T) { /* candidate probe fails; old pointer restored */ }
```

- [ ] **Step 5: Implement atomic activation and one-version rollback**

Use a version directory plus an atomically replaced active pointer. Probe the staged executable before activation. Keep exactly one previous managed version after successful activation. Refuse uninstall or mutation when `Origin=external`.

- [ ] **Step 6: Add Wails API and real-package integration**

The UI request contains only version and target; it cannot provide arbitrary URLs. Run the official independent Pi package in an environment without Node, then execute the F08 contract probe.

Run: `go test ./internal/piinstall ./internal/app -run 'TestInstall|TestRollback|TestPiInstall' -count=1 -v`

Expected: PASS with fixture counters and active-pointer hashes in evidence.

- [ ] **Step 7: Run target integration and commit F06**

Run: `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=integration --target=win-x64`

```bash
git add internal/piinstall internal/app/pi_install_api.go internal/app/pi_install_api_test.go internal/app/desktop.go build/pi-manifest.json
git commit -m "F06: add managed Pi install upgrade and rollback"
```

---

### Task F09: Signed Background Application Update Service

**Dependencies:** C09、U00、F01、F07。U00 先完成 Anvil 启动入口与品牌迁移边界，C09 之后 F09 在同一入口加入 pending update；F06 再在 F09 之后接入。这些任务不得并行修改 `cmd/myagent/main.go` 或 `internal/app/desktop.go`。

**Files:**
- Create: `internal/update/manifest.go`
- Create: `internal/update/download.go`
- Create: `internal/update/service.go`
- Create: `internal/update/service_test.go`
- Create: `internal/update/fixture_test.go`
- Create: `internal/app/update_api.go`
- Create: `internal/app/update_api_test.go`
- Create: `cmd/anvil-updater/main.go`
- Create: `internal/updater/transaction.go`
- Create: `internal/updater/transaction_test.go`
- Modify: `cmd/myagent/main.go`
- Modify: `internal/app/desktop.go`
- Create: `build/update-public-key.pem`

**Interfaces:**
- Consumes: current version, channel, target ID, signed update manifest, platform updater adapter.
- Produces: `Check(ctx) (UpdateStatus, error)`, `Download(ctx, version string) error`, `ApplyPending(ctx) error`, and states `idle`, `checking`, `update_available`, `downloading`, `verifying`, `ready`, `applying`, `health_checking`, `completed`, `rollback`, `failed`, `blocked`.

- [ ] **Step 1: Write failing selection and signature tests**

```go
func TestSelectsOnlyMatchingChannelTargetAndNewerVersion(t *testing.T) { /* stable + win-x64 only */ }
func TestManifestOrPackageSignatureFailureNeverReady(t *testing.T) { /* both signature layers */ }
func TestDowngradeAndUnknownSchemaRejected(t *testing.T) { /* no ordinary downgrade */ }
```

- [ ] **Step 2: Run tests and confirm expected failure**

Run: `go test ./internal/update -run 'TestSelectsOnly|TestManifestOrPackage|TestDowngrade' -count=1 -v`

Expected: FAIL because the update service does not exist.

- [ ] **Step 3: Implement check, resumable download, and verification**

```go
type UpdateManifest struct {
    SchemaVersion int `json:"schemaVersion"`
    Channel string `json:"channel"`
    Version string `json:"version"`
    TargetID string `json:"targetId"`
    MinUpgradeVersion string `json:"minUpgradeVersion"`
    URL string `json:"url"`
    SizeBytes int64 `json:"sizeBytes"`
    SHA256 string `json:"sha256"`
    PackageSignature string `json:"packageSignature"`
    DatabaseSchema int `json:"databaseSchema"`
    PiRange string `json:"piRange"`
}
```

Persist download metadata and ETag. Resume only when the server validator still matches; otherwise discard partial bytes. Verification success writes a pending transaction, but never restarts the app.

- [ ] **Step 4: Write failing next-launch, crash, and data-rollback tests**

```go
func TestReadyUpdateDoesNotRestartActiveApp(t *testing.T) { /* process PID unchanged */ }
func TestNextLaunchSwapsRealVersion(t *testing.T) { /* old --version then new --version */ }
func TestFailedHealthCheckRestoresBinaryAndDatabase(t *testing.T) { /* hash + DB backup */ }
func TestInterruptedSwapLeavesOneCompleteVersion(t *testing.T) { /* crash each transaction phase */ }
```

- [ ] **Step 5: Implement updater transaction and platform adapters**

The helper reads a pre-verified pending transaction, backs up the application and database, atomically switches files, launches the candidate with a health-check nonce, and commits only after acknowledgement. Windows swaps the signed installation directory, macOS verifies Team ID/bundle ID, AppImage swaps the active file, and `.deb` enters the OS package authorization flow or returns `blocked`.

- [ ] **Step 6: Wire startup and Wails API without forced restart**

`cmd/myagent/main.go` checks for a pending transaction before normal app startup. The app API exposes status, check, download, retry, and “exit and update”; it refuses the latter while runs, approvals, Pi installs, browser takeover, unsaved edits, or cleanup failures are active.

- [ ] **Step 7: Verify real old-to-new process transition**

Run: `go test ./internal/update ./internal/updater ./internal/app -run 'TestUpdate|TestNextLaunch|TestFailedHealth|TestInterruptedSwap' -count=1 -v`

Run the package fixture with two distinct binaries whose `--version` output and SHA-256 differ. Evidence must contain both process versions and hashes.

- [ ] **Step 8: Commit F09 independently**

```bash
git add internal/update internal/updater internal/app/update_api.go internal/app/update_api_test.go internal/app/desktop.go cmd/anvil-updater cmd/myagent/main.go build/update-public-key.pem
git commit -m "F09: add signed staged Anvil updates"
```

---

## Plan Verification

After F08, F06, and F09:

1. Run `go test ./internal/runtime/pi ./internal/piinstall ./internal/update ./internal/updater ./internal/app -count=1`.
2. Run `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static`.
3. Run `powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=unit`.
4. Run G0 cases `PI_DISC01–02` and `PI_COMP01–04` on all six targets.
5. Run G2 cases `PI_INS01–03` and `PI_UPG01–03` on all six targets.
6. Leave G4 `PI_PKG01–02` and `UPD01–14` BLOCKED until real old/new packages exist on each target; do not convert fixture success into package success.
