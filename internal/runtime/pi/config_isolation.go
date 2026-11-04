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
// 本产品的策略分三层，不能混成一句"已切断"：
//
//  1. 项目级资源一律不加载：扩展、MCP、提示词模板、主题、SYSTEM.md、上下文文件，
//     以及 <workspace>/.pi/skills 与工作区祖先目录的 .agents/skills。
//     2026-11-04 用真实 Pi 1.5.0 做过正反两向测量：-na 下这些 marker 不出现在
//     系统提示里，换成 -a 后立刻出现（说明 marker 本身可被发现，不是假阴性）。
//  2. 用户级资源按设计同步：<home>/.agents/skills、<agentDir>/skills、用户配置里
//     列出的技能。用户 2026-11-04 明确要求"不抑制本地 pi 的 skill，直接同步过来"，
//     所以 -ns / --no-skills 被移除——它会连用户级发现一起关掉，而 Pi 1.5.0 没有
//     "只关项目级、保留用户级"的单独开关。
//  3. 托管技能（本产品 <home>/skills 目录）不在 Pi 的发现路径上，仍通过
//     --skill <绝对路径> 显式加载。
//
// 参数来源是 Pi 1.5.0 的 CLI（真实 --help 摘录见 docs/evidence/c03-skills.md）：
//   - -na / --no-approve          忽略项目本地配置与受保护资源（项目技能由此切断）
//   - -nc / --no-context-files    不加载 AGENTS.md / CLAUDE.md
//   - -ne / --no-extensions       关闭发现、配置与内置扩展（显式 -e 仍加载）
//   - -np / --no-prompt-templates 关闭提示词模板
//   - --no-themes                 关闭主题
//   - --no-mcp                    关闭内置 MCP 支持
//   - -ns / --no-skills           **不使用**：它会连用户级技能发现一起关掉
//
// 注意 -ne 会连带关掉内置 MCP（MCP 本身就是一个内置扩展），要保留 MCP 必须
// 再显式 `-e builtin:mcp`。这个联动在官方文档里没有一句话写清楚，是实测得出的。
//
// 已知同名冲突语义（实测，不是猜测）：Pi 先加载自动发现的副本，再加载 --skill，
// 同名时保留先出现的那条。因此自动发现副本会压过同名的托管技能——禁用托管技能
// 不保证副本消失，启用托管技能也不保证覆盖副本。这条限制由 ADR 0003 记录，
// 并由真实 Pi 测试钉住。
type IsolationSpec struct {
	// PolicyExtension 是本产品的策略扩展路径（pi-extensions/harness-policy.ts）。
	// 必须显式 -e 加载：-ne 关掉的是"发现与配置"，显式路径不受影响。
	PolicyExtension string
	// HarnessMCP 为 true 时重新启用内置 MCP 支持（自有 MCP server 需要它）。
	HarnessMCP bool
	// ExtraExtensions 是额外显式加载的扩展（托管扩展），同样不受 -ne 影响。
	// 项目目录里的扩展永远不会出现在这里。
	ExtraExtensions []string
	// Skills 是"托管目录里启用、要显式加载"的技能（SKILL.md 绝对路径）。
	//
	// 这些路径不在 Pi 的自动发现目录里，不显式 --skill 就不会加载。
	// Pi 自己的用户级技能发现保持打开（不再用 -ns 抑制）；项目级与祖先级发现
	// 由 -na 切断（2026-11-04 实测）。注意同名时自动发现副本先被加载，会压过
	// 这里的 --skill 路径。
	Skills []string
}

