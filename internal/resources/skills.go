// 技能资源（C03）。
//
// 三条边界：
//   - 导入是"复制进产品管理目录"，源目录只读；导入过程不执行技能里的任何脚本、
//     不安装任何命令。用户导入一个技能 = 复制文件，不等于允许它运行。
//   - 托管技能是否被 Pi 加载，由配置里的 enabled 决定：启用的技能逐个
//     `--skill <绝对路径>` 显式加载（托管目录不在 Pi 的发现路径上，不显式传就不加载）。
//     Pi 的用户级技能自动发现按 2026-11-04 方向保持打开（不再用 -ns 抑制），
//     <home>/.agents/skills 等本地技能直接同步过来；项目级 .pi/skills 与祖先
//     目录的 .agents/skills 仍由 -na 切断（2026-11-04 真实 Pi 1.5.0 实测）。
//     已知限制（实测）：同名时 Pi 先加载自动发现副本、再加载 --skill，保留先出现的
//     那条。所以禁用托管技能不保证同名副本消失，启用托管技能也不保证覆盖副本。
//   - 依赖缺失直接标为不可用并给出原因，不静默安装、也不"先加载再说"。
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// ResourceScope 是资源的生效范围。
type ResourceScope string

const (
	// ScopeUser 对全部工作区生效。
	ScopeUser ResourceScope = "user"
	// ScopeWorkspace 只对指定工作区生效。
	ScopeWorkspace ResourceScope = "workspace"
)

// OriginImported 表示这条资源由用户从本地导入。
const OriginImported = "imported"

// SkillResource 是一条可管理的技能。
//
// 不含"原始内容"：SKILL.md 由用户在编辑器里改，产品只保存"它在哪、是否启用"。
// 名称/描述/哈希每次列举时从文件重读——存一份副本意味着用户手改文件后，
// 界面还在展示旧描述，而运行时加载的是新内容，两边说的不是同一个技能。
type SkillResource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Path 是托管目录里 SKILL.md 的绝对路径。
	Path        string        `json:"path"`
	Scope       ResourceScope `json:"scope"`
	WorkspaceID string        `json:"workspaceId,omitempty"`
	Enabled     bool          `json:"enabled"`
	Origin      string        `json:"origin"`
	// Revision 是这条记录所属的配置版本（CAS 用）。
	Revision uint64 `json:"revision"`
	// Hash 是当前 SKILL.md 内容的 sha256 前 16 位，用于诊断与运行时签名。
	Hash string `json:"hash"`
	// Dependencies 是 frontmatter 里声明的外部命令依赖。
	Dependencies []string `json:"dependencies"`
	// MissingDependencies 是声明了但本机 PATH 上找不到的依赖。
	MissingDependencies []string `json:"missingDependencies"`
	// Available 为 false 时不会被交给 Pi；原因在 UnavailableReason。
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	// DisableModelInvocation 对应 frontmatter 的 disable-model-invocation。
	DisableModelInvocation bool  `json:"disableModelInvocation"`
	Bytes                  int64 `json:"bytes"`
}

// SkillSet 是一次列举的结果。
type SkillSet struct {
	Items    []SkillResource `json:"items"`
	Revision uint64          `json:"revision"`
}

// SkillPlan 是交给运行时的技能集合。
type SkillPlan struct {
	// Paths 是启用的技能 SKILL.md 绝对路径（按 ID 排序，保证签名稳定）。
	Paths []string
	// IDs 与 Paths 一一对应，供运行快照记录。
	IDs []string
	// Digest 是"启用集合 + 各自内容"的摘要，进入运行时签名。
	Digest string
	// Skipped 是"已启用但本次无法加载"的说明，运行时如实告知而不是静默丢弃。
	Skipped []SkillSkip
}

// SkillSkip 说明某条已启用的技能为何没有生效。
type SkillSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// skillEntry 是配置里保存的技能记录。
//
// 只保存"身份 + 位置 + 状态"：名称、描述、依赖全部从 SKILL.md 读，
// 避免同一事实存两份而分叉。
type skillEntry struct {
	ID          string        `json:"id"`
	Scope       ResourceScope `json:"scope"`
	WorkspaceID string        `json:"workspaceId,omitempty"`
	// Dir 是相对 app home 的目录名，便于整个 home 搬家。
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"`
	Origin  string `json:"origin"`
}

