package artifact

// C07 产物导入 / 核对 / 导出：路径解析与磁盘事实。
//
// 这里只有一条纪律：**路径与内容都必须自己重新证明**。
//   - 用户/工具给的路径被当作"候选定位串"，先词法拒绝、再逐段查链接、最后用
//     policy.PathGuard 证明解析后的真实路径仍在工作区根内；
//   - 读内容时 size 与 sha256 取自**同一个打开句柄**，判断和读取是同一份事实；
//   - 导出先核对源、再写目标；对不上就一个字节都不落盘。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"my-agent-go/internal/contracts"
)

// sniffLen 是内容嗅探需要的最大字节数（net/http 的口径）。
const sniffLen = 512

// ImportArtifact 登记一个工作区内的文件为产物。
//
// 拒绝顺序是刻意的：词法 → 链接 → 根内 → stat → 读字节 → 写索引。
// 任何一步失败都不会产生索引行，也不会读到工作区外的字节。
func (s *Service) ImportArtifact(ctx context.Context, req ImportRequest) (Artifact, error) {
	if s == nil {
		return Artifact{}, contracts.NewAppError(contracts.CodeInternal, "产物服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return Artifact{}, err
	}
	root, err := s.workspaceRootFor(ctx, req.WorkspaceID)
	if err != nil {
		return Artifact{}, err
	}
	resolved, err := resolveSourcePath(root, req.Path)
	if err != nil {
		return Artifact{}, err
	}

	obs, err := observeFile(resolved, sniffLen)
	if err != nil {
		return Artifact{}, err
	}
	if !obs.regular {
		return Artifact{}, contracts.NewAppError(contracts.CodeUnsupported,
			"不是普通文件（目录/管道/设备不能登记为产物）: "+resolved, false)
	}
	if obs.size > s.maxBytes {
		return Artifact{}, contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
			"产物超过大小上限（%d > %d 字节）: %s", obs.size, s.maxBytes, resolved), false)
	}

	display := strings.TrimSpace(req.DisplayName)
	if display == "" {
		display = filepath.Base(resolved)
	}
	mediaType := detectMediaType(display, obs.head)
	now := formatTS(s.now())
	workspaceID := strings.TrimSpace(string(req.WorkspaceID))
	runID := strings.TrimSpace(string(req.RunID))
	sessionID := strings.TrimSpace(string(req.SessionID))
	key := pathKey(resolved)

	id, err := newArtifactID()
	if err != nil {
		return Artifact{}, err
	}

	// 同一工作区同一路径只登记一次：重新导入是更新（hash/大小/归属），不是新增。
	// run_id/session_id/display_name 为空时保留旧值——"重新核对一遍"不该把归属抹掉。
	if _, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO artifacts(id, workspace_id, run_id, session_id, display_name,
		                     canonical_path, path_key, sha256, size_bytes, media_type,
		                     created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, path_key) DO UPDATE SET
		    run_id         = CASE WHEN excluded.run_id = '' THEN artifacts.run_id ELSE excluded.run_id END,
		    session_id     = CASE WHEN excluded.session_id = '' THEN artifacts.session_id ELSE excluded.session_id END,
		    display_name   = CASE WHEN excluded.display_name = '' THEN artifacts.display_name ELSE excluded.display_name END,
		    canonical_path = excluded.canonical_path,
		    sha256         = excluded.sha256,
		    size_bytes     = excluded.size_bytes,
		    media_type     = excluded.media_type,
		    updated_at     = excluded.updated_at`,
		id, workspaceID, runID, sessionID, display,
		resolved, key, obs.sha256, obs.size, mediaType,
		now, now); err != nil {
		return Artifact{}, contracts.NewAppError(contracts.CodeInternal, "写入产物索引失败: "+err.Error(), true)
	}

	row := s.db.SQL().QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts WHERE workspace_id = ? AND path_key = ?`,
		workspaceID, key)
	item, err := scanArtifact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, contracts.NewAppError(contracts.CodeInternal, "产物写入后读不回: "+resolved, true)
	}
	if err != nil {
		return Artifact{}, err
	}
	return item, nil
}

