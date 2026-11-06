package resources

// C03 的资源层测试：RES01–RES03 里"能单测的部分"。
//
// 真机/真实 Pi 的部分不在这里：
//   - "禁用后新 run 不加载、同名自动发现副本不能绕过"要真起 Pi 才看得见，
//     在 skills_loading_integration_test.go；
//   - "导入不执行脚本"在这里用"写个会留痕的脚本"来验证，比读代码更硬。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/runtime/pi"
)

// newSkillService 构造带托管目录的资源服务。
func newSkillService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	svc := New(store, credentials.NewMemStore(), WithLayout(NewLayout(root)))
	return svc, root
}

// writeSourceSkill 在临时目录里造一个源技能，返回目录路径。
func writeSourceSkill(t *testing.T, name, body string, extra map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src-"+name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建源目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("写 SKILL.md: %v", err)
	}
	for rel, content := range extra {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建子目录: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写 %s: %v", rel, err)
		}
	}
	return dir
}

func skillMD(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n正文：按步骤执行。\n"
}

// TestImportRejectsInvalidSkills 覆盖 RES03 的"损坏 frontmatter / 恶意路径被拒"。
func TestImportRejectsInvalidSkills(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"没有 frontmatter", "# 只是正文\n", "frontmatter"},
		{"缺少 name", "---\ndescription: 只有描述\n---\n正文\n", "name"},
		{"缺少 description", "---\nname: no-desc\n---\n正文\n", "description"},
		{"name 大写", "---\nname: Bad-Name\ndescription: x\n---\n", "name"},
		{"name 连续连字符", "---\nname: a--b\ndescription: x\n---\n", "连续连字符"},
		{"description 过长", "---\nname: long-desc\ndescription: " + strings.Repeat("字", 1025) + "\n---\n", "1024"},
		{"YAML 语法错误", "---\nname: [unclosed\ndescription: x\n---\n", "YAML"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := newSkillService(t)
			dir := writeSourceSkill(t, "case", c.body, nil)
			_, err := svc.ImportSkill(context.Background(), dir, ScopeUser, "")
			if err == nil {
				t.Fatal("应当拒绝导入")
			}
			if !strings.Contains(messageOf(err), c.want) {
				t.Fatalf("拒绝原因应提到 %q，实际 %q", c.want, messageOf(err))
			}
		})
	}
}

// TestImportRejectsBrokenUTF8 守 RES03 的"损坏UTF8"。
func TestImportRejectsBrokenUTF8(t *testing.T) {
	svc, _ := newSkillService(t)
	dir := t.TempDir()
	raw := append([]byte("---\nname: broken\ndescription: 坏字节 \n---\n"), 0xff, 0xfe, 0xfd)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), raw, 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	if _, err := svc.ImportSkill(context.Background(), dir, ScopeUser, ""); err == nil {
		t.Fatal("非法 UTF-8 应被拒绝")
	}
}

// TestImportRejectsSymlinkEscape 守 RES03 的"恶意路径"。
//
// 符号链接可以指向技能目录之外（甚至 C:\Windows）。复制它等于把任意路径
// 带进产品托管目录，之后删除技能时会沿着链接去动别人的文件。
func TestImportRejectsSymlinkEscape(t *testing.T) {
	svc, root := newSkillService(t)
	outside := filepath.Join(root, "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("should-not-be-imported"), 0o600); err != nil {
		t.Fatalf("写外部文件: %v", err)
	}
	dir := writeSourceSkill(t, "link", skillMD("linked-skill", "带符号链接的技能"), nil)
	link := filepath.Join(dir, "leak.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前系统不允许创建符号链接，跳过: %v", err)
	}

	_, err := svc.ImportSkill(context.Background(), dir, ScopeUser, "")
	if err == nil {
		t.Fatal("含符号链接的技能应被拒绝")
	}
	if !strings.Contains(messageOf(err), "符号链接") {
		t.Fatalf("错误应说明是符号链接，实际 %q", messageOf(err))
	}
}

