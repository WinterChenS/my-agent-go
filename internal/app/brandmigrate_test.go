package app

// U00：MyAgent → Anvil 数据目录迁移。
//
// 这个迁移的失败模式都很贵：删掉用户数据、把旧目录搬成半个、把明文秘密
// 复制到新位置、或者每次启动都重搬一遍。所以断言全部是"磁盘上到底是什么"：
//   - 旧目录逐字节不变（迁移是复制 + 原子改名，不是移动）；
//   - 新目录要么完整，要么不存在（失败时没有半成品）；
//   - 凭证目录不复制（秘密不进新根，也不进日志）；
//   - 迁移标记保证幂等；
//   - Pi 自己的 ~/.pi 全程零触碰。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// treeHash 对整棵目录做内容指纹（路径 + 内容），用来证明"这家目录没被动过"。
func treeHash(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s: %v", root, err)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, rel := range paths {
		data, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			t.Fatalf("读取 %s: %v", rel, rerr)
		}
		h.Write([]byte(filepath.ToSlash(rel) + "\x00"))
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeFileT(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写文件 %s: %v", path, err)
	}
}

// seedLegacyTree 造一份"旧版开发目录"：非敏感配置、数据库、凭证和日志都在。
func seedLegacyTree(t *testing.T, legacy string) {
	t.Helper()
	writeFileT(t, filepath.Join(legacy, "settings.json"), []byte(`{"workspace":"D:\\work","model":"fixture-model"}`))
	writeFileT(t, filepath.Join(legacy, "config", "config-user.json"), []byte(`{"schemaVersion":1,"revision":2,"body":{"modelRef":"m-1"}}`))
	writeFileT(t, filepath.Join(legacy, "config", "config-workspace-ws-default.json"), []byte(`{"schemaVersion":1,"revision":1,"body":{"x":1}}`))
	writeFileT(t, filepath.Join(legacy, "sessions.db"), []byte("SQLite format 3\x00sessions"))
	writeFileT(t, filepath.Join(legacy, "memory.db-wal"), []byte("wal"))
	writeFileT(t, filepath.Join(legacy, "credentials", "ref-abc.bin"), []byte("SECRET-SENTINEL-DO-NOT-COPY"))
	writeFileT(t, filepath.Join(legacy, "logs", "app.log"), []byte("SECRET-SENTINEL-IN-LOG"))
	writeFileT(t, filepath.Join(legacy, "artifacts", "run-1.bin"), []byte("artifact-body"))
}

func TestBrandMigrationIsIdempotentAndNeverDeletes(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "MyAgent")
	target := filepath.Join(base, "Anvil")
	seedLegacyTree(t, legacy)

	// Pi 自己的 ~/.pi 与本次迁移无关，但必须被证明"没碰"。
	home := t.TempDir()
	piHome := filepath.Join(home, ".pi")
	writeFileT(t, filepath.Join(piHome, "agent", "config.json"), []byte(`{"pi":true}`))
	piHash := treeHash(t, piHome)

	legacyHash := treeHash(t, legacy)

	first, err := MigrateBrandData(target, legacy, BrandMigrationOptions{})
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if first.Status != BrandMigrationDone {
		t.Fatalf("首次迁移状态应为 %q，实际 %q（%s）", BrandMigrationDone, first.Status, first.Error)
	}
	if first.Source != legacy || first.Target != target {
		t.Fatalf("迁移结果未记录来源/目标: %+v", first)
	}

	// 白名单语义：配置与数据库过去，凭证与日志不过去。
	for _, rel := range []string{"settings.json", "config/config-user.json", "config/config-workspace-ws-default.json", "sessions.db", "memory.db-wal"} {
		src, err := os.ReadFile(filepath.Join(legacy, rel))
		if err != nil {
			t.Fatalf("读旧文件 %s: %v", rel, err)
		}
		dst, err := os.ReadFile(filepath.Join(target, rel))
		if err != nil {
			t.Fatalf("迁移后缺少 %s: %v", rel, err)
		}
		if string(src) != string(dst) {
			t.Fatalf("%s 迁移后内容不一致", rel)
		}
	}
	for _, rel := range []string{"credentials", "logs", "artifacts"} {
		if _, err := os.Stat(filepath.Join(target, rel)); err == nil {
			t.Fatalf("%s 不该被复制到新目录", rel)
		}
	}

	// 秘密未复制：断言扫一遍新根，sentinel 命中数必须是 0。
	if hits := scanTreeFor(t, target, "SECRET-SENTINEL"); len(hits) != 0 {
		t.Fatalf("新目录里出现秘密痕迹: %v", hits)
	}

	// 迁移标记：既是幂等依据，也是"这次迁移复制了哪些文件"的证据。
	markerPath := filepath.Join(target, brandMigrationMarkerName)
	rawMarker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("缺少迁移标记 %s: %v", markerPath, err)
	}
	var marker BrandMigrationMarker
	if err := json.Unmarshal(rawMarker, &marker); err != nil {
		t.Fatalf("迁移标记不是合法 JSON: %v", err)
	}
	if marker.From != LegacyBrandName || marker.To != BrandName {
		t.Fatalf("标记品牌不符: %+v", marker)
	}
	if len(marker.Files) == 0 {
		t.Fatalf("标记未记录文件清单: %+v", marker)
	}
	for _, f := range marker.Files {
		if strings.Contains(f.Path, "credentials") {
			t.Fatalf("标记里出现凭证路径: %+v", f)
		}
		if f.SHA256 == "" {
			t.Fatalf("标记缺少文件摘要: %+v", f)
		}
	}

	// 旧目录逐字节不变：迁移不是移动，删旧目录是不可接受的数据丢失。
	if after := treeHash(t, legacy); after != legacyHash {
		t.Fatalf("旧目录被改动（迁移必须是复制而非移动）")
	}
	// ~/.pi 零触碰。
	if after := treeHash(t, piHome); after != piHash {
		t.Fatalf("~/.pi 被改动")
	}

	// 幂等：第二次运行什么都不写，状态是 already_migrated。
	targetHash := treeHash(t, target)
	second, err := MigrateBrandData(target, legacy, BrandMigrationOptions{})
	if err != nil {
		t.Fatalf("第二次迁移报错: %v", err)
	}
	if second.Status != BrandMigrationSkipped {
		t.Fatalf("第二次迁移状态应为 %q，实际 %q", BrandMigrationSkipped, second.Status)
	}
	if after := treeHash(t, target); after != targetHash {
		t.Fatalf("重复迁移改动了新目录（不幂等）")
	}
	if after := treeHash(t, legacy); after != legacyHash {
		t.Fatalf("重复迁移改动了旧目录")
	}
}

