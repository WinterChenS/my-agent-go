// 设置路由的分区定位（UX1）。
//
// 对话页的阻塞面板用 `/settings?section=pi|permissions|model` 把人送到这里。
// 落地这一跳必须真的到达那个分区：滚到它、给它焦点，并说明"你是被哪一项带来的"。
// 否则用户点完"去设置"只会看到一个长长的设置页，还得自己找。
//
// 为什么不认识的 section 不猜：猜错分区比停在页首更糟——用户会以为那一项
// 已经处理完了。找不到元素就什么都不做，也不显示跳转说明。

import { useEffect, useState } from "react";
import { hasKey, t } from "../i18n";
import { SettingsPage as SettingsPageView } from "../features/settings/SettingsPage";
import type { PageProps } from "./navigation";

/** 分区标题来自 catalog；后端/URL 里的陌生 id 原样显示（它是数据，不是文案）。 */
function sectionLabel(section: string): string {
  const key = `settings.${section}.title`;
  return hasKey(key) ? t(key) : section;
}

export function SettingsRoute(pageProps: PageProps) {
  const section = (pageProps.route?.query.get("section") ?? "").trim();
  const [focusedSection, setFocusedSection] = useState("");

  useEffect(() => {
    setFocusedSection("");
    if (!section) return;
    const el = document.querySelector(`[data-testid="settings-${section}"]`);
    if (!(el instanceof HTMLElement)) return;

    // tabindex=-1：可编程聚焦，但不把整个分区塞进 Tab 序列。
    el.setAttribute("tabindex", "-1");
    el.setAttribute("data-focused", "true");
    el.focus?.();
    el.scrollIntoView?.({ block: "start" });
    setFocusedSection(section);

    return () => {
      el.removeAttribute("data-focused");
      el.removeAttribute("tabindex");
    };
  }, [section]);

  return (
    <div className="space-y-2">
      {focusedSection ? (
        <p
          data-settings-jump-notice=""
          role="status"
          className="mx-auto mt-3 max-w-3xl rounded-md border border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] px-3 py-1.5 text-[11px] text-[color:var(--anvil-accent)]"
        >
          {t("settings.jumpNotice", { section: sectionLabel(focusedSection) })}
        </p>
      ) : null}
      <SettingsPageView {...pageProps} />
    </div>
  );
}
