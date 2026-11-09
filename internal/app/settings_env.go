package app

// 模型配置的环境变量来源（M1ENV）。
//
// 为什么需要这一层：预览包、CI 与"临时指到一个 fixture provider"都不该去改用户的
// settings.json——那是用户资产，脚本改写它会在真实使用中丢配置。
//
// 优先级只有一条：**设置 > 环境变量 > 默认值**。
//   - 设置：settings.json 里**写了这个键**（哪怕是空串）就以它为准；
//   - 环境变量：只在键缺席时补位，走 envLookup（ANVIL_* 优先，MYAGENT_* 兼容期仍可用）；
//   - 默认值：两者都没说话时才用（本机 fixture 端点等）。
//
// 旧实现（applyEnvOverrides）是无条件覆盖：脚本定一次 ANVIL_MODEL，用户在设置里改的
// 模型就永远不生效，而且界面完全看不出是谁在生效。这一层把"谁生效"变成可查询的事实
// （ModelConfig 的 Source/EnvVar），设置页据此显示来源，不允许静默覆盖。
//
// 环境变量补上的值**不落盘**：saveSettings 会把"仍等于环境值、且磁盘上没写过这个键"
// 的字段清空后再写（见 stripEnvDerived）。否则一次编辑就把自动化用的值钉进用户配置，
// 环境变量消失后行为会莫名其妙地变化。
//
// 密钥的纪律更严格：环境变量给的 key（ANVIL_MODEL_API_KEY）只以**引用**形式存在
// （env:ANVIL_MODEL_API_KEY），明文只在进程内存里，经子进程环境交给 Pi。

import (
	"encoding/json"
	"os"
	"strings"
)

// 环境变量名（不带前缀，由 envLookup 解析成 ANVIL_*/MYAGENT_*）。
const (
	envProvider  = "PROVIDER"
	envModel     = "MODEL"
	envBaseURL   = "BASE_URL"
	envModelKey  = "MODEL_API_KEY"
	envLegacyKey = "API_KEY"
	envNodePath  = "NODE_PATH"
	envPiPath    = "PI_PATH"
	envWorkspace = "WORKSPACE"
	envShell     = "SHELL"
)

// modelAPIKeyEnvPrefix 是密钥引用的形态：env:ANVIL_MODEL_API_KEY。
//
// 引用只说"从哪取"，不含密钥本身，所以可以安全落盘、也可以显示在设置页。
const modelAPIKeyEnvPrefix = "env:"

// ConfigSource 是一个配置字段的生效来源。
type ConfigSource string

const (
	// SourceSettings 表示值来自 settings.json 里用户写下的那个键。
	SourceSettings ConfigSource = "settings"
	// SourceEnv 表示值来自环境变量（实际变量名在 EnvVar 里）。
	SourceEnv ConfigSource = "env"
	// SourceDefault 表示环境与设置都没说话，用的是内置默认值。
	SourceDefault ConfigSource = "default"
	// SourceNone 表示这个字段确实没有值。
	SourceNone ConfigSource = "none"
)

// ConfigField 是"生效值 + 它从哪来"。
//
// 来源必须能查询而不是靠推断：界面上显示的是设置值、实际跑的是环境值，
// 是这类"假旋钮"缺陷最难查的那一面。
type ConfigField struct {
	Value  string       `json:"value"`
	Source ConfigSource `json:"source"`
	// EnvVar 只在 Source=env 时非空：命中的完整变量名（ANVIL_* 或旧 MYAGENT_*）。
	EnvVar string `json:"envVar,omitempty"`
}

// ModelConfig 是有效模型配置与每项来源。
//
// 它是 EffectiveConfig 与设置页的唯一事实来源：被环境变量补上的字段必须在这里
// 露出来（OverriddenByEnv），不允许"界面显示设置值、实际跑的是环境值"。
type ModelConfig struct {
	Provider  ConfigField `json:"provider"`
	Model     ConfigField `json:"model"`
	BaseURL   ConfigField `json:"baseUrl"`
	APIKeyRef ConfigField `json:"apiKeyRef"`
	// OverriddenByEnv 列出被环境变量补上的字段（provider/model/baseUrl/apiKeyRef）。
	OverriddenByEnv []string `json:"overriddenByEnv,omitempty"`
}

