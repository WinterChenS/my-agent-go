package redact

// 脱敏规则测试。
//
// 这里守两件事：
//   - 每条规则在自己的典型写法上真的命中，并且不会把整段文本吞掉；
//   - 规则表与 internal/runtime/pi/worker.go 里的 redactionRules **逐条一致**：
//     C06e 不允许改 internal/runtime/pi，所以本包照搬了那张表；一旦任何一侧
//     改了规则而另一侧没跟着改，TestRulesMatchPiWorker 立刻失败。

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestTextRedactsKnownSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"sk 风格 key", "key sk-live-abcdef1234567890 结束", "key sk-*** 结束"},
		{"bearer 头", "Authorization: Bearer abc.def-123", "Authorization: Bearer ***"},
		{"api_key 赋值", `config.api_key = "abcdef123456789"`, `config.api_key = "***"`},
		{"token 查询参数", "https://x.test/v1?token=abcdef123456789", "https://x.test/v1?token=***"},
		{"secret 冒号", "secret: abcdef123456789", "secret: ***"},
		{"windows 路径用户名", `C:\Users\alice\AppData\Local\x.log`, `C:\Users\<user>\AppData\Local\x.log`},
		{"home 路径用户名", "/home/alice/.pi/config", "/home/<user>/.pi/config"},
	}
	for _, tc := range cases {
		got, n := Text(tc.in)
		if got != tc.want {
			t.Fatalf("%s：期望 %q，实际 %q", tc.name, tc.want, got)
		}
		if n == 0 {
			t.Fatalf("%s：命中计数为 0", tc.name)
		}
	}
}

func TestTextLeavesHarmlessTextAlone(t *testing.T) {
	in := "普通的一句话：把 README 改一下，不要外泄。"
	got, n := Text(in)
	if got != in || n != 0 {
		t.Fatalf("无秘密的文本不应被改动：%q (n=%d)", got, n)
	}
	if _, n := Text(""); n != 0 {
		t.Fatalf("空串不应有命中: %d", n)
	}
}

// piRuleRe 匹配 worker.go 里的 `{regexp.MustCompile(`...`), "..."},` 形态。
var piRuleRe = regexp.MustCompile("\\{regexp\\.MustCompile\\(`([^`]+)`\\), \"((?:[^\"\\\\]|\\\\.)*)\"\\}")

// TestRulesMatchPiWorker 证明本包的规则不是"另写一套"，而是 pi 那张表的同一份。
//
// 读源文件而不是调用函数，是因为 internal/runtime/pi 的 redactionRules 未导出，
// 而 C06e 的边界不允许改那个包。这不是运行时依赖：只是编译期/测试期的漂移告警。
func TestRulesMatchPiWorker(t *testing.T) {
	const src = "../runtime/pi/worker.go"
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读不到 %s（规则来源必须可核对）: %v", src, err)
	}
	var want []Rule
	for _, m := range piRuleRe.FindAllStringSubmatch(string(raw), -1) {
		repl, uerr := strconv.Unquote(`"` + m[2] + `"`)
		if uerr != nil {
			t.Fatalf("无法解析替换串 %q: %v", m[2], uerr)
		}
		want = append(want, Rule{Pattern: m[1], Replacement: repl})
	}
	if len(want) == 0 {
		t.Fatalf("%s 里没有找到任何 redactionRules 条目（规则来源已变，必须重新核对）", src)
	}
	got := Rules()
	if len(got) != len(want) {
		t.Fatalf("规则条数不一致：internal/redact %d 条，%s %d 条", len(got), src, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条规则与 %s 分叉：\n internal/redact: %+v\n %s: %+v\n"+
				"两处必须同时改（收敛方向：pi 直接调用 redact.Text）", i, src, got[i], src, want[i])
		}
	}
	if !strings.Contains(string(raw), "redactText") {
		t.Fatalf("%s 里的脱敏入口改名了，本包的来源注释需要同步", src)
	}
}
