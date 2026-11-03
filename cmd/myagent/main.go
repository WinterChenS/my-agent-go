// Command myagent 是桌面宿主（Wails v3）。
//
// 它只负责壳：窗口、服务绑定、事件出口。运行时判断全在 internal 各包里，
// 这样"缺 Pi 就拒绝启动""审批超时即拒绝"这类结论可以被单测覆盖，不依赖 GUI。
package main

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"my-agent-go/frontend"
	"my-agent-go/internal/app"
	"my-agent-go/internal/contracts"
)

func main() {
	root, err := appRoot()
	if err != nil {
		log.Fatalf("定位应用数据目录失败: %v", err)
	}

	desktop, err := app.New(root)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	assets, err := fs.Sub(frontend.Dist, "dist")
	if err != nil {
		log.Fatalf("读取内嵌前端资源失败: %v", err)
	}

	a := application.New(application.Options{
		Name:        "MyAgent",
		Description: "本地 agent 客户端",
		Services: []application.Service{
			application.NewService(desktop),
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(assets),
		},
		Windows: windowsOptions(),
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// 事件出口：RunService 的每条产品事件都推给前端，前端只认这些类型。
	desktop.SetEmitter(func(ev contracts.Event) {
		a.Event.Emit(app.EventName, ev)
	})

	a.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "MyAgent",
		Width:            1180,
		Height:           820,
		MinWidth:         880,
		MinHeight:        600,
		URL:              "/",
		DevToolsEnabled:  true,
		BackgroundColour: application.NewRGB(15, 18, 24),
	})

	a.OnShutdown(desktop.Shutdown)

	if err := a.Run(); err != nil {
		log.Fatalf("应用退出: %v", err)
	}
}

// windowsOptions 返回 Windows 平台选项。
//
// MYAGENT_UI_A11Y=1 时给 WebView2 打开渲染器可访问性：
//   - 读屏软件能读到页面内容（产品可访问性）；
//   - UI 自动化（agent-desktop 的 accessibility tree）能定位到 DOM 元素，
//     而不是只看到一个空的 "Chrome Legacy Window"。
//
// 默认关闭：它会增加渲染进程的常驻开销，普通使用不需要。
func windowsOptions() application.WindowsOptions {
	opts := application.WindowsOptions{}
	if v := os.Getenv("MYAGENT_UI_A11Y"); v == "1" || strings.EqualFold(v, "true") {
		opts.AdditionalBrowserArgs = []string{"--force-renderer-accessibility"}
	}
	return opts
}

// appRoot 返回应用数据根目录。
//
// 允许 MYAGENT_HOME 覆盖：自动化验证需要把状态放进临时目录，
// 以免污染真实用户数据（也避免两次运行互相干扰）。
func appRoot() (string, error) {
	if v := os.Getenv("MYAGENT_HOME"); v != "" {
		if err := os.MkdirAll(v, 0o700); err != nil {
			return "", err
		}
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户配置目录: %w", err)
	}
	root := filepath.Join(base, "MyAgent")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}
