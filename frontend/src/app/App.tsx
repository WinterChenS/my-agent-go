// 顶层装配：加载后端数据、订阅事件、把视图接进三栏外壳。
//
// 这里只做"接线"，不做业务判断：谁能运行由后端判定（工作区信任、profile 引用），
// 前端不复制一份规则——两份规则迟早会不一致，而不一致的那一份会悄悄放行。

import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import { api, onRunEvent } from "../api";
import { initialState, reduce } from "../runState";
import type { ApprovalChoice, ApprovalItem, CallError, Settings } from "../types";
import { Layout } from "./Layout";
import { RouterProvider, useRouter, type ViewId } from "./Router";
import { toCallError, useBackend } from "./useBackend";
import { Sidebar } from "../features/workspaces/Sidebar";
import { WorkbenchView } from "../features/workbench/WorkbenchView";
import { ResourcesView } from "../features/resources/ResourcesView";
import { MemoryView } from "../features/memory/MemoryView";
import { RunsView } from "../features/runs/RunsView";
import { SettingsView } from "../features/settings/SettingsView";
import { DetailPanel } from "./DetailPanel";

const PHASE_META: Record<string, { label: string; tone: string }> = {
  idle: { label: "空闲", tone: "bg-slate-500" },
  queued: { label: "排队中", tone: "bg-amber-400" },
  starting: { label: "启动中", tone: "bg-sky-400" },
  running: { label: "运行中", tone: "bg-emerald-400" },
  waiting_approval: { label: "等待授权", tone: "bg-amber-400" },
  stopping: { label: "停止中", tone: "bg-orange-400" },
  completed: { label: "已完成", tone: "bg-emerald-400" },
  failed: { label: "失败", tone: "bg-rose-400" },
  cancelled: { label: "已取消", tone: "bg-slate-400" },
  interrupted: { label: "已中断", tone: "bg-rose-400" },
};

export default function App() {
  return (
    <RouterProvider>
      <Shell />
    </RouterProvider>
  );
}

