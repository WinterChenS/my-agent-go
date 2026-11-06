package devtools

// 机器索引与验收矩阵的一致性（GOV04）。
//
// v1-gates.md 要求：所有本矩阵 case 都在机器索引里有 owner/runner；未实现 runner
// 必须非零。索引一旦漏一条 case，门禁就永远不会要求那份证据——「没排在必需集合里
// 就等于不存在」，这是最容易被忽略的假通过。所以这条检查放在门禁自己的测试里，
// 而不是靠人对照文档。

import (
	"encoding/json"
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

// legacyProcessOracleCeiling 是 test-process 欠账清单的棘轮上限。
//
// 只允许下调：把某个旧 runner 改成输出 external-observation 后，从
// oracle-allowlist.json 移除并同步下调本常量。上调意味着有人想让新 case 自证，
// 必须作为显式代码改动经过审查，不能只改 JSON。
const legacyProcessOracleCeiling = 12

// TestOracleKindIsDeclaredOrGrandfathered：新 case 不能用 test-process 自证。
//
// 背景（2026-11-05 复核）：test-process oracle 只能证明「报告与子进程输出一致」，
// 无法证明 fixture 真收到请求、数据库真写入、进程真退出。所以除「加固前已有
// canonical runner」的欠账 case 外，所有原子 case 必须 oracleKind=external。
// 是否「已有 runner」不靠人工统计，直接从 integrationCases()/nativeCases() 计算。
func TestOracleKindIsDeclaredOrGrandfathered(t *testing.T) {
	idx := loadRealIndex(t)
	allowPath, err := repoFile("tests/acceptance/oracle-allowlist.json")
	if err != nil {
		t.Fatalf("定位 oracle allowlist: %v", err)
	}
	raw, err := os.ReadFile(allowPath)
	if err != nil {
		t.Fatalf("读取 oracle allowlist: %v", err)
	}
	var allow struct {
		SchemaVersion  int      `json:"schemaVersion"`
		ProcessAllowed []string `json:"processAllowed"`
	}
	if err := json.Unmarshal(raw, &allow); err != nil {
		t.Fatalf("解析 oracle allowlist: %v", err)
	}
	if allow.SchemaVersion != 1 {
		t.Fatalf("oracle allowlist schemaVersion=%d，期望 1", allow.SchemaVersion)
	}
	if len(allow.ProcessAllowed) > legacyProcessOracleCeiling {
		t.Errorf("test-process 欠账清单有 %d 条，超过棘轮上限 %d：新 case 不得加入欠账", len(allow.ProcessAllowed), legacyProcessOracleCeiling)
	}

	runnerBacked := map[string]bool{}
	for _, c := range integrationCases() {
		runnerBacked[c.ID] = true
	}
	for _, c := range nativeCases() {
		runnerBacked[c.ID] = true
	}

	byID := map[string]CaseEntry{}
	for _, c := range idx.Cases {
		byID[c.ID] = c
	}

	allowed := map[string]bool{}
	for _, id := range allow.ProcessAllowed {
		if allowed[id] {
			t.Errorf("oracle allowlist 重复条目 %s", id)
		}
		allowed[id] = true
		c, ok := byID[id]
		switch {
		case !ok:
			t.Errorf("oracle allowlist 里的 %s 不在用例索引中（悬空条目会掩盖删除）", id)
		case c.Composite:
			t.Errorf("oracle allowlist 里的 %s 是复合 case，复合 case 没有自己的证据，不能列入", id)
		case !runnerBacked[id]:
			t.Errorf("oracle allowlist 里的 %s 没有 canonical runner：欠账只针对已有 runner 的旧 case", id)
		}
	}

	for _, c := range idx.Cases {
		if c.Composite {
			if c.OracleKind != "" {
				t.Errorf("复合 case %s 不应声明 oracleKind（由子 case 各自声明）", c.ID)
			}
			continue
		}
		switch c.OracleKind {
		case "external":
			if allowed[c.ID] {
				t.Errorf("case %s 已声明 external oracle，但仍留在 allowlist；改完必须移除", c.ID)
			}
		case "":
			if !allowed[c.ID] {
				t.Errorf("case %s 未声明 oracleKind=external 且不在 allowlist：新 case 必须用实现边界之外的 oracle", c.ID)
			}
		default:
			t.Errorf("case %s 的 oracleKind=%q 非法（只允许 external 或省略）", c.ID, c.OracleKind)
		}
	}
}

// planIndex 是 docs/acceptance/v1-plan-index.json 的最小解析结构。
type planIndex struct {
	Tasks []struct {
		ID          string   `json:"id"`
		Plan        string   `json:"plan"`
		DependsOn   []string `json:"dependsOn"`
		AcceptAfter []string `json:"acceptAfter"`
	} `json:"tasks"`
	Gates []struct {
		ID        string   `json:"id"`
		DependsOn []string `json:"dependsOn"`
	} `json:"gates"`
	Requirements map[string][]string `json:"requirements"`
}

// TestPlanIndexIsConsistent 把计划索引变成可机检的契约。
//
// 为什么用三类事件建图：任务对任务是「开工前提」，任务对 gate 是「验收顺序」，
// 两者语义不同。混在一张图里会出现假环（2026-11-05 复核实测：
// F06→F09→C09→C08→U01→C06→G2→F06）。这里区分
//
//	Impl(T)  任务 T 已实现
//	Acc(T)   任务 T 的 case 已验收
//	Pass(G)  gate G 已通过
//
// 边：Impl(T)→Impl(D)（D 为开工前提）；Impl(T)→Pass(G)（X03/X05 这类硬性等待）；
// Acc(T)→Impl(T)；Acc(T)→Pass(G)（acceptAfter）；Pass(G)→Acc(T)、Pass(G)→Pass(G')。
// 图必须无环；每个 case 所属 gate 的 Pass 必须可达其每个 owner 的 Acc，
// 否则该 gate 会在 owner 未验收时就能判定。
func TestPlanIndexIsConsistent(t *testing.T) {
	p, err := repoFile("docs/acceptance/v1-plan-index.json")
	if err != nil {
		t.Fatalf("定位计划索引: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取计划索引: %v", err)
	}
	var plan planIndex
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("解析计划索引: %v", err)
	}

	isTask := map[string]bool{}
	isGate := map[string]bool{}
	for _, task := range plan.Tasks {
		if isTask[task.ID] {
			t.Errorf("任务 %s 重复", task.ID)
		}
		isTask[task.ID] = true
	}
	for _, g := range plan.Gates {
		isGate[g.ID] = true
	}

	edges := map[string][]string{}
	add := func(from, to string) { edges[from] = append(edges[from], to) }
	for _, task := range plan.Tasks {
		add("Acc:"+task.ID, "Impl:"+task.ID)
		for _, d := range task.DependsOn {
			switch {
			case isTask[d]:
				add("Impl:"+task.ID, "Impl:"+d)
			case isGate[d]:
				add("Impl:"+task.ID, "Pass:"+d)
			default:
				t.Errorf("任务 %s 的 dependsOn 引用未知节点 %s", task.ID, d)
			}
		}
		for _, g := range task.AcceptAfter {
			if !isGate[g] {
				t.Errorf("任务 %s 的 acceptAfter 只能是 gate，得到 %s", task.ID, g)
				continue
			}
			add("Acc:"+task.ID, "Pass:"+g)
		}
	}
	for _, g := range plan.Gates {
		for _, d := range g.DependsOn {
			switch {
			case isTask[d]:
				add("Pass:"+g.ID, "Acc:"+d)
			case isGate[d]:
				add("Pass:"+g.ID, "Pass:"+d)
			default:
				t.Errorf("gate %s 引用未知节点 %s", g.ID, d)
			}
		}
	}

	// 环检测（DFS 三色）。
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range edges[n] {
			switch color[m] {
			case grey:
				t.Errorf("计划索引存在环: %s -> %s", strings.Join(stack, " -> "), m)
				return false
			case white:
				if !visit(m) {
					return false
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return true
	}
	for n := range edges {
		if color[n] == white && !visit(n) {
			break
		}
	}

	reach := func(from string) map[string]bool {
		seen := map[string]bool{from: true}
		queue := []string{from}
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			for _, m := range edges[n] {
				if !seen[m] {
					seen[m] = true
					queue = append(queue, m)
				}
			}
		}
		return seen
	}

	idx := loadRealIndex(t)
	owned := map[string]bool{}
	gateReach := map[string]map[string]bool{}
	for _, c := range idx.Cases {
		owners := strings.Split(c.Owner, "/")
		for _, o := range owners {
			owned[o] = true
			if !isTask[o] {
				t.Errorf("case %s 的 owner %s 不是计划任务", c.ID, o)
			}
		}
		if c.Composite {
			continue
		}
		for _, g := range c.Gates {
			if gateReach[g] == nil {
				gateReach[g] = reach("Pass:" + g)
			}
			for _, o := range owners {
				if !gateReach[g]["Acc:"+o] {
					t.Errorf("case %s 属于 %s，但 %s 不等待 owner %s 验收（gate 的 dependsOn 漏了它）", c.ID, g, g, o)
				}
			}
		}
	}

	traced := map[string]bool{}
	for _, tasks := range plan.Requirements {
		for _, id := range tasks {
			traced[id] = true
		}
	}
	for _, task := range plan.Tasks {
		if !owned[task.ID] {
			t.Errorf("任务 %s 没有任何验收 case", task.ID)
		}
		if !traced[task.ID] {
			t.Errorf("任务 %s 没有追踪到任何 R 需求", task.ID)
		}
	}
}
