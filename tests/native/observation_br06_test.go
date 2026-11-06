//go:build windows

package nativetest

// BR06 的外部观察：50 轮创建/销毁的句柄、内存与浏览器子进程。
//
// 观察源：
//   - handles.jsonl：每轮一行（GetProcessHandleCount / GetProcessMemoryInfo 的直读值、
//     子窗口是否还在、窗口类实例数、采样轮的浏览器子进程 PID）；
//   - process-table-before/during/after.json：由**另一个 PowerShell 进程**执行
//     Get-CimInstance Win32_Process 写出的 OS 进程表（只筛本用例 profile 的
//     msedgewebview2.exe）。
//
// 事实（rounds / handlesFirst / handlesLast / handlesMax / childPIDsBefore /
// childPIDsAfter / workingSetDeltaBytes）全部从这些文件算出。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// processTableEntry 是 OS 进程表里的一条（由 PowerShell 写出）。
type processTableEntry struct {
	Pid  int     `json:"pid"`
	Ppid int     `json:"ppid"`
	Name string  `json:"name"`
	WSMB float64 `json:"wsMB"`
}

// processTableFile 是 process-table-*.json 的结构。
type processTableFile struct {
	CapturedAt   string              `json:"capturedAt"`
	Phase        string              `json:"phase"`
	Filter       string              `json:"filter"`
	SelfPid      int                 `json:"selfPid"`
	SelfHandles  int                 `json:"selfHandles"`
	ProcessCount int                 `json:"processCount"`
	Processes    []processTableEntry `json:"processes"`
	Error        string              `json:"error,omitempty"`
}

