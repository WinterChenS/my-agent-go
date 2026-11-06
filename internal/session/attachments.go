// Package session 的 C06c 部分：普通会话的附件接收与只读投递。
//
// 边界（与基线 §5、计划 C06「普通会话与附件」一致）：
//   - 用户通过原生选择器选文件，Go 只收**路径**，字节从不经过前端；
//   - 路径必须重新解析：`..`、符号链接、Windows 目录联接、越界绝对路径一律
//     结构化拒绝，并且拒绝发生在读取之前——不产生 blob；
//   - 字节复制进 app 托管、content-addressed 的 blob store，位置在
//     `<appRoot>/blobs/<aa>/<sha256>`，不在 run 被授予的 workspace 里；
//   - run 侧只拿到**只读能力**（capability id），字节只能经 OpenAttachment
//     在**同一个打开句柄**上重新校验 size/sha256 之后交付；
//   - 源文件只读，从不回写；blob 被替换/截断、能力过期都 fail closed。
//
// 本包保证的是"policy 边界内的 integrity-checked snapshot"，不宣称能抵抗
// 已经取得同一用户任意进程/磁盘写权限的攻击者：那种攻击者可以直接改 blob，
// 这里能做的是"改了就一定被发现，并且一个字节都不交出去"（见 OpenAttachment）。
//
// 元数据（展示名/媒体类型/session 归属）保存在内存里的能力登记表，不新增表：
// 附件引用的权威是内容本身（sha256），重启后重新选择即可重新登记。这样做避免了
// 迁移 006（被 C08/C09 占用）和"第二套附件索引"。

package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"sync"
	"time"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

const (
	// DefaultCapabilityTTL 是附件能力（capability）的默认有效期。
	// 它限制"登记过的引用能读多久"，不是"文件何时过期"：blob 本身按内容寻址保留。
	DefaultCapabilityTTL = 30 * time.Minute
	// DefaultMaxAttachmentBytes 是单个附件的大小上限。
	// 校验在内存里完成（先验后发），所以必须有上限，否则一个大文件就能把进程撑爆。
	DefaultMaxAttachmentBytes int64 = 64 << 20
	// blobTempDirName 是同卷临时目录：先写这里，再 rename 到内容地址，
	// 保证最终的 blob 要么完整、要么不存在，不会被读者看到半份。
	blobTempDirName = "tmp"
	// mediaTypeSniffLen 是 http.DetectContentType 需要的最大字节数。
	mediaTypeSniffLen = 512
)

// AttachmentRequest 是用户在原生文件选择器里选中的一项。
//
// 只带路径，不带字节：前端传字节等于让"用户选的文件"和"实际落地的文件"
// 变成两件事，hash 校验也就失去意义。
type AttachmentRequest struct {
	Path        string `json:"path"`
	DisplayName string `json:"displayName"`
}

// AttachmentReady 是唯一可交付的状态：内容已落地且已按 sha256 登记。
const AttachmentReady = "ready"

// AttachmentMeta 是一个已落地、可供 run 只读读取的附件引用。
//
// 它不包含任何可写路径：Pi 只拿得到 ID，字节只能经 broker 读取。
type AttachmentMeta struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	MediaType   string    `json:"mediaType"`
	Status      string    `json:"status"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// AttachmentRejection 是单个文件被拒的**结构化**原因。
//
// 一个坏文件不能静默丢掉其余文件，也不能让整批失败：
// 调用方要能逐条告诉用户"这个为什么没进来"。
type AttachmentRejection struct {
	Path        string `json:"path"`
	DisplayName string `json:"displayName"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

// AttachmentResult 是一批附件选择的结果：Items + Rejected 共同构成完整答案。
type AttachmentResult struct {
	SessionID string                `json:"sessionId"`
	Items     []AttachmentMeta      `json:"items"`
	Rejected  []AttachmentRejection `json:"rejected"`
}

// attachmentCapability 是一条只读读取许可。
//
// 它绑定 (sha256, size)：ID 与内容一一对应，读的时候再校验一次，
// 因此"换掉 blob"不会让旧能力读到新内容，只会让旧能力失效。
type attachmentCapability struct {
	id          string
	sessionID   string
	sha256      string
	size        int64
	mediaType   string
	displayName string
	expiresAt   time.Time
}

