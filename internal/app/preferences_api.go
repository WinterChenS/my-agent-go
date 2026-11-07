package app

// U00：展示偏好（语言 / 主题）的绑定层。
//
// 为什么不塞进 Settings：
//   - Settings 是**运行参数**（模型、工作区、shell 许可），改动会改运行时签名、
//     让 RunService 重建。语言/主题只是展示，改它不该中断正在跑的 run。
//   - Settings 会进诊断包与运行时快照；偏好放进去只会稀释真正重要的字段。
//
// 存储用的是 F07 已有的版本化配置存储（CAS + checksum），因此"两个窗口
// 同时改主题"这种真实并发会得到 CONFIG_CONFLICT，而不是静默覆盖。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// 支持的语言与主题。前端 catalog 的 key 集合与此一一对应。
const (
	LocaleZhCN = "zh-CN"
	LocaleEnUS = "en-US"

	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// 偏好占用的配置存储 key。以 preferences.json 落盘在 <appRoot>/config/ 下。
const preferencesKey = "preferences"

// Preferences 是用户可见的展示偏好。
//
// 只有两项：语言与主题。它们都是"选择方式"级别的偏好，不承载任何业务状态。
type Preferences struct {
	Locale string `json:"locale"`
	Theme  string `json:"theme"`
}

// preferencesDoc 是偏好**落盘**的形状：在既有版本化配置信封的 body 里
// 必须带 schemaVersion（config.Store 统一校验），但前端不需要看到它。
type preferencesDoc struct {
	SchemaVersion int    `json:"schemaVersion"`
	Locale        string `json:"locale"`
	Theme         string `json:"theme"`
}

// PreferencesResult 是读取/保存偏好的结果。
//
// Revision 是 CAS 的凭据：调用方保存时必须把读到的那一版带回来。
type PreferencesResult struct {
	Preferences Preferences `json:"preferences"`
	Revision    uint64      `json:"revision"`
	Error       *CallError  `json:"error,omitempty"`
}

// defaultPreferences 是"从未保存过"时的默认值。
func defaultPreferences() Preferences {
	return Preferences{Locale: LocaleZhCN, Theme: ThemeSystem}
}

func supportedLocales() []string { return []string{LocaleZhCN, LocaleEnUS} }
func supportedThemes() []string  { return []string{ThemeSystem, ThemeLight, ThemeDark} }

// normalizePreferences 校验并把空字段填成默认值。
//
// 空字段归一化而不是拒绝：前端在拿到偏好之前先渲染一次是常态，
// 它手上只有"默认值"；把它当成非法输入会让首屏就报错。
// 但"非空的未知值"必须拒绝——那是真正的调用方 bug 或旧版本兼容问题，
// 静默替换成默认值会让用户以为自己的选择被记住了。
func normalizePreferences(p Preferences) (Preferences, *contracts.AppError) {
	out := Preferences{Locale: strings.TrimSpace(p.Locale), Theme: strings.TrimSpace(p.Theme)}
	if out.Locale == "" {
		out.Locale = LocaleZhCN
	}
	if out.Theme == "" {
		out.Theme = ThemeSystem
	}
	if !containsString(supportedLocales(), out.Locale) {
		return out, contracts.NewAppError(contracts.CodeInvalidInput, "不支持的语言: "+out.Locale, false).
			WithParams(map[string]string{"field": "locale", "value": out.Locale, "supported": strings.Join(supportedLocales(), ", ")}).
			WithOperation("preferences.save")
	}
	if !containsString(supportedThemes(), out.Theme) {
		return out, contracts.NewAppError(contracts.CodeInvalidInput, "不支持的主题: "+out.Theme, false).
			WithParams(map[string]string{"field": "theme", "value": out.Theme, "supported": strings.Join(supportedThemes(), ", ")}).
			WithOperation("preferences.save")
	}
	return out, nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// preferencesStore 返回偏好所在的配置存储（与其它用户级配置同一个根）。
func (d *Desktop) preferencesStore() (*config.Store, error) {
	return config.NewStore(filepath.Join(d.root, "config"))
}

// GetPreferences 读取当前偏好。
//
// 每次都读磁盘而不是缓存：配置存储允许外部编辑（用户手改、导入配置），
// 缓存一份就意味着"改完文件要重启才生效"，而且缓存与磁盘不一致时
// 没人说得清哪个是真相。
func (d *Desktop) GetPreferences() PreferencesResult {
	def := defaultPreferences()
	store, err := d.preferencesStore()
	if err != nil {
		return PreferencesResult{Preferences: def, Error: toCallError(err)}
	}
	doc, err := store.Load(context.Background(), preferencesKey)
	if err != nil {
		return PreferencesResult{Preferences: def, Error: toCallError(err)}
	}
	if len(doc.Body) == 0 {
		// 从未保存过：revision=0，返回默认值，不落盘。
		return PreferencesResult{Preferences: def, Revision: 0}
	}
	var p Preferences
	var docFile preferencesDoc
	if uerr := json.Unmarshal(doc.Body, &docFile); uerr != nil {
		return PreferencesResult{
			Preferences: def,
			Revision:    doc.Revision,
			Error: toCallError(contracts.NewAppError(
				contracts.CodeInternal, "偏好文件内容不是合法 JSON", false).
				WithOperation("preferences.get").
				WithCause(uerr.Error())),
		}
	}
	p = Preferences{Locale: docFile.Locale, Theme: docFile.Theme}
	norm, appErr := normalizePreferences(p)
	if appErr != nil {
		// 兼容旧数据：磁盘上存着已不支持的语言时，回落到默认值但明确报告，
		// 不静默改写用户的文件（改写会掩盖"是谁写进去的"这条线索）。
		return PreferencesResult{Preferences: def, Revision: doc.Revision, Error: toCallError(appErr.WithOperation("preferences.get"))}
	}
	// Revision 是 CAS 凭据，必须原样带出。
	return PreferencesResult{Preferences: norm, Revision: doc.Revision}
}

// SavePreferences 以 CAS 保存偏好。expectedRevision 必须来自最近一次读取。
//
// 拒绝条件：revision 不一致（另一个窗口先写了）→ CONFIG_CONFLICT；
// 未知 locale/theme → INVALID_INPUT（带 params，前端可本地化）。
// 两种拒绝都不改动磁盘，也不推高 revision。
func (d *Desktop) SavePreferences(expectedRevision uint64, p Preferences) PreferencesResult {
	norm, appErr := normalizePreferences(p)
	if appErr != nil {
		return PreferencesResult{Preferences: norm, Error: toCallError(appErr)}
	}
	store, err := d.preferencesStore()
	if err != nil {
		return PreferencesResult{Preferences: norm, Error: toCallError(err)}
	}
	body, err := json.Marshal(preferencesDoc{
		SchemaVersion: config.ConfigSchemaVersion,
		Locale:        norm.Locale,
		Theme:         norm.Theme,
	})
	if err != nil {
		return PreferencesResult{Preferences: norm, Error: toCallError(contracts.NewAppError(
			contracts.CodeInternal, "序列化偏好失败", false).WithOperation("preferences.save"))}
	}
	doc, err := store.CompareAndSwap(context.Background(), preferencesKey, expectedRevision, body)
	if err != nil {
		// 存储层已经把冲突/外部编辑都收敛成 CONFIG_CONFLICT；这里补上 operation，
		// 让前端与日志能一眼看出是哪次写操作失败。
		ce := toCallError(err)
		ce.Operation = "preferences.save"
		return PreferencesResult{Preferences: norm, Error: ce}
	}
	return PreferencesResult{Preferences: norm, Revision: doc.Revision}
}
