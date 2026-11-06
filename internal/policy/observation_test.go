package policy

// SEC01 / SEC05 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go、internal/run/observation_test.go 一致：
//   - 目录来自 ANVIL_OBSERVATION_DIR（集成 runner 传本 case 的证据目录）；
//   - observation.json 合并写，artifact 覆盖写；
//   - 没设置该变量（本地 focused 运行）时什么也不写。
//
// 这里的外部观察源有两个，都在测试进程之外：
//   - executions.jsonl：由 testdata/executor 这个**独立进程**追加，一行一次真实执行
//     （时间戳、pid、标签、工具名、目标路径、argv）。父进程只读回统计；
//   - tree.json：对沙箱根做一次磁盘遍历（相对路径、大小、权限位、sha256），
//     证明"哪些文件真的存在"，而不是"策略返回了 deny"。
//
// 测试进程自己不写账本：它只负责启动 executor、读回文件。否则"被拒路径没有副作用"
// 又会退化成同一个进程的自述。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func policyObservationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

// policyArtifactDir 返回 artifact 的落盘目录。
//
// 未配置观察目录时退回临时目录：外部执行装置仍然要真的跑一遍（本地 focused
// 运行也要观察真实副作用），只是不往证据树里写。
func policyArtifactDir(t *testing.T) string {
	t.Helper()
	if dir := policyObservationDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
		}
		return dir
	}
	return t.TempDir()
}

// obsTreeEntry 是一次磁盘遍历里的一条记录。
type obsTreeEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

// obsExecRecord 是 executor 进程追加的一行账本。
type obsExecRecord struct {
	TS     string   `json:"ts"`
	PID    int      `json:"pid"`
	Label  string   `json:"label"`
	Tool   string   `json:"tool"`
	Target string   `json:"target"`
	Argv   []string `json:"argv"`
}

// readExecRecords 从磁盘读回 executor 账本。
//
// 读不到就判失败：声明了 artifact 却拿不到文件，等于没有观察值，
// 不能退回"用测试变量代替"。
func readExecRecords(t *testing.T, path string) []obsExecRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回 executor 账本 %s 失败: %v", path, err)
	}
	var out []obsExecRecord
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec obsExecRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("账本记录不是合法 JSON: %v（原文 %s）", err, line)
		}
		if rec.PID <= 0 || rec.Target == "" {
			t.Fatalf("账本记录缺少 pid/target: %s", line)
		}
		out = append(out, rec)
	}
	return out
}

// countExecLabel 数某个标签下**真实发生过**的执行次数（来自账本，不是期望值）。
func countExecLabel(records []obsExecRecord, label string) int {
	n := 0
	for _, rec := range records {
		if rec.Label == label {
			n++
		}
	}
	return n
}

// execLabels 按账本顺序取出标签列表（证据里能直接看出哪几条真的执行了）。
func execLabels(records []obsExecRecord) []string {
	out := make([]string, 0, len(records))
	for _, rec := range records {
		out = append(out, rec.Label)
	}
	return out
}

// walkObsTree 只遍历磁盘：相对路径 + 大小 + 权限位 + 内容哈希。
// 目录本身也记录（size=-1），这样"新增/删除目录"同样可见。
func walkObsTree(t *testing.T, root string) []obsTreeEntry {
	t.Helper()
	var out []obsTreeEntry
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
			out = append(out, obsTreeEntry{Path: rel, Size: -1, Mode: info.Mode().String()})
			return nil
		}
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(b)
		out = append(out, obsTreeEntry{
			Path: rel, Size: info.Size(), Mode: info.Mode().String(),
			SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// treeFilePaths 取文件条目（目录不计），用于"盘上有哪些文件"的判断。
func treeFilePaths(entries []obsTreeEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Size >= 0 {
			out = append(out, e.Path)
		}
	}
	return out
}

// writePolicyObservation 把观察值与 artifact 合并写进观察目录。
func writePolicyObservation(t *testing.T, facts map[string]any, files map[string][]byte) {
	t.Helper()
	dir := policyObservationDir()
	if dir == "" {
		return
	}
	policyObsMu.Lock()
	defer policyObsMu.Unlock()
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

var policyObsMu sync.Mutex

// ---- 独立执行进程 ----

var (
	executorOnce sync.Once
	executorBin  string
	executorErr  error
)

// executorPath 构建（一次）SEC01/SEC05 的独立执行进程。
//
// 构建产物落在临时目录，不进仓库：观察装置不是产品代码，不能留在工作树里。
func executorPath(t *testing.T) string {
	t.Helper()
	executorOnce.Do(func() {
		root, err := policyRepoRoot()
		if err != nil {
			executorErr = err
			return
		}
		dir, err := os.MkdirTemp("", "policy-executor-")
		if err != nil {
			executorErr = err
			return
		}
		bin := filepath.Join(dir, "policy-executor")
		if strings.EqualFold(filepath.Ext(os.Args[0]), ".exe") {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, "./internal/policy/testdata/executor")
		cmd.Dir = root
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			executorErr = fmt.Errorf("构建独立执行进程失败: %v\n%s", buildErr, out)
			return
		}
		executorBin = bin
	})
	if executorErr != nil {
		t.Fatalf("%v", executorErr)
	}
	return executorBin
}

// policyRepoRoot 从 internal/policy 回溯到仓库根。
func policyRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		return "", fmt.Errorf("定位仓库根失败（%s 下没有 go.mod）: %w", root, statErr)
	}
	return root, nil
}

// runExecutor 让独立进程真正执行一次副作用，并由它自己记账。
//
// 父进程只等它退出；账本内容一律事后从磁盘读回。
func runExecutor(t *testing.T, bin, logPath, label, tool, target, content string) {
	t.Helper()
	cmd := exec.Command(bin,
		"-log", logPath,
		"-label", label,
		"-tool", tool,
		"-path", target,
		"-content", content,
	)
	cmd.Dir = filepath.Dir(target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("独立执行进程失败（%s → %s）: %v\n%s", label, target, err, out)
	}
}