func (c attachmentCapability) meta() AttachmentMeta {
	return AttachmentMeta{
		ID:          c.id,
		DisplayName: c.displayName,
		Size:        c.size,
		SHA256:      c.sha256,
		MediaType:   c.mediaType,
		Status:      AttachmentReady,
		ExpiresAt:   c.expiresAt,
	}
}

// AttachmentStore 是 app 托管的 content-addressed blob store 与只读 broker。
//
// 并发安全：能力登记表与 blob 发布在同一把锁下；校验/读取在锁外，
// 靠 sha256 重新校验而不是靠"没人会改"，避免把整批读串行化。
type AttachmentStore struct {
	cat      *Catalog
	blobRoot string

	mu          sync.Mutex
	caps        map[string]attachmentCapability
	now         func() time.Time
	ttl         time.Duration
	maxBytes    int64
	sourceGuard *policy.PathGuard
}

// NewAttachmentStore 构造附件存储。
//
// cat 用来确认"这个 session 存在且是普通会话"——附件归属会话，不允许挂到
// 自动化/调度会话上。blobRoot 由绑定层给出（生产为 <appRoot>/blobs），
// 必须与 run 的 workspace 不同（绑定层负责证明这一点）。
func NewAttachmentStore(cat *Catalog, blobRoot string) (*AttachmentStore, error) {
	if cat == nil {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "附件存储缺少会话目录句柄", false)
	}
	if strings.TrimSpace(blobRoot) == "" {
		return nil, contracts.NewAppError(contracts.CodeConfigMissing, "附件 blob 根目录为空", false)
	}
	abs, err := filepath.Abs(blobRoot)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "附件 blob 根目录无法绝对化: "+err.Error(), false)
	}
	abs = filepath.Clean(abs)
	if err := os.MkdirAll(filepath.Join(abs, blobTempDirName), 0o700); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "创建附件 blob 目录失败: "+abs+" : "+err.Error(), false)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, contracts.NewAppError(contracts.CodeInternal, "附件 blob 根不是目录: "+abs, false)
	}
	return &AttachmentStore{
		cat:      cat,
		blobRoot: abs,
		caps:     map[string]attachmentCapability{},
		now:      time.Now,
		ttl:      DefaultCapabilityTTL,
		maxBytes: DefaultMaxAttachmentBytes,
	}, nil
}

// BlobRoot 返回 blob store 的根（只读定位，供诊断与验收断言使用）。
func (s *AttachmentStore) BlobRoot() string { return s.blobRoot }

// BlobPath 返回某个 sha256 对应的 blob 路径。
func (s *AttachmentStore) BlobPath(sha string) string { return s.blobPath(sha) }

// SetSourceRoots 限定"允许选择附件的来源根"。
//
// 普通会话由原生选择器决定来源范围，可以不设根；项目会话与任何需要"只能从
// 这个目录里选"的场景必须设根——越界一律 UNAUTHORIZED。判定直接复用
// internal/policy 的 PathGuard（同一套链接解析与越界语义，不另写一套）。
func (s *AttachmentStore) SetSourceRoots(roots []string) error {
	if len(roots) == 0 {
		s.sourceGuard = nil
		return nil
	}
	guard, err := policy.NewPathGuard(roots...)
	if err != nil {
		return err
	}
	s.sourceGuard = guard
	return nil
}

// SetClock 注入时钟，仅用于测试确定性与能力过期。
func (s *AttachmentStore) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// SetCapabilityTTL 覆盖能力默认有效期（测试与将来的会话级策略）。
func (s *AttachmentStore) SetCapabilityTTL(ttl time.Duration) {
	if ttl > 0 {
		s.ttl = ttl
	}
}

// SetMaxAttachmentBytes 覆盖单附件大小上限。
func (s *AttachmentStore) SetMaxAttachmentBytes(n int64) {
	if n > 0 {
		s.maxBytes = n
	}
}

