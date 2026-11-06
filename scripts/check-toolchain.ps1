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
  [switch]$Verbose,
  # ENV01–ENV03 的机器可读观察目录（集成 runner 通过 ANVIL_OBSERVATION_DIR 传入）。
  # 为空时脚本的 stdout 与退出码与改造前逐字一致；非空时**额外**落盘三份机器报告
  # 和 observation.json，供 external-observation oracle 引用。
  [string]$ObservationDir = $env:ANVIL_OBSERVATION_DIR
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

function Get-FileSha256 {
  param([string]$path)
  if (-not $path) { return $null }
  try { return 'sha256:' + (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() }
  catch { return $null }
}

function Compare-VersionParts {
  # 与 internal/devtools/platforms.go 的 compareOSVersion 同口径：逐段比较数字，
  # 缺段视为 0（"11" vs "11.0" 相等）。任一段不可解析时返回 $null（不猜）。
  param([string]$a, [string]$b)
  if (-not $a -or -not $b) { return $null }
  $pa = $a.Trim().Split('.')
  $pb = $b.Trim().Split('.')
  $n = [Math]::Max($pa.Count, $pb.Count)
  for ($i = 0; $i -lt $n; $i++) {
    $va = 0; $vb = 0
    if ($i -lt $pa.Count) {
      if (-not [int]::TryParse($pa[$i].Trim(), [ref]$va) -or $va -lt 0) { return $null }
    }
    if ($i -lt $pb.Count) {
      if (-not [int]::TryParse($pb[$i].Trim(), [ref]$vb) -or $vb -lt 0) { return $null }
    }
    if ($va -lt $vb) { return -1 }
    if ($va -gt $vb) { return 1 }
  }
  return 0
}

function Get-HostBuildNumber {
  # RtlGetVersion 的 PowerShell 等价物：GetVersionEx 受兼容性清单影响会谎报 6.2，
  # 这里用 CIM 的真实 BuildNumber，读不到再退回注册表。取不到就返回 $null（不猜）。
  try {
    $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
    if ($os -and $os.BuildNumber) { return [string]$os.BuildNumber }
  } catch { }
  try {
    $reg = Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion' -Name CurrentBuildNumber -ErrorAction Stop
    if ($reg -and $reg.CurrentBuildNumber) { return [string]$reg.CurrentBuildNumber }
  } catch { }
  return $null
}

function Write-JsonReport {
  param([string]$path, $value)
  $json = $value | ConvertTo-Json -Depth 12
  [System.IO.File]::WriteAllText($path, $json + "`n", (New-Object System.Text.UTF8Encoding($false)))
}

# 观察状态：下面各段在解析/判定时把真实结果记进这些变量，报告段只读它们，
# 不重复实现一遍判定规则（两套规则必然会分叉）。
$goVer = $null; $nodeVer = $null; $npmVer = $null; $wailsVer = $null
$goMatch = $false; $nodeMatch = $false; $npmMatch = $false; $wailsMatch = $false

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
  else { Pass "go: $actGoPath -> $goVer"; $goMatch = $true }
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
  else { Pass "node: $actNodePath -> $nodeVer"; $nodeMatch = $true }
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
  else { Pass "npm: $actNpmPath -> $npmVer"; $npmMatch = $true }
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
  else { Pass "wails3: $actWailsPath -> $wailsVer"; $wailsMatch = $true }
}

# ---- native toolchain report (informational; not a hard gate) -------------
$cgo = (& "D:\Program Files\Go\bin\go.exe" env CGO_ENABLED 2>$null)
Write-Output "INFO: CGO_ENABLED=$cgo"
$wv = Test-Path "C:\Program Files (x86)\Microsoft\EdgeWebView\Application"
Write-Output "INFO: WebView2 runtime present under EdgeWebView/Application: $wv"
$gcc = Resolve-Actual 'gcc'
Write-Output "INFO: gcc on PATH: $(if($gcc){$gcc}else{'ABSENT'})"