// resolveSourcePath 把候选路径解析成"工作区根内、逐段都不是链接的普通文件路径"。
//
// 每一步都在读取字节之前：被拒的导入不会产生 blob，也不会留下索引行。
//
// 两道判定，顺序是刻意的：
//  1. 写法一致时（绝大多数情况）**逐段**拒绝链接/目录联接。这条比"解析后仍在根内"
//     更严：产物是"用户看见的那个文件"的快照，经过链接就与索引里的路径脱钩。
//  2. 规范化后再判根内。规范化用 filepath.EvalSymlinks：它在 Windows 上还会把
//     8.3 短名（ADMINI~1）展开成真实名。只做路径字符串比较会把"用短名给出的
//     工作区内文件"判成越界——那是真实的可用性缺陷，而越界真正的边界是
//     "规范化后落在哪个目录"，不是"字面怎么写"。
//
// 边界声明：当调用方用另一种写法（短名）给路径时，第 1 条的逐段检查没有可比的
// 前缀，此时只保证第 2 条——解析后仍在根内、且最终文件本身不是链接。
// 这条限制写在 C07 报告里，不用"看起来更严"的话掩盖。
func resolveSourcePath(root, raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "产物路径为空", false)
	}
	if strings.ContainsRune(path, 0) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "产物路径包含 NUL 字节", false)
	}
	if err := lexicalRejectPath(path); err != nil {
		return "", err
	}
	if hasDotDotSegment(path) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"产物路径不允许用 .. 拼接（请使用工具实际写出的路径）: "+path, false)
	}
	if !filepath.IsAbs(path) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "产物路径必须是绝对路径: "+path, false)
	}
	rootAbs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "工作区根无法绝对化: "+err.Error(), false)
	}
	rootAbs = filepath.Clean(rootAbs)
	if fi, statErr := os.Stat(rootAbs); statErr != nil || !fi.IsDir() {
		return "", contracts.NewAppError(contracts.CodeNotFound, "工作区根不可用: "+rootAbs, false)
	}
	abs := filepath.Clean(path)

	// 第 1 道：同一种写法时逐段查链接（在读字节之前）。
	if rel, relErr := filepath.Rel(rootAbs, abs); relErr == nil && !escapesRoot(rel) {
		link, linkErr := linkBelowRoot(rootAbs, rel)
		if linkErr != nil {
			return "", contracts.NewAppError(contracts.CodeUnauthorized,
				"产物路径无法解析: "+abs+" : "+linkErr.Error(), false)
		}
		if link != "" {
			return "", contracts.NewAppError(contracts.CodeUnauthorized,
				"产物路径经过符号链接/目录联接，拒绝登记: "+link, false)
		}
	}

	// 第 2 道：规范化后必须落在工作区根内。
	canonRoot, err := canonicalForScope(rootAbs)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeNotFound, "工作区根不可用: "+rootAbs, false)
	}
	canonTarget, err := canonicalForScope(abs)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeNotFound, "产物文件不存在: "+abs, false)
	}
	rel, err := filepath.Rel(canonRoot, canonTarget)
	if err != nil || escapesRoot(rel) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "产物路径越界: "+abs, false)
	}

	fi, err := os.Stat(canonTarget)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return "", contracts.NewAppError(contracts.CodeNotFound, "产物文件不存在: "+abs, false)
	case errors.Is(err, fs.ErrPermission):
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "无权读取产物文件: "+abs, false)
	default:
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"产物文件不可访问: "+abs+"（"+err.Error()+"）", false)
	}
	if fi.IsDir() {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "登记的是目录，不是文件: "+abs, false)
	}
	if !fi.Mode().IsRegular() {
		return "", contracts.NewAppError(contracts.CodeUnsupported,
			"不是普通文件（管道/设备等不能登记为产物）: "+abs, false)
	}
	return abs, nil
}