// ResolveAttachments 接收一批附件选择。
//
// 顶层 error 只用于"整批无法处理"（会话不存在/不是普通会话/存储不可用）；
// 单个文件的问题进 Rejected，不让一个坏文件把其余文件一起拖死。
func (s *AttachmentStore) ResolveAttachments(sessionID string, reqs []AttachmentRequest) (AttachmentResult, error) {
	result := AttachmentResult{Items: []AttachmentMeta{}, Rejected: []AttachmentRejection{}}
	if s == nil {
		return result, contracts.NewAppError(contracts.CodeInternal, "附件存储未初始化", false)
	}
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return result, contracts.NewAppError(contracts.CodeConfigMissing, "缺少会话 ID，无法接收附件", false)
	}
	sess, err := s.cat.GetSession(context.Background(), contracts.SessionID(sid))
	if err != nil {
		return result, err
	}
	if sess.Kind != KindChat {
		return result, contracts.NewAppError(contracts.CodeUnsupported,
			"附件只支持普通会话；会话 "+sid+" 的类型是 "+sess.Kind, false)
	}
	result.SessionID = sid

	s.sweepExpired()
	for _, req := range reqs {
		meta, aerr := s.accept(sid, req)
		if aerr != nil {
			result.Rejected = append(result.Rejected, rejectionFor(req, aerr))
			continue
		}
		result.Items = append(result.Items, meta)
	}
	return result, nil
}

// Lookup 返回一个附件的元数据（不交付字节、不产生新能力）。
//
// 它给绑定层用：UI 选中附件后拿到的是能力 ID，把 ID 冻进 run 之前必须重新确认
// “这个 ID 仍然有效”。过期/不存在的 ID 一律结构化失败，不允许把“已经不存在的
// 引用”带进 run（模型只会拿到一个永远读不到的工具）。
func (s *AttachmentStore) Lookup(id string) (AttachmentMeta, error) {
	if s == nil {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal, "附件存储未初始化", false)
	}
	key := strings.TrimSpace(id)
	if key == "" {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeNotFound, "缺少附件 ID", false)
	}
	s.mu.Lock()
	cap, ok := s.caps[key]
	s.mu.Unlock()
	if !ok {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeNotFound,
			"附件能力不存在（可能已过期，或应用重启后未重新选择）: "+key, false)
	}
	meta := cap.meta()
	if !s.now().UTC().Before(cap.expiresAt) {
		return meta, contracts.NewAppError(contracts.CodeUnauthorized,
			"附件读取能力已过期，请重新选择文件: "+cap.displayName, false)
	}
	return meta, nil
}

// OpenAttachment 是只读 broker：只有通过校验的字节才会被交出去。
//
// 顺序（任何一步失败都返回 nil 句柄）：
//  1. 能力必须存在且未过期；
//  2. blob 必须存在，大小与登记一致——stat 与读取都在**同一个打开句柄**上；
//  3. 读出的字节必须恰好是登记的长度，且 sha256 与登记一致；
//  4. 返回的是**已校验字节**的副本，不是文件句柄：校验之后文件再被改/截断，
//     也不会把未校验的内容交出去。
func (s *AttachmentStore) OpenAttachment(id string) (io.ReadCloser, AttachmentMeta, error) {
	if s == nil {
		return nil, AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal, "附件存储未初始化", false)
	}
	key := strings.TrimSpace(id)
	if key == "" {
		return nil, AttachmentMeta{}, contracts.NewAppError(contracts.CodeNotFound, "缺少附件 ID", false)
	}
	s.mu.Lock()
	cap, ok := s.caps[key]
	s.mu.Unlock()
	if !ok {
		return nil, AttachmentMeta{}, contracts.NewAppError(contracts.CodeNotFound,
			"附件能力不存在（可能已过期，或应用重启后未重新选择）: "+key, false)
	}
	meta := cap.meta()
	if !s.now().UTC().Before(cap.expiresAt) {
		return nil, meta, contracts.NewAppError(contracts.CodeUnauthorized,
			"附件读取能力已过期，请重新选择文件: "+cap.displayName, false)
	}

	blobPath := s.blobPath(cap.sha256)
	f, err := os.Open(blobPath)
	if err != nil {
		return nil, meta, verificationFailed("附件内容已不可用: "+cap.displayName+" : "+err.Error(), true)
	}
	defer func() { _ = f.Close() }()

	// stat 与读取用同一个句柄：size 不是"路径现在的 size"，而是这次打开看到的 size。
	fi, err := f.Stat()
	if err != nil {
		return nil, meta, verificationFailed("附件内容无法校验: "+cap.displayName+" : "+err.Error(), true)
	}
	if !fi.Mode().IsRegular() {
		return nil, meta, verificationFailed("附件内容不是普通文件: "+cap.displayName, false)
	}
	if fi.Size() != cap.size {
		return nil, meta, verificationFailed(fmt.Sprintf(
			"附件大小已变化（登记 %d，实际 %d），拒绝交付: %s", cap.size, fi.Size(), cap.displayName), false)
	}
	data, err := io.ReadAll(io.LimitReader(f, cap.size+1))
	if err != nil {
		return nil, meta, verificationFailed("读取附件内容失败: "+cap.displayName+" : "+err.Error(), true)
	}
	if int64(len(data)) != cap.size {
		return nil, meta, verificationFailed(fmt.Sprintf(
			"附件长度与登记不一致（登记 %d，实际 %d），拒绝交付: %s", cap.size, len(data), cap.displayName), false)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != cap.sha256 {
		return nil, meta, verificationFailed("附件内容校验失败（sha256 不符），拒绝交付: "+cap.displayName, false)
	}
	return io.NopCloser(bytes.NewReader(data)), meta, nil
}

