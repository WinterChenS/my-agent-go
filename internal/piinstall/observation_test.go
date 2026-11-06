package piinstall

// PI_INS01–03 / PI_UPG01–03 的外部观察值输出。
//
// 契约与 internal/history/observation_test.go、internal/run/observation_test.go 一致：
//   - 目录来自 ANVIL_OBSERVATION_DIR（集成 runner 传本 case 的证据目录）；
//   - observation.json 是**合并**写（key 并进已有 JSON，累积 tests 列表）；
//   - artifact 覆盖写，stages.jsonl 追加写；
//   - 没设置该变量（本地 focused 运行）时什么也不写。
//
// 纪律：观察值只从磁盘/产品产物读出来，绝不在测试进程里"构造一个 passed:true"。
//   - managed-tree.json：托管根的真实磁盘遍历（大小/权限/sha256）；
//   - active-pointer.json：托管根里的 active.json 原样（从磁盘读回）；
//   - external-hashes.json：外部安装与 PATH 的前后哈希（外部 Pi 从未被改写）；
//   - stages.jsonl：服务实际发出的阶段转换（一行一条，追加）。

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

var observationStarted sync.Once

// prepareObservation 在进程内第一次写观察时清掉陈旧产物（同一 revision 重跑不累积）。
func prepareObservation(dir string) {
	observationStarted.Do(func() {
		_ = os.Remove(filepath.Join(dir, "observation.json"))
		_ = os.Remove(filepath.Join(dir, "stages.jsonl"))
	})
}

// writeCaseObservation 合并写 observation.json，并写/追加 artifact。
func writeCaseObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	prepareObservation(dir)

	for name, content := range files {
		if name == "stages.jsonl" {
			f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				t.Fatalf("追加 artifact %s 失败: %v", name, err)
			}
			_, writeErr := f.Write(content)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("追加 artifact %s 失败: write=%v close=%v", name, writeErr, closeErr)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}

	path := filepath.Join(dir, "observation.json")
	merged := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if jsonErr := json.Unmarshal(raw, &merged); jsonErr != nil {
			t.Fatalf("已有的 observation.json 不是合法 JSON: %v", jsonErr)
		}
	}
	// 一条 case 有多个子场景时（PI_INS01 的 10 种非法包、PI_INS03 的 5 种失败、
	// PI_UPG02 的 2 条路径），逐条**追加**进 scenarios 数组。
	// 直接并进顶层会让后一条覆盖前一条，证据里只剩最后一个场景——那是把证据削薄。
	if _, multi := facts["scenario"]; multi {
		prev, _ := merged["scenarios"].([]any)
		merged["scenarios"] = append(prev, facts)
		if c, ok := facts["case"]; ok {
			merged["case"] = c
		}
	} else {
		for k, v := range facts {
			merged[k] = v
		}
	}
	prev, _ := merged["tests"].([]any)
	merged["tests"] = append(prev, t.Name())
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化 observation.json 失败: %v", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}

// treeEntry 是托管根磁盘遍历的一行。
type treeEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
}