func TestBrandMigrationSkipsWhenTargetHasData(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "MyAgent")
	target := filepath.Join(base, "Anvil")
	seedLegacyTree(t, legacy)
	// 目标目录里已经有用户数据但没有迁移标记：这是"已经在新版本里用过一阵"的
	// 真实形态之一（例如迁移标记被误删）。这种情况下不许覆盖。
	writeFileT(t, filepath.Join(target, "settings.json"), []byte(`{"workspace":"D:\\new"}`))
	targetHash := treeHash(t, target)

	res, err := MigrateBrandData(target, legacy, BrandMigrationOptions{})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if res.Status != BrandMigrationExisting {
		t.Fatalf("状态应为 %q，实际 %q", BrandMigrationExisting, res.Status)
	}
	if after := treeHash(t, target); after != targetHash {
		t.Fatalf("已有数据的目标目录被改动")
	}
	if _, err := os.Stat(filepath.Join(target, brandMigrationMarkerName)); err == nil {
		t.Fatalf("跳过时不该写迁移标记（否则下次会误判为已迁移）")
	}
}

func TestBrandMigrationWithoutLegacyIsNoop(t *testing.T) {
	base := t.TempDir()
	res, err := MigrateBrandData(filepath.Join(base, "Anvil"), filepath.Join(base, "MyAgent"), BrandMigrationOptions{})
	if err != nil {
		t.Fatalf("没有旧目录不该报错: %v", err)
	}
	if res.Status != BrandMigrationNone {
		t.Fatalf("状态应为 %q，实际 %q", BrandMigrationNone, res.Status)
	}
	if _, err := os.Stat(filepath.Join(base, "Anvil")); !os.IsNotExist(err) {
		t.Fatalf("没有旧目录时不该创建新目录: %v", err)
	}
}

func TestBrandMigrationFailureKeepsSourceAndReportsDiagnostic(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "MyAgent")
	target := filepath.Join(base, "Anvil")
	seedLegacyTree(t, legacy)
	legacyHash := treeHash(t, legacy)

	// 注入第 2 个文件的复制失败：模拟磁盘满/权限被拒这类真实故障。
	calls := 0
	failing := BrandMigrationOptions{CopyFile: func(src, dst string) error {
		calls++
		if calls == 2 {
			return errors.New("注入的复制失败")
		}
		return copyFileForMigration(src, dst)
	}}

	res, err := MigrateBrandData(target, legacy, failing)
	if err == nil {
		t.Fatal("复制失败必须作为错误返回，不能静默成功")
	}
	if res.Status != BrandMigrationFailed {
		t.Fatalf("状态应为 %q，实际 %q", BrandMigrationFailed, res.Status)
	}
	if !strings.Contains(res.Error, "注入的复制失败") {
		t.Fatalf("结果里要给出失败原因（诊断来源）: %q", res.Error)
	}

	// 关键：失败时目标目录不能出现（半成品目录会让下次启动误判"已有数据"而永久跳过迁移）。
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("失败后不该留下目标目录: %v", statErr)
	}
	// staging 目录也必须清理干净，不能在 base 下留垃圾。
	entries, derr := os.ReadDir(base)
	if derr != nil {
		t.Fatalf("读 base: %v", derr)
	}
	for _, e := range entries {
		if e.Name() != "MyAgent" {
			t.Fatalf("失败后留下了中间目录: %s", e.Name())
		}
	}
	// 源目录逐字节不变。
	if after := treeHash(t, legacy); after != legacyHash {
		t.Fatalf("失败时改动了源目录")
	}
}

