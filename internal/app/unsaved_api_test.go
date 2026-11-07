package app

// U00：未保存改动的登记表（F09 的"退出并更新"阻止项之一）。
//
// 为什么放在 Go 而不是纯前端：退出/更新决策发生在宿主侧。
// 如果"还有未保存改动"只存在于某个渲染进程的内存里，窗口一关就没人知道，
// 自动更新会踩着用户的草稿重启应用。

import (
	"reflect"
	"testing"
)

func TestUnsavedScopesAreDeduplicated(t *testing.T) {
	d, _ := newTestDesktop(t)

	if got := d.UnsavedScopes(); len(got) != 0 {
		t.Fatalf("初始应为空: %v", got)
	}

	// 重复上报同一个 scope 只能算一条：编辑器每次按键都会重报，
	// 不去重的话这份列表会无限增长，且"有 3 处改动"会变成假数字。
	d.ReportUnsavedChanges("agent-editor", true)
	d.ReportUnsavedChanges("agent-editor", true)
	if got := d.UnsavedScopes(); !reflect.DeepEqual(got, []string{"agent-editor"}) {
		t.Fatalf("去重失败: %v", got)
	}

	// 顺序必须稳定（字典序）：F09 会把这份列表原样显示给用户，
	// 每次调用顺序不同会让对话框在重绘时跳字。
	d.ReportUnsavedChanges("settings", true)
	d.ReportUnsavedChanges("chat-composer", true)
	want := []string{"agent-editor", "chat-composer", "settings"}
	if got := d.UnsavedScopes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("顺序不稳定: %v (want %v)", got, want)
	}

	// 清除：dirty=false 把 scope 摘掉，其余不受影响。
	d.ReportUnsavedChanges("chat-composer", false)
	if got := d.UnsavedScopes(); !reflect.DeepEqual(got, []string{"agent-editor", "settings"}) {
		t.Fatalf("清除后不符: %v", got)
	}

	// 清除一个没登记过的 scope 是幂等的（页面卸载时无条件清除）。
	d.ReportUnsavedChanges("settings", false)
	d.ReportUnsavedChanges("settings", false)
	if got := d.UnsavedScopes(); !reflect.DeepEqual(got, []string{"agent-editor"}) {
		t.Fatalf("重复清除不幂等: %v", got)
	}
}

func TestUnsavedScopesIgnoreBlankAndNormalize(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 空白 scope 既不能污染列表，也不能被当成一个合法条目。
	d.ReportUnsavedChanges("   ", true)
	d.ReportUnsavedChanges("", true)
	if got := d.UnsavedScopes(); len(got) != 0 {
		t.Fatalf("空白 scope 不该被登记: %v", got)
	}

	// 同一个 scope 带首尾空格视为同一个：否则 "editor" 与 "editor " 会并列出现。
	d.ReportUnsavedChanges(" editor ", true)
	d.ReportUnsavedChanges("editor", true)
	if got := d.UnsavedScopes(); !reflect.DeepEqual(got, []string{"editor"}) {
		t.Fatalf("未归一化: %v", got)
	}
}

// TestUnsavedScopesAreCopies 守住"返回值不是内部切片"：
// 调用方（比如诊断或事件推送）改返回值不该改到登记表本身。
func TestUnsavedScopesAreCopies(t *testing.T) {
	d, _ := newTestDesktop(t)
	d.ReportUnsavedChanges("a", true)
	d.ReportUnsavedChanges("b", true)

	got := d.UnsavedScopes()
	got[0] = "被改掉了"
	if next := d.UnsavedScopes(); next[0] != "a" {
		t.Fatalf("UnsaveScopes 暴露了内部切片: %v", next)
	}
}
