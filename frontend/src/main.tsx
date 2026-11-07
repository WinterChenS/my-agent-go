import React from "react";
import ReactDOM from "react-dom/client";
import App from "./app/App";
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

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