func TestBrandMigrationKeepsCredentialRefsAndDropsSecrets(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "MyAgent")
	target := filepath.Join(base, "Anvil")
	writeFileT(t, filepath.Join(legacy, "config", "models.json"), []byte(`{"providers":{"fixture":{"credentialRef":"ref-abc","apiKey":"$MYAGENT_API_KEY"}}}`))
	writeFileT(t, filepath.Join(legacy, "credentials", "ref-abc.bin"), []byte("SECRET-SENTINEL"))

	res, err := MigrateBrandData(target, legacy, BrandMigrationOptions{})
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if res.Status != BrandMigrationDone {
		t.Fatalf("状态不符: %q", res.Status)
	}

	raw, err := os.ReadFile(filepath.Join(target, "config", "models.json"))
	if err != nil {
		t.Fatalf("模型配置未迁移: %v", err)
	}
	// credentialRef 是稳定标识：迁移不得改写，否则旧凭证从此找不到。
	if !strings.Contains(string(raw), "ref-abc") {
		t.Fatalf("credentialRef 丢失: %s", raw)
	}
	if hits := scanTreeFor(t, target, "SECRET-SENTINEL"); len(hits) != 0 {
		t.Fatalf("秘密被复制进新目录: %v", hits)
	}
	if hits := scanTreeFor(t, target, "SECRET-SENTINEL"); len(hits) != 0 {
		t.Fatalf("秘密出现在迁移标记里: %v", hits)
	}
}

func TestPrepareDataRootMigratesOnceThenReuses(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, LegacyBrandName)
	seedLegacyTree(t, legacy)

	root, res, err := PrepareDataRoot(base)
	if err != nil {
		t.Fatalf("PrepareDataRoot: %v", err)
	}
	if root != filepath.Join(base, BrandName) {
		t.Fatalf("数据根应指向 Anvil 目录，实际 %s", root)
	}
	if res.Status != BrandMigrationDone {
		t.Fatalf("首次状态应为已迁移，实际 %q", res.Status)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("数据根未创建: %v", err)
	}

	// 第二次启动：复用同一个目录，不重复迁移。

	again, res2, err := PrepareDataRoot(base)
	if err != nil {
		t.Fatalf("第二次 PrepareDataRoot: %v", err)
	}
	if again != root {
		t.Fatalf("数据根不稳定: %s", again)
	}
	if res2.Status != BrandMigrationSkipped {
		t.Fatalf("第二次状态应为已迁移跳过，实际 %q", res2.Status)
	}
}

// TestPrepareDataRootFreshInstall 守住"全新安装"路径：
// 没有旧目录时直接建 Anvil 根，不写迁移标记、不产生迁移结果。
func TestPrepareDataRootFreshInstall(t *testing.T) {
	base := t.TempDir()
	root, res, err := PrepareDataRoot(base)
	if err != nil {
		t.Fatalf("PrepareDataRoot: %v", err)
	}
	if root != filepath.Join(base, BrandName) {
		t.Fatalf("数据根不符: %s", root)
	}
	if res.Status != BrandMigrationNone {
		t.Fatalf("状态应为 none，实际 %q", res.Status)
	}
	if _, err := os.Stat(filepath.Join(root, brandMigrationMarkerName)); err == nil {
		t.Fatalf("全新安装不该写迁移标记")
	}
}

// TestMigrationMarkerTimestampIsUTC 守住标记的时间字段：
// 用本地时间写进证据会在跨时区比对时对不上。
func TestMigrationMarkerTimestampIsUTC(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, LegacyBrandName)
	writeFileT(t, filepath.Join(legacy, "settings.json"), []byte(`{}`))
	target := filepath.Join(base, BrandName)

	fixed := time.Date(2026, 11, 5, 1, 2, 3, 0, time.FixedZone("UTC+8", 8*3600))
	if _, err := MigrateBrandData(target, legacy, BrandMigrationOptions{Now: func() time.Time { return fixed }}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(target, brandMigrationMarkerName))
	if err != nil {
		t.Fatalf("读标记: %v", err)
	}
	var marker BrandMigrationMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		t.Fatalf("解析标记: %v", err)
	}
	if !strings.HasSuffix(marker.MigratedAt, "Z") {
		t.Fatalf("迁移时间应写成 UTC，实际 %q", marker.MigratedAt)
	}
	if marker.MigratedAt != fixed.UTC().Format(time.RFC3339) {
		t.Fatalf("迁移时间不符: %q", marker.MigratedAt)
	}
}

// scanTreeFor 返回目录内命中 sentinel 的文件（相对路径）。
func scanTreeFor(t *testing.T, root, sentinel string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), sentinel) {
			rel, _ := filepath.Rel(root, p)
			hits = append(hits, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 %s: %v", root, err)
	}
	return hits
}
