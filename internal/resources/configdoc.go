package resources

// 本文件收纳"用户配置文档里某一类资源数组"的通用读写，以及产品管理目录的布局。
//
// 这些操作原本只有 models 一份（rawModels/withModels/upsertModel），C03 加入
// skills 与 extensions 之后如果各写一份，三份实现的"保留其它顶层字段"细节就会
// 慢慢分叉——最典型的后果是保存技能时把 models 抹掉。抽成一份并让 models 复用，
// 是让"新增一类资源"不再需要重写一遍正确性。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// Layout 描述产品托管资源的物理位置。
//
// 所有路径都从 app home（MYAGENT_HOME）派生，不落在用户的 ~/.pi 或工作区里：
// 这些目录由产品负责写入、覆盖、删除，混进用户自己的目录会让"删除技能"变成
// 一次不可预测的破坏性操作。
type Layout struct {
	root string
}

// NewLayout 构造目录布局。root 是 app home。
func NewLayout(root string) Layout {
	return Layout{root: filepath.Clean(root)}
}

// Root 返回 app home。
func (l Layout) Root() string { return l.root }

// UserSkillsDir 是用户级技能目录（跨工作区共用）。
func (l Layout) UserSkillsDir() string { return filepath.Join(l.root, "skills") }

// WorkspaceSkillsDir 是某一工作区的技能目录。
func (l Layout) WorkspaceSkillsDir(workspaceID string) string {
	return filepath.Join(l.root, "workspaces", sanitizeSegment(workspaceID), "skills")
}

// ExtensionsDir 是托管扩展目录。
func (l Layout) ExtensionsDir() string { return filepath.Join(l.root, "extensions") }

// safeSegment 只允许不会跳出父目录的字符。
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// sanitizeSegment 把可能来自用户输入的路径片段变安全。
//
// workspaceID 目前由内部生成，但它最终会出现在文件路径里；一旦某个调用方
// 传入 "../../x"，拼接出的目录就会跑到 app home 之外。这里用白名单而不是
// 事后检查：不能表达的路径比"检查了但漏了"更可靠。
func sanitizeSegment(v string) string {
	if safeSegment.MatchString(v) && v != "." && v != ".." {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	return "h-" + hex.EncodeToString(sum[:8])
}

// rawField 取出配置 body 里某个数组字段的原始元素。
func rawField(body json.RawMessage, field string) ([]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigConflict,
			"用户配置解析失败: "+err.Error(), false)
	}
	raw, ok := fields[field]
	if !ok {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigConflict,
			"用户配置的 "+field+" 不是数组: "+err.Error(), false)
	}
	return items, nil
}

// withField 把数组写回 body，保留其它顶层字段。
func withField(old json.RawMessage, field string, items []json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &fields); err != nil {
			return nil, contracts.NewAppError(contracts.CodeConfigConflict,
				"用户配置解析失败: "+err.Error(), false)
		}
	}
	if _, ok := fields["schemaVersion"]; !ok {
		v, err := json.Marshal(config.ConfigSchemaVersion)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "序列化 schemaVersion 失败: "+err.Error(), false)
		}
		fields["schemaVersion"] = v
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化 "+field+" 失败: "+err.Error(), false)
	}
	fields[field] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化用户配置失败: "+err.Error(), false)
	}
	return out, nil
}

// idOf 取一条原始资源的 id，用于按 ID 替换。
func idOf(raw json.RawMessage) string {
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v.ID
}

// indexOfID 返回指定 ID 在数组里的下标，找不到为 -1。
func indexOfID(items []json.RawMessage, id string) int {
	for i, raw := range items {
		if idOf(raw) == id {
			return i
		}
	}
	return -1
}

// upsertEncoded 在保留同层其它条目的前提下按 ID 写入一条已编码资源。
func upsertEncoded(old json.RawMessage, field, id string, encoded json.RawMessage) (json.RawMessage, error) {
	items, err := rawField(old, field)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(items)+1)
	replaced := false
	for _, raw := range items {
		if idOf(raw) == id {
			out = append(out, encoded)
			replaced = true
			continue
		}
		out = append(out, raw)
	}
	if !replaced {
		out = append(out, encoded)
	}
	return withField(old, field, out)
}

// removeEncoded 按 ID 删掉一条，返回新 body 与"是否真的删掉了"。
func removeEncoded(old json.RawMessage, field, id string) (json.RawMessage, bool, error) {
	items, err := rawField(old, field)
	if err != nil {
		return nil, false, err
	}
	kept := make([]json.RawMessage, 0, len(items))
	found := false
	for _, raw := range items {
		if idOf(raw) == id {
			found = true
			continue
		}
		kept = append(kept, raw)
	}
	if !found {
		return old, false, nil
	}
	body, err := withField(old, field, kept)
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}
