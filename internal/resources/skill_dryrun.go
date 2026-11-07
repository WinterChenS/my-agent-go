// 技能的隔离试运行（RES05 的第二半）。
//
// "只读"不等于"安全"：试运行必须先把环境缩到最小，再谈工具策略。
// 一个试运行沙箱由四件东西组成：
//  1. 临时 agentDir（Pi 的 PI_CODING_AGENT_DIR 指这里，不碰用户真实配置）；
//  2. 空 scratch 工作区（工作目录里没有用户的任何文件）；
//  3. 待测技能的只读副本（托管目录里的原件在整个过程中不被读写）；
//  4. 只读权限天花板 + 没有审批人（需要人工批准的工具等于被拒绝）。
//
// 外部 MCP / 连接器 / 浏览器不是"策略里拒一下"就够：它们根本不被挂载
// （隔离参数 --no-mcp、不加载额外扩展），所以试运行里也不会出现这些工具。
// 万一出现了（扩展自带同名工具），GuardedDecision 会让"需要审批"变成拒绝。
package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/runtime/pi"
)

// 试运行被显式禁用的能力名。
const (
	DryRunCapabilityWrite       = "write"
	DryRunCapabilityShell       = "shell"
	DryRunCapabilityNetwork     = "network"
	DryRunCapabilityBrowser     = "browser"
	DryRunCapabilityConnectors  = "connectors"
	DryRunCapabilityExternalMCP = "external-mcp"
)

// 试运行的拒绝规则 ID（前端按 ID 取文案）。
const (
	DryRunRuleWrite          = "DRYRUN_WRITE_DENIED"
	DryRunRuleShell          = "DRYRUN_SHELL_DENIED"
	DryRunRuleNetwork        = "DRYRUN_NETWORK_DENIED"
	DryRunRuleBrowser        = "DRYRUN_BROWSER_DENIED"
	DryRunRuleConnectors     = "DRYRUN_CONNECTORS_DENIED"
	DryRunRuleExternalMCP    = "DRYRUN_EXTERNAL_MCP_DENIED"
	DryRunRuleApprovalAbsent = "DRYRUN_APPROVAL_NOT_AVAILABLE"
)

// DryRunCapability 是"这次试运行没有提供什么"的一条说明。
type DryRunCapability struct {
	Name string `json:"name"`
	Rule string `json:"rule"`
}

// DryRunSandbox 是一次隔离试运行的环境。
type DryRunSandbox struct {
	root      string
	agentDir  string
	workspace string
	skillDir  string
	skillPath string
	digest    string

	once    sync.Once
	cleaned bool
}

// BeginDryRun 建一个试运行沙箱。
//
// appRoot 是应用数据根（沙箱必须落在同一卷上，便于原子删除）；
// sourceSkillDir 是托管目录里的技能目录——只读，整个试运行不写它。
func BeginDryRun(appRoot, skillID, sourceSkillDir string) (*DryRunSandbox, error) {
	id := strings.TrimSpace(skillID)
	if id == "" {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "试运行缺少技能 ID", false)
	}
	src := filepath.Clean(strings.TrimSpace(sourceSkillDir))
	if src == "" || src == "." {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "试运行缺少技能目录", false)
	}
	digest, err := dirDigest(src)
	if err != nil {
		return nil, err
	}
	if _, err := readSkillFile(filepath.Join(src, "SKILL.md")); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(appRoot, 0o700); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建试运行根目录失败: "+err.Error(), false)
	}
	root, err := os.MkdirTemp(appRoot, "dryrun-")
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建试运行目录失败: "+err.Error(), false)
	}
	sb := &DryRunSandbox{
		root:      root,
		agentDir:  filepath.Join(root, "agent"),
		workspace: filepath.Join(root, "workspace"),
		skillDir:  filepath.Join(root, "skills", id),
		digest:    digest,
	}
	sb.skillPath = filepath.Join(sb.skillDir, "SKILL.md")
	if err := os.MkdirAll(sb.agentDir, 0o700); err != nil {
		_ = sb.Cleanup()
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建临时 agentDir 失败: "+err.Error(), false)
	}
	if err := os.MkdirAll(sb.workspace, 0o700); err != nil {
		_ = sb.Cleanup()
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建临时工作区失败: "+err.Error(), false)
	}
	if err := copyDirReadOnly(src, sb.skillDir); err != nil {
		_ = sb.Cleanup()
		return nil, err
	}
	return sb, nil
}

