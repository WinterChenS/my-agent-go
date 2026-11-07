// 壳层内的页面组件（U01）。
//
// 一个文件装下路由需要的页面：它们都很薄——读壳层上下文、把真实数据交给
// 已有的分区组件，自己不复制业务判断。U03–U07 会逐页替换成各自的产品页；
// 在那之前，这里保证每个路由都渲染**真实数据或明确状态**，没有占位卡片、
// 没有假计数、没有"点了就成功"的动作。
//
// 连接器与定时任务的页面**只有** shared not_implemented 状态：
// C08/C09 合入前不得出现原型卡片、connected 徽标或任何成功动作。

import { useEffect, useState } from "react";
import { AsyncState } from "../../components/states/AsyncState";
import { StatusBadge } from "../../components/status/StatusBadge";
import { formatError, t } from "../../i18n";
import type { CallError } from "../../types";
import { SkillsSection } from "../../features/skills/SkillsSection";
import { ExtensionsSection } from "../../features/skills/ExtensionsSection";
import { MCPSection } from "../../features/mcp/MCPSection";
import { ToolsSection } from "../../features/tools/ToolsSection";
import { ResourcesView } from "../../features/resources/ResourcesView";
import { MemoryView } from "../../features/memory/MemoryView";
import { RunsView } from "../../features/runs/RunsView";
import { SettingsView } from "../../features/settings/SettingsView";
import { ProfileEditor } from "../../features/profiles/ProfileEditor";
import { WorkbenchView } from "../../features/workbench/WorkbenchView";
import { useShell } from "../shell";
import type { PageProps } from "../navigation";

// ---------------------------------------------------------------- 对话

/**
 * 对话页：路由里带了会话 id 就是那个会话；没带就让人先选一个。
 *
 * 不在挂载时自动建会话：用户只是点开"对话"入口，不该凭空多出一条空会话。
 */
export function ChatPage({ onNavigate, route }: PageProps) {
  const shell = useShell();
  const sessionId = route?.params.id ?? "";

  if (!sessionId) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-10">
        <AsyncState
          phase="empty"
          emptyTitle={t("shell.emptyHistory")}
          emptyHint={t("shell.emptyHistoryHint")}
          action={
            <button
              type="button"
              onClick={() => void shell.onNewSession()}
              className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
            >
              {t("shell.newSession")}
            </button>
          }
        />
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <header className="flex shrink-0 items-center justify-between border-b border-[color:var(--anvil-border)] px-4 py-2">
        <span className="font-mono text-[11px] text-[color:var(--anvil-muted)]">{sessionId}</span>
        <button
          type="button"
          onClick={() => onNavigate("/")}
          className="text-[11px] text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-foreground)]"
        >
          {t("nav.workbench")}
        </button>
      </header>
      <div className="min-h-0 flex-1">
        <WorkbenchView
          ui={shell.ui}
          input={shell.input}
          onInput={shell.onInput}
          onSend={shell.onSend}
          onStop={shell.onStop}
          onDecide={shell.onDecide}
          busy={shell.busy}
          workspace={shell.currentWorkspace}
          onGoSettings={() => onNavigate("/settings")}
        />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- Agent

/** Agent 页：单个 Profile 的产品化编辑（U03 会接管更完整的字段）。 */
export function AgentsPage({ route }: PageProps) {
  const shell = useShell();
  const selectedId = route?.query.get("agent") ?? "";
  const [selected, setSelected] = useState<string>(selectedId);
  const selectedProfile = shell.profiles.find((p) => p.id === selected) ?? null;

  useEffect(() => {
    setSelected(selectedId);
  }, [selectedId]);

  if (shell.backend.loading && shell.profiles.length === 0) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-6">
        <AsyncState phase="loading" />
      </div>
    );
  }

  if (shell.backend.error) {
    return (
      <div className="mx-auto max-w-2xl px-4 py-6">
        <AsyncState phase="error" error={shell.backend.error} onRetry={shell.refreshProfiles} />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-2xl space-y-4 px-4 py-5">
      <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">{t("nav.agents")}</h1>
      {shell.profiles.length === 0 ? (
        <AsyncState phase="empty" emptyTitle={t("state.emptyTitle")} emptyHint={t("state.emptyHint")} />
      ) : (
        <ul className="space-y-1">
          {shell.profiles.map((p) => (
            <li key={p.id}>
              <button
                type="button"
                onClick={() => setSelected(p.id)}
                aria-current={p.id === selected ? "true" : undefined}
                className="flex w-full items-center justify-between rounded-md border border-[color:var(--anvil-border)] px-2.5 py-2 text-left hover:bg-[color:var(--anvil-surface)]"
              >
                <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">
                  {p.name || p.id}
                </span>
                <span className="shrink-0 font-mono text-[10px] text-[color:var(--anvil-muted)]">
                  {p.modelRef || t("common.notAvailable")}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {selectedProfile ? (
        <div className="rounded-lg border border-[color:var(--anvil-border)] p-3">
          <ProfileEditor
            profile={selectedProfile}
            workspaceId={shell.currentWorkspace?.id ?? ""}
            onSaved={() => shell.refreshProfiles()}
            onReloaded={() => shell.refreshProfiles()}
          />
        </div>
      ) : null}
    </div>
  );
}

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

/** 资源库：模型、Skill、MCP、扩展与配置档（U05 会拆出产物与记忆分区）。 */
export function LibraryPage() {
  const shell = useShell();
  return (
    <ResourcesView
      profiles={shell.profiles}
      workspaceId={shell.currentWorkspace?.id ?? ""}
      models={shell.models}
      modelOptions={shell.modelOptions}
      modelRevision={shell.modelRevision}
      currentModel={shell.currentModel}
      skills={shell.skills}
      skillRevision={shell.skillRevision}
      skillsPending={shell.skillsPending}
      skillsActiveRun={shell.skillsActiveRun}
      runtimeSkills={shell.status?.runtimeSkills ?? []}
      extensions={shell.extensions}
      extensionRevision={shell.extensionRevision}
      onChanged={shell.refreshProfiles}
      onModelsChanged={shell.refreshModels}
      onSkillsChanged={shell.refreshSkills}
    />
  );
}

/** 记忆：跨会话保留的上下文（C05）。 */
export function MemoryPage() {
  return <MemoryView />;
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
