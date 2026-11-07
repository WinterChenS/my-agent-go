package app

// U00：环境变量品牌迁移（ANVIL_* 优先，MYAGENT_* 保留一个开发兼容周期）。
//
// 这里守的是"兼容"的两个方向：
//   - 只设旧变量：**仍然生效**（否则所有既有脚本、CI、证据命令一夜失效）；
//   - 同时设两个：新变量优先，并且旧变量被使用这件事必须留痕，
//     否则兼容周期永远不会结束——没人知道还有谁在用旧名字。

import (
	"os"
	"strings"
	"sync"
	"testing"
)

func TestEnvLookupPrefersAnvilAndWarnsOnce(t *testing.T) {
	resetEnvDeprecations()
	t.Cleanup(resetEnvDeprecations)

	t.Setenv("ANVIL_U00_PROBE", "new")
	t.Setenv("MYAGENT_U00_PROBE", "old")

	value, source := EnvLookup("U00_PROBE")
	if value != "new" {
		t.Fatalf("ANVIL_ 必须优先，实际 %q", value)
	}
	if source != "ANVIL_U00_PROBE" {
		t.Fatalf("source 应指出实际命中的变量名，实际 %q", source)
	}
	if got := EnvDeprecations(); len(got) != 0 {
		t.Fatalf("命中新变量不该记录弃用: %v", got)
	}

	// 只有旧变量：值照用，但每一次"变量名首次被命中"都要留下一条弃用记录，
	// 且只留一条——每次读取都写日志会把日志刷成噪音。
	if err := os.Unsetenv("ANVIL_U00_PROBE"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	for i := 0; i < 3; i++ {
		v, src := EnvLookup("U00_PROBE")
		if v != "old" {
			t.Fatalf("第 %d 次读取旧变量失败: %q", i, v)
		}
		if src != "MYAGENT_U00_PROBE" {
			t.Fatalf("source 应标出旧变量名，实际 %q", src)
		}
	}
	got := EnvDeprecations()
	if len(got) != 1 {
		t.Fatalf("同一旧变量只应记录一次，实际 %v", got)
	}
	if !strings.Contains(got[0], "MYAGENT_U00_PROBE") || !strings.Contains(got[0], "ANVIL_U00_PROBE") {
		t.Fatalf("弃用记录要同时给出旧名与新名: %q", got[0])
	}

	// 都不设：返回空值 + 空来源，不编造"默认值来自某个变量"。
	if err := os.Unsetenv("MYAGENT_U00_PROBE"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	v, src := EnvLookup("U00_PROBE")
	if v != "" || src != "" {
		t.Fatalf("未设置时应返回空: value=%q source=%q", v, src)
	}
}

// TestEnvLookupTrimsAndIgnoresBlank 守住"空字符串不算设置"：
// CI 里常见的 `MYAGENT_X=` 会导出空值，把它当成有效值会把工作区路径清空。
func TestEnvLookupTrimsAndIgnoresBlank(t *testing.T) {
	resetEnvDeprecations()
	t.Cleanup(resetEnvDeprecations)

	t.Setenv("ANVIL_U00_BLANK", "   ")
	t.Setenv("MYAGENT_U00_BLANK", " from-legacy ")
	v, src := EnvLookup("U00_BLANK")
	if v != "from-legacy" {
		t.Fatalf("空白的新变量应被忽略，实际 %q", v)
	}
	if src != "MYAGENT_U00_BLANK" {
		t.Fatalf("source 不符: %q", src)
	}
}

// TestEnvLookupIsConcurrencySafe 守住弃用记录表。
//
// 多个服务在启动时并发送行读取环境变量；没有锁的话这张表会随机 panic
// （concurrent map writes），而 panic 发生在启动路径上，表现为"应用偶尔打不开"。
func TestEnvLookupIsConcurrencySafe(t *testing.T) {
	resetEnvDeprecations()
	t.Cleanup(resetEnvDeprecations)

	t.Setenv("MYAGENT_U00_RACE", "v")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				if v, _ := EnvLookup("U00_RACE"); v != "v" {
					t.Errorf("并发读取值不符: %q", v)
				}
				_ = EnvDeprecations()
			}
		}()
	}
	wg.Wait()
	if got := EnvDeprecations(); len(got) != 1 {
		t.Fatalf("并发下也应只记录一次: %v", got)
	}
}
