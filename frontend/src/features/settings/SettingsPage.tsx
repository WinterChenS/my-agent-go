// 设置页（U06 / UI10）。
//
// 分区一次只说自己那块的事实，页面本身不做业务判断：
//   通用（语言/主题，CAS） → 模型（列表/测试/当前） → 权限（真实上限与工作区信任）
//   → Pi（候选/安装/升级/回滚） → 更新（F09 未实现，只说不可用）
//   → 安全（凭证存在性、明文兜底风险） → 诊断（导出入口）。
//
// 这个页面从不假设运行时就绪：缺 Pi、缺凭证、缺更新服务时，每一块都给出
// 真实状态与下一步，而不是白屏或"看起来好了"。

import type { PageProps } from "../../app/navigation";
import { t } from "../../i18n";
import { DiagnosticsExportPanel } from "../observability/DiagnosticsExport";
import { GeneralSettings } from "./GeneralSettings";
import { ModelSettings } from "./ModelSettings";
import { PermissionSettings } from "./PermissionSettings";
import { PiSettings } from "./PiSettings";
import { SecuritySettings } from "./SecuritySettings";
import { UpdateSettings } from "./UpdateSettings";
import { SettingsSection, BUTTON_CLASS } from "./Section";

export function SettingsPage({ onNavigate }: PageProps) {
  return (
    <div className="mx-auto max-w-3xl space-y-4 px-4 py-5">
      <header>
        <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">{t("settings.title")}</h1>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("settings.subtitle")}
        </p>
      </header>

      <GeneralSettings />
      <ModelSettings />
      <PermissionSettings />
      <PiSettings />
      <UpdateSettings />
      <SecuritySettings />

      <SettingsSection
        id="diagnostics"
        titleKey="settings.diagnostics.title"
        hintKey="settings.diagnostics.hint"
      >
        <div className="space-y-2">
          <DiagnosticsExportPanel />
          <button type="button" className={BUTTON_CLASS} onClick={() => onNavigate("/observability")}>
            {t("settings.diagnostics.openPage")}
          </button>
        </div>
      </SettingsSection>
    </div>
  );
}