// Root / AgentDir / WorkspaceDir / SkillPath 是沙箱里各处的绝对路径。
func (sb *DryRunSandbox) Root() string         { return sb.root }
func (sb *DryRunSandbox) AgentDir() string     { return sb.agentDir }
func (sb *DryRunSandbox) WorkspaceDir() string { return sb.workspace }
func (sb *DryRunSandbox) SkillPath() string    { return sb.skillPath }

// Permissions 是试运行的权限天花板：只读文件系统、无网络、无 shell。
func (sb *DryRunSandbox) Permissions() config.Permissions {
	return config.Permissions{Filesystem: "read", Network: false, Shell: false}
}

// Policy 构造只读策略服务。
//
// PathGuard 仍然绑在临时工作区上：文件类工具即便只读，也要先证明
// 资源范围在沙箱内，而不是"读哪个路径都行"。
func (sb *DryRunSandbox) Policy() (*policy.Service, error) {
	guard, err := policy.NewPathGuard(sb.workspace)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "构造试运行路径守卫失败: "+err.Error(), false)
	}
	return policy.New(policy.Options{
		Permissions: sb.Permissions(),
		PathGuard:   guard,
		// 试运行没有审批 UI：给一个短 TTL，避免"审批挂着等一个不会来的人"。
		ApprovalTTL: 5 * time.Second,
	}), nil
}

// Isolation 返回试运行的 Pi 隔离参数。
//
// 只挂载沙箱里的那一份技能；不启用自有 MCP、不加载额外扩展。
// policyExtension 为空时 Validate 会失败——没有执行前授权就没有"只读"。
func (sb *DryRunSandbox) Isolation(policyExtension string) pi.IsolationSpec {
	return pi.IsolationSpec{
		PolicyExtension: strings.TrimSpace(policyExtension),
		HarnessMCP:      false,
		ExtraExtensions: nil,
		Skills:          []string{sb.skillPath},
	}
}

// GuardedDecision 把策略结论压成试运行结论：只有 allow 才算允许。
//
// "需要审批"在试运行里等于拒绝：没有审批人，也没有人会为一次试运行
// 批准外部连接器调用。
func (sb *DryRunSandbox) GuardedDecision(res policy.Result) policy.Decision {
	return approveNothing(res).Decision
}

// DeniedCapabilities 返回"这次试运行没有提供什么"的完整清单。
func (sb *DryRunSandbox) DeniedCapabilities() []DryRunCapability {
	return []DryRunCapability{
		{Name: DryRunCapabilityWrite, Rule: DryRunRuleWrite},
		{Name: DryRunCapabilityShell, Rule: DryRunRuleShell},
		{Name: DryRunCapabilityNetwork, Rule: DryRunRuleNetwork},
		{Name: DryRunCapabilityBrowser, Rule: DryRunRuleBrowser},
		{Name: DryRunCapabilityConnectors, Rule: DryRunRuleConnectors},
		{Name: DryRunCapabilityExternalMCP, Rule: DryRunRuleExternalMCP},
	}
}

// SkillDigest 返回沙箱里技能副本的内容摘要。
func (sb *DryRunSandbox) SkillDigest() (string, error) {
	return dirDigest(sb.skillDir)
}

// SourceSkillDigest 返回开始试运行时的源技能摘要（用于事后核对原件没被改）。
func (sb *DryRunSandbox) SourceSkillDigest() string { return sb.digest }

// VerifyUserStateUnchanged 对比执行前后的用户目录摘要。
//
// paths 在这里由调用方（绑定层）给出：真实 agentDir、真实工作区、数据根。
// 缺失路径用空摘要表示，与"空目录"区分开。
func (sb *DryRunSandbox) VerifyUserStateUnchanged(before map[string]string) error {
	paths := make([]string, 0, len(before))
	for p := range before {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	after, err := TreeDigest(paths...)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "试运行后无法核对用户数据: "+err.Error(), false)
	}
	var changed []string
	for _, p := range paths {
		if before[p] != after[p] {
			changed = append(changed, p)
		}
	}
	if len(changed) > 0 {
		return contracts.NewAppError(contracts.CodeVerificationFailed,
			"试运行改动了用户数据: "+strings.Join(changed, ", "), false)
	}
	return nil
}