// requiredIsolationFlags 是"切断项目级可执行资源"必需参数。删任何一个都必须被判为不合格。
//
// 注意：-ns 不在这里。技能的用户级自动发现按设计保持打开（本地技能直接同步过来），
// 项目级与祖先级技能发现已由 -na 覆盖。单独抽出来是为了让 ValidateArgs 能真的
// 检查，而不是靠注释承诺。
var requiredIsolationFlags = []string{"-na", "-nc", "-ne", "-np", "--no-themes"}

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
	// 技能放在最后：Pi 的用户级自动发现保持打开（不再用 -ns），
	// 这里的 --skill 是"托管目录里启用技能"的额外显式加载。
	for _, sk := range s.Skills {
		if sk = strings.TrimSpace(sk); sk != "" {
			args = append(args, "--skill", sk)
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
//
// 技能通道拆成三层，BlockedBy 留空表示"按设计开放"，Coverage() 会如实报
// Blocked=false；项目/祖先层由 -na 切断，不能标成开放：
//   - .pi/skills（工作区自己的技能）        → -na
//   - 项目/祖先 .agents/skills（不含 home） → -na
//   - 用户级 ~/.agents/skills、<agentDir>/skills → 按设计开放（2026-11-04 用户指令）
//
// 2026-11-04 真实 Pi 1.5.0 测量：-na 下项目/祖先 marker 不出现，-a 下出现，
// 用户级 marker 在两种情况下都出现。表按测量结果写，不按注释推断。
var isolationChannels = []IsolationChannel{
	{Name: ".pi/settings.json（项目设置）", BlockedBy: "-na"},
	{Name: ".pi/mcp.json（项目 MCP 服务器）", BlockedBy: "-na"},
	{Name: ".pi/extensions（项目扩展）", BlockedBy: "-na"},
	{Name: ".pi/skills（项目技能）", BlockedBy: "-na"},
	{Name: "项目/祖先 .agents/skills（当前目录到仓库根/卷根，不含 ~/.agents/skills）", BlockedBy: "-na"},
	{Name: ".pi/prompts（项目提示词模板）", BlockedBy: "-na"},
	{Name: ".pi/themes（项目主题）", BlockedBy: "-na"},
	{Name: ".pi/SYSTEM.md / .pi/APPEND_SYSTEM.md", BlockedBy: "-na"},
	{Name: "项目 packages（项目设置引入的包）", BlockedBy: "-na"},
	{Name: "项目设置里的 !command 凭证命令", BlockedBy: "-na"},
	{Name: "AGENTS.md / CLAUDE.md（上下文文件）", BlockedBy: "-nc"},
	{Name: "内置 MCP 自动连接", BlockedBy: "--no-mcp"},
	// 下面五条针对"配置与发现"层面的资源：-na 只管项目级，用户级/配置里列的
	// 扩展、模板、主题由 -ne/-np/--no-themes 关掉。技能那条按设计开放，不在此列。
	// 即使托管 agentDir 本该是干净的，也不依赖"本该"——参数层面直接切断。
	{Name: "配置与发现的内置/已安装扩展", BlockedBy: "-ne"},
	{Name: "用户级技能自动发现（~/.agents/skills、<agentDir>/skills）", BlockedBy: ""}, // 按设计开放：本地技能同步
	{Name: "用户级配置里的技能（<agentDir>/settings.json 的 skills）", BlockedBy: ""},  // 按设计开放：本地技能同步
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
	if err := s.validateExtension(); err != nil {
		return err
	}
	return s.validateSkills()
}

// validateSkills 要求每个技能路径都是绝对路径且真实存在。
//
// 不存在的技能不能被静默跳过：用户以为已启用、模型却没有这份说明，
// 症状是"技能怎么不生效"，而错误信息里没有任何线索。
func (s IsolationSpec) validateSkills() error {
	for _, sk := range s.Skills {
		p := strings.TrimSpace(sk)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return contracts.NewAppError(contracts.CodeConfigMissing,
				"技能路径必须是绝对路径: "+p, false)
		}
		info, err := os.Stat(p)
		if err != nil {
			return contracts.NewAppError(contracts.CodeDependencyMissing,
				"技能文件不可读: "+p+": "+err.Error(), false)
		}
		if info.IsDir() {
			return contracts.NewAppError(contracts.CodeConfigMissing,
				"技能路径必须是 SKILL.md 文件: "+p, false)
		}
	}
	return nil
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
