package diagnostics

// ART03 / SEC07.diagnostics 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写、artifact 覆盖写；未设置时不写任何文件。
//
// 事实从两侧读：
//   - diagnostics-dump.json：第二个 storage.DB 句柄读 run_usage / run_events 真实行
//     （availability 与 NULL 数字是库里那一份，"unknown 没被写成 0"由库回答）；
//   - diagnostics-bundle.json：导出目录的真实遍历 + 对每个文件重新扫描 sentinel
//     得到的命中列表（"命中 0"与"定位信息还在"都由磁盘字节回答）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"my-agent-go/internal/storage"
)

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

type usageRow struct {
	RunID        string   `json:"runId"`
	SessionID    string   `json:"sessionId"`
	State        string   `json:"state"`
	Availability string   `json:"availability"`
	InputTokens  *int64   `json:"inputTokens"`
	OutputTokens *int64   `json:"outputTokens"`
	TotalTokens  *int64   `json:"totalTokens"`
	CostUSD      *float64 `json:"costUsd"`
	PriceSource  string   `json:"priceSource"`
	Estimated    int64    `json:"estimated"`
}

type eventRow struct {
	RunID     string `json:"runId"`
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Truncated int64  `json:"payloadTruncated"`
}

type diagnosticsDump struct {
	Usage  []usageRow `json:"runUsage"`
	Events []eventRow `json:"runEvents"`
	Errors []string   `json:"errors,omitempty"`
}

// dumpDiagnosticsFile 用第二个句柄读库里的诊断行。
func dumpDiagnosticsFile(t *testing.T, path string) *diagnosticsDump {
	t.Helper()
	dump := &diagnosticsDump{Usage: []usageRow{}, Events: []eventRow{}}
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows, qerr := db.SQL().QueryContext(ctx, `
		SELECT run_id, session_id, state, usage_availability,
		       input_tokens, output_tokens, total_tokens, cost_usd,
		       COALESCE(price_source, ''), estimated
		FROM run_usage ORDER BY run_id`)
	if qerr != nil {
		dump.Errors = append(dump.Errors, "run_usage: "+qerr.Error())
	} else {
		for rows.Next() {
			var r usageRow
			if err := rows.Scan(&r.RunID, &r.SessionID, &r.State, &r.Availability,
				&r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.CostUSD, &r.PriceSource, &r.Estimated); err != nil {
				t.Fatalf("扫描 run_usage 行失败: %v", err)
			}
			dump.Usage = append(dump.Usage, r)
		}
		_ = rows.Close()
	}

	erows, eerr := db.SQL().QueryContext(ctx,
		`SELECT run_id, seq, type, payload_truncated FROM run_events ORDER BY run_id, seq`)
	if eerr != nil {
		dump.Errors = append(dump.Errors, "run_events: "+eerr.Error())
	} else {
		for erows.Next() {
			var r eventRow
			if err := erows.Scan(&r.RunID, &r.Seq, &r.Type, &r.Truncated); err != nil {
				t.Fatalf("扫描 run_events 行失败: %v", err)
			}
			dump.Events = append(dump.Events, r)
		}
		_ = erows.Close()
	}
	return dump
}

type bundleFile struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Sha256 string `json:"sha256"`
}