// 导入限制。数字不是拍脑袋：技能是给模型读的说明文本，不是数据集。
// 8 MiB / 200 个文件足以容纳带脚本与示例的技能包，同时挡住"误把整个仓库导入"。
const (
	maxSkillFileBytes = 512 << 10
	maxSkillTreeBytes = 8 << 20
	maxSkillFiles     = 200
)

// skillNamePattern 与 Pi 的 validateName 保持一致（小写字母、数字、连字符）。
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ListSkills 返回全部技能（用户级 + 全部工作区级）。
func (s *Service) ListSkills(ctx context.Context) (SkillSet, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return SkillSet{}, err
	}
	entries, err := decodeSkillEntries(doc.Body)
	if err != nil {
		return SkillSet{}, err
	}
	out := make([]SkillResource, 0, len(entries))
	for _, e := range entries {
		out = append(out, s.describeSkill(e, doc.Revision))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].ID < out[j].ID
	})
	return SkillSet{Items: out, Revision: doc.Revision}, nil
}

// GetSkill 读取单条技能。
func (s *Service) GetSkill(ctx context.Context, id string) (SkillResource, error) {
	set, err := s.ListSkills(ctx)
	if err != nil {
		return SkillResource{}, err
	}
	for _, it := range set.Items {
		if it.ID == id {
			return it, nil
		}
	}
	return SkillResource{}, contracts.NewAppError(contracts.CodeNotFound, "技能不存在: "+id, false)
}

// describeSkill 把一条记录变成对外的资源描述，元数据从文件实时读取。
func (s *Service) describeSkill(e skillEntry, revision uint64) SkillResource {
	res := SkillResource{
		ID:          e.ID,
		Scope:       e.Scope,
		WorkspaceID: e.WorkspaceID,
		Enabled:     e.Enabled,
		Origin:      e.Origin,
		Revision:    revision,
		Path:        s.skillFilePath(e),
	}
	doc, err := readSkillFile(res.Path)
	if err != nil {
		res.Available = false
		res.UnavailableReason = messageOf(err)
		return res
	}
	res.Name = doc.Manifest.Name
	res.Description = doc.Manifest.Description
	res.Hash = doc.Hash
	res.Bytes = doc.Bytes
	res.Dependencies = doc.Manifest.Requires
	res.DisableModelInvocation = doc.Manifest.DisableModelInvocation
	res.MissingDependencies = missingCommands(doc.Manifest.Requires, s.lookPath)
	if len(res.MissingDependencies) > 0 {
		res.Available = false
		res.UnavailableReason = "缺少依赖命令: " + strings.Join(res.MissingDependencies, ", ")
		return res
	}
	res.Available = true
	return res
}

// skillFilePath 计算一条记录的 SKILL.md 绝对路径。
func (s *Service) skillFilePath(e skillEntry) string {
	if strings.TrimSpace(e.Dir) != "" {
		return filepath.Join(s.layout.Root(), filepath.FromSlash(e.Dir), "SKILL.md")
	}
	return filepath.Join(s.managedSkillDir(e.Scope, e.WorkspaceID, e.ID), "SKILL.md")
}

