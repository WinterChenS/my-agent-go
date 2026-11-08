// Command myagent 是桌面宿主（Wails v3）。
//
// 它只负责壳：窗口、服务绑定、事件出口。运行时判断全在 internal 各包里，
// 这样"缺 Pi 就拒绝启动""审批超时即拒绝"这类结论可以被单测覆盖，不依赖 GUI。
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"my-agent-go/frontend"
	"my-agent-go/internal/app"
	"my-agent-go/internal/browser"
	"my-agent-go/internal/browser/native"
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

	win := a.Window.NewWithOptions(application.WebviewWindowOptions{
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

	// 右侧浏览面板：只有真的挂上原生宿主才注册桥接层（见 registerBrowserPanel）。
	registerBrowserPanel(desktop, a, win, root)

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

const (
	// browserProfileDir 是浏览器视图的 WebView2 user data folder。
	//
	// 它**不能**与主 WebView 共用（native.NewWindowsHost 会拒绝空值）：两个 WebView2
	// 环境共用一个目录会让缓存/登录态/进程模型互相干扰。
	browserProfileDir = "browser-profile"
	// browserAttachTimeout 是“等窗口句柄 + 挂载原生视图”的总预算。
	//
	// 超过它就不再等：面板会以“没有原生宿主”的诚实状态显示，而不是无限期地
	// 在后台盯着窗口。
	browserAttachTimeout = 30 * time.Second
	// browserAttachPoll 是等主窗口句柄的轮询间隔。
	browserAttachPoll = 100 * time.Millisecond
	// browserActionTimeout 是单次浏览器动作（导航/接管）的上限。
	//
	// 与 internal/app 的绑定层同一量级：面板按钮不该无限期转圈。
	browserActionTimeout = 20 * time.Second
)

// registerBrowserPanel 把右侧浏览面板的桥接层接上真实宿主与服务。
//
// 顺序是硬性的：
//  1. 等主窗口句柄。Wails 在 a.Run() 之后才创建原生窗口，之前 NativeWindow() 是 nil；
//  2. 在 UI 线程上 Attach 原生子窗口（跨线程创建子窗口在 WebView2 下是隐性坑，
//     native 宿主会直接拒绝，见 host_windows.go Attach）；
//  3. 立刻 SetVisible(false)。Attach 建出来的子窗口默认可见且覆盖整个客户区，
//     不等面板上报几何就显示会盖住整个应用（句柄在、页面空白、用户点击全被吃掉）；
//  4. 最后才 SetBrowserBridge：桥接层一装上，面板就会读 BrowserState()。
//
// 任何一步失败都**不**注册一个“看起来可用”的空桥接：写日志，保持 Desktop 的
// 默认桥（available=false + 具体原因），面板会如实说明右侧没有原生视图。
// 非 Windows 平台在（1）之后的 NewWindowsHost 就会失败：B02/B03 尚未落地，
// 这里的失败原因就是面板显示的原因，不是被吞掉的错误。
func registerBrowserPanel(desktop *app.Desktop, a *application.App, win *application.WebviewWindow, root string) {
	if win == nil {
		log.Printf("浏览器面板不可用：没有主窗口，右侧不显示原生网页")
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), browserAttachTimeout)
		defer cancel()

		handle := waitForNativeHandle(ctx, win)
		if handle == nil {
			log.Printf("浏览器面板不可用：等不到主窗口句柄（NativeWindow() 一直是 nil），右侧不显示原生网页")
			return
		}
		parent := browser.NewNativeParent(handle, "wails-main-window")
		host, err := native.NewWindowsHost(parent, native.HostOptions{
			ProfilePath: filepath.Join(root, browserProfileDir),
		})
		if err != nil {
			// 这里就是“本平台没有原生宿主”的处所（B02/B03）。不构造假对象。
			log.Printf("浏览器面板不可用：%v", err)
			return
		}

		// Attach 必须在拥有主窗口 HWND 的 UI 线程上跑：Wails 的 UI 线程就是它。
		if err := application.InvokeSyncWithError(func() error { return host.Attach(ctx, parent) }); err != nil {
			log.Printf("浏览器面板不可用：挂载原生宿主失败: %v", err)
			return
		}
		// 先隐藏：面板还没有上报几何，此时子窗口不该盖住任何东西。
		if err := application.InvokeSyncWithError(func() error { return host.SetVisible(ctx, false) }); err != nil {
			log.Printf("浏览器面板：初始隐藏原生视图失败（面板上报几何时会再试）: %v", err)
		}

		// 写动作走真实的策略授权器（BR07.tool）：交互默认 ask，用户可给本会话/站点范围
		// 的许可，站点与文档身份进审批指纹。授权器为空（装配失败）时 browser.Service
		// 会拒绝所有写动作：默认拒绝，绝不能换成“总是同意”的替身。
		authorizer := desktop.BrowserAuthorizer()
		if authorizer == nil {
			log.Printf("浏览器写动作保持默认拒绝：授权器未装配（策略服务/审批队列不可用）")
		}
		svc, err := browser.NewService(browser.ServiceOptions{
			Backend:       host,
			Authorizer:    authorizer,
			ActionTimeout: browserActionTimeout,
		})
		if err != nil {
			log.Printf("浏览器面板不可用：浏览器服务构造失败: %v", err)
			_ = host.Detach(ctx)
			return
		}

		desktop.SetBrowserBridge(app.NewBrowserBridge(app.BrowserBridgeOptions{
			Surface: host,
			Service: svc,
			// 面板跟随当前 run 的会话（tab 的归属是会话，不是进程）。
			ExecProvider: desktop.BrowserExecutionContext,
		}))

		a.OnShutdown(func() {
			svc.Close()
			detachCtx, detachCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer detachCancel()
			if err := host.Detach(detachCtx); err != nil {
				log.Printf("浏览器面板：拆卸原生宿主失败: %v", err)
			}
		})

		log.Printf("浏览器面板已装配：右侧原生视图挂载在主窗口下（profile=%s）",
			filepath.Join(root, browserProfileDir))
	}()
}

// waitForNativeHandle 等主窗口句柄就绪（NativeWindow 在窗口创建前返回 nil）。
func waitForNativeHandle(ctx context.Context, win *application.WebviewWindow) any {
	ticker := time.NewTicker(browserAttachPoll)
	defer ticker.Stop()
	for {
		if h := win.NativeWindow(); h != nil {
			return h
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
