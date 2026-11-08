// Profile（配置档）编辑器。
//
// 三件事必须做对：
//   1. 保存带 revision —— 不带就永远覆盖别人的改动
//   2. 冲突时展示差异 —— 只说"冲突了"等于让用户盲选
//   3. 密钥不进来 —— profile 是会被导出、贴进 issue 的普通 JSON
//
// 模型/技能/MCP 目前是手填 ID：它们的资源定义在 C02–C04，还没有列表可下拉。
// 宁可显示一个带说明的输入框，也不做假的"选择器"让人以为资源库已经存在。

import { useState } from "react";
import { AlertTriangle, Check, Loader2, RotateCcw, Save } from "lucide-react";
import { api } from "../../api";
import { toCallError } from "../../app/useBackend";
import type {
  CallError,
  FilesystemScope,
  Profile,
  ProfileConflictResult,
  ProfileFieldDiff,
} from "../../types";

const FILESYSTEM_OPTIONS: { value: FilesystemScope; label: string }[] = [
  { value: "none", label: "不访问文件" },
  { value: "read", label: "只读" },
  { value: "workspace", label: "仅工作区" },
  { value: "unrestricted", label: "不限（仍受上层限制）" },
];

export interface ProfileEditorProps {
  /** null 表示新建。 */
  profile: Profile | null;
  workspaceId: string;
  onSaved: (p: Profile) => void;
  onReloaded: () => void;
}

export function ProfileEditor({ profile, workspaceId, onSaved, onReloaded }: ProfileEditorProps) {
  const [draft, setDraft] = useState<Profile>(() => blank(profile));
  const [baseRevision, setBaseRevision] = useState(profile?.revision ?? 0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<CallError | null>(null);
  const [conflict, setConflict] = useState<ProfileConflictResult | null>(null);
  const [copyId, setCopyId] = useState("");

  const patch = (p: Partial<Profile>) => setDraft((d) => ({ ...d, ...p }));

  const save = async (idOverride?: string, revisionOverride?: number) => {
    const id = (idOverride ?? draft.id).trim();
    if (!id) {
      setError({ code: "VALIDATION", message: "配置档需要一个 ID", retryable: false });
      return;
    }
    if (!draft.modelRef.trim()) {
      setError({ code: "VALIDATION", message: "必须指定模型（modelRef）", retryable: false });
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const res = await api.saveProfile(
        id,
        workspaceId,
        revisionOverride ?? baseRevision,
        { ...draft, id }
      );
      if (res.error) {
        if (res.error.code === "CONFIG_CONFLICT") {
          // 冲突必须拿磁盘现状来对比，不能拿自己保存前的快照充数。
          const c = await api.profileConflict(id, { ...draft, id });
          if (c.error) setError(c.error);
          else setConflict(c);
        } else {
          setError(res.error);
        }
        return;
      }
      setBaseRevision(res.item.revision);
      setDraft(blank(res.item));
      setConflict(null);
      onSaved(res.item);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setSaving(false);
    }
  };

  const reloadFromDisk = async () => {
    if (!conflict) return;
    const res = await api.profile(conflict.current.id);
    if (res.error) {
      setError(res.error);
      return;
    }
    setDraft(blank(res.item));
    setBaseRevision(res.item.revision);
    setConflict(null);
    onReloaded();
  };

  return (
    <div className="space-y-3">
      {conflict && (
        <ConflictPanel
          conflict={conflict}
          copyId={copyId}
          onCopyId={setCopyId}
          onReload={() => void reloadFromDisk()}
          onSaveAsCopy={() => {
            const id = copyId.trim();
            if (!id) return;
            void save(id, 0);
          }}
        />
      )}

      {error && (
        <div className="rounded-md border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2.5 py-2 text-[11px] text-[color:var(--anvil-danger)]">
          <span className="font-mono text-[10px]">{error.code}</span> {error.message}
        </div>
      )}

      <div className="grid grid-cols-2 gap-2">
        <Field label="配置档 ID" hint={profile ? "已存在的配置档 ID 不可改" : "新建时填写"}>
          <input
            value={draft.id}
            disabled={!!profile}
            onChange={(e) => patch({ id: e.target.value })}
            className={inputCls}
          />
        </Field>
        <Field label="名称">
          <input value={draft.name} onChange={(e) => patch({ name: e.target.value })} className={inputCls} />
        </Field>
        <Field label="模型 ID" hint="资源库里定义过的模型 ID">
          <input
            value={draft.modelRef}
            onChange={(e) => patch({ modelRef: e.target.value })}
            className={inputCls}
          />
        </Field>
        <Field label="技能 ID" hint="多个用逗号分隔">
          <input
            value={(draft.skillIDs ?? []).join(",")}
            onChange={(e) => patch({ skillIDs: splitIDs(e.target.value) })}
            className={inputCls}
          />
        </Field>
        <Field label="MCP ID" hint="多个用逗号分隔">
          <input
            value={(draft.mcpIDs ?? []).join(",")}
            onChange={(e) => patch({ mcpIDs: splitIDs(e.target.value) })}
            className={inputCls}
          />
        </Field>
        <Field label="文件访问" hint="只能比上层更严，不能放宽">
          <select
            value={draft.toolPolicy.filesystem}
            onChange={(e) =>
              patch({
                toolPolicy: { ...draft.toolPolicy, filesystem: e.target.value as FilesystemScope },
              })
            }
            className={inputCls}
          >
            {FILESYSTEM_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </Field>
      </div>

      <div className="flex flex-wrap items-center gap-4 text-[11px] text-[color:var(--anvil-muted)]">
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.toolPolicy.network}
            onChange={(e) =>
              patch({ toolPolicy: { ...draft.toolPolicy, network: e.target.checked } })
            }
            className="h-3.5 w-3.5 accent-sky-500"
          />
          允许联网
        </label>
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.toolPolicy.shell}
            onChange={(e) => patch({ toolPolicy: { ...draft.toolPolicy, shell: e.target.checked } })}
            className="h-3.5 w-3.5 accent-sky-500"
          />
          允许 shell
        </label>
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.memoryPolicy.enabled}
            onChange={(e) =>
              patch({ memoryPolicy: { ...draft.memoryPolicy, enabled: e.target.checked } })
            }
            className="h-3.5 w-3.5 accent-sky-500"
          />
          启用记忆
        </label>
        <label className="flex items-center gap-1.5">
          上限条目
          <input
            type="number"
            min={0}
            value={draft.memoryPolicy.maxEntries}
            onChange={(e) =>
              patch({
                memoryPolicy: { ...draft.memoryPolicy, maxEntries: Number(e.target.value) || 0 },
              })
            }
            className="w-16 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1.5 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] outline-none focus:border-[color:var(--anvil-accent-border)]"
          />
        </label>
      </div>

      <div className="flex items-center gap-2">
        <button
          onClick={() => void save()}
          disabled={saving}
          className="flex items-center gap-1.5 rounded-md bg-[color:var(--anvil-accent)] px-3 py-1.5 text-xs font-medium text-[color:var(--anvil-background)] hover:bg-[color:var(--anvil-accent)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          {saving ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
          ) : (
            <Save className="h-3.5 w-3.5" aria-hidden />
          )}
          {saving ? "保存中…" : "保存"}
        </button>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">基于 revision {baseRevision}</span>
      </div>
    </div>
  );
}

