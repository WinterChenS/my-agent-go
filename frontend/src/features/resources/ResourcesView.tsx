// 资源库：配置档（Profile）+ 真正的资源（模型/技能/MCP）。
//
// 配置档现在是真的能用；模型/技能/MCP 的定义分别属于 C02–C04，尚未实现。
// 未实现的部分必须明确写出来并且不显示任何数据——
// 用空列表配一句"暂无"会让人以为"我还没添加"，而不是"这个功能不存在"。

import { useState } from "react";
import { Boxes, Plus } from "lucide-react";
import type { CallError, Profile } from "../../types";
import { api } from "../../api";
import { toCallError } from "../../app/useBackend";
import { ProfileEditor } from "../profiles/ProfileEditor";
import { EmptyState } from "../../components/StateViews";

export interface ResourcesViewProps {
  profiles: Profile[];
  workspaceId: string;
  onChanged: () => void;
}

export function ResourcesView({ profiles, workspaceId, onChanged }: ResourcesViewProps) {
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
      <section>
        <header className="mb-2 flex items-center justify-between">
          <div>
            <h2 className="text-sm font-medium text-slate-200">配置档</h2>
            <p className="mt-0.5 text-[11px] text-slate-500">
              一次会话用哪套模型与工具策略。引用不存在的资源会被拒绝保存。
            </p>
          </div>
          <button
            onClick={() => setEditing(null)}
            className="flex items-center gap-1 rounded-md border border-white/10 px-2 py-1 text-[11px] text-slate-300 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
          >
            <Plus className="h-3 w-3" aria-hidden /> 新建
          </button>
        </header>

        {loadError && (
          <p role="alert" className="mb-2 text-[11px] text-rose-300">
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
                className="flex items-center gap-2 rounded-md border border-white/10 bg-[#141922] px-2.5 py-2"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate text-[12px] text-slate-200">{p.name || p.id}</span>
                    <span className="shrink-0 rounded bg-white/5 px-1 py-0.5 font-mono text-[9px] text-slate-500">
                      rev {p.revision}
                    </span>
                  </div>
                  <div className="truncate text-[10px] text-slate-600">
                    {p.modelRef || "未指定模型"} · 文件 {p.toolPolicy.filesystem}
                    {p.toolPolicy.shell ? " · shell" : ""}
                  </div>
                </div>
                <button
                  onClick={() => void openExisting(p)}
                  disabled={loadingId === p.id}
                  className="rounded border border-white/10 px-2 py-0.5 text-[11px] text-slate-300 hover:bg-white/5 disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
                >
                  {loadingId === p.id ? "读取中…" : "编辑"}
                </button>
              </li>
            ))}
          </ul>
        )}

        {editing !== undefined && (
          <div className="mt-3 rounded-lg border border-white/10 bg-[#141922] p-3">
            <div className="mb-2 flex items-center justify-between">
              <h3 className="text-[12px] font-medium text-slate-200">
                {editing ? `编辑 ${editing.name || editing.id}` : "新建配置档"}
              </h3>
              <button
                onClick={close}
                className="text-[11px] text-slate-500 hover:text-slate-300"
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

      <PendingSection
        title="模型"
        task="C02"
        reason="模型定义需要连接测试与凭证存储，还没做。因此这里不列出任何模型——配置档里的模型 ID 需要手动填写，且必须先在配置层里存在。"
      />
      <PendingSection
        title="技能"
        task="C03"
        reason="技能的导入、启停与真实 Pi 加载验证属于 C03，尚未实现。"
      />
      <PendingSection
        title="MCP 服务"
        task="C04"
        reason="MCP 配置与诊断属于 C04。产品内置的 harness MCP 已在运行，但它的配置不对外暴露。"
      />
    </div>
  );
}

function PendingSection({ title, task, reason }: { title: string; task: string; reason: string }) {
  return (
    <section className="rounded-lg border border-dashed border-white/10 px-3 py-2.5">
      <div className="flex items-center gap-2">
        <h2 className="text-[12px] text-slate-400">{title}</h2>
        <span className="rounded bg-white/5 px-1.5 py-0.5 text-[9px] text-slate-500">
          未实现 · {task}
        </span>
      </div>
      <p className="mt-1 text-[11px] leading-relaxed text-slate-600">{reason}</p>
    </section>
  );
}