// TestImportCopiesWithoutExecutingSource 守 RES01 的"不改源目录、不执行导入脚本"。
func TestImportCopiesWithoutExecutingSource(t *testing.T) {
	svc, root := newSkillService(t)
	marker := filepath.Join(root, "executed.marker")
	// 一个"被执行就会留痕"的脚本。导入过程绝不能创建 marker。
	dir := writeSourceSkill(t, "runme", skillMD("runme-skill", "不该被执行的技能"), map[string]string{
		"install.sh": "#!/bin/sh\ntouch " + marker + "\n",
		"setup.py":   "open(" + strconvQuote(marker) + ", 'w').write('x')\n",
	})
	before := treeSnapshot(t, dir)

	if _, err := svc.ImportSkill(context.Background(), dir, ScopeUser, ""); err != nil {
		t.Fatalf("导入失败: %v", messageOf(err))
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("导入过程执行了技能里的脚本：marker 被创建")
	}
	after := treeSnapshot(t, dir)
	if before != after {
		t.Fatalf("源目录被改动了\n之前: %s\n之后: %s", before, after)
	}

	// 内容确实复制过来了，而且落在产品托管目录里。
	managed := filepath.Join(root, "skills", "runme-skill", "SKILL.md")
	if _, err := os.Stat(managed); err != nil {
		t.Fatalf("托管目录里没有 SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "runme-skill", "install.sh")); err != nil {
		t.Fatalf("附属文件没有一起复制: %v", err)
	}

	// RES01 的外部观察（filesystem）：导入后在磁盘上的真实文件树。
	facts := map[string]any{}
	mergeSkillsFact(t, facts, "importedSkills", "runme-skill")
	recordSkillsTree(t, root, "after-import", facts)
}

// TestImportRejectsDuplicateWithoutOverwrite 守 RES01 的"覆盖须明确动作"。
func TestImportRejectsDuplicateWithoutOverwrite(t *testing.T) {
	svc, _ := newSkillService(t)
	dir := writeSourceSkill(t, "dup", skillMD("dup-skill", "第一版"), nil)
	if _, err := svc.ImportSkill(context.Background(), dir, ScopeUser, ""); err != nil {
		t.Fatalf("首次导入失败: %v", messageOf(err))
	}

	dir2 := writeSourceSkill(t, "dup2", skillMD("dup-skill", "第二版"), nil)
	_, err := svc.ImportSkill(context.Background(), dir2, ScopeUser, "")
	if err == nil {
		t.Fatal("同名技能再次导入应被拒绝，而不是静默覆盖")
	}
	if !strings.Contains(messageOf(err), "已存在同名技能") {
		t.Fatalf("错误应说明同名已存在，实际 %q", messageOf(err))
	}
	got, _ := svc.GetSkill(context.Background(), "dup-skill")
	if !strings.Contains(got.Description, "第一版") {
		t.Fatalf("被覆盖了：现在描述是 %q", got.Description)
	}
}

// TestOverwriteSkillReplacesContentAndKeepsEnabled 守"覆盖是显式动作"。
func TestOverwriteSkillReplacesContentAndKeepsEnabled(t *testing.T) {
	svc, _ := newSkillService(t)
	ctx := context.Background()
	dir := writeSourceSkill(t, "ow", skillMD("ow-skill", "第一版"), nil)
	if _, err := svc.ImportSkill(ctx, dir, ScopeUser, ""); err != nil {
		t.Fatalf("导入失败: %v", messageOf(err))
	}
	set, _ := svc.ListSkills(ctx)
	if _, err := svc.SetSkillEnabled(ctx, "ow-skill", set.Revision, false); err != nil {
		t.Fatalf("禁用失败: %v", messageOf(err))
	}

	dir2 := writeSourceSkill(t, "ow2", skillMD("ow-skill", "第二版"), nil)
	set, _ = svc.ListSkills(ctx)
	got, err := svc.OverwriteSkill(ctx, "ow-skill", dir2, set.Revision)
	if err != nil {
		t.Fatalf("覆盖失败: %v", messageOf(err))
	}
	if !strings.Contains(got.Description, "第二版") {
		t.Fatalf("描述应更新为第二版，实际 %q", got.Description)
	}
	if got.Enabled {
		t.Fatal("覆盖内容不应把用户禁用的技能重新打开")
	}

	// revision 不对时必须拒绝，避免拿旧版本信息做破坏性覆盖。
	if _, err := svc.OverwriteSkill(ctx, "ow-skill", dir2, set.Revision+99); err == nil {
		t.Fatal("revision 不匹配时应拒绝覆盖")
	}
}

// TestCompileSkillsRespectsEnabledAndScope 守 RES02 的可测部分：
// 只有"启用 + 可用 + 作用域匹配"的技能会出现在运行集合里。
func TestCompileSkillsRespectsEnabledAndScope(t *testing.T) {
	svc, _ := newSkillService(t)
	ctx := context.Background()

	for _, s := range []struct {
		name string
		body string
		ws   ResourceScope
		wid  string
	}{
		{"alpha-skill", skillMD("alpha-skill", "用户级启用"), ScopeUser, ""},
		{"beta-skill", skillMD("beta-skill", "用户级将被禁用"), ScopeUser, ""},
		{"gamma-skill", skillMD("gamma-skill", "别的工作区"), ScopeWorkspace, "ws-other"},
	} {
		dir := writeSourceSkill(t, s.name, s.body, nil)
		if _, err := svc.ImportSkill(ctx, dir, s.ws, s.wid); err != nil {
			t.Fatalf("导入 %s: %v", s.name, messageOf(err))
		}
	}
	set, _ := svc.ListSkills(ctx)
	if _, err := svc.SetSkillEnabled(ctx, "beta-skill", set.Revision, false); err != nil {
		t.Fatalf("禁用失败: %v", messageOf(err))
	}

	plan, err := svc.CompileSkills(ctx, "ws-default")
	if err != nil {
		t.Fatalf("编译失败: %v", messageOf(err))
	}
	if len(plan.IDs) != 1 || plan.IDs[0] != "alpha-skill" {
		t.Fatalf("只应包含 alpha-skill，实际 %v", plan.IDs)
	}
	if !strings.HasSuffix(plan.Paths[0], filepath.Join("alpha-skill", "SKILL.md")) {
		t.Fatalf("路径应是托管目录里的 SKILL.md，实际 %s", plan.Paths[0])
	}

	// 换到 gamma 所属的工作区，它就应当出现。
	planOther, err := svc.CompileSkills(ctx, "ws-other")
	if err != nil {
		t.Fatalf("编译失败: %v", messageOf(err))
	}
	if len(planOther.IDs) != 2 {
		t.Fatalf("ws-other 应有 alpha + gamma，实际 %v", planOther.IDs)
	}
}

// TestMissingDependencyMarksUnavailable 守 RES03 的"依赖缺失显示原因、不静默安装"。
func TestMissingDependencyMarksUnavailable(t *testing.T) {
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	// 注入一个"什么命令都找不到"的 LookPath，模拟缺依赖的机器。
	svc := New(store, credentials.NewMemStore(),
		WithLayout(NewLayout(root)),
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }))

	body := "---\nname: dep-skill\ndescription: 需要外部命令\nrequires:\n  - python3\n  - rg\n---\n正文\n"
	dir := writeSourceSkill(t, "dep", body, nil)
	if _, err := svc.ImportSkill(context.Background(), dir, ScopeUser, ""); err != nil {
		t.Fatalf("导入失败: %v", messageOf(err))
	}

	got, err := svc.GetSkill(context.Background(), "dep-skill")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Available {
		t.Fatal("缺依赖时不应标记为可用")
	}
	if len(got.MissingDependencies) != 2 {
		t.Fatalf("应报告两个缺失依赖，实际 %v", got.MissingDependencies)
	}
	if !strings.Contains(got.UnavailableReason, "python3") {
		t.Fatalf("原因里应点名缺的依赖，实际 %q", got.UnavailableReason)
	}

	plan, err := svc.CompileSkills(context.Background(), "ws-default")
	if err != nil {
		t.Fatalf("编译失败: %v", messageOf(err))
	}
	if len(plan.Paths) != 0 {
		t.Fatalf("依赖缺失的技能不应进运行集合，实际 %v", plan.Paths)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].ID != "dep-skill" {
		t.Fatalf("应如实报告被跳过的技能，实际 %v", plan.Skipped)
	}

	// RES03 的外部观察（filesystem）：技能还在磁盘上，但编译出的启动参数里没有它。
	// argv 由 fakeworker 子进程自己写下（-record-argv），父进程只读文件。
	isoArgs := pi.IsolationSpec{Skills: plan.Paths}.Args()
	rec := runRecordingChild(t, isoArgs)
	if resourcesObservationDir() == "" {
		return
	}
	writeResourcesArtifact(t, "compiled-args.json", map[string]any{
		"capturedBy":    "fakeworker -record-argv（子进程自写）",
		"isolationArgs": isoArgs,
		"argv":          rec.Argv,
		"env":           rec.Env,
	})
	compiled := readResourcesArtifact(t, "compiled-args.json")
	argvAny, _ := compiled["argv"].([]any)
	argv := make([]string, 0, len(argvAny))
	for _, a := range argvAny {
		if v, ok := a.(string); ok {
			argv = append(argv, v)
		}
	}
	skillFlags := 0
	isolationFlags := make([]string, 0, 8)
	for _, a := range argv {
		if a == "--skill" {
			skillFlags++
		}
		if strings.HasPrefix(a, "-n") || strings.HasPrefix(a, "--no-") {
			isolationFlags = append(isolationFlags, a)
		}
	}
	recordSkillsTree(t, root, "after-unavailable-import", map[string]any{
		"argv":               argv,
		"argvLen":            len(argv),
		"compiledSkillFlags": skillFlags,
		"isolationFlags":     isolationFlags,
		"childEnvCount":      len(rec.Env),
		"childExe":           argv[0],
		"skippedSkill":       plan.Skipped[0].ID,
	})
}

