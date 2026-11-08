// 侧栏底部（D1）：Pi 运行时状态 + 当前本地工作区。
//
// 版式来自原型的 `.runtime-mini` 与 `.user-chip`：一个带 π 字标的运行时行
// （小圆点表示状态）+ 一个"头像 + 名称 + 次要信息"的工作区行。
//
// 纪律：这里**只显示后端给的事实**。原型那两行写着"本机运行中 · 4 个任务"、
// "个人 · 本地工作区"——那些是设计稿的示例数据，不是真实数字。读不到 Pi 版本
// 就显示"未检测到 Pi"，读不到工作区就显示"尚未选择工作区"，绝不编一个 4。

import type { Status, Workspace } from "../../types";
import { t } from "../../i18n";

export interface RuntimeFooterProps {
  status: Status | null;
  workspace: Workspace | null;
  /** 点运行时行：去设置里的运行环境段。 */
  onOpenRuntime: () => void;
  /** 点工作区行：去设置里的工作区段。 */
  onOpenWorkspace: () => void;
}

export function RuntimeFooter({ status, workspace, onOpenRuntime, onOpenWorkspace }: RuntimeFooterProps) {
  const piVersion = status?.piVersion ?? "";
  const missing = piVersion === "";
  const workspaceName = workspace?.name ?? t("shell.noWorkspace");
  // 没有工作区时不拿占位文案的首字当头像（那会显示成"尚"这种半截字）。
  const avatar = workspace ? workspaceName.trim().slice(0, 1).toUpperCase() : "—";

  return (
    <>
      <button
        type="button"
        className="anvil-runtime"
        onClick={onOpenRuntime}
        title={missing ? t("shell.piMissing") : t("shell.piStatus")}
      >
        <span className="anvil-runtime-icon" aria-hidden>
          π
        </span>
        <span className="anvil-runtime-copy">
          <span className="anvil-runtime-name">{t("shell.piAgent")}</span>
          <span className="anvil-runtime-meta">
            <span className="anvil-runtime-dot" data-missing={missing ? "true" : "false"} aria-hidden />
            {missing ? t("shell.piMissing") : `${t("shell.piStatus")} · v${piVersion}`}
          </span>
        </span>
      </button>

      <button
        type="button"
        className="anvil-user-chip"
        onClick={onOpenWorkspace}
        title={workspace?.canonicalPath ?? t("shell.noWorkspace")}
      >
        <span className="anvil-avatar" aria-hidden>
          {avatar}
        </span>
        <span className="min-w-0">
          <span className="anvil-user-name">{workspaceName}</span>
          <span className="anvil-user-meta">{workspace?.canonicalPath ?? t("shell.workspaceLabel")}</span>
        </span>
      </button>
    </>
  );
}
