package memory

// MEM01/MEM02/MEM04/MEM05 的外部观察值输出。
//
// 契约与 internal/run/observation_test.go、internal/history/observation_test.go 一致：
//   - 目录来自 ANVIL_OBSERVATION_DIR（集成 runner 传本 case 的证据目录）；
//   - observation.json 是**合并**写，artifact 覆盖写；
//   - 没设置该变量（本地 focused 运行）时什么也不写。
//
// 与 run/history 的那两份有一处**故意不同**：这里第一次写入时**不清空**
// observation.json。MEM05 是跨包两步 case（internal/memory + internal/app），
// 第二步必须保留第一步写下的观察值；runner 在整条 case 开始前已经清过一次
// 证据目录，所以这里再清一次会把第一步的事实抹掉。
//
// 观察值的来源必须是磁盘：memory-dump.json 由**第二个 storage.DB 句柄**从
// SQLite 文件里读真实行（status/scope/expires_at 都是库里存的那一份），
// 不是问被测 Service "你记对了吗"。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/storage"
)

var memoryObservationMu sync.Mutex

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// memoryDumpRow 是库里真实存在的一行（不是 Service 返回的 Memory）。
type memoryDumpRow struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	CreatedAt   string `json:"createdAt"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

type memoryDumpIndexRow struct {
	RowID int64  `json:"rowid"`
	Scope string `json:"scope,omitempty"`
}

type memoryDumpScopeSetting struct {
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId"`
	Enabled     int    `json:"enabled"`
}

// observeMemoryDB 从数据库文件本身读出观察值，写入 memory-dump.json 与
// observation.json。extra 是本条测试特有的观察值（例如注入字符数）。
func observeMemoryDB(t *testing.T, path string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return // 本地 focused 运行：不落盘
	}
	artifact, facts := dumpMemoryFile(t, path)
	for k, v := range extra {
		facts[k] = v
	}
	// 每条测试自己的快照先拍下来：它只描述“这一条测试的临时库”，
	// 不能把下面跨测试的并集混进去（否则审查者会以为别的测试的库也在这个文件里）。
	own := map[string]any{
		"dbPath":            facts["dbPath"],
		"rows":              facts["rows"],
		"scopes":            facts["scopes"],
		"activeRows":        facts["activeRows"],
		"expiredExcluded":   facts["expiredExcluded"],
		"probeRows":         facts["probeRows"],
		"trigramHits":       facts["trigramHits"],
		"deletedRows":       facts["deletedRows"],
		"disabledScopeRows": facts["disabledScopeRows"],
	}
	for k, v := range extra {
		own[k] = v
	}
	// MEM02 的两项事实是跨测试的集合（一条测试看“禁用”、另一条看“删除”），
	// 后写入者直接覆盖会把前一条的观察抹成空数组。这里取并集：
	// 顶层 deletedRows/disabledScopeRows 是**本 case 里所有测试观察到的并集**，
	// 每条测试自己的快照仍然完整地留在 byTest:<name> 里。
	for _, key := range []string{"deletedRows", "disabledScopeRows"} {
		facts[key] = unionListFact(t, key, facts[key])
	}
	facts["byTest:"+t.Name()] = own
	writeMemoryObservation(t, facts, map[string][]byte{"memory-dump.json": artifact})
}