// settingsDocument 是 settings.json 的原始视图：值 + "这个键写没写"。
//
// 为什么必须区分：JSON 的零值分不清"用户没配"与"用户显式配成空"。优先级规则依赖
// 这个区别——缺席才是"没说话"，环境变量只能补缺席的字段；显式空串是一个设置
// （例如用户清掉了端点），此时环境变量不得偷偷顶替。
type settingsDocument struct {
	Settings Settings
	present  map[string]bool
}

// parseSettingsDocument 解析磁盘上的设置文档（raw 为空表示还没有这个文件）。
func parseSettingsDocument(raw []byte) (settingsDocument, error) {
	doc := settingsDocument{present: map[string]bool{}}
	if len(raw) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(raw, &doc.Settings); err != nil {
		return doc, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return doc, err
	}
	for k := range keys {
		doc.present[k] = true
	}
	return doc, nil
}

// settingsDocumentOf 把"准备落盘的那份设置"当成文档看待（键集取自实际序列化结果）。
//
// 保存后内存里的生效值要按"磁盘上到底写了什么 + 环境变量"重新解析，
// 所以键集必须来自真实序列化，而不是手工列一遍字段名——手工清单会和 json tag 分叉。
func settingsDocumentOf(s Settings) settingsDocument {
	doc := settingsDocument{Settings: s, present: map[string]bool{}}
	raw, err := json.Marshal(s)
	if err != nil {
		return doc
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return doc
	}
	for k := range keys {
		doc.present[k] = true
	}
	return doc
}

// has 报告用户文档里是否写过这个键。
func (doc settingsDocument) has(key string) bool { return doc.present[key] }

// resolveSettings 按 设置 > 环境 > 默认 得到生效设置与模型配置来源。
//
// 这是唯一的解析点：loadSettings / SettingsWithRevision / saveSettings 都调用它，
// 三处各写一份优先级就会出现"读到的来源和跑起来的来源不是同一件事"。
func (d *Desktop) resolveSettings(doc settingsDocument) (Settings, ModelConfig) {
	def := defaultSettings(d.root)
	out := doc.Settings
	cfg := ModelConfig{}

	pick := func(key string, userValue string, envName string, defValue string) ConfigField {
		if doc.has(key) {
			return ConfigField{Value: userValue, Source: SourceSettings}
		}
		if v, name := envLookup(envName); v != "" {
			return ConfigField{Value: v, Source: SourceEnv, EnvVar: name}
		}
		if defValue != "" {
			return ConfigField{Value: defValue, Source: SourceDefault}
		}
		return ConfigField{Value: "", Source: SourceNone}
	}

	cfg.Provider = pick("provider", doc.Settings.Provider, envProvider, def.Provider)
	cfg.Model = pick("model", doc.Settings.Model, envModel, def.Model)
	cfg.BaseURL = pick("baseUrl", doc.Settings.BaseURL, envBaseURL, def.BaseURL)
	// 密钥只以引用形式出现：设置里写 ref，或环境变量给了 key（引用自动变成 env:<变量名>）。
	cfg.APIKeyRef = pick("modelAPIKeyRef", doc.Settings.ModelAPIKeyRef, envModelKey, "")
	if cfg.APIKeyRef.Source == SourceEnv {
		cfg.APIKeyRef.Value = modelAPIKeyEnvPrefix + cfg.APIKeyRef.EnvVar
	}

	out.Provider = cfg.Provider.Value
	out.Model = cfg.Model.Value
	out.BaseURL = cfg.BaseURL.Value
	out.ModelAPIKeyRef = cfg.APIKeyRef.Value
	// 旧路径的明文密钥字段：环境变量补上时只在内存里，落盘前会被 stripEnvDerived 清掉。
	out.APIKey = pick("apiKey", doc.Settings.APIKey, envLegacyKey, "").Value

	// 其余运行期字段同一套优先级。PiPath 以前只在 resolveInstallation 里补，
	// 内存里的值却仍被环境无条件覆盖——两处口径不同，正是"设置里选了 Pi 却被
	// 环境变量顶掉"的来源。
	out.PiPath = pick("piPath", doc.Settings.PiPath, envPiPath, "").Value
	out.NodePath = pick("nodePath", doc.Settings.NodePath, envNodePath, def.NodePath).Value
	out.Workspace = pick("workspace", doc.Settings.Workspace, envWorkspace, def.Workspace).Value
	// 这两个字段没有环境变量：saved 路径是上一次点击选出来的，ModelRef 是资源库引用。
	out.PiSavedPath = doc.Settings.PiSavedPath
	out.ModelRef = doc.Settings.ModelRef

	// shell 开关是布尔：JSON 里的缺失与 false 无法区分，所以只按"键在不在"判断。
	switch {
	case doc.has("shellAllowed"):
		out.ShellAllowed = doc.Settings.ShellAllowed
	default:
		if v, _ := envLookup(envShell); v != "" {
			out.ShellAllowed = v == "1" || strings.EqualFold(v, "true")
		} else {
			out.ShellAllowed = def.ShellAllowed
		}
	}

	for _, f := range []struct {
		name  string
		field ConfigField
	}{
		{"provider", cfg.Provider},
		{"model", cfg.Model},
		{"baseUrl", cfg.BaseURL},
		{"apiKeyRef", cfg.APIKeyRef},
	} {
		if f.field.Source == SourceEnv {
			cfg.OverriddenByEnv = append(cfg.OverriddenByEnv, f.name)
		}
	}
	return out, cfg
}

