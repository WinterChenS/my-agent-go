package config

import (
	"encoding/json"
	"sort"
	"strings"

	"my-agent-go/internal/contracts"
)

// LayerName 是配置层的标识，顺序即合并优先级（从低到高）。
type LayerName string

const (
	LayerDefault   LayerName = "default"
	LayerUser      LayerName = "user"
	LayerWorkspace LayerName = "workspace"
	LayerProfile   LayerName = "profile"
	LayerSession   LayerName = "session"
)

// LayeredPriority 返回合并优先级，数值越大越优先。
func LayeredPriority(n LayerName) int {
	switch n {
	case LayerDefault:
		return 0
	case LayerUser:
		return 1
	case LayerWorkspace:
		return 2
	case LayerProfile:
		return 3
	case LayerSession:
		return 4
	default:
		return -1
	}
}

// Layer 是一个待合并的配置层。Body 为 nil 表示该层没有覆盖。
type Layer struct {
	Name LayerName
	Body json.RawMessage
}

// Permissions 是有效权限。filesystem 取有序等级中最严者，network/shell 逐层取与。
//
// 基线 §5：权限不允许被项目层扩大。因此只有 default 层能确定基线，
// user/workspace/profile/session 只能收紧。
type Permissions struct {
	Filesystem string `json:"filesystem"` // none | read | workspace | unrestricted
	Network    bool   `json:"network"`
	Shell      bool   `json:"shell"`
}

// filesystemRank 给出文件系统权限的宽严序。未知值一律按 none 处理（安全默认）。
// NormalizeFilesystem 把权限等级名归一成规范形式。
//
// 这里同时是"合法取值"的唯一判定：调用方（Profile 校验、UI 下拉）必须用它，
// 不要各自维护一份枚举——两套枚举的实际后果是 UI 接受 "write"，
// 而 Merge 不认识就按最严格处理，配置静默变成 none。
func NormalizeFilesystem(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "unrestricted":
		return "unrestricted", true
	case "workspace":
		return "workspace", true
	case "read":
		return "read", true
	case "none", "":
		return "none", true
	default:
		return "", false
	}
}

