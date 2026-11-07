package resources

// MOD02/MOD03 与 MCP01/MCP02/MCP03/MCP05 的外部观察值输出。
//
// 与 observation_test.go 同一契约：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写（不清空：一条 case 里多个测试各是一次真实观察），
// artifact 覆盖写，没设置该变量（本地 focused 运行）时什么也不写。
//
// 观察装置必须是实现边界之外的东西：
//   - MOD：providerSim 是进程内 httptest server，但它扮演的是**网络对端**——
//     请求体由它自己的 ServeHTTP 读下来，不是从 Pi 的日志或我们的客户端里推测；
//   - MCP：fixture MCP server 是独立进程，它自己往 -calls / -log / -counter
//     追加记录（谁调用了什么、谁启动了它、退出没有），测试只读这些文件。
//
// 纪律：artifact 先落盘，observation.json 里的事实**再从磁盘读回来**派生。
// 直接拿测试变量写事实，等于让测试自证"我知道自己做了什么"。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// appendObservationSnapshot 把一份快照**追加**进 artifact。
//
// 追加而不是覆盖：同一个 case 里的多条测试/子测试是彼此独立的真实观察
// （例如 401 / 429 / 超时三类失败各是一份），覆盖写会让先前的证据消失。
func appendObservationSnapshot(t *testing.T, name string, snapshot map[string]any) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	merged := readResourcesArtifact(t, name)
	snaps, _ := merged["snapshots"].([]any)
	merged["snapshots"] = append(snaps, snapshot)
	writeResourcesArtifact(t, name, merged)
}

// readObservationSnapshots 从磁盘读回 artifact 里的 snapshots 数组。
func readObservationSnapshots(t *testing.T, name string) []map[string]any {
	t.Helper()
	merged := readResourcesArtifact(t, name)
	raw, _ := merged["snapshots"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// findObservationSnapshot 在以 test+label 标记的快照里找本次观察。
//
// 找不到就是合并写坏了：宁可判死，也不要拿别的测试的快照冒充本次结论。
func findObservationSnapshot(t *testing.T, name string, snaps []map[string]any, testName, label string) map[string]any {
	t.Helper()
	for _, s := range snaps {
		test, _ := s["test"].(string)
		lbl, _ := s["label"].(string)
		if test == testName && lbl == label {
			return s
		}
	}
	t.Fatalf("%s 里找不到 test=%q label=%q 的快照（已合并 %d 份）", name, testName, label, len(snaps))
	return nil
}

// recordProbeObservation 把 fixture provider 实收请求与探针结论落盘，
// 再从这两份 artifact 读回事实写 observation.json。
//
// label 区分同一条 case 里的不同探测（文本/工具/三类失败），
// 因为 TestProbeClassifiesFailures 的子测试共用同一个 t.Name() 前缀。
func recordProbeObservation(t *testing.T, sim *providerSim, endpoint string, res ProbeResult, label string) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	requests := sim.requests()
	appendObservationSnapshot(t, "probe-requests.json", map[string]any{
		"test":         t.Name(),
		"label":        label,
		"endpoint":     endpoint,
		"capturedAt":   time.Now().UTC().Format(time.RFC3339),
		"capturedBy":   "internal/resources providerSim.ServeHTTP 实际读到的请求体",
		"requestCount": len(requests),
		"requests":     requests,
	})
	appendObservationSnapshot(t, "probe-result.json", map[string]any{
		"test":       t.Name(),
		"label":      label,
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
		"capturedBy": "Prober.Probe 返回的探针结论（可达性/能力/失败分类）",
		"result":     res,
	})

	// —— 以下事实全部由两份 artifact 的磁盘内容派生 ——
	reqSnaps := readObservationSnapshots(t, "probe-requests.json")
	resSnaps := readObservationSnapshots(t, "probe-result.json")
	reqSnap := findObservationSnapshot(t, "probe-requests.json", reqSnaps, t.Name(), label)
	resSnap := findObservationSnapshot(t, "probe-result.json", resSnaps, t.Name(), label)

	reqs, _ := reqSnap["requests"].([]any)
	bodies := make([]string, 0, len(reqs))
	for _, r := range reqs {
		if s, ok := r.(string); ok {
			bodies = append(bodies, s)
		}
	}
	result, _ := resSnap["result"].(map[string]any)
	reachable, _ := result["reachable"].(bool)
	toolCalling, _ := result["toolCalling"].(string)
	vision, _ := result["vision"].(string)
	latency, _ := result["latencyMs"].(float64)
	errCode, errRetryable := "", false
	if em, ok := result["error"].(map[string]any); ok {
		errCode, _ = em["code"].(string)
		errRetryable, _ = em["retryable"].(bool)
	}

	// 请求体里的工具声明只能从落盘的字节里数，不能靠测试记忆。
	firstHasTools, firstToolCount := false, 0
	if len(bodies) > 0 {
		var first map[string]any
		if err := json.Unmarshal([]byte(bodies[0]), &first); err == nil {
			if tools, ok := first["tools"].([]any); ok {
				firstToolCount = len(tools)
				firstHasTools = firstToolCount > 0
			}
		}
	}
	lastDeclaresRead := len(bodies) > 0 && strings.Contains(bodies[len(bodies)-1], `"read"`)

	totalRequests := 0
	labels := []any{}
	for _, s := range reqSnaps {
		if n, ok := s["requestCount"].(float64); ok {
			totalRequests += int(n)
		}
		if l, ok := s["label"].(string); ok {
			labels = append(labels, l)
		}
	}
	observedEndpoint, _ := reqSnap["endpoint"].(string)

	facts := map[string]any{
		"probeRequestSnapshots": len(reqSnaps),
		"probeResultSnapshots":  len(resSnaps),
		"probeRequests":         totalRequests,
		"probeLabels":           unionResourcesListFact(t, "probeLabels", labels),
	}
	if errCode != "" {
		facts["probeErrorCodes"] = unionResourcesListFact(t, "probeErrorCodes", []any{errCode})
	}
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"endpoint":                observedEndpoint,
		"requestCount":            len(bodies),
		"reachable":               reachable,
		"toolCalling":             toolCalling,
		"vision":                  vision,
		"latencyMs":               int64(latency),
		"errorCode":               errCode,
		"errorRetryable":          errRetryable,
		"firstRequestHasTools":    firstHasTools,
		"firstRequestToolCount":   firstToolCount,
		"lastRequestDeclaresRead": lastDeclaresRead,
	}
	writeResourcesObservation(t, facts, nil)
}

