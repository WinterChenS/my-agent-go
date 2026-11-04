package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// BrowserSurface 负责把原生网页视图放进主窗口右侧的槽位。
//
// 它与 BrowserBackend 分开：backend 管"网页内容"，surface 管"网页在窗口里的位置"。
// 两者在 B01–B03 里由同一个 platform host 实现，但契约上不能合并——B04 的 DOM
// 语义测试不需要窗口，B07 的布局测试也不应该碰导航。
//
// 与 BrowserBackend 一样，所有方法都要尊重 ctx，且不能阻塞 UI 线程等待
// 需要 UI 线程才能完成的工作（基线 §7 / 设计文档 §5）。
type BrowserSurface interface {
	// Attach 在 parent 下创建原生宿主容器；重复调用返回错误而不是叠一层。
	Attach(ctx context.Context, parent NativeParent) error
	// Present 把某个 tab 的原生视图摆到 slot 对应的位置。
	// Revision 比上次小的消息必须被丢弃（拖动分栏时的迟到消息）。
	Present(ctx context.Context, tab TabID, slot SlotGeometry) error
	// SetVisible 只隐藏/显示，不销毁视图：隐藏面板不能丢 tab 状态。
	SetVisible(ctx context.Context, visible bool) error
	// Detach 拆除原生容器。关闭窗口或切换承载方式时调用。
	Detach(ctx context.Context) error
}

// ErrNativeParentNotSerializable 表示有人试图把原生句柄写进 JSON。
var ErrNativeParentNotSerializable = errors.New(
	"browser: NativeParent 持有平台原生句柄，不能序列化（不进 IPC、不进模型上下文）")

// NativeParent 是宿主窗口/容器的原生句柄载体。
//
// 它只在 native 内部流转：字段全部未导出，外部包既不能读也不能改，只能通过
// NewNativeParent 构造、由 native 适配层用 Handle() 取出真实句柄。为了把
// "不能导出成 JSON"从口头约定变成可执行的约束，它实现了会返回错误的
// MarshalJSON：任何把 NativeParent 放进 DTO、事件或模型上下文的地方都会在
// 序列化时立刻失败，而不是悄悄写出一串指针地址。
//
// 句柄类型按平台不同：Windows 是父窗口 HWND，macOS 是 contentView 的 NSView，
// Linux 是 GTK 容器。用 any 承载是为了让契约层不 import 任何平台包。
type NativeParent struct {
	handle any
	label  string
}

// NewNativeParent 由平台适配层调用，把原生句柄包成契约类型。
//
// label 只用于日志和错误消息（例如 "wails-main-window"），必须能脱敏展示，
// 不允许放句柄地址。
func NewNativeParent(handle any, label string) NativeParent {
	return NativeParent{handle: handle, label: label}
}

// Handle 返回底层原生句柄，仅供 internal/browser/native 内部使用。
//
// 拿到它的代码必须已经在对应平台的 UI 线程上，且不得把它序列化或跨进程发送。
func (p NativeParent) Handle() any { return p.handle }

// Label 是用于日志的稳定标签，例如 "wails-main-window"。
func (p NativeParent) Label() string { return p.label }

// Valid 表示是否真的带了句柄。
//
// spike 在窗口还没创建时拿到 nil 句柄，这是"初始失败"的一部分，必须有办法表达，
// 不能让 Attach(nil 句柄) 看起来像正常调用。
func (p NativeParent) Valid() bool { return p.handle != nil }

func (p NativeParent) String() string {
	return fmt.Sprintf("NativeParent(%s, valid=%t)", p.label, p.Valid())
}

// MarshalJSON 永远失败：NativeParent 不允许进 JSON。
func (p NativeParent) MarshalJSON() ([]byte, error) {
	return nil, ErrNativeParentNotSerializable
}

// UnimplementedSurface 是 B00 的占位 surface。
//
// spike 用它在真实 Wails 窗口上尝试挂载右侧视图，并如实打印
// "NOT_IMPLEMENTED"：右侧槽位仍然是空的，这是 B00 的预期失败，不是 bug 掩盖。
type UnimplementedSurface struct {
	// Reason 是给人看的具体原因。
	Reason string
}

// Attach 永远失败。
func (s UnimplementedSurface) Attach(_ context.Context, parent NativeParent) error {
	return appErr(CodeNotImplemented, fmt.Sprintf(
		"browser surface 未实现（B00）：右侧没有原生子视图，Attach(parent=%s) 无法挂载；%s",
		parent.Label(), s.reason()))
}

// Present 永远失败。
func (s UnimplementedSurface) Present(_ context.Context, tab TabID, slot SlotGeometry) error {
	return appErr(CodeNotImplemented, fmt.Sprintf(
		"browser surface 未实现（B00）：没有原生视图可以摆到 slot %q（tab %q）；%s",
		slot.SlotID, string(tab), s.reason()))
}

// SetVisible 永远失败：没有视图可隐藏，不能返回 nil 假装成功。
func (s UnimplementedSurface) SetVisible(context.Context, bool) error {
	return appErr(CodeNotImplemented, "browser surface 未实现（B00）：没有原生视图可显示/隐藏；"+s.reason())
}

// Detach 永远失败：没有挂载过就没有拆卸成功可言。
func (s UnimplementedSurface) Detach(context.Context) error {
	return appErr(CodeNotImplemented, "browser surface 未实现（B00）：没有挂载过的容器可拆卸；"+s.reason())
}

func (s UnimplementedSurface) reason() string {
	if s.Reason == "" {
		return "没有平台 native 宿主"
	}
	return s.Reason
}

var _ BrowserSurface = UnimplementedSurface{}

// 编译期断言：NativeParent 的 JSON 禁令靠 MarshalJSON 实现，这里确认接口形状
// 没有被误删（删掉它 TestNativeParentNeverSerializes 也会失败）。
var _ json.Marshaler = NativeParent{}
