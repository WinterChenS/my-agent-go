# Anvil R0-RS01 D-drive-only Rust/Cargo environment (Windows).
#
# Usage (the execution policy forbids calling the script directly):
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts\env-rs.ps1
#   powershell -NoProfile -ExecutionPolicy Bypass -Command "& .\scripts\env-rs.ps1; cargo test --locked -p anvil-paths"
#
# Every writable location set here is on D:. The values below can be overridden
# by exporting the same variable with a D: path before running the script; a
# non-D: value is replaced by the D: default and the replacement is reported.
# Nothing silently falls back to a C: default.
#
# Exit codes: 0 = environment ready, 2 = a required item is missing or not on D:.

$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
    [Console]::Error.WriteLine("env-rs: ERROR: $Message")
    exit 2
}

function Normalize-PathText([string]$Value) {
    return ($Value -replace '/', '\')
}

function Test-DriveD([string]$Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return $false }
    $normalized = Normalize-PathText $Value
    if ($normalized.Equals('D:', [StringComparison]::OrdinalIgnoreCase)) { return $true }
    return $normalized.StartsWith('D:\', [StringComparison]::OrdinalIgnoreCase)
}

function Invoke-Capture([string]$Exe, [string[]]$Arguments) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = (& $Exe @Arguments 2>&1 | Out-String).Trim()
        $code = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previous
    }
    return [pscustomobject]@{ Output = $output; ExitCode = $code }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not (Test-DriveD $repoRoot)) {
    Fail "repository root '$repoRoot' is not on D:; the rebuild worktree must live on D:"
}

# Absolute D: defaults; override by exporting a D: value before this script.
$defaults = [ordered]@{
    RUSTUP_HOME      = 'D:\Microsoft\tooling\rustup'
    CARGO_HOME       = 'D:\Microsoft\tooling\cargo'
    TEMP             = (Join-Path $repoRoot '.tmp\tooling\tmp')
    TMP              = (Join-Path $repoRoot '.tmp\tooling\tmp')
    CARGO_TARGET_DIR = (Join-Path $repoRoot '.tmp\tooling\cargo-target')
}

foreach ($name in $defaults.Keys) {
    $current = [Environment]::GetEnvironmentVariable($name, 'Process')
    if (-not (Test-DriveD $current)) {
        if (-not [string]::IsNullOrWhiteSpace($current)) {
            Write-Output "env-rs: note: $name was '$current' (not on D:) -> '$($defaults[$name])'"
        }
        Set-Item -Path "Env:$name" -Value $defaults[$name]
    }
}
$env:TMPDIR = $env:TEMP

$msvcRoot = if ($env:ANVIL_MSVC_ROOT) { $env:ANVIL_MSVC_ROOT } else { 'D:\Microsoft\VS\2022\BuildTools' }
$sdkRoot = if ($env:ANVIL_SDK_ROOT) { $env:ANVIL_SDK_ROOT } else { 'D:\Microsoft\WindowsKits\10' }
if (-not (Test-DriveD $msvcRoot)) { Fail "ANVIL_MSVC_ROOT '$msvcRoot' is not on D:" }
if (-not (Test-DriveD $sdkRoot)) { Fail "ANVIL_SDK_ROOT '$sdkRoot' is not on D:" }

$vcTools = Join-Path $msvcRoot 'VC\Tools\MSVC'
if (-not (Test-Path -LiteralPath $vcTools -PathType Container)) {
    Fail "MSVC tools directory not found: $vcTools"
}
if ($env:ANVIL_MSVC_VERSION) {
    $msvcVer = $env:ANVIL_MSVC_VERSION
}
else {
    $msvcVer = Get-ChildItem -LiteralPath $vcTools -Directory |
        Sort-Object { try { [version]$_.Name } catch { [version]'0.0' } } -Descending |
        Select-Object -First 1 -ExpandProperty Name
}
if (-not $msvcVer) { Fail "no MSVC toolset under $vcTools" }

$sdkIncludes = Join-Path $sdkRoot 'Include'
if (-not (Test-Path -LiteralPath $sdkIncludes -PathType Container)) {
    Fail "Windows SDK include directory not found: $sdkIncludes"
}
if ($env:ANVIL_SDK_VERSION) {
    $sdkVer = $env:ANVIL_SDK_VERSION
}
else {
    $sdkVer = Get-ChildItem -LiteralPath $sdkIncludes -Directory |
        Where-Object { $_.Name -match '^\d+\.\d+\.\d+\.\d+$' } |
        Sort-Object { [version]$_.Name } -Descending |
        Select-Object -First 1 -ExpandProperty Name
}
if (-not $sdkVer) { Fail "no Windows SDK version under $sdkIncludes" }

$vcBin = Join-Path $msvcRoot "VC\Tools\MSVC\$msvcVer\bin\Hostx64\x64"
$vcLib = Join-Path $msvcRoot "VC\Tools\MSVC\$msvcVer\lib\x64"
$vcInc = Join-Path $msvcRoot "VC\Tools\MSVC\$msvcVer\include"
$sdkBin = Join-Path $sdkRoot "bin\$sdkVer\x64"
$sdkLibU = Join-Path $sdkRoot "Lib\$sdkVer\ucrt\x64"
$sdkLibM = Join-Path $sdkRoot "Lib\$sdkVer\um\x64"
$sdkInc = Join-Path $sdkRoot "Include\$sdkVer"

