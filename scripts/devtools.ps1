# Anvil rebuild devtools wrapper.
#
# Builds the Go CLI and then invokes it, returning the child process exit code
# unchanged: 0 pass, 1 valid FAIL evidence, 2 incomplete input/evidence.
# Do not replace this with `go run`: `go run` collapses exit codes 1 and 2 to 1
# (docs/adr/0001-devtools-exit-code-fidelity.md).
#
# All writable tool locations are pinned inside the D: worktree before the
# build, so invoking this script from a shell without the rebuild environment
# cannot leak caches or temp files to another drive.

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    $tooling = Join-Path $repoRoot '.tmp\tooling'
    $tmp = Join-Path $tooling 'tmp'
    foreach ($dir in @($tooling, $tmp,
                       (Join-Path $tooling 'go-build'),
                       (Join-Path $tooling 'go-mod'),
                       (Join-Path $tooling 'gopath'),
                       (Join-Path $tooling 'npm-cache'),
                       (Join-Path $repoRoot '.tmp\bin'))) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
    $env:TEMP = $tmp
    $env:TMP = $tmp
    $env:TMPDIR = $tmp
    $env:GOTMPDIR = $tmp
    $env:GOCACHE = Join-Path $tooling 'go-build'
    $env:GOMODCACHE = Join-Path $tooling 'go-mod'
    $env:GOPATH = Join-Path $tooling 'gopath'
    $env:npm_config_cache = Join-Path $tooling 'npm-cache'

    $bin = Join-Path $repoRoot '.tmp\bin\devtools.exe'
    & go build -o $bin ./cmd/devtools
    if ($LASTEXITCODE -ne 0) {
        Write-Error "devtools build failed with exit code $LASTEXITCODE"
        exit 2
    }

    & $bin @args
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
