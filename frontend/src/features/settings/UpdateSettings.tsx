// 应用更新设置（U06 的硬约束：F09 还不存在）。
//
// F09 的 `UpdateStatus/CheckForUpdate/DownloadUpdate/UpdateBlockers/ExitAndUpdate`
// 都还没有绑定。所以这一节**只能**说"不可用"和原因：
//   - 不给进度条（没有真实进度可显示，画一条就是在演）；
//   - 不给"退出并更新"（没有 blocker 评估、没有 helper、没有事务）；
//   - 不给渠道选择（没有清单可对照，选了也不能验证）。
// 等 F09 落地后，这里按 UpdateStatus 的真实状态渲染，并"下次启动安装"而不是
// 当场重启。

import { StatusBadge } from "../../components/status/StatusBadge";
import { t } from "../../i18n";
import { SettingsSection } from "./Section";

export function UpdateSettings() {
  return (
    <SettingsSection
      id="update"
      titleKey="settings.update.title"
      hintKey="settings.update.hint"
      state="unavailable"
    >
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge status="unavailable" />
          <span className="text-[11px] text-[color:var(--anvil-muted)]">
            {t("settings.update.unavailableReason")}
          </span>
        </div>
        <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("settings.update.noFakeActions")}
        </p>
      </div>
    </SettingsSection>
  );
}
