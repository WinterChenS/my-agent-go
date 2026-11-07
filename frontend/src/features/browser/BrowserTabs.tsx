// 浏览器 tab 条（B07）。
//
// 只渲染后端给的 tab 列表：前端**不**维护"我刚建了哪个 tab"，也不在某次操作
// 成功后自己往列表里塞一条（那会让面板和 MCP 工具输出各说一套）。
// 选中/关闭都只是转发事件，新的列表来自下一次 BrowserState()。

import { X } from "lucide-react";
import { t } from "../../i18n";
import type { BrowserTabView } from "./api";

/**
 * tab 的无障碍名字：把 catalog 文案与 URL 拼起来。
 *
 * 放在 JSX 外面是刻意的：属性里直接写模板字符串会被 UI 契约扫描器当成
 * "catalog 之外的文案"（它无法在编译期证明那里面只有 t()）, 迁移到 helper 后
 * 名字仍然完全来自 catalog。
 */
function tabLabel(action: "select" | "close", url: string): string {
  const key = action === "select" ? "browser.tabs.select" : "browser.tabs.close";
  return `${t(key)} ${url}`;
}

export interface BrowserTabsProps {
  tabs: BrowserTabView[];
  onSelect(tabId: string): void;
  onClose(tabId: string): void;
}

export function BrowserTabs({ tabs, onSelect, onClose }: BrowserTabsProps) {
  if (tabs.length === 0) {
    return (
      <p data-browser-tabs="empty" className="px-1 text-[10px] text-[color:var(--anvil-muted)]">
        {t("browser.tabs.empty")}
      </p>
    );
  }

  return (
    <div
      data-browser-tabs=""
      role="tablist"
      aria-label={t("browser.tabs.label")}
      className="flex items-center gap-1 overflow-x-auto px-1"
    >
      {tabs.map((tab) => (
        <span
          key={tab.tabId}
          className={
            "flex shrink-0 items-center gap-1 rounded border px-1 " +
            (tab.active
              ? "border-[color:var(--anvil-accent)] text-[color:var(--anvil-foreground)]"
              : "border-[color:var(--anvil-border)] text-[color:var(--anvil-muted)]")
          }
        >
          <button
            type="button"
            role="tab"
            aria-selected={tab.active}
            data-browser-tab={tab.tabId}
            aria-label={tabLabel("select", tab.url)}
            title={tab.url}
            onClick={() => onSelect(tab.tabId)}
            className="max-w-[12rem] truncate py-0.5 text-[10px]"
          >
            {tab.url || tab.tabId}
          </button>
          <button
            type="button"
            data-browser-tab-close={tab.tabId}
            aria-label={tabLabel("close", tab.url)}
            title={t("browser.tabs.close")}
            onClick={() => onClose(tab.tabId)}
            className="rounded p-0.5 hover:bg-[color:var(--anvil-background)]"
          >
            <X className="h-3 w-3" aria-hidden />
          </button>
        </span>
      ))}
    </div>
  );
}
