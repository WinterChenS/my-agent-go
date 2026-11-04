// 扩展资源（C03 / RES04）。
//
// 分两类，规则完全不同：
//   - 系统扩展（本产品的策略扩展 harness-policy.ts）不可关闭。它负责"工具调用
//     执行前授权"；让它可关闭，等于给界面留一个把审批整个关掉的开关。
//   - 第三方可执行扩展默认不启用，必须逐条显式打开。启用即信任（它会在 Pi 进程
//     里执行任意代码），但这**不会**让它的工具调用免审批——审批由策略扩展统一
//     拦截，与某个扩展是否被信任无关。
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// 扩展来源。
const (
	OriginSystem = "system"
	OriginLocal  = "local"
)

// SystemExtensionID 是系统策略扩展的固定 ID。
//
// 固定值而不是从文件名推导：界面、配置与测试都引用同一个字符串，
// 换实现文件名时不会出现"界面能关、实际还在跑"的错位。
const SystemExtensionID = "harness-policy"

// Extension 是一条可管理的扩展。
type Extension struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	System bool   `json:"system"`
	// Executable 恒为 true：扩展本身就是会在 Pi 进程里执行的代码。
	Executable bool   `json:"executable"`
	Enabled    bool   `json:"enabled"`
	Origin     string `json:"origin"`
	// CanDisable 为 false 时界面不应提供关闭入口，绑定层也会拒绝。
	CanDisable bool `json:"canDisable"`
	// Note 说明这条扩展的信任边界，直接展示给用户。
	Note     string `json:"note"`
	Revision uint64 `json:"revision"`
	Hash     string `json:"hash"`
}

// ExtensionSet 是一次列举的结果。
type ExtensionSet struct {
	Items    []Extension `json:"items"`
	Revision uint64      `json:"revision"`
}

// ExtensionPlan 是交给运行时的扩展集合。
type ExtensionPlan struct {
	Paths  []string
	IDs    []string
	Digest string
}

// extensionEntry 是配置里保存的第三方扩展状态。
type extensionEntry struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// SetSystemExtension 刷新系统扩展路径。
//
// 路径来自仓库布局解析，理论上启动时就固定；保留这个入口是为了让"先列出
// 扩展、后解析到路径"的调用顺序也能拿到系统资源，而不是永久缺一条。
func (s *Service) SetSystemExtension(p string) {
	s.systemExtension = p
}

// ListExtensions 返回系统扩展与托管目录里的第三方扩展。
func (s *Service) ListExtensions(ctx context.Context) (ExtensionSet, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return ExtensionSet{}, err
	}
	entries, err := decodeExtensionEntries(doc.Body)
	if err != nil {
		return ExtensionSet{}, err
	}
	enabled := map[string]bool{}
	for _, e := range entries {
		enabled[e.ID] = e.Enabled
	}

	out := make([]Extension, 0, len(entries)+1)
	if p := strings.TrimSpace(s.systemExtension); p != "" {
		out = append(out, Extension{
			ID:         SystemExtensionID,
			Name:       "策略扩展（执行前授权）",
			Path:       p,
			System:     true,
			Executable: true,
			Enabled:    true,
			Origin:     OriginSystem,
			CanDisable: false,
			Note:       "系统资源，不可关闭。它负责工具调用的执行前授权；关掉它等于取消所有审批。",
			Revision:   doc.Revision,
			Hash:       fileHash(p),
		})
	}

	local, err := s.localExtensions()
	if err != nil {
		return ExtensionSet{}, err
	}
	for _, e := range local {
		e.Enabled = enabled[e.ID]
		e.Revision = doc.Revision
		e.Origin = OriginLocal
		e.CanDisable = true
		e.Note = "第三方可执行扩展：启用即信任（它会在 Pi 进程里执行代码），但不会让它的工具调用免审批。"
		out = append(out, e)
	}
	return ExtensionSet{Items: out, Revision: doc.Revision}, nil
}

