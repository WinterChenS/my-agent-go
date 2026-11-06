// Package redact 是产品共享的脱敏规则（日志、诊断包、会话导出共用一份）。
//
// 规则来源与边界
//   - 这张表原本只存在于 internal/runtime/pi/worker.go 的 redactionRules（未导出，
//     用于 stderr 环形缓冲脱敏）。C06e 需要同一组规则来脱敏会话导出，而任务边界
//     不允许改 internal/runtime/pi，所以这里把那张表**逐条照搬**过来（不是另写
//     一套新规则）：pattern 与 replacement 都与 worker.go 字面一致。
//   - redact_test.go 的 TestRulesMatchPiWorker 直接读 worker.go 比对字面量。
//     任何一侧改规则而另一侧没跟着改，测试立刻失败——这是两处暂时同源时的漂移
//     告警，而不是"两份规则各管一段"的许可。
//
// 收敛方向（需要对应任务批准，见 C06e 报告）
//   - internal/runtime/pi/worker.go 的 redactText 改为调用本包 Text，删掉自己的表；
//   - C07 的 internal/diagnostics/redact.go 应当复用本包，而不是再写一张表。
//
// 边界声明：这是"尽力而为"的文本脱敏，不能替代"不把密钥写进日志/导出"。
// 规则之外的自定义凭证格式仍可能漏过。
package redact

import "regexp"

// Rule 是一条脱敏规则：正则 + 替换串（支持 ${1} 形式的分组引用）。
type Rule struct {
	Pattern     string
	Replacement string
}

// specs 是规则表本体，顺序即执行顺序（与 internal/runtime/pi/worker.go 一致）。
var specs = []Rule{
	{Pattern: `sk-[A-Za-z0-9_\-]{8,}`, Replacement: "sk-***"},
	{Pattern: `(?i)(authorization:\s*bearer\s+)\S+`, Replacement: "${1}***"},
	{Pattern: `(?i)("?(?:api[_-]?key|token|secret)"?\s*[:=]\s*"?)[A-Za-z0-9_\-.]{8,}`, Replacement: "${1}***"},
	{Pattern: `(?i)([A-Za-z]:\\Users\\)[^\\\s]+`, Replacement: "${1}<user>"},
	{Pattern: `(/home/)[^/\s]+`, Replacement: "${1}<user>"},
}

// compiled 是 specs 的编译结果；与 specs 一一对应。
var compiled = func() []compiledRule {
	out := make([]compiledRule, 0, len(specs))
	for _, spec := range specs {
		out = append(out, compiledRule{re: regexp.MustCompile(spec.Pattern), repl: spec.Replacement})
	}
	return out
}()

type compiledRule struct {
	re   *regexp.Regexp
	repl string
}

// Rules 返回规则表的只读副本（顺序与执行顺序一致）。
func Rules() []Rule {
	out := make([]Rule, len(specs))
	copy(out, specs)
	return out
}

// Text 按产品规则脱敏文本，返回脱敏结果与命中次数。
//
// 命中次数是给证据/诊断用的："导出里有 3 处脱敏"比"应该脱敏了"可核查。
func Text(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	hits := 0
	for _, rule := range compiled {
		if n := len(rule.re.FindAllStringIndex(s, -1)); n > 0 {
			hits += n
			s = rule.re.ReplaceAllString(s, rule.repl)
		}
	}
	return s, hits
}
