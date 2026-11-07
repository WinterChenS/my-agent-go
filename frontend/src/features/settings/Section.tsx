// 设置分区的统一外壳（U06）。
//
// 每个分区自己负责"这一块的真相是什么"（加载/错误/不可用），外壳只负责
// 标题、说明与可断言的边界：
//   - `data-testid=settings-<id>`：测试与验收矩阵按分区定位；
//   - `data-state`：这个分区当前给用户的结论（loading / success / error /
//     unavailable）。更新分区永远是 unavailable——F09 不存在，界面不能假装
//     那里有东西在跑。

import type { ReactNode } from "react";
import { t } from "../../i18n";

export type SectionState = "loading" | "success" | "error" | "unavailable";

export interface SettingsSectionProps {
  id: string;
  titleKey: string;
  hintKey: string;
  state?: SectionState;
  children: ReactNode;
}

export function SettingsSection({ id, titleKey, hintKey, state = "success", children }: SettingsSectionProps) {
  return (
    <section
      data-testid={`settings-${id}`}
      data-state={state}
      aria-labelledby={`settings-${id}-title`}
      className="rounded-xl border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-3.5"
    >
      <header className="mb-2">
        <h2
          id={`settings-${id}-title`}
          className="text-sm font-medium text-[color:var(--anvil-foreground)]"
        >
          {t(titleKey)}
        </h2>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t(hintKey)}
        </p>
      </header>
      {children}
    </section>
  );
}

// 设置页共用的控件样式。
//
// 直接颜色会绕过 U00 的语义 token（深色/浅色各写一套），所以这里只用
// background/surface/foreground/muted/border/accent/focus 这些 token。
export const BUTTON_CLASS =
  "rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-50 disabled:hover:bg-transparent focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]";

export const INPUT_CLASS =
  "rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1.5 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]";

export const ROW_CLASS =
  "rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2.5 py-2";

export const LABEL_CLASS = "text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]";

export const MONO_CLASS = "break-all font-mono text-[10px] text-[color:var(--anvil-muted)]";