func filesystemRank(v string) int {
	name, ok := NormalizeFilesystem(v)
	if !ok {
		return 0
	}
	switch name {
	case "unrestricted":
		return 3
	case "workspace":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

func filesystemName(rank int) string {
	switch rank {
	case 3:
		return "unrestricted"
	case 2:
		return "workspace"
	case 1:
		return "read"
	default:
		return "none"
	}
}

// Resource 是数组型资源的合并单位：按稳定资源 ID 合并，同 ID 高层覆盖整项。
//
// Attrs 保留 id/enabled 之外的字段，整项覆盖时一并替换——不做事后字段级合并，
// 否则"高层改一个字段"会与"高层禁用整项"混在一起，失去确定性。
type Resource struct {
	ID      string                     `json:"id"`
	Enabled *bool                      `json:"enabled,omitempty"`
	Attrs   map[string]json.RawMessage `json:"attrs,omitempty"`
}

// Values 是合并后的有效配置值。
type Values struct {
	Permissions   Permissions `json:"permissions"`
	CredentialRef string      `json:"credentialRef,omitempty"`
	Models        []Resource  `json:"models"`
	Skills        []Resource  `json:"skills"`
	MCP           []Resource  `json:"mcp"`
}

// Merged 是 Merge 的结果。
type Merged struct {
	Values     Values
	Provenance map[string]string
	Warnings   []string
}

// rawPermissions 用指针区分"未设置"与"显式设为零值"。
type rawPermissions struct {
	Filesystem *string `json:"filesystem"`
	Network    *bool   `json:"network"`
	Shell      *bool   `json:"shell"`
}

// rawBody 是配置 body 的宽松视图；未识别的顶层字段忽略（前向兼容）。
type rawBody struct {
	Permissions   *rawPermissions   `json:"permissions"`
	CredentialRef *string           `json:"credentialRef"`
	Models        []json.RawMessage `json:"models"`
	Skills        []json.RawMessage `json:"skills"`
	MCP           []json.RawMessage `json:"mcp"`
}

// Merge 按 默认→用户→工作区→Profile→会话覆盖 的顺序合并。
//
// 规则（基线 §5）：
//   - 数组型资源按稳定 ID 合并，同 ID 高层覆盖整项
//   - 显式 enabled=false 生效，且必须以显式禁用表达，不能用空数组表达禁用
//   - 权限只能收紧：filesystem 取更严等级，network/shell 取与
//   - credentialRef 只能由 default/user 层确定，项目层设置会被忽略并留痕
func Merge(layers []Layer) (Merged, error) {
	ordered := make([]Layer, len(layers))
	copy(ordered, layers)
	sort.SliceStable(ordered, func(i, j int) bool {
		return LayeredPriority(ordered[i].Name) < LayeredPriority(ordered[j].Name)
	})

	out := Merged{Provenance: map[string]string{}}
	// 无任何层设置时的安全基线：不给任何权限。
	out.Values.Permissions = Permissions{Filesystem: "none"}

	sets := map[string]*resourceSet{
		"models": newResourceSet("models"),
		"skills": newResourceSet("skills"),
		"mcp":    newResourceSet("mcp"),
	}

	// fsRank<0 表示还没有任何层显式设置过 filesystem；
	// netSet/shellSet 用来区分"第一次有人说话"与"后续层收紧"。
	fsRank := -1
	netSet, shellSet := false, false
	for _, layer := range ordered {
		if LayeredPriority(layer.Name) < 0 {
			return Merged{}, contracts.NewAppError(contracts.CodeUnsupported, "未知配置层: "+string(layer.Name), false)
		}
		if len(layer.Body) == 0 {
			continue
		}
		var rb rawBody
		if err := json.Unmarshal(layer.Body, &rb); err != nil {
			return Merged{}, contracts.NewAppError(
				contracts.CodeUnsupported,
				"配置层 "+string(layer.Name)+" 解析失败: "+err.Error(),
				false,
			)
		}

		// 权限合并的语义是"取所有显式设置里最严格的那个"，而不是
		// "先由 default 层定基线、后续层只能收紧"。
		//
		// 旧实现对 default 层做了特判，代价是：当 default 层没写 permissions 时，
		// 第一个写 permissions 的层会被当成"当前是 none"来收紧，于是
		// filesystem 无法从 none 升到 workspace，network/shell 的 true 被
		// false && true 永久锁死——用户在设置里打开的开关永远不生效。
		// 用"未设置"哨兵表达"还没有任何层说过话"，两种情况才能统一处理。
		if p := rb.Permissions; p != nil {
			if p.Filesystem != nil {
				if want := filesystemRank(*p.Filesystem); fsRank < 0 || want < fsRank {
					fsRank = want
					out.Values.Permissions.Filesystem = filesystemName(want)
					out.Provenance["permissions.filesystem"] = string(layer.Name)
				}
			}
			if p.Network != nil {
				if !netSet {
					netSet = true
					out.Values.Permissions.Network = *p.Network
					out.Provenance["permissions.network"] = string(layer.Name)
				} else if out.Values.Permissions.Network && !*p.Network {
					// 只有真正把值收紧时才改归属：来源应指向"决定结果的那一层"。
					out.Values.Permissions.Network = false
					out.Provenance["permissions.network"] = string(layer.Name)
				}
			}
			if p.Shell != nil {
				if !shellSet {
					shellSet = true
					out.Values.Permissions.Shell = *p.Shell
					out.Provenance["permissions.shell"] = string(layer.Name)
				} else if out.Values.Permissions.Shell && !*p.Shell {
					out.Values.Permissions.Shell = false
					out.Provenance["permissions.shell"] = string(layer.Name)
				}
			}
		}

		if rb.CredentialRef != nil {
			switch layer.Name {
			case LayerDefault, LayerUser:
				out.Values.CredentialRef = *rb.CredentialRef
				out.Provenance["credentialRef"] = string(layer.Name)
			default:
				// 项目层不得替换凭证来源：忽略并留痕，不静默。
				out.Warnings = append(out.Warnings,
					"配置层 "+string(layer.Name)+" 试图设置 credentialRef，已被忽略（凭证来源不允许被项目层替换）")
			}
		}

		for _, spec := range []struct {
			kind string
			raws []json.RawMessage
		}{
			{"models", rb.Models},
			{"skills", rb.Skills},
			{"mcp", rb.MCP},
		} {
			for _, raw := range spec.raws {
				res, err := parseResource(raw)
				if err != nil {
					return Merged{}, contracts.NewAppError(
						contracts.CodeUnsupported,
						"配置层 "+string(layer.Name)+" 的 "+spec.kind+" 资源非法: "+err.Error(),
						false,
					)
				}
				sets[spec.kind].put(res, layer.Name)
			}
		}
	}

	out.Values.Models = sets["models"].list()
	out.Values.Skills = sets["skills"].list()
	out.Values.MCP = sets["mcp"].list()
	for kind, set := range sets {
		for _, id := range set.order {
			out.Provenance[kind+"."+id] = string(set.provider[id])
		}
	}
	return out, nil
}

// parseResource 把一条资源解析为 Resource；id 必填。
func parseResource(raw json.RawMessage) (Resource, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Resource{}, err
	}
	idRaw, ok := fields["id"]
	if !ok {
		return Resource{}, contracts.NewAppError(contracts.CodeUnsupported, "资源缺少 id", false)
	}
	var id string
	if err := json.Unmarshal(idRaw, &id); err != nil || strings.TrimSpace(id) == "" {
		return Resource{}, contracts.NewAppError(contracts.CodeUnsupported, "资源 id 非法", false)
	}
	res := Resource{ID: id}
	if enRaw, ok := fields["enabled"]; ok {
		var enabled bool
		if err := json.Unmarshal(enRaw, &enabled); err != nil {
			return Resource{}, contracts.NewAppError(contracts.CodeUnsupported, "资源 "+id+" 的 enabled 不是布尔值", false)
		}
		res.Enabled = &enabled
	}
	attrs := map[string]json.RawMessage{}
	for k, v := range fields {
		if k == "id" || k == "enabled" {
			continue
		}
		attrs[k] = v
	}
	if len(attrs) > 0 {
		res.Attrs = attrs
	}
	return res, nil
}

// resourceSet 保持首次出现顺序，同 ID 覆盖整项。
type resourceSet struct {
	kind     string
	order    []string
	items    map[string]Resource
	provider map[string]LayerName
}

func newResourceSet(kind string) *resourceSet {
	return &resourceSet{kind: kind, items: map[string]Resource{}, provider: map[string]LayerName{}}
}

func (s *resourceSet) put(res Resource, layer LayerName) {
	if _, seen := s.items[res.ID]; !seen {
		s.order = append(s.order, res.ID)
	}
	s.items[res.ID] = res
	s.provider[res.ID] = layer
}

func (s *resourceSet) list() []Resource {
	out := make([]Resource, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.items[id])
	}
	return out
}
