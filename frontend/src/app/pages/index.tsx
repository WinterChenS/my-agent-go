// 壳层内的页面组件（U01）。
//
// 一个文件装下路由需要的页面：它们都很薄——读壳层上下文、把真实数据交给
// 已有的分区组件，自己不复制业务判断。U03–U07 会逐页替换成各自的产品页；
// 在那之前，这里保证每个路由都渲染**真实数据或明确状态**，没有占位卡片、
// 没有假计数、没有"点了就成功"的动作。
//
// 连接器与定时任务的页面**只有** shared not_implemented 状态：
// C08/C09 合入前不得出现原型卡片、connected 徽标或任何成功动作。

import { AsyncState } from "../../components/states/AsyncState";
import { StatusBadge } from "../../components/status/StatusBadge";
import { formatError, t } from "../../i18n";
import type { CallError } from "../../types";
import { SkillsSection } from "../../features/skills/SkillsSection";
import { ExtensionsSection } from "../../features/skills/ExtensionsSection";
import { MCPSection } from "../../features/mcp/MCPSection";
import { ToolsSection } from "../../features/tools/ToolsSection";
import { RunsView } from "../../features/runs/RunsView";
import { SettingsView } from "../../features/settings/SettingsView";
import { WorkbenchView } from "../../features/workbench/WorkbenchView";
import { useShell } from "../shell";

// ---------------------------------------------------------------- 对话
//
// 对话页在 U02 换成了 features/chat/ChatPage（消息流、审批、澄清、附件、右栏四分区）。
// 这里只做转出：页面自己的状态与动作都在那个模块里，路由文件不再保存一份。

export { ChatPage } from "../../features/chat/ChatPage";
export { LibraryPage } from "../../features/library/LibraryPage";
export { MemoryPage } from "../../features/memory/MemoryPage";
// ---------------------------------------------------------------- Agent
//
// Agent 页在 U03 换成了 features/agents/AgentsPage（列表 + 单 Agent 编辑器：
// CAS 保存、冲突差异、权限上限、复制/删除、来源与 unavailable 缺口）。
// 这里只做转出：路由文件不再保存一份页面状态。

export { AgentsPage } from "../../features/agents/AgentsPage";

// ---------------------------------------------------------------- Skill / MCP / 资源库

/** Skill 页：托管技能 + 扩展（真实启停与试运行仍由 C03 服务判定）。 */
export function SkillsPage() {
  const shell = useShell();
  return (
    <div className="mx-auto max-w-3xl space-y-5 px-4 py-5">
      <SkillsSection
        skills={shell.skills}
        revision={shell.skillRevision}
        pending={shell.skillsPending}
        activeRun={shell.skillsActiveRun}
        runtimeSkills={shell.status?.runtimeSkills ?? []}
        workspaceId={shell.currentWorkspace?.id ?? ""}
        onChanged={shell.refreshSkills}
      />
      <ExtensionsSection
        extensions={shell.extensions}
        revision={shell.extensionRevision}
        onChanged={shell.refreshSkills}
      />
    </div>
  );
}

/** MCP 页：MCP 服务与工具目录（连接状态来自真实诊断）。 */
export function MCPPage() {
  return (
    <div className="mx-auto max-w-3xl space-y-5 px-4 py-5">
      <MCPSection />
      <ToolsSection />
    </div>
  );
}

/** 运行记录：本次运行的事件序列（右栏"详情"的展开）。 */
export function RunsPage() {
  const shell = useShell();
  return <RunsView ui={shell.ui} workspace={shell.currentWorkspace} />;
}

/** 设置：运行环境、工作区信任、语言与主题。 */
export function SettingsPage() {
  const shell = useShell();
  if (!shell.settings) {
    return (
      <div className="mx-auto max-w-3xl px-4 py-5">
        <AsyncState
          phase={shell.backend.loading ? "loading" : "error"}
          error={shell.backend.error ?? undefined}
          onRetry={shell.refreshProfiles}
        />
      </div>
    );
  }
  return (
    <SettingsView
      settings={shell.settings}
      status={shell.status}
      workspaces={shell.workspaces}
      saving={shell.savingSettings}
      error={shell.actionError}
      onSave={shell.saveSettings}
      onTrust={shell.trustWorkspace}
    />
  );
}

// ---------------------------------------------------------------- 未实现（共享状态）

export interface NotImplementedPageProps {
  feature: "connectors" | "schedules";
}

/**
 * 共享的 not_implemented 页面。
 *
 * 它故意**没有**动作按钮：C08/C09 合入前，任何"连接/立即运行"的按钮都只能
 * 伪造结果，而伪造成功是规格明确禁止的（§1、§5.6、§5.7）。这里给出真实的
 * 状态徽标与原因，让用户知道"这页还没做"，而不是"这个连接器连不上"。
 */
export function NotImplementedPage({ feature }: NotImplementedPageProps) {
  return (
    <div className="mx-auto max-w-2xl space-y-3 px-4 py-8">
      <div className="flex items-center gap-2">
        <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
          {t(`nav.${feature}`)}
        </h1>
        <StatusBadge status="not_implemented" />
      </div>
      <p className="text-xs leading-relaxed text-[color:var(--anvil-muted)]">
        {t(`notImplemented.${feature}`)}
      </p>
      <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t("notImplemented.hint")}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------- 错误边界

/**
 * 页面渲染失败时的兜底。
 *
 * 显式保存错误码与文案：白屏是这个应用里最没用的失败形态。
 */
export function PageError({ error }: { error: CallError }) {
  return (
    <div className="mx-auto max-w-2xl px-4 py-8">
      <AsyncState phase="error" error={error} />
      <p className="mt-2 font-mono text-[10px] text-[color:var(--anvil-muted)]">{formatError(error)}</p>
    </div>
  );
}