// ImportSkill 把本地技能复制进产品管理目录。
//
// 源目录只读：整个过程只有 ReadDir/Open/Read，不做任何写、不执行任何东西。
// 目标已存在时拒绝——覆盖是另一个动作（OverwriteSkill），需要显式调用。
func (s *Service) ImportSkill(ctx context.Context, sourcePath string, scope ResourceScope, workspaceID string) (SkillResource, error) {
	if err := scope.validate(workspaceID); err != nil {
		return SkillResource{}, err
	}
	srcRoot, err := resolveSkillSource(sourcePath)
	if err != nil {
		return SkillResource{}, err
	}
	doc, err := readSkillFile(filepath.Join(srcRoot, "SKILL.md"))
	if err != nil {
		return SkillResource{}, err
	}
	id := doc.Manifest.Name

	// 先看配置里有没有同一个 ID：有就是"已存在"，不用再动文件系统。
	set, err := s.ListSkills(ctx)
	if err != nil {
		return SkillResource{}, err
	}
	for _, it := range set.Items {
		if it.ID == id {
			return SkillResource{}, contracts.NewAppError(contracts.CodeConfigConflict,
				"已存在同名技能 "+id+"；如需替换请用「覆盖导入」并确认版本", false)
		}
	}

	dstDir := s.managedSkillDir(scope, workspaceID, id)
	if _, statErr := os.Stat(dstDir); statErr == nil {
		return SkillResource{}, contracts.NewAppError(contracts.CodeConfigConflict,
			"目标目录已存在但不在配置里: "+dstDir+"；请先手工清理或换个技能名", false)
	}
	staged, err := stageTree(srcRoot, dstDir)
	if err != nil {
		return SkillResource{}, err
	}
	defer discardStaged(staged)

	entry := skillEntry{
		ID:          id,
		Scope:       scope,
		WorkspaceID: workspaceID,
		Dir:         s.relativeSkillDir(scope, workspaceID, id),
		Enabled:     true,
		Origin:      OriginImported,
	}
	// 导入是"新增一条"，用刚才读到的 revision 做 CAS：中途有人改了配置就失败，
	// 用户重试即可。这里不能无条件覆盖——那会顺带把别人的改动抹掉。
	if _, err := s.saveSkillEntry(ctx, entry, set.Revision); err != nil {
		return SkillResource{}, err
	}
	if err := finishInstall(staged, dstDir); err != nil {
		// 配置已写入而文件没就位：撤回配置，避免"列表里有、文件没有"。
		_ = s.dropSkillEntry(ctx, id)
		return SkillResource{}, err
	}
	return s.GetSkill(ctx, id)
}

// OverwriteSkill 用新的源目录内容替换一条已有技能（显式动作 + CAS）。
//
// 与 ImportSkill 分开是刻意的：覆盖会丢掉用户对现有 SKILL.md 的修改，
// 不能和"新增"共用一个入口，否则一次误点就静默丢了内容。
func (s *Service) OverwriteSkill(ctx context.Context, id, sourcePath string, expectedRevision uint64) (SkillResource, error) {
	srcRoot, err := resolveSkillSource(sourcePath)
	if err != nil {
		return SkillResource{}, err
	}
	doc, err := readSkillFile(filepath.Join(srcRoot, "SKILL.md"))
	if err != nil {
		return SkillResource{}, err
	}
	if doc.Manifest.Name != id {
		return SkillResource{}, contracts.NewAppError(contracts.CodeUnsupported,
			"源技能的 name（"+doc.Manifest.Name+"）与要覆盖的技能（"+id+"）不一致", false)
	}
	set, err := s.ListSkills(ctx)
	if err != nil {
		return SkillResource{}, err
	}
	if set.Revision != expectedRevision {
		return SkillResource{}, contracts.NewAppError(contracts.CodeConfigConflict,
			"配置已被修改（当前 revision "+strconv.FormatUint(set.Revision, 10)+
				"），覆盖前请刷新后再确认", false)
	}
	var target *SkillResource
	for i := range set.Items {
		if set.Items[i].ID == id {
			target = &set.Items[i]
			break
		}
	}
	if target == nil {
		return SkillResource{}, contracts.NewAppError(contracts.CodeNotFound, "技能不存在: "+id, false)
	}

	targetDir := filepath.Dir(target.Path)
	staged, err := stageTree(srcRoot, targetDir)
	if err != nil {
		return SkillResource{}, err
	}
	defer discardStaged(staged)
	return s.swapIn(ctx, staged, targetDir, id)
}

// SetSkillEnabled 启用/禁用一条技能（CAS）。
func (s *Service) SetSkillEnabled(ctx context.Context, id string, expectedRevision uint64, enabled bool) (SkillResource, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return SkillResource{}, err
	}
	entries, err := decodeSkillEntries(doc.Body)
	if err != nil {
		return SkillResource{}, err
	}
	idx := -1
	for i := range entries {
		if entries[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return SkillResource{}, contracts.NewAppError(contracts.CodeNotFound, "技能不存在: "+id, false)
	}
	entries[idx].Enabled = enabled
	if _, err := s.writeSkillEntries(ctx, doc.Body, entries, expectedRevision); err != nil {
		return SkillResource{}, err
	}
	return s.GetSkill(ctx, id)
}

