package devtools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"my-agent-go/internal/contracts"
)

// KnownGates 列出所有门禁阶段。
var KnownGates = []string{"G0", "G1", "G2", "G3", "G4", "G5"}

// KnownTargets 是六目标平台（v1-gates.md §1）。未知 target 必须判死。
var KnownTargets = []string{
	"win-x64", "win-arm64", "mac-x64", "mac-arm64", "linux-x64", "linux-arm64",
}

// 显示会话标识。Linux 的 Gate 判定必须区分 X11 与 Wayland：只跑其中一个
// 就宣称「Linux 通过」，等于把两个必需环境合并成一份结果。
const (
	DisplayNone    = ""
	DisplayDesktop = "desktop"
	DisplayX11     = "x11"
	DisplayWayland = "wayland"
)

// CaseEntry 是机器索引中的单条用例记录。
type CaseEntry struct {
	ID       string   `json:"id"`
	Owner    string   `json:"owner"`
	Runner   string   `json:"runner"`
	Required bool     `json:"required"`
	Scope    string   `json:"scope"`
	Phase    string   `json:"phase"`
	Gates    []string `json:"gates"`
	// OracleKind 声明该 case 需要的证据强度：
	//   - "external"：必须提供实现边界之外的观察（fixture counter/DB/文件/进程），
	//     不接受 test-process 自证；副作用与真实包类 case 必须用它；
	//   - "" 或 "process"：断言本身就是子进程结果，允许 test-process。
	// 缺省值是有意保守的：新加的副作用 case 必须显式声明，否则不得降级。
	OracleKind string   `json:"oracleKind,omitempty"`
	Composite  bool     `json:"composite,omitempty"`
	Subcases   []string `json:"subcases,omitempty"`
	Manual     bool     `json:"manual,omitempty"`
}

// CasesIndex 是 tests/acceptance/cases.json 的解析结构。
type CasesIndex struct {
	SchemaVersion int         `json:"schemaVersion"`
	Source        string      `json:"source"`
	Note          string      `json:"note"`
	Gates         []string    `json:"gates"`
	Targets       []string    `json:"targets"`
	Cases         []CaseEntry `json:"cases"`
}

// CaseEval 是单个必需 case 的汇总评估结果。
type CaseEval struct {
	CaseID string `json:"caseId"`
	Found  bool   `json:"found"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Requirement 是一条「必需证据位」：case × target × display。
//
// 为什么不是一个 case 一条：G0 的 ALL 用例要在六个目标各有一份真实证据。
// 按 case 汇总会让「同一个 case 的任意一份结果」满足整个 case（win-x64 通过
// 就等于 ALL 通过），同一 case 在两个目标上一成一败也会互相覆盖。
type Requirement struct {
	CaseID string `json:"caseId"`
	// Target 为空表示 scope=HOST：任一已知目标提供了有效证据即可，
	// 但一旦出现多份，每一份都必须通过（不能挑一份绿的当结论）。
	Target  string `json:"target,omitempty"`
	Display string `json:"display,omitempty"`
}

// RequirementEval 是单条必需证据位的评估结果。
type RequirementEval struct {
	Requirement
	Found  bool   `json:"found"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// GateReport 汇总门禁判定结果。
type GateReport struct {
	GateID   string   `json:"gateId"`
	Passed   bool     `json:"passed"`
	ExitCode int      `json:"exitCode"`
	Reasons  []string `json:"reasons"`
	// SourceRevision/WorktreeDirty 是「下结论时」的基线身份。
	// 证据必须绑定同一个 revision 且自身来自干净工作区（见 validateEvidence）。
	SourceRevision string `json:"sourceRevision,omitempty"`
	WorktreeDirty  bool   `json:"worktreeDirty,omitempty"`

	PerCase        map[string]CaseEval `json:"perCase"`
	PerRequirement []RequirementEval   `json:"perRequirement,omitempty"`
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// LoadCasesIndex 读取机器索引。
func LoadCasesIndex(path string) (*CasesIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取用例索引失败 %s: %w", path, err)
	}
	var idx CasesIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("解析用例索引失败 %s: %w", path, err)
	}
	return &idx, nil
}

