// 故意违规夹具：用了 catalog 里不存在的消息 key（message-key-drift）。
import { t } from "../i18n";

export function DriftedLabels() {
  return (
    <div>
      <span>{t("nav.settingss")}</span>
      <span>{t("errors.NO_SUCH_CODE")}</span>
      <span>{t("nav.settings")}</span>
    </div>
  );
}