// Cleanup 删除整个沙箱。可重复调用。
func (sb *DryRunSandbox) Cleanup() error {
	var err error
	sb.once.Do(func() {
		err = os.RemoveAll(sb.root)
		sb.cleaned = err == nil
	})
	return err
}

// Cleaned 表示沙箱目录已经被删除。
func (sb *DryRunSandbox) Cleaned() bool {
	if !sb.cleaned {
		return false
	}
	_, err := os.Stat(sb.root)
	return errors.Is(err, os.ErrNotExist)
}

// TreeDigest 计算一组路径的内容摘要。
//
// 路径不存在时摘要为空串：空串是"这里没有东西"，与"空目录的摘要"不同，
// 界面据此显示 unavailable 而不是 0。
func TreeDigest(paths ...string) (map[string]string, error) {
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "路径无法绝对化: "+err.Error(), false)
		}
		if _, err := os.Stat(abs); errors.Is(err, fs.ErrNotExist) {
			out[p] = ""
			continue
		} else if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "读取路径失败: "+err.Error(), false)
		}
		digest, err := dirDigest(abs)
		if err != nil {
			return nil, err
		}
		out[p] = digest
	}
	return out, nil
}

// dirDigest 对目录（或单文件）计算稳定摘要。
//
// 内容 + 相对路径都进摘要：只算内容的话，把 a.md 改名成 b.md 不会被发现。
func dirDigest(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", contracts.NewAppError(contracts.CodeNotFound, "路径不存在: "+root, false)
		}
		return "", contracts.NewAppError(contracts.CodeInternal, "读取路径失败: "+err.Error(), false)
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(root)
		if err != nil {
			return "", contracts.NewAppError(contracts.CodeInternal, "读取文件失败: "+err.Error(), false)
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), nil
	}
	type entry struct {
		rel  string
		size int64
		sum  string
	}
	var entries []entry
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			// 读不了的文件（权限/占用）用标记而不是跳过：
			// 跳过会让"读不出来"看起来像"内容没变"。
			entries = append(entries, entry{rel: filepath.ToSlash(rel), size: -1, sum: "unreadable"})
			return nil
		}
		sum := sha256.Sum256(raw)
		entries = append(entries, entry{rel: filepath.ToSlash(rel), size: int64(len(raw)), sum: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "遍历目录失败: "+err.Error(), false)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e.rel))
		h.Write([]byte{0})
		h.Write([]byte(e.sum))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32], nil
}

// copyDirReadOnly 复制技能目录到沙箱。
//
// 符号链接直接拒绝（与导入同一条规则）；文件权限只给 0600：
// 试运行不需要可执行位，去掉它也能避免"临时目录里的脚本被别的工具顺手执行"。
func copyDirReadOnly(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return contracts.NewAppError(contracts.CodeUnsupported, "读取技能目录失败: "+walkErr.Error(), false)
		}
		rel, relErr := filepath.Rel(src, p)
		if relErr != nil {
			return contracts.NewAppError(contracts.CodeInternal, "计算相对路径失败: "+relErr.Error(), false)
		}
		target := filepath.Join(dst, rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return contracts.NewAppError(contracts.CodeUnsupported,
				"技能目录里不允许符号链接: "+rel, false)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return contracts.NewAppError(contracts.CodeUnsupported, "技能目录里有非普通文件: "+rel, false)
		}
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			return contracts.NewAppError(contracts.CodeInternal, "读取技能文件失败: "+readErr.Error(), false)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return contracts.NewAppError(contracts.CodeInternal, "创建目录失败: "+err.Error(), false)
		}
		return os.WriteFile(target, raw, 0o600)
	})
}

// approveNothing 让"需要审批"在试运行里必然落到拒绝。
//
// 生产调用由绑定层显式处理审批事件（一律答复 deny）；这里保留一个
// 可断言的映射函数，避免两边各写一份"什么算允许"。
func approveNothing(res policy.Result) policy.Result {
	if res.Decision == policy.DecisionAsk {
		return policy.Result{Decision: policy.DecisionDeny, Reason: DryRunRuleApprovalAbsent}
	}
	return res
}
