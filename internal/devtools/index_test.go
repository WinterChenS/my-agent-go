package devtools

// 机器索引与验收矩阵的一致性（GOV04）。
//
// v1-gates.md 要求：所有本矩阵 case 都在机器索引里有 owner/runner；未实现 runner
// 必须非零。索引一旦漏一条 case，门禁就永远不会要求那份证据——「没排在必需集合里
// 就等于不存在」，这是最容易被忽略的假通过。所以这条检查放在门禁自己的测试里，
// 而不是靠人对照文档。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// matrixRowID 匹配 v1-gates.md 第 2 节用例表的 ID 列（| ENV01 | F00 | ...）。
// 新增 Pi 原子 case 使用 PI_DISC01 形式；下划线必须被纳入，否则 GOV04 会漏检整组新门禁。
var matrixRowID = regexp.MustCompile(`(?m)^\| *([A-Z][A-Z0-9_]{1,15}\d{2}(?:\.[a-z]+)?) *\|`)

// knownCaseRunners 是 case 索引允许使用的逻辑 runner 名。注册名称不表示该
// runner 已实现：integration/native 仍按 case ID 查实际执行定义，缺失时返回 2。
// 这里的目的，是让拼错或随意填写 runner 不能通过 GOV04 的“可执行”检查。
var knownCaseRunners = map[string]bool{
	"anvil-ui": true, "artifact": true, "browser": true, "check-toolchain": true,
	"config": true, "connector": true, "credentials": true, "devtools-gate": true,
	"e2e": true, "mcp": true, "memory": true, "model": true, "packaging": true,
	"perf": true, "pi-lifecycle": true, "pi-locator": true, "pi-probe": true,
	"piinstall": true, "policy": true, "release": true, "rpc-framing": true,
	"rpc-worker": true, "run": true, "scheduler": true, "skill": true,
	"storage": true, "update": true, "workbench": true,
}

func loadRealIndex(t *testing.T) *CasesIndex {
	t.Helper()
	p, err := repoFile("tests/acceptance/cases.json")
	if err != nil {
		t.Fatalf("定位 cases.json: %v", err)
	}
	idx, err := LoadCasesIndex(p)
	if err != nil {
		t.Fatalf("读取索引: %v", err)
	}
	return idx
}

func TestIndexCoversEveryMatrixCase(t *testing.T) {
	idx := loadRealIndex(t)
	docPath, err := repoFile("docs/acceptance/v1-gates.md")
	if err != nil {
		t.Fatalf("定位验收矩阵: %v", err)
	}
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("读取验收矩阵: %v", err)
	}
	known := map[string]bool{}
	for _, c := range idx.Cases {
		known[c.ID] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, m := range matrixRowID.FindAllStringSubmatch(string(raw), -1) {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		// G0..G5 这类门禁行不是用例行。
		if len(id) <= 2 {
			continue
		}
		if !known[id] {
			missing = append(missing, id)
		}
	}
	if len(seen) == 0 {
		t.Fatal("没有从验收矩阵里解析出任何 case id：矩阵格式变了，检查会变成永远通过")
	}
	if len(missing) > 0 {
		t.Fatalf("验收矩阵里有 case 不在机器索引中（门禁永远不会要求它们）：%v", missing)
	}
}

func TestIndexEntriesAreActionable(t *testing.T) {
	idx := loadRealIndex(t)
	byID := map[string]CaseEntry{}
	for _, c := range idx.Cases {
		if _, exists := byID[c.ID]; exists {
			t.Errorf("机器索引包含重复 case id %s（后项会静默覆盖前项）", c.ID)
			continue
		}
		byID[c.ID] = c
	}
	for _, c := range idx.Cases {
		if c.Owner == "" || c.Runner == "" {
			t.Errorf("case %s 缺 owner 或 runner（无法追踪责任与执行方式）", c.ID)
		}
		if c.Runner != "" && !knownCaseRunners[c.Runner] {
			t.Errorf("case %s 使用未注册 runner %q（拼写错误会让门禁永远缺实现）", c.ID, c.Runner)
		}
		for _, g := range c.Gates {
			if !contains(KnownGates, g) {
				t.Errorf("case %s 归属未知 gate %q", c.ID, g)
			}
		}
		if len(c.Subcases) > 0 {
			if !c.Composite {
				t.Errorf("case %s 有 subcases 但没标 composite", c.ID)
			}
			for _, sub := range c.Subcases {
				if _, ok := byID[sub]; !ok {
					t.Errorf("case %s 的 subcase %s 在索引里不存在", c.ID, sub)
				}
			}
			continue
		}
		if !c.Required {
			continue
		}
		// 必需原子 case 必须有可解析的 scope，否则门禁无法确定要哪些目标证据。
		if strings.TrimSpace(c.Scope) == "" {
			t.Errorf("必需原子 case %s 没声明 scope", c.ID)
			continue
		}
		if _, ok := targetsForScope(c.Scope); !ok {
			t.Errorf("必需原子 case %s 的 scope %q 无法解析", c.ID, c.Scope)
		}
	}
}