// captureProcessTable 让一个**独立的 PowerShell 进程**把 OS 进程表写进 artifact。
//
// 查询走 Get-CimInstance（本机 wmic 被安全策略拦住，AGENTS.md 已记录），
// 只筛命令行里带 filter 的 msedgewebview2.exe：机器上别的应用的 WebView2 不数。
// phase="before" 只写一次（case 的第一条测试决定起点），"during"/"after" 覆盖写
// （最后一个写下的就是 case 的最终状态）。
func captureProcessTable(t *testing.T, phase, filter string) processTableFile {
	t.Helper()
	name := "process-table-" + phase + ".json"
	if !observationEnabled() {
		return processTableFile{}
	}
	if phase == "before" {
		if _, err := os.Stat(observationPath(name)); err == nil {
			var existing processTableFile
			observationReadJSON(t, name, &existing)
			return existing
		}
	}
	if phase == "after" {
		// WebView2 的子进程是异步退出的：controller Close 之后到进程表里真的消失
		// 之间有一段窗口。这里先按文档口径等这段时间，再抓表——否则抓到的
		// "还有 3 个进程" 只是抓早了，不是没回收（实测 2026-11-06 出现过）。
		if waited := waitForNoWebView2Processes(t, filter, webView2ReapWindow); waited > 0 {
			t.Logf("process-table-after：等待 %s 后浏览器子进程才全部退出", waited.Round(time.Millisecond))
		}
	}
	path := observationPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
try {
  $p = @(Get-CimInstance Win32_Process -Filter "Name='msedgewebview2.exe'" | Where-Object { $_.CommandLine -like '*%s*' })
  $rows = @($p | ForEach-Object { [pscustomobject]@{ pid = [int]$_.ProcessId; ppid = [int]$_.ParentProcessId; name = $_.Name; wsMB = [math]::Round($_.WorkingSetSize/1MB, 1) } })
  $self = Get-Process -Id $PID
  $doc = [pscustomobject]@{
    capturedAt = (Get-Date).ToUniversalTime().ToString('o')
    phase = %q
    filter = %q
    selfPid = %d
    selfHandles = [int]$self.HandleCount
    processCount = $rows.Count
    processes = $rows
  }
} catch {
  $doc = [pscustomobject]@{ capturedAt = (Get-Date).ToUniversalTime().ToString('o'); phase = %q; filter = %q; selfPid = %d; selfHandles = 0; processCount = 0; processes = @(); error = $_.Exception.Message }
}
$json = $doc | ConvertTo-Json -Depth 6
[IO.File]::WriteAllText(%q, $json, (New-Object Text.UTF8Encoding $false))`,
		filter, phase, filter, os.Getpid(), phase, filter, os.Getpid(), path)
	code, out := runPowerShellToFile(t, script)
	if code != 0 {
		t.Fatalf("独立 PowerShell 进程写进程表 %s 失败（exit=%d）: %s", name, code, truncate(out, 300))
	}
	var table processTableFile
	observationReadJSON(t, name, &table)
	if table.Error != "" {
		t.Fatalf("进程表 %s 记录到错误: %s", name, table.Error)
	}
	return table
}

// webView2ReapWindow 是"controller 关掉后子进程退出"的宽限窗口。
//
// 取 10s 而不是 1s：退出延迟取决于机器负载（50 轮 soak 时进程销毁更慢），
// 宽限太短会把"正在退出"记成"没回收"。超出窗口仍未退出才算真泄漏。
const webView2ReapWindow = 10 * time.Second

// waitForNoWebView2Processes 轮询到没有带该 filter 的 msedgewebview2 进程，最多等 limit。
//
// 返回实际等待时长；limit 内没清空就返回 limit（调用方照常抓表并让断言失败）。
func waitForNoWebView2Processes(t *testing.T, filter string, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		n, err := CountWebView2Processes(filter)
		if err == nil && n.Count == 0 {
			return time.Since(start)
		}
		if time.Since(start) >= limit {
			return limit
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// pidsOf 取进程表里的 PID 列表（保持进程表顺序）。
func pidsOf(entries []processTableEntry) []any {
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Pid)
	}
	return out
}

// observeCreateDestroyRound 往 handles.jsonl 追加一轮的句柄/内存读数。
//
// 句柄数来自 GetProcessHandleCount（OS），working set 来自 GetProcessMemoryInfo
// （OS），子窗口/窗口类实例来自 IsWindow/GetWindow 遍历。全部落盘后再读回。
func observeCreateDestroyRound(t *testing.T, testName string, round int, mem ProcessMemory,
	childHWND uintptr, childExists bool, childClassInstances int, extra obsFacts) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	rec := obsFacts{
		"test":                testName,
		"round":               round,
		"handleCount":         mem.HandleCount,
		"workingSetBytes":     mem.WorkingSetBytes,
		"peakWorkingSetBytes": mem.PeakWorkingSet,
		"pagefileBytes":       mem.PagefileBytes,
		"childHWND":           childHWND,
		"childWindowExists":   childExists,
		"childClassInstances": childClassInstances,
		"recordedAt":          time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		rec[k] = v
	}
	observationAppendJSONL(t, "handles.jsonl", rec)
}

// finalizeCreateDestroyObservation 从 handles.jsonl + process-table-*.json 读回事实。
//
// 同一个 case 的两条测试都会调用：都从**整份文件**重算，所以最后跑的那条写下的
// 就是完整总量（不依赖测试执行顺序）。
func finalizeCreateDestroyObservation(t *testing.T) {
	t.Helper()
	if !observationEnabled() {
		return
	}
	lines := observationReadJSONL(t, "handles.jsonl")
	roundsByTest := map[string]int{}
	minHandles, maxHandles := -1, 0
	firstHandles, lastHandles := 0, 0
	firstWS, lastWS := 0.0, 0.0
	childClassLeftovers := 0
	for i, rec := range lines {
		name := rec.text(t, "test")
		roundsByTest[name]++
		handles := int(rec.num(t, "handleCount"))
		ws := rec.num(t, "workingSetBytes")
		if minHandles < 0 || handles < minHandles {
			minHandles = handles
		}
		if handles > maxHandles {
			maxHandles = handles
		}
		if i == 0 {
			firstHandles, firstWS = handles, ws
		}
		lastHandles, lastWS = handles, ws
		if rec.num(t, "childClassInstances") != 0 || rec.flag(t, "childWindowExists") {
			childClassLeftovers++
		}
	}

	before := processTableFile{}
	observationReadJSON(t, "process-table-before.json", &before)
	after := processTableFile{}
	observationReadJSON(t, "process-table-after.json", &after)
	during := processTableFile{}
	if _, err := os.Stat(observationPath("process-table-during.json")); err == nil {
		observationReadJSON(t, "process-table-during.json", &during)
	}

	if len(lines) < 20 {
		t.Fatalf("handles.jsonl 只有 %d 行，期望至少 20 轮", len(lines))
	}
	if roundsByTest["TestNativeCreateDestroy50Rounds"] < 50 {
		t.Fatalf("handles.jsonl 里 50 轮用例只记录了 %d 轮，BR06 要求 50 轮", roundsByTest["TestNativeCreateDestroy50Rounds"])
	}
	if childClassLeftovers != 0 {
		t.Fatalf("handles.jsonl 里有 %d 轮留下子窗口/窗口类实例", childClassLeftovers)
	}
	if before.ProcessCount != 0 {
		t.Fatalf("process-table-before.json 里已经有 %d 个带本 profile 的 msedgewebview2 进程（起点不干净）", before.ProcessCount)
	}
	if after.ProcessCount != 0 {
		t.Fatalf("process-table-after.json 里还有 %d 个带本 profile 的 msedgewebview2 进程（没回收）", after.ProcessCount)
	}
	if during.ProcessCount == 0 {
		t.Fatal("process-table-during.json 里没有浏览器子进程：整条 case 从没真的起过 WebView2")
	}
	if during.SelfPid != os.Getpid() {
		t.Fatalf("process-table-during.json 的 selfPid=%d，与本进程 %d 不一致（进程表不是这次跑出来的）", during.SelfPid, os.Getpid())
	}

	observationMergeFacts(t, obsFacts{
		"rounds":                  len(lines),
		"roundsByTest":            roundsByTest,
		"handlesFirst":            firstHandles,
		"handlesLast":             lastHandles,
		"handlesMin":              minHandles,
		"handlesMax":              maxHandles,
		"workingSetFirstBytes":    firstWS,
		"workingSetLastBytes":     lastWS,
		"workingSetDeltaBytes":    lastWS - firstWS,
		"childPIDsBefore":         pidsOf(before.Processes),
		"childPIDsAfter":          pidsOf(after.Processes),
		"childPIDsDuring":         pidsOf(during.Processes),
		"childProcessCountDuring": during.ProcessCount,
		"handlesArtifact":         "handles.jsonl",
		"processTableArtifacts":   []string{"process-table-before.json", "process-table-during.json", "process-table-after.json"},
		"createDestroyObservedBy": "handles.jsonl 每轮的句柄数/working set 来自 GetProcessHandleCount / " +
			"GetProcessMemoryInfo（OS），子窗口状态来自 IsWindow + 窗口类遍历；process-table-*.json 由一个独立的 " +
			"PowerShell 进程用 Get-CimInstance Win32_Process 写出",
	})
}