// SaveSkillContent 覆盖一条技能的 SKILL.md（编辑）。
//
// 顺序是"先写临时文件、再原子替换"。配置里不存内容哈希，所以编辑不产生新的
// revision；expectedRevision 只用来确认"我编辑时看到的版本没有被别人改过"。
func (s *Service) SaveSkillContent(ctx context.Context, id string, expectedRevision uint64, content string) (SkillResource, error) {
	set, err := s.ListSkills(ctx)
	if err != nil {
		return SkillResource{}, err
	}
	if set.Revision != expectedRevision {
		return SkillResource{}, contracts.NewAppError(contracts.CodeConfigConflict,
			"配置已被修改（当前 revision "+strconv.FormatUint(set.Revision, 10)+
				"），请刷新后再保存", false)
	}
	var target *SkillResource
	for i := range set.Items {
		if set.Items[i].ID == id {
			target = &set.Items[i]
			break
		}
	}
	if target == nil {
		return SkillResource{}, contracts.NewAppError(contracts.CodeNotFound, "技能不存在: "+id, false)
	}
	normalized, err := normalizeSkillContent(content)
	if err != nil {
		return SkillResource{}, err
	}
	// 改名会让 ID 与内容对不上（ID 来自 name），这里直接拒绝，
	// 而不是悄悄把记录改到新名下——那会让"禁止覆盖"形同虚设。
	if normalized.Manifest.Name != id {
		return SkillResource{}, contracts.NewAppError(contracts.CodeUnsupported,
			"frontmatter 的 name（"+normalized.Manifest.Name+"）不能与技能 ID（"+id+"）不同；"+
				"要改名请重新导入", false)
	}
	if err := replaceFile(target.Path, []byte(normalized.Source)); err != nil {
		return SkillResource{}, err
	}
	out, err := s.GetSkill(ctx, id)
	if err != nil {
		return SkillResource{}, err
	}
	out.Revision = set.Revision
	return out, nil
}

// DeleteSkill 删除一条技能：先原子挪走目录，再 CAS 配置，最后真删。
//
// 顺序是为了两个方向都能回退：CAS 失败就把目录挪回去；CAS 成功而删除失败，
// 留下的只是垃圾而不是"配置说有、文件已没"的坏状态。
func (s *Service) DeleteSkill(ctx context.Context, expectedRevision uint64, id string) error {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return err
	}
	entries, err := decodeSkillEntries(doc.Body)
	if err != nil {
		return err
	}
	idx := -1
	for i := range entries {
		if entries[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return contracts.NewAppError(contracts.CodeNotFound, "技能不存在: "+id, false)
	}
	target := entries[idx]
	dir := filepath.Dir(s.skillFilePath(target))
	trash := dir + ".deleting"
	_ = os.RemoveAll(trash)
	moved := false
	if err := os.Rename(dir, trash); err == nil {
		moved = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return contracts.NewAppError(contracts.CodeInternal, "无法移除技能目录: "+err.Error(), false)
	}

	kept := make([]skillEntry, 0, len(entries)-1)
	kept = append(kept, entries[:idx]...)
	kept = append(kept, entries[idx+1:]...)
	if _, err := s.writeSkillEntries(ctx, doc.Body, kept, expectedRevision); err != nil {
		if moved {
			_ = os.Rename(trash, dir)
		}
		return err
	}
	if moved {
		_ = os.RemoveAll(trash)
	}
	return nil
}

// CompileSkills 计算本次运行要交给 Pi 的技能。
//
// 只包含"已启用 + 可用 + 作用域匹配"的技能；其余禁用项不出现，
// 已启用但不可用的进 Skipped，让上层能说清"为什么它没生效"。
func (s *Service) CompileSkills(ctx context.Context, workspaceID string) (SkillPlan, error) {
	set, err := s.ListSkills(ctx)
	if err != nil {
		return SkillPlan{}, err
	}
	plan := SkillPlan{}
	h := sha256.New()
	for _, it := range set.Items {
		if !it.Enabled {
			continue
		}
		if it.Scope == ScopeWorkspace && it.WorkspaceID != workspaceID {
			continue
		}
		if !it.Available {
			plan.Skipped = append(plan.Skipped, SkillSkip{ID: it.ID, Reason: it.UnavailableReason})
			continue
		}
		plan.Paths = append(plan.Paths, it.Path)
		plan.IDs = append(plan.IDs, it.ID)
		io.WriteString(h, it.ID+"\x00"+it.Hash+"\x00")
	}
	plan.Digest = hex.EncodeToString(h.Sum(nil))[:16]
	return plan, nil
}

