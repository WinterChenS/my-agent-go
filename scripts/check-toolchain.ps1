<#
.SYNOPSIS
  F00 toolchain gate. Validates that the *actually resolved* executables on PATH
  match the frozen baseline in build/versions.lock.json.

  Exit codes:
    0  environment satisfies the frozen baseline
    2  configuration missing or environment does not satisfy the baseline

  The script NEVER invokes npm/node to "fix" anything; it only inspects the
  resolved command path and its version, and prints the actual path so a wrong
  (e.g. WeChat-devtools-grabbing) node is visibly rejected.

  Path comparison canonicalizes symlinks/junctions, so an nvm-managed baseline
  ('D:\Program Files\nvm\v24.16.0') and the baseline symlink
  ('D:\Program Files\nodejs') are recognised as the same install, while a real
  different-version node is still rejected by the version check.
#>
param(
  [string]$LockFile = "",
  [switch]$Verbose
)

$ErrorActionPreference = 'Continue'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
if (-not $LockFile) {
  # scripts/ -> repo root -> build/versions.lock.json
  $LockFile = Join-Path (Split-Path -Parent $ScriptDir) 'build\versions.lock.json'
}
$LockFile = Resolve-Path $LockFile -ErrorAction SilentlyContinue

$errors   = [System.Collections.Generic.List[string]]::new()
$warnings = [System.Collections.Generic.List[string]]::new()

function Fail { param([string]$m) $script:errors.Add($m); Write-Output "FAIL: $m" }
function Warn { param([string]$m) $script:warnings.Add($m); Write-Output "WARN: $m" }
function Pass { param([string]$m) Write-Output "OK:   $m" }

function Resolve-Actual {
  param([string]$name)
  try {
    $c = Get-Command $name -ErrorAction Stop
    return $c.Path
  } catch {
    return $null
  }
}

