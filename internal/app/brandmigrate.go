package app

// MyAgent → Anvil 的数据目录迁移（首次启动，一次，幂等，绝不删旧数据）。
//
// 为什么单独立一个入口而不是"改个字符串"：
// 数据根换了名字以后，老用户第一次启动时新根是空的——如果不迁移，
// 症状是"更新完应用，工作区、会话、模型配置全没了"。这类投诉无法靠
// 远程排查恢复，所以迁移必须可验证、可重试、且永不破坏原件。
//
// 纪律（每条都对应一种曾经真实发生过的数据事故）：
//   - 复制 + 校验 + 原子改名，不是"移动"：旧目录逐字节保留，
//     用户回退到旧版本时仍然可用；
//   - 白名单复制（配置与数据库），凭证/日志/产物不复制：
//     秘密不进新目录，新目录也不该因为几百 MB 的产物而卡住启动；
//   - 失败即清理 staging：绝不留下一个"看起来有数据"的半成品目录，
//     否则下次启动会因为目标已存在而永久跳过迁移；
//   - 迁移标记写在 staging 里再一起改名：标记与数据同生共死，
//     不会出现"标记说有数据、实际没有"的错位；
//   - 全程不读不写 Pi 自己的 ~/.pi（那是 Pi 的用户目录，不是本产品的）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 品牌名。目录名、窗口标题、可执行文件名与用户可见标题统一用它。
const (
	BrandName       = "Anvil"
	LegacyBrandName = "MyAgent"
)

// brandMigrationMarkerName 是写在新根里的迁移标记文件名。
const brandMigrationMarkerName = "brand-migration.json"

// brandMigrationStagingPrefix 是 staging 目录前缀（与 base 同级，保证 rename 同卷）。
const brandMigrationStagingPrefix = ".anvil-migrate-"

// BrandMigrationStatus 是迁移结果的状态。
type BrandMigrationStatus string

const (
	// BrandMigrationNone：没有旧目录，什么都没做（全新安装）。
	BrandMigrationNone BrandMigrationStatus = "none"
	// BrandMigrationDone：本次完成了迁移。
	BrandMigrationDone BrandMigrationStatus = "migrated"
	// BrandMigrationSkipped：目标已有迁移标记，幂等跳过。
	BrandMigrationSkipped BrandMigrationStatus = "already_migrated"
	// BrandMigrationExisting：目标已有非空数据但没有标记，拒绝覆盖。
	BrandMigrationExisting BrandMigrationStatus = "existing_target"
	// BrandMigrationFailed：迁移失败，源目录保持原样、目标未创建。
	BrandMigrationFailed BrandMigrationStatus = "failed"
)

// BrandMigrationFile 是迁移标记里的一条文件记录（路径 + 摘要 + 大小）。
//
// 记录摘要而不是只记路径：事后要能回答"这份配置迁移后还是原来那份吗"。
type BrandMigrationFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// BrandMigrationMarker 是写在新数据根里的迁移标记。
type BrandMigrationMarker struct {
	SchemaVersion int                  `json:"schemaVersion"`
	From          string               `json:"from"`
	To            string               `json:"to"`
	MigratedAt    string               `json:"migratedAt"`
	Source        string               `json:"source"`
	Files         []BrandMigrationFile `json:"files"`
}

// BrandMigrationResult 是一次迁移尝试的结果（也用作首次启动的诊断来源）。
type BrandMigrationResult struct {
	Status BrandMigrationStatus `json:"status"`
	Source string               `json:"source,omitempty"`
	Target string               `json:"target,omitempty"`
	Files  []BrandMigrationFile `json:"files,omitempty"`
	// Marker 是标记文件的绝对路径（成功时有值）。
	Marker string `json:"marker,omitempty"`
	// Error 是失败原因的脱敏摘要（不含堆栈、不含秘密）。
	Error string `json:"error,omitempty"`
}

// BrandMigrationOptions 允许测试注入复制失败与固定时钟。生产用零值。
type BrandMigrationOptions struct {
	// CopyFile 覆盖单文件复制（测试注入故障用）；nil 用真实复制。
	CopyFile func(src, dst string) error
	// Now 覆盖时钟（测试断言标记里的时间格式）；nil 用 time.Now。
	Now func() time.Time
}