// managedSkillDir 返回某个 ID 在托管目录里的目标目录。
func (s *Service) managedSkillDir(scope ResourceScope, workspaceID, id string) string {
	base := s.layout.UserSkillsDir()
	if scope == ScopeWorkspace {
		base = s.layout.WorkspaceSkillsDir(workspaceID)
	}
	return filepath.Join(base, id)
}

func (s *Service) relativeSkillDir(scope ResourceScope, workspaceID, id string) string {
	rel, err := filepath.Rel(s.layout.Root(), s.managedSkillDir(scope, workspaceID, id))
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// saveSkillEntry 写入一条记录（新增或替换）。
func (s *Service) saveSkillEntry(ctx context.Context, entry skillEntry, expectedRevision uint64) (contracts.ConfigDocument, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	entries, err := decodeSkillEntries(doc.Body)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	replaced := false
	for i := range entries {
		if entries[i].ID == entry.ID {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	return s.writeSkillEntries(ctx, doc.Body, entries, expectedRevision)
}

// dropSkillEntry 在写文件失败时撤回刚写入的记录（尽力而为）。
func (s *Service) dropSkillEntry(ctx context.Context, id string) error {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return err
	}
	entries, err := decodeSkillEntries(doc.Body)
	if err != nil {
		return err
	}
	kept := make([]skillEntry, 0, len(entries))
	for _, e := range entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	_, err = s.writeSkillEntries(ctx, doc.Body, kept, doc.Revision)
	return err
}

func (s *Service) writeSkillEntries(ctx context.Context, body json.RawMessage, entries []skillEntry, expectedRevision uint64) (contracts.ConfigDocument, error) {
	items := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeInternal, "序列化技能记录失败: "+err.Error(), false)
		}
		items = append(items, raw)
	}
	next, err := withField(body, "skills", items)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	return s.store.CompareAndSwap(ctx, config.KeyUser, expectedRevision, next)
}

func decodeSkillEntries(body json.RawMessage) ([]skillEntry, error) {
	raws, err := rawField(body, "skills")
	if err != nil {
		return nil, err
	}
	out := make([]skillEntry, 0, len(raws))
	for _, raw := range raws {
		var e skillEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "技能记录解析失败: "+err.Error(), false)
		}
		if strings.TrimSpace(e.ID) == "" {
			return nil, contracts.NewAppError(contracts.CodeInternal, "技能记录缺少 id", false)
		}
		if e.Scope == "" {
			e.Scope = ScopeUser
		}
		if e.Origin == "" {
			e.Origin = OriginImported
		}
		out = append(out, e)
	}
	return out, nil
}

func (r ResourceScope) validate(workspaceID string) error {
	switch r {
	case ScopeUser:
		return nil
	case ScopeWorkspace:
		if strings.TrimSpace(workspaceID) == "" {
			return contracts.NewAppError(contracts.CodeUnsupported,
				"工作区作用域的技能必须指定工作区", false)
		}
		return nil
	default:
		return contracts.NewAppError(contracts.CodeUnsupported,
			"未知的作用域: "+string(r)+"（可选 user / workspace）", false)
	}
}

// skillDoc 是解析后的一份 SKILL.md。
type skillDoc struct {
	Manifest skillManifest
	// Source 是归一化换行后的全文，写盘用。
	Source string
	// Hash 是内容的 sha256 前 16 位。
	Hash  string
	Bytes int64
}

// skillManifest 是 frontmatter 里我们关心的字段。
type skillManifest struct {
	Name        string
	Description string
	// Requires 是技能声明的外部命令依赖（V1 只认命令名，检查 PATH）。
	Requires []string
	// DisableModelInvocation 为 true 时 Pi 不会把它列进系统提示。
	DisableModelInvocation bool
}

// ReadSkillContent 读取 SKILL.md 的归一化全文（编辑器用）。
//
// 返回归一化后的内容而不是原始字节：编辑器保存时会把内容原样写回，
// 若这里返回 CRLF 原文、保存时又归一化，用户的文件会在一次"打开-保存"
// 之后整体换行符被改（git diff 一片红），而我们并没有改任何实质内容。
func ReadSkillContent(p string) (string, error) {
	doc, err := readSkillFile(p)
	if err != nil {
		return "", err
	}
	return doc.Source, nil
}