// recordMCPFixtureObservation 记录 fixture MCP server **自己写下的**文件：
//
//	callsPath    每次真实工具调用一行（副作用计数）
//	tracePath    它收到的 JSON-RPC 请求（stdio 模式，-log）
//	counterPath  进程启动/退出与 ppid（判断是 Pi 起它还是 Go 起了它）
//
// 路径为空表示该装置这一次没有启用（例如 HTTP 模式没有 counter），
// 那就如实记"没有这个文件"，不编一行。
func recordMCPFixtureObservation(t *testing.T, label, callsPath, tracePath, counterPath string, extra map[string]any) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	snapshot := map[string]any{
		"test":       t.Name(),
		"label":      label,
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
		"capturedBy": "tests/fixtures/mcp 进程自己写下的 -calls/-log/-counter 文件",
		"files":      map[string]any{"calls": callsPath, "trace": tracePath, "counter": counterPath},
		"calls":      []string{},
		"requests":   []string{},
		"process":    map[string]any{"lines": []string{}, "starts": 0, "exits": 0, "parentPIDs": []string{}},
	}
	if callsPath != "" {
		snapshot["calls"] = readLines(t, callsPath)
	}
	if tracePath != "" {
		snapshot["requests"] = readLines(t, tracePath)
	}
	if counterPath != "" {
		lines := readLines(t, counterPath)
		starts, exits, ppids := countStarts(lines)
		snapshot["process"] = map[string]any{"lines": lines, "starts": starts, "exits": exits, "parentPIDs": ppids}
	}
	for k, v := range extra {
		snapshot[k] = v
	}
	appendObservationSnapshot(t, "mcp-server-log.json", snapshot)

	// —— 事实从落盘文件读回 ——
	snaps := readObservationSnapshots(t, "mcp-server-log.json")
	mine := findObservationSnapshot(t, "mcp-server-log.json", snaps, t.Name(), label)
	calls, _ := mine["calls"].([]any)
	requests, _ := mine["requests"].([]any)
	process, _ := mine["process"].(map[string]any)
	starts, _ := process["starts"].(float64)
	exits, _ := process["exits"].(float64)
	ppids, _ := process["parentPIDs"].([]any)

	totalCalls := 0
	labels := []any{}
	for _, s := range snaps {
		if cs, ok := s["calls"].([]any); ok {
			totalCalls += len(cs)
		}
		if l, ok := s["label"].(string); ok {
			labels = append(labels, l)
		}
	}
	ppidStrings := make([]any, 0, len(ppids))
	for _, p := range ppids {
		if s, ok := p.(string); ok {
			ppidStrings = append(ppidStrings, s)
		}
	}
	facts := map[string]any{
		"mcpFixtureSnapshots": len(snaps),
		"mcpFixtureCallRows":  totalCalls,
		"mcpFixtureLabels":    unionResourcesListFact(t, "mcpFixtureLabels", labels),
	}
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"callLines":     len(calls),
		"requestLines":  len(requests),
		"processStarts": int(starts),
		"processExits":  int(exits),
		"parentPIDs":    ppidStrings,
	}
	writeResourcesObservation(t, facts, nil)
}

