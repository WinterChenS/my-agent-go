package browser

// 观察的限额与脱敏。
//
// 基线 §7 的三条上限（元素 300 / 文本 40000 字符 / JSON 256KiB）必须由实现自己
// 执行并**如实标注**裁剪。这里放的是与平台无关的那一半：裁剪算法、脱敏与
// 元素敏感判定都是纯函数，能在没有 WebView2 的机器上被单元测试锁死；
// native 只负责把页面数据喂进来。
//
// 关于 "truncated" 的落点：Snapshot.Truncated 刻意不进 JSON（schema 由
// schemas/browser.schema.json 串行维护，B04 不动它）。裁剪事实同时写进证据
// artifact（limits.json），MCP 层（B05）可以直接读 Go 字段把它交给模型。

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"my-agent-go/internal/contracts"
)

// RedactedPlaceholder 是敏感值被抹掉后的占位符。
//
// 用固定字符串而不是空串：空串会让"字段本来为空"和"被抹掉了"看起来一样，
// 排查泄漏时反而少一条线索。
const RedactedPlaceholder = "[redacted]"

// minSecretLen 是参与脱敏的最短敏感串长度。
//
// 低于这个长度的值（例如 "a"）会把正常文本也一起抹掉，从而让"脱敏"变成
// "看不见页面内容"。真正的密码字段通常远长于 3 个字符。
const minSecretLen = 3

// IsSecretInput 报告该元素的值是否属于"永远不得进入快照"的敏感字段。
//
// 只认 password 一种：它是浏览器层面唯一有"值不可读"语义的输入类型。
// 其他字段（text/email/tel）的值属于页面内容，需要被观察到才能填表与核验。
func (e Element) IsSecretInput() bool {
	return strings.EqualFold(e.InputType, "password")
}

// TruncateText 按 Unicode 字符（rune）上限裁剪文本。
//
// 按 rune 而不是字节：中文页面按字节截断会提前一半，且可能切碎多字节字符。
func TruncateText(s string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", s != ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:maxRunes]), true
}

// ScrubSecrets 把已知敏感串从文本里抹掉。
//
// 这是纵深防御的第二道（第一道在页面侧：密码字段的值根本不取）。它挡的是
// "页面把密码值回显到正文/标签里"这类泄漏——只靠"我们不读 value"是不够的。
func ScrubSecrets(s string, secrets []string) (string, bool) {
	changed := false
	for _, secret := range secrets {
		if len([]rune(secret)) < minSecretLen {
			continue
		}
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, RedactedPlaceholder)
			changed = true
		}
	}
	return s, changed
}

// FitSnapshotJSON 把快照收进 JSON 字节上限，并返回最终大小与是否裁剪过。
//
// 裁剪顺序是刻意的：
//  1. 先砍文本（它对元素的可用性影响最小，agent 仍能按元素动作）；
//  2. 文本见底后再从**尾部**丢元素（保留文档靠前的元素，顺序稳定、可解释）；
//  3. 到了保底数量还超限，返回 INTERNAL 错误——绝不返回一个超大 JSON 让
//     调用方自己截断（那会破坏 JSON，看起来却"有快照"）。
//
// 任何一次裁剪都会让返回的 truncated 变成 true。
func FitSnapshotJSON(s *Snapshot, maxBytes int) (size int, truncated bool, err error) {
	if maxBytes <= 0 {
		maxBytes = MaxSnapshotJSONBytes
	}
	size, err = snapshotJSONSize(s)
	if err != nil {
		return 0, false, err
	}
	if size <= maxBytes {
		return size, false, nil
	}

	// 1) 文本对半砍，至少保留 1000 字符给 agent 读。
	for size > maxBytes {
		n := utf8.RuneCountInString(s.Text)
		if n <= 1000 {
			break
		}
		s.Text = truncateRunesLocal(s.Text, n/2)
		truncated = true
		if size, err = snapshotJSONSize(s); err != nil {
			return 0, truncated, err
		}
	}
	// 2) 从尾部丢元素，每次丢 10%，至少保留 20 个（元素太少就没有可操作性了）。
	for size > maxBytes && len(s.Elements) > 20 {
		keep := len(s.Elements) - maxInt(1, len(s.Elements)/10)
		s.Elements = s.Elements[:keep]
		truncated = true
		if size, err = snapshotJSONSize(s); err != nil {
			return 0, truncated, err
		}
	}
	// 3) 文本已经砍到 1000 字符，元素好歹留着 20 个，还没进上限说明上限本身
	//    与页面规模严重不匹配：明确报错，不静默交出一个超限 JSON。
	if size > maxBytes {
		return size, truncated, appErr(contracts.CodeInternal, fmt.Sprintf(
			"快照在裁剪到 %d 个元素 / %d 字符后仍有 %d 字节，超过上限 %d：拒绝返回超限 JSON",
			len(s.Elements), utf8.RuneCountInString(s.Text), size, maxBytes))
	}
	return size, truncated, nil
}

func snapshotJSONSize(s *Snapshot) (int, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return 0, appErr(contracts.CodeInternal, "快照无法序列化: "+err.Error())
	}
	return len(b), nil
}

func truncateRunesLocal(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ScreenshotDownscaleFactors 是"受控缩小"的候选因子（严格递减，全部 < 1）。
//
// 为什么要有这条路径：4MiB 是硬上限，超了不能静默截断 base64（那会得到一张坏图，
// 却看起来"有截图"）。可选的正确做法只有两个：明确报错，或在**更新 scale** 的
// 前提下受控缩小。这里给出确定的缩小档位，让三平台的行为一致、可复核。
func ScreenshotDownscaleFactors() []float64 {
	return []float64{0.75, 0.5, 0.35, 0.25}
}

// ScreenshotFits 报告截图字节数是否在 MaxScreenshotBytes 之内。
func ScreenshotFits(size int) bool {
	return size > 0 && size <= MaxScreenshotBytes
}

// DerivedScale 用实际像素宽度反算 scale。
//
// 缩小重采样时像素尺寸可能因取整与期望值差 1，此时以**实际像素**为准：
// 截图坐标必须能回到 CSS 坐标系，否则 BR04 的 ±2 CSS px 无从核对。
func DerivedScale(pixelWidth int, cssWidth, fallback float64) float64 {
	if pixelWidth > 0 && cssWidth > 0 {
		return float64(pixelWidth) / cssWidth
	}
	if fallback > 0 {
		return fallback
	}
	return 1
}