// accept 处理单个文件：校验路径 → 读源文件 → 落地 blob → 登记能力。
func (s *AttachmentStore) accept(sessionID string, req AttachmentRequest) (AttachmentMeta, error) {
	realPath, displayName, err := s.checkSource(req)
	if err != nil {
		return AttachmentMeta{}, err
	}
	return s.storeBlob(sessionID, realPath, displayName)
}

// checkSource 把用户给的字符串重新解析成"确实是一个普通文件"。
//
// 拒绝顺序是刻意的：先词法（不依赖文件系统）、再链接（不跟随）、最后 stat。
// 任何一步都在读取字节之前完成，所以被拒的文件不会产生 blob。
func (s *AttachmentStore) checkSource(req AttachmentRequest) (string, string, error) {
	raw := strings.TrimSpace(req.Path)
	if raw == "" {
		return "", "", contracts.NewAppError(contracts.CodeConfigMissing, "附件路径为空", false)
	}
	if strings.ContainsRune(raw, 0) {
		return "", "", contracts.NewAppError(contracts.CodeUnauthorized, "附件路径包含 NUL 字节", false)
	}
	if err := lexicalRejectAttachmentPath(raw); err != nil {
		return "", "", err
	}
	if hasDotDotSegment(raw) {
		return "", "", contracts.NewAppError(contracts.CodeUnauthorized,
			"附件路径不允许用 .. 拼接（请使用选择器给出的真实路径）: "+raw, false)
	}
	if !filepath.IsAbs(raw) {
		return "", "", contracts.NewAppError(contracts.CodeUnauthorized,
			"附件路径必须是绝对路径: "+raw, false)
	}
	abs := filepath.Clean(raw)

	// 链接一律拒绝：附件是"用户看见的那个文件"的快照，任何链接（符号链接、
	// Windows 目录联接）都让"看见的路径"与"实际资源"脱钩。比 PathGuard 的
	// "解析后仍在根内就放行"更严，因为附件不需要通过链接选择文件。
	if link, err := firstLinkComponent(abs); err != nil {
		return "", "", contracts.NewAppError(contracts.CodeUnauthorized,
			"附件路径无法解析: "+abs+" : "+err.Error(), false)
	} else if link != "" {
		return "", "", contracts.NewAppError(contracts.CodeUnauthorized,
			"附件路径经过符号链接/目录联接，拒绝接收: "+link, false)
	}
	if s.sourceGuard != nil {
		dec, err := s.sourceGuard.Check(abs)
		if err != nil {
			return "", "", err
		}
		abs = dec.Real
	}

	fi, err := os.Stat(abs)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return "", "", contracts.NewAppError(contracts.CodeNotFound, "附件不存在: "+abs, false)
	case errors.Is(err, fs.ErrPermission):
		return "", "", contracts.NewAppError(contracts.CodeUnauthorized, "无权读取附件: "+abs, false)
	default:
		return "", "", contracts.NewAppError(contracts.CodeUnauthorized,
			"附件不可访问: "+abs+"（"+err.Error()+"）", false)
	}
	if fi.IsDir() {
		return "", "", contracts.NewAppError(contracts.CodeUnsupported, "选择的是目录，不是文件: "+abs, false)
	}
	if !fi.Mode().IsRegular() {
		return "", "", contracts.NewAppError(contracts.CodeUnsupported,
			"不是普通文件（管道/设备等不能作为附件）: "+abs, false)
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = filepath.Base(abs)
	}
	return abs, displayName, nil
}