// TestDeleteSkillRemovesDirAndAllowsReimport 守"删除真实落盘"。
func TestDeleteSkillRemovesDirAndAllowsReimport(t *testing.T) {
	svc, root := newSkillService(t)
	ctx := context.Background()
	dir := writeSourceSkill(t, "del", skillMD("del-skill", "待删除"), map[string]string{"notes.md": "x"})
	if _, err := svc.ImportSkill(ctx, dir, ScopeUser, ""); err != nil {
		t.Fatalf("导入失败: %v", messageOf(err))
	}
	set, _ := svc.ListSkills(ctx)
	if err := svc.DeleteSkill(ctx, set.Revision, "del-skill"); err != nil {
		t.Fatalf("删除失败: %v", messageOf(err))
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "del-skill")); !os.IsNotExist(err) {
		t.Fatalf("目录应被删掉，stat err=%v", err)
	}
	if _, err := svc.GetSkill(ctx, "del-skill"); err == nil {
		t.Fatal("删除后不应还能读到")
	}

	// RES01 的外部观察：删除后磁盘上真的没了（快照留档），再导入又回来。
	delFacts := map[string]any{}
	mergeSkillsFact(t, delFacts, "importedSkills", "del-skill")
	mergeSkillsFact(t, delFacts, "deletedSkills", "del-skill")
	recordSkillsTree(t, root, "after-delete", delFacts)

	// 残留目录会让再次导入失败，这里顺带守住"删干净"。
	if _, err := svc.ImportSkill(ctx, dir, ScopeUser, ""); err != nil {
		t.Fatalf("删除后应能重新导入: %v", messageOf(err))
	}
	recordSkillsTree(t, root, "after-reimport", map[string]any{})
}