// brandMigrationCopies 判断一个**相对路径**是否属于"非敏感配置与数据库"白名单。
//
// 白名单而不是黑名单：新增一种文件（比如未来的缓存目录）时，
// 黑名单会默默把它搬过去，白名单只会少搬——少搬可以补，多搬（尤其是秘密）无法撤销。
func brandMigrationCopies(rel string) bool {
	rel = filepath.ToSlash(rel)
	// 顶层配置文件。
	if rel == "settings.json" {
		return true
	}
	// 版本化配置存储（config/ 下都是配置文档，不进秘密：秘密只在 OS 凭证库）。
	if strings.HasPrefix(rel, "config/") {
		return true
	}
	// 顶层数据库与其 WAL/SHM 附属文件。
	if !strings.Contains(rel, "/") {
		switch {
		case strings.HasSuffix(rel, ".db"),
			strings.HasSuffix(rel, ".db-wal"),
			strings.HasSuffix(rel, ".db-shm"):
			return true
		}
	}
	return false
}

// MigrateBrandData 把 legacyRoot 里的非敏感数据迁移到 targetRoot。
//
// 返回错误只表示"调用本身不成立"（例如路径解析失败）；迁移失败会以
// Status=BrandMigrationFailed + Error 摘要的形式返回，让壳层可以记录诊断
// 后继续以空的新根启动——打不开应用比丢一份旧配置更糟。
func MigrateBrandData(targetRoot, legacyRoot string, opts BrandMigrationOptions) (BrandMigrationResult, error) {
	if strings.TrimSpace(targetRoot) == "" || strings.TrimSpace(legacyRoot) == "" {
		return BrandMigrationResult{Status: BrandMigrationFailed, Error: "数据目录路径为空"},
			errors.New("数据目录路径为空")
	}
	target, err := filepath.Abs(targetRoot)
	if err != nil {
		return BrandMigrationResult{Status: BrandMigrationFailed, Error: "目标目录路径无法绝对化"}, err
	}
	legacy, err := filepath.Abs(legacyRoot)
	if err != nil {
		return BrandMigrationResult{Status: BrandMigrationFailed, Error: "旧目录路径无法绝对化"}, err
	}
	res := BrandMigrationResult{Source: legacy, Target: target}

	legacyInfo, statErr := os.Stat(legacy)
	if statErr != nil || !legacyInfo.IsDir() {
		// 没有旧目录：全新安装，什么都不做（连空目录都不建，否则调用方
		// 无法区分"迁移过"和"刚建的空目录"）。
		res.Status = BrandMigrationNone
		return res, nil
	}

	targetInfo, terr := os.Stat(target)
	targetExists := terr == nil && targetInfo.IsDir()
	if targetExists {
		if _, merr := os.Stat(filepath.Join(target, brandMigrationMarkerName)); merr == nil {
			res.Status = BrandMigrationSkipped
			res.Marker = filepath.Join(target, brandMigrationMarkerName)
			return res, nil
		}
		empty, eerr := dirIsEmpty(target)
		if eerr != nil {
			res.Status = BrandMigrationFailed
			res.Error = "读取目标目录失败"
			return res, eerr
		}
		if !empty {
			// 已经有数据但没有标记：可能是标记被删、也可能是新版本已经用过。
			// 无论哪种，覆盖都是错的。
			res.Status = BrandMigrationExisting
			return res, nil
		}
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	copyFile := copyFileForMigration
	if opts.CopyFile != nil {
		copyFile = opts.CopyFile
	}

	staging, err := os.MkdirTemp(filepath.Dir(target), brandMigrationStagingPrefix)
	if err != nil {
		res.Status = BrandMigrationFailed
		res.Error = "创建迁移暂存目录失败"
		return res, err
	}
	// 失败路径统一清理 staging：留下的半成品会让下一次启动误判"目标已有数据"。
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	files, err := copyBrandData(legacy, staging, copyFile)
	if err != nil {
		res.Status = BrandMigrationFailed
		res.Error = "复制数据失败: " + err.Error()
		return res, err
	}

	// 校验：逐个比大小与摘要。复制"看起来成功"但内容被截断，
	// 是磁盘满/杀进程时的常见形态；不校验就等于把损坏当成迁移完成。
	if err := verifyMigrated(legacy, staging, files); err != nil {
		res.Status = BrandMigrationFailed
		res.Error = "校验迁移结果失败: " + err.Error()
		return res, err
	}

	marker := BrandMigrationMarker{
		SchemaVersion: 1,
		From:          LegacyBrandName,
		To:            BrandName,
		MigratedAt:    now().UTC().Format(time.RFC3339),
		Source:        legacy,
		Files:         files,
	}
	markerRaw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		res.Status = BrandMigrationFailed
		res.Error = "序列化迁移标记失败"
		return res, err
	}
	if err := os.WriteFile(filepath.Join(staging, brandMigrationMarkerName), markerRaw, 0o600); err != nil {
		res.Status = BrandMigrationFailed
		res.Error = "写入迁移标记失败"
		return res, err
	}

	// 目标若存在（只可能是空目录）：先摘掉它，再原子改名，
	// 这样"目标目录出现"与"迁移完成"是同一个瞬间。
	if targetExists {
		if err := os.Remove(target); err != nil {
			res.Status = BrandMigrationFailed
			res.Error = "清理空目标目录失败"
			return res, err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		res.Status = BrandMigrationFailed
		res.Error = "原子改名失败"
		return res, err
	}
	committed = true

	res.Status = BrandMigrationDone
	res.Files = files
	res.Marker = filepath.Join(target, brandMigrationMarkerName)
	return res, nil
}

// copyBrandData 按白名单把 legacy 复制到 staging，返回已复制文件的记录（按路径排序）。
func copyBrandData(legacy, staging string, copyFile func(src, dst string) error) ([]BrandMigrationFile, error) {
	var files []BrandMigrationFile
	err := filepath.WalkDir(legacy, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(legacy, p)
		if rerr != nil {
			return rerr
		}
		if !brandMigrationCopies(rel) {
			return nil
		}
		dst := filepath.Join(staging, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := copyFile(p, dst); err != nil {
			return fmt.Errorf("%s: %w", filepath.ToSlash(rel), err)
		}
		sum, size, herr := fileDigest(dst)
		if herr != nil {
			return herr
		}
		files = append(files, BrandMigrationFile{Path: filepath.ToSlash(rel), SHA256: sum, Size: size})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// verifyMigrated 逐个比对源文件与新文件的摘要与大小。
func verifyMigrated(legacy, staging string, files []BrandMigrationFile) error {
	for _, f := range files {
		srcSum, srcSize, err := fileDigest(filepath.Join(legacy, filepath.FromSlash(f.Path)))
		if err != nil {
			return err
		}
		dstSum, dstSize, err := fileDigest(filepath.Join(staging, filepath.FromSlash(f.Path)))
		if err != nil {
			return err
		}
		if srcSum != dstSum || srcSize != dstSize {
			return fmt.Errorf("%s 复制后内容不一致", f.Path)
		}
		if f.SHA256 != dstSum || f.Size != dstSize {
			return fmt.Errorf("%s 的记录与落盘内容不一致", f.Path)
		}
	}
	return nil
}

// fileDigest 返回文件的 sha256（十六进制）与字节数。
func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// copyFileForMigration 是默认的单文件复制（0600：迁移过来的配置可能含路径信息）。
func copyFileForMigration(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dirIsEmpty 判断目录是否为空（只用于"目标目录里有没有用户数据"）。
func dirIsEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(names) == 0, nil
}

// PrepareDataRoot 给出应用数据根，并在首次启动时执行品牌迁移。
//
// 顺序刻意是"先迁移、再建目录"：如果先 MkdirAll 出新根，
// MigrateBrandData 会看到"目标已存在"，于是永远跳过迁移。
func PrepareDataRoot(base string) (string, BrandMigrationResult, error) {
	target := filepath.Join(base, BrandName)
	legacy := filepath.Join(base, LegacyBrandName)

	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", BrandMigrationResult{Status: BrandMigrationFailed}, fmt.Errorf("创建用户配置基目录失败: %w", err)
	}

	res, err := MigrateBrandData(target, legacy, BrandMigrationOptions{})
	if err != nil && res.Status != BrandMigrationFailed {
		return "", res, err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", res, fmt.Errorf("创建应用数据目录失败: %w", err)
	}
	return target, res, nil
}