// recordMCPDiagnoseObservation 记录一次 MCP 诊断的结论。
//
// 诊断本身是 `pi mcp list --json` 的真实输出经产品映射后的状态，
// 所以这份 JSON 记录的是"真实 Pi 观察到的状态 + 产品判定"，而不是界面文案。
func recordMCPDiagnoseObservation(t *testing.T, label string, st MCPStatus) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	errCode, errMessage, errRetryable := "", "", false
	if st.Error != nil {
		errCode, errMessage, errRetryable = st.Error.Code, st.Error.Message, st.Error.Retryable
	}
	appendObservationSnapshot(t, "mcp-diagnose.json", map[string]any{
		"test":           t.Name(),
		"label":          label,
		"capturedAt":     time.Now().UTC().Format(time.RFC3339),
		"capturedBy":     "Service.DiagnoseMCP（pi mcp list --json 的真实输出映射）",
		"id":             st.ID,
		"state":          st.State,
		"transport":      st.Transport,
		"enabled":        st.Enabled,
		"exposure":       st.Exposure,
		"source":         st.Source,
		"oauthSupported": st.OAuthSupported,
		"tools":          toolNames(st.Tools),
		"errorCode":      errCode,
		"errorMessage":   errMessage,
		"errorRetryable": errRetryable,
		"detail":         st.Detail,
	})

	// —— 事实从落盘的诊断记录读回 ——
	snaps := readObservationSnapshots(t, "mcp-diagnose.json")
	mine := findObservationSnapshot(t, "mcp-diagnose.json", snaps, t.Name(), label)
	state, _ := mine["state"].(string)
	tools, _ := mine["tools"].([]any)
	oauth, _ := mine["oauthSupported"].(bool)
	code, _ := mine["errorCode"].(string)
	toolStrings := make([]any, 0, len(tools))
	for _, tool := range tools {
		if s, ok := tool.(string); ok {
			toolStrings = append(toolStrings, s)
		}
	}

	states := []any{}
	for _, s := range snaps {
		if v, ok := s["state"].(string); ok {
			states = append(states, v)
		}
	}
	facts := map[string]any{
		"mcpDiagnoseSnapshots": len(snaps),
		"mcpDiagnoseStates":    unionResourcesListFact(t, "mcpDiagnoseStates", states),
	}
	if code != "" {
		facts["mcpDiagnoseErrorCodes"] = unionResourcesListFact(t, "mcpDiagnoseErrorCodes", []any{code})
	}
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"state":          state,
		"tools":          toolStrings,
		"toolCount":      len(toolStrings),
		"oauthSupported": oauth,
		"errorCode":      code,
	}
	writeResourcesObservation(t, facts, nil)
}
