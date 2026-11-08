// 干净对照源码：只用语义 token、文案走 catalog、没有写死指标与演示数据。
import { t } from "../i18n";

export function CleanCard({ count }: { count: number }) {
  return (
    <div className="rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] text-[color:var(--anvil-foreground)]">
      <span>{t("nav.settings")}</span>
      <span>{count}</span>
    </div>
  );
}
