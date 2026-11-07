package resources

// RES01/RES03/RES04 的外部观察值输出。
//
// 契约与 internal/run/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写（不清空：RES04 是跨包两步 case），artifact 覆盖写；
// 没设置该变量（本地 focused 运行）时什么也不写。
//
// 观察值必须来自磁盘/子进程：
//   - skills-tree.json：filepath.Walk 托管技能目录，记录 {relpath, sha256, mode, size}；
//   - agent-config.json：产品写到隔离 agentDir 里的配置文件，读回来核对；
//   - compiled-args.json / worker-argv.json：由 fakeworker 子进程写出它**实际收到**的
//     argv（-record-argv），测试只读文件，不自己拼一份"应该是什么"。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/runtime/pi"
)

var resourcesObservationMu sync.Mutex

func resourcesObservationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// skillsTreeEntry 是托管目录里的一个真实条目。
type skillsTreeEntry struct {
	RelPath string `json:"relpath"`
	IsDir   bool   `json:"isDir"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
}

// recordSkillsTree 从磁盘 walk 托管技能目录，把快照追加进 skills-tree.json，
// 并把本次观察到的事实合并进 observation.json。
//
// 追加而不是覆盖：同一个 case 里"删除前/删除后"是两份不同的真实状态，
// 只留最后一份会让删除那一步的证据消失。
func recordSkillsTree(t *testing.T, root string, label string, facts map[string]any) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	skillsRoot := filepath.Join(root, "skills")
	entries := make([]skillsTreeEntry, 0, 16)
	walkErr := filepath.Walk(skillsRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 目录不存在（例如全部删掉）也是一份真实状态
			}
			return err
		}
		rel, relErr := filepath.Rel(skillsRoot, p)
		if relErr != nil {
			return relErr
		}
		e := skillsTreeEntry{
			RelPath: filepath.ToSlash(rel),
			IsDir:   info.IsDir(),
			Mode:    info.Mode().String(),
			Size:    info.Size(),
		}
		if !info.IsDir() {
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(raw)
			e.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
		}
		entries = append(entries, e)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("从磁盘 walk 托管技能目录 %s 失败: %v", skillsRoot, walkErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })

	snapshot := map[string]any{
		"test":       t.Name(),
		"label":      label,
		"root":       filepath.ToSlash(skillsRoot),
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
		"entries":    entries,
	}
	merged := readResourcesArtifact(t, "skills-tree.json")
	snaps, _ := merged["snapshots"].([]any)
	merged["snapshots"] = append(snaps, snapshot)
	writeResourcesArtifact(t, "skills-tree.json", merged)

	fileCount := 0
	skillDirs := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir {
			fileCount++
			continue
		}
		// 顶层目录 = 一个已落盘的技能（只数一层，避免把 SKILL.md 之类算成技能）。
		if parts := strings.Split(e.RelPath, "/"); len(parts) == 1 && parts[0] != "." {
			skillDirs[parts[0]] = true
		}
	}
	skillNames := make([]string, 0, len(skillDirs))
	for name := range skillDirs {
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)
	if facts == nil {
		facts = map[string]any{}
	}
	// 集合类事实跨测试累积（导入/删除/CAS 拒绝分散在不同测试里，
	// 后写入者直接覆盖会只剩最后一条测试观察到的那个技能）。
	for _, key := range []string{"importedSkills", "deletedSkills", "casRejected"} {
		facts[key] = unionResourcesListFact(t, key, facts[key])
	}
	facts["skillsRoot"] = filepath.ToSlash(skillsRoot)
	facts["fileCount"] = fileCount
	facts["skillCount"] = len(skillNames)
	facts["skillNames"] = unionResourcesListFact(t, "skillNames", skillNames)
	facts["treeEntries"] = len(entries)
	facts["treeLabel"] = label
	facts["treeSnapshotCount"] = len(merged["snapshots"].([]any))
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"skillsRoot":  filepath.ToSlash(skillsRoot),
		"fileCount":   fileCount,
		"skillCount":  len(skillNames),
		"skillNames":  skillNames,
		"treeEntries": len(entries),
	}
	writeResourcesObservation(t, facts, nil)
}

// configTreeEntry 是配置存储根 / agentDir 里的一份真实磁盘事实。
//
// 为什么连目录也记：MOD01/MOD04/MCP04 要证明的不是"函数返回了什么"，
// 而是产品到底在磁盘上留下了哪些文件（少了该有的、多了不该有的都看得见）。
type configTreeEntry struct {
	RelPath string `json:"relpath"`
	Kind    string `json:"kind"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256,omitempty"`
}

// walkResourcesTree 从磁盘遍历 root（不跟随符号链接）。
func walkResourcesTree(t *testing.T, root string) []configTreeEntry {
	t.Helper()
	entries := make([]configTreeEntry, 0, 16)
	walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // 目录不存在也是一份真实状态（例如什么都没写）
			}
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		e := configTreeEntry{RelPath: filepath.ToSlash(rel), Kind: "file", Bytes: info.Size()}
		if info.IsDir() {
			e.Kind = "dir"
			e.Bytes = 0
		} else {
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(raw)
			e.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
		}
		entries = append(entries, e)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("从磁盘遍历 %s 失败: %v", root, walkErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	return entries
}