// walkManagedTree 只遍历磁盘：相对路径 + 大小 + 权限位 + 内容哈希。
// 目录记为 size=-1，这样"目录被创建/删除"同样可见。
func walkManagedTree(t *testing.T, root string) []treeEntry {
	t.Helper()
	out := []treeEntry{}
	if _, err := os.Stat(root); err != nil {
		return out
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		entry := treeEntry{Path: filepath.ToSlash(rel), Size: info.Size(), Mode: info.Mode().String()}
		if d.IsDir() {
			entry.Size = -1
		} else if info.Mode().IsRegular() {
			sum, sumErr := HashFileContent(p)
			if sumErr != nil {
				return sumErr
			}
			entry.SHA256 = sum
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// readActivePointerBytes 从磁盘读回 active.json（不存在时返回 nil）。
func readActivePointerBytes(t *testing.T, managedRoot string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(managedRoot, ActiveFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读 active.json 失败: %v", err)
	}
	return raw
}

// stageRecorder 记录服务实际发出的阶段转换（用于 stages.jsonl）。
type stageRecorder struct {
	mu     sync.Mutex
	stages []Stage
}

func (r *stageRecorder) record(s Stage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, s)
}

// progress 是传给 Install 的 progress 回调。
func (r *stageRecorder) progress() func(Stage) { return r.record }

// jsonl 把阶段列表渲染成一行一条的 JSONL。
func (r *stageRecorder) jsonl() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for i, s := range r.stages {
		line, _ := json.Marshal(map[string]any{"seq": i, "stage": string(s)})
		b.Write(line)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func (r *stageRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.stages))
	for _, s := range r.stages {
		out = append(out, string(s))
	}
	return out
}

// externalSnapshot 是"外部安装 + PATH"的前后哈希（证明外部 Pi 与 PATH 没被动过）。
type externalSnapshot struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	SHA256      string `json:"sha256,omitempty"`
	PATH        string `json:"pathEnv"`
	PATHHash    string `json:"pathEnvHash"`
	RealPiHome  string `json:"realPiHome"`
	RealPiHash  string `json:"realPiHomeHash"`
	RealAppData string `json:"realAppData"`
	RealAppHash string `json:"realAppDataHash"`
}

func captureExternalSnapshot(t *testing.T, externalPath string) externalSnapshot {
	t.Helper()
	snap := externalSnapshot{Path: externalPath, PATH: os.Getenv("PATH"), PATHHash: hashString(os.Getenv("PATH"))}
	if externalPath != "" {
		if sum, err := HashFileContent(externalPath); err == nil {
			snap.Exists = true
			snap.SHA256 = sum
		}
	}
	// 真实用户目录只读快照：证明本包没写它们（安全纪律的第二重证据）。
	for name, path := range safetyTargetsForMain() {
		s, err := snapshotRoot(path)
		if err != nil {
			s = safetySnapshot{SnapshotHash: "SNAPSHOT-ERROR: " + err.Error()}
		}
		switch name {
		case "user-pi-home":
			snap.RealPiHome, snap.RealPiHash = path, s.SnapshotHash
		case "real-appdata":
			snap.RealAppData, snap.RealAppHash = path, s.SnapshotHash
		}
	}
	return snap
}

// writeInstallObservation 写一条完整的安装观察（四个 artifact 全部非空）。
func writeInstallObservation(t *testing.T, managedRoot string, facts map[string]any, stages *stageRecorder, external externalSnapshot) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	tree, err := json.MarshalIndent(walkManagedTree(t, managedRoot), "", "  ")
	if err != nil {
		t.Fatalf("序列化托管树失败: %v", err)
	}
	active := readActivePointerBytes(t, managedRoot)
	if len(active) == 0 {
		active = []byte("{}\n")
	}
	ext, err := json.MarshalIndent(external, "", "  ")
	if err != nil {
		t.Fatalf("序列化外部快照失败: %v", err)
	}
	files := map[string][]byte{
		"managed-tree.json":    append(tree, '\n'),
		"active-pointer.json":  active,
		"external-hashes.json": append(ext, '\n'),
		"stages.jsonl":         stages.jsonl(),
	}
	if len(files["stages.jsonl"]) == 0 {
		// 连一次阶段都没发出，说明服务在进入安装事务前就拒绝了：
		// 这本身是可观察的事实，写成一行而不是留空文件。
		files["stages.jsonl"] = []byte(`{"seq":0,"stage":"none","note":"服务在发出任何阶段前拒绝"}` + "\n")
	}
	writeCaseObservation(t, facts, files)
}

// summarizeStages 是 facts 里 stages 字段的稳定形态。
func summarizeStages(stages *stageRecorder) []string { return stages.list() }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return append(b, '\n')
}

func debugf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf(format, args...)
}
