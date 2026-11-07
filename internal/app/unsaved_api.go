package app

// U00：未保存改动的登记表。
//
// 谁在用：F09 的"退出并更新"以及窗口关闭确认。判断必须发生在宿主侧，
// 因为退出/更新是宿主的行为；如果这份状态只活在渲染进程里，
// 窗口一关就没人知道还有草稿，自动更新会踩着用户没保存的改动重启。
//
// 语义刻意保持极简：只有"哪些 scope 现在是脏的"，不保存内容、不做 diff。
// 真正的脏状态在编辑器里，这里只是一份可被宿主查询的声明。

import (
	"sort"
	"strings"
)

// 锁与状态在 Desktop 上（见 desktop.go 的 unsavedMu / unsavedScopes）：
// 与 d.mu 分开，因为 ReportUnsavedChanges 会被编辑器在每次按键/焦点变化时调用，
// 抢 d.mu 会和 Status/Start 这类重量级操作互相拖慢。

// ReportUnsavedChanges 上报某个 scope（页面/编辑器）当前是否有未保存改动。
//
// scope 是稳定的页面标识（例如 "agent-editor"、"settings"），不是给用户看的文案。
// dirty=false 是幂等的清除：页面卸载时无条件调用即可。
func (d *Desktop) ReportUnsavedChanges(scope string, dirty bool) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		// 空白 scope 既不能登记也不能清除别人：静默忽略比把它当成一个条目有用。
		return
	}
	d.unsavedMu.Lock()
	defer d.unsavedMu.Unlock()
	if !dirty {
		delete(d.unsavedScopes, scope)
		return
	}
	if d.unsavedScopes == nil {
		d.unsavedScopes = map[string]struct{}{}
	}
	d.unsavedScopes[scope] = struct{}{}
}

// UnsavedScopes 返回当前有未保存改动的 scope，按字典序去重排序。
//
// 排序是为了稳定：F09 会把这份列表原样展示给用户，顺序抖动会让对话框重绘时跳字，
// 也让"两次调用结果一致"这类断言变得不可能。
func (d *Desktop) UnsavedScopes() []string {
	d.unsavedMu.Lock()
	defer d.unsavedMu.Unlock()
	out := make([]string, 0, len(d.unsavedScopes))
	for scope := range d.unsavedScopes {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}
