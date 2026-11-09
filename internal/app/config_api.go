package app

// C 系列（F07/C01）的绑定层：**可查询来源**的有效配置。
//
// 为什么需要它：`internal/config.Merge` 已经算出每一层对每个键的最终贡献
// （Provenance），但那只活在一次函数调用里。规格 §5 要求"五层合并正确且**可查询
// 来源**"——界面要能回答"这个模型/这条技能是哪一层设的"，出问题时也必须能导出
// 这份事实。没有落点，CFG01 只能靠进程内断言自证。
//
// 纪律：
//   - 这里只装配与转换，合并规则仍在 internal/config（不重写一遍）；
//   - 层文档来自版本化配置存储（磁盘上的文件），不来自内存；
//   - 该接口只读：不改配置、不建目录、不碰 Pi。

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// effectiveConfigKeys 是五层的配置存储 key（与 internal/config 的 LayerName 一一对应）。
//
// 键名与写入方约定：default/user 由应用写，workspace/profile/session 由对应层写。
// 缺哪一层就是"该层没说话"，不是错误。
var effectiveConfigLayers = []struct {
	Layer config.LayerName
	Key   string
}{
	{config.LayerDefault, "config.default"},
	{config.LayerUser, "config.user"},
	{config.LayerWorkspace, "config.workspace"},
	{config.LayerProfile, "config.profile"},
	{config.LayerSession, "config.session"},
}

// EffectiveConfigResult 是给界面的有效配置 + 来源。
type EffectiveConfigResult struct {
	Values     config.Values     `json:"values"`
	Provenance map[string]string `json:"provenance"`
	Warnings   []string          `json:"warnings,omitempty"`
	// Layers 只报告哪几层真的存在（磁盘上有文档），便于解释"为什么这个键是默认值"。
	Layers []string `json:"layers"`
	// Model 是**当前生效**的模型配置与每项来源（设置 / 环境变量 / 默认）。
	//
	// 它不来自五层配置：provider/model/baseUrl 这三个值住在 settings.json 与
	// 环境变量里（五层配置里只有 models 资源列表）。所以单独列一段，但来源的
	// 口径与层完全一致：界面必须能回答"这个模型值是谁给的"，环境变量补位
	// 不允许静默发生（OverriddenByEnv）。
	Model ModelConfig `json:"model"`
	Error *CallError  `json:"error,omitempty"`
}

// EffectiveConfig 读取并合并五层配置，返回可查询来源的结果。
func (d *Desktop) EffectiveConfig() EffectiveConfigResult {
	ctx := context.Background()
	// 模型来源取自**当前生效的设置**（内存里的那一份，与启动 run 用的是同一份）。
	// 不从磁盘现读：读路径算出的快照可能与已生效的设置不同（外部改文件），
	// 而这里回答的是"现在跑起来会用哪个模型、它是谁给的"。
	model := d.ModelConfig()
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return EffectiveConfigResult{Error: toCallError(err), Model: model}
	}

	layers := make([]config.Layer, 0, len(effectiveConfigLayers))
	present := make([]string, 0, len(effectiveConfigLayers))
	for _, l := range effectiveConfigLayers {
		doc, loadErr := store.Load(ctx, l.Key)
		if loadErr != nil {
			if isConfigNotFound(loadErr) {
				continue // 该层没有文档 = 没说话
			}
			return EffectiveConfigResult{Error: toCallError(loadErr), Model: model}
		}
		if configDocumentAbsent(doc.Revision, doc.Body) {
			continue
		}
		layers = append(layers, config.Layer{Name: l.Layer, Body: doc.Body})
		present = append(present, string(l.Layer))
	}
	if len(layers) == 0 {
		return EffectiveConfigResult{
			Error: toCallError(contracts.NewAppError(contracts.CodeConfigMissing,
				"没有任何配置层（default/user/workspace/profile/session 都没有文档）", false)),
			Layers: present,
			Model:  model,
		}
	}

	merged, mergeErr := config.Merge(layers)
	if mergeErr != nil {
		return EffectiveConfigResult{Error: toCallError(mergeErr), Layers: present, Model: model}
	}
	sort.Strings(present)
	return EffectiveConfigResult{
		Values:     merged.Values,
		Provenance: merged.Provenance,
		Warnings:   merged.Warnings,
		Layers:     present,
		Model:      model,
	}
}

// ModelConfig 返回当前生效模型配置的来源（设置 / 环境变量 / 默认值）。
//
// 导出给绑定层与诊断用：它不含任何密钥，只有值与来源（密钥只有引用）。
func (d *Desktop) ModelConfig() ModelConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.modelConfig
}

// SaveConfigLayer 写一层的配置文档（CAS）。
//
// 存在的理由：CFG01 的证据必须来自**磁盘上的层文档**，测试与界面都需要一条
// 正式的写入路径，而不是直接 WriteFile 绕过校验。expected=0 表示"这一层还没有"。
func (d *Desktop) SaveConfigLayer(layer string, expectedRevision uint64, body string) (uint64, *CallError) {
	name, key, ok := configLayerKey(layer)
	if !ok {
		return 0, toCallError(contracts.NewAppError(contracts.CodeConfigMissing,
			"未知配置层: "+layer+"（已知: default,user,workspace,profile,session）", false))
	}
	if !json.Valid([]byte(body)) {
		return 0, toCallError(contracts.NewAppError(contracts.CodeUnsupported,
			"配置层 "+string(name)+" 的 body 不是合法 JSON", false))
	}
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return 0, toCallError(err)
	}
	doc, err := store.CompareAndSwap(context.Background(), key, expectedRevision, json.RawMessage(body))
	if err != nil {
		return 0, toCallError(err)
	}
	return doc.Revision, nil
}

// configLayerKey 把层名映射到存储 key。
func configLayerKey(layer string) (config.LayerName, string, bool) {
	want := config.LayerName(strings.TrimSpace(layer))
	for _, l := range effectiveConfigLayers {
		if l.Layer == want {
			return l.Layer, l.Key, true
		}
	}
	return "", "", false
}

// configDocumentAbsent 判断这一层是否"没有文档"。
//
// 为什么不能只看 Load 的 error：store.Load 对缺失的 key 返回 revision=0、body=null
// 的文档（而不是错误）。把这种空文档当成一层会让"来源"里出现空层，
// 也会让 Merge 收到 null body。
func configDocumentAbsent(revision uint64, body []byte) bool {
	if revision == 0 || len(body) == 0 {
		return true
	}
	trimmed := strings.TrimSpace(string(body))
	return trimmed == "" || trimmed == "null"
}

// isConfigNotFound 判断"这一层没有文档"。
func isConfigNotFound(err error) bool {
	var appErr *contracts.AppError
	if errors.As(err, &appErr) && appErr != nil {
		return appErr.Code == contracts.CodeNotFound || appErr.Code == contracts.CodeConfigMissing
	}
	return false
}