// storeBlob 把源文件复制进 blob store 并登记能力。
//
// 源文件只以只读方式打开，整个函数没有任何写源文件的路径。
func (s *AttachmentStore) storeBlob(sessionID, srcPath, displayName string) (AttachmentMeta, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeUnauthorized,
			"打开附件失败: "+srcPath+" : "+err.Error(), false)
	}
	defer func() { _ = src.Close() }()

	// size 取自这个打开句柄，不是路径：判断与读取必须是同一份事实。
	fi, err := src.Stat()
	if err != nil {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal,
			"读取附件元数据失败: "+srcPath+" : "+err.Error(), false)
	}
	if !fi.Mode().IsRegular() {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeUnsupported,
			"不是普通文件: "+srcPath, false)
	}
	if fi.Size() > s.maxBytes {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
			"附件超过大小上限（%d > %d 字节）: %s", fi.Size(), s.maxBytes, srcPath), false)
	}

	tmp, err := os.CreateTemp(filepath.Join(s.blobRoot, blobTempDirName), "att-*.tmp")
	if err != nil {
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal,
			"创建附件临时文件失败: "+err.Error(), false)
	}
	tmpPath := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hasher := sha256.New()
	head := make([]byte, 0, mediaTypeSniffLen)
	buf := make([]byte, 128*1024)
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if len(head) < mediaTypeSniffLen {
				want := mediaTypeSniffLen - len(head)
				if n < want {
					want = n
				}
				head = append(head, buf[:want]...)
			}
			total += int64(n)
			if total > s.maxBytes {
				discard()
				return AttachmentMeta{}, contracts.NewAppError(contracts.CodeUnsupported, fmt.Sprintf(
					"附件超过大小上限（> %d 字节）: %s", s.maxBytes, srcPath), false)
			}
			if _, werr := hasher.Write(buf[:n]); werr != nil {
				discard()
				return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal,
					"附件哈希计算失败: "+werr.Error(), false)
			}
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				discard()
				return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal,
					"写入附件临时文件失败: "+werr.Error(), false)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			discard()
			return AttachmentMeta{}, contracts.NewAppError(contracts.CodeUnauthorized,
				"读取附件失败: "+srcPath+" : "+rerr.Error(), false)
		}
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal,
			"刷新附件临时文件失败: "+err.Error(), false)
	}
	if err := tmp.Close(); err != nil {
		discard()
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal,
			"关闭附件临时文件失败: "+err.Error(), false)
	}

	sum := hasher.Sum(nil)
	sha := hex.EncodeToString(sum)
	if err := s.publish(tmpPath, s.blobPath(sha), sha, total); err != nil {
		_ = os.Remove(tmpPath)
		return AttachmentMeta{}, contracts.NewAppError(contracts.CodeInternal,
			"落地附件内容失败: "+err.Error(), false)
	}

	id, err := newAttachmentID()
	if err != nil {
		return AttachmentMeta{}, err
	}
	cap := attachmentCapability{
		id:          id,
		sessionID:   sessionID,
		sha256:      sha,
		size:        total,
		mediaType:   mediaTypeFor(displayName, head),
		displayName: displayName,
		expiresAt:   s.now().UTC().Add(s.ttl),
	}
	s.mu.Lock()
	s.caps[id] = cap
	s.mu.Unlock()
	return cap.meta(), nil
}

// publish 把临时文件发布成内容地址，并保证"同 hash 的既有 blob 先校验再复用"。
//
// 既有 blob 校验不过（被外部改坏）时不是"报错走开"，而是用这次刚写好的、
// 已算过 hash 的副本替换它：否则一个坏 blob 会让这份内容永远无法再被选为附件。
func (s *AttachmentStore) publish(tmpPath, dst, sha string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(dst); err == nil {
		if verifyBlob(dst, sha, size) == nil {
			return os.Remove(tmpPath)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

// sweepExpired 清掉已过期的能力，避免登记表随使用无限增长。
// 清掉只影响"还能不能读"，不影响 blob（内容寻址，可能仍被别的能力引用）。
func (s *AttachmentStore) sweepExpired() {
	now := s.now().UTC()
	s.mu.Lock()
	for id, cap := range s.caps {
		if !now.Before(cap.expiresAt) {
			delete(s.caps, id)
		}
	}
	s.mu.Unlock()
}

func (s *AttachmentStore) blobPath(sha string) string {
	if len(sha) < 2 {
		return filepath.Join(s.blobRoot, "invalid", sha)
	}
	return filepath.Join(s.blobRoot, sha[:2], sha)
}

// verifyBlob 在同一打开句柄上校验 size 与 sha256。
func verifyBlob(path, wantSHA string, wantSize int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat() // 与读取同一个句柄
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errBlockMismatch
	}
	if fi.Size() != wantSize {
		return errBlockMismatch
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, wantSize+1))
	if err != nil {
		return err
	}
	if n != wantSize || hex.EncodeToString(h.Sum(nil)) != wantSHA {
		return errBlockMismatch
	}
	return nil
}

var errBlockMismatch = errors.New("blob 内容与登记不一致")

func verificationFailed(msg string, retryable bool) error {
	return contracts.NewAppError(contracts.CodeVerificationFailed, msg, retryable)
}

func rejectionFor(req AttachmentRequest, err error) AttachmentRejection {
	out := AttachmentRejection{Path: req.Path, DisplayName: strings.TrimSpace(req.DisplayName)}
	var appErr *contracts.AppError
	if errors.As(err, &appErr) {
		out.Code = appErr.Code
		out.Message = appErr.Message
		return out
	}
	out.Code = contracts.CodeInternal
	out.Message = err.Error()
	return out
}

// mediaTypeFor 先按扩展名，再按内容嗅探；两者都失败时给二进制兜底。
// 它只用于展示与预览白名单，不参与任何安全判定。
func mediaTypeFor(name string, head []byte) string {
	if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); mt != "" {
		return mt
	}
	if len(head) > 0 {
		return http.DetectContentType(head)
	}
	return "application/octet-stream"
}