// readSkillFile 读取并校验一个 SKILL.md。
func readSkillFile(p string) (skillDoc, error) {
	info, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return skillDoc{}, contracts.NewAppError(contracts.CodeNotFound,
				"技能文件缺失: "+p, false)
		}
		return skillDoc{}, contracts.NewAppError(contracts.CodeInternal, "读取技能文件失败: "+err.Error(), false)
	}
	if info.IsDir() {
		return skillDoc{}, contracts.NewAppError(contracts.CodeUnsupported, "技能路径是目录而不是文件: "+p, false)
	}
	if info.Size() > maxSkillFileBytes {
		return skillDoc{}, contracts.NewAppError(contracts.CodeUnsupported,
			"SKILL.md 超过 "+humanBytes(maxSkillFileBytes)+"，拒绝加载", false)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return skillDoc{}, contracts.NewAppError(contracts.CodeInternal, "读取技能文件失败: "+err.Error(), false)
	}
	doc, err := parseSkillContent(string(raw))
	if err != nil {
		return skillDoc{}, err
	}
	doc.Bytes = info.Size()
	return doc, nil
}

// parseSkillContent 归一化并校验 SKILL.md 内容。
//
// 归一化与 Pi 的 extractFrontmatter 保持一致（去 BOM、CRLF→LF）：
// 我们算出来的 name/description 必须与 Pi 读到的是同一份，否则
// "界面显示已启用"与"Pi 实际加载了什么"会悄悄分叉。
func parseSkillContent(content string) (skillDoc, error) {
	if !utf8.ValidString(content) {
		return skillDoc{}, contracts.NewAppError(contracts.CodeUnsupported,
			"SKILL.md 不是合法的 UTF-8；损坏的文件不导入", false)
	}
	src := normalizeSkillNewlines(strings.TrimPrefix(content, "\ufeff"))
	manifest, err := parseSkillManifest(src)
	if err != nil {
		return skillDoc{}, err
	}
	sum := sha256.Sum256([]byte(src))
	return skillDoc{
		Manifest: manifest,
		Source:   src,
		Hash:     hex.EncodeToString(sum[:])[:16],
		Bytes:    int64(len(src)),
	}, nil
}

func normalizeSkillContent(content string) (skillDoc, error) {
	return parseSkillContent(content)
}

func normalizeSkillNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// parseSkillManifest 解析 frontmatter 并做校验。
//
// 校验比 Pi 更严是刻意的：Pi 在 description 缺失时只是**不加载**这个技能
// （没有任何报错），用户会看到"技能在列表里但模型从没用过"。我们在导入时
// 就拒绝，并说清缺什么。
func parseSkillManifest(src string) (skillManifest, error) {
	yamlText, ok := frontmatterText(src)
	if !ok {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"SKILL.md 缺少 frontmatter（文件必须以 --- 开头，并用单独一行 --- 结束）", false)
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &raw); err != nil {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"frontmatter 不是合法 YAML: "+err.Error(), false)
	}
	m := skillManifest{}
	if v, ok := raw["name"].(string); ok {
		m.Name = strings.TrimSpace(v)
	}
	if v, ok := raw["description"].(string); ok {
		m.Description = strings.TrimSpace(v)
	}
	switch v := raw["requires"].(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			m.Requires = []string{s}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				m.Requires = append(m.Requires, strings.TrimSpace(s))
			}
		}
	}
	if v, ok := raw["disable-model-invocation"].(bool); ok {
		m.DisableModelInvocation = v
	}

	if m.Name == "" {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"技能缺少 name：它是稳定引用与目录名，不能省略", false)
	}
	if len(m.Name) > 64 {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"技能 name 超过 64 个字符（Pi 的硬限制）", false)
	}
	if !skillNamePattern.MatchString(m.Name) {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"技能 name 非法: "+m.Name+"（只允许小写字母、数字、连字符，且不能以连字符开头或结尾）", false)
	}
	if strings.Contains(m.Name, "--") {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"技能 name 不能包含连续连字符: "+m.Name, false)
	}
	if m.Description == "" {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"技能缺少 description：Pi 靠它判断何时调用，没有它技能不会被加载", false)
	}
	if len(m.Description) > 1024 {
		return skillManifest{}, contracts.NewAppError(contracts.CodeUnsupported,
			"技能 description 超过 1024 个字符（Pi 的硬限制）", false)
	}
	return m, nil
}

// frontmatterText 按 Pi 的规则取出 frontmatter 正文。
func frontmatterText(src string) (string, bool) {
	if !strings.HasPrefix(src, "---") {
		return "", false
	}
	idx := strings.Index(src[3:], "\n---")
	if idx < 0 {
		return "", false
	}
	end := 3 + idx
	yamlText := src[4:end]
	if strings.TrimSpace(yamlText) == "" {
		return "", false
	}
	return yamlText, true
}