function Shell() {
  const { state: router, dispatch: navigate } = useRouter();
  const backend = useBackend();

  const [ui, runDispatch] = useReducer(reduce, initialState);
  const [input, setInput] = useState("");
  const [rightOpen, setRightOpen] = useState(false);
  const [savingSettings, setSavingSettings] = useState(false);
  const [actionError, setActionError] = useState<CallError | null>(null);

  // 事件订阅。所有会话状态都由事件推进，调用返回值只用于拿到 runId。
  useEffect(() => onRunEvent((ev) => runDispatch({ kind: "event", ev })), []);

  const busy =
    ui.phase === "starting" ||
    ui.phase === "running" ||
    ui.phase === "waiting_approval" ||
    ui.phase === "stopping";

  const send = useCallback(async () => {
    const text = input.trim();
    if (!text || busy) return;
    setInput("");
    setActionError(null);
    runDispatch({ kind: "user_message", text });
    try {
      const snap = await api.start(text);
      // 运行时是这次 Start 才构建的：状态里的 provider/model 到这里才成立。
      void backend.refreshStatus();
      runDispatch({
        kind: "run_started",
        runId: snap.runId,
        state: snap.state,
        // 记的是启动那一刻的快照：provider/model 用的是这一轮真正发给 Pi 的值，
        // 不是"现在设置里是什么"。
        meta: {
          provider: snap.provider ?? "",
          model: snap.model ?? "",
          configRevision: snap.configRevision,
        },
      });
    } catch (e) {
      const ce = toCallError(e);
      // 后端的拒绝理由要对用户可见：这里不翻译成"发送失败"，
      // 因为"工作区未信任"和"Pi 不存在"需要的下一步动作完全不同。
      runDispatch({ kind: "run_start_failed", message: `${ce.code}: ${ce.message}` });
    }
  }, [input, busy, backend.refreshStatus]);

  const stop = useCallback(async () => {
    if (!ui.runId) return;
    try {
      await api.abort(ui.runId);
    } catch (e) {
      runDispatch({ kind: "error", message: toCallError(e).message });
    }
  }, [ui.runId]);

  const decide = useCallback(
    async (item: ApprovalItem, choice: ApprovalChoice) => {
      if (!ui.runId) return;
      try {
        await api.resolveApproval(ui.runId, item.approvalId, choice);
        runDispatch({ kind: "approval_resolved", approvalId: item.approvalId });
      } catch (e) {
        runDispatch({ kind: "error", message: toCallError(e).message });
      }
    },
    [ui.runId]
  );

  const addWorkspace = useCallback(
    async (path: string) => {
      const res = await api.createWorkspace(path);
      if (res.error) throw res.error;
      // 新加的工作区顺带设为当前工作目录：用户刚添加它，意图就是用它。
      if (backend.settings) {
        const next: Settings = { ...backend.settings, workspace: res.item.canonicalPath };
        const saved = await api.saveSettings(next).then(
          () => null,
          (e) => toCallError(e)
        );
        if (saved) throw saved;
      }
      await backend.reload();
    },
    [backend]
  );

  const trustWorkspace = useCallback(
    async (id: string, trusted: boolean) => {
      const res = await api.setWorkspaceTrust(id, trusted);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setActionError(null);
      await backend.refreshWorkspaces();
    },
    [backend]
  );

  const saveSettings = useCallback(
    async (s: Settings) => {
      setSavingSettings(true);
      setActionError(null);
      try {
        await api.saveSettings(s);
        await backend.reload();
      } catch (e) {
        setActionError(toCallError(e));
      } finally {
        setSavingSettings(false);
      }
    },
    [backend]
  );

  const go = useCallback((view: ViewId) => navigate({ kind: "go", view }), [navigate]);

  const phaseMeta = useMemo(() => PHASE_META[ui.phase] ?? PHASE_META.idle, [ui.phase]);

  const main = useMemo(() => {
    switch (router.view) {
      case "resources":
        return (
          <ResourcesView
            profiles={backend.profiles}
            workspaceId={backend.currentWorkspace?.id ?? ""}
            models={backend.models}
            modelOptions={backend.modelOptions}
            modelRevision={backend.modelRevision}
            currentModel={backend.currentModel}
            onChanged={() => void backend.refreshProfiles()}
            onModelsChanged={() => void backend.refreshModels()}
          />
        );
      case "memory":
        return <MemoryView />;
      case "runs":
        return <RunsView ui={ui} workspace={backend.currentWorkspace} />;
      case "settings":
        return backend.settings ? (
          <SettingsView
            settings={backend.settings}
            status={backend.status}
            workspaces={backend.workspaces}
            saving={savingSettings}
            error={actionError}
            onSave={saveSettings}
            onTrust={trustWorkspace}
          />
        ) : (
          <div className="mx-auto max-w-3xl px-4 py-5 text-xs text-slate-500" role="status">
            {backend.loading ? "正在读取设置…" : (backend.error?.message ?? "设置不可用")}
          </div>
        );
      case "workbench":
      default:
        return (
          <WorkbenchView
            ui={ui}
            input={input}
            onInput={setInput}
            onSend={() => void send()}
            onStop={() => void stop()}
            onDecide={(item, choice) => void decide(item, choice)}
            busy={busy}
            workspace={backend.currentWorkspace}
            onGoSettings={() => go("settings")}
          />
        );
    }
  }, [
    router.view,
    backend,
    ui,
    input,
    busy,
    actionError,
    savingSettings,
    send,
    stop,
    decide,
    go,
    saveSettings,
    trustWorkspace,
  ]);

  return (
    <Layout
      status={backend.status}
      view={router.view}
      onNavigate={go}
      rightOpen={rightOpen}
      onToggleRight={() => setRightOpen((v) => !v)}
      phaseLabel={ui.phase === "idle" && !busy ? null : phaseMeta}
      left={
        <Sidebar
          workspaces={backend.workspaces}
          currentId={backend.currentWorkspace?.id ?? null}
          onSelect={(id) => {
            const ws = backend.workspaces.find((w) => w.id === id);
            if (!ws || !backend.settings) return;
            void saveSettings({ ...backend.settings, workspace: ws.canonicalPath });
          }}
          onAdd={addWorkspace}
          onTrust={trustWorkspace}
          loading={backend.loading}
          error={backend.error}
        />
      }
      right={
        <DetailPanel
          ui={ui}
          status={backend.status}
          workspace={backend.currentWorkspace}
          phaseMeta={phaseMeta}
          onGoRuns={() => go("runs")}
        />
      }
    >
      {main}
    </Layout>
  );
}
