//go:build !windows

package native

// 非 Windows 平台上没有原生宿主实现：B02（WKWebView）与 B03（WebKitGTK）
// 各自在自己的任务里落地。这里返回明确错误，绝不给一个"能构造但什么都不做"
// 的占位对象——那会让调用方误以为右侧已经有网页。
import (
	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
)

// NewWindowsHost 在非 Windows 平台上永远失败，并说明原因。
func NewWindowsHost(_ browser.NativeParent, _ HostOptions) (NativeHost, error) {
	return nil, contracts.NewAppError(contracts.CodeUnsupported,
		"B01a 的原生宿主只有 Windows/WebView2 实现；本平台需要 B02/B03", false)
}