// resolveSkillSource 把用户给的路径解析成一个"含 SKILL.md 的目录"。
func resolveSkillSource(sourcePath string) (string, error) {
	raw := strings.TrimSpace(sourcePath)
	if raw == "" {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "导入路径为空", false)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "导入路径无法绝对化: "+err.Error(), false)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "导入路径不可访问: "+abs+"（"+err.Error()+"）", false)
	}
	if !info.IsDir() {
		if !strings.EqualFold(filepath.Base(abs), "SKILL.md") {
			return "", contracts.NewAppError(contracts.CodeUnsupported,
				"只接受技能目录或 SKILL.md 文件，收到: "+abs, false)
		}
		return filepath.Dir(abs), nil
	}
	if _, err := os.Stat(filepath.Join(abs, "SKILL.md")); err != nil {
		return "", contracts.NewAppError(contracts.CodeUnsupported,
			"目录里没有 SKILL.md: "+abs, false)
	}
	return abs, nil
}

// stagedTree 是一个已经复制完成、但还没就位的技能目录。
type stagedTree struct {
	path string
}

func discardStaged(st stagedTree) {
	if st.path != "" {
		_ = os.RemoveAll(st.path)
	}
}

// stageTree 把源目录完整复制到 dst 旁边的临时目录。
//
// 先复制到**目标同盘的父目录**再 rename：中途失败不会留下半个技能，
// 而"半个技能"比没有技能更危险——它会被 Pi 当成完整技能加载。
//
// 暂存目录必须与目标同盘。这里曾经用 os.MkdirTemp("")（系统临时目录），
// 单元测试里临时根也在 C 盘所以一路绿灯；真机上 app home 在 D 盘，
// rename 直接报 "cannot move the file to a different disk drive"。
func stageTree(src, dst string) (stagedTree, error) {
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return stagedTree{}, contracts.NewAppError(contracts.CodeInternal, "创建技能目录失败: "+err.Error(), false)
	}
	tmp, err := os.MkdirTemp(parent, ".staging-"+filepath.Base(dst)+"-")
	if err != nil {
		return stagedTree{}, contracts.NewAppError(contracts.CodeInternal, "创建临时目录失败: "+err.Error(), false)
	}
	// 同盘是 rename 的前提。跨盘时 Windows 会返回 "cannot move the file to a
	// different disk drive"，那句话与"技能导入失败"之间没有任何线索；
	// 在这里显式挡一下，把不变式说出来。
	if !sameVolume(tmp, dst) {
		_ = os.RemoveAll(tmp)
		return stagedTree{}, contracts.NewAppError(contracts.CodeInternal,
			"暂存目录与目标不在同一磁盘（"+filepath.VolumeName(tmp)+" / "+filepath.VolumeName(dst)+"），无法原子安装", false)
	}
	st := stagedTree{path: tmp}

	var files int
	var total int64
	walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return contracts.NewAppError(contracts.CodeUnsupported, "读取源技能失败: "+err.Error(), false)
		}
		rel, relErr := filepath.Rel(src, p)
		if relErr != nil {
			return contracts.NewAppError(contracts.CodeInternal, "计算相对路径失败: "+relErr.Error(), false)
		}
		if rel == "." {
			return nil
		}
		// .git / node_modules 是仓库与依赖元数据，不是技能内容；
		// 带进去只会让托管目录变成另一个仓库。
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return fs.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// 符号链接可以指向托管目录之外（甚至 C:\Windows），复制它等于把
			// 任意路径带进产品目录。V1 直接拒绝，并指出是哪一条。
			return contracts.NewAppError(contracts.CodeUnsupported,
				"技能目录里不允许符号链接: "+rel+"（无法保证它指向技能目录之内）", false)
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return contracts.NewAppError(contracts.CodeUnsupported, "技能目录里有非普通文件: "+rel, false)
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return contracts.NewAppError(contracts.CodeInternal, "读取文件信息失败: "+infoErr.Error(), false)
		}
		files++
		total += info.Size()
		if files > maxSkillFiles {
			return contracts.NewAppError(contracts.CodeUnsupported,
				"技能文件数超过 "+strconv.Itoa(maxSkillFiles)+" 个，疑似误选了整个仓库", false)
		}
		if total > maxSkillTreeBytes {
			return contracts.NewAppError(contracts.CodeUnsupported,
				"技能内容超过 "+humanBytes(maxSkillTreeBytes)+"，拒绝导入", false)
		}
		return copyFile(p, target, info.Mode())
	})
	if walkErr != nil {
		discardStaged(st)
		return stagedTree{}, walkErr
	}
	// 复制完再校验一次 SKILL.md：防止"源目录在复制过程中被改"。
	if _, err := readSkillFile(filepath.Join(tmp, "SKILL.md")); err != nil {
		discardStaged(st)
		return stagedTree{}, err
	}
	return st, nil
}

