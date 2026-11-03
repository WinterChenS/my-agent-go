package pi

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"my-agent-go/internal/contracts"
)

// IsolationSpec 描述"这一次 Pi 运行允许碰到哪些项目级可执行资源"（SEC08）。
//
// 背景：Pi 会从工作目录发现一堆可以执行代码的东西——.pi/settings.json 里的设置、
// .pi/mcp.json 里的 MCP 服务器、.pi/extensions、.pi/skills、.pi/prompts、
// .pi/SYSTEM.md、祖先目录的 .agents/skills、以及 AGENTS.md/CLAUDE.md 里的指令。
// 这些内容来自"当前打开的项目"，默认等同于不可信输入（clone 一个仓库就中招）。
//
// 本产品的策略是：一律不加载项目级可执行资源，只加载我们自己显式 -e 指定的扩展。
// 项目仍然可以有自己的 AGENTS.md 之类文件，但不会被自动喂给模型。
//
// 参数来源是 Pi 1.5.0 的 CLI：
//   - -na / --no-approve          不信任项目本地配置与受保护资源
//   - -nc / --no-context-files    不加载 AGENTS.md / CLAUDE.md
//   - -ne / --no-extensions       关闭发现、配置与内置扩展（显式 -e 仍加载）
//   - -ns / --no-skills           关闭发现与配置的技能
//   - -np / --no-prompt-templates 关闭提示词模板
//   - --no-themes                 关闭主题
//   - --no-mcp                    关闭内置 MCP 支持
//
// 注意 -ne 会连带关掉内置 MCP（MCP 本身就是一个内置扩展），要保留 MCP 必须
// 再显式 `-e builtin:mcp`。这个联动在官方文档里没有一句话写清楚，是实测得出的。
type IsolationSpec struct {
	// PolicyExtension 是本产品的策略扩展路径（pi-extensions/harness-policy.ts）。
	// 必须显式 -e 加载：-ne 关掉的是"发现与配置"，显式路径不受影响。
	PolicyExtension string
	// HarnessMCP 为 true 时重新启用内置 MCP 支持（自有 MCP server 需要它）。
	HarnessMCP bool
	// ExtraExtensions 是额外显式加载的扩展（托管扩展），同样不受 -ne 影响。
	// 项目目录里的扩展永远不会出现在这里。
	ExtraExtensions []string
}

// requiredIsolationFlags 是"切断项目资源"必需参数。删任何一个都必须被判为不合格。
// 单独抽出来是为了让 ValidateArgs 能真的检查，而不是靠注释承诺。
var requiredIsolationFlags = []string{"-na", "-nc", "-ne", "-ns", "-np", "--no-themes"}

// Args 返回隔离参数。顺序固定：先隔离，再显式加载。
func (s IsolationSpec) Args() []string {
	args := append([]string{}, requiredIsolationFlags...)
	if s.HarnessMCP {
		// 显式重新启用内置 MCP；不加这句，-ne 已经把 MCP 关掉了。
		args = append(args, "-e", "builtin:mcp")
	} else {
		args = append(args, "--no-mcp")
	}
	if p := strings.TrimSpace(s.PolicyExtension); p != "" {
		args = append(args, "-e", p)
	}
	for _, e := range s.ExtraExtensions {
		if e = strings.TrimSpace(e); e != "" {
			args = append(args, "-e", e)
		}
	}
	return args
}

// IsolationChannel 是一条"项目级可执行资源"通道及其切断方式，用于证据与门禁展示。
type IsolationChannel struct {
	// Name 是通道名（项目里的具体位置）。
	Name string `json:"name"`
	// BlockedBy 是负责切断它的参数。
	BlockedBy string `json:"blockedBy"`
	// Blocked 表示当前参数集合下该通道确实被切断。
	Blocked bool `json:"blocked"`
}

