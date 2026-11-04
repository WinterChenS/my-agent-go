// Package profile 管理会话配置档（C01）。
//
// Profile 只保存"引用与策略"，不保存任何秘密：模型密钥属于凭证存储（F07），
// profile 里只出现 credentialRef 这种指针。这条边界不是风格问题——
// profile 是可以被导出、被日志打印、被贴进 issue 的普通 JSON。
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// ToolPolicy 是 profile 对工具能力的收紧要求。
//
// 与上层是"只能收紧"关系：这里写 true 不会把上层的 false 放开，
// 只有上层本来就允许时才生效（合并由 config.Merge 负责）。
type ToolPolicy struct {
	// Filesystem 取值必须是 config.NormalizeFilesystem 认可的
	// none / read / workspace / unrestricted。保存时归一，不合法直接拒绝——
	// 放过一个不认识的取值等于让它静默降级成 none。
	Filesystem string `json:"filesystem"`
	Network    bool   `json:"network"`
	Shell      bool   `json:"shell"`
}

// MemoryPolicy 是本 profile 的记忆策略。
type MemoryPolicy struct {
	Enabled bool `json:"enabled"`
	// MaxEntries 为 0 表示不额外限制（沿用上层）。
	MaxEntries int `json:"maxEntries"`
}

// Profile 是对外的 profile 形态。
type Profile struct {
	ID           contracts.ProfileID `json:"id"`
	Name         string              `json:"name"`
	ModelRef     string              `json:"modelRef"`
	SkillIDs     []string            `json:"skillIDs"`
	MCPIDs       []string            `json:"mcpIDs"`
	ToolPolicy   ToolPolicy          `json:"toolPolicy"`
	MemoryPolicy MemoryPolicy        `json:"memoryPolicy"`
	// Revision 是读取时的版本；保存时用 expectedRevision 传入，不从本字段回填。
	Revision uint64 `json:"revision"`
}

// Service 提供 profile 的读写。
type Service struct {
	store *config.Store
}

// New 构造 profile 服务。
func New(store *config.Store) *Service {
	return &Service{store: store}
}

// Save 以 CAS 保存 profile。
//
// expectedRevision 由调用方（UI 读到的版本）提供，不复用 body 里的字段：
// 让"我基于哪个版本改的"与"我要写什么"成为两个独立输入，
// 否则一次序列化往返就可能把过期版本伪装成当前版本。
func (s *Service) Save(ctx context.Context, id contracts.ProfileID, wsID contracts.WorkspaceID, expectedRevision uint64, p Profile) (Profile, error) {
	if strings.TrimSpace(string(id)) == "" {
		return Profile{}, contracts.NewAppError(contracts.CodeConfigMissing, "profile ID 为空", false)
	}
	p.ID = id
	p.Revision = 0 // 由存储层决定，不接受调用方指定

	if err := validateShape(&p); err != nil {
		return Profile{}, err
	}
	if err := s.checkReferences(ctx, wsID, p); err != nil {
		return Profile{}, err
	}

	body, err := buildLayerBody(p)
	if err != nil {
		return Profile{}, err
	}
	doc, err := s.store.CompareAndSwap(ctx, config.ProfileKey(id), expectedRevision, body)
	if err != nil {
		return Profile{}, err
	}
	p.Revision = doc.Revision
	return p, nil
}

// Get 读取 profile。
func (s *Service) Get(ctx context.Context, id contracts.ProfileID) (Profile, error) {
	doc, err := s.store.Load(ctx, config.ProfileKey(id))
	if err != nil {
		return Profile{}, err
	}
	if doc.Revision == 0 && len(doc.Body) == 0 {
		return Profile{}, contracts.NewAppError(contracts.CodeNotFound, "profile 不存在: "+string(id), false)
	}
	p, err := decode(doc.Body)
	if err != nil {
		return Profile{}, err
	}
	p.Revision = doc.Revision
	return p, nil
}