// sameVolume 判断两个路径是否在同一卷上。
//
// Windows 上"同卷"是 rename 能成功的前提；POSIX 上 VolumeName 为空，
// 跨挂载点 rename 也会失败，但这里没有可靠且便宜的判定手段，
// 因此只在有卷名的平台上做判断（不假装知道得更多）。
func sameVolume(a, b string) bool {
	va, vb := filepath.VolumeName(a), filepath.VolumeName(b)
	if va == "" || vb == "" {
		return true
	}
	return strings.EqualFold(va, vb)
}

// finishInstall 把暂存目录搬到目标位置（目标必须不存在）。
func finishInstall(st stagedTree, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建技能目录失败: "+err.Error(), false)
	}
	if _, err := os.Stat(dst); err == nil {
		return contracts.NewAppError(contracts.CodeConfigConflict, "目标目录已存在: "+dst, false)
	}
	if err := os.Rename(st.path, dst); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "安装技能目录失败: "+err.Error(), false)
	}
	return nil
}

// swapIn 用暂存目录替换已有目录（覆盖导入）。
func (s *Service) swapIn(ctx context.Context, st stagedTree, dst, id string) (SkillResource, error) {
	old := dst + ".replaced"
	_ = os.RemoveAll(old)
	movedOld := false
	if err := os.Rename(dst, old); err == nil {
		movedOld = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return SkillResource{}, contracts.NewAppError(contracts.CodeInternal, "无法替换技能目录: "+err.Error(), false)
	}
	if _, err := os.Stat(dst); err == nil {
		if movedOld {
			_ = os.Rename(old, dst)
		}
		return SkillResource{}, contracts.NewAppError(contracts.CodeConfigConflict, "目标目录仍存在: "+dst, false)
	}
	if err := os.Rename(st.path, dst); err != nil {
		if movedOld {
			_ = os.Rename(old, dst)
		}
		return SkillResource{}, contracts.NewAppError(contracts.CodeInternal, "覆盖技能失败: "+err.Error(), false)
	}
	if movedOld {
		_ = os.RemoveAll(old)
	}
	return s.GetSkill(ctx, id)
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "打开源文件失败: "+filepath.Base(src)+": "+err.Error(), false)
	}
	defer in.Close()
	// 保留可执行位，但去掉组/其他可写：托管目录里的文件不应被别的用户改。
	perm := fs.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入文件失败: "+filepath.Base(dst)+": "+err.Error(), false)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return contracts.NewAppError(contracts.CodeInternal, "复制文件失败: "+filepath.Base(src)+": "+err.Error(), false)
	}
	return out.Close()
}

// replaceFile 原子替换一个文件（写同目录临时文件再 rename）。
func replaceFile(path string, content []byte) error {
	tmp := path + ".tmp-edit"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入临时文件失败: "+err.Error(), false)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return contracts.NewAppError(contracts.CodeInternal, "替换文件失败: "+err.Error(), false)
	}
	return nil
}

// missingCommands 用 LookPath 判断命令是否存在——只查，不执行。
func missingCommands(cmds []string, look func(string) (string, error)) []string {
	var missing []string
	for _, c := range cmds {
		if strings.TrimSpace(c) == "" {
			continue
		}
		if _, err := look(c); err != nil {
			missing = append(missing, c)
		}
	}
	return missing
}

// messageOf 取出 AppError 里的人话部分，用于展示给用户的"原因"。
func messageOf(err error) string {
	var appErr *contracts.AppError
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return err.Error()
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.Itoa(n>>20) + " MiB"
	case n >= 1<<10:
		return strconv.Itoa(n>>10) + " KiB"
	default:
		return strconv.Itoa(n) + " B"
	}
}