function Resolve-RealPath {
  # Resolve symlinks/junctions in *any* path segment to the real target.
  # Needed because the baseline node lives at 'D:\Program Files\nodejs\node.exe',
  # which is itself a symlink managed by nvm. A correctly pinned nvm install
  # ('D:\Program Files\nvm\v24.16.0\node.exe') is the SAME file, so comparing raw
  # paths would produce a false rejection of a valid baseline environment.
  param([string]$p)
  if (-not $p) { return $null }
  $full = [System.IO.Path]::GetFullPath($p)
  $root = [System.IO.Path]::GetPathRoot($full)
  $cur = $root
  foreach ($seg in $full.Substring($root.Length).Split('\')) {
    if (-not $seg) { continue }
    $cur = Join-Path $cur $seg
    for ($i = 0; $i -lt 12; $i++) {
      $item = $null
      try { $item = Get-Item -LiteralPath $cur -Force -ErrorAction Stop } catch { break }
      if (-not ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) { break }
      $t = $null
      try { $t = $item.Target } catch { $t = $null }
      if (-not $t) { break }
      if ($t -is [array]) { $t = $t[0] }
      if (-not [System.IO.Path]::IsPathRooted($t)) { $t = Join-Path (Split-Path -Parent $cur) $t }
      $cur = [System.IO.Path]::GetFullPath($t)
    }
  }
  return $cur.TrimEnd('\')
}

function Normalize($p) {
  if (-not $p) { return $null }
  $real = Resolve-RealPath $p
  if (-not $real) { $real = $p }
  return $real.TrimEnd('\').ToLowerInvariant()
}

function Get-Version {
  param([string]$path, [string]$arg)
  if (-not $path) { return $null }
  try {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $path
    $psi.Arguments = $arg
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.UseShellExecute = $false
    $p = [System.Diagnostics.Process]::Start($psi)
    $stdout = $p.StandardOutput.ReadToEnd()
    $stderr = $p.StandardError.ReadToEnd()
    $p.WaitForExit()
    return ($stdout + $stderr).Trim()
  } catch {
    return $null
  }
}

Write-Output "=== F00 toolchain check ($(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')) ==="

# ---- load expected baseline from lock -------------------------------------
$exp = $null
if (-not $LockFile -or -not (Test-Path $LockFile)) {
  Fail "versions.lock.json not found at '$LockFile' (configuration missing)"
} else {
  try { $exp = Get-Content $LockFile -Raw -Encoding UTF8 | ConvertFrom-Json }
  catch { Fail "versions.lock.json unparseable: $_" }
}

if (-not $exp) {
  Write-Output "--- summary: $($errors.Count) failure(s) ---"
  foreach ($e in $errors) { Write-Output "  - $e" }
  exit 2
}

$expGo      = [string]$exp.go
$expNode    = [string]$exp.node
$expNpm     = [string]$exp.npm
$expWails   = [string]$exp.wails
$expPaths   = $exp.expectedPaths

# ---- GO --------------------------------------------------------------------
$goPath = Resolve-Actual 'go'
if (-not $goPath) { Fail "go: not found on PATH" }
else {
  $goVer = Get-Version $goPath 'version'
  $ok = $goVer -match "go$expGo\b"
  $expGoPath = if ($expPaths -and $expPaths.go) { Normalize $expPaths.go } else { $null }
  $actGoPath = Normalize $goPath
  if (-not $ok) { Fail "go: resolved '$actGoPath' reports '$goVer', expected go$expGo" }
  elseif ($expGoPath -and $actGoPath -ne $expGoPath) {
    Fail "go: resolved to '$actGoPath' but expected '$expGoPath' (wrong toolchain on PATH)"
  }
  else { Pass "go: $actGoPath -> $goVer" }
}

# ---- NODE (the WeChat-devtools-grab guard) --------------------------------
$nodePath = Resolve-Actual 'node'
if (-not $nodePath) { Fail "node: not found on PATH" }
else {
  $nodeVer = Get-Version $nodePath '-v'
  $ok = $nodeVer -eq "v$expNode"
  $expNodePath = if ($expPaths -and $expPaths.node) { Normalize $expPaths.node } else { $null }
  $actNodePath = Normalize $nodePath
  if (-not $ok) {
    # This is exactly the ToolchainRejectsWechatNode scenario: a wrong node
    # (e.g. v16 from WeChat devtools) is first on PATH. Reject and show path.
    Fail "node: resolved to '$actNodePath' (version '$nodeVer'), expected v$expNode"
  }
  elseif ($expNodePath -and $actNodePath -ne $expNodePath) {
    Fail "node: resolved to '$actNodePath' but expected baseline '$expNodePath'"
  }
  else { Pass "node: $actNodePath -> $nodeVer" }
}

# ---- NPM ------------------------------------------------------------------
# Per project rule, npm is always invoked via npm.cmd (npm.ps1 is blocked by
# the execution policy on this host). Resolve npm.cmd explicitly.
$npmPath = Resolve-Actual 'npm.cmd'
if (-not $npmPath) { Fail "npm: not found on PATH" }
else {
  $npmVer = Get-Version $npmPath '-v'
  $ok = $npmVer -eq $expNpm
  $expNpmPath = if ($expPaths -and $expPaths.npm) { Normalize $expPaths.npm } else { $null }
  $actNpmPath = Normalize $npmPath
  # Get-Command npm may resolve to npm.ps1 or npm.cmd (nodejs ships both); what
  # matters is that it lives next to the baseline node and reports the right
  # version, i.e. it is the npm belonging to node 24.16.0, not the workbuddy v22.
  $actDir = Normalize (Split-Path -Parent $npmPath)
  $expDir = if ($expNpmPath) { Normalize (Split-Path -Parent $expNpmPath) } else { $null }
  if (-not $ok) { Fail "npm: resolved to '$actNpmPath' (version '$npmVer'), expected $expNpm" }
  elseif ($expDir -and $actDir -ne $expDir) {
    Fail "npm: resolved to '$actNpmPath' but expected it under '$expDir' (wrong node/npm pairing)"
  }
  else { Pass "npm: $actNpmPath -> $npmVer" }
}

# ---- WAILS CLI -------------------------------------------------------------
$wailsPath = Resolve-Actual 'wails3'
if (-not $wailsPath) { Fail "wails3: not found on PATH (install Wails v3 CLI)" }
else {
  $wailsVer = Get-Version $wailsPath 'version'
  if (-not $wailsVer) { $wailsVer = Get-Version $wailsPath '--version' }
  $ok = $wailsVer -match [regex]::Escape($expWails)
  $expWailsPath = if ($expPaths -and $expPaths.wails) { Normalize $expPaths.wails } else { $null }
  $actWailsPath = Normalize $wailsPath
  if (-not $ok) { Fail "wails3: resolved to '$actWailsPath' (version '$wailsVer'), expected $expWails" }
  elseif ($expWailsPath -and $actWailsPath -ne $expWailsPath) {
    Fail "wails3: resolved to '$actWailsPath' but expected '$expWailsPath'"
  }
  else { Pass "wails3: $actWailsPath -> $wailsVer" }
}

# ---- native toolchain report (informational; not a hard gate) -------------
$cgo = (& "D:\Program Files\Go\bin\go.exe" env CGO_ENABLED 2>$null)
Write-Output "INFO: CGO_ENABLED=$cgo"
$wv = Test-Path "C:\Program Files (x86)\Microsoft\EdgeWebView\Application"
Write-Output "INFO: WebView2 runtime present under EdgeWebView/Application: $wv"
$gcc = Resolve-Actual 'gcc'
Write-Output "INFO: gcc on PATH: $(if($gcc){$gcc}else{'ABSENT'})"

Write-Output "--- summary: $($errors.Count) failure(s), $($warnings.Count) warning(s) ---"
foreach ($e in $errors) { Write-Output "  FAIL: $e" }

if ($errors.Count -eq 0) { exit 0 } else { exit 2 }
