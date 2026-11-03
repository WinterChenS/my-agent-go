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
// 每一条项目级可执行资源都必须有一个参数负责切断它。
func TestIsolationArgsCoverEveryProjectChannel(t *testing.T) {
	spec := IsolationSpec{PolicyExtension: policyExtFixture(t)}
	if err := spec.Validate(); err != nil {
		t.Fatalf("合格参数被拒: %v", err)
	}

	cov := spec.Coverage()
	if len(cov) == 0 {
		t.Fatal("覆盖表为空")
	}
	for _, ch := range cov {
		if !ch.Blocked {
			t.Fatalf("通道未被切断: %s（应由 %s 切断）", ch.Name, ch.BlockedBy)
		}
	}

	args := spec.Args()
	for _, flag := range []string{"-na", "-nc", "-ne", "-ns", "-np", "--no-themes", "--no-mcp"} {
		if !containsArg(args, flag) {
			t.Fatalf("缺参数 %s，实际 %v", flag, args)
		}
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
