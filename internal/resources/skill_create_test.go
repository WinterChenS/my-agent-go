package resources

// 本地新建技能的测试：名称是稳定引用，落盘位置必须与导入路径一致。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

// TestCreateSkillWritesManagedSkillAndReadsBack 覆盖"新建 → 列举 → 内容"。
func TestCreateSkillWritesManagedSkillAndReadsBack(t *testing.T) {
	svc, root := newSkillService(t)
	item, err := svc.CreateSkill(context.Background(), "my-local-skill", ScopeUser, "")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if item.ID != "my-local-skill" || !item.Enabled || item.Origin != OriginCreated {
		t.Fatalf("新建结果不对: %+v", item)
	}
	wantPath := filepath.Join(root, "skills", "my-local-skill", "SKILL.md")
	if item.Path != wantPath {
		t.Fatalf("托管路径 = %s，期望 %s", item.Path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("文件没落盘: %v", err)
	}
	// 新建的技能必须能立刻通过 Pi 的校验（name + description 齐全），
	// 否则用户建完就看到"不可用"，而原因只是模板不合法。
	if !item.Available {
		t.Fatalf("新建的技能应当可用，实际不可用: %s", item.UnavailableReason)
	}
	if item.Description == "" {
		t.Fatal("新建的技能必须带占位 description")
	}
	// 内容可编辑：占位描述要能被替换。
	content, err := ReadSkillContent(wantPath)
	if err != nil {
		t.Fatalf("读取内容失败: %v", err)
	}
	if !strings.Contains(content, "name: my-local-skill") {
		t.Fatalf("模板内容不对: %q", content)
	}
}

// TestCreateSkillRejectsInvalidOrDuplicateNames：名称是稳定引用，不能随便取。
func TestCreateSkillRejectsInvalidOrDuplicateNames(t *testing.T) {
	svc, _ := newSkillService(t)
	if _, err := svc.CreateSkill(context.Background(), "Bad Name", ScopeUser, ""); err == nil {
		t.Fatal("非法名称必须被拒绝")
	} else {
		requireCode(t, err, contracts.CodeUnsupported)
	}
	if _, err := svc.CreateSkill(context.Background(), "ok-name", ScopeUser, ""); err != nil {
		t.Fatalf("合法名称被拒: %v", err)
	}
	if _, err := svc.CreateSkill(context.Background(), "ok-name", ScopeUser, ""); err == nil {
		t.Fatal("重名必须被拒绝")
	} else {
		requireCode(t, err, contracts.CodeConfigConflict)
	}
	if _, err := svc.CreateSkill(context.Background(), "ok-name", ScopeWorkspace, ""); err == nil {
		t.Fatal("workspace 作用域缺工作区必须被拒绝")
	}
}

// TestCreateSkillRejectsPreExistingDirectoryWithoutWriting：
// 目标目录已被占用时拒绝，且不往里面写任何东西（不留半个技能）。
func TestCreateSkillRejectsPreExistingDirectoryWithoutWriting(t *testing.T) {
	svc, root := newSkillService(t)
	target := filepath.Join(root, "skills", "taken-name")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("建目标目录: %v", err)
	}
	_, err := svc.CreateSkill(context.Background(), "taken-name", ScopeUser, "")
	if err == nil {
		t.Fatal("目标目录已存在时必须拒绝")
	}
	requireCode(t, err, contracts.CodeConfigConflict)
	entries, readErr := os.ReadDir(target)
	if readErr != nil {
		t.Fatalf("读取目标目录: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("拒绝路径不应写文件: %v", entries)
	}
}

// TestCreateSkillLeavesNoStagingDirectory 守"暂存目录必须被清掉"。
func TestCreateSkillLeavesNoStagingDirectory(t *testing.T) {
	svc, root := newSkillService(t)
	if _, err := svc.CreateSkill(context.Background(), "clean-skill", ScopeUser, ""); err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatalf("读取托管目录: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatalf("留下了暂存目录: %s", e.Name())
		}
	}
}
