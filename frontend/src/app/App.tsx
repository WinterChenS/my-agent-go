// 顶层装配：加载后端数据、订阅事件、把路由渲染进壳层。
//
// 这里只做"接线"，不做业务判断：谁能运行由后端判定（工作区信任、profile 引用），
// 前端不复制一份规则——两份规则迟早会不一致，而不一致的那一份会悄悄放行。
//
// U01 起外壳换成规格 §5.1 的形态：左侧栏是导航 + 历史会话 + Pi 状态/工作区，
// 顶栏中间是全局搜索。页面由 navigation.ts 的静态目录决定，App 只负责把
// "当前后端快照 + 动作"放进 ShellProvider。

import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import { api, onRunEvent } from "../api";
import { initialState, reduce } from "../runState";
import type { ApprovalChoice, ApprovalItem, CallError, Settings } from "../types";
import { isLocale, onLocaleChange, setLocale, storeLocalePreference, t } from "../i18n";
import { applyTheme, currentMedia, isThemePreference, storeThemePreference } from "../theme/theme";
import { settingsApi } from "../features/settings/api";
import { RuntimeFooter } from "../components/shell/RuntimeFooter";
import { historyApi } from "../features/history/api";
import { chatApi, type ChatClarificationResponse } from "../features/chat/api";
import { HistorySidebar } from "../features/history/HistorySidebar";
import { GlobalSearch } from "../features/search/GlobalSearch";
import { BrowserOcclusionProvider } from "./BrowserOcclusionCoordinator";
import { Layout } from "./Layout";
import { RouterProvider, useRouter } from "./Router";
import { ShellProvider, type ShellValue } from "./shell";
import { toCallError, useBackend } from "./useBackend";
import { DetailPanel } from "./DetailPanel";

/** run 阶段的状态点颜色；文案来自 catalog 的 status.*。 */
const PHASE_TONE: Record<string, string> = {
  idle: "bg-[color:var(--anvil-muted)]",
  queued: "bg-[color:var(--anvil-warning)]",
  starting: "bg-[color:var(--anvil-accent)]",
  running: "bg-[color:var(--anvil-success)]",
  waiting_approval: "bg-[color:var(--anvil-warning)]",
  stopping: "bg-[color:var(--anvil-warning)]",
  completed: "bg-[color:var(--anvil-success)]",
  failed: "bg-[color:var(--anvil-danger)]",
  cancelled: "bg-[color:var(--anvil-muted)]",
  interrupted: "bg-[color:var(--anvil-danger)]",
};

export default function App({ initialPath }: { initialPath?: string } = {}) {
  return (
    <RouterProvider initialPath={initialPath}>
      <Shell />
    </RouterProvider>
  );
}

