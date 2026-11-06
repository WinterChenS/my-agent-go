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
var compiled = compileRules(specs)

// compileRules 把规则表编译成可执行形式；顺序与输入一致。
func compileRules(rules []Rule) []compiledRule {
	out := make([]compiledRule, 0, len(rules))
	for _, spec := range rules {
		out = append(out, compiledRule{re: regexp.MustCompile(spec.Pattern), repl: spec.Replacement})
	}
	return out
}

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
	return apply(compiled, s)
}

// diagnosticSpecs 是诊断包额外需要的规则（C07 增量，**只增不改**）。
//
// 为什么单独一张表而不是并进 specs：specs 必须与 internal/runtime/pi/worker.go
// 的 redactionRules 逐条一致（TestRulesMatchPiWorker）。把诊断专用规则并进去
// 会让"两处同源"的告警失效，而 worker.go 在 C06e/C07 的边界外。
//
// 增量覆盖的是基础规则认不出、诊断包里却真的会出现的形态：
//   - 不带 Bearer 的 Authorization（Basic / 裸 token / 自定义 scheme）；
//   - X-Api-Key / Cookie 这类头；
//   - 连接串里的口令（scheme://user:secret@host）；
//   - 口令字段（password / access_token / signature / credential）；
//   - URL query 里 key / sig / signature 这类参数。
//
// 原则仍然是"宁可多脱敏"：诊断包的用途是定位故障，不是还原请求。
//
// 字符集里的 `"` 与 `\` 不是随手写的：诊断包的文档是 JSON 文本，转义后的
// 换行是 `\n` 两个字符。若值字符集允许 `\` 与 `"`，一条规则就会从"某个 query
// 参数的值"一路吃到下一个真实空白位——既丢掉后面的可定位文本，也可能把
// JSON 结构吃掉。这些规则在**整份文档文本**上也会跑，所以必须对 JSON 友好。
var diagnosticSpecs = []Rule{
	// `(?:\w+\s+)?` 保留 scheme（Bearer/Basic/...）：方案名本身是定位线索，
	// 秘密只是它后面那一段。没有 scheme 的裸 token 也会命中。
	{Pattern: `(?i)((?:proxy-)?authorization\s*:\s*(?:\w+\s+)?)[^\s"]+`, Replacement: "${1}***"},
	// JSON 形态的 authorization 头（`"authorization": "Bearer ..."`）：键后的
	// 引号挡住了上面那条规则，而基础规则只认 bearer 前缀且同样过不了引号，
	// 不补这一条就会漏掉真正的 header 泄露形态。
	{Pattern: `(?i)("(?:proxy-)?authorization"\s*:\s*"(?:[A-Za-z][A-Za-z0-9+.\-]*\s+)?)[^"]+`, Replacement: "${1}***"},
	{Pattern: `(?i)((?:x-api-key|x-auth-token|x-access-token|api-key)\s*:\s*)[^\s"]+`, Replacement: "${1}***"},
	{Pattern: `(?i)((?:set-)?cookie\s*:\s*)[^"\r\n\\]+`, Replacement: "${1}***"},
	// 连接串的 userinfo 段：只替换口令，用户名保留（定位需要）。
	{Pattern: `(?i)([a-z][a-z0-9+.\-]*://[^/\s:@]+:)[^@\s/]+@`, Replacement: "${1}***@"},
	{Pattern: `(?i)("?(?:password|passwd|pwd|access[_-]?token|refresh[_-]?token|id[_-]?token|signature|credential)"?\s*[:=]\s*"?)[A-Za-z0-9_\-.\/+=]{6,}`, Replacement: "${1}***"},
	// JSON 形态的敏感键：值里可能含 base64 的 `+/=`，上面那条的字集盖不住。
	{Pattern: `(?i)("(?:api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|token|secret|password)"\s*:\s*"(?:[A-Za-z][A-Za-z0-9+.\-]*\s+)?)[^"]+`, Replacement: "${1}***"},
	// 基础规则已经覆盖 token/secret/api_key 三种 query 参数；这里补其余常见名字。
	{Pattern: `(?i)([?&](?:access_token|id_token|refresh_token|api_key|apikey|key|sig|signature|password)=)[^&\s#\\"]+`, Replacement: "${1}***"},
}

// diagnosticCompiled 是 diagnosticSpecs 的编译结果；与 diagnosticSpecs 一一对应。
var diagnosticCompiled = compileRules(diagnosticSpecs)

// DiagnosticRules 返回诊断专用规则的只读副本（顺序与执行顺序一致）。
//
// 与 Rules() 分开：基础表必须与 Pi worker 逐条一致，诊断增量不在其中。
func DiagnosticRules() []Rule {
	out := make([]Rule, len(diagnosticSpecs))
	copy(out, diagnosticSpecs)
	return out
}

// DiagnosticsText 先跑产品基础规则，再跑诊断增量规则。
//
// 复用的是同一份基础表（不是另写一套），增量只是补形态；返回值与 Text 同语义：
// 脱敏后的文本 + 命中总次数。
func DiagnosticsText(s string) (string, int) {
	out, hits := apply(compiled, s)
	more, extra := apply(diagnosticCompiled, out)
	return more, hits + extra
}

// apply 逐条执行规则表，返回结果与命中次数。
func apply(rules []compiledRule, s string) (string, int) {
	if s == "" {
		return s, 0
	}
	hits := 0
	for _, rule := range rules {
		if n := len(rule.re.FindAllStringIndex(s, -1)); n > 0 {
			hits += n
			s = rule.re.ReplaceAllString(s, rule.repl)
		}
	}
	return s, hits
}