# ---- machine-readable observation report (ENV01/ENV02/ENV03) -------------
# 只在观察目录非空时写文件：没有 -ObservationDir / ANVIL_OBSERVATION_DIR 时
# 脚本的 stdout 与退出码必须与改造前逐字一致（现有调用方零影响）。
#
# 报告里的值不是把上面的 OK/FAIL 行再拄一遍：
#   - 路径是 Get-Command 实际解析到的那一个（并附规范化的真实路径）；
#   - 版本是被解析到的那个二进制自己报的；
#   - 哈希是文件内容的 sha256；
#   - 目标可达性由真实宿主探测 + build/platforms.json 推得。
# 三者都落盘后，observation.json 只引用这三份文件（附各自哈希），
# 集成 runner 再把它封装成 external-observation oracle。
if ($ObservationDir) {
  try {
    $reportDir = [System.IO.Path]::GetFullPath($ObservationDir)
    if (-not (Test-Path -LiteralPath $reportDir)) {
      New-Item -ItemType Directory -Path $reportDir -Force | Out-Null
    }
    $repoRoot = Split-Path -Parent $ScriptDir
    $generatedAt = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    $scriptExit = if ($errors.Count -eq 0) { 0 } else { 2 }

    # ---- ENV01: 工具链实际解析结果 ----
    $gccPath = Resolve-Actual 'gcc'
    $gccVer = Get-Version $gccPath '--version'
    if ($gccVer) { $gccVer = ($gccVer -split "`n")[0].Trim() }
    $tcEntries = @(
      [ordered]@{
        name = 'go'; hardGate = $true; command = 'go'
        rawPath = $goPath; resolvedPath = $(if ($goPath) { Normalize $goPath } else { $null })
        version = $goVer; expected = "go$expGo"
        expectedPath = $(if ($expPaths -and $expPaths.go) { $expPaths.go } else { $null })
        match = [bool]$goMatch
      },
      [ordered]@{
        name = 'node'; hardGate = $true; command = 'node'
        rawPath = $nodePath; resolvedPath = $(if ($nodePath) { Normalize $nodePath } else { $null })
        version = $nodeVer; expected = "v$expNode"
        expectedPath = $(if ($expPaths -and $expPaths.node) { $expPaths.node } else { $null })
        match = [bool]$nodeMatch
      },
      [ordered]@{
        name = 'npm'; hardGate = $true; command = 'npm.cmd'
        rawPath = $npmPath; resolvedPath = $(if ($npmPath) { Normalize $npmPath } else { $null })
        version = $npmVer; expected = $expNpm
        expectedPath = $(if ($expPaths -and $expPaths.npm) { $expPaths.npm } else { $null })
        match = [bool]$npmMatch
      },
      [ordered]@{
        name = 'wails'; hardGate = $true; command = 'wails3'
        rawPath = $wailsPath; resolvedPath = $(if ($wailsPath) { Normalize $wailsPath } else { $null })
        version = $wailsVer; expected = $expWails
        expectedPath = $(if ($expPaths -and $expPaths.wails) { $expPaths.wails } else { $null })
        match = [bool]$wailsMatch
      },
      [ordered]@{
        # 原生工具链在脚本里一直是 INFO 级（不是硬门禁）：如实记录，不伪装成 PASS/FAIL。
        name = 'native-sdk'; hardGate = $false; command = 'gcc'
        rawPath = $gccPath; resolvedPath = $(if ($gccPath) { Normalize $gccPath } else { $null })
        version = $gccVer; expected = $null; expectedPath = $null
        match = $(if ($gccPath) { $true } else { $null })
      }
    )
    $toolchainReport = [ordered]@{
      schemaVersion = 1
      generatedAt = $generatedAt
      lockFile = [string]$LockFile
      entries = $tcEntries
      hardGatesMatch = [bool]($goMatch -and $nodeMatch -and $npmMatch -and $wailsMatch)
      exitCode = $scriptExit
    }
    Write-JsonReport (Join-Path $reportDir 'toolchain.json') $toolchainReport

    # ---- ENV02: 真实宿主环境 vs 六目标最低要求 ----
    $hostOS = if ($env:OS -eq 'Windows_NT') { 'windows' } else { 'unknown' }
    $hostBuild = Get-HostBuildNumber
    $hostOSVersion = $null
    $buildNum = 0
    if ($hostBuild -and [int]::TryParse([string]$hostBuild, [ref]$buildNum)) {
      $hostOSVersion = if ([int]$hostBuild -ge 22000) { '11' } else { '10' }
    }
    $hostArch = $null
    try {
      $ri = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
      $hostArch = switch ($ri) { 'X64' { 'amd64' } 'Arm64' { 'arm64' } 'X86' { '386' } default { $null } }
    } catch { $hostArch = $null }
    if (-not $hostArch) {
      $hostArch = switch ($env:PROCESSOR_ARCHITECTURE) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } 'x86' { '386' } default { $null } }
    }

    $platformsPath = Join-Path $repoRoot 'build\platforms.json'
    $targetsWithEvidence = [System.Collections.Generic.List[string]]::new()
    $targetsMissing = [System.Collections.Generic.List[string]]::new()
    $hostMatchesOnly = [System.Collections.Generic.List[string]]::new()
    $targetRows = [System.Collections.Generic.List[object]]::new()
    $platExp = $null
    if (Test-Path -LiteralPath $platformsPath) {
      try { $platExp = Get-Content $platformsPath -Raw -Encoding UTF8 | ConvertFrom-Json } catch { $platExp = $null }
    }
    if ($platExp -and $platExp.targets) {
      foreach ($t in $platExp.targets) {
        $osMatch = ([string]$t.goos -eq $hostOS)
        $archMatch = ([string]$t.goarch -eq $hostArch)
        $hostMatches = [bool]($osMatch -and $archMatch)
        if ($hostMatches) { $hostMatchesOnly.Add([string]$t.targetId) }
        $meetsMin = $true
        $why = ''
        $minBuild = 0
        if ($t.minOSBuild) { $minBuild = [int]$t.minOSBuild }
        if ($minBuild -gt 0) {
          if (-not $hostBuild) { $meetsMin = $false; $why = 'host OS build unknown' }
          elseif ([int]$hostBuild -lt $minBuild) {
            $meetsMin = $false
            $why = "host OS build $hostBuild below target minimum $minBuild"
          }
        }
        if ($meetsMin) {
          $cmp = Compare-VersionParts $hostOSVersion ([string]$t.minOSVersion)
          if ($null -eq $cmp) { $meetsMin = $false; $why = 'host/target OS version unparseable' }
          elseif ($cmp -lt 0) {
            $meetsMin = $false
            $why = "host OS version $hostOSVersion below target minimum $($t.minOSVersion)"
          }
        }
        if ($hostMatches -and $meetsMin) { $targetsWithEvidence.Add([string]$t.targetId) }
        else { $targetsMissing.Add([string]$t.targetId) }
        $targetRows.Add([ordered]@{
          targetId = [string]$t.targetId; goos = [string]$t.goos; goarch = [string]$t.goarch
          minOSVersion = [string]$t.minOSVersion; minOSBuild = $minBuild
          hostMatches = $hostMatches; meetsMinimum = [bool]$meetsMin
          belowMinimumReason = $why
        })
      }
    }
    $hostReport = [ordered]@{
      schemaVersion = 1
      generatedAt = $generatedAt
      hostOS = $hostOS
      hostOSVersion = $hostOSVersion
      hostBuild = $hostBuild
      hostArch = $hostArch
      platformsFile = [string]$platformsPath
      platformsReadable = [bool]($null -ne $platExp)
      targets = $targetRows
      targetsWithEvidence = $targetsWithEvidence.ToArray()
      targetsMissing = $targetsMissing.ToArray()
      hostMatchesOnly = $hostMatchesOnly.ToArray()
      note = 'ENV02 是 ALL scope：单台机器不可能覆盖六个目标。这里只记录本机真实可达的目标，不声称六目标。'
    }
    Write-JsonReport (Join-Path $reportDir 'host-environment.json') $hostReport

    # ---- ENV03: 锁文件哈希（从磁盘读） ----
    $lockFiles = @(
      [ordered]@{ name = 'frontendLock'; path = (Join-Path $repoRoot 'frontend\package-lock.json') },
      [ordered]@{ name = 'goSum'; path = (Join-Path $repoRoot 'go.sum') },
      [ordered]@{ name = 'versionsLock'; path = [string]$LockFile }
    )
    $lockHashes = [ordered]@{}
    $lockRows = [System.Collections.Generic.List[object]]::new()
    foreach ($lf in $lockFiles) {
      $exists = [bool]($lf.path -and (Test-Path -LiteralPath $lf.path))
      $hash = if ($exists) { Get-FileSha256 $lf.path } else { $null }
      $size = $null
      if ($exists) { try { $size = (Get-Item -LiteralPath $lf.path).Length } catch { $size = $null } }
      $lockHashes[$lf.name] = $hash
      $lockRows.Add([ordered]@{ name = $lf.name; path = [string]$lf.path; exists = $exists; bytes = $size; sha256 = $hash })
    }
    $lockReport = [ordered]@{
      schemaVersion = 1
      generatedAt = $generatedAt
      lockHashes = $lockHashes
      files = $lockRows
    }
    Write-JsonReport (Join-Path $reportDir 'lock-hashes.json') $lockReport

    # ---- observation.json：只引用上面三份文件（附哈希），不重复实现判定 ----
    $observation = [ordered]@{
      schemaVersion = 1
      generatedAt = $generatedAt
      toolchain = $toolchainReport
      host = $hostReport
      lockHashes = $lockReport
      artifacts = @(
        [ordered]@{ file = 'toolchain.json'; sha256 = (Get-FileSha256 (Join-Path $reportDir 'toolchain.json')) },
        [ordered]@{ file = 'host-environment.json'; sha256 = (Get-FileSha256 (Join-Path $reportDir 'host-environment.json')) },
        [ordered]@{ file = 'lock-hashes.json'; sha256 = (Get-FileSha256 (Join-Path $reportDir 'lock-hashes.json')) }
      )
    }
    Write-JsonReport (Join-Path $reportDir 'observation.json') $observation
  } catch {
    # 写报告失败不能改变退出码（门禁口径不变），但也不能静默：
    # 没写出来时 case 会以「未产出 observation.json」失败，这里把原因留在 stderr。
    [Console]::Error.WriteLine("observation report failed: $_")
  }
}

Write-Output "--- summary: $($errors.Count) failure(s), $($warnings.Count) warning(s) ---"
foreach ($e in $errors) { Write-Output "  FAIL: $e" }

if ($errors.Count -eq 0) { exit 0 } else { exit 2 }
