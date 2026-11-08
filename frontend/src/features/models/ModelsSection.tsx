// 模型资源列表。
//
// 列表只展示"定义"这一层事实：模型 ID、发给 API 的模型名、API 类型、端点、
// 是否当前在用。能力（工具调用/视觉）只在连接测试后才有结论，
// 所以默认不显示“支持/不支持”——没测过就说支持是最容易骗到自己的做法。

import { useState } from "react";
import { Cpu, Plus } from "lucide-react";
import { api } from "../../api";
import type {
  CallError,
  ModelDefinition,
  ModelOptions,
} from "../../types";
import { toCallError } from "../../app/useBackend";
import { EmptyState } from "../../components/StateViews";
import { ModelEditor } from "./ModelEditor";

export interface ModelsSectionProps {
  models: ModelDefinition[];
  options: ModelOptions;
  revision: number;
  currentRef: string;
  currentSource: "resources" | "settings" | "none";
  currentProvider: string;
  currentModelName: string;
  onChanged: () => void;
}

export function ModelsSection({
  models,
  options,
  revision,
  currentRef,
  currentSource,
  currentProvider,
  currentModelName,
  onChanged,
}: ModelsSectionProps) {
  const [editing, setEditing] = useState<ModelDefinition | null | undefined>(undefined);
  const [error, setError] = useState<CallError | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  async function activate(id: string) {
    setBusyId(id);
    setError(null);
    setNotice(null);
    try {
      const res = await api.setActiveModel(id);
      if (res.error) {
        setError(res.error);
        return;
      }
      if (res.deferred) {
        setNotice("已切换。当前还有 run 在跑，新模型会在下一次运行生效。");
      }
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  async function remove(id: string) {
    setBusyId(id);
    setError(null);
    try {
      const res = await api.deleteModel(revision, id);
      if (res.error) {
        setError(res.error);
        return;
      }
      setConfirming(null);
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  return (
    <section>
      <header className="mb-2 flex items-center justify-between">
        <div>
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">模型</h2>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            定义端点与模型名；密钥保存在系统凭证库，配置里只有引用名。
          </p>
        </div>
        <button
          onClick={() => {
            setEditing(null);
            setNotice(null);
            setError(null);
          }}
          className="flex items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <Plus className="h-3 w-3" aria-hidden /> 新建
        </button>
      </header>

      {currentSource === "settings" && (
        <p className="mb-2 rounded border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2 py-1 text-[10px] text-[color:var(--anvil-warning)]">
          当前使用的是设置里的 {currentProvider || "?"}/{currentModelName || "?"}
          （明文密钥路径）。从下面选一个模型设为当前即可切到凭证库路径。
        </p>
      )}

      {error && (
        <p role="alert" className="mb-2 text-[11px] text-[color:var(--anvil-danger)]">
          <span className="font-mono text-[10px]">{error.code}</span> {error.message}
        </p>
      )}
      {notice && (
        <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-warning)]">
          {notice}
        </p>
      )}

      {models.length === 0 && editing === undefined ? (
        <EmptyState
          title="还没有模型"
          hint="先定义一个模型（端点 + 模型名 + 凭证），配置档才能引用它，工作台才会真正发出请求。"
          icon={<Cpu className="h-4 w-4" aria-hidden />}
        />
      ) : (
        <ul className="space-y-1">
          {models.map((m) => {
            const isCurrent = currentRef === m.id;
            // C01 之前的条目是 {provider, baseUrl, model}，没有 providerId/modelId。
            // 它既不能编译（会生成空 provider 键），也说不清上下文窗口有多大——
            // 与其显示"上下文 0 · 输出 0"这种看起来像事实的数字，不如直说要重建。
            const isLegacy = !m.providerId || !m.modelId;
            return (
              <li
                key={m.id}
                className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2"
              >
                <div className="flex items-center gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">{m.id}</span>
                      {isCurrent && (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-success-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-success)]">
                          当前
                        </span>
                      )}
                      {m.enabled === false && (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-muted)]">
                          已禁用
                        </span>
                      )}
                      {isLegacy && (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-warning-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-warning)]">
                          旧格式
                        </span>
                      )}
                    </div>
                    {isLegacy ? (
                      <div className="truncate text-[10px] text-[color:var(--anvil-warning)]">
                        缺 provider 名与模型名（旧版本保存的形状），请编辑补齐后保存
                      </div>
                    ) : (
                      <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                        {m.providerId}/{m.modelId} · {m.api} · {m.baseUrl}
                      </div>
                    )}
                    {!isLegacy && (
                      <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                        上下文 {m.contextWindow} · 输出 {m.maxOutput}
                        {m.credentialRef ? ` · 凭证 ${m.credentialRef}` : " · 无凭证"}
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <button
                      onClick={() => {
                        setEditing(m);
                        setError(null);
                        setNotice(null);
                      }}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      编辑
                    </button>
                    <button
                      onClick={() => void activate(m.id)}
                      disabled={busyId === m.id || isCurrent || isLegacy}
                      title={isLegacy ? "旧格式条目无法编译成 models.json，请先编辑补齐" : undefined}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      {isCurrent ? "使用中" : "设为当前"}
                    </button>
                    {confirming === m.id ? (
                      <>
                        <button
                          onClick={() => void remove(m.id)}
                          disabled={busyId === m.id}
                          className="rounded border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] disabled:opacity-50"
                        >
                          确认删除
                        </button>
                        <button
                          onClick={() => setConfirming(null)}
                          className="rounded px-1 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-foreground)]"
                        >
                          取消
                        </button>
                      </>
                    ) : (
                      <button
                        onClick={() => setConfirming(m.id)}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                      >
                        删除
                      </button>
                    )}
                  </div>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {editing !== undefined && (
        <div className="mt-3 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-3">
          <h3 className="mb-2 text-[12px] font-medium text-[color:var(--anvil-foreground)]">
            {editing ? `编辑 ${editing.id}` : "新建模型"}
          </h3>
          <ModelEditor
            model={editing}
            options={options}
            revision={revision}
            onSaved={() => {
              setEditing(undefined);
              onChanged();
            }}
            onClose={() => setEditing(undefined)}
          />
        </div>
      )}
    </section>
  );
}
