# devtools 包装脚本（PowerShell）
#
# 为什么需要它：`go run` 会把被运行程序的退出码压成 1，无法区分
# "断言失败(1)" 与 "配置缺失(2)"。见 docs/adr/0001-devtools-exit-code-fidelity.md。
#
# 用法（仓库任意目录）：
#   powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 verify --suite=static
#   powershell -ExecutionPolicy Bypass -File scripts/devtools.ps1 gate --id=G0 --evidence=D:\evidence
#
# 退出码：原样透传 devtools 的 0 / 1 / 2。

param(
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$DevArgs
)

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot

# 临时目录放到 D 盘：C 盘曾因测试产物（每个 UI case 一个 31MB 的构建目录、
# 每次 policy 测试一个执行器二进制）被占满到只剩 1GB。这里显式覆盖子进程的
# TEMP/TMP，让 runner 派生的一切临时文件落在 D 盘的 caches/tmp 下（可用 ANVIL_CACHE_ROOT 改）。
$cacheRoot = if ($env:ANVIL_CACHE_ROOT) { $env:ANVIL_CACHE_ROOT } elseif (Test-Path 'D:\caches') { 'D:\caches' } else { '' }
if ($cacheRoot) {
  $tmpDir = Join-Path $cacheRoot 'tmp'
  if (-not (Test-Path $tmpDir)) { New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null }
  $env:TEMP = $tmpDir
  $env:TMP = $tmpDir
}
$bin = Join-Path $root '.tools\bin\devtools.exe'

Push-Location $root
try {
  & go build -o $bin ./cmd/devtools
  if ($LASTEXITCODE -ne 0) {
    Write-Output "devtools build FAILED (configuration error, exit 2)"
    exit 2
  }
  & $bin @DevArgs
  exit $LASTEXITCODE
} finally {
  Pop-Location
}