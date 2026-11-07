package app

// U00：偏好（语言/主题）走既有版本化配置存储的 CAS。
//
// 三条纪律守在这里：
//  1. 不进 Settings —— Settings 是运行参数，语言/主题是纯展示偏好，
//     混在一起会让"改主题"触发运行时重建（或反之，改模型把主题重置）。
//  2. 写入必须 CAS：两个窗口同时改主题时，后写的必须看见 CONFIG_CONFLICT，
//     而不是静默覆盖对方的语言选择。
//  3. 未知 locale/theme 必须带结构化错误码与 params，UI 才能本地化成
//     "语言 fr-FR 不受支持"，而不是靠解析中文文案。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
)

func TestPreferencesDefaultsBeforeFirstSave(t *testing.T) {
	d, _ := newTestDesktop(t)

	got := d.GetPreferences()
	if got.Error != nil {
		t.Fatalf("首次读取不该报错: %+v", got.Error)
	}
	// 首次启动没有落盘偏好：revision=0 是"从未保存过"的诚实表示，
	// 不是"保存过一份空的"。
	if got.Revision != 0 {
		t.Fatalf("首次 revision 应为 0，实际 %d", got.Revision)
	}
	if got.Preferences.Locale != LocaleZhCN || got.Preferences.Theme != ThemeSystem {
		t.Fatalf("默认偏好不符: %+v", got.Preferences)
	}
	if _, err := os.Stat(filepath.Join(d.root, "config", "preferences.json")); !os.IsNotExist(err) {
		t.Fatalf("只读不该写出 preferences.json: %v", err)
	}
}

func TestPreferencesCASAndPersistence(t *testing.T) {
	d, root := newTestDesktop(t)

	saved := d.SavePreferences(0, Preferences{Locale: LocaleEnUS, Theme: ThemeDark})
	if saved.Error != nil {
		t.Fatalf("首次保存失败: %+v", saved.Error)
	}
	if saved.Revision != 1 {
		t.Fatalf("首次保存后 revision 应为 1，实际 %d", saved.Revision)
	}
	if saved.Preferences.Locale != LocaleEnUS || saved.Preferences.Theme != ThemeDark {
		t.Fatalf("返回值应回显已保存的偏好: %+v", saved.Preferences)
	}

	// 落盘位置固定在既有配置存储里，且不带任何 Settings 字段。
	raw, err := os.ReadFile(filepath.Join(root, "config", "preferences.json"))
	if err != nil {
		t.Fatalf("偏好未落盘: %v", err)
	}
	if !strings.Contains(string(raw), "\"en-US\"") || !strings.Contains(string(raw), "\"dark\"") {
		t.Fatalf("落盘内容不含偏好: %s", raw)
	}

	// 重新打开应用（新 Desktop，同一 root）必须读到同一份偏好：
	// "重启后语言/主题回到默认"是用户最讨厌的一类回归。
	d2, err := New(root)
	if err != nil {
		t.Fatalf("重新构造 Desktop: %v", err)
	}
	t.Cleanup(d2.Shutdown)
	reloaded := d2.GetPreferences()
	if reloaded.Error != nil {
		t.Fatalf("重启后读取失败: %+v", reloaded.Error)
	}
	if reloaded.Revision != 1 {
		t.Fatalf("重启后 revision 应为 1，实际 %d", reloaded.Revision)
	}
	if reloaded.Preferences.Locale != LocaleEnUS || reloaded.Preferences.Theme != ThemeDark {
		t.Fatalf("重启后偏好丢失: %+v", reloaded.Preferences)
	}

	// 陈旧 revision：另一个窗口已经写过，这里必须冲突而不是覆盖。
	stale := d2.SavePreferences(0, Preferences{Locale: LocaleZhCN, Theme: ThemeLight})
	if stale.Error == nil {
		t.Fatal("陈旧 revision 必须被拒绝")
	}
	if stale.Error.Code != contracts.CodeConfigConflict {
		t.Fatalf("期望 CONFIG_CONFLICT，实际 %q（%s）", stale.Error.Code, stale.Error.Message)
	}
	if stale.Error.Retryable {
		// 重放同一个 expectedRevision 只会再冲突一次；修复方式是"重读后带新 revision 重写"，
		// 不是自动重试。标成可重试会让 UI 弹出一个永远失败的按钮。
		t.Fatalf("陈旧 revision 不应标为可直接重试: %+v", stale.Error)
	}
	if stale.Error.Operation != "preferences.save" {
		t.Fatalf("冲突要标明失败操作，实际 %q", stale.Error.Operation)
	}
	after := d2.GetPreferences()
	if after.Revision != 1 || after.Preferences.Locale != LocaleEnUS {
		t.Fatalf("冲突不得改动已存偏好: %+v", after)
	}
}

