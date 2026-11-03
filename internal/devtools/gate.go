package devtools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
)

// KnownGates 列出所有门禁阶段。
var KnownGates = []string{"G0", "G1", "G2", "G3", "G4", "G5"}

// KnownTargets 是六目标平台（v1-gates.md §1）。未知 target 必须判死。
var KnownTargets = []string{
	"win-x64", "win-arm64", "mac-x64", "mac-arm64", "linux-x64", "linux-arm64",
}

// CaseEntry 是机器索引中的单条用例记录。
type CaseEntry struct {
	ID        string   `json:"id"`
	Owner     string   `json:"owner"`
	Runner    string   `json:"runner"`
	Required  bool     `json:"required"`
	Scope     string   `json:"scope"`
	Phase     string   `json:"phase"`
	Gates     []string `json:"gates"`
	Composite bool     `json:"composite,omitempty"`
	Subcases  []string `json:"subcases,omitempty"`
	Manual    bool     `json:"manual,omitempty"`
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

// CaseEval 是单个必需 case 的评估结果。
type CaseEval struct {
	CaseID string `json:"caseId"`
	Found  bool   `json:"found"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// GateReport 汇总门禁判定结果。
type GateReport struct {
	GateID   string              `json:"gateId"`
	Passed   bool                `json:"passed"`
	ExitCode int                 `json:"exitCode"`
	Reasons  []string            `json:"reasons"`
	PerCase  map[string]CaseEval `json:"perCase"`
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

// evidenceFile 是把 GateEvidence 与其所在目录绑定，便于重新计算报告哈希。
type evidenceFile struct {
	contracts.GateEvidence
	dir string
}

// EvaluateGate 评估某 gate 的证据树，返回退出码与报告。
//
// 门禁判定必须把以下情况全部判死（GOV01/GOV02）：
//   - 缺必需 case（证据树中未找到该 case 目录）
//   - 旧 revision / 旧 versions.lock 哈希（与基线不一致）
//   - 空 suite 或全 SKIP 的 native 报告
//   - 报告哈希被篡改（重新计算与 result.json.reportHash 不符）
//   - 人工证据缺审查人/机器信息
//   - 未知 target / 未知 gate
//
// 任一必需 case 不满足，gate 即判死，退出码固定为 2（配置/索引/门禁缺失或未知）。
func EvaluateGate(idx *CasesIndex, gateID, evidenceRoot string, base GateBaseline) (*GateReport, error) {
	rep := &GateReport{
		GateID:  gateID,
		Passed:  true,
		Reasons: []string{},
		PerCase: map[string]CaseEval{},
	}
	if !contains(KnownGates, gateID) {
		failGate(rep, fmt.Sprintf("未知 gate: %s（已知: %s）", gateID, strings.Join(KnownGates, ",")))
		return rep, nil
	}
	required := ResolveRequiredAtomicCases(idx, gateID)
	if len(required) == 0 {
		failGate(rep, fmt.Sprintf("gate %s 没有必需 case（索引缺失或不完整）", gateID))
		return rep, nil
	}

	// 收集证据树中的所有 result.json；caseId 取目录名。
	found := map[string]evidenceFile{}
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
		found[caseID] = evidenceFile{GateEvidence: ev, dir: dir}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	for _, id := range required {
		ce := CaseEval{CaseID: id, Found: false}
		ev, ok := found[id]
		if !ok {
			ce.Reason = "缺必需 case 证据（证据树中未找到该 case 目录）"
			rep.PerCase[id] = ce
			failGate(rep, fmt.Sprintf("缺必需 case: %s", id))
			continue
		}
		ce.Found = true
		problems := validateEvidence(ev, id, base)
		if evidenceAllSkippedOrEmpty(ev.GateEvidence) {
			problems = append(problems, "报告为空或全部 SKIP，不能冒充通过")
		}
		if len(problems) > 0 {
			ce.Status = "rejected"
			ce.Reason = strings.Join(problems, "; ")
			rep.PerCase[id] = ce
			failGate(rep, fmt.Sprintf("case %s: %s", id, ce.Reason))
			continue
		}
		ce.Status = "pass"
		rep.PerCase[id] = ce
	}
	return rep, nil
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
// 注意：gate 只能校验人工证据的结构完整性；结构通过不等于事实审查通过。
func validateEvidence(ev evidenceFile, caseID string, base GateBaseline) []string {
	var p []string
	if !contains(KnownTargets, ev.TargetID) {
		p = append(p, fmt.Sprintf("未知 target: %s", ev.TargetID))
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
	p = append(p, validateCaseStatuses(ev.GateEvidence, caseID)...)
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
func validateCaseStatuses(ev contracts.GateEvidence, caseID string) []string {
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
	fmt.Printf("passed=%v exitCode=%d\n", rep.Passed, rep.ExitCode)
	for id, ce := range rep.PerCase {
		fmt.Printf("  [%s] found=%v status=%s\n", id, ce.Found, ce.Status)
		if ce.Reason != "" {
			fmt.Printf("      %s\n", ce.Reason)
		}
	}
	for _, r := range rep.Reasons {
		fmt.Printf("  - %s\n", r)
	}
	return rep.ExitCode
}
