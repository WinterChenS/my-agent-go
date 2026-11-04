package resources

// 本文件是"API 白名单来自认证 Pi 版本"的证据，而不是一句注释承诺。
//
// 它直接扫描已安装 Pi 1.5.0 的 bundle，把里面真实存在的 API 分发名与我们的
// 白名单对照：
//   - 白名单里的每一项都必须在 bundle 里存在（防止凭空写一个看似合理的值）；
//   - bundle 里出现的每一项都必须被明确收录或明确排除（Pi 升级新增 API 时
//     测试会红，迫使我们复核，而不是默默漏掉）。
//
// 需要装有认证 Pi 的环境（与 worker 集成测试同一个开关）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestSupportedAPIsMatchInstalledPiBundle(t *testing.T) {
	if os.Getenv("MYAGENT_PI_WORKER_INTEGRATION") != "1" {
		t.Skip("设置 MYAGENT_PI_WORKER_INTEGRATION=1 才核对已安装 Pi 的 API 分发")
	}
	chunks := piBundleChunks(t)
	found := map[string]bool{}
	for _, f := range chunks {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s: %v", f, err)
		}
		for _, m := range apiLiteral.FindAllStringSubmatch(string(data), -1) {
			found[m[1]] = true
		}
	}
	if len(found) == 0 {
		t.Fatalf("没有从 Pi bundle 里提取到任何 API 名，扫描规则需要更新（%d 个文件）", len(chunks))
	}

	for _, api := range supportedAPIs {
		if !found[api] {
			t.Errorf("白名单里的 %q 不在已安装 Pi 的 API 分发里：这是凭空写的值", api)
		}
	}

	var unknown []string
	for api := range found {
		if apiExists(api) {
			continue
		}
		if _, ok := excludedAPIs[api]; ok {
			continue
		}
		unknown = append(unknown, api)
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Fatalf("Pi bundle 里有未复核的 API：%v；请确认它是对话协议后加入 supportedAPIs 或 excludedAPIs", unknown)
	}

	// 分类器/图像接口不能被误收录成对话模型。
	for _, api := range []string{"openai-decisions", "typesafe-system-one", "openrouter-images"} {
		if apiExists(api) {
			t.Fatalf("%q 不是对话协议，不能出现在 supportedAPIs", api)
		}
	}
}

// apiLiteral 匹配 bundle 里 `api: "openai-completions"` 这类字面量。
var apiLiteral = regexp.MustCompile(`api: *"([a-z0-9][a-z0-9-]*)"`)

// piBundleChunks 找到已安装 Pi 的 bundle chunk 文件。
func piBundleChunks(t *testing.T) []string {
	t.Helper()
	root := repoRootForTest(t)
	matches, err := filepath.Glob(filepath.Join(root, ".tools", "pi", "*",
		"node_modules", "@earendil-works", "pi-coding-agent", "dist", "bundle", "chunks", "*.js"))
	if err != nil {
		t.Fatalf("查找 Pi bundle: %v", err)
	}
	if len(matches) == 0 {
		t.Skip("本机没有 .tools/pi 安装件，跳过 API 分发核对（CI 的 Pi 集成 job 会跑）")
	}
	return matches
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未找到仓库根目录（从 %s 向上）", dir)
		}
		dir = parent
	}
}

// TestExcludedAPIsHaveReasons 保证豁免名单不是空的"万能通行证"。
func TestExcludedAPIsHaveReasons(t *testing.T) {
	for api, reason := range excludedAPIs {
		if strings.TrimSpace(reason) == "" {
			t.Fatalf("排除的 API %q 没有写明原因", api)
		}
		if apiExists(api) {
			t.Fatalf("%q 同时在白名单与排除名单里，判定矛盾", api)
		}
	}
}