// canonicalForScope 规范化路径：解析链接，并在 Windows 上把 8.3 短名展开。
//
// 目标可能不存在（写导出文件、文件已被删）；此时解析最近存在的祖先，再把剩余段
// 拼回去，这样"根内判定"仍然成立，而"文件在不在"由后续 Stat 回答——
// 不能因为"还没走到"就把路径判成越界或放行。
func canonicalForScope(p string) (string, error) {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved), nil
	}
	rest := ""
	cur := clean
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", contracts.NewAppError(contracts.CodeNotFound, "路径不可解析: "+p, false)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest), nil
		}
	}
}

// observation 是一次文件读取的事实：大小、hash 与开头若干字节。
type observation struct {
	regular bool
	size    int64
	sha256  string
	head    []byte
}

// observeFile 在**同一个打开句柄**上取 size、算 sha256、留 head。
//
// keepHead<=0 时不留内容（只核对）。读取上限是 maxBytes+1：超出即报错，
// 不截断——"只核对了一半的文件"比报错更危险。
func observeFile(path string, keepHead int) (observation, error) {
	f, err := os.Open(path)
	if err != nil {
		return observation{}, err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat() // 与读取同一个句柄：判断和读取是同一份事实
	if err != nil {
		return observation{}, err
	}
	obs := observation{regular: fi.Mode().IsRegular(), size: fi.Size()}
	if !obs.regular {
		return obs, nil
	}

	hasher := sha256.New()
	var head []byte
	buf := make([]byte, 128*1024)
	var total int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			total += int64(n)
			if _, werr := hasher.Write(buf[:n]); werr != nil {
				return observation{}, werr
			}
			if keepHead > 0 && len(head) < keepHead {
				want := keepHead - len(head)
				if n < want {
					want = n
				}
				head = append(head, buf[:want]...)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return observation{}, rerr
		}
	}
	obs.size = total
	obs.head = head
	obs.sha256 = hex.EncodeToString(hasher.Sum(nil))
	return obs, nil
}

// verify 核对一条索引行的当前磁盘事实。
//
// 结论只有三种，且都是"这次读到的事实"：
//   - verified：size 与 sha256 都与登记一致；
//   - drifted：文件还在但内容/大小变了，或路径现在经过链接/越界；
//   - missing：文件或工作区根已经不可用。
func (s *Service) verify(ctx context.Context, item Artifact) Verification {
	now := s.now().UTC()
	src, problem := s.resolveSource(ctx, item)
	if problem != nil {
		problem.CheckedAt = now
		return *problem
	}
	obs, err := observeFile(src, 0)
	if err != nil {
		return Verification{
			Status:    StatusDrifted,
			Reason:    "无法读取产物内容: " + err.Error(),
			CheckedAt: now,
		}
	}
	if !obs.regular {
		return Verification{Status: StatusDrifted, Reason: "不再是普通文件", CheckedAt: now}
	}
	size := obs.size
	ver := Verification{ObservedSHA256: obs.sha256, ObservedSize: &size, CheckedAt: now}
	switch {
	case obs.size != item.Size && obs.sha256 != item.SHA256:
		ver.Status = StatusDrifted
		ver.Reason = fmt.Sprintf("大小与 hash 都变了（登记 %d 字节 %s，实际 %d 字节 %s）",
			item.Size, shortSHA(item.SHA256), obs.size, shortSHA(obs.sha256))
	case obs.size != item.Size:
		ver.Status = StatusDrifted
		ver.Reason = fmt.Sprintf("大小变了（登记 %d 字节，实际 %d 字节）", item.Size, obs.size)
	case obs.sha256 != item.SHA256:
		ver.Status = StatusDrifted
		ver.Reason = fmt.Sprintf("内容变了（登记 sha256 %s，实际 %s）", shortSHA(item.SHA256), shortSHA(obs.sha256))
	default:
		ver.Status = StatusVerified
	}
	return ver
}