// List 返回全部 profile。
func (s *Service) List(ctx context.Context) ([]Profile, error) {
	keys, err := s.store.ListKeys("config-profile-")
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(keys))
	for _, key := range keys {
		doc, err := s.store.Load(ctx, key)
		if err != nil {
			return nil, err
		}
		if doc.Revision == 0 && len(doc.Body) == 0 {
			continue
		}
		p, err := decode(doc.Body)
		if err != nil {
			return nil, err
		}
		p.Revision = doc.Revision
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AssertRunnable 在启动 run 之前做第二道检查。
//
// 保存时的校验只能保证"当时引用的资源存在"。资源可能在之后被删除或禁用，
// 所以启动前必须再看一遍：拒绝启动要给出可修复的原因，而不是让 Pi
// 抛一个 Unknown provider 之后由用户自己猜。
//
// 判定基于**不含本 profile** 的下层配置：profile 不能自己定义模型，
// 否则"引用的模型存在"就成了自证（自己写一条再引用它）。
func (s *Service) AssertRunnable(ctx context.Context, id contracts.ProfileID, wsID contracts.WorkspaceID) error {
	p, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	lower, err := s.lowerLayer(ctx, wsID)
	if err != nil {
		return err
	}
	enabled, ok := lower.models[p.ModelRef]
	if !ok {
		return contracts.NewAppError(
			contracts.CodeDependencyMissing,
			"profile「"+p.Name+"」引用的模型 "+p.ModelRef+" 不存在，无法启动；请先在资源库中定义该模型或改选其它模型",
			false,
		)
	}
	if !enabled {
		return contracts.NewAppError(
			contracts.CodeDependencyMissing,
			"profile「"+p.Name+"」引用的模型 "+p.ModelRef+" 已被禁用，无法启动；请启用该模型或改选其它模型",
			false,
		)
	}
	return nil
}

// lowerLayer 是下层（默认/用户/工作区）配置里已定义资源的快照。
type lowerLayer struct {
	// models 记录 id → 是否启用（未写 enabled 视为启用）。
	models map[string]bool
	skills map[string]bool
	mcp    map[string]bool
}

func (s *Service) lowerLayer(ctx context.Context, wsID contracts.WorkspaceID) (lowerLayer, error) {
	eff, err := s.store.Compile(ctx, config.CompileRequest{WorkspaceID: wsID, ProfileID: ""})
	if err != nil {
		return lowerLayer{}, err
	}
	var values struct {
		Models []struct {
			ID      string `json:"id"`
			Enabled *bool  `json:"enabled"`
		} `json:"models"`
		Skills []struct {
			ID string `json:"id"`
		} `json:"skills"`
		MCP []struct {
			ID string `json:"id"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(eff.Values, &values); err != nil {
		return lowerLayer{}, contracts.NewAppError(contracts.CodeInternal, "解析有效配置失败: "+err.Error(), false)
	}
	out := lowerLayer{
		models: map[string]bool{},
		skills: map[string]bool{},
		mcp:    map[string]bool{},
	}
	for _, m := range values.Models {
		out.models[m.ID] = m.Enabled == nil || *m.Enabled
	}
	for _, v := range values.Skills {
		out.skills[v.ID] = true
	}
	for _, v := range values.MCP {
		out.mcp[v.ID] = true
	}
	return out, nil
}

// validateShape 校验不依赖其它资源的字段。
func validateShape(p *Profile) error {
	if strings.TrimSpace(p.ModelRef) == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "profile 必须指定 modelRef", false)
	}
	normalized, ok := config.NormalizeFilesystem(p.ToolPolicy.Filesystem)
	if !ok {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"toolPolicy.filesystem 非法: "+p.ToolPolicy.Filesystem+"（允许 none/read/workspace/unrestricted）", false)
	}
	p.ToolPolicy.Filesystem = normalized
	if p.MemoryPolicy.MaxEntries < 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "memoryPolicy.maxEntries 不能为负", false)
	}
	// 空切片统一成 nil，避免 "[]" 与 null 在同一份配置里出现两种写法。
	if len(p.SkillIDs) == 0 {
		p.SkillIDs = nil
	}
	if len(p.MCPIDs) == 0 {
		p.MCPIDs = nil
	}
	return nil
}

// checkReferences 校验引用的资源 ID 在下层配置里确实存在。
//
// 只看 default/user/workspace 三层，不看本 profile：profile 不能自己定义
// 一个模型再引用它，那会让"资源库"与"引用"互相证明，等于没校验。
// 与 AssertRunnable 共用 lowerLayer，避免两处判定口径分叉。
func (s *Service) checkReferences(ctx context.Context, wsID contracts.WorkspaceID, p Profile) error {
	lower, err := s.lowerLayer(ctx, wsID)
	if err != nil {
		return err
	}
	if _, ok := lower.models[p.ModelRef]; !ok {
		return contracts.NewAppError(
			contracts.CodeDependencyMissing,
			"引用的模型 "+p.ModelRef+" 不存在；请先在资源库中定义它",
			false,
		)
	}
	if missing := missingIDs(p.SkillIDs, lower.skills); len(missing) > 0 {
		return contracts.NewAppError(
			contracts.CodeDependencyMissing,
			"引用的技能不存在: "+strings.Join(missing, ", "),
			false,
		)
	}
	if missing := missingIDs(p.MCPIDs, lower.mcp); len(missing) > 0 {
		return contracts.NewAppError(
			contracts.CodeDependencyMissing,
			"引用的 MCP 服务不存在: "+strings.Join(missing, ", "),
			false,
		)
	}
	return nil
}

func missingIDs(want []string, have map[string]bool) []string {
	var missing []string
	for _, id := range want {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

// secretKeyPattern 命中形如密钥的字段名。
//
// 这是"按键名拦截"，不是内容扫描：它拦不住把密钥塞进 name 里这种绕法，
// 但能拦住所有正常的误用路径。真正的边界是凭证存储（F07），
// profile 里根本没有地方放秘密，这里只是把误用挡在写入之前。
var secretKeyPattern = regexp.MustCompile(`(?i)^(api[-_]?key|secret|token|password|passwd|authorization|bearer)$`)

// buildLayerBody 把 profile 编译成配置层 body。
//
// 同时写两份视图：
//   - profile 子对象：原始 DTO，UI 读回来不用反推
//   - permissions：config.Merge 实际消费的权限字段
//
// 刻意**不**把 modelRef/skillIDs/mcpIDs 展开成 models/skills/mcp 数组。
// 原因是 Merge 对同 ID 资源是"整项覆盖"（见 config.Resource 注释）：
// 若 profile 写一条 {id:"m-gpt", enabled:true}，下层的
// {id:"m-gpt", provider:"openai", baseUrl:"..."} 会被整条替换掉，
// 连接信息全部丢失，而 UI 上看起来只是"选了个模型"。
// 选择关系由 profile 自己的 modelRef 表达，运行期去下层配置解析实际参数。
//
// C03/C04 若需要 profile 级启停，得先让 Merge 支持"仅覆盖 enabled"，
// 否则同样会把下层 attrs 清空。
func buildLayerBody(p Profile) (json.RawMessage, error) {
	if err := rejectSecrets(p); err != nil {
		return nil, err
	}

	body := map[string]any{
		"schemaVersion": config.ConfigSchemaVersion,
		"profile":       p,
		"permissions": map[string]any{
			"filesystem": p.ToolPolicy.Filesystem,
			"network":    p.ToolPolicy.Network,
			"shell":      p.ToolPolicy.Shell,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化 profile 失败: "+err.Error(), false)
	}
	return raw, nil
}

// rejectSecrets 在序列化前拦掉密钥字段。
func rejectSecrets(p Profile) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "序列化 profile 失败: "+err.Error(), false)
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "解析 profile 失败: "+err.Error(), false)
	}
	if path, ok := findSecret(probe, "profile"); ok {
		return contracts.NewAppError(
			contracts.CodeUnauthorized,
			fmt.Sprintf("profile 不能保存密钥（发现字段 %s）；请改用 credentialRef 引用凭证存储中的条目", path),
			false,
		)
	}
	return nil
}

func findSecret(v any, path string) (string, bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			here := path + "." + k
			if secretKeyPattern.MatchString(k) {
				if s, ok := child.(string); !ok || strings.TrimSpace(s) != "" {
					return here, true
				}
			}
			if p, ok := findSecret(child, here); ok {
				return p, true
			}
		}
	case []any:
		for i, child := range t {
			if p, ok := findSecret(child, fmt.Sprintf("%s[%d]", path, i)); ok {
				return p, true
			}
		}
	}
	return "", false
}

// decode 从配置层 body 读回 profile。
func decode(body json.RawMessage) (Profile, error) {
	var parsed struct {
		Profile Profile `json:"profile"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Profile{}, contracts.NewAppError(contracts.CodeInternal, "profile 文档解析失败: "+err.Error(), false)
	}
	if parsed.Profile.ID == "" {
		return Profile{}, contracts.NewAppError(contracts.CodeInternal, "profile 文档缺少 profile 元数据", false)
	}
	return parsed.Profile, nil
}

// IsConflict 判断错误是否为配置冲突，供上层决定是否走"重新加载并展示差异"。
func IsConflict(err error) bool {
	var appErr *contracts.AppError
	return errors.As(err, &appErr) && appErr.Code == contracts.CodeConfigConflict
}
