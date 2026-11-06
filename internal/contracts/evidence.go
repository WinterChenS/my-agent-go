package contracts

// 用例结果状态枚举：不允许 null/缺字段当作通过。
// 任何未知值经 IsValidStatus 判定为不合法，门禁必须判死。
const (
	StatusPass    = "PASS"
	StatusFail    = "FAIL"
	StatusBlocked = "BLOCKED"
	StatusSkip    = "SKIP"
	StatusNotRun  = "NOT_RUN"
)

// IsValidStatus 校验用例状态是否为已知枚举值。
func IsValidStatus(s string) bool {
	switch s {
	case StatusPass, StatusFail, StatusBlocked, StatusSkip, StatusNotRun:
		return true
	}
	return false
}

// Reviewer 记录人工/机器审查元数据（v1-gates.md §4）。
// 注意：gate 只能校验该结构完整性；结构通过不等于事实审查通过。
// 真实机器/人员审查由独立的审查流程负责，脚本不得自行生成“已审查”字段。
type Reviewer struct {
	Name       string `json:"name"`
	Machine    string `json:"machine"`
	ReviewedAt string `json:"reviewedAt"`
	// Signature 为可选审查签名；结构存在不代表事实有效。
	Signature string `json:"signature,omitempty"`
	Note      string `json:"note,omitempty"`
}

// CaseResult 是单个用例结果的原子记录（v1-gates.md §4）。
// 每个结果必须含 id/subcase/status/expected/actual/oraclePath/startedAt/endedAt。
type CaseResult struct {
	ID         string `json:"id"`
	Subcase    string `json:"subcase,omitempty"`
	Status     string `json:"status"`
	Expected   string `json:"expected"`
	Actual     string `json:"actual"`
	OraclePath string `json:"oraclePath,omitempty"`
	StartedAt  string `json:"startedAt"`
	EndedAt    string `json:"endedAt"`
}

// ArtifactRef 指向证据目录中的产物文件。
type ArtifactRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Hash string `json:"hash,omitempty"`
}

// Environment 记录生成证据时的真实运行环境（GOV05）。
//
// 为什么证据必须自带环境身份：同一份 PASS 在 Windows 10 build 19045 上产生的
// 和 Windows 11 24H2 上产生的，结论强度完全不同。只贴 targetId 时两者长得一模一样，
// 门禁无法拒绝「低于最低验收系统」的证据。这里记录的是 runner 实测值，
// 不是目标声明值；写声明值等于自证，门禁会按目标最低要求校验。
//
// 字段口径：
//   - os/arch：runtime.GOOS / runtime.GOARCH；
//   - osVersion：Windows "10"/"11"；macOS "14.5"；Linux VERSION_ID "24.04"；
//   - osBuild：Windows build "19045"；macOS buildVersion；Linux 留空；
//   - display：desktop / x11 / wayland；Linux 集成证据可为空（不区分会话）。
type Environment struct {
	OS        string `json:"os"`
	OSVersion string `json:"osVersion"`
	OSBuild   string `json:"osBuild"`
	Arch      string `json:"arch"`
	Display   string `json:"display"`
}

// GateEvidence 是一条用例门禁证据。
// 关键纪律：绝不允许用写出 passed.json 冒充执行；子进程真实 exit code 与
// 原始 stdout/stderr 必须保留。reportHash 在证据生成时由 evidence 工具计算，
// 门禁重新计算并比对，用于发现报告被篡改（GOV02）。
type GateEvidence struct {
	SchemaVersion    int    `json:"schemaVersion"`
	GateID           string `json:"gateId"`
	TaskID           string `json:"taskId"`
	SourceRevision   string `json:"sourceRevision"`
	VersionsLockHash string `json:"versionsLockHash"`
	TargetID         string `json:"targetId"`
	// Environment 是生成证据时的真实宿主环境（GOV05）。
	// 不加 omitempty：缺环境身份的证据必须显式判死，而不是被序列化悄悄省略。
	Environment Environment `json:"environment"`
	// Display 是原生/显示会话标识（Linux: x11/wayland；Windows/macOS: desktop）。
	// 不区分显示的用例留空：门禁会把非 Linux 目标的空值归一化为 desktop。
	// 加这个维度是因为「gate 按 case 汇总」会丢平台维度：linux 只跑 X11 也能
	// 满足 LNX 的 case，结论就变成了「Linux 通过」。
	Display string `json:"display,omitempty"`
	// WorktreeDirty 表示生成证据时工作区不干净。
	// 门禁判死：dirty 工作区的代码不是 sourceRevision 那一份，贴上去就是假归属。
	WorktreeDirty bool          `json:"worktreeDirty,omitempty"`
	StartedAt     string        `json:"startedAt"`
	EndedAt       string        `json:"endedAt"`
	Command       string        `json:"command"`
	ExitCode      int           `json:"exitCode"`
	CaseResults   []CaseResult  `json:"caseResults"`
	Artifacts     []ArtifactRef `json:"artifacts,omitempty"`
	Limitations   string        `json:"limitations,omitempty"`
	Reviewer      Reviewer      `json:"reviewer"`
	// ReportHash 是命令/输出/结果文件的规范化哈希，用于篡改检测。
	ReportHash string `json:"reportHash,omitempty"`
}