$required = @(
    (Join-Path $vcBin 'cl.exe'),
    (Join-Path $vcBin 'link.exe'),
    $vcLib,
    $vcInc,
    (Join-Path $sdkBin 'rc.exe'),
    $sdkLibU,
    $sdkLibM,
    $sdkInc
)
foreach ($item in $required) {
    if (-not (Test-Path -LiteralPath $item)) { Fail "required MSVC/SDK item is missing: $item" }
}

$cargoBin = Join-Path $env:CARGO_HOME 'bin'
# Drop any user-profile C: cargo/bin shim directory. The rustup proxies must come
# from the D: CARGO_HOME so a missing D: toolchain fails loudly instead of
# silently using a C: install. Pre-existing tools elsewhere stay readable through
# the explicit D: entries only.
$pathEntries = @($vcBin, $sdkBin, $cargoBin) + (($env:PATH -split ';') | Where-Object {
    $_ -and -not ($_ -match '^[A-Za-z]:\\Users\\[^\\]+\\.cargo\\bin\\?$')
})
$env:PATH = ($pathEntries -join ';')
$env:LIB = (@($vcLib, $sdkLibU, $sdkLibM) -join ';')
$env:INCLUDE = (@(
    $vcInc,
    (Join-Path $sdkInc 'ucrt'),
    (Join-Path $sdkInc 'um'),
    (Join-Path $sdkInc 'shared'),
    (Join-Path $sdkInc 'winrt')
) -join ';')
$env:RC = Join-Path $sdkBin 'rc.exe'

foreach ($directory in @($env:TEMP, $env:TMP, $env:CARGO_TARGET_DIR)) {
    if (-not (Test-DriveD $directory)) { Fail "writable directory '$directory' is not on D:" }
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
}
foreach ($name in $defaults.Keys) {
    if (-not (Test-DriveD ([Environment]::GetEnvironmentVariable($name, 'Process')))) {
        Fail "$name is not on D: after setup"
    }
}

foreach ($tool in @('rustc', 'cargo', 'rustup', 'cl', 'link')) {
    $command = Get-Command $tool -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $command) { Fail "required tool '$tool' is not on PATH" }
}

foreach ($tool in @('rustc', 'cargo', 'rustup')) {
    $source = Normalize-PathText ((Get-Command $tool -CommandType Application | Select-Object -First 1).Source)
    if (-not $source.StartsWith($cargoBin, [StringComparison]::OrdinalIgnoreCase)) {
        Fail @"
$tool resolves to '$source', not the D: rustup proxy under '$cargoBin'.
Bootstrap the D: rustup controller once, then re-run this script:
  New-Item -ItemType Directory -Force '$cargoBin' | Out-Null
  Copy-Item (Get-Command rustup.exe).Source '$cargoBin\rustup.exe' -Force
  & '$cargoBin\rustup.exe' toolchain install 1.95.0 --profile minimal --component rustfmt,clippy --target x86_64-pc-windows-msvc
"@
    }
}

$rustc = Invoke-Capture 'rustc' @('-V')
if ($rustc.ExitCode -ne 0) { Fail "rustc -V failed ($($rustc.ExitCode)): $($rustc.Output)" }
if (-not $rustc.Output.StartsWith('rustc 1.95.0')) {
    Fail "rustc -V reported '$($rustc.Output)'; rust-toolchain.toml pins 1.95.0 (run from the repository root)"
}
$cargo = Invoke-Capture 'cargo' @('-V')
if ($cargo.ExitCode -ne 0) { Fail "cargo -V failed ($($cargo.ExitCode)): $($cargo.Output)" }

Write-Output "env-rs: repo_root        = $repoRoot"
Write-Output "env-rs: RUSTUP_HOME      = $env:RUSTUP_HOME"
Write-Output "env-rs: CARGO_HOME       = $env:CARGO_HOME"
Write-Output "env-rs: TEMP             = $env:TEMP"
Write-Output "env-rs: TMP              = $env:TMP"
Write-Output "env-rs: TMPDIR           = $env:TMPDIR"
Write-Output "env-rs: CARGO_TARGET_DIR = $env:CARGO_TARGET_DIR"
Write-Output "env-rs: MSVC             = $msvcRoot $msvcVer"
Write-Output "env-rs: SDK              = $sdkRoot $sdkVer"
Write-Output "env-rs: rustc            = $((Get-Command rustc -CommandType Application | Select-Object -First 1).Source) :: $($rustc.Output)"
Write-Output "env-rs: cargo            = $((Get-Command cargo -CommandType Application | Select-Object -First 1).Source) :: $($cargo.Output)"
Write-Output "env-rs: cl               = $((Get-Command cl -CommandType Application | Select-Object -First 1).Source)"
Write-Output "env-rs: link             = $((Get-Command link -CommandType Application | Select-Object -First 1).Source)"
Write-Output "env-rs: rc               = $env:RC"
Write-Output "env-rs: OK"
exit 0
