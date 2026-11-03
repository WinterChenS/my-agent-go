# F07：配置 CAS、SQLite 与凭证存储（实现证据）

- 代码：`internal/config/{store,merge,compile}.go`、`internal/storage/{db,migrate}.go`、
  `internal/storage/migrations/001_initial.sql`、`internal/credentials/{store,native_windows,native_other}.go`
- 契约：`internal/contracts/config.go` 新增 `EffectiveConfig`（唯一类型源）
- 测试：`internal/config/config_test.go`、`internal/storage/migration_test.go`、`internal/credentials/credentials_test.go`

## 状态

| Case | 断言要点 | 状态 | 依据 |
|---|---|---|---|
| CFG01 | 五层合并/数组按ID/显式禁用/来源可查询/项目不能扩权限或换凭证 | PASS（单测） | `TestMergePrecedenceAndDisableByID`、`TestPermissionsCannotBeWidenedByProjectLayer`、`TestCredentialRefCannotBeReplacedByProjectLayer` |
| CFG02 | 同 revision 只有一个成功；外部编辑 ConfigConflict；失败不覆盖源文件 | PASS（单测） | `TestCASRejectsExternalEdit` |
| CFG03 | 旧 run 与新 run 的 revision 可见 | PARTIAL | 编译结果携带 revision/hash/provenance；`CFG03.isolation` 属 F04，未做 |
| CFG04 | 原子写中断后仍可恢复；独立 agentDir 不改原 Pi 目录 | PASS（单测） | `TestLoadRecoversFromInterruptedWrite`、`TestCompileIsolatesAgentDir` |
| DB01 | 初建/迁移重跑/事务失败无半迁移 | PASS（单测） | `TestMigrateCreatesSchemaAndIsIdempotent`、`TestMigrateFailureLeavesNoHalfState` |
| DB02 | 锁定驱动真实支持 FTS5 trigram；锁/权限错误可诊断；不偷换内存库 | PASS（单测） | `TestFTS5TrigramRealSupport`、`TestOpenRejectsMemoryDB` |
| DB03 | JSON/DB/transcript 职责不冲突；重建索引不改其它权威数据 | PASS（单测） | `TestRebuildIndexDoesNotTouchConfigOrTranscript` |
| SEC07 | OS 库真实读写；失效不落明文；sentinel 命中 0 | PARTIAL / BLOCKED | Windows DPAPI 分支 PASS（`TestNativeStoreRoundTripAndNoPlaintext`）；macOS Keychain 与 Linux Secret Service **未实现**，`NewNativeStore` 返回 `CREDENTIAL_STORE_UNAVAILABLE` |

"PASS（单测）"只代表该断言在测试层面成立；G2 门禁还需要六目标的真实 target 证据，本轮没有声称已过 G2。

## 关键设计

1. **CAS 用 checksum 信封识别外部编辑**：磁盘格式为 `{schemaVersion,revision,body,checksum}`，
   checksum 绑定 revision + 规范化 body。外部改了 body 却没重算 checksum 时，`Load` 与
   `CompareAndSwap` 都返回 `CONFIG_CONFLICT`，源文件不被覆盖。不引入额外 sidecar 文件，
   也不靠"进程内记住曾读到什么"（跨进程无效）。
2. **权限只能收紧**：`default` 层确定基线（未给出即最严 `none/false/false`），
   `user/workspace/profile/session` 逐层对 filesystem 取更严等级、对 network/shell 取与。
   项目层把 `filesystem` 设成 `unrestricted` 无法放大用户层已收紧的值。
3. **credentialRef 分层受限**：只有 `default`/`user` 能设置；项目层设置会被忽略**并写进
   `EffectiveConfig.Warnings`**，不静默吞掉。
4. **数组资源整项覆盖**：按稳定 ID 合并，高层同 ID 覆盖整项（含 attrs），不做字段级合并——
   否则"高层改一个字段"与"高层禁用整项"会混成不确定结果。`enabled=false` 的项仍留在列表里，
   禁用不靠"消失"表达。
5. **FTS5 trigram 是真验证**：迁移后执行 `VerifyFTSTrigram`，既查 `sqlite_compileoption_used('ENABLE_FTS5')`，
   也建临时 trigram 表并做一次中文子串命中查询。查询串必须是文本的真实子串（首版写成
   "周报整理" 而文本是"每周整理项目…"，命中 0 直接暴露了这一点）。
6. **凭证用 DPAPI（Windows）**：`CryptProtectData` 用户作用域加密后落盘，文件名取 ref 的
   sha256 前缀。非 Windows 明确返回 `CREDENTIAL_STORE_UNAVAILABLE`，改用 `MemStore`
   （仅当前会话，不落盘）。

## 对抗性验证（确认测试有区分力）

| 破坏点 | 期望 | 实际 |
|---|---|---|
| `Store.Load`/`CAS` 的 checksum 判断短路为永假 | 外部编辑检测失效 → 测试红 | `TestCASRejectsExternalEdit` 红：`期望 AppError(CONFIG_CONFLICT)，实际 <nil>` |
| `nativeStore.Put` 直接写入未加密 payload | 落盘出现可直接还原的凭证 → 测试红 | 红在**扫描**断言（不是 Get）：`落盘文件出现明文凭证: [...5b549dd9...cred]` |
| （首版）扫描只查原始字节 | —— | 未加密落盘时原始字节扫描 0 命中，因为 payload 里 secret 被 base64。已改为同时扫原始字节与 base64（标准/raw/URL），否则这条 SEC07 断言是假绿 |

## 本地验证

```text
go build ./...                                   OK
go vet ./...                                     OK
go test ./... -count=1 -timeout 600s             全部 ok
CGO_ENABLED=1 go test -race ./internal/...       全部 ok（含并发 CAS / MemStore 并发用例）
gofmt -l internal cmd tools tests                无输出
GOOS/GOARCH ∈ {linux/amd64, darwin/arm64, windows/arm64} go vet ./...   OK
```

## 限制（不得当作已通过）

- **跨进程并发未覆盖**：CAS 的互斥是进程内 per-key mutex。跨进程同时写同一 key 需要 OS 文件锁
  （LockFileEx / flock），本轮未实现，也未声称已覆盖。
- **SEC07 的 mac/linux 分支**：Keychain / Secret Service 未接入。这三个目标的凭证持久化记
  BLOCKED，而不是"用内存先用着"。
- **DB02 的锁/权限诊断**只覆盖"打开失败带路径的错误信息"，没有构造真实文件锁竞争。
- 迁移目前只有 `001_initial`，**没有升级迁移的实测**（DB01 的"升级 schema"部分只验证了
  重复运行幂等与失败回滚，未验证 v1→v2 升级路径）。