// resolveSource 重新证明"索引里的路径仍然指向登记时那个工作区根内的普通文件"。
//
// 返回 (路径, nil) 或 ("", 问题)。问题里的 Status 只会是 drifted/missing：
// 这是"内容层面的事实"，不是调用错误（产物不存在是完全可以展示的状态）。
func (s *Service) resolveSource(ctx context.Context, item Artifact) (string, *Verification) {
	root, err := s.workspaceRootFor(ctx, item.WorkspaceID)
	if err != nil {
		return "", &Verification{Status: StatusMissing, Reason: "工作区根不可用: " + err.Error()}
	}
	rootAbs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", &Verification{Status: StatusMissing, Reason: "工作区根无法绝对化: " + err.Error()}
	}
	rootAbs = filepath.Clean(rootAbs)
	if fi, statErr := os.Stat(rootAbs); statErr != nil || !fi.IsDir() {
		return "", &Verification{Status: StatusMissing, Reason: "工作区根已不可用: " + rootAbs}
	}

	// 与导入同一顺序：先逐段查链接（写法一致时），再规范化判根内。
	if rel, relErr := filepath.Rel(rootAbs, item.CanonicalPath); relErr == nil && !escapesRoot(rel) {
		link, linkErr := linkBelowRoot(rootAbs, rel)
		if linkErr != nil {
			return "", &Verification{Status: StatusDrifted, Reason: "路径无法解析: " + linkErr.Error()}
		}
		if link != "" {
			return "", &Verification{Status: StatusDrifted,
				Reason: "路径现在经过符号链接/目录联接，拒绝按它读文件: " + link}
		}
	}
	canonRoot, err := canonicalForScope(rootAbs)
	if err != nil {
		return "", &Verification{Status: StatusMissing, Reason: "工作区根不可用: " + rootAbs}
	}
	canonTarget, err := canonicalForScope(item.CanonicalPath)
	if err != nil {
		return "", &Verification{Status: StatusMissing, Reason: "产物文件已不存在: " + item.CanonicalPath}
	}
	rel, err := filepath.Rel(canonRoot, canonTarget)
	if err != nil || escapesRoot(rel) {
		return "", &Verification{Status: StatusDrifted,
			Reason: "登记的路径已经不在工作区根内: " + item.CanonicalPath}
	}
	if _, statErr := os.Stat(canonTarget); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return "", &Verification{Status: StatusMissing, Reason: "产物文件已不存在: " + item.CanonicalPath}
		}
		return "", &Verification{Status: StatusDrifted,
			Reason: "产物文件不可访问: " + item.CanonicalPath + "（" + statErr.Error() + "）"}
	}
	return item.CanonicalPath, nil
}

// ExportArtifact 把产物复制到导出根下的 target。
//
// 顺序：解析目标 → 核对源 → 同目录临时文件 → fsync → rename。
// 源与登记不符时一个字节都不写；写完的字节先经过 sha256 复核，再改名发布。
func (s *Service) ExportArtifact(ctx context.Context, id, target string) (ExportResult, error) {
	if s == nil {
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "产物服务未初始化", false)
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	item, err := s.load(ctx, id)
	if err != nil {
		return ExportResult{}, err
	}
	targetPath, err := s.resolveExportTarget(target)
	if err != nil {
		return ExportResult{}, err
	}
	src, problem := s.resolveSource(ctx, item)
	if problem != nil {
		return ExportResult{}, verificationError(item, problem)
	}

	in, err := os.Open(src)
	if err != nil {
		return ExportResult{}, contracts.NewAppError(contracts.CodeVerificationFailed,
			"产物内容已不可读: "+item.CanonicalPath+" : "+err.Error(), true)
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat() // 与复制同一个句柄
	if err != nil {
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "读取产物元数据失败: "+err.Error(), true)
	}
	if !fi.Mode().IsRegular() {
		return ExportResult{}, contracts.NewAppError(contracts.CodeVerificationFailed, "产物不再是普通文件", false)
	}

	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".artifact-*.tmp")
	if err != nil {
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "创建导出临时文件失败: "+err.Error(), false)
	}
	tmpPath := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(in, s.maxBytes+1))
	if err != nil {
		discard()
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "导出产物失败: "+err.Error(), true)
	}
	if written > s.maxBytes {
		discard()
		return ExportResult{}, contracts.NewAppError(contracts.CodeUnsupported,
			fmt.Sprintf("产物超过大小上限（> %d 字节）: %s", s.maxBytes, item.CanonicalPath), false)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if written != item.Size || sum != item.SHA256 {
		discard()
		return ExportResult{}, verificationError(item, &Verification{
			Status:         StatusDrifted,
			ObservedSHA256: sum,
			ObservedSize:   &written,
			Reason: fmt.Sprintf("导出时读到的内容与登记不符（登记 %d 字节 %s，实际 %d 字节 %s）",
				item.Size, shortSHA(item.SHA256), written, shortSHA(sum)),
		})
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "刷新导出文件失败: "+err.Error(), false)
	}
	if err := tmp.Close(); err != nil {
		discard()
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "关闭导出文件失败: "+err.Error(), false)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		discard()
		return ExportResult{}, contracts.NewAppError(contracts.CodeInternal, "发布导出文件失败: "+err.Error(), false)
	}
	return ExportResult{
		ArtifactID: item.ID,
		Path:       targetPath,
		Bytes:      written,
		SHA256:     sum,
		WrittenAt:  s.now().UTC(),
	}, nil
}

