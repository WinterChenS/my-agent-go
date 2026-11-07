import React from "react";
import ReactDOM from "react-dom/client";
import App from "./app/App";
import { initialRouteFrom } from "./app/navigation";
import { initLocale } from "./i18n";
import { initTheme } from "./theme/theme";
import "./index.css";

// 主题与语言必须在 React 挂载之前定下来。
//
// 顺序是有原因的：先应用主题再渲染，深色系统下不会闪一帧白底；
// 先确定语言再渲染，首屏不会先出现中文再跳到英文。
// 两者都只读本地镜像（localStorage），权威偏好在后端，页面加载后再对齐。
initTheme();
initLocale();

// 初始路由：anvil:// 逻辑地址（搜索结果、诊断、宿主传入的深链接）与页面路径
// 走同一个解析器。桌面壳本身没有可分享 URL，所以这里只读一次启动参数；
// 不认识的地址回落到工作台，宁可落在首页也不要白屏。
const initialPath = initialRouteFrom(
  typeof window === "undefined" ? "" : window.location.search,
  typeof window === "undefined" ? "" : window.location.hash
);

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App initialPath={initialPath} />
  </React.StrictMode>
);
