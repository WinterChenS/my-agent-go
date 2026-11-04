// 扩展区块（C03 / RES04）。
//
// 只有两类，规则不同，所以不用一个通用开关糊过去：
//   - 系统策略扩展：不可关闭。界面上不画关闭按钮，绑定层也会拒绝。
//   - 第三方可执行扩展：默认关闭。开启它等于允许任意代码在 Pi 进程里跑，
//     但这**不会**让它的工具调用免审批——审批由策略扩展统一拦截。

import { useState } from "react";
import { api } from "../../api";
import type { CallError, Extension } from "../../types";
import { toCallError } from "../../app/useBackend";

export interface ExtensionsSectionProps {
  extensions: Extension[];
  revision: number;
  onChanged: () => void;
}

export function ExtensionsSection({ extensions, revision, onChanged }: ExtensionsSectionProps) {
  const [error, setError] = useState<CallError | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);

  async function toggle(e: Extension) {
    setError(null);
    if (!e.enabled) {
      // 开启第三方扩展 = 允许它的代码在 Pi 进程里执行，必须是一次明确动作。
      setConfirming(e.id);
      return;
    }
    await apply(e, false);
  }

  async function apply(e: Extension, enabled: boolean) {
    setBusy(e.id);
    try {
      const res = await api.setExtensionEnabled(e.id, revision, enabled);
      if (res.error) {
        setError(res.error);
        return;
      }
      setConfirming(null);
      onChanged();
    } catch (err) {
      setError(toCallError(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <section>
      <header className="mb-2">
        <h2 className="text-sm font-medium text-slate-200">扩展</h2>
        <p className="mt-0.5 text-[11px] text-slate-500">
          扩展是会在 Pi 进程里执行的代码。系统策略扩展不可关闭；第三方扩展默认关闭，
          启用即信任——但信任不等于免审批。
        </p>
      </header>

      {error && (
        <p role="alert" className="mb-2 text-[11px] text-rose-300">
          <span className="font-mono text-[10px]">{error.code}</span> {error.message}
        </p>
      )}

      {extensions.length === 0 ? (
        <p className="rounded border border-dashed border-white/10 px-3 py-2 text-[11px] text-slate-600">
          没有可列出的扩展。系统策略扩展未解析到时也会是空的——那说明仓库布局有问题，
          而不是"本来就没有"。
        </p>
      ) : (
        <ul className="space-y-1">
          {extensions.map((e) => (
            <li key={e.id} className="rounded-md border border-white/10 bg-[#141922] px-2.5 py-2">
              <div className="flex items-center gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate text-[12px] text-slate-200">{e.name || e.id}</span>
                    {e.system && (
                      <span className="shrink-0 rounded bg-sky-400/10 px-1 py-0.5 text-[9px] text-sky-300">
                        系统资源 · 不可关闭
                      </span>
                    )}
                    {!e.system && (
                      <span className="shrink-0 rounded bg-white/5 px-1 py-0.5 text-[9px] text-slate-500">
                        {e.enabled ? "已启用" : "未启用"}
                      </span>
                    )}
                  </div>
                  <div className="truncate text-[10px] text-slate-600">{e.path}</div>
                  <div className="mt-0.5 text-[10px] leading-relaxed text-slate-500">{e.note}</div>
                </div>
                {e.canDisable && (
                  <div className="flex shrink-0 items-center gap-1">
                    {confirming === e.id ? (
                      <>
                        <button
                          onClick={() => void apply(e, true)}
                          disabled={busy === e.id}
                          className="rounded border border-amber-400/40 px-2 py-0.5 text-[11px] text-amber-200 hover:bg-amber-400/10 disabled:opacity-40"
                        >
                          确认启用
                        </button>
                        <button
                          onClick={() => setConfirming(null)}
                          className="rounded border border-white/10 px-2 py-0.5 text-[11px] text-slate-400 hover:bg-white/5"
                        >
                          取消
                        </button>
                      </>
                    ) : (
                      <button
                        onClick={() => void toggle(e)}
                        disabled={busy === e.id}
                        className="rounded border border-white/10 px-2 py-0.5 text-[11px] text-slate-300 hover:bg-white/5 disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
                      >
                        {e.enabled ? "禁用" : "启用"}
                      </button>
                    )}
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