// recordResourcesTree 把 root 的真实磁盘快照**追加**进 artifact，并合并本次事实。
//
// 追加而不是覆盖：同一条 case 的多条测试各自是独立的临时目录，
// 覆盖写会让前面测试观察到的磁盘事实消失。
func recordResourcesTree(t *testing.T, root, artifact, label string, extra map[string]any) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	entries := walkResourcesTree(t, root)
	snapshot := map[string]any{
		"test":       t.Name(),
		"label":      label,
		"root":       filepath.ToSlash(root),
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
		"entries":    entries,
	}
	merged := readResourcesArtifact(t, artifact)
	snaps, _ := merged["snapshots"].([]any)
	merged["snapshots"] = append(snaps, snapshot)
	writeResourcesArtifact(t, artifact, merged)

	fileCount := 0
	for _, e := range entries {
		if e.Kind == "file" {
			fileCount++
		}
	}
	facts := map[string]any{
		"treeRoot":          filepath.ToSlash(root),
		"treeLabel":         label,
		"treeEntries":       len(entries),
		"treeFiles":         fileCount,
		"treeSnapshotCount": len(merged["snapshots"].([]any)),
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"treeRoot":    filepath.ToSlash(root),
		"treeLabel":   label,
		"treeEntries": len(entries),
		"treeFiles":   fileCount,
	}
	writeResourcesObservation(t, facts, nil)
}

// secretScanRecord 是"从磁盘重新扫描 sentinel"的一次结果。
//
// 只记命中的文件路径与哨兵的 sha256，绝不把哨兵本身写进证据——
// 否则证据文件自己就成了一份泄漏。
type secretScanRecord struct {
	Test           string   `json:"test"`
	Label          string   `json:"label"`
	Roots          []string `json:"roots"`
	ScannedFiles   int      `json:"scannedFiles"`
	SentinelSHA256 string   `json:"sentinelSha256"`
	HitCount       int      `json:"hitCount"`
	Hits           []string `json:"hits"`
}

// recordSecretScan 扫描 roots 下每个文件的原始字节与三种 base64 变体。
//
// 为什么要扫 base64：凭证在 JSON payload 里往往是编码过的，只查原始字节会
// 让"忘记加密"这种最危险的缺陷假绿（F07 实测过）。
func recordSecretScan(t *testing.T, artifact, label string, roots []string, secret string, extra map[string]any) {
	t.Helper()
	if resourcesObservationDir() == "" {
		return
	}
	needles := [][]byte{
		[]byte(secret),
		[]byte(base64.StdEncoding.EncodeToString([]byte(secret))),
		[]byte(base64.RawStdEncoding.EncodeToString([]byte(secret))),
		[]byte(base64.URLEncoding.EncodeToString([]byte(secret))),
	}
	sum := sha256.Sum256([]byte(secret))
	rec := secretScanRecord{
		Test:           t.Name(),
		Label:          label,
		Roots:          make([]string, 0, len(roots)),
		SentinelSHA256: "sha256:" + hex.EncodeToString(sum[:]),
		Hits:           []string{},
	}
	for _, root := range roots {
		rec.Roots = append(rec.Roots, filepath.ToSlash(root))
		walkErr := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return nil
			}
			rec.ScannedFiles++
			for _, n := range needles {
				if len(n) > 0 && strings.Contains(string(raw), string(n)) {
					rel, relErr := filepath.Rel(root, p)
					if relErr != nil {
						rel = p
					}
					rec.Hits = append(rec.Hits, filepath.ToSlash(rel))
					break
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("扫描 %s 查找 sentinel 失败: %v", root, walkErr)
		}
	}
	sort.Strings(rec.Hits)
	rec.HitCount = len(rec.Hits)

	merged := readResourcesArtifact(t, artifact)
	snaps, _ := merged["snapshots"].([]any)
	merged["snapshots"] = append(snaps, rec)
	writeResourcesArtifact(t, artifact, merged)

	facts := map[string]any{
		"scanLabel":         label,
		"scannedFiles":      rec.ScannedFiles,
		"sentinelHitCount":  rec.HitCount,
		"sentinelHits":      rec.Hits,
		"sentinelSha256":    rec.SentinelSHA256,
		"scanSnapshotCount": len(merged["snapshots"].([]any)),
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()+"/"+label] = map[string]any{
		"scannedFiles":     rec.ScannedFiles,
		"sentinelHitCount": rec.HitCount,
		"sentinelHits":     rec.Hits,
	}
	writeResourcesObservation(t, facts, nil)
}

// writeResourcesObservation 把观察值与 artifact 合并写进 ANVIL_OBSERVATION_DIR。
func writeResourcesObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := resourcesObservationDir()
	if dir == "" {
		return
	}
	resourcesObservationMu.Lock()
	defer resourcesObservationMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}
	merged := readResourcesArtifact(t, "observation.json")
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
	writeResourcesArtifact(t, "observation.json", merged)
}

