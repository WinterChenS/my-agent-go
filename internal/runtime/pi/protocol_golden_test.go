package pi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProtocolGolden 校验 testdata/v1.5.0 下的真实捕获记录。
//
// 与 TestCaptureRealPiProtocol 的分工：
//   - 捕获测试需要本机装有 Pi 1.5.0，默认跳过，负责"生成"记录；
//   - 本测试只读已提交的记录做结构与序列断言，任何机器都能跑，负责"钉住"协议形状。
//
// 这些记录由真实 Pi 1.5.0 进程产出，provider 是受控 fixture（不请求任何真实模型）。
// 断言值来自实测，不是从文档抄来的期望值。
func TestProtocolGolden(t *testing.T) {
	t.Run("所有记录都是合法 JSON", func(t *testing.T) {
		for _, name := range []string{"text", "unicode", "tool", "empty", "provider-error", "long", "mcp", "extension", "compaction"} {
			recs := loadRecords(t, name)
			if len(recs) == 0 {
				t.Fatalf("%s.jsonl 为空", name)
			}
		}
	})

	t.Run("文本轮次的事件序列", func(t *testing.T) {
		recs := loadRecords(t, "text")
		want := []string{
			"response",
			"agent_start",
			"turn_start",
			"message_update/text_start",
			"message_update/text_delta",
			"message_update/text_delta",
			"message_update/text_end",
			"turn_end",
			"agent_end",
			"agent_settled",
		}
		// 去掉 system/user 消息的 start/end，只保留协议骨架。
		got := filterOut(eventTypes(recs), "message_start", "message_end")
		if err := containsSubsequence(got, want); err != nil {
			t.Fatalf("事件序列不符合预期：%v\n实际：%v", err, got)
		}
	})

	t.Run("prompt 响应先于事件流且 disposition=started", func(t *testing.T) {
		recs := loadRecords(t, "text")
		var promptIdx, firstEventIdx = -1, -1
		for i, rec := range recs {
			if rec.Type == "response" && rec.Command == "prompt" {
				promptIdx = i
				disp, _ := rec.Data["disposition"].(string)
				if disp != "started" {
					t.Fatalf("disposition 应为 started，实际 %q", disp)
				}
			}
			if rec.Type == "agent_start" && firstEventIdx < 0 {
				firstEventIdx = i
			}
		}
		if promptIdx < 0 {
			t.Fatal("没有找到 prompt 的 response")
		}
		if firstEventIdx >= 0 && promptIdx > firstEventIdx {
			t.Fatalf("prompt 响应应在 agent_start 之前，实际 %d vs %d", promptIdx, firstEventIdx)
		}
	})

	// U+2028 是合法 JSON 字符串内容，不能被当作记录边界。
	// 这条断言同时证明：分帧必须只在 LF 处切分，不能用会按 U+2028 断行的通用行读取器。
	t.Run("U+2028 不会切断记录", func(t *testing.T) {
		recs := loadRecords(t, "unicode")
		var sb strings.Builder
		for _, rec := range recs {
			if rec.EventType() != "message_update/text_delta" {
				continue
			}
			d, _ := rec.AssistantEvent["delta"].(string)
			sb.WriteString(d)
		}
		want := "中文多字节😀行分隔\u2028不该断行制表\t和\r\n回车结尾"
		if got := sb.String(); got != want {
			t.Fatalf("增量拼接结果不符\n期望：%q\n实际：%q", want, got)
		}
		if !strings.Contains(sb.String(), "\u2028") {
			t.Fatal("拼接结果缺少 U+2028，说明该场景未被真实诱发")
		}
	})

	// 工具调用必须收敛：agent_settled 之前只发生一轮工具执行。
	// 若 fixture 在收到工具结果后仍索要工具，历史上出现过 122 轮死循环。
	t.Run("工具调用收敛且事件齐全", func(t *testing.T) {
		recs := loadRecords(t, "tool")
		counts := eventCounts(recs)
		for _, k := range []string{
			"message_update/toolcall_start",
			"message_update/toolcall_delta",
			"message_update/toolcall_end",
			"tool_execution_start",
			"tool_execution_end",
		} {
			if counts[k] == 0 {
				t.Fatalf("缺少事件 %s", k)
			}
		}
		if counts["message_update/toolcall_start"] != 1 {
			t.Fatalf("toolcall_start 应为 1 轮，实际 %d（工具循环未收敛）", counts["message_update/toolcall_start"])
		}
		if counts["tool_execution_end"] != 1 {
			t.Fatalf("tool_execution_end 应为 1，实际 %d", counts["tool_execution_end"])
		}

		// 顺序约束：toolcall_end 必须在工具执行开始之前。
		order := eventTypes(recs)
		iCallEnd := indexOf(order, "message_update/toolcall_end")
		iExecStart := indexOf(order, "tool_execution_start")
		if iCallEnd < 0 || iExecStart < 0 || iCallEnd > iExecStart {
			t.Fatalf("顺序错误：toolcall_end 应在 tool_execution_start 之前（%d vs %d）", iCallEnd, iExecStart)
		}
	})

	t.Run("空响应仍进入 idle", func(t *testing.T) {
		recs := loadRecords(t, "empty")
		counts := eventCounts(recs)
		if counts["message_update/text_delta"] != 0 {
			t.Fatalf("空响应场景不应有 text_delta，实际 %d", counts["message_update/text_delta"])
		}
		if counts["agent_settled"] != 1 {
			t.Fatalf("空响应仍必须进入 agent_settled，实际 %d", counts["agent_settled"])
		}
	})

	// provider 失败会走 Pi 自己的重试策略；客户端不能把它当作成功。
	t.Run("provider 失败触发自动重试并最终收敛", func(t *testing.T) {
		recs := loadRecords(t, "provider-error")
		counts := eventCounts(recs)
		if counts["auto_retry_start"] < 1 {
			t.Fatalf("应观察到 auto_retry_start，实际 %d", counts["auto_retry_start"])
		}
		if counts["auto_retry_end"] != 1 {
			t.Fatalf("auto_retry_end 应为 1，实际 %d", counts["auto_retry_end"])
		}
		last := eventTypes(recs)[len(eventTypes(recs))-1]
		if last != "agent_settled" {
			t.Fatalf("重试结束后应以 agent_settled 收尾，实际 %s", last)
		}
	})

	// 扩展加载的可见结果：扩展注册的命令出现在 get_commands 响应里。
	t.Run("扩展注册的命令出现在 get_commands", func(t *testing.T) {
		recs := loadRecords(t, "extension")
		var found bool
		for _, rec := range recs {
			if rec.Type != "response" || rec.Command != "get_commands" {
				continue
			}
			if !rawContainsName(rec.Data, "fixture-ext") {
				t.Fatalf("get_commands 响应里没有扩展注册的命令 fixture-ext")
			}
			found = true
		}
		if !found {
			t.Fatal("没有找到 get_commands 的 response")
		}
	})

	// 压缩事件的字段是 F03/F05 的解析依据。
	// 本次捕获到的是"压缩失败"的真实记录：fixture 会话不足以产生可压缩内容。
	// 成功压缩未被诱发，此处如实断言失败字段的存在，不伪造成功。
	t.Run("压缩事件字段完整（实测为失败压缩）", func(t *testing.T) {
		recs := loadRecords(t, "compaction")
		var start, end map[string]any
		for _, rec := range recs {
			switch rec.Type {
			case "compaction_start":
				start = rec.Raw
			case "compaction_end":
				end = rec.Raw
			}
		}
		if start == nil || end == nil {
			t.Fatal("缺少 compaction_start 或 compaction_end")
		}
		if r, _ := start["reason"].(string); r != "manual" {
			t.Fatalf("compaction_start.reason 应为 manual，实际 %q", r)
		}
		for _, k := range []string{"reason", "aborted", "willRetry"} {
			if _, ok := end[k]; !ok {
				t.Fatalf("compaction_end 缺少字段 %s", k)
			}
		}
		if msg, _ := end["errorMessage"].(string); msg == "" {
			t.Fatal("本次为失败压缩，errorMessage 不应为空；若已能成功压缩，需更新本断言与证据文档")
		}
	})
}

