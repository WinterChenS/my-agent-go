# GooeyPi 右侧浏览器实现调研

日期：2026-11-03。仓库：[am-will/gooey-pi](https://github.com/am-will/gooey-pi)。阅读提交：`f63b9cfa707488880eeb9f47984fbf383e397322`，package.json 版本 1.1.21。

这是源码静态分析，没有安装或运行 GooeyPi，没有验证其三端实际行为。它是 Electron + React 桌面应用；右侧浏览器使用 Electron 的真实 webview guest，不是 iframe、截图流，也不是复用外部 Chrome 窗口。以下结论只针对所读提交。

## 1. 网页怎样出现在右侧

`src/components/AgentBrowserLayer.tsx` 在应用壳层长期挂载，每个 agent tab 对应一个 Electron `<webview>`。初始 src 固定为 about:blank，后续导航由主进程控制，避免 React 重渲染导致页面重新加载。

`BrowserPanel.tsx` 负责右侧栏的导航和标签 UI，用 getBoundingClientRect、ResizeObserver、窗口 resize 和补充轮询上报显示槽位的位置。常驻的 AgentBrowserLayer 覆盖到该槽位上，切换面板时隐藏而不卸载 guest。这使网页生命周期不依赖右侧栏当前是否展开。

webview 在 dom-ready 后获取 webContentsId，再通过 preload bridge 回报主进程绑定。主进程持有 tab/session → WebContents 的注册表，React 镜像其状态。

源码：[AgentBrowserLayer](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/src/components/AgentBrowserLayer.tsx)、[BrowserPanel](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/src/components/inspector/BrowserPanel.tsx)。

## 2. Agent 怎样调用浏览器

```text
Pi/OMP 里的 browser_* extension tools
    → 本机 HTTP POST /v1/call + Bearer token
    → AgentBrowserBridge
    → AgentBrowserService（按 session 查找并验证 tab）
    → Electron WebContents / debugger
    → 右侧正在显示的同一个 webview guest
```

`assets/extensions/omp-work-browser.ts` 虽然文件名包含 OMP，但实现了基础 Pi 的工具注册兼容。它注册 browser_tabs、browser_navigate、browser_screenshot、browser_read_page、browser_click、browser_type、browser_press_key、browser_scroll 等工具。工具参数和调用结果经 fetch 发送到环境变量指定的本机地址。

这条浏览器调用链是自定义 extension + HTTP capability bridge，不是依靠 MCP 或 Playwright 来控制右侧网页。不能把项目有 Playwright 测试依赖当作浏览器自动化实现的证据。

源码：[Extension](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/assets/extensions/omp-work-browser.ts)、[AgentBrowserBridge](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/electron/main/browser/agent-bridge.ts)。

## 3. 怎样读取和操作网页

- 页面读取：GuestPageIO 在 isolated world 执行自有脚本。readPageScript 提取交互元素的 tag、name、role 等字段，生成 ref 并保留对应 DOM 引用；text 模式补充页面文本。导航后或元素断开连接时，旧引用报错，要求重新读取。这是自建 DOM 元素快照，不是完整浏览器 accessibility tree。
- 点击：refPointScript 将元素滚动到可见区并计算中心点；GuestInput 通过 guest.debugger.sendCommand 发送 CDP Input.dispatchMouseEvent。鼠标经过目标前可能触发 hover 布局变化，所以服务会在按下前再解析一次 ref。
- 键盘输入：CDP Input.insertText、Input.dispatchKeyEvent，必要时使用 Electron insertText 回退。源码说明直接 sendInputEvent 无法正确路由到 OOPIF guest，因此改走 debugger 的 Input 域。
- 截图：WebContents.capturePage，然后缩放到 CSS viewport，使截图坐标与点击坐标一致。截图是给 agent 观察的工具结果，并非右侧网页显示的来源。
- 光标：真实 CDP 输入动作同时广播 pointer event，React 绘制对应轨迹和点击波纹。这是可视化叠层，不代表正在移动用户系统鼠标。

源码：[GuestInput](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/electron/main/browser/guest-input.ts)、[GuestPageIO](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/electron/main/browser/guest-page-io.ts)、[Page scripts](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/electron/main/browser/page-scripts.ts)、[AgentBrowserService](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/electron/main/browser/agent-service.ts)。

## 4. 登录态、会话和边界

guest 使用 `persist:prime-work-browser` 分区，持久化浏览器状态。该常量由应用内 webviews 共享：tab 的 session 授权隔离不等于 cookies 按 session 隔离，不能把两者混为一谈。

webpreferences 明确关闭 nodeIntegration，开启 contextIsolation 和 sandbox。服务持有 approved guest 列表，按 session 校验 tab 所有权，动作串行排队，撤销或解绑用 generation 阻止排队中的旧授权继续执行。

CapabilityBridge 监听 127.0.0.1 的随机端口，生成随机 Bearer token，注入 agent 运行环境；检查有效期、请求体与频率，在 dispatch 前再次检查撤销状态，并拒绝带 Origin 的请求。它提供值得参考的边界机制，但本次没有做完整安全审计。

源码：[CapabilityBridge](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/electron/main/lib/capability-bridge.ts)、[Browser API types](https://github.com/am-will/gooey-pi/blob/f63b9cfa707488880eeb9f47984fbf383e397322/src/types/api/browser.ts)。

## 5. 对 Wails 项目的结论

可以借鉴：浏览器服务与 UI 分离、session/tab 授权、常驻 guest 生命周期、DOM ref + 截图两种观察方式、串行操作、超时撤销、坐标归一化和光标反馈。桥接服务可用 Go 实现；Pi 侧可以沿用薄 extension HTTP 思路，或使用我们已有的 Harness MCP，两者择一，避免重复维护相同工具。

不能直接搬用：Electron webview、WebContents、debugger、capturePage。它们依赖 Electron 随附的 Chromium，不是普通 React 组件，Wails 的系统 WebView 不会识别同样的 guest 宿主能力。保留 Wails 时，需要实现三端原生浏览器视图与控制 adapter，或评估独立浏览器组件/画面流路线；不能据此承诺零改动移植。

因此不自动改变用户已选的 Wails。下一步值得做的原型是：定义 BrowserHost 接口，尝试在右侧区域挂载独立网页视图，完成 navigate、readPage、click、type、screenshot 和缩放测试，再分别确认 macOS/Linux 的等价能力。Windows 的成功不能当作三端已验证。

如果后续将“尽快得到 GooeyPi 同款原生嵌入浏览器”置于 Go 桌面宿主和基础包体积之前，Electron 壳 + Go Harness 是可以重新比较的方案，但需用户明确调整选型。

补充：Electron 官方当前不建议新项目使用 `<webview>`，推荐评估 WebContentsView 等替代方式。因此即使改用 Electron，也应借鉴架构后选择当前适合的 API，而不是原样复制。

官方参考：[Electron webview](https://www.electronjs.org/docs/latest/api/webview-tag)。