function ConflictPanel({
  conflict,
  copyId,
  onCopyId,
  onReload,
  onSaveAsCopy,
}: {
  conflict: ProfileConflictResult;
  copyId: string;
  onCopyId: (v: string) => void;
  onReload: () => void;
  onSaveAsCopy: () => void;
}) {
  const diff = conflict.diff ?? [];
  return (
    <div className="rounded-md border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] p-2.5" role="alert">
      <div className="flex items-center gap-1.5 text-[11px] font-medium text-[color:var(--anvil-warning)]">
        <AlertTriangle className="h-3.5 w-3.5" aria-hidden />
        保存被拒绝：磁盘上的版本已被改动（当前 revision {conflict.current.revision}）
      </div>
      {diff.length === 0 ? (
        <p className="mt-1.5 text-[11px] text-[color:var(--anvil-warning)]">
          字段内容相同，只有 revision 不同——可能是同一份内容被保存了两次。
        </p>
      ) : (
        <table className="mt-2 w-full text-[11px]">
          <thead>
            <tr className="text-left text-[color:var(--anvil-warning)]">
              <th className="py-0.5 pr-2 font-normal">字段</th>
              <th className="py-0.5 pr-2 font-normal">我的改动</th>
              <th className="py-0.5 font-normal">磁盘上的</th>
            </tr>
          </thead>
          <tbody>
            {diff.map((d) => (
              <DiffRow key={d.field} d={d} />
            ))}
          </tbody>
        </table>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-1.5">
        <button
          onClick={onReload}
          className="flex items-center gap-1 rounded border border-[color:var(--anvil-warning-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-warning)] hover:bg-[color:var(--anvil-warning-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <RotateCcw className="h-3 w-3" aria-hidden /> 放弃我的改动，重新加载
        </button>
        <input
          value={copyId}
          onChange={(e) => onCopyId(e.target.value)}
          placeholder="新配置档 ID"
          aria-label="另存为的新配置档 ID"
          className="w-32 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] outline-none focus:border-[color:var(--anvil-warning-border)]"
        />
        <button
          onClick={onSaveAsCopy}
          disabled={!copyId.trim()}
          className="flex items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <Check className="h-3 w-3" aria-hidden /> 另存为新配置档
        </button>
      </div>
    </div>
  );
}

function DiffRow({ d }: { d: ProfileFieldDiff }) {
  return (
    <tr className="border-t border-[color:var(--anvil-warning-border)] align-top">
      <td className="py-1 pr-2 font-mono text-[10px] text-[color:var(--anvil-warning)]">{d.field}</td>
      <td className="py-1 pr-2 text-[color:var(--anvil-foreground)]">{d.mine || "（空）"}</td>
      <td className="py-1 text-[color:var(--anvil-muted)]">{d.theirs || "（空）"}</td>
    </tr>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">{label}</span>
      {children}
      {hint && <span className="text-[10px] text-[color:var(--anvil-muted)]">{hint}</span>}
    </label>
  );
}

const inputCls =
  "rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1.5 text-[12px] text-[color:var(--anvil-foreground)] outline-none focus:border-[color:var(--anvil-accent-border)] disabled:opacity-50";

function splitIDs(v: string): string[] | null {
  const ids = v
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  return ids.length ? ids : null;
}

function blank(p: Profile | null): Profile {
  if (p) {
    return {
      ...p,
      skillIDs: p.skillIDs ?? null,
      mcpIDs: p.mcpIDs ?? null,
      toolPolicy: { ...p.toolPolicy, filesystem: p.toolPolicy.filesystem || "none" },
      memoryPolicy: { ...p.memoryPolicy },
    };
  }
  return {
    id: "",
    name: "",
    modelRef: "",
    skillIDs: null,
    mcpIDs: null,
    toolPolicy: { filesystem: "none", network: false, shell: false },
    memoryPolicy: { enabled: false, maxEntries: 0 },
    revision: 0,
  };
}