// ResolveRequiredAtomicCases 返回某 gate 需要的原子 case 列表。
// 复合 case（如 SEC03）按子 case 的 gates 归属展开；子 case 未归属该 gate 则不计入。
// 已去重，避免复合 case 与原子子 case 同时出现在索引时重复计入。
//
// 注意：这里只回答「哪些 case」，目标/显示维度由 ResolveRequiredEvidence 决定。
// 集成 runner 用它决定要跑哪些 case；门禁判定必须用 ResolveRequiredEvidence。
func ResolveRequiredAtomicCases(idx *CasesIndex, gateID string) []string {
	byID := map[string]CaseEntry{}
	for _, c := range idx.Cases {
		byID[c.ID] = c
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, c := range idx.Cases {
		if !c.Required {
			continue
		}
		if len(c.Subcases) > 0 {
			for _, sub := range c.Subcases {
				sc, ok := byID[sub]
				if ok && sc.Required && contains(sc.Gates, gateID) {
					add(sub)
				}
			}
			continue
		}
		if contains(c.Gates, gateID) {
			add(c.ID)
		}
	}
	return out
}

// targetsForScope 把验收矩阵的 scope 缩写翻译成必需的 target 集合。
//
// scope 是矩阵里人读的列（「ALL」「LNX」「Windows两架构」…）。把它在这里
// 显式翻译成机器判定，而不是让每个 case 手抄六行：抄漏一个目标的代价远高于
// 多写一个 switch。未知 scope 必须判死——新增 case 写了 typos 不能静默变成
// 「不需要证据」。
func targetsForScope(scope string) ([]string, bool) {
	all := append([]string{}, KnownTargets...)
	switch strings.TrimSpace(scope) {
	case "ALL", "NATIVE", "HOST+ALL路径集成":
		return all, true
	case "LNX", "Linux两架构":
		return []string{"linux-x64", "linux-arm64"}, true
	case "Windows两架构":
		return []string{"win-x64", "win-arm64"}, true
	case "macOS两架构":
		return []string{"mac-x64", "mac-arm64"}, true
	case "HOST":
		// 由 Requirement.Target 为空表达，见 ResolveRequiredEvidence。
		return nil, true
	}
	return nil, false
}

// normalizeDisplay 让非 Linux 目标的空 display 与 "desktop" 等价。
//
// 集成 runner 不写 display（""），原生 runner 写 "desktop"；如果要求两者
// 字面相等，同一个必需位会因为生成路径不同而永远对不上。Linux 不做归一化：
// X11 与 Wayland 是两个必须分别跑的环境。
func normalizeDisplay(target, display string) string {
	if strings.HasPrefix(target, "linux-") {
		return display
	}
	if strings.TrimSpace(display) == "" {
		return DisplayDesktop
	}
	return display
}

// requiredDisplays 返回某 scope 在某个 target 上必须出现的 display 集合。
func requiredDisplays(scope, target string) []string {
	if strings.HasPrefix(target, "linux-") {
		switch strings.TrimSpace(scope) {
		case "NATIVE", "LNX", "Linux两架构":
			return []string{DisplayX11, DisplayWayland}
		default:
			return []string{DisplayNone}
		}
	}
	return []string{DisplayDesktop}
}

// allowedDisplays 返回某 target 上合法的 display 取值。
// 用来拒绝 "banana" 这种编造值，而不是悄悄当成 NULL。
func allowedDisplays(target string) []string {
	if strings.HasPrefix(target, "linux-") {
		return []string{DisplayNone, DisplayX11, DisplayWayland}
	}
	return []string{DisplayDesktop}
}

// ResolveRequiredEvidence 展开某 gate 的全部必需证据位（case × target × display）。
// 返回的问题列表非空时，门禁必须判死（索引/scope 有误，无法判定）。
func ResolveRequiredEvidence(idx *CasesIndex, gateID string) ([]Requirement, []string) {
	byID := map[string]CaseEntry{}
	for _, c := range idx.Cases {
		byID[c.ID] = c
	}
	var reqs []Requirement
	var problems []string
	for _, id := range ResolveRequiredAtomicCases(idx, gateID) {
		c, ok := byID[id]
		if !ok {
			problems = append(problems, fmt.Sprintf("case %s 在索引里找不到定义", id))
			continue
		}
		scope := strings.TrimSpace(c.Scope)
		if scope == "" {
			problems = append(problems, fmt.Sprintf("case %s 未声明 scope（原子 case 必须有目标范围）", id))
			continue
		}
		targets, ok := targetsForScope(scope)
		if !ok {
			problems = append(problems, fmt.Sprintf("case %s 的 scope %q 未知（无法确定必需目标）", id, c.Scope))
			continue
		}
		if scope == "HOST" {
			reqs = append(reqs, Requirement{CaseID: id})
			continue
		}
		for _, t := range targets {
			for _, d := range requiredDisplays(scope, t) {
				reqs = append(reqs, Requirement{CaseID: id, Target: t, Display: d})
			}
		}
	}
	return reqs, problems
}

// evidenceFile 是把 GateEvidence 与其所在目录绑定，便于重新计算报告哈希。
type evidenceFile struct {
	contracts.GateEvidence
	dir string
}

// EvaluateGate 评估某 gate 的证据树，返回退出码与报告。
//
// 门禁判定必须把以下情况全部判死（GOV01/GOV02）：
//   - 缺必需证据位（case × target × display 任意一维缺失）
//   - 同一必需位出现多份证据（无法确定哪一份是结论）
//   - 证据的 gateId 与请求的 gate 不一致（串用别的阶段的报告）
//   - 旧 revision / 旧 versions.lock 哈希（与基线不一致）
//   - 证据来自 dirty 工作区（贴错了 revision）
//   - 空 suite 或全 SKIP 的报告
//   - 报告哈希被篡改（重新计算与 result.json.reportHash 不符）
//   - 人工证据缺审查人/机器信息
//   - 未知 target / 未知 gate / 未知 scope
//
// 任一必需证据位不满足，gate 即判死，退出码固定为 2（配置/索引/门禁缺失或未知）。
func EvaluateGate(idx *CasesIndex, gateID, evidenceRoot string, base GateBaseline) (*GateReport, error) {
	rep := &GateReport{
		GateID:         gateID,
		Passed:         true,
		Reasons:        []string{},
		PerCase:        map[string]CaseEval{},
		SourceRevision: base.SourceRevision,
		WorktreeDirty:  base.WorktreeDirty,
	}
	if !contains(KnownGates, gateID) {
		failGate(rep, fmt.Sprintf("未知 gate: %s（已知: %s）", gateID, strings.Join(KnownGates, ",")))
		return rep, nil
	}
	reqs, problems := ResolveRequiredEvidence(idx, gateID)
	for _, p := range problems {
		failGate(rep, p)
	}
	if len(reqs) == 0 {
		failGate(rep, fmt.Sprintf("gate %s 没有必需 case（索引缺失或不完整）", gateID))
		return rep, nil
	}

	// 收集证据树中的所有 result.json；caseId 取目录名，target/display 取内容。
	byCase := map[string][]evidenceFile{}
	walkErr := filepath.Walk(evidenceRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || info.Name() != "result.json" {
			return nil
		}
		data, e := os.ReadFile(p)
		if e != nil {
			return nil
		}
		var ev contracts.GateEvidence
		if e := json.Unmarshal(data, &ev); e != nil {
			return nil
		}
		dir := filepath.Dir(p)
		caseID := filepath.Base(dir)
		byCase[caseID] = append(byCase[caseID], evidenceFile{GateEvidence: ev, dir: dir})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	// 每个必需 case 的证据集合必须先自证合法：
	// 同一 (target, display) 重复、未知 target、未知 display，都不是"多一份保险"，
	// 而是"结论不确定"。
	caseProblems := map[string][]string{}
	seenKey := map[string]int{}
	for caseID, evs := range byCase {
		for i := range evs {
			ev := evs[i]
			target := ev.TargetID
			if !contains(KnownTargets, target) {
				caseProblems[caseID] = append(caseProblems[caseID],
					fmt.Sprintf("未知 target: %q（目录 %s）", target, relDir(evidenceRoot, ev.dir)))
				continue
			}
			disp := normalizeDisplay(target, ev.Display)
			if !contains(allowedDisplays(target), disp) {
				caseProblems[caseID] = append(caseProblems[caseID],
					fmt.Sprintf("未知 display: %q（target=%s，目录 %s）", ev.Display, target, relDir(evidenceRoot, ev.dir)))
				continue
			}
			key := caseID + "\x00" + target + "\x00" + disp
			seenKey[key]++
			if seenKey[key] > 1 {
				caseProblems[caseID] = append(caseProblems[caseID],
					fmt.Sprintf("同一必需位出现重复证据: target=%s display=%s（目录 %s）", target, disp, relDir(evidenceRoot, ev.dir)))
			}
		}
	}

	requiredCases := map[string]bool{}
	requiredKeys := map[string]bool{}
	entryByID := map[string]CaseEntry{}
	for _, c := range idx.Cases {
		entryByID[c.ID] = c
	}
	for _, r := range reqs {
		requiredCases[r.CaseID] = true
		requiredKeys[r.CaseID+"\x00"+r.Target+"\x00"+r.Display] = true
	}

	for _, r := range reqs {
		eval := RequirementEval{Requirement: r}
		hits := matchingEvidence(byCase[r.CaseID], r)
		if len(hits) == 0 {
			eval.Reason = "缺必需证据（证据树中没有匹配的 target/display）"
			if r.Target == "" {
				eval.Reason = "缺必需证据（scope=HOST：至少需要一个已知目标的证据）"
			}
			rep.PerRequirement = append(rep.PerRequirement, eval)
			failGate(rep, fmt.Sprintf("缺必需证据: %s target=%s display=%s",
				r.CaseID, orAny(r.Target), orNone(r.Display)))
			addCaseReason(rep, r.CaseID, eval.Reason)
			continue
		}
		eval.Found = true
		var probs []string
		for _, ev := range hits {
			probs = append(probs, validateEvidence(ev, r.CaseID, gateID, base, entryByID[r.CaseID])...)
		}
		probs = append(probs, caseProblems[r.CaseID]...)
		if len(probs) > 0 {
			eval.Status = "rejected"
			eval.Reason = strings.Join(dedupe(probs), "; ")
			rep.PerRequirement = append(rep.PerRequirement, eval)
			failGate(rep, fmt.Sprintf("case %s (target=%s display=%s): %s",
				r.CaseID, orAny(r.Target), orNone(r.Display), eval.Reason))
			addCaseReason(rep, r.CaseID, eval.Reason)
			continue
		}
		eval.Status = "pass"
		rep.PerRequirement = append(rep.PerRequirement, eval)
		addCaseReason(rep, r.CaseID, "")
	}

	// 同一 case 的其它证据（例如某个不在必需集合里的 target）也必须有效：
	// 否则「win-arm64 失败」会因为没有排在必需集合里而被悄悄忽略。
	for caseID := range requiredCases {
		for _, ev := range byCase[caseID] {
			target := ev.TargetID
			if !contains(KnownTargets, target) {
				continue // 已记入 caseProblems
			}
			key := caseID + "\x00" + target + "\x00" + normalizeDisplay(target, ev.Display)
			if requiredKeys[key] {
				continue // 必需位已单独校验，不重复报
			}
			if probs := validateEvidence(ev, caseID, gateID, base, entryByID[caseID]); len(probs) > 0 {
				reason := strings.Join(dedupe(probs), "; ")
				rep.PerRequirement = append(rep.PerRequirement, RequirementEval{
					Requirement: Requirement{CaseID: caseID, Target: target, Display: normalizeDisplay(target, ev.Display)},
					Found:       true,
					Status:      "rejected",
					Reason:      reason,
				})
				failGate(rep, fmt.Sprintf("case %s 的额外证据 (target=%s): %s", caseID, target, reason))
				addCaseReason(rep, caseID, reason)
			}
		}
	}

	for caseID := range requiredCases {
		ce, ok := rep.PerCase[caseID]
		if !ok {
			ce = CaseEval{CaseID: caseID, Reason: "没有必需证据位"}
		}
		ce.Found = len(byCase[caseID]) > 0
		rep.PerCase[caseID] = ce
	}
	sortRequirements(rep.PerRequirement)
	return rep, nil
}

// matchingEvidence 找出满足某必需证据位的证据。
// HOST（Target 为空）接受任一已知目标——但调用方会把该 case 的全部证据都校验一遍。
func matchingEvidence(evs []evidenceFile, r Requirement) []evidenceFile {
	var out []evidenceFile
	for _, ev := range evs {
		if r.Target == "" {
			if contains(KnownTargets, ev.TargetID) {
				out = append(out, ev)
			}
			continue
		}
		if ev.TargetID != r.Target {
			continue
		}
		if normalizeDisplay(ev.TargetID, ev.Display) != r.Display {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// addCaseReason 汇总 case 级别的状态：只要有一个必需位不通过，case 就不通过。
func addCaseReason(rep *GateReport, caseID, reason string) {
	cur, ok := rep.PerCase[caseID]
	if !ok {
		cur = CaseEval{CaseID: caseID}
	}
	if reason == "" {
		if cur.Status != "rejected" {
			cur.Status = "pass"
		}
	} else {
		cur.Status = "rejected"
		cur.Reason = joinReason(cur.Reason, reason)
	}
	rep.PerCase[caseID] = cur
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	if strings.Contains(a, b) {
		return a
	}
	return a + " | " + b
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func orAny(s string) string {
	if s == "" {
		return "(任一已知目标)"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "(不区分)"
	}
	return s
}

func relDir(root, dir string) string {
	if rel, err := filepath.Rel(root, dir); err == nil {
		return rel
	}
	return dir
}

// sortRequirements 让报告顺序稳定，便于人工复核与 diff。
func sortRequirements(rs []RequirementEval) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].CaseID != rs[j].CaseID {
			return rs[i].CaseID < rs[j].CaseID
		}
		if rs[i].Target != rs[j].Target {
			return rs[i].Target < rs[j].Target
		}
		return rs[i].Display < rs[j].Display
	})
}

// failGate 标记 gate 判死，退出码固定为 2（门禁/配置缺失或未知）。
func failGate(rep *GateReport, reason string) {
	rep.Passed = false
	if rep.ExitCode == 0 {
		rep.ExitCode = 2
	}
	rep.Reasons = append(rep.Reasons, reason)
}

// validateEvidence 校验单条证据的完整性。
//
// 注意：gate 只能校验人工证据的结构完整性；结构通过不等于事实审查通过。
func validateEvidence(ev evidenceFile, caseID, gateID string, base GateBaseline, entry CaseEntry) []string {
	var p []string
	if ev.GateID != gateID {
		p = append(p, fmt.Sprintf("证据的 gateId=%q 与请求的 %s 不一致（目录 %s）",
			ev.GateID, gateID, ev.dir))
	}
	if !contains(KnownTargets, ev.TargetID) {
		p = append(p, fmt.Sprintf("未知 target: %s", ev.TargetID))
	}
	if ev.WorktreeDirty {
		p = append(p, "证据来自 dirty 工作区：代码不是 sourceRevision 那一份，不能贴该 revision 当结论")
	}
	if base.SourceRevision != "" && ev.SourceRevision != base.SourceRevision {
		p = append(p, fmt.Sprintf("sourceRevision 过期: 证据=%q 基线=%q", ev.SourceRevision, base.SourceRevision))
	}
	if base.VersionsLockHash != "" && ev.VersionsLockHash != base.VersionsLockHash {
		p = append(p, "versions.lock 哈希不匹配（可能被篡改或过期）")
	}
	if ev.Reviewer.Name == "" || ev.Reviewer.Machine == "" {
		p = append(p, "缺审查人/机器信息（reviewer.name 或 reviewer.machine 为空）")
	}
	if symlink := evidenceTreeSymlink(ev.dir); symlink != "" {
		p = append(p, "证据目录包含符号链接，不能引用目录外可变内容: "+symlink)
	}
	if ev.ReportHash == "" {
		p = append(p, "reportHash 缺失，无法校验报告完整性")
	} else {
		actual := ComputeEvidenceHash(ev.dir)
		if actual != ev.ReportHash {
			p = append(p, "报告哈希被篡改（与文件实际哈希不符）")
		}
	}
	if ev.ExitCode != 0 {
		p = append(p, fmt.Sprintf("命令退出码非零: %d", ev.ExitCode))
	}
	if evidenceAllSkippedOrEmpty(ev.GateEvidence) {
		p = append(p, "报告为空或全部 SKIP，不能冒充通过")
	}
	p = append(p, validateCaseStatuses(ev.GateEvidence, caseID, ev.dir, entry)...)
	return p
}

// caseResultMatches 判断单条 CaseResult 是否属于该 case。
// 子 case 允许两种写法：{id:"SEC03", subcase:"runtime"} 或 {id:"SEC03.runtime"}。
func caseResultMatches(caseID string, cr contracts.CaseResult) bool {
	if cr.ID == caseID {
		return true
	}
	if cr.Subcase == caseID {
		return true
	}
	if cr.ID+"."+cr.Subcase == caseID {
		return true
	}
	return false
}

// validateCaseStatuses 校验用例状态。
// 这是最容易漏的一环：命令 exitCode=0 但用例状态是 FAIL/BLOCKED/NOT_RUN 的报告，
// 不得因"非空且非全 SKIP"就被判通过。必需 case 只认 PASS。
func validateCaseStatuses(ev contracts.GateEvidence, caseID, evidenceDir string, entry CaseEntry) []string {
	var p []string
	matched := 0
	passed := 0
	for _, cr := range ev.CaseResults {
		if !contracts.IsValidStatus(cr.Status) {
			p = append(p, fmt.Sprintf("用例状态非法: %q（id=%s）", cr.Status, cr.ID))
			continue
		}
		if !caseResultMatches(caseID, cr) {
			continue
		}
		matched++
		if cr.Status == contracts.StatusPass {
			passed++
			if strings.TrimSpace(cr.Expected) == "" || strings.TrimSpace(cr.Actual) == "" ||
				strings.TrimSpace(cr.StartedAt) == "" || strings.TrimSpace(cr.EndedAt) == "" {
				p = append(p, "PASS case 缺 expected/actual/startedAt/endedAt，属于不可审计的自报成功")
			}
			p = append(p, validateOraclePath(evidenceDir, cr.OraclePath, ev.Command, ev.ExitCode, entry.OracleKind == "external")...)
		} else {
			p = append(p, fmt.Sprintf("必需 case 状态为 %s（只认 PASS）", cr.Status))
		}
	}
	if matched == 0 {
		p = append(p, "报告中没有属于该 case 的用例结果（caseResults 与 case 未绑定）")
	} else if passed == 0 {
		p = append(p, "该 case 没有任何 PASS 结果")
	}
	return p
}

func validateOraclePath(evidenceDir, oraclePath, command string, exitCode int, wantExternal bool) []string {
	if strings.TrimSpace(oraclePath) == "" {
		return []string{"PASS case 缺 oraclePath，不能只由实现自身报告成功"}
	}
	clean := filepath.Clean(oraclePath)
	switch clean {
	case "result.json", "command.txt", "stdout.log", "stderr.log":
		return []string{"oraclePath 不能指向 runner 自报结果、命令或输出文件"}
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return []string{"oraclePath 必须是证据目录内的相对路径"}
	}
	full := filepath.Join(evidenceDir, clean)
	rel, err := filepath.Rel(evidenceDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return []string{"oraclePath 越出证据目录"}
	}
	current := evidenceDir
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return []string{fmt.Sprintf("oraclePath 不可读: %s", oraclePath)}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return []string{"oraclePath 或其父目录是符号链接"}
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return []string{fmt.Sprintf("oraclePath 不可读: %s", oraclePath)}
	}
	if !info.Mode().IsRegular() {
		return []string{"oraclePath 不是常规文件"}
	}
	if info.Size() == 0 {
		return []string{"oraclePath 是空文件，不能证明外部结果"}
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return []string{"oraclePath 不可读: " + err.Error()}
	}
	var header oracleHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return []string{"oracle 不是合法 JSON: " + err.Error()}
	}
	switch header.Kind {
	case "test-process":
		if wantExternal {
			return []string{"case 要求实现边界之外的 oracle，test-process 自证不满足"}
		}
		if err := validateProcessOracle(evidenceDir, clean, command, exitCode); err != nil {
			return []string{"oracle 无法独立核对子测试进程: " + err.Error()}
		}
	case "external-observation":
		if err := validateExternalOracle(evidenceDir, clean); err != nil {
			return []string{"external oracle 不成立: " + err.Error()}
		}
	default:
		return []string{fmt.Sprintf("未知 oracle kind: %q", header.Kind)}
	}
	return nil
}

