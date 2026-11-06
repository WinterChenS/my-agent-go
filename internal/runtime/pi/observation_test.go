package pi

// CFG03.isolation 的外部观察值输出。
//
// 契约与 internal/run/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写，artifact 覆盖写；没设置该变量（本地 focused 运行）时什么也不写。
//
// 这里唯一的外部观察源是**子进程自己写下的文件**：fakeworker 通过
// FAKEWORKER_RECORD_ARGV / FAKEWORKER_RECORD_ENV 把 os.Args 与 os.Environ 落盘，
// 父进程只读。父进程不拼"应该是这样的 argv"，也不从自己的变量里推断 HOME。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func piObservationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// childRecorded 是子进程自己写下的 argv/env（从磁盘读回）。
type childRecorded struct {
	Argv []string
	Env  []string
}

// readChildRecorded 从观察目录读回子进程写下的 argv/env。
//
// 必须在子进程真的写出文件之后调用：读不到就判失败——声明了 artifact
// 却拿不到文件，等于没有观察值，不能退回"用测试变量代替"。
func readChildRecorded(t *testing.T) childRecorded {
	t.Helper()
	dir := piObservationDir()
	argvRaw, err := os.ReadFile(filepath.Join(dir, "worker-argv.json"))
	if err != nil {
		t.Fatalf("读回子进程写下的 worker-argv.json 失败: %v", err)
	}
	envRaw, err := os.ReadFile(filepath.Join(dir, "worker-env.json"))
	if err != nil {
		t.Fatalf("读回子进程写下的 worker-env.json 失败: %v", err)
	}
	var rec childRecorded
	if err := json.Unmarshal(argvRaw, &rec.Argv); err != nil {
		t.Fatalf("worker-argv.json 不是合法 JSON: %v", err)
	}
	if err := json.Unmarshal(envRaw, &rec.Env); err != nil {
		t.Fatalf("worker-env.json 不是合法 JSON: %v", err)
	}
	if len(rec.Argv) == 0 {
		t.Fatal("子进程写下的 argv 为空：观察值不能是空的")
	}
	return rec
}

// envLookup 从子进程写下的环境变量列表里取值（大小写不敏感，Windows 上必须如此）。
func envLookup(env []string, key string) (string, bool) {
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && strings.EqualFold(kv[:i], key) {
			return kv[i+1:], true
		}
	}
	return "", false
}

// ---- 通用外部观察装置（PI01/PI02/PI04/RPC05 共用） ----
//
// 这里只做两件事：
//   1. 从磁盘读出**别人写下的东西**（子进程自述、OS 进程表、程序 stdout）；
//   2. 把磁盘上的真实状态规范化成 artifact（遍历 + 大小 + 模式 + sha256）。
// 绝不在这里"构造"观察值：拿不到文件就判失败。

type treeEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// walkTree 只遍历磁盘：相对路径 + 大小 + 权限位 + 内容哈希。
// 目录本身也记录（size=-1），这样"新增/删除目录"同样可见。
func walkTree(t *testing.T, root string) []treeEntry {
	t.Helper()
	var out []treeEntry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if d.IsDir() {
			out = append(out, treeEntry{Path: rel, Size: -1, Mode: info.Mode().String()})
			return nil
		}
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		out = append(out, treeEntry{Path: rel, Size: info.Size(), Mode: info.Mode().String(), SHA256: sha256Of(b)})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// treeDigest 是整棵树的稳定摘要（对规范化后的条目列表取哈希）。
func treeDigest(t *testing.T, entries []treeEntry) string {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("序列化树条目失败: %v", err)
	}
	return sha256Of(b)
}

// writeArtifact 把一个已从磁盘读出的观察值写进观察目录（未配置观察目录时不写）。
func writeArtifact(t *testing.T, name string, value any) {
	t.Helper()
	dir := piObservationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
		t.Fatalf("写 artifact %s 失败: %v", name, err)
	}
}

// parseKVRecords 解析子进程/OS 写下的 "key=value" 逐行记录（顺序保留）。
// 这是把外部原始文本规范化成 JSON 的唯一动作，内容本身不加工。
func parseKVRecords(t *testing.T, path string) []map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回外部记录 %s 失败: %v", path, err)
	}
	var out []map[string]string
	cur := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = map[string]string{}
			}
			continue
		}
		if i := strings.IndexByte(line, '='); i > 0 {
			cur[line[:i]] = line[i+1:]
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// isolationFlagsOf 从 argv 里挑出隔离参数（-na/-nc/-ne/-np/--no-*）。
func isolationFlagsOf(argv []string) []string {
	out := make([]string, 0, 8)
	for _, a := range argv {
		if strings.HasPrefix(a, "-n") || strings.HasPrefix(a, "--no-") {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
