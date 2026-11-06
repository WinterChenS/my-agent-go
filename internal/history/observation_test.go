package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// RUN07 的外部观察值输出，契约与 internal/run/observation_test.go 一致：
//   - 目录来自 ANVIL_OBSERVATION_DIR（集成 runner 传本 case 的证据目录）；
//   - 进程内第一次写入先清掉上一轮残留，之后 observation.json **合并**、
//     artifact 覆盖/追加；
//   - 没设置该变量（本地 focused 运行）时什么也不写。
//
// 为什么 history 包自己带一份：run 包的那份是 _test.go 里的包内私有函数，
// 跨包不可见。共享它需要把测试辅助提成非测试代码，代价比这段重复更大。
var caseObservationStarted sync.Once

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

func writeCaseObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	caseObservationStarted.Do(func() {
		_ = os.Remove(filepath.Join(dir, "observation.json"))
		_ = os.Remove(filepath.Join(dir, "index-dump.json"))
	})

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}

	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			// 不静默重建：已有文件损坏时继续写会把"合并"变成"混脏数据"。
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
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == t.Name() {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, t.Name())
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
