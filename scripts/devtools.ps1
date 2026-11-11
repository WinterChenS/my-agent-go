# Anvil rebuild devtools wrapper (Rust).
#
# Builds `tools/devtools` with Cargo and then invokes the binary, returning the
# child process exit code unchanged: 0 pass, 1 valid FAIL evidence, 2 incomplete
# input/evidence. Do not replace this with anything that normalizes exit codes
# (docs/adr/0001-devtools-exit-code-fidelity.md).
#
# All writable tool locations are pinned inside the D: worktree before the
# build: scripts/env-rs.ps1 sets RUSTUP_HOME, CARGO_HOME, TEMP and
# RUSTUP_AUTO_INSTALL=0, and .cargo/config.toml pins the target directory. Run
# this script with -ExecutionPolicy Bypass so the environment script may be
# dot-sourced.

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    . (Join-Path $PSScriptRoot 'env-rs.ps1') | Out-Null

    & cargo build --locked -p anvil-devtools
    if ($LASTEXITCODE -ne 0) {
        Write-Error "devtools build failed with exit code $LASTEXITCODE"
        exit 2
    }

    $binary = Join-Path $env:CARGO_TARGET_DIR 'debug\anvil-devtools.exe'
    if (-not (Test-Path -LiteralPath $binary)) {
        Write-Error "devtools binary not found after a successful build: $binary"
        exit 2
    }

    & $binary @args
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
