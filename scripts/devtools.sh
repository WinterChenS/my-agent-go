#!/usr/bin/env bash
# devtools 包装脚本（POSIX sh）
#
# 为什么需要它：`go run` 会把被运行程序的退出码压成 1，无法区分
# "断言失败(1)" 与 "配置缺失(2)"。见 docs/adr/0001-devtools-exit-code-fidelity.md。
#
# 用法：
#   ./scripts/devtools.sh verify --suite=static
#   ./scripts/devtools.sh gate --id=G0 --evidence=/abs/evidence
#
# 退出码：原样透传 devtools 的 0 / 1 / 2。
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/.tools/bin/devtools"

if ! (cd "$root" && go build -o "$bin" ./cmd/devtools); then
  echo "devtools 构建失败，无法执行检查（配置问题，退出 2）" >&2
  exit 2
fi

"$bin" "$@"
exit $?
