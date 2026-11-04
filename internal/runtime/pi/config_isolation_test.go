package pi

import (
	"os"
	"path/filepath"
	"testing"

	"my-agent-go/internal/contracts"
)

// policyExtFixture 造一个存在的扩展文件，供 Validate 的路径检查通过。
func policyExtFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "harness-policy.ts")
	if err := os.WriteFile(p, []byte("export default function () {}\n"), 0o644); err != nil {
		t.Fatalf("写扩展 fixture: %v", err)
	}
	return p
}

// TestIsolationArgsCoverEveryProjectChannel 覆盖 SEC08 的通道清单：
// 每一条"按设计应切断"的项目级可执行资源都必须有一个参数负责切断它。
//
// 技能通道 2026-11-04 真实 Pi 1.5.0 测量后拆成三层：项目/祖先级由 -na 切断，
// 用户级按设计开放（本地技能同步过来）。这里既断言开放的那条确实开放，
// 也断言项目/祖先那两条确实标为被 -na 切断——防止有人把"没切断"写成"按设计开放"。
func TestIsolationArgsCoverEveryProjectChannel(t *testing.T) {
	spec := IsolationSpec{PolicyExtension: policyExtFixture(t)}
	if err := spec.Validate(); err != nil {
		t.Fatalf("合格参数被拒: %v", err)
	}

	cov := spec.Coverage()
	if len(cov) == 0 {
		t.Fatal("覆盖表为空")
	}
	openByDesign := map[string]bool{
		"用户级技能自动发现（~/.agents/skills、<agentDir>/skills）": true,
		"用户级配置里的技能（<agentDir>/settings.json 的 skills）":  true,
	}
	// 项目/祖先技能通道必须由 -na 切断（实测结论，不能改成开放）。
	mustBeBlockedByNA := []string{
		".pi/skills（项目技能）",
		"项目/祖先 .agents/skills（当前目录到仓库根/卷根，不含 ~/.agents/skills）",
	}
	blockedByNA := map[string]string{}
	for _, ch := range cov {
		if openByDesign[ch.Name] {
			if ch.Blocked {
				t.Fatalf("技能通道按设计应开放（本地技能同步），实际却被 %s 切断", ch.BlockedBy)
			}
			continue
		}
		if !ch.Blocked {
			t.Fatalf("通道未被切断: %s（应由 %s 切断）", ch.Name, ch.BlockedBy)
		}
		if ch.BlockedBy != "" {
			blockedByNA[ch.Name] = ch.BlockedBy
		}
	}
	for _, name := range mustBeBlockedByNA {
		if got, ok := blockedByNA[name]; !ok || got != "-na" {
			t.Fatalf("通道 %q 必须由 -na 切断（实测），实际 BlockedBy=%q 存在=%v", name, got, ok)
		}
	}

	args := spec.Args()
	for _, flag := range []string{"-na", "-nc", "-ne", "-np", "--no-themes", "--no-mcp"} {
		if !containsArg(args, flag) {
			t.Fatalf("缺参数 %s，实际 %v", flag, args)
		}
	}
	if containsArg(args, "-ns") {
		t.Fatalf("不应再带 -ns（本地技能自动发现按设计保持打开）: %v", args)
	}
	if !containsPair(args, "-e", spec.PolicyExtension) {
		t.Fatalf("策略扩展必须显式 -e 加载，实际 %v", args)
	}
}

// TestValidateArgsDetectsEveryMissingFlag 是这张覆盖表的"有没有用"证明：
// 逐个删掉负责参数，ValidateArgs 必须报错，且对应通道被标为未切断。
//
// 只断言 spec.Args() 自洽是没意义的——删参数时两边一起变，测试永远绿。
func TestValidateArgsDetectsEveryMissingFlag(t *testing.T) {
	spec := IsolationSpec{PolicyExtension: policyExtFixture(t)}

	for _, flag := range append(append([]string{}, requiredIsolationFlags...), "--no-mcp") {
		t.Run("缺少 "+flag, func(t *testing.T) {
			full := spec.Args()
			slim := removeArg(full, flag)
			if err := ValidateArgs(slim, spec); err == nil {
				t.Fatalf("缺少 %s 时必须拒绝启动，实际通过（参数 %v）", flag, slim)
			} else {
				requireCodeIso(t, err, contracts.CodeConfigMissing)
			}
			// 覆盖表也要跟着变化：说明表是推导的，不是写死的。
			unblocked := 0
			for _, ch := range coverageOf(slim) {
				if ch.BlockedBy == flag && !ch.Blocked {
					unblocked++
				}
			}
			if unblocked == 0 {
				t.Fatalf("参数 %s 被删后覆盖表仍无变化，说明覆盖表是写死的", flag)
			}
		})
	}
}

