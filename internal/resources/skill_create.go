package resources

// 本地新建技能（RES01 的"新建"入口）。
//
// 与链接导入/本地导入共用同一套落盘纪律：
//   - 目录先在目标旁边暂存，写完再 rename 就位；
//   - 配置写入用 CAS，失败就把刚建好的目录删掉，不留"文件在、列表没有"的孤儿；
//   - 名称是稳定引用（ID 来自 name），非法名称直接拒绝，而不是自动改写。

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"my-agent-go/internal/contracts"
)

// OriginCreated 表示这条资源是在产品里本地新建的。
const OriginCreated = "created"

// LocalSkillDescriptionPlaceholder 是新建技能的占位描述。
//
// Pi 靠 description 判断何时调用技能，缺了它技能不会被加载；
// 占位值让"刚建好"与"可用"不是互相矛盾的状态，用户可以马上在编辑器里改写。
const LocalSkillDescriptionPlaceholder = "本地新建技能：请补充 description，说明模型应该在什么时候使用它。"

// CreateSkill 在托管目录里新建一条技能。
//
// scope/workspaceID 的合法性与导入路径一致（见 ResourceScope.validate）。
func (s *Service) CreateSkill(ctx context.Context, name string, scope ResourceScope, workspaceID string) (SkillResource, error) {
	if err := scope.validate(workspaceID); err != nil {
		return SkillResource{}, err
	}
	id := strings.TrimSpace(name)
	if err := validateSkillName(id); err != nil {
		return SkillResource{}, err
	}

	set, err := s.ListSkills(ctx)
	if err != nil {
		return SkillResource{}, err
	}
	for _, it := range set.Items {
		if it.ID == id {
			return SkillResource{}, contracts.NewAppError(contracts.CodeConfigConflict,
				"已存在同名技能 "+id+"；要改内容请直接编辑，要替换文件请用「覆盖导入」", false)
		}
	}

	dstDir := s.managedSkillDir(scope, workspaceID, id)
	if _, statErr := os.Stat(dstDir); statErr == nil {
		return SkillResource{}, contracts.NewAppError(contracts.CodeConfigConflict,
			"目标目录已存在但不在配置里: "+dstDir+"；请先清理该目录或换一个名称", false)
	}
	parent := filepath.Dir(dstDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return SkillResource{}, contracts.NewAppError(contracts.CodeInternal, "创建技能目录失败: "+err.Error(), false)
	}
	// 暂存目录与目标同盘，保证 rename 能成功（跨盘 rename 在 Windows 上直接失败）。
	tmp, err := os.MkdirTemp(parent, ".staging-"+id+"-")
	if err != nil {
		return SkillResource{}, contracts.NewAppError(contracts.CodeInternal, "创建临时目录失败: "+err.Error(), false)
	}
	staged := stagedTree{path: tmp}
	defer discardStaged(staged)

	content := localSkillTemplate(id)
	tmpFile := filepath.Join(tmp, "SKILL.md")
	if err := os.WriteFile(tmpFile, []byte(content), 0o600); err != nil {
		return SkillResource{}, contracts.NewAppError(contracts.CodeInternal, "写 SKILL.md 失败: "+err.Error(), false)
	}
	// 模板必须先过一遍与运行期相同的校验：新建出来的技能如果 Pi 不认，
	// 用户看到的会是"建好了但不可用"，而原因只是模板不合法。
	if _, err := readSkillFile(tmpFile); err != nil {
		return SkillResource{}, err
	}

	entry := skillEntry{
		ID:          id,
		Scope:       scope,
		WorkspaceID: workspaceID,
		Dir:         s.relativeSkillDir(scope, workspaceID, id),
		Enabled:     true,
		Origin:      OriginCreated,
	}
	if _, err := s.saveSkillEntry(ctx, entry, set.Revision); err != nil {
		return SkillResource{}, err
	}
	if err := finishInstall(staged, dstDir); err != nil {
		_ = s.dropSkillEntry(ctx, id)
		return SkillResource{}, err
	}
	return s.GetSkill(ctx, id)
}

// validateSkillName 是新建入口的名称校验，规则与 parseSkillManifest 一致。
func validateSkillName(name string) error {
	if name == "" {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"技能名不能为空：它是稳定引用与托管目录名", false)
	}
	if len(name) > 64 {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"技能名超过 64 个字符（Pi 的硬限制）", false)
	}
	if !skillNamePattern.MatchString(name) {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"技能名非法: "+name+"（只允许小写字母、数字、连字符，且不能以连字符开头或结尾）", false)
	}
	if strings.Contains(name, "--") {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"技能名不能包含连续连字符: "+name, false)
	}
	return nil
}

// localSkillTemplate 是新技能的 SKILL.md 模板。
func localSkillTemplate(name string) string {
	return "---\nname: " + name + "\ndescription: " + LocalSkillDescriptionPlaceholder + "\n---\n\n" +
		"# " + name + "\n\n在这里写清步骤、边界与输出格式。\n"
}
