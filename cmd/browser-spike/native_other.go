//go:build !windows

package main

// B01a 的原生宿主只有 Windows/WebView2 实现；其它平台如实记录 BLOCKED，
// 不给一个"看起来能跑"的空实现。
import (
	"github.com/wailsapp/wails/v3/pkg/application"

	browserfixture "my-agent-go/tests/fixtures/browser"
)

func runNativeProbe(state *spikeState, _ *application.WebviewWindow, _ *shellServer, _ *browserfixture.Fixture, _ string) {
	state.nativeFail("本平台没有 B01a 原生宿主（Windows/WebView2 only）；请用 B02/B03")
}
