//go:build windows

package native

// controller 可见性辅助函数的单测：不启动窗口，只钉住两件会静默漂移的事——
//  1. HRESULT 失败判定必须是 MSB 语义（不是"非 0 即失败"）；
//  2. vtable 槽位号必须和依赖库的结构体布局一致（依赖升级改了顺序就立刻失败，
//     而不是等某次 SetVisible 调用去踩错函数指针）。

import (
	"reflect"
	"testing"

	"github.com/jchv/go-webview2/pkg/edge"
)

func TestHresultFailedUsesMsbSemantics(t *testing.T) {
	// S_OK / S_FALSE 都不是失败；E_ 前缀（MSB=1）才是。
	for _, hr := range []uintptr{0x00000000, 0x00000001, 0x000003E8} {
		if hresultFailed(hr) {
			t.Fatalf("HRESULT 0x%08x 不应判失败", uint32(hr))
		}
	}
	for _, hr := range []uintptr{0x80004005, 0x80070057, 0x80070578, 0xFFFFFFFF} {
		if !hresultFailed(hr) {
			t.Fatalf("HRESULT 0x%08x 应判失败", uint32(hr))
		}
	}
}

func TestControllerVtblSlotsMatchDependencyLayout(t *testing.T) {
	ctrl := reflect.TypeOf(edge.ICoreWebView2Controller{})
	if ctrl.Kind() != reflect.Struct || ctrl.NumField() != 1 {
		t.Fatalf("ICoreWebView2Controller 布局变了（%s），本文件的 unsafe 读法需要重新核对", ctrl)
	}
	vtblPtr := ctrl.Field(0).Type
	if vtblPtr.Kind() != reflect.Ptr || vtblPtr.Elem().Kind() != reflect.Struct {
		t.Fatalf("vtable 字段不是结构体指针（%s），本文件的 unsafe 读法需要重新核对", vtblPtr)
	}
	vtbl := vtblPtr.Elem()

	// vtable 结构体里 _IUnknownVtbl 是**嵌入**字段：内存槽位是"嵌入结构展开在前"，
	// reflect 看到的却是"一个字段"。所以先把嵌入结构递归展开成槽位名列表，再断言。
	slots := flattenVtblSlots(vtbl)
	want := map[int]string{
		ctrlSlotGetIsVisible: "GetIsVisible",
		ctrlSlotPutIsVisible: "PutIsVisible",
	}
	for idx, name := range want {
		if idx >= len(slots) {
			t.Fatalf("vtable 只有 %d 个槽位，取不到 %s", len(slots), name)
		}
		if got := slots[idx]; got != name {
			t.Fatalf("vtable 槽位 %d = %s，期望 %s（依赖库升级导致偏移变化）", idx, got, name)
		}
	}
}

// flattenVtblSlots 把 vtable 结构体展开成内存槽位顺序的方法名列表。
//
// 嵌入结构（_IUnknownVtbl）在内存里是"原地展开"的，所以必须递归展开才和
// unsafe 读到的槽位偏移对得上。
func flattenVtblSlots(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			out = append(out, flattenVtblSlots(f.Type)...)
			continue
		}
		out = append(out, f.Name)
	}
	return out
}
