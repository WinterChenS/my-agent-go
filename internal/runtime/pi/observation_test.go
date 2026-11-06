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
	"encoding/json"
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
