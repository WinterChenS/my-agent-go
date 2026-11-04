// 左栏：项目（工作区）列表。
//
// 未信任状态必须是可见的、且和"已信任"明显不同：工作区是执行代码的地方，
// 用户需要随时知道当前这个目录有没有被允许跑东西。
// 把信任只做成设置里的一个开关、列表里毫无痕迹，等于把安全状态藏起来了。

import { useState } from "react";
import { FolderPlus, Folder, Lock, LockOpen } from "lucide-react";
import type { CallError, Workspace } from "../../types";
import { ErrorState } from "../../components/StateViews";

export interface SidebarProps {
  workspaces: Workspace[];
  currentId: string | null;
  onSelect: (id: string) => void;
  onAdd: (path: string) => Promise<void>;
  onTrust: (id: string, trusted: boolean) => Promise<void>;
  loading?: boolean;
  error?: CallError | null;
}

export function Sidebar({
  workspaces,
  currentId,
  onSelect,
  onAdd,
  onTrust,
  loading,
  error,
}: SidebarProps) {
  const [adding, setAdding] = useState(false);
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [localErr, setLocalErr] = useState<CallError | null>(null);

  const submit = async () => {
    const p = path.trim();
    if (!p || busy) return;
    setBusy(true);
    setLocalErr(null);
    try {
      await onAdd(p);
      setPath("");
      setAdding(false);
    } catch (e) {
      setLocalErr(e as CallError);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between px-3 py-2.5">
        <span className="text-[11px] font-medium uppercase tracking-wide text-slate-500">
          项目
        </span>
        <button
          onClick={() => setAdding((v) => !v)}
          className="rounded p-1 text-slate-400 hover:bg-white/5 hover:text-slate-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
          title="添加工作区"
          aria-label="添加工作区"
          aria-expanded={adding}
        >
          <FolderPlus className="h-3.5 w-3.5" aria-hidden />
        </button>
      </div>

      {adding && (
        <div className="border-y border-white/5 bg-black/20 px-3 py-2">
          <label className="block text-[10px] text-slate-500" htmlFor="ws-path">
            目录路径
          </label>
          <input
            id="ws-path"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void submit();
              }
              if (e.key === "Escape") setAdding(false);
            }}
            placeholder="D:\work\proj"
            autoFocus
            className="mt-1 w-full rounded border border-white/10 bg-[#0f1218] px-2 py-1 text-[11px] text-slate-200 outline-none focus:border-sky-500/50"
          />
          <p className="mt-1 text-[10px] leading-relaxed text-slate-600">
            暂时需要手动输入路径；目录选择器要在原生对话框接好后才有。
          </p>
          <div className="mt-1.5 flex gap-1.5">
            <button
              onClick={() => void submit()}
              disabled={busy || !path.trim()}
              className="rounded bg-sky-500 px-2 py-1 text-[11px] font-medium text-white hover:bg-sky-400 disabled:opacity-40"
            >
              {busy ? "添加中…" : "添加"}
            </button>
            <button
              onClick={() => setAdding(false)}
              className="rounded border border-white/10 px-2 py-1 text-[11px] text-slate-300 hover:bg-white/5"
            >
              取消
            </button>
          </div>
        </div>
      )}

      {(error || localErr) && (
        <div className="px-2 py-2">
          <ErrorState error={(localErr ?? error)!} title={localErr ? "添加失败" : "读取失败"} />
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 py-1">
        {loading && workspaces.length === 0 && (
          <p className="px-2 py-3 text-[11px] text-slate-600" role="status" aria-live="polite">
            正在读取工作区…
          </p>
        )}

        {!loading && workspaces.length === 0 && !error && (
          <p className="px-2 py-3 text-[11px] leading-relaxed text-slate-600">
            还没有工作区。添加一个目录后，agent 会在其中读写文件。
          </p>
        )}

        <ul className="space-y-0.5">
          {workspaces.map((ws) => {
            const active = ws.id === currentId;
            return (
              <li key={ws.id}>
                <div
                  className={
                    "group flex items-center gap-1.5 rounded px-2 py-1.5 " +
                    (active ? "bg-sky-500/10" : "hover:bg-white/[0.04]")
                  }
                >
                  <button
                    onClick={() => onSelect(ws.id)}
                    className="flex min-w-0 flex-1 items-center gap-1.5 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
                    aria-current={active ? "true" : undefined}
                  >
                    <Folder
                      className={"h-3.5 w-3.5 shrink-0 " + (active ? "text-sky-400" : "text-slate-500")}
                      aria-hidden
                    />
                    <span className="min-w-0 flex-1">
                      <span
                        className={
                          "block truncate text-[12px] " +
                          (active ? "text-slate-100" : "text-slate-300")
                        }
                      >
                        {ws.name}
                      </span>
                      <span className="block truncate text-[10px] text-slate-600" title={ws.canonicalPath}>
                        {ws.canonicalPath}
                      </span>
                    </span>
                  </button>

                  <button
                    onClick={() => void onTrust(ws.id, !ws.trusted)}
                    className={
                      "shrink-0 rounded p-1 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400 " +
                      (ws.trusted
                        ? "text-emerald-400/70 hover:bg-emerald-500/10 hover:text-emerald-300"
                        : "text-amber-400/80 hover:bg-amber-500/10 hover:text-amber-300")
                    }
                    title={ws.trusted ? "已信任：点击取消信任" : "未信任：点击允许在此执行代码"}
                    aria-label={ws.trusted ? `取消信任 ${ws.name}` : `信任 ${ws.name}`}
                  >
                    {ws.trusted ? (
                      <LockOpen className="h-3.5 w-3.5" aria-hidden />
                    ) : (
                      <Lock className="h-3.5 w-3.5" aria-hidden />
                    )}
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      </div>

      <div className="border-t border-white/5 px-3 py-2 text-[10px] leading-relaxed text-slate-600">
        未信任的工作区不能执行代码。
      </div>
    </div>
  );
}