// readResourcesArtifact 读取已有 artifact（不存在时返回空对象）。
func readResourcesArtifact(t *testing.T, name string) map[string]any {
	t.Helper()
	out := map[string]any{}
	raw, err := os.ReadFile(filepath.Join(resourcesObservationDir(), name))
	if err != nil {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		// 不静默重建：已有文件损坏时继续写会把"合并"变成"混脏数据"。
		t.Fatalf("已有 %s 不是合法 JSON 对象: %v", name, err)
	}
	return out
}

// writeResourcesArtifact 原子感地写一个 JSON artifact。
func writeResourcesArtifact(t *testing.T, name string, value any) {
	t.Helper()
	dir := resourcesObservationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", name, err)
	}
}

// unionResourcesListFact 把本次观察到的列表与已有 observation.json 里的同名列表取并集。
func unionResourcesListFact(t *testing.T, key string, cur any) any {
	t.Helper()
	if resourcesObservationDir() == "" {
		return cur
	}
	prior := readResourcesArtifact(t, "observation.json")
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

// mergeSkillsFact 追加一条"这个技能被导入/删除/被 CAS 拒绝"的事实（并集，跨测试累积）。
func mergeSkillsFact(t *testing.T, facts map[string]any, key, id string) {
	t.Helper()
	cur, _ := facts[key].([]string)
	for _, v := range cur {
		if v == id {
			return
		}
	}
	facts[key] = append(append([]string{}, cur...), id)
}

// sha256File 返回磁盘文件的哈希（agentDir 配置核对用）。
func sha256File(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ---- RES03/RES04/CFG03：由子进程自己写 argv/env ----

var (
	fakeWorkerOnce sync.Once
	fakeWorkerPath string
	fakeWorkerErr  error
)

// buildFakeWorker 编译 fakeworker（整个包只编译一次）。
//
// 为什么不在测试里拼一份"应该是这样的 argv"：拼出来的东西只能证明测试自己的
// 拼接逻辑。argv 必须由真正被启动的那个子进程写下来，父进程只读文件。
func buildFakeWorker(t *testing.T) string {
	t.Helper()
	fakeWorkerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "myagent-resources-fakeworker-*")
		if err != nil {
			fakeWorkerErr = err
			return
		}
		name := "fakeworker"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "my-agent-go/internal/runtime/pi/fakeworker")
		if b, err := cmd.CombinedOutput(); err != nil {
			fakeWorkerErr = fmt.Errorf("编译 fakeworker 失败: %v\n%s", err, b)
			return
		}
		fakeWorkerPath = out
	})
	if fakeWorkerErr != nil {
		t.Fatal(fakeWorkerErr)
	}
	return fakeWorkerPath
}

// childRecord 是子进程自己写下的 argv/env 观察值（从磁盘读回）。
type childRecord struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
}

// runRecordingChild 用真实隔离参数启动 fakeworker，让子进程把 argv/env 写进 outDir，
// 父进程只负责读回。返回值来自文件，不来自这里的变量。
func runRecordingChild(t *testing.T, isolationArgs []string) childRecord {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "worker-argv.json")
	envPath := filepath.Join(dir, "worker-env.json")

	exe := buildFakeWorker(t)
	// 观察路径走环境变量：argv 里只保留产品真正会发出的隔离参数，
	// 否则"-record-argv 出现在 argv 里"会让证据自证。
	env := []string{
		"FAKEWORKER_RECORD_ARGV=" + argvPath,
		"FAKEWORKER_RECORD_ENV=" + envPath,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, err := pi.StartWorker(ctx, pi.WorkerSpec{
		Installation: pi.PiInstallation{ID: "fixture:fakeworker", Executable: exe},
		WorkingDir:   dir,
		Env:          env,
		Args:         append([]string{}, isolationArgs...),
	})
	if err != nil {
		t.Fatalf("启动 fakeworker 失败: %v", err)
	}
	defer func() {
		closeCtx, cc := context.WithTimeout(context.Background(), 15*time.Second)
		defer cc()
		_ = w.Close(closeCtx)
	}()

	rec := childRecord{}
	deadline := time.Now().Add(15 * time.Second)
	for {
		argvRaw, argvErr := os.ReadFile(argvPath)
		envRaw, envErr := os.ReadFile(envPath)
		if argvErr == nil && envErr == nil {
			if err := json.Unmarshal(argvRaw, &rec.Argv); err != nil {
				t.Fatalf("子进程写下的 worker-argv.json 不是合法 JSON: %v", err)
			}
			if err := json.Unmarshal(envRaw, &rec.Env); err != nil {
				t.Fatalf("子进程写下的 worker-env.json 不是合法 JSON: %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等不到子进程写出的 argv/env（argvErr=%v envErr=%v）", argvErr, envErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rec.Argv) == 0 {
		t.Fatal("子进程写下的 argv 为空：观察值不能是空的")
	}
	return rec
}

// envValue 从子进程写下的环境变量列表里取一个值。
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}
