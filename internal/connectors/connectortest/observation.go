// Package connectortest 的观察值输出（C08 的 external-observation 约定）。
//
// 为什么放在这里而不是各自的 _test.go：五个平台驱动分属五个包，观察输出必须
// 走同一份实现，否则每个包各写一份"合并写 / 追加写"就迟早出现第三种口径。
// 本包只被 _test.go 引用，不参与产品构建。
//
// 事实来源（都不问被测代码"你记对了吗"）：
//   - connector-counter.json：fixture 自己原子写下的副作用快照；
//   - connector-http-requests.jsonl：fixture 收到每个请求后追加的行；
//   - audit.jsonl：连接器服务自己落盘的审计镜像（allow/deny 与 outcome）。
//
// observation.json 合并写（同 key 覆盖，累积 tests），artifact 按测试追加，
// 与 internal/memory|run|history 的契约一致。未设置 ANVIL_OBSERVATION_DIR 时
// 什么也不写。
package connectortest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"my-agent-go/internal/connectors"
	"my-agent-go/internal/connectors/fixtures"
)

// ObservationDir 返回集成 runner 传来的证据目录（未设置时为空）。
func ObservationDir() string { return strings.TrimSpace(os.Getenv(connectors.ObservationDirEnv)) }

// ObservationRecord 是单条测试的观察记录。
//
// 诚实说明：同一 case 的多条测试共用一个证据目录，fixture 的请求日志与副作用
// 快照是**整条 case 累积**的（jsonl 追加、快照覆盖）。所以这里的 Requests/Writes
// 是"到此为止的文件总量"，不是本条测试的增量；每条测试自己的观察值是 Label 与
// Extra，加上它结束时磁盘上那份快照的内容（含 mtime，便于核对是哪一次写的）。
type ObservationRecord struct {
	Test      string            `json:"test"`
	Label     string            `json:"label"`
	Counter   fixtures.Snapshot `json:"counterSnapshot"`
	CounterAt string            `json:"counterWrittenAt,omitempty"`
	Requests  int               `json:"totalRequestsSoFar"`
	Writes    int               `json:"totalWritesSoFar"`
	Extra     map[string]any    `json:"extra,omitempty"`
}

// PlatformTotals 是请求日志按平台聚合出的可核对事实。
type PlatformTotals struct {
	Platform string `json:"platform"`
	Requests int    `json:"requests"`
	Writes   int    `json:"writes"`
}

// Observe 读回本次测试结束时磁盘上的 fixture 产物，写进 ANVIL_OBSERVATION_DIR。
//
// counter 可以为 nil。所有事实都从磁盘上的文件读出来；测试进程里的计数器不参与
// 观察值。
func Observe(t *testing.T, label string, counter *fixtures.Counter, extra map[string]any) {
	t.Helper()
	dir := ObservationDir()
	if dir == "" {
		return
	}
	record := ObservationRecord{Test: t.Name(), Label: label, Extra: extra}

	if info, err := os.Stat(filepath.Join(dir, connectors.ObservationCounterFile)); err == nil {
		record.CounterAt = info.ModTime().UTC().Format("2006-01-02T15:04:05.000000000Z")
		if data, rerr := os.ReadFile(filepath.Join(dir, connectors.ObservationCounterFile)); rerr == nil {
			var snap fixtures.Snapshot
			if json.Unmarshal(data, &snap) == nil {
				record.Counter = snap
			}
		}
	}

	totals := map[string]*PlatformTotals{}
	if raw, err := os.ReadFile(filepath.Join(dir, fixtures.RequestLogFile)); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec fixtures.RequestRecord
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			record.Requests++
			if rec.WriteApplied {
				record.Writes++
			}
			agg := totals[rec.Platform]
			if agg == nil {
				agg = &PlatformTotals{Platform: rec.Platform}
				totals[rec.Platform] = agg
			}
			agg.Requests++
			if rec.WriteApplied {
				agg.Writes++
			}
		}
	}
	plats := make([]PlatformTotals, 0, len(totals))
	for _, v := range totals {
		plats = append(plats, *v)
	}
	sort.Slice(plats, func(i, j int) bool { return plats[i].Platform < plats[j].Platform })

	writeObservation(t, dir, record, map[string]any{
		"label":              label,
		"counterSnapshot":    record.Counter,
		"counterWrittenAt":   record.CounterAt,
		"totalRequestsSoFar": record.Requests,
		"totalWritesSoFar":   record.Writes,
		"requestsByPlatform": plats,
		"note":               "请求/写入计数是整条 case 累积的文件总量（fixture jsonl 追加写）；每条测试的观察值见 byTest:<name>",
		"byTest:" + t.Name(): record,
	})
}

// writeObservation 合并写 observation.json，并追加本次记录到 connector-observation.json。
func writeObservation(t *testing.T, dir string, record ObservationRecord, facts map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}

	artifact := filepath.Join(dir, "connector-observation.json")
	records := []json.RawMessage{}
	if raw, err := os.ReadFile(artifact); err == nil {
		if uerr := json.Unmarshal(raw, &records); uerr != nil {
			t.Fatalf("已有 connector-observation.json 不是合法 JSON 数组: %v", uerr)
		}
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化观察记录失败: %v", err)
	}
	records = append(records, json.RawMessage(body))
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("序列化 connector-observation.json 失败: %v", err)
	}
	if err := os.WriteFile(artifact, data, 0o644); err != nil {
		t.Fatalf("写 connector-observation.json 失败: %v", err)
	}

	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if uerr := json.Unmarshal(raw, &merged); uerr != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", uerr)
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
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), out, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}