// hasDotDotSegment 判断路径里是否出现 `..` 段（Clean 之前判断）。
func hasDotDotSegment(raw string) bool {
	segs := strings.FieldsFunc(raw, func(r rune) bool { return r == '/' || r == '\\' })
	for _, seg := range segs {
		if seg == ".." {
			return true
		}
	}
	return false
}

// lexicalRejectAttachmentPath 是纯词法拒绝，必须在解析之前执行。
//
// 与 internal/policy/pathscope.go 的 lexicalReject 同源（UNC/设备路径、驱动器
// 相对路径、备用数据流）。policy 没有导出它，而 C06c 不改 policy，所以这里保留
// 同一组规则；两处若将来分叉，以 policy 的版本为准并在对应任务里收敛。
func lexicalRejectAttachmentPath(raw string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, "//") {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"附件不允许 UNC/设备路径: "+raw, false)
	}
	if len(raw) >= 2 && raw[1] == ':' {
		if len(raw) == 2 || (raw[2] != '\\' && raw[2] != '/') {
			return contracts.NewAppError(contracts.CodeUnauthorized,
				"附件不允许驱动器相对路径: "+raw, false)
		}
		for _, seg := range strings.Split(raw[2:], `\`) {
			if strings.Contains(seg, ":") {
				return contracts.NewAppError(contracts.CodeUnauthorized,
					"附件不允许备用数据流路径: "+raw, false)
			}
		}
	}
	return nil
}

// firstLinkComponent 返回路径中第一个符号链接/目录联接的组件；没有则返回空。
//
// 逐段 Lstat（不跟随）而不是"解析后比较字符串"：Windows 上 t.TempDir() 可能
// 带 8.3 短名（ADMINI~1），真实路径却是长名，字符串比较会把所有正常文件误判成
// 链接。逐段判断只看"这一层是不是 reparse point"，与短名/大小写无关。
//
// 目录联接在 Go 的 Lstat 里可能没有 ModeSymlink，但 Readlink 一定能读出目标，
// 所以对每个非 symlink 的条目也试探一次 Readlink（与 policy 的做法一致）。
func firstLinkComponent(abs string) (string, error) {
	vol := filepath.VolumeName(abs)
	rest := strings.Trim(strings.TrimPrefix(abs, vol), `/\`)
	cur := vol + string(filepath.Separator)
	if vol == "" {
		cur = string(filepath.Separator)
	}
	if rest == "" {
		return "", nil
	}
	for _, seg := range strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' }) {
		next := filepath.Join(cur, seg)
		fi, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// 剩余部分不存在：交给后续 Stat 给出 NOT_FOUND，
				// 这里不能因为"还没走到"就放行成链接。
				return "", nil
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return next, nil
		}
		if target, rerr := os.Readlink(next); rerr == nil && target != "" {
			return next, nil
		}
		cur = next
	}
	return "", nil
}

func newAttachmentID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", contracts.NewAppError(contracts.CodeInternal, "生成附件 ID 失败: "+err.Error(), false)
	}
	return "att-" + hex.EncodeToString(buf), nil
}