function Shell() {
  const { match, navigate } = useRouter();
  const backend = useBackend();

  // 语言是模块级状态，改它不会自动重渲染：这里订阅一次，切换语言后整棵树重画。
  // 没有这行，"设置里切到英文"只会换掉设置页自己那几行文案。
  const [, rerenderForLocale] = useReducer((n: number) => n + 1, 0);
  useEffect(() => onLocaleChange(rerenderForLocale), []);

  // 启动后把后端偏好对齐到界面。
  //
  // 首屏用的是本地镜像（main.tsx 在挂载前应用，避免闪一帧系统语言/浅色），
  // 但权威值在磁盘上的 Preferences 里：换了机器、清了浏览器存储、或者上次是
  // 另一台设备写的偏好时，两者会不一致，必须以磁盘为准。
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const res = await settingsApi.getPreferences();
        if (cancelled || !res || res.error || !res.preferences) return;
        const { locale, theme } = res.preferences;
        if (isLocale(locale)) {
          setLocale(locale);
          storeLocalePreference(locale);
        }
        if (isThemePreference(theme)) {
          storeThemePreference(theme);
          applyTheme(theme, currentMedia());
        }
      } catch {
        // 偏好读不到时保留本地镜像：读失败不该把界面语言/主题清空。
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const [ui, runDispatch] = useReducer(reduce, initialState);
  const [input, setInput] = useState("");
  const [rightOpen, setRightOpen] = useState(false);
  const [savingSettings, setSavingSettings] = useState(false);
  const [actionError, setActionError] = useState<CallError | null>(null);
  const [workspaceFilter, setWorkspaceFilter] = useState("");

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
        // 不是"现在设置里是什么"。技能同理：托管显式加载的 ID 与自动发现开关
        // 都取自快照，不在界面上重新推算。
        meta: {
          provider: snap.provider ?? "",
          model: snap.model ?? "",
          configRevision: snap.configRevision,
          managedSkills: snap.managedSkills ?? [],
          skillAutoDiscovery: snap.skillAutoDiscovery,
        },
      });
    } catch (e) {
      const ce = toCallError(e);
      // 后端的拒绝理由要对用户可见：这里不翻译成"发送失败"，
      // 因为"工作区未信任"和"Pi 不存在"需要的下一步动作完全不同。
      runDispatch({ kind: "run_start_failed", message: `${ce.code}: ${ce.message}` });
    }
  }, [input, busy, backend]);

  const stop = useCallback(async () => {
    if (!ui.runId) return;
    try {
      await api.abort(ui.runId);
      // 后端接下了 abort，界面才切到“停止中”：停止也是一次真实的请求，
      // 不是“点了就算停了”。
      runDispatch({ kind: "stop_requested" });
    } catch (e) {
      const ce = toCallError(e);
      runDispatch({ kind: "error", message: `${ce.code}: ${ce.message}`, code: ce.code });
    }
  }, [ui.runId]);

  /**
   * 审批结论提交。
   *
   * 成功后只能标“已提交”——卡片必须留到后端 approval.resolved 到达才消失。
   * 之前这里直接 dispatch approval_resolved（本地移除），那正是规格 §5.3 禁止的行为。
   */
  const decide = useCallback(
    async (item: ApprovalItem, choice: ApprovalChoice) => {
      if (!ui.runId) return;
      try {
        await api.resolveApproval(ui.runId, item.approvalId, choice);
        runDispatch({ kind: "approval_submitted", approvalId: item.approvalId });
      } catch (e) {
        const ce = toCallError(e);
        runDispatch({ kind: "error", message: `${ce.code}: ${ce.message}`, code: ce.code });
      }
    },
    [ui.runId]
  );

  /** 会话级提交（StartSessionRun）：会话归属与附件都由后端判定。 */
  const sendToSession = useCallback(
    async (sessionId: string, text: string, attachmentIds: string[]) => {
      const trimmed = text.trim();
      if (!trimmed || busy) return;
      setActionError(null);
      runDispatch({ kind: "user_message", text: trimmed, sessionId });
      try {
        const snap = await chatApi.startSessionRun(sessionId, trimmed, attachmentIds);
        void backend.refreshStatus();
        runDispatch({
          kind: "run_started",
          runId: snap.runId,
          sessionId: snap.sessionId,
          state: snap.state,
          meta: {
            provider: snap.provider ?? "",
            model: snap.model ?? "",
            configRevision: snap.configRevision,
            managedSkills: snap.managedSkills ?? [],
            skillAutoDiscovery: snap.skillAutoDiscovery,
          },
        });
      } catch (e) {
        const ce = toCallError(e);
        runDispatch({ kind: "run_start_failed", message: `${ce.code}: ${ce.message}` });
      }
    },
    [busy, backend]
  );

  /** 澄清答复：独立通道，失败也只是一个可选错误，不影响审批。 */
  const answerClarification = useCallback(
    (requestId: string, response: ChatClarificationResponse) => {
      const sessionId = match.params.id ?? "";
      if (!sessionId) return;
      void chatApi
        .answerClarification(sessionId, requestId, response)
        .then(() => {
          const value = response.value?.text;
          runDispatch({ kind: "clarification_submitted", requestId, value });
        })
        .catch((e: unknown) => {
          const ce = toCallError(e);
          runDispatch({ kind: "error", message: `${ce.code}: ${ce.message}`, code: ce.code });
        });
    },
    [match.params.id]
  );

  /** 重放事件：走与实时订阅同一个归约，去重靠 eventId/seq。 */
  const replayRun = useCallback((runId: string, afterSeq: number) => {
    void chatApi
      .replay(runId, afterSeq)
      .then((events) => {
        for (const ev of events) runDispatch({ kind: "event", ev });
      })
      .catch((e: unknown) => {
        const ce = toCallError(e);
        runDispatch({ kind: "error", message: `${ce.code}: ${ce.message}`, code: ce.code });
      });
  }, []);

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

  /** 新建一个普通会话并跳到它。失败时把结构化错误留在界面上。 */
  const newSession = useCallback(async () => {
    const res = await historyApi.create(backend.currentWorkspace?.id ?? "");
    if (res.error || !res.item) {
      setActionError(
        res.error ?? { code: "INTERNAL_ERROR", message: t("errors.INTERNAL_ERROR"), retryable: true }
      );
      return;
    }
    setActionError(null);
    navigate(`/chat/${res.item.id}`);
  }, [backend.currentWorkspace, navigate]);

  const shellValue = useMemo<ShellValue>(
    () => ({
      backend,
      status: backend.status,
      settings: backend.settings,
      workspaces: backend.workspaces,
      currentWorkspace: backend.currentWorkspace,
      profiles: backend.profiles,
      models: backend.models,
      modelOptions: backend.modelOptions,
      modelRevision: backend.modelRevision,
      currentModel: backend.currentModel,
      skills: backend.skills,
      skillRevision: backend.skillRevision,
      skillsPending: backend.skillsPending,
      skillsActiveRun: backend.skillsActiveRun,
      extensions: backend.extensions,
      extensionRevision: backend.extensionRevision,
      ui,
      input,
      onInput: setInput,
      onSend: () => void send(),
      onStop: () => void stop(),
      onDecide: (item, choice) => void decide(item, choice),
      onSendToSession: (sessionId, text, attachmentIds) =>
        void sendToSession(sessionId, text, attachmentIds),
      onAnswerClarification: answerClarification,
      onReplayRun: replayRun,
      busy,
      savingSettings,
      actionError,
      saveSettings,
      trustWorkspace,
      addWorkspace,
      refreshProfiles: () => void backend.refreshProfiles(),
      refreshModels: () => void backend.refreshModels(),
      refreshSkills: () => void backend.refreshSkills(),
      onNewSession: newSession,
    }),
    [
      backend,
      ui,
      input,
      busy,
      savingSettings,
      actionError,
      saveSettings,
      trustWorkspace,
      addWorkspace,
      send,
      stop,
      decide,
      sendToSession,
      answerClarification,
      replayRun,
      newSession,
    ]
  );

  const phaseLabel = useMemo(() => {
    if (ui.phase === "idle" && !busy) return null;
    return {
      label: t(`status.${ui.phase}`),
      tone: PHASE_TONE[ui.phase] ?? PHASE_TONE["idle"],
    };
  }, [ui.phase, busy]);

  const Page = match.spec.component;
  const workspace = backend.currentWorkspace;

  return (
    // 遮挡协调器装在壳层最外面：任何浮层（设置 dialog / 命令面板 / 审批框）
    // 都通过它报告遮挡，原生网页才不会被浮层之上的子窗口盖住。
    <BrowserOcclusionProvider>
    <ShellProvider value={shellValue}>
      <Layout
        status={backend.status}
        current={match.spec.id}
        onNavigate={navigate}
        search={<GlobalSearch onNavigate={navigate} />}
        history={
          <HistorySidebar
            workspaces={backend.workspaces}
            workspaceFilter={workspaceFilter}
            onFilterChange={setWorkspaceFilter}
            activeSessionId={match.params.id ?? null}
            onOpenSession={(id) => navigate(`/chat/${id}`)}
            onNewSession={() => void newSession()}
          />
        }
        footer={
          <RuntimeFooter
            status={backend.status}
            workspace={workspace}
            onOpenRuntime={() => navigate("/settings")}
            onOpenWorkspace={() => navigate("/settings")}
          />
        }
        rightOpen={rightOpen}
        onToggleRight={() => setRightOpen((v) => !v)}
        phaseLabel={phaseLabel}
        right={
          <DetailPanel
            ui={ui}
            status={backend.status}
            workspace={backend.currentWorkspace}
            phaseMeta={phaseLabel ?? { label: t("status.idle"), tone: PHASE_TONE["idle"] }}
            onGoRuns={() => navigate("/runs")}
          />
        }
      >
        <Page onNavigate={navigate} route={match} />
      </Layout>
    </ShellProvider>
    </BrowserOcclusionProvider>
  );
}