// dumpMemoryFile 用第二个句柄从磁盘读真实行，返回 artifact 与可核对的观察事实。
func dumpMemoryFile(t *testing.T, path string) ([]byte, map[string]any) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开记忆库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows := make([]memoryDumpRow, 0, 8)
	res, err := db.SQL().QueryContext(ctx, `
		SELECT id, scope, COALESCE(workspace_id, ''), COALESCE(kind, ''), status,
		       COALESCE(created_at, ''), COALESCE(expires_at, '')
		FROM memories ORDER BY id`)
	if err != nil {
		t.Fatalf("从磁盘读取 memories 行失败: %v", err)
	}
	for res.Next() {
		var r memoryDumpRow
		if err := res.Scan(&r.ID, &r.Scope, &r.WorkspaceID, &r.Kind, &r.Status, &r.CreatedAt, &r.ExpiresAt); err != nil {
			res.Close()
			t.Fatalf("扫描 memories 行失败: %v", err)
		}
		rows = append(rows, r)
	}
	if err := res.Err(); err != nil {
		res.Close()
		t.Fatalf("遍历 memories 行失败: %v", err)
	}
	res.Close()

	indexRows := make([]memoryDumpIndexRow, 0, 8)
	res, err = db.SQL().QueryContext(ctx, `
		SELECT f.rowid, COALESCE(m.scope, '') FROM memory_fts f
		LEFT JOIN memories m ON m.rowid = f.rowid ORDER BY f.rowid`)
	if err != nil {
		t.Fatalf("从磁盘读取 memory_fts 行失败: %v", err)
	}
	for res.Next() {
		var r memoryDumpIndexRow
		if err := res.Scan(&r.RowID, &r.Scope); err != nil {
			res.Close()
			t.Fatalf("扫描 memory_fts 行失败: %v", err)
		}
		indexRows = append(indexRows, r)
	}
	res.Close()

	settings := make([]memoryDumpScopeSetting, 0, 2)
	res, err = db.SQL().QueryContext(ctx, `
		SELECT scope, COALESCE(workspace_id, ''), enabled FROM memory_scope_settings
		ORDER BY scope, workspace_id`)
	if err != nil {
		t.Fatalf("从磁盘读取 memory_scope_settings 失败: %v", err)
	}
	for res.Next() {
		var s memoryDumpScopeSetting
		if err := res.Scan(&s.Scope, &s.WorkspaceID, &s.Enabled); err != nil {
			res.Close()
			t.Fatalf("扫描 memory_scope_settings 失败: %v", err)
		}
		settings = append(settings, s)
	}
	res.Close()

	// 一次真实的中文子串 trigram 查询（在磁盘库上，不经过 Service.Search）。
	var trigramHits int
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_fts WHERE memory_fts MATCH '连接池上'`).Scan(&trigramHits); err != nil {
		t.Fatalf("磁盘库上的 trigram 查询失败: %v", err)
	}

	scopes := map[string]int{}
	activeRows := 0
	expiredExcluded := 0
	deletedIDs := make([]string, 0, 2)
	now := time.Now().UTC()
	for _, r := range rows {
		scopes[r.Scope]++
		if r.Status == string(StatusActive) {
			activeRows++
			if r.ExpiresAt != "" {
				if ts, err := parseStoredTime(r.ExpiresAt); err == nil && !ts.IsZero() && !ts.After(now) {
					expiredExcluded++
				}
			}
		}
		if r.Status == string(StatusDeleted) {
			deletedIDs = append(deletedIDs, r.ID)
		}
	}
	disabledScopes := make([]map[string]any, 0, 2)
	for _, s := range settings {
		if s.Enabled == 0 {
			disabledScopes = append(disabledScopes, map[string]any{"scope": s.Scope, "workspaceId": s.WorkspaceID})
		}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	artifact, err := json.MarshalIndent(map[string]any{
		"database":      abs,
		"dumpedAt":      now.Format(time.RFC3339),
		"memories":      rows,
		"indexRows":     indexRows,
		"scopeSettings": settings,
		"trigramQuery":  "连接池上",
		"trigramHits":   trigramHits,
	}, "", "  ")
	if err != nil {
		t.Fatalf("序列化 memory-dump.json 失败: %v", err)
	}
	sum := sha256.Sum256(artifact)
	facts := map[string]any{
		"dbPath":            abs,
		"dumpSha256":        "sha256:" + hex.EncodeToString(sum[:]),
		"rows":              len(rows),
		"scopes":            scopes,
		"activeRows":        activeRows,
		"expiredExcluded":   expiredExcluded,
		"probeRows":         len(indexRows),
		"trigramHits":       trigramHits,
		"deletedRows":       deletedIDs,
		"disabledScopeRows": disabledScopes,
		"scopeSettings":     settings,
	}
	return artifact, facts
}

// unionListFact 把本次观察到的列表与已有 observation.json 里的同名列表取并集。
//
// 只用于“同一 case 多条测试各看到一部分”的集合类事实（删除行 / 关闭的 scope）。
// 数字类事实不这样做：不同测试用的是不同临时库，把它们相加只会得到一个
// 不对应任何真实数据库的数字。
func unionListFact(t *testing.T, key string, cur any) any {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return cur
	}
	prior := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &prior); err != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", err)
		}
	}
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
	out := make([]any, 0, 4)
	seen := map[string]bool{}
	for _, src := range []any{prior[key], cur} {
		// cur 可能是 []string（内部构造），prior 是 []any（JSON 解出）；
		// 统一走一遍 JSON 才能用同一个类型断言处理，否则并集会静默变成空集。
		var items []any
		switch v := src.(type) {
		case []any:
			items = v
		case nil:
			continue
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			if err := json.Unmarshal(b, &items); err != nil {
				continue
			}
		}
		for _, item := range items {
			k := encode(item)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, item)
		}
	}
	return out
}

// writeMemoryObservation 把观察值与 artifact 合并写进 ANVIL_OBSERVATION_DIR。
func writeMemoryObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	memoryObservationMu.Lock()
	defer memoryObservationMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
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

// writeMemoryArtifact 只写 artifact（不写 facts），供需要额外原始产物的 case 用。
func writeMemoryArtifact(t *testing.T, name string, content []byte) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	memoryObservationMu.Lock()
	defer memoryObservationMu.Unlock()
	if err := os.MkdirAll(observationDir(), 0o755); err != nil {
		t.Fatalf("创建观察目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(observationDir(), name), content, 0o644); err != nil {
		t.Fatalf("写 artifact %s 失败: %v", name, err)
	}
}
