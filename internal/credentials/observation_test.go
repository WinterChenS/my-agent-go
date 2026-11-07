package credentials

// SEC07.store 的外部观察值输出。
//
// 契约与 internal/memory|run|config 的 observation 文件一致：目录来自
// ANVIL_OBSERVATION_DIR，observation.json 合并写、artifact 按测试追加（同一 case 的
// 多条测试各有自己的临时凭证根）；未设置该变量时什么也不写。
//
// 事实来源是磁盘本身：cred-store.json 记录每次快照时凭证根下的真实文件
// （文件名/字节数/sha256）以及**重新扫描**这些文件得到的 sentinel 命中数。
// 哨兵只记 sha256，绝不把秘密写进证据文件。

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func observeDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// credFileEntry 是凭证根下的一份真实磁盘事实。
type credFileEntry struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
}

// credSnapshot 是一次"落盘后扫盘"的观察记录。
type credSnapshot struct {
	Test           string          `json:"test"`
	Label          string          `json:"label"`
	Root           string          `json:"root"`
	CapturedAt     string          `json:"capturedAt"`
	Entries        []credFileEntry `json:"entries"`
	ScannedFiles   int             `json:"scannedFiles"`
	SentinelSHA256 string          `json:"sentinelSha256"`
	HitCount       int             `json:"hitCount"`
	Hits           []string        `json:"hits"`
}

// observeCredentialStore 扫一遍凭证根并写进 ANVIL_OBSERVATION_DIR。
//
// 为什么扫 base64 变体：凭证在 JSON/文本接口里常以 base64 形式出现，
// 只查原始字节会让"忘记加密"这类最危险的缺陷假绿。
func observeCredentialStore(t *testing.T, label, root string, secrets [][]byte, extra map[string]any) {
	t.Helper()
	dir := observeDir()
	if dir == "" {
		return
	}
	snapshot := credSnapshot{
		Test:       t.Name(),
		Label:      label,
		Root:       filepath.ToSlash(root),
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Entries:    []credFileEntry{},
		Hits:       []string{},
	}
	needles := make([][]byte, 0, 4*len(secrets))
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		needles = append(needles,
			secret,
			[]byte(base64.StdEncoding.EncodeToString(secret)),
			[]byte(base64.RawStdEncoding.EncodeToString(secret)),
			[]byte(base64.URLEncoding.EncodeToString(secret)),
		)
	}
	if len(secrets) > 0 {
		sum := sha256.Sum256(secrets[0])
		snapshot.SentinelSHA256 = "sha256:" + hex.EncodeToString(sum[:])
	}

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 目录不存在（例如后端不落文件）也是一份真实状态
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(body)
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		snapshot.Entries = append(snapshot.Entries, credFileEntry{
			Name:   filepath.ToSlash(rel),
			Bytes:  int64(len(body)),
			SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		})
		snapshot.ScannedFiles++
		for _, n := range needles {
			if len(n) > 0 && strings.Contains(string(body), string(n)) {
				snapshot.Hits = append(snapshot.Hits, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描凭证根 %s 失败: %v", root, walkErr)
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool { return snapshot.Entries[i].Name < snapshot.Entries[j].Name })
	sort.Strings(snapshot.Hits)
	snapshot.HitCount = len(snapshot.Hits)

	writeCredSnapshot(t, snapshot, extra)
}

// writeCredSnapshot 把快照追加进 cred-store.json，并把本次事实合并进 observation.json。
func writeCredSnapshot(t *testing.T, snapshot credSnapshot, extra map[string]any) {
	t.Helper()
	dir := observeDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	artifactPath := filepath.Join(dir, "cred-store.json")
	records := []json.RawMessage{}
	if raw, err := os.ReadFile(artifactPath); err == nil {
		if uerr := json.Unmarshal(raw, &records); uerr != nil {
			t.Fatalf("已有 cred-store.json 不是合法 JSON 数组: %v", uerr)
		}
	}
	record, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("序列化凭证快照失败: %v", err)
	}
	records = append(records, record)
	out, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("序列化 cred-store.json 失败: %v", err)
	}
	if err := os.WriteFile(artifactPath, out, 0o644); err != nil {
		t.Fatalf("写 cred-store.json 失败: %v", err)
	}

	obsPath := filepath.Join(dir, "observation.json")
	merged := map[string]any{}
	if raw, err := os.ReadFile(obsPath); err == nil {
		if uerr := json.Unmarshal(raw, &merged); uerr != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON 对象: %v", uerr)
		}
	}
	facts := map[string]any{
		"label":            snapshot.Label,
		"credentialRoot":   snapshot.Root,
		"scannedFiles":     snapshot.ScannedFiles,
		"sentinelHitCount": snapshot.HitCount,
		"sentinelHits":     snapshot.Hits,
		"sentinelSha256":   snapshot.SentinelSHA256,
		"snapshotCount":    len(records),
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+snapshot.Test+"/"+snapshot.Label] = map[string]any{
		"credentialRoot":   snapshot.Root,
		"scannedFiles":     snapshot.ScannedFiles,
		"sentinelHitCount": snapshot.HitCount,
		"entries":          snapshot.Entries,
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
		if s, ok := v.(string); ok && s == snapshot.Test {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, snapshot.Test)
	}
	merged["tests"] = tests
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation.json 失败: %v", err)
	}
	if err := os.WriteFile(obsPath, data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}