func evidenceTreeSymlink(dir string) string {
	var found string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || found != "" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if rel, err := filepath.Rel(dir, path); err == nil {
				found = filepath.ToSlash(rel)
			} else {
				found = path
			}
		}
		return nil
	})
	return found
}

// evidenceAllSkippedOrEmpty 判断报告是否为空或全部 SKIP。
func evidenceAllSkippedOrEmpty(ev contracts.GateEvidence) bool {
	if len(ev.CaseResults) == 0 {
		return true
	}
	for _, cr := range ev.CaseResults {
		if cr.Status != contracts.StatusSkip {
			return false
		}
	}
	return true
}

// RunGate 是 `gate` 子命令的入口。
func RunGate(gateID, evidenceRoot string) int {
	if !contains(KnownGates, gateID) {
		fmt.Fprintf(os.Stderr, "未知 gate: %s（已知: %s）\n", gateID, strings.Join(KnownGates, ","))
		return 2
	}
	if strings.TrimSpace(evidenceRoot) == "" {
		fmt.Fprintln(os.Stderr, "gate 必须指定 --evidence 证据根目录")
		return 2
	}
	if _, err := os.Stat(evidenceRoot); err != nil {
		fmt.Fprintf(os.Stderr, "证据根目录不可读: %s: %v\n", evidenceRoot, err)
		return 2
	}
	idxPath := "tests/acceptance/cases.json"
	idx, err := LoadCasesIndex(idxPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	base, err := BuildBaseline()
	if err != nil {
		fmt.Fprintf(os.Stderr, "无法构建门禁基线: %v\n", err)
		return 2
	}
	rep, err := EvaluateGate(idx, gateID, evidenceRoot, base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gate 评估错误: %v\n", err)
		return 2
	}
	// 打印可读报告，便于人工/CI 复核（绝不写 passed.json 冒充执行）。
	fmt.Printf("== gate --id=%s ==\n", gateID)
	fmt.Printf("基线 revision=%s worktreeDirty=%v 证据根=%s\n", orNone(rep.SourceRevision), rep.WorktreeDirty, evidenceRoot)
	fmt.Printf("passed=%v exitCode=%d 必需证据位=%d\n", rep.Passed, rep.ExitCode, len(rep.PerRequirement))
	failed := 0
	for _, r := range rep.PerRequirement {
		if r.Status == "pass" {
			continue
		}
		failed++
		fmt.Printf("  [%s] %s target=%s display=%s found=%v\n",
			statusOr(r.Status, "missing"), r.CaseID, orAny(r.Target), orNone(r.Display), r.Found)
		if r.Reason != "" {
			fmt.Printf("      %s\n", r.Reason)
		}
	}
	fmt.Printf("通过 %d / 不通过 %d\n", len(rep.PerRequirement)-failed, failed)
	printPerTargetSummary(rep)
	for id, ce := range rep.PerCase {
		if ce.Status == "pass" {
			continue
		}
		fmt.Printf("  [case %s] status=%s found=%v\n", id, statusOr(ce.Status, "missing"), ce.Found)
		if ce.Reason != "" {
			fmt.Printf("      %s\n", ce.Reason)
		}
	}
	for _, r := range rep.Reasons {
		fmt.Printf("  - %s\n", r)
	}
	return rep.ExitCode
}

func statusOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// printPerTargetSummary 按目标汇总，便于单目标机器上做诊断。
//
// 这不是另一个通过口径：gate 结论仍然是全矩阵，某一行 18/18 只说明
// 「这个目标本身的证据齐了」，不代表 gate 通过（其它目标可能一条都没有）。
func printPerTargetSummary(rep *GateReport) {
	type tally struct{ pass, fail int }
	perTarget := map[string]*tally{}
	for _, r := range rep.PerRequirement {
		key := r.Target
		if key == "" {
			key = "(HOST 任一目标)"
		}
		t := perTarget[key]
		if t == nil {
			t = &tally{}
			perTarget[key] = t
		}
		if r.Status == "pass" {
			t.pass++
		} else {
			t.fail++
		}
	}
	keys := make([]string, 0, len(perTarget))
	for k := range perTarget {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("按目标汇总（仅供参考，全矩阵才是门禁结论）:")
	for _, k := range keys {
		t := perTarget[k]
		fmt.Printf("  %-22s %d/%d%s\n", k, t.pass, t.pass+t.fail, map[bool]string{true: " ✓本目标自身齐全", false: ""}[t.fail == 0])
	}
}