// modelSecret 返回交给 Pi 的密钥**原文**：只写进子进程环境，不落盘、不进日志。
//
// 引用形态 env:NAME 从环境变量现取（明文只在进程内存里，settings.json 里只有引用）；
// 旧路径（settings.apiKey 明文）保持原样，由界面的明文告警负责劝退。
func modelSecret(s Settings) string {
	ref := strings.TrimSpace(s.ModelAPIKeyRef)
	if name, ok := strings.CutPrefix(ref, modelAPIKeyEnvPrefix); ok {
		return strings.TrimSpace(os.Getenv(name))
	}
	return strings.TrimSpace(s.APIKey)
}

// stripEnvDerived 清掉"这一次其实来自环境变量、磁盘上也还没写过这个键"的字段，
// 返回被清掉的键集合。
//
// 目的不是审计，而是防固化：任何保存路径（设置页保存、切模型、迁移）都不得把
// ANVIL_* 的值写进 settings.json。否则脚本用一次，用户配置就被永久改掉；
// 之后环境变量消失，行为会变，而且没人说得清是哪一步写进去的。
//
// 判定条件是三个：磁盘上没写过这个键 + 环境变量有值 + 待保存的值正好等于它。
// 用户显式改成了别的值（或显式清空）都不满足条件，会照常落盘。
func stripEnvDerived(next Settings, doc settingsDocument) (Settings, map[string]bool) {
	stripped := map[string]bool{}
	strip := func(key string, field *string, envName string) {
		if doc.has(key) {
			return // 用户写过这个键：那是他的值，保留
		}
		env, _ := envLookup(envName)
		if env == "" || strings.TrimSpace(*field) != env {
			return
		}
		*field = ""
		stripped[key] = true
	}
	strip("provider", &next.Provider, envProvider)
	strip("model", &next.Model, envModel)
	strip("baseUrl", &next.BaseURL, envBaseURL)
	strip("apiKey", &next.APIKey, envLegacyKey)
	strip("nodePath", &next.NodePath, envNodePath)
	strip("piPath", &next.PiPath, envPiPath)
	// 引用是自动生成的（env:<变量名>），不该被当成用户输入固化下来。
	if _, name := envLookup(envModelKey); name != "" && !doc.has("modelAPIKeyRef") &&
		strings.TrimSpace(next.ModelAPIKeyRef) == modelAPIKeyEnvPrefix+name {
		next.ModelAPIKeyRef = ""
		stripped["modelAPIKeyRef"] = true
	}
	return next, stripped
}