// resolveExportTarget 把 target 解析成"导出根内的普通文件路径"。
//
// 与 C06e 的会话导出同源：先词法（不依赖文件系统）、再链接（不跟随）、
// 再根内判定（policy.PathGuard 解析真实路径），最后看目标本身。
func (s *Service) resolveExportTarget(target string) (string, error) {
	raw := strings.TrimSpace(target)
	if raw == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "导出目标路径为空", false)
	}
	if strings.ContainsRune(raw, 0) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "导出目标路径包含 NUL 字节", false)
	}
	if err := lexicalRejectPath(raw); err != nil {
		return "", err
	}
	if hasDotDotSegment(raw) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标不允许用 .. 拼接（请使用导出目录下的名字）: "+raw, false)
	}
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.exportRoot, raw)
	}
	abs = filepath.Clean(abs)

	rel, err := filepath.Rel(s.exportRoot, abs)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标与导出根无法比较: "+abs, false)
	}
	if escapesRoot(rel) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "导出目标越界: "+abs, false)
	}
	link, err := linkBelowRoot(s.exportGuard.Roots()[0], rel)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标无法解析: "+abs+" : "+err.Error(), false)
	}
	if link != "" {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标经过符号链接/目录联接，拒绝写入: "+link, false)
	}
	dec, err := s.exportGuard.Check(abs)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(dec.Real)
	fi, err := os.Stat(dir)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return "", contracts.NewAppError(contracts.CodeUnsupported, "导出目标的父路径不是目录: "+dir, false)
		}
	case errors.Is(err, fs.ErrNotExist):
		return "", contracts.NewAppError(contracts.CodeNotFound, "导出目标目录不存在: "+dir, false)
	default:
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标目录不可访问: "+dir+"（"+err.Error()+"）", false)
	}
	if fi, err := os.Lstat(dec.Real); err == nil {
		if fi.IsDir() {
			return "", contracts.NewAppError(contracts.CodeUnsupported, "导出目标是目录，不是文件: "+dec.Real, false)
		}
		if !fi.Mode().IsRegular() {
			return "", contracts.NewAppError(contracts.CodeUnsupported,
				"导出目标不是普通文件（管道/设备等不能作为导出目标）: "+dec.Real, false)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", contracts.NewAppError(contracts.CodeUnauthorized,
			"导出目标不可访问: "+dec.Real+"（"+err.Error()+"）", false)
	}
	return dec.Real, nil
}

// verificationError 把"内容核对不上"变成结构化错误。
//
// 用 VERIFICATION_FAILED 而不是 NOT_FOUND：文件可能还在，只是不再是登记的那一份。
// 错误文案里同时带索引值与实测值，排查不需要再去猜。
func verificationError(item Artifact, ver *Verification) error {
	if ver.Status == StatusMissing {
		return contracts.NewAppError(contracts.CodeVerificationFailed,
			"产物内容不可用（"+ver.Status+"）："+ver.Reason, true)
	}
	return contracts.NewAppError(contracts.CodeVerificationFailed,
		"产物内容与登记不符（"+ver.Status+"）："+ver.Reason, false)
}