func TestPreferencesRejectUnknownLocaleAndTheme(t *testing.T) {
	d, _ := newTestDesktop(t)

	cases := []struct {
		name  string
		p     Preferences
		field string
		value string
	}{
		{"未知语言", Preferences{Locale: "fr-FR", Theme: ThemeSystem}, "locale", "fr-FR"},
		{"未知主题", Preferences{Locale: LocaleZhCN, Theme: "solarized"}, "theme", "solarized"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := d.SavePreferences(0, c.p)
			if res.Error == nil {
				t.Fatalf("必须拒绝 %+v", c.p)
			}
			if res.Error.Code != contracts.CodeInvalidInput {
				t.Fatalf("期望 INVALID_INPUT，实际 %q", res.Error.Code)
			}
			// params 是前端本地化替换的唯一依据；缺了就只能显示中文原文。
			if res.Error.Params["field"] != c.field || res.Error.Params["value"] != c.value {
				t.Fatalf("params 应给出 field=%s value=%s: %+v", c.field, c.value, res.Error.Params)
			}
			if res.Error.Params["supported"] == "" {
				t.Fatalf("params 应给出允许集合: %+v", res.Error.Params)
			}
			if res.Error.Operation != "preferences.save" {
				t.Fatalf("operation 应标明失败操作，实际 %q", res.Error.Operation)
			}
			// 被拒绝的写入不得推高 revision：否则下一次合法保存会莫名撞冲突。
			if got := d.GetPreferences(); got.Revision != 0 {
				t.Fatalf("被拒绝的写入改了 revision: %d", got.Revision)
			}
		})
	}
}

// TestPreferencesAreNotPartOfSettings 用反射守住"不许往 Settings 里加字段"。
//
// 这条断言看起来像洁癖，但它挡住的是真实的耦合：Settings 一变，
// 运行时签名与 model/agent 重建逻辑就跟着变，语言切换会中断正在跑的 run。
func TestPreferencesAreNotPartOfSettings(t *testing.T) {
	typ := reflect.TypeOf(Settings{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if name == "locale" || name == "theme" {
			t.Fatalf("Settings 不得承载展示偏好: 发现字段 %s", typ.Field(i).Name)
		}
	}
}

// TestPreferencesNormalizesEmptyToDefaults 守住"空值不落成空串"：
// 空 locale 落盘后前端会拿到一个没有 catalog 的语言。
func TestPreferencesNormalizesEmptyToDefaults(t *testing.T) {
	d, root := newTestDesktop(t)

	res := d.SavePreferences(0, Preferences{})
	if res.Error != nil {
		t.Fatalf("空偏好应落成默认值: %+v", res.Error)
	}
	if res.Preferences.Locale != LocaleZhCN || res.Preferences.Theme != ThemeSystem {
		t.Fatalf("空偏好未归一化: %+v", res.Preferences)
	}
	raw, err := os.ReadFile(filepath.Join(root, "config", "preferences.json"))
	if err != nil {
		t.Fatalf("读取落盘偏好: %v", err)
	}
	var env struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析信封: %v", err)
	}
	var body Preferences
	if err := json.Unmarshal(env.Body, &body); err != nil {
		t.Fatalf("解析 body: %v", err)
	}
	if body.Locale != LocaleZhCN || body.Theme != ThemeSystem {
		t.Fatalf("body 未归一化: %+v", body)
	}
}