// channels 是 SEC08 要求覆盖的通道清单。
//
// MCP 与 !command 两行指向同一组参数（-na / --no-mcp），但不合并：
// 它们是两条独立通道，将来任何一条换了切断方式，这里必须单独改。
var isolationChannels = []IsolationChannel{
	{Name: ".pi/settings.json（项目设置）", BlockedBy: "-na"},
	{Name: ".pi/mcp.json（项目 MCP 服务器）", BlockedBy: "-na"},
	{Name: ".pi/extensions（项目扩展）", BlockedBy: "-na"},
	{Name: ".pi/skills（项目技能）", BlockedBy: "-na"},
	{Name: ".pi/prompts（项目提示词模板）", BlockedBy: "-na"},
	{Name: ".pi/themes（项目主题）", BlockedBy: "-na"},
	{Name: ".pi/SYSTEM.md / .pi/APPEND_SYSTEM.md", BlockedBy: "-na"},
	{Name: ".agents/skills（当前及祖先目录）", BlockedBy: "-na"},
	{Name: "项目 packages（项目设置引入的包）", BlockedBy: "-na"},
	{Name: "项目设置里的 !command 凭证命令", BlockedBy: "-na"},
	{Name: "AGENTS.md / CLAUDE.md（上下文文件）", BlockedBy: "-nc"},
	{Name: "内置 MCP 自动连接", BlockedBy: "--no-mcp"},
	// 下面四条针对"配置与发现"层面的资源：-na 只管项目级，用户级/配置里列的
	// 扩展、技能、模板、主题由 -ne/-ns/-np/--no-themes 关掉。
	// 即使托管 agentDir 本该是干净的，也不依赖"本该"——参数层面直接切断。
	{Name: "配置与发现的内置/已安装扩展", BlockedBy: "-ne"},
	{Name: "配置的技能", BlockedBy: "-ns"},
	{Name: "配置的提示词模板", BlockedBy: "-np"},
	{Name: "配置的主题", BlockedBy: "--no-themes"},
}

// Coverage 返回通道覆盖表。Blocked 由实际参数推导，不是写死的 true。
func (s IsolationSpec) Coverage() []IsolationChannel { return coverageOf(s.Args()) }

// coverageOf 按一组具体参数计算覆盖情况。
// 单独成函数是为了让测试能传入"缺了某个 flag 的参数"并观察覆盖表变化——
// 若 Coverage 只读 spec.Args()，断言与被测对象同源，删参数时两边一起变，测试永远绿。
func coverageOf(args []string) []IsolationChannel {
	out := make([]IsolationChannel, 0, len(isolationChannels))
	for _, ch := range isolationChannels {
		ch.Blocked = containsArg(args, ch.BlockedBy)
		out = append(out, ch)
	}
	return out
}

// Validate 检查这套隔离参数确实成立，且策略扩展真实存在。
//
// 失败即拒绝启动：带着"项目资源还能加载"的状态跑 agent，比不跑更糟。
func (s IsolationSpec) Validate() error {
	if err := ValidateArgs(s.Args(), s); err != nil {
		return err
	}
	return s.validateExtension()
}

func (s IsolationSpec) validateExtension() error {
	p := strings.TrimSpace(s.PolicyExtension)
	if p == "" {
		return contracts.NewAppError(contracts.CodeConfigMissing,
			"未指定策略扩展（pi-extensions/harness-policy.ts）：没有它，工具调用没有执行前授权", false)
	}
	if !filepath.IsAbs(p) {
		// 相对路径会按 Pi 的 cwd 解析；cwd 是用户工作区，可被换成任意目录。
		return contracts.NewAppError(contracts.CodeConfigMissing,
			"策略扩展必须用绝对路径: "+p, false)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return contracts.NewAppError(contracts.CodeConfigMissing, "策略扩展不可读: "+p+": "+err.Error(), false)
	}
	if fi.IsDir() {
		return contracts.NewAppError(contracts.CodeConfigMissing, "策略扩展必须是文件: "+p, false)
	}
	return nil
}

// ValidateArgs 校验一组实际要交给 Pi 的参数。
//
// 单独暴露是为了能测"少一个参数会被发现"——只校验 spec.Args() 的话，
// 断言与被测对象来自同一个函数，删掉参数后仍然自洽，等于没测。
func ValidateArgs(args []string, spec IsolationSpec) error {
	var missing []string
	for _, flag := range requiredIsolationFlags {
		if !containsArg(args, flag) {
			missing = append(missing, flag)
		}
	}
	if spec.HarnessMCP {
		if !containsPair(args, "-e", "builtin:mcp") {
			missing = append(missing, "-e builtin:mcp")
		}
	} else if !containsArg(args, "--no-mcp") {
		missing = append(missing, "--no-mcp")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return contracts.NewAppError(contracts.CodeConfigMissing,
			"隔离参数不完整，项目级可执行资源仍可能加载，拒绝启动: 缺少 "+strings.Join(missing, ", "), false)
	}
	return nil
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func containsPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}