// observeDiagnostics 把库里的 usage/事件行与导出目录的磁盘事实写进观察目录。
func observeDiagnostics(t *testing.T, dbPath, bundleRoot string, sentinels []string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	dump := dumpDiagnosticsFile(t, dbPath)
	dumpJSON, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		t.Fatalf("序列化诊断库快照失败: %v", err)
	}

	files := []bundleFile{}
	hits := []string{}
	if _, statErr := os.Stat(bundleRoot); statErr == nil {
		walkErr := filepath.Walk(bundleRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(bundleRoot, path)
			if rerr != nil {
				rel = path
			}
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			sum := sha256.Sum256(body)
			files = append(files, bundleFile{
				Path:   filepath.ToSlash(rel),
				Bytes:  len(body),
				Sha256: hex.EncodeToString(sum[:]),
			})
			text := string(body)
			for _, s := range sentinels {
				if s != "" && strings.Contains(text, s) {
					hits = append(hits, filepath.ToSlash(rel)+" contains sentinel")
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("遍历诊断根失败: %v", walkErr)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	// 一条 case 的多条测试各自有临时库/临时导出根：artifact 因此是**按测试追加**的
	// 数组，后写者不会把前一条测试的 bundle 覆盖成空目录；顶层事实也从累积的
	// artifact 里重新并集出来，而不是只保留最后一条测试看到的那些。
	record := map[string]any{
		"test":         t.Name(),
		"dbPath":       dbPath,
		"bundleRoot":   bundleRoot,
		"usage":        dump.Usage,
		"events":       dump.Events,
		"files":        files,
		"sentinelHits": hits,
	}
	prior := readDiagnosticsRecords(t)
	allUsage, allEvents, allFiles, allHits := unionDiagnostics(prior, record)
	bundleJSON := appendDiagnosticsRecord(t, "diagnostics-bundle.json", record)

	facts := map[string]any{
		"runUsageRows":  allUsage,
		"runEventRows":  allEvents,
		"bundleFiles":   allFiles,
		"sentinelHits":  allHits,
		"diagnosticsDB": dbPath,
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeDiagnosticsObservation(t, facts, map[string][]byte{
		"diagnostics-dump.json":   dumpJSON,
		"diagnostics-bundle.json": bundleJSON,
	})
}

// readDiagnosticsRecords 读出已累积的 artifact 记录（上一轮已由 runner 清空）。
func readDiagnosticsRecords(t *testing.T) []map[string]json.RawMessage {
	t.Helper()
	dir := observationDir()
	out := []map[string]json.RawMessage{}
	if dir == "" {
		return out
	}
	raw, err := os.ReadFile(filepath.Join(dir, "diagnostics-bundle.json"))
	if err != nil {
		return out
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return out
	}
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("已有 diagnostics-bundle.json 不是合法 JSON 数组: %v", uerr)
	}
	return out
}

// unionDiagnostics 把当前记录并入已有记录，按稳定键去重后返回顶层事实。
func unionDiagnostics(prior []map[string]json.RawMessage, current map[string]any) ([]usageRow, []eventRow, []bundleFile, []string) {
	usageSeen := map[string]bool{}
	eventSeen := map[string]bool{}
	fileSeen := map[string]bool{}
	hitSeen := map[string]bool{}
	usage := []usageRow{}
	events := []eventRow{}
	files := []bundleFile{}
	hits := []string{}

	add := func(rec map[string]json.RawMessage) {
		if raw, ok := rec["usage"]; ok {
			var rows []usageRow
			if json.Unmarshal(raw, &rows) == nil {
				for _, r := range rows {
					key := r.RunID + "|" + r.Availability + "|" + r.State
					if !usageSeen[key] {
						usageSeen[key] = true
						usage = append(usage, r)
					}
				}
			}
		}
		if raw, ok := rec["events"]; ok {
			var rows []eventRow
			if json.Unmarshal(raw, &rows) == nil {
				for _, r := range rows {
					key := r.RunID + "|" + strconv.FormatInt(r.Seq, 10)
					if !eventSeen[key] {
						eventSeen[key] = true
						events = append(events, r)
					}
				}
			}
		}
		if raw, ok := rec["files"]; ok {
			var rows []bundleFile
			if json.Unmarshal(raw, &rows) == nil {
				for _, f := range rows {
					if !fileSeen[f.Path] {
						fileSeen[f.Path] = true
						files = append(files, f)
					}
				}
			}
		}
		if raw, ok := rec["sentinelHits"]; ok {
			var list []string
			if json.Unmarshal(raw, &list) == nil {
				for _, v := range list {
					if !hitSeen[v] {
						hitSeen[v] = true
						hits = append(hits, v)
					}
				}
			}
		}
	}

	for _, rec := range prior {
		add(rec)
	}
	body, err := json.Marshal(current)
	if err == nil {
		var rec map[string]json.RawMessage
		if json.Unmarshal(body, &rec) == nil {
			add(rec)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(hits)
	return usage, events, files, hits
}

// appendDiagnosticsRecord 把本条测试的记录追加进已有 artifact 数组。
func appendDiagnosticsRecord(t *testing.T, artifact string, record map[string]any) []byte {
	t.Helper()
	dir := observationDir()
	records := []json.RawMessage{}
	if dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, artifact)); err == nil {
			if uerr := json.Unmarshal(raw, &records); uerr != nil {
				t.Fatalf("已有 %s 不是合法 JSON 数组: %v", artifact, uerr)
			}
		}
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化 %s 记录失败: %v", artifact, err)
	}
	records = append(records, json.RawMessage(body))
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", artifact, err)
	}
	return data
}

// writeDiagnosticsObservation 合并写 observation.json，并覆盖写 artifact。
func writeDiagnosticsObservation(t *testing.T, facts map[string]any, artifacts map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	for name, content := range artifacts {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}
	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", err)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	self := t.Name()
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == self {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, self)
	}
	merged["tests"] = tests
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}
