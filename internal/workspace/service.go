// Package workspace 管理用户添加的本地工作区（C01）。
//
// 权威边界：工作区元数据与它的配置层共用同一份版本化文档（config.Store），
// 不额外维护索引——索引与正文一旦不一致就是两份真相，得靠人工修复。
//
// 安全默认：新添加的工作区 Trusted=false。未信任时不启动任何可执行资源，
// 因为"用户选了目录"与"用户允许在此目录执行代码"是两件事。
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// Workspace 是一个被显式添加进产品的本地目录。
type Workspace struct {
	ID contracts.WorkspaceID `json:"id"`
	// Name 是展示名，默认取目录名；仅用于 UI，不参与任何路径判定。
	Name string `json:"name"`
	// CanonicalPath 是解析掉符号链接/junction 之后的真实绝对路径。
	// 所有越界判断都以它为准，不能用用户输入的原始字符串。
	CanonicalPath string    `json:"canonicalPath"`
	Trusted       bool      `json:"trusted"`
	CreatedAt     time.Time `json:"createdAt"`
}

const keyPrefix = "config-workspace-"

// Service 提供工作区的增删查与信任管理。
type Service struct {
	store *config.Store
	now   func() time.Time
}

// New 构造工作区服务。
func New(store *config.Store) *Service {
	return &Service{store: store, now: time.Now}
}

// SetClock 注入时钟，仅用于测试确定性。
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// CreateWorkspace 添加一个工作区；同一真实路径重复添加返回已有条目。
//
// 规范化顺序：Abs → Clean → Stat → EvalSymlinks。必须先解析链接再做去重，
// 否则同一个目录经由 symlink/junction 进来的两个路径会被当成两个工作区，
// 而它们的信任状态与配置层却是分开的——用户以为只信任了一个，实际裂成两份。
func (s *Service) CreateWorkspace(ctx context.Context, path string) (Workspace, error) {
	canonical, err := canonicalize(path)
	if err != nil {
		return Workspace{}, err
	}
	id := idFor(canonical)
	key := config.WorkspaceKey(id)

	existing, found, err := s.load(ctx, key)
	if err != nil {
		return Workspace{}, err
	}
	if found {
		// 同一 ID 必然对应同一去重键；若内容对不上，说明文件被外部改过，
		// 此时返回"已有"会让用户以为添加成功。宁可报冲突。
		if dedupeKey(existing.CanonicalPath) != dedupeKey(canonical) {
			return Workspace{}, contracts.NewAppError(
				contracts.CodeConfigConflict,
				"工作区 "+string(id)+" 的已存路径与本次不一致（"+existing.CanonicalPath+" ≠ "+canonical+"），拒绝覆盖",
				false,
			)
		}
		return existing, nil
	}

	ws := Workspace{
		ID:            id,
		Name:          filepath.Base(canonical),
		CanonicalPath: canonical,
		Trusted:       false,
		CreatedAt:     s.now().UTC().Truncate(time.Second),
	}
	body, err := json.Marshal(map[string]any{
		// schemaVersion 是 config 存储的强校验字段，缺了会被直接拒绝。
		"schemaVersion": config.ConfigSchemaVersion,
		"workspace":     ws,
	})
	if err != nil {
		return Workspace{}, contracts.NewAppError(contracts.CodeInternal, "序列化工作区失败: "+err.Error(), false)
	}
	if _, err := s.store.CompareAndSwap(ctx, key, 0, body); err != nil {
		// 并发添加同一路径：另一个 writer 已经写入，读回来即可，不算失败。
		if got, ok, lerr := s.load(ctx, key); lerr == nil && ok {
			return got, nil
		}
		return Workspace{}, err
	}
	return ws, nil
}

// List 返回全部工作区，按创建时间升序（先添加的在前）。
func (s *Service) List(ctx context.Context) ([]Workspace, error) {
	keys, err := s.store.ListKeys(keyPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(keys))
	for _, key := range keys {
		ws, found, err := s.load(ctx, key)
		if err != nil {
			// 不静默跳过：一个坏掉的工作区如果被吞掉，用户会觉得"我的项目不见了"。
			return nil, err
		}
		if found {
			out = append(out, ws)
		}
	}
	// 目录扫描按 key 字典序，这里按时间排，避免 UI 列表顺序随 ID 哈希跳动。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// Get 读取单个工作区。
func (s *Service) Get(ctx context.Context, id contracts.WorkspaceID) (Workspace, error) {
	ws, found, err := s.load(ctx, config.WorkspaceKey(id))
	if err != nil {
		return Workspace{}, err
	}
	if !found {
		return Workspace{}, contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+string(id), false)
	}
	return ws, nil
}