// TestSkillCASConflictIsRejected 守 CFG02 在技能上的延伸：拿旧 revision 写必须失败。
func TestSkillCASConflictIsRejected(t *testing.T) {
	svc, root := newSkillService(t)
	ctx := context.Background()
	dir := writeSourceSkill(t, "cas", skillMD("cas-skill", "d"), nil)
	if _, err := svc.ImportSkill(ctx, dir, ScopeUser, ""); err != nil {
		t.Fatalf("导入失败: %v", messageOf(err))
	}
	set, _ := svc.ListSkills(ctx)
	if _, err := svc.SetSkillEnabled(ctx, "cas-skill", set.Revision, false); err != nil {
		t.Fatalf("第一次禁用失败: %v", messageOf(err))
	}
	// 用已经过期的 revision 再写一次。
	if _, err := svc.SetSkillEnabled(ctx, "cas-skill", set.Revision, true); err == nil {
		t.Fatal("过期 revision 的写入应被拒绝")
	}

	// RES01 的外部观察：CAS 拒绝之后磁盘上的文件没有被改写。
	casFacts := map[string]any{}
	mergeSkillsFact(t, casFacts, "importedSkills", "cas-skill")
	mergeSkillsFact(t, casFacts, "casRejected", "cas-skill")
	recordSkillsTree(t, root, "after-cas-reject", casFacts)
}