// goldenRecord 是 testdata 里一条记录的解析结果。
type goldenRecord struct {
	Type           string
	Command        string
	Success        *bool
	ID             string
	Data           map[string]any
	AssistantEvent map[string]any
	// Raw 保留整条记录的顶层字段：compaction_start/end 等事件的字段在顶层而非 data 下。
	Raw map[string]any
}

// EventType 返回带 assistantMessageEvent 子类型的事件名，如 message_update/text_delta。
func (r goldenRecord) EventType() string {
	if r.AssistantEvent != nil {
		if sub, ok := r.AssistantEvent["type"].(string); ok {
			return r.Type + "/" + sub
		}
	}
	return r.Type
}

func loadRecords(t *testing.T, name string) []goldenRecord {
	t.Helper()
	path := filepath.Join("testdata", "v1.5.0", name+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开 %s 失败（先运行 MYAGENT_PI_CAPTURE=1 的捕获测试生成）: %v", path, err)
	}
	defer f.Close()

	var out []goldenRecord
	sc := bufio.NewScanner(f)
	// long/bulk 场景单行可达数百 KB，默认 64KB 上限不够。
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("%s 第 %d 行不是合法 JSON: %v", name, lineNo, err)
		}
		rec := goldenRecord{Type: str(m["type"]), Command: str(m["command"]), ID: str(m["id"]), Raw: m}
		if v, ok := m["success"].(bool); ok {
			rec.Success = &v
		}
		if d, ok := m["data"].(map[string]any); ok {
			rec.Data = d
		}
		if ev, ok := m["assistantMessageEvent"].(map[string]any); ok {
			rec.AssistantEvent = ev
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return out
}

// rawContainsName 在任意嵌套结构里查找 name 字段等于 target 的对象，
// 用于在不写死响应形状的前提下核对命令是否被注册（Pi 的命令结构可能后续演进）。
func rawContainsName(v any, target string) bool {
	switch x := v.(type) {
	case map[string]any:
		if n, _ := x["name"].(string); n == target {
			return true
		}
		for _, vv := range x {
			if rawContainsName(vv, target) {
				return true
			}
		}
	case []any:
		for _, vv := range x {
			if rawContainsName(vv, target) {
				return true
			}
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func eventTypes(recs []goldenRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.EventType())
	}
	return out
}

func eventCounts(recs []goldenRecord) map[string]int {
	m := map[string]int{}
	for _, r := range recs {
		m[r.EventType()]++
	}
	return m
}

func filterOut(in []string, drop ...string) []string {
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !dropSet[s] {
			out = append(out, s)
		}
	}
	return out
}

func indexOf(in []string, target string) int {
	for i, s := range in {
		if s == target {
			return i
		}
	}
	return -1
}

// containsSubsequence 判断 want 是否为 got 的子序列（允许中间夹杂其它事件）。
func containsSubsequence(got, want []string) error {
	j := 0
	for _, g := range got {
		if j < len(want) && g == want[j] {
			j++
		}
	}
	if j != len(want) {
		return fmt.Errorf("缺少第 %d 个期望事件 %q", j+1, want[j])
	}
	return nil
}
