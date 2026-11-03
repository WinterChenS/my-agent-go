# ADR 0001：统一命令契约的退出码保真

- 日期：2026-11-03
- 状态：已采纳
- 影响任务：F01（及其后所有依赖门禁命令的任务）

## 背景

master plan 的「统一命令契约」把入口写成：

```text
go run ./cmd/devtools verify --suite=static
go run ./cmd/devtools gate --id=G0 --evidence=<abs-path>
```

同时规定了退出码语义：**0 通过 / 1 断言失败 / 2 配置或索引或目标或门禁缺失/未知**，
并要求「所有子进程保留退出码，不允许 `|| true`」。GOV03 明确要求区分 1 与 2。

## 观测到的事实

`go run` 不会把被运行程序的退出码原样传出：程序 `os.Exit(2)` 时，`go run` 自身
以 **1** 退出，并在 stderr 打一行 `exit status 2`。实测：

```text
$ go run ./cmd/devtools verify --suite=unit
... 前端缺少 test 脚本（runner 不存在） ...
unit 退出码: 2
go.exe : exit status 2
$ echo $LASTEXITCODE
1
```

即：以 `go run` 作为 CI/门禁入口时，**门禁看到的永远是 1，无法区分"断言失败"与
"配置缺失"**，GOV03 的判定条件不可满足，且 CI 会把配置错误误报成测试失败。

## 决策

1. 保留 `go run ./cmd/devtools ...` 作为人工调试入口（可读性好），但在文档中标注
   **其退出码会被压成 1**，不得用于门禁判定。
2. 新增包装脚本 `scripts/devtools.ps1` / `scripts/devtools.sh`：先
   `go build -o .tools/bin/devtools[.exe] ./cmd/devtools`，再直接执行二进制并
   `exit $LASTEXITCODE`。这是 **CI 与门禁的规范入口**，退出码原样保真。
3. `cmd/devtools` 自身的退出码语义不变（0/1/2），不因入口形式而放宽。

## 影响

- 变更的是**入口形式**，不是验收阈值，也未扩大或缩小任何功能范围。
- `docs/superpowers/plans/2026-11-03-agent-client-master.md` 里的命令串保持字面不变，
  但执行时以包装脚本为准；此处差异由本 ADR 承载，不改原计划文档。
- 后续 X03/X04 的打包与安装包校验，同样走包装脚本判定退出码。

## 未采纳的方案

- 让 devtools 用 0/1 两态表达：与基线及 v1-gates.md 的 2 语义冲突，等于降低门禁区分度。
- 在 CI 里解析 stdout 判断码：脆弱且等于用日志冒充退出码，违反「不能把日志成功覆盖命令失败」。
