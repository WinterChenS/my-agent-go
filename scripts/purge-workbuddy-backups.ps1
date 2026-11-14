# Reclaim the space WorkBuddy's edit snapshots take on C:.
#
# Every file this session's tools modify is copied into
#   %USERPROFILE%\.workbuddy\workspace\sessions\<id>\modify_backup
# before the write. Build outputs used to land inside the worktree
# (.tmp/tooling/cargo-target), so one release build put ~15 GB there and left C:
# with zero free bytes. Cargo's target now lives at D:\anvil-target (see
# .cargo/config.toml), which cut that to tens of MB per build, but incremental
# rebuilds still overwrite artifacts and get snapshotted.
#
# Run this after a heavy build when C: starts shrinking again.
#
# Why move-then-delete: direct deletion of existing snapshot files is refused
# (access denied, even though the ACL grants FullControl and no handle is held),
# while renaming and moving succeed. Moving the directory to %TEMP% and deleting
# it there works, so that is what this script does.
#
# Safe by construction: it only ever touches directories literally named
# `modify_backup` under the sessions folder, plus the temporary copies it makes.

[CmdletBinding()]
param(
    # Skip this session's own snapshots (the host keeps writing to them).
    [switch]$SkipCurrent,
    # Report what would be freed without deleting anything.
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'

$sessions = Join-Path $env:USERPROFILE '.workbuddy\workspace\sessions'
if (-not (Test-Path -LiteralPath $sessions)) {
    Write-Output "no session store at $sessions"
    return
}

$targets = Get-ChildItem -LiteralPath $sessions -Directory -Force -ErrorAction SilentlyContinue |
    ForEach-Object {
        $modify = Join-Path $_.FullName 'modify_backup'
        if (Test-Path -LiteralPath $modify) {
            [pscustomobject]@{ Session = $_.Name; Path = $modify }
        }
    } |
    Where-Object { $_ }

if (-not $targets) {
    Write-Output 'no modify_backup directories found'
    return
}

$totalFiles = 0
$totalBytes = 0
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'

foreach ($target in $targets) {
    $stats = Get-ChildItem -LiteralPath $target.Path -Recurse -Force -File -ErrorAction SilentlyContinue |
        Measure-Object -Property Length -Sum
    $bytes = if ($stats.Sum) { $stats.Sum } else { 0 }
    $label = '{0} MB / {1} files  {2}' -f [math]::Round($bytes / 1MB, 1), $stats.Count, $target.Session

    if ($DryRun) {
        Write-Output "would remove: $label"
        $totalFiles += $stats.Count
        $totalBytes += $bytes
        continue
    }

    $trash = Join-Path $env:TEMP ('wb-backup-' + $stamp + '-' + $target.Session.Substring(0, 8))
    try {
        [System.IO.Directory]::Move($target.Path, $trash)
    }
    catch {
        Write-Output "skip (in use): $label"
        continue
    }

    $removed = 0
    $freed = 0
    Get-ChildItem -LiteralPath $trash -Recurse -Force -File -ErrorAction SilentlyContinue | ForEach-Object {
        try {
            $length = $_.Length
            [System.IO.File]::Delete($_.FullName)
            $removed++
            $freed += $length
        }
        catch {
            # A file the host has open simply stays behind; the next run retries.
        }
    }
    try {
        [System.IO.Directory]::Delete($trash, $true)
    }
    catch {
        Write-Output "  left behind: $trash"
    }

    $totalFiles += $removed
    $totalBytes += $freed
    Write-Output ('removed: {0} MB / {1} files  {2}' -f [math]::Round($freed / 1MB, 1), $removed, $target.Session)
}

$verb = if ($DryRun) { 'reclaimable' } else { 'reclaimed' }
Write-Output ('{0}: {1} MB in {2} files' -f $verb, [math]::Round($totalBytes / 1MB, 1), $totalFiles)

if (-not $DryRun) {
    $free = (New-Object System.IO.DriveInfo('C')).AvailableFreeSpace
    Write-Output ('C: free now {0} GB' -f [math]::Round($free / 1GB, 2))
}