// TestHarnessMCPRequiresExplicitEnable 覆盖 -ne 会连带关闭内置 MCP 这个实测结论：
// 打开 MCP 时必须有 -e builtin:mcp，否则 Validate 必须拒绝。
func TestHarnessMCPRequiresExplicitEnable(t *testing.T) {
	spec := IsolationSpec{PolicyExtension: policyExtFixture(t), HarnessMCP: true}
	args := spec.Args()
	if containsArg(args, "--no-mcp") {
		t.Fatalf("启用 MCP 时不应带 --no-mcp: %v", args)
	}
	if !containsPair(args, "-e", "builtin:mcp") {
		t.Fatalf("启用 MCP 时必须显式 -e builtin:mcp（-ne 已关掉内置扩展）: %v", args)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("启用 MCP 的合格参数被拒: %v", err)
	}

	// 删掉显式启用后必须判不合格——否则等于静默没有 MCP。
	if err := ValidateArgs(removePair(args, "-e", "builtin:mcp"), spec); err == nil {
		t.Fatal("缺少 -e builtin:mcp 时必须拒绝")
	}
}

// TestIsolationRequiresExistingAbsoluteExtension 防止"扩展没加载上却以为加载了"。
func TestIsolationRequiresExistingAbsoluteExtension(t *testing.T) {
	existing := policyExtFixture(t)

	cases := []struct {
		name    string
		spec    IsolationSpec
		wantErr bool
	}{
		{"合格", IsolationSpec{PolicyExtension: existing}, false},
		{"空路径", IsolationSpec{}, true},
		{"相对路径", IsolationSpec{PolicyExtension: "pi-extensions/harness-policy.ts"}, true},
		{"不存在", IsolationSpec{PolicyExtension: filepath.Join(t.TempDir(), "nope.ts")}, true},
		{"目录", IsolationSpec{PolicyExtension: t.TempDir()}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("应拒绝")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("应通过: %v", err)
			}
			if tc.wantErr {
				requireCodeIso(t, err, contracts.CodeConfigMissing)
			}
		})
	}
}

func removeArg(args []string, drop string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != drop {
			out = append(out, a)
		}
	}
	return out
}

// TestSkillsKeepAutoDiscoveryOpen 钉住 2026-11-04 的方向调整：
// 加载托管技能时**不再**带 -ns，Pi 的本地技能自动发现保持打开（本地技能同步过来）。
// 托管技能本身仍通过 --skill 显式加载（它不在 Pi 的发现路径上）。
func TestSkillsKeepAutoDiscoveryOpen(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte("---\nname: demo\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatalf("写技能: %v", err)
	}
	spec := IsolationSpec{
		PolicyExtension: file, // 复用同一路径，Validate 只检查"存在且是文件"
		Skills:          []string{file},
	}
	args := spec.Args()
	if containsArg(args, "-ns") {
		t.Fatalf("不应再带 -ns（本地技能自动发现按设计保持打开）: %v", args)
	}
	if !containsPair(args, "--skill", file) {
		t.Fatalf("托管技能应通过 --skill 显式加载，实际参数: %v", args)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("合法技能路径应通过校验: %v", err)
	}
}

// TestValidateSkillsRejectsMissingOrRelative 守"不静默跳过"。
func TestValidateSkillsRejectsMissingOrRelative(t *testing.T) {
	ext := t.TempDir()
	policy := filepath.Join(ext, "policy.ts")
	if err := os.WriteFile(policy, []byte("export default {}"), 0o644); err != nil {
		t.Fatalf("写策略扩展: %v", err)
	}

	rel := IsolationSpec{PolicyExtension: policy, Skills: []string{"relative/SKILL.md"}}
	if err := rel.Validate(); err == nil {
		t.Fatal("相对路径的技能必须被拒绝")
	}

	missing := IsolationSpec{PolicyExtension: policy, Skills: []string{filepath.Join(ext, "nope", "SKILL.md")}}
	if err := missing.Validate(); err == nil {
		t.Fatal("不存在的技能路径必须被拒绝，而不是悄悄少加载一个")
	}
}

func removePair(args []string, a, b string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if i+1 < len(args) && args[i] == a && args[i+1] == b {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func requireCodeIso(t *testing.T, err error, code string) {
	t.Helper()
	app, ok := err.(*contracts.AppError)
	if !ok {
		t.Fatalf("错误类型应为 *AppError，实际 %T: %v", err, err)
	}
	if app.Code != code {
		t.Fatalf("错误码 = %s，期望 %s（消息: %s）", app.Code, code, app.Message)
	}
}