// TestSaveSkillContentGuardsIdentity 守"编辑不能悄悄改名"。
func TestSaveSkillContentGuardsIdentity(t *testing.T) {
	svc, _ := newSkillService(t)
	ctx := context.Background()
	dir := writeSourceSkill(t, "edit", skillMD("edit-skill", "原文"), nil)
	if _, err := svc.ImportSkill(ctx, dir, ScopeUser, ""); err != nil {
		t.Fatalf("导入失败: %v", messageOf(err))
	}
	set, _ := svc.ListSkills(ctx)

	renamed := "---\nname: other-skill\ndescription: 改名\ndescription2: x\n---\n正文\n"
	if _, err := svc.SaveSkillContent(ctx, "edit-skill", set.Revision, renamed); err == nil {
		t.Fatal("把 name 改成别的值应被拒绝（ID 来自 name）")
	}

	ok := "---\nname: edit-skill\ndescription: 新描述\n---\n\n新的正文\n"
	got, err := svc.SaveSkillContent(ctx, "edit-skill", set.Revision, ok)
	if err != nil {
		t.Fatalf("保存失败: %v", messageOf(err))
	}
	if got.Description != "新描述" {
		t.Fatalf("描述应更新，实际 %q", got.Description)
	}
	raw, _ := os.ReadFile(got.Path)
	if !strings.Contains(string(raw), "新的正文") {
		t.Fatalf("文件内容没更新: %s", raw)
	}
}

// TestSkillImportPreservesOtherConfig 守"新增一类资源不能把别的抹掉"。
func TestSkillImportPreservesOtherConfig(t *testing.T) {
	svc, _ := newSkillService(t)
	ctx := context.Background()
	if _, err := svc.Save(ctx, 0, validModel(), ""); err != nil {
		t.Fatalf("保存模型: %v", messageOf(err))
	}
	dir := writeSourceSkill(t, "keep", skillMD("keep-skill", "d"), nil)
	if _, err := svc.ImportSkill(ctx, dir, ScopeUser, ""); err != nil {
		t.Fatalf("导入失败: %v", messageOf(err))
	}
	doc, err := svc.store.Load(ctx, config.KeyUser)
	if err != nil {
		t.Fatalf("读配置: %v", err)
	}
	var body struct {
		Models []json.RawMessage `json:"models"`
		Skills []json.RawMessage `json:"skills"`
	}
	if err := json.Unmarshal(doc.Body, &body); err != nil {
		t.Fatalf("解析配置: %v", err)
	}
	if len(body.Models) != 1 || len(body.Skills) != 1 {
		t.Fatalf("保存技能后模型定义被抹了：models=%d skills=%d", len(body.Models), len(body.Skills))
	}
}

// treeSnapshot 把目录的内容与大小拍平成可比较的字符串。
func treeSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b.WriteString(rel)
		b.WriteString("|")
		b.WriteString(info.Mode().String())
		b.WriteString("|")
		if !info.IsDir() {
			raw, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			b.WriteString(string(raw))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("拍快照失败: %v", err)
	}
	return b.String()
}

func strconvQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// TestSameVolumeGuard 守跨盘 rename 这个真机缺陷的不变式。
//
// 单测的临时根与 app home 都在 C 盘，所以"把暂存目录放到系统临时目录"
// 这个错误在单测里永远是绿的；真机 home 在 D 盘时才会暴露成
// "cannot move the file to a different disk drive"。这里把判断本身钉住。
func TestSameVolumeGuard(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("卷名判定只在 Windows 上有意义")
	}
	if sameVolume(`C:/a/b`, `C:/c/d`) != true {
		t.Fatal("同卷路径应判为同卷")
	}
	if sameVolume(`C:/a/b`, `D:/e/f`) != false {
		t.Fatal("不同卷路径必须判为不同卷")
	}
	if sameVolume(`c:/a`, `C:/b`) != true {
		t.Fatal("卷名比较应忽略大小写")
	}
}
