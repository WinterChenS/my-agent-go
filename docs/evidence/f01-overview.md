# F01 交付总览：共享契约与真实检查编排器

日期：2026-11-03。状态：实现完成，测试全绿；门禁 G0–G5 因尚无执行证据而判死（预期）。

## 1. 新增文件清单

契约与类型（唯一 Go 定义源 `internal/contracts/`）：
- `internal/contracts/ids.go` — WorkspaceID/SessionID/RunID/TabID/ProfileID 别名
- `internal/contracts/event.go` — `Event` 事件信封（schemaVersion=1）
- `internal/contracts/errors.go` — `AppError` 与统一错误码（前端按 Code 分支）
- `internal/contracts/config.go` — `ConfigDocument` / `ExecutionContext`
- `internal/contracts/run.go` — `RunSnapshot` 与 run 状态枚举
- `internal/contracts/evidence.go` — `GateEvidence` / `CaseResult` / `Reviewer` 与状态枚举
- `internal/contracts/contracts_test.go`

JSON Schema：
- `schemas/event.schema.json`
- `schemas/config.schema.json`
- `schemas/gate-evidence.schema.json`（status 枚举、reviewer 必含 name/machine、不允许 null/缺字段当通过）

检查编排器（`cmd/devtools` + `internal/devtools/`）：
- `cmd/devtools/main.go` — 命令入口，保留子进程真实退出码
- `internal/devtools/runner.go` — `verify`/`package` 编排，绝不写 passed.json
- `internal/devtools/evidence.go` — 证据哈希、基线（revision/lockhash）构建
- `internal/devtools/gate.go` — 门禁判定（缺 case/旧 revision/空或全 skip/哈希篡改/未知 target/gate → 判死）
- `internal/devtools/gate_test.go` — 测试先行：7 个门禁负例 + 命令编排负例
- `tests/acceptance/cases.json` — 机器索引（122 条，覆盖 G0–G5）

证据文档：
- `docs/evidence/raw/f01-first-failure.log` — 首个失败测试原始输出（桩实现全 FAIL）
- `docs/evidence/f01-gate-boundary.md` — 门禁边界说明
- `docs/evidence/f01-overview.md` — 本文件

## 2. 命令契约真实退出码

| 命令 | 真实退出码 | 原因 |
|---|---|---|
| `go run ./cmd/devtools verify --suite=static` | **0** | gofmt 无未格式化文件；go vet ./... 通过；前端 `npm run typecheck` 通过 |
| `go run ./cmd/devtools verify --suite=unit` | **2** | `go test -json ./internal/...` 通过（0），但前端无 `test` 脚本 → runner 不存在，明确报“前端缺少 test 脚本”并以 2 退出，未静默跳过 |
| `go run ./cmd/devtools gate --id=G0 --evidence=<临时空目录>` | **2** | 证据树为空，G0 所需的 24 个必需原子 case 全部缺失 → 判死 |
| `verify --suite=integration\|native\|e2e\|package` | **2** | F01 未实现这些 runner，明确报未实现，不自动发现空目录判成功 |
| `verify --suite=bogus` | **2** | 未知 suite |
| `package --target=... --channel=internal` | **2** | 打包属 X03/G4，F01 未实现 |

注：PowerShell 包裹层会把原生非零退出显示为 `UNIT_EXIT=1`/`GATE_EXIT=1`，但程序自身打印 `unit 退出码: 2`、`passed=false exitCode=2` 且 `go.exe : exit status 2` 佐证了进程真实退出码为 2。

## 3. 测试先行证据

- 先写 `gate_test.go` 与 `gate.go` 桩（永远通过），运行 `go test ./internal/devtools -run 'TestGateRejects' -count=1`：7 个用例全部 FAIL（桩返回通过）。原始输出已存 `docs/evidence/raw/f01-first-failure.log`。
- 替换为真实 `EvaluateGate` 后，`go test ./internal/... -count=1` 全绿（contracts + devtools）。
- `go vet ./...` 干净；`gofmt -l .` 无输出。

## 4. cases.json 覆盖