// localExtensions 扫描托管扩展目录。
//
// 只认两种形态：单个 .ts/.js 文件，或含 index.ts/index.js 的目录。
// 不做递归猜测——"哪个文件是入口"必须由结构决定，而不是由我们挑一个看起来像的。
func (s *Service) localExtensions() ([]Extension, error) {
	dir := s.layout.ExtensionsDir()
	if strings.TrimSpace(s.layout.Root()) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取扩展目录失败: "+err.Error(), false)
	}
	out := make([]Extension, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.IsDir() {
			entry := ""
			for _, cand := range []string{"index.ts", "index.js"} {
				p := filepath.Join(full, cand)
				if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
					entry = p
					break
				}
			}
			if entry == "" {
				continue
			}
			out = append(out, Extension{
				ID:         sanitizeSegment(name),
				Name:       name,
				Path:       entry,
				Executable: true,
				Hash:       fileHash(entry),
			})
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".ts" && ext != ".js" {
			continue
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		out = append(out, Extension{
			ID:         sanitizeSegment(id),
			Name:       id,
			Path:       full,
			Executable: true,
			Hash:       fileHash(full),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// SetExtensionEnabled 启用/禁用一条第三方扩展（CAS）。
func (s *Service) SetExtensionEnabled(ctx context.Context, id string, expectedRevision uint64, enabled bool) (Extension, error) {
	if id == SystemExtensionID {
		// 拒绝而不是"静默忽略"：界面若误传，用户需要看到这是一条硬约束。
		return Extension{}, contracts.NewAppError(contracts.CodeUnsupported,
			"系统策略扩展不可关闭：它负责工具调用前的授权，关掉它等于取消所有审批", false)
	}
	local, err := s.localExtensions()
	if err != nil {
		return Extension{}, err
	}
	var target *Extension
	for i := range local {
		if local[i].ID == id {
			target = &local[i]
			break
		}
	}
	if target == nil {
		return Extension{}, contracts.NewAppError(contracts.CodeNotFound, "扩展不存在: "+id, false)
	}

	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return Extension{}, err
	}
	entries, err := decodeExtensionEntries(doc.Body)
	if err != nil {
		return Extension{}, err
	}
	replaced := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Enabled = enabled
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, extensionEntry{ID: id, Enabled: enabled})
	}
	if _, err := s.writeExtensionEntries(ctx, doc.Body, entries, expectedRevision); err != nil {
		return Extension{}, err
	}
	target.Enabled = enabled
	target.Origin = OriginLocal
	target.Revision = expectedRevision + 1
	target.CanDisable = true
	target.Note = "第三方可执行扩展：启用即信任（它会在 Pi 进程里执行代码），但不会让它的工具调用免审批。"
	return *target, nil
}

// CompileExtensions 计算本次运行要显式 -e 加载的扩展。
//
// 系统扩展永远在第一位：即使配置里没有任何第三方扩展，策略扩展也必须加载，
// 否则工具调用会在没有授权端点的情况下执行。
func (s *Service) CompileExtensions(ctx context.Context) (ExtensionPlan, error) {
	set, err := s.ListExtensions(ctx)
	if err != nil {
		return ExtensionPlan{}, err
	}
	plan := ExtensionPlan{}
	h := sha256.New()
	for _, e := range set.Items {
		if !e.Enabled {
			continue
		}
		if strings.TrimSpace(e.Path) == "" {
			continue
		}
		plan.Paths = append(plan.Paths, e.Path)
		plan.IDs = append(plan.IDs, e.ID)
		io.WriteString(h, e.ID+"\x00"+e.Hash+"\x00")
	}
	plan.Digest = hex.EncodeToString(h.Sum(nil))[:16]
	return plan, nil
}

func decodeExtensionEntries(body json.RawMessage) ([]extensionEntry, error) {
	raws, err := rawField(body, "extensions")
	if err != nil {
		return nil, err
	}
	out := make([]extensionEntry, 0, len(raws))
	for _, raw := range raws {
		var e extensionEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "扩展记录解析失败: "+err.Error(), false)
		}
		if strings.TrimSpace(e.ID) == "" {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Service) writeExtensionEntries(ctx context.Context, body json.RawMessage, entries []extensionEntry, expectedRevision uint64) (contracts.ConfigDocument, error) {
	items := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeInternal, "序列化扩展记录失败: "+err.Error(), false)
		}
		items = append(items, raw)
	}
	next, err := withField(body, "extensions", items)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	return s.store.CompareAndSwap(ctx, config.KeyUser, expectedRevision, next)
}

// fileHash 计算文件内容摘要；读不到时返回空串（不把读失败当成"没变化"）。
func fileHash(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