// SetTrust 设置信任状态。
//
// 走 CAS：并发的两次信任切换只有一个成功，另一个看到冲突——
// 不能让"用户点取消信任"被一个滞后的旧值覆盖回去。
func (s *Service) SetTrust(ctx context.Context, id contracts.WorkspaceID, trusted bool) error {
	key := config.WorkspaceKey(id)
	cur, err := s.store.Load(ctx, key)
	if err != nil {
		return err
	}
	if cur.Revision == 0 && len(cur.Body) == 0 {
		return contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+string(id), false)
	}
	ws, found, err := s.load(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		return contracts.NewAppError(contracts.CodeNotFound, "工作区不存在: "+string(id), false)
	}
	if ws.Trusted == trusted {
		return nil
	}
	ws.Trusted = trusted
	body, err := mergeBody(cur.Body, ws)
	if err != nil {
		return err
	}
	_, err = s.store.CompareAndSwap(ctx, key, cur.Revision, body)
	return err
}

// AssertRunnable 在启动 run 之前调用：未信任直接拒绝。
//
// 这条检查放在服务层而不是 UI 层，是因为 UI 可以被绕过（直接调绑定、
// 未来的 CLI、未来的恢复流程），而"不信任不执行"必须是数据层不变量。
func (s *Service) AssertRunnable(ctx context.Context, id contracts.WorkspaceID) error {
	ws, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ws.Trusted {
		return contracts.NewAppError(
			contracts.CodeUnauthorized,
			"工作区「"+ws.Name+"」尚未信任，不能在其中启动可执行资源",
			false,
		)
	}
	return nil
}

// load 读取并解析一份工作区文档。
func (s *Service) load(ctx context.Context, key string) (Workspace, bool, error) {
	d, err := s.store.Load(ctx, key)
	if err != nil {
		return Workspace{}, false, err
	}
	if d.Revision == 0 && len(d.Body) == 0 {
		return Workspace{}, false, nil
	}
	var parsed struct {
		Workspace Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(d.Body, &parsed); err != nil {
		return Workspace{}, false, contracts.NewAppError(
			contracts.CodeInternal, "工作区文档 "+key+" 解析失败: "+err.Error(), false,
		)
	}
	if parsed.Workspace.ID == "" {
		return Workspace{}, false, contracts.NewAppError(
			contracts.CodeInternal, "工作区文档 "+key+" 缺少 workspace 元数据", false,
		)
	}
	return parsed.Workspace, true, nil
}

// mergeBody 在保留同层其它配置字段的前提下替换 workspace 元数据。
//
// 不能整份重写成只有 workspace 的对象：那一层可能还有用户写的
// permissions/models 等配置，覆盖掉等于静默删除用户的配置。
func mergeBody(old json.RawMessage, ws Workspace) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &fields); err != nil {
			return nil, contracts.NewAppError(contracts.CodeConfigConflict, "工作区配置解析失败: "+err.Error(), false)
		}
	}
	if _, ok := fields["schemaVersion"]; !ok {
		v, err := json.Marshal(config.ConfigSchemaVersion)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "序列化 schemaVersion 失败: "+err.Error(), false)
		}
		fields["schemaVersion"] = v
	}
	meta, err := json.Marshal(ws)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化工作区失败: "+err.Error(), false)
	}
	fields["workspace"] = meta
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化工作区配置失败: "+err.Error(), false)
	}
	return out, nil
}

// canonicalize 把用户输入解析成真实绝对路径。
func canonicalize(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", contracts.NewAppError(contracts.CodeConfigMissing, "工作区路径为空", false)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "无法解析工作区路径: "+err.Error(), false)
	}
	cleaned := filepath.Clean(abs)

	info, err := os.Stat(cleaned)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return "", contracts.NewAppError(contracts.CodeNotFound, "目录不存在: "+cleaned, false)
	case errors.Is(err, fs.ErrPermission):
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "无权访问目录: "+cleaned, false)
	default:
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "目录不可访问: "+cleaned+"（"+err.Error()+"）", false)
	}
	if !info.IsDir() {
		return "", contracts.NewAppError(contracts.CodeUnsupported, "不是目录: "+cleaned, false)
	}

	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		// 解析不了真实路径就不能继续：越界判断与信任都基于真实路径，
		// 用"看起来对"的原样路径会给出虚假的安全感。
		return "", contracts.NewAppError(contracts.CodeUnauthorized, "无法解析目录真实路径: "+cleaned+"（"+err.Error()+"）", false)
	}
	return filepath.Clean(resolved), nil
}

// dedupeKey 是判断"两条路径是否指同一目录"的键。
//
// Windows 文件系统默认大小写不敏感：C:\Work 与 c:\work 是同一目录，
// 若按字节比较会造出两个工作区、两份信任状态。
func dedupeKey(canonical string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(canonical)
	}
	return canonical
}

// idFor 从去重键派生稳定 ID：同一真实路径恒得同一 ID。
//
// 用路径哈希而不是自增序号，是为了让"先加载还是先创建"不影响结果——
// 自增 ID 在并发或恢复场景下会分叉，而哈希不会。
func idFor(canonical string) contracts.WorkspaceID {
	sum := sha256.Sum256([]byte(dedupeKey(canonical)))
	return contracts.WorkspaceID("ws-" + hex.EncodeToString(sum[:])[:12])
}
