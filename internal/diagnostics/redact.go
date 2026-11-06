package diagnostics

// 诊断包的脱敏：**复用** internal/redact，不另写一套规则。
//
// 规则来源与 C06e 的会话导出一致：先跑产品基础规则（internal/redact.Text），
// 再跑诊断增量规则（internal/redact.DiagnosticsText 里的 additive 表）。
// 本文件只负责"怎么把规则套到 JSON 文档上"，不定义任何 pattern。
//
// 为什么要分两步：
//  1. 文本层：在整个文档文本上跑规则。`{"api_key": "..."}` 这种"键与值相邻"的
//     形态只有在这一层可见——JSON 解码之后键名与值是两个互不相干的字符串；
//  2. 值层：解码后对每个字符串值再跑一遍。转义形态（`\"token\":\"...\"`）
//     在文本层看不出来，解码后才会露出真实内容。
//
// 第 1 步只在结果仍是合法 JSON 时采用：像 bearer 规则的 `\S+` 可能越过字符串
// 边界把引号一起吃掉，那样就丢掉第 1 步、交给第 2 步兜底——宁可少脱敏一次，
// 也不能产出一份解析不了的诊断包。

import (
	"bytes"
	"encoding/json"
	"fmt"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/redact"
)

// redactPayload 把一条事件载荷脱敏成可落盘的 JSON 文本。
//
// 返回 (载荷, 是否截断, 命中数)。超限时只记录"被截断"这个事实：留下半份 JSON
// 只会让读取侧解析失败，而类型/时间/事件 ID 已经足够定位。
func redactPayload(raw []byte) (string, bool, int) {
	if len(raw) == 0 {
		return "", false, 0
	}
	if len(raw) > maxEventPayloadBytes {
		return fmt.Sprintf(`{"payload_truncated":true,"bytes":%d}`, len(raw)), true, 0
	}
	text, textHits := redact.DiagnosticsText(string(raw))
	if json.Valid([]byte(text)) {
		return text, false, textHits
	}
	value, extra, err := redactJSONValue(raw)
	if err != nil {
		// 连解析都不行（不是 JSON）：把脱敏后的文本当 JSON 字符串存，仍然是合法 JSON。
		enc, encErr := json.Marshal(text)
		if encErr != nil {
			return `{"payload_unavailable":true}`, true, 0
		}
		return string(enc), false, textHits
	}
	return value, false, extra
}

// redactJSONValue 解码后再编码，对每个字符串值跑脱敏。
//
// 键名是结构，不改：改了键名等于改文档结构，读取侧就找不到字段了。
// 键名里出现秘密的形态（`"sk-..."` 当键）由文本层负责。
func redactJSONValue(raw []byte) (string, int, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 保留数字原始写法（大整数不被 float64 改写）
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", 0, err
	}
	redacted, hits := redactAny(value)
	out, err := json.Marshal(redacted)
	if err != nil {
		return "", 0, err
	}
	return string(out), hits, nil
}

// redactAny 递归脱敏 JSON 值里的所有字符串。
func redactAny(v any) (any, int) {
	switch t := v.(type) {
	case string:
		return redact.DiagnosticsText(t)
	case []any:
		hits := 0
		for i := range t {
			var n int
			t[i], n = redactAny(t[i])
			hits += n
		}
		return t, hits
	case map[string]any:
		hits := 0
		for k, val := range t {
			var n int
			t[k], n = redactAny(val)
			hits += n
		}
		return t, hits
	default:
		return v, 0
	}
}

// redactDocument 脱敏整份诊断包文档，返回 (字节, 命中数)。
//
// 输出一定是合法 JSON（两步都保证结构）；不是的话调用方必须失败，而不是
// 写出一份读不了的诊断包。
func redactDocument(raw []byte) ([]byte, int, error) {
	text, textHits := redact.DiagnosticsText(string(raw))
	candidate := []byte(text)
	if !json.Valid(candidate) {
		// 文本层破坏了结构：丢掉它，只用值层（结构优先）。
		value, extra, err := redactJSONValue(raw)
		if err != nil {
			return nil, 0, contracts.NewAppError(contracts.CodeInternal,
				"诊断包脱敏失败（原文与脱敏结果都不是合法 JSON）: "+err.Error(), false)
		}
		return []byte(value), extra, nil
	}
	value, extra, err := redactJSONValue(candidate)
	if err != nil {
		return nil, 0, contracts.NewAppError(contracts.CodeInternal, "诊断包脱敏失败: "+err.Error(), false)
	}
	return []byte(value), textHits + extra, nil
}
