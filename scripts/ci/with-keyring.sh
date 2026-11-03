#!/usr/bin/env bash
# 在 Linux 上把命令放进一个带 D-Bus session + 已解锁 keyring 的环境里执行；
# 其它平台直接执行。
#
# 为什么需要它：SEC07 的 Linux 后端走 Secret Service，而 CI runner 默认没有
# keyring daemon。只在某一个步骤里起 keyring 是不够的——job 级设了
# MYAGENT_REQUIRE_NATIVE_CREDSTORE=1，任何一步跑到凭证测试都会因为
# "后端不可用"而判红（实测：test 步骤通过、race 步骤红的正是这个原因）。
#
# 用法：bash scripts/ci/with-keyring.sh go test ./... -count=1
set -euo pipefail

if [ "$(uname -s)" != "Linux" ]; then
  exec "$@"
fi

if ! command -v secret-tool >/dev/null 2>&1; then
  echo "::error::未安装 secret-tool（libsecret-tools），SEC07 无法在 Linux 上验证" >&2
  exit 2
fi

exec dbus-run-session -- bash -c '
  set -euo pipefail
  printf "%s" "myagent-ci" | gnome-keyring-daemon --unlock --components=secrets --daemonize >/dev/null 2>&1
  # 探测：exit 1 = 查不到（服务活着），其它非 0 = 服务不可用。
  set +e
  secret-tool lookup service cn.myagent.credentials account __probe__ >/dev/null 2>&1
  probe=$?
  set -e
  if [ "$probe" -ne 1 ]; then
    echo "::error::Secret Service 不可用（secret-tool 探测退出码 $probe）" >&2
    ps -ef | grep -i keyring >&2 || true
    exit 2
  fi
  exec "$@"
' -- "$@"
