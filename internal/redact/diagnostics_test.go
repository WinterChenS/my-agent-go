package redact

// 诊断包专用规则的测试（C07 增量）。
//
// 边界与 C06e 一致：**不另写一套规则表**。DiagnosticsText 先跑产品基础规则
// （specs，与 internal/runtime/pi/worker.go 逐条同源），再跑这里的增量规则——
// 增量只补"基础规则没覆盖、但诊断包里真的会出现"的形态：不带 Bearer 的
// Authorization 头、X-Api-Key/Cookie 头、连接串口令、口令字段、
// URL query 里的 key/sig 参数。
//
// TestRulesMatchPiWorker 仍然只比对 Rules()（基础表）；本文件的增量规则
// 有独立的用例，两者不会互相掩护。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticsTextRedactsHeadersAndSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "基础规则的 bearer 头仍然生效",
			in:   "Authorization: Bearer sk-live-abcdef1234567890",
			// 基础规则先把 `sk-live-...` 换成 `sk-***`，诊断规则再把 bearer 后面的
			// 整段秘密换掉；scheme（Bearer）作为定位线索保留。
			want: "Authorization: Bearer ***",
		},
		{
			name: "Basic 认证（基础规则不认）",
			in:   "Authorization: Basic dXNlcjpwYXNzd29yZA==",
			want: "Authorization: Basic ***",
		},
		{
			name: "裸 Authorization token（没有 scheme）",
			in:   "Authorization: raw-secret-token-1234",
			want: "Authorization: ***",
		},
		{
			name: "X-Api-Key 头",
			in:   "X-Api-Key: abcdef1234567890",
			want: "X-Api-Key: ***",
		},
		{
			name: "Cookie 头",
			in:   "Cookie: session=abcdef123456; theme=dark",
			want: "Cookie: ***",
		},
		{
			name: "连接串里的口令",
			in:   "postgres://anvil:hunter2secret@db.local:5432/app",
			want: "postgres://anvil:***@db.local:5432/app",
		},
		{
			name: "URL query 里的 key 参数",
			in:   "https://api.test/v1/models?key=abcd1234efgh&limit=2",
			want: "https://api.test/v1/models?key=***&limit=2",
		},
		{
			name: "口令字段",
			in:   `{"password": "correct-horse-battery"}`,
			want: `{"password": "***"}`,
		},
		{
			name: "基础规则的 token query 仍然生效",
			in:   "https://x.test/v1?token=abcdef123456789",
			want: "https://x.test/v1?token=***",
		},
	}
	for _, tc := range cases {
		got, hits := DiagnosticsText(tc.in)
		if got != tc.want {
			t.Fatalf("%s：期望 %q，实际 %q", tc.name, tc.want, got)
		}
		if hits == 0 {
			t.Fatalf("%s：命中计数为 0", tc.name)
		}
	}
}

func TestDiagnosticsTextLeavesHarmlessTextAlone(t *testing.T) {
	in := "普通诊断：run sess-1 在 2026-11-05T10:00:00Z 以 COMPATIBILITY 结束。"
	got, hits := DiagnosticsText(in)
	if got != in {
		t.Fatalf("没有秘密的文本不应被改动：%q", got)
	}
	if hits != 0 {
		t.Fatalf("没有秘密的文本命中数应为 0，实际 %d", hits)
	}
	if got, hits := DiagnosticsText(""); got != "" || hits != 0 {
		t.Fatalf("空串不应被改动：%q (hits=%d)", got, hits)
	}
}

// TestDiagnosticsTextIsIdempotent 守"脱敏结果再跑一遍不再变化"。
//
// 诊断包导出会在写完后再核对一遍；如果规则对已经脱敏的文本还会继续改写，
// 那个核对就永远不可能稳定。`Authorization: Bearer ***` 是最容易踩的一条：
// bearer 规则的 `\S+` 会把 `***` 当成新秘密再替换。
func TestDiagnosticsTextIsIdempotent(t *testing.T) {
	inputs := []string{
		"Authorization: Bearer abc.def-123",
		"Authorization: Basic dXNlcjpwYXNzd29yZA==",
		"Authorization: raw-secret-token-1234",
		"X-Api-Key: abcdef1234567890",
		"Cookie: session=abcdef123456",
		"postgres://anvil:hunter2secret@db.local:5432/app",
		"https://api.test/v1?key=abcd1234efgh&token=abcdef123456789",
		`sk-live-abcdef1234567890 C:\Users\alice\.pi\config.json /home/alice/.pi/config`,
	}
	for _, in := range inputs {
		once, _ := DiagnosticsText(in)
		twice, hits := DiagnosticsText(once)
		if twice != once {
			t.Fatalf("脱敏不幂等：\n 一次：%q\n 二次：%q", once, twice)
		}
		if hits != 0 && twice != once {
			t.Fatalf("二次命中应不改变结果：%q", in)
		}
	}
}

// TestDiagnosticRulesAreAdditive 守"增量规则没有混进基础表"。
//
// 基础表必须与 Pi worker 逐条一致（TestRulesMatchPiWorker）。如果哪天有人
// 把增量规则并进 specs，这条会先失败，提醒他两处同源的约束。
func TestDiagnosticRulesAreAdditive(t *testing.T) {
	base := Rules()
	for _, extra := range DiagnosticRules() {
		for _, r := range base {
			if r == extra {
				t.Fatalf("诊断规则 %+v 已经在基础规则表里，不应重复", extra)
			}
		}
	}
	if len(DiagnosticRules()) == 0 {
		t.Fatalf("诊断规则表为空：增量规则应当真实存在")
	}
}

// TestDiagnosticsTextHandlesJSONShapes 守"规则在 JSON 文本上不会漏、也不会越界吞"。
//
// 两个真实事故形态：
//  1. `"authorization": "Bearer ..."` —— 键值之间隔着引号，基础规则与纯文本规则
//     都过不去，必须由 JSON 形态的规则兜住；
//  2. 转义换行是 `\n` 两个字符，不是空白。值字符集若允许 `\`，一条 query 规则会
//     从参数值一路吃到下一处真实空白，把后面的可定位文本（goroutine 栈头）吞掉。
func TestDiagnosticsTextHandlesJSONShapes(t *testing.T) {
	jsonHeader := `{"headers":{"authorization":"Bearer eyJhbGciOiJIUzI1NiJ9.cGF5bG9hZA.c2ln"}}`
	got, hits := DiagnosticsText(jsonHeader)
	if strings.Contains(got, "eyJhbGciOiJIUzI1NiJ9") {
		t.Fatalf("JSON 形态的 authorization 头没有被脱敏：%s", got)
	}
	if hits == 0 {
		t.Fatalf("JSON 形态的 authorization 头命中数为 0")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("脱敏破坏了 JSON 结构: %v\n%s", err, got)
	}

	jsonWithNewline := `{"message":"GET https://x.test/v1?key=SENTINELVALUE123\ngoroutine 1 [running]:"}`
	got, _ = DiagnosticsText(jsonWithNewline)
	if !strings.Contains(got, "goroutine 1 [running]") {
		t.Fatalf("规则越界吞掉了转义换行后的文本（会破坏可定位性）：%s", got)
	}
	if strings.Contains(got, "SENTINELVALUE123") {
		t.Fatalf("query 参数值没有被脱敏：%s", got)
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("脱敏破坏了 JSON 结构：%s", got)
	}
}