// detectMediaType 先按扩展名，再按内容嗅探；两者都失败时给二进制兜底。
//
// 它只用于展示与预览白名单，不参与任何安全判定：真正的边界是"工作区内 + 逐段非链接"。
func detectMediaType(name string, head []byte) string {
	if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); mt != "" {
		return normalizeMediaType(mt)
	}
	if len(head) > 0 {
		return normalizeMediaType(http.DetectContentType(head))
	}
	return "application/octet-stream"
}

// normalizeMediaType 去掉参数（charset 等）并统一小写。
func normalizeMediaType(mt string) string {
	if idx := strings.IndexByte(mt, ';'); idx >= 0 {
		mt = mt[:idx]
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

// linkBelowRoot 返回 root/rel 这条路径上第一个符号链接/目录联接组件；没有则返回空。
//
// 只从**工作区根之下**开始逐段 Lstat，不从卷根开始：
//   - macOS 的 /var 本身就是符号链接，从卷根走会把每个正常文件都判成链接；
//   - Windows 的 t.TempDir() 可能带 8.3 短名，字符串比较会把正常路径误判，
//     而逐段 Lstat 只看"这一层是不是 reparse point"，与短名/大小写无关。
//
// 目录联接在 Go 的 Lstat 里可能没有 ModeSymlink，但 Readlink 一定能读出目标，
// 所以对每个非 symlink 的条目也试探一次 Readlink（与 internal/policy 同一判据）。
func linkBelowRoot(realRoot, rel string) (string, error) {
	if rel == "" || rel == "." {
		return "", nil
	}
	cur := realRoot
	for _, seg := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == "." || seg == ".." || seg == "" {
			continue
		}
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// 剩下的部分不存在：交给后续 Stat 报 NOT_FOUND / missing，
				// 不能因为"还没走到"就放行成链接。
				return "", nil
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return cur, nil
		}
		if target, rerr := os.Readlink(cur); rerr == nil && target != "" {
			return cur, nil
		}
	}
	return "", nil
}

// escapesRoot 判断相对路径是否指向根之外（含绝对路径与 `..` 前缀）。
func escapesRoot(rel string) bool {
	if rel == "" {
		return false
	}
	if filepath.IsAbs(rel) {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// hasDotDotSegment 判断路径里是否出现 `..` 段（Clean 之前判断）。
func hasDotDotSegment(raw string) bool {
	for _, seg := range strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// lexicalRejectPath 是纯词法拒绝，必须在解析之前执行。
//
// 与 internal/policy/pathscope.go 的 lexicalReject 同源（UNC/设备路径、
// 驱动器相对路径、备用数据流）。policy 没有导出它，而本任务的边界不允许改
// policy，所以这里保留同一组规则；两处若将来分叉，以 policy 的版本为准。
func lexicalRejectPath(raw string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, "//") {
		return contracts.NewAppError(contracts.CodeUnauthorized, "产物路径不允许 UNC/设备路径: "+raw, false)
	}
	if len(raw) >= 2 && raw[1] == ':' {
		if len(raw) == 2 || (raw[2] != '\\' && raw[2] != '/') {
			return contracts.NewAppError(contracts.CodeUnauthorized, "产物路径不允许驱动器相对路径: "+raw, false)
		}
		for _, seg := range strings.Split(raw[2:], `\`) {
			if strings.Contains(seg, ":") {
				return contracts.NewAppError(contracts.CodeUnauthorized, "产物路径不允许备用数据流路径: "+raw, false)
			}
		}
	}
	return nil
}

// runtimeIsCaseInsensitive 判断当前平台的文件名是否默认大小写不敏感。
func runtimeIsCaseInsensitive() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

// shortSHA 把 hash 截短用于错误文案；只有前 12 位，够区分也够短。
func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func newArtifactID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成产物 ID 失败: "+err.Error(), false)
	}
	return "art-" + hex.EncodeToString(buf), nil
}
