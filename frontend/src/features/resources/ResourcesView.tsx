// 资源库：模型 + 技能 + 扩展 + MCP + 工具目录 + 配置档。
//
// 模型（C02）、技能与扩展（C03）、MCP 与工具目录（C04）、配置档（C01）都是真的能用。
// MCP 与工具目录自己加载后端数据：它们的"已连接/可用"必须来自 Pi 的诊断，
// 不能由父组件的静态 props 决定。

import { useState } from "react";
import { Boxes, Plus } from "lucide-react";
import type {
  CallError,
  CurrentModelResult,
  Extension,
  ModelDefinition,
  ModelOptions,
  Profile,
  SkillResource,
} from "../../types";
import { api } from "../../api";
import { toCallError } from "../../app/useBackend";
import { ProfileEditor } from "../profiles/ProfileEditor";
import { ModelsSection } from "../models/ModelsSection";
import { SkillsPage } from "../skills/SkillsPage";
import { McpPage } from "../mcp/McpPage";
import { EmptyState } from "../../components/StateViews";

export interface ResourcesViewProps {
  profiles: Profile[];
  workspaceId: string;
  models: ModelDefinition[];
  modelOptions: ModelOptions;
  modelRevision: number;
  currentModel: CurrentModelResult | null;
  skills: SkillResource[];
  skillRevision: number;
  skillsPending: boolean;
  skillsActiveRun: boolean;
  /**
   * 运行中的 worker **显式加载的托管技能**（--skill，RES02 的“旧 run 保持快照”）。
   * 不含 Pi 自动发现的技能：那份清单没有可信来源。
   */
  runtimeSkills: string[];
  extensions: Extension[];
  extensionRevision: number;
  onChanged: () => void;
  onModelsChanged: () => void;
  onSkillsChanged: () => void;
}

export function ResourcesView({
  profiles,
  workspaceId,
  models,
  modelOptions,
  modelRevision,
  currentModel,
  skills,
  skillRevision,
  skillsPending,
  skillsActiveRun,
  runtimeSkills,
  extensions,
  extensionRevision,
  onChanged,
  onModelsChanged,
  onSkillsChanged,
}: ResourcesViewProps) {
  const [editing, setEditing] = useState<Profile | null | undefined>(undefined);
  const [loadingId, setLoadingId] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<CallError | null>(null);

  const close = () => setEditing(undefined);

  // 打开已有配置档时重新读一次磁盘。
  //
  // 列表里的对象是上次加载时的快照。外部改过之后直接拿它去编辑，人就是对着
  // 陈旧的值做决定——保存虽有 CAS 兜底不会丢数据，但"看到的不是真的"本身就是缺陷。
  const openExisting = async (p: Profile) => {
    setLoadError(null);
    setLoadingId(p.id);
    try {
      const res = await api.profile(p.id);
      if (res.error) setLoadError(res.error);
      else setEditing(res.item);
    } catch (e) {
      setLoadError(toCallError(e));
    } finally {
      setLoadingId(null);
    }
  };

  return (
    <div className="mx-auto max-w-3xl space-y-5 px-4 py-5">
      <ModelsSection
        models={models}
        options={modelOptions}
        revision={modelRevision}
        currentRef={currentModel?.ref ?? ""}
        currentSource={currentModel?.source ?? "none"}
        currentProvider={currentModel?.provider ?? ""}
        currentModelName={currentModel?.model ?? ""}
        onChanged={onModelsChanged}
      />

      <SkillsPage
        skills={skills}
        revision={skillRevision}
        pending={skillsPending}
        activeRun={skillsActiveRun}
        runtimeSkills={runtimeSkills}
        workspaceId={workspaceId}
        onChanged={onSkillsChanged}
        extensions={extensions}
        extensionRevision={extensionRevision}
      />

      <section>
        <header className="mb-2 flex items-center justify-between">
          <div>
            <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">配置档</h2>
            <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
              一次会话用哪套模型与工具策略。引用不存在的资源会被拒绝保存。
            </p>
          </div>
          <button
            onClick={() => setEditing(null)}
            className="flex items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            <Plus className="h-3 w-3" aria-hidden /> 新建
          </button>
        </header>

        {loadError && (
          <p role="alert" className="mb-2 text-[11px] text-[color:var(--anvil-danger)]">
            <span className="font-mono text-[10px]">{loadError.code}</span> {loadError.message}
          </p>
        )}

        {profiles.length === 0 && editing === undefined ? (
          <EmptyState
            title="还没有配置档"
            hint="配置档决定会话用哪个模型、能开哪些工具。资源库里先有模型，配置档才能引用它。"
            icon={<Boxes className="h-4 w-4" aria-hidden />}
          />
        ) : (
          <ul className="space-y-1">
            {profiles.map((p) => (
              <li
                key={p.id}
                className="flex items-center gap-2 rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">{p.name || p.id}</span>
                    <span className="shrink-0 rounded bg-[color:var(--anvil-surface)] px-1 py-0.5 font-mono text-[9px] text-[color:var(--anvil-muted)]">
                      rev {p.revision}
                    </span>
                  </div>
                  <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                    {p.modelRef || "未指定模型"} · 文件 {p.toolPolicy.filesystem}
                    {p.toolPolicy.shell ? " · shell" : ""}
                  </div>
                </div>
                <button
                  onClick={() => void openExisting(p)}
                  disabled={loadingId === p.id}
                  className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                >
                  {loadingId === p.id ? "读取中…" : "编辑"}
                </button>
              </li>
            ))}
          </ul>
        )}

        {editing !== undefined && (
          <div className="mt-3 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-3">
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
                {editing ? `编辑 ${editing.name || editing.id}` : "新建配置档"}
              </h3>
              <button
                onClick={close}
                className="text-[11px] text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-foreground)]"
              >
                收起
              </button>
            </div>
            <ProfileEditor
              profile={editing}
              workspaceId={workspaceId}
              onSaved={() => {
                onChanged();
                close();
              }}
              onReloaded={onChanged}
            />
          </div>
        )}
      </section>

      <McpPage />
    </div>
  );
}