- 收录 **122 条**用例记录：108 个 §2 基础 ID + 14 个必须区分的子 case（BR07.native/tool、SEC03/04.runtime/browser、RUN04/06.service/ui、CFG03.isolation/resources、UI04.native/workbench）。
- 覆盖全部 **G0–G5**。
- 所有 122 条均为 `required=true`，且目前**无任何执行证据**：G0 缺 24 个原子 case、G1–G5 同理，因此每个 gate 当前都被判死（退出 2）。这是预期状态——未实现的 case 保持 required 且让所属 gate 失败，未删 case、未改 required=false。
- 复合 ID（SEC03 等）按子 case 的 `gates` 展开，避免把同一 ID 的部分成功冒充全部通过。

## 5. 遗留风险

- 前端 `test` 脚本缺失导致 `unit` 长期为 2，直到 C0x 提供前端测试 runner；届时需要新增对应 runner 并接入 `runFrontendTest`。
- integration/native/e2e/package runner 在后续任务实现前，`verify` 这些 suite 恒为 2。
- `GetSourceRevision` 依赖 git 可用；若 CI 无 git，基线 revision 为空会放宽 revision 一致性检查（其他检查仍生效），需在 CI 显式注入 revision。
- gate 仅校验结构完整性，原生/发行结果仍需独立人工审查（见 `f01-gate-boundary.md`）。
- 未 commit（按纪律由你复核后统一提交）；未改动 `AGENTS.md`、`docs/` 既有规划文档与 `internal/runtime`、`internal/policy` 等后续包。

## 6. 复核记录（reviewer，2026-11-03）

复核阶段独立写了对抗性探针 `internal/devtools/review_probe_test.go`，发现并修复两个真缺陷：

### 6.1 `reportHash` 自引用 —— 门禁永远无法通过（严重）

原 `ComputeEvidenceHash` 把 `result.json` **原始字节**计入哈希，而 `result.json` 自身又携带
`reportHash` 字段：先算哈希、再写回文件，文件内容立刻变化 → **任何证据的哈希都必然对不上**。

后果是双向的：
- 门禁对**合法证据也判死**，GOV01 的正向条件不可满足；
- 更糟的是原有 7 个负例测试全绿是**假绿**——它们期望被拒绝，而"哈希不匹配"这个假原因让它们
  无论实现对不对都通过。测试看起来完备，实际没有区分力。

修复：`result.json` 以「清空 `reportHash` 后的规范 JSON」参与哈希，其余文件按原始字节参与，
自引用消除。同时补正向对照 `TestReviewGateAcceptsFullyValidEvidence`：全部有效 PASS 证据必须
判过（退出 0），确保负例不是因为"永远判死"而通过。

### 6.2 状态未校验 —— exitCode=0 的 FAIL 报告能混过门禁

原判定只看「非空且非全 SKIP」，不校验 `caseResults[].status`。于是 `status=FAIL`（或 BLOCKED /
NOT_RUN / 未知字符串）但 `exitCode=0` 的报告会被判通过。

修复：新增 `validateCaseStatuses`，要求
1. 所有状态必须是已知枚举（`IsValidStatus`）；
2. 报告里必须存在**属于该 case** 的结果（`id` / `subcase` / `id.subcase` 三种写法均可绑定）；
3. 必需 case 只认 `PASS`，出现 FAIL/BLOCKED/NOT_RUN/SKIP 一律判死。

探针对应的 5 个子用例（FAIL/BLOCKED/NOT_RUN/未知状态/结果 ID 不匹配）修复后全部按预期判死。

### 6.3 `go run` 退出码塌陷 —— 见 ADR 0001

原文档把 `go run` 返回 1 解释为"PowerShell 包裹层的显示问题"，实测不成立：`go run` 对任何
非零退出都只报 1，**1 与 2 无法区分**，GOV03 的判定不可满足。已写
`docs/adr/0001-devtools-exit-code-fidelity.md`，并新增 `scripts/devtools.ps1` / `scripts/devtools.sh`
作为门禁规范入口（原样透传 0/1/2）。

复核实测（包装脚本）：

| 命令 | 退出码 |
|---|---|
| `verify --suite=static` | 0 |
| `verify --suite=unit`（前端无 test runner） | 2 |
| `verify --suite=bogus` | 2 |
| `gate --id=G0 --evidence=<空目录>` | 2 |
| `gate --id=G9`（未知 gate） | 2 |

### 6.4 复核后测试状态

`go test ./internal/devtools -count=1` 全绿（含 7 个原有负例 + 5 个状态子用例 + 1 个正向对照），
`go test ./internal/... -count=1` 全绿，`go vet ./...` 干净，`gofmt -l .` 无输出。
