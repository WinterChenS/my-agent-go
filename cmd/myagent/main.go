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
	root, migration, err := appRoot()
	if err != nil {
		log.Fatalf("定位应用数据目录失败: %v", err)
	}
	logBrandMigration(migration)

	desktop, err := app.New(root)
	if err != nil {
		log.Fatalf("初始化失败: %v", err)
	}

	assets, err := fs.Sub(frontend.Dist, "dist")
	if err != nil {
		log.Fatalf("读取内嵌前端资源失败: %v", err)
	}

	a := application.New(application.Options{
		// 用户可见名称统一走 app.BrandName（Anvil）：窗口标题、任务栏、通知
		// 都从这里取，避免出现"安装包叫 Anvil、窗口标题还是 MyAgent"的分裂。
		Name:        app.BrandName,
		Description: app.BrandName,
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
		Title:            app.BrandName,
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
// ANVIL_UI_A11Y=1（旧名 MYAGENT_UI_A11Y 仍可用，会记一次弃用警告）时
// 给 WebView2 打开渲染器可访问性：
//   - 读屏软件能读到页面内容（产品可访问性）；
//   - UI 自动化（agent-desktop 的 accessibility tree）能定位到 DOM 元素，
//     而不是只看到一个空的 "Chrome Legacy Window"。
//
// 默认关闭：它会增加渲染进程的常驻开销，普通使用不需要。
func windowsOptions() application.WindowsOptions {
	opts := application.WindowsOptions{}
	if v, _ := app.EnvLookup("UI_A11Y"); v == "1" || strings.EqualFold(v, "true") {
		opts.AdditionalBrowserArgs = []string{"--force-renderer-accessibility"}
	}
	return opts
}

// appRoot 返回应用数据根目录，并在首次启动时执行 MyAgent → Anvil 迁移。
//
// 允许 ANVIL_HOME（旧名 MYAGENT_HOME）覆盖：自动化验证需要把状态放进临时目录，
// 以免污染真实用户数据（也避免两次运行互相干扰）。显式覆盖时不迁移——
// 那个目录是调用方自己管的，不是"用户的旧数据"。
func appRoot() (string, app.BrandMigrationResult, error) {
	if v, _ := app.EnvLookup("HOME"); v != "" {
		if err := os.MkdirAll(v, 0o700); err != nil {
			return "", app.BrandMigrationResult{}, err
		}
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", app.BrandMigrationResult{}, err
		}
		return abs, app.BrandMigrationResult{Status: app.BrandMigrationNone, Target: abs}, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", app.BrandMigrationResult{}, fmt.Errorf("无法定位用户配置目录: %w", err)
	}
	root, res, err := app.PrepareDataRoot(base)
	if err != nil {
		return "", res, err
	}
	return root, res, nil
}

// logBrandMigration 把迁移结果写进启动日志。
//
// 失败不阻断启动：打不开应用比丢一份旧配置更糟。但失败原因必须出现在日志里，
// 否则"更新完数据没了"这条线索只能靠用户回忆。
func logBrandMigration(res app.BrandMigrationResult) {
	switch res.Status {
	case app.BrandMigrationDone:
		log.Printf("已将 %s 的 %d 个配置文件迁移到 %s（旧目录保留）", res.Source, len(res.Files), res.Target)
	case app.BrandMigrationSkipped:
		log.Printf("数据目录已迁移过，跳过（%s）", res.Target)
	case app.BrandMigrationExisting:
		log.Printf("目标数据目录已有数据，未执行迁移（%s）", res.Target)
	case app.BrandMigrationFailed:
		log.Printf("品牌数据迁移失败，将以空数据目录启动: %s", res.Error)
	}
}
