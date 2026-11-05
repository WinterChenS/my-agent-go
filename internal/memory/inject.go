package memory

// C05 的注入与防误存（MEM03/MEM05）。
//
// 两条边界：
//   - 注入块必须自报"这是参考数据、不是系统指令"，并逐条带来源；
//     它不能升级成系统提示，也不能改写用户原文。
//   - 形如凭证的文本拒绝写入记忆，错误信息只说明"像哪一类凭证"，
//     不回显内容本身——否则拒绝提示本身就是一次泄漏。

import (
	"regexp"
)

// secretPatterns 按"先具体后宽泛"排列，命中的第一个作为拒绝原因。
var secretPatterns = []struct {
	reason  string
	pattern *regexp.Regexp
}{
	{"私钥块（BEGIN PRIVATE KEY）", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{12,}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`)},
	{"OpenAI 风格 API key", regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`)},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`)},
	{"Authorization 头", regexp.MustCompile(`(?i)\bauthorization\s*:\s*(bearer|basic|token)\s+\S{8,}`)},
	{"形如 key=value 的凭证", regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|secret[_-]?key|refresh[_-]?token|client[_-]?secret|private[_-]?key|password|passwd|token)\b\s*[:=]\s*["']?[^\s"']{8,}`)},
}

// DetectSecret 返回"这段文本像哪一类凭证"；空串表示未命中。
//
// 只返回类别名，不回显匹配到的内容：拒绝原因会进 UI、日志与测试输出。
func DetectSecret(content string) string {
	for _, p := range secretPatterns {
		if p.pattern.MatchString(content) {
			return p.reason
		}
	}
	return ""
}

// AugmentPrompt 把记忆参考块放到用户消息前面，用户原文原样保留在末尾。
//
// 参考块由 renderContextBlock 从条目重建，而不是直接信任 mc.Text：这样
// “预览里的块”和“真正发给模型的块”只有一个渲染点，也不会因为调用方
// 手工拼了一个没有声明的 Context 就丢掉“不是系统指令”的说明。
//
// 没有记忆时不改写 prompt：空的 <memory-context> 只会让模型以为
// "这次找不到记忆"是系统给它的信息，而不是产品没有可注入的数据。
func AugmentPrompt(userMessage string, mc MemoryContext) string {
	if len(mc.Entries) == 0 {
		return userMessage
	}
	return renderContextBlock(mc.Entries) + "\n\n" + userMessage
}
