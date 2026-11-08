// Pi 运行时设置（F08 发现 + F06 安装/回滚）。
//
// 这里展示的每一项都是后端事实，界面不推断：
//   - 候选的真实路径（启动器 + 真正被执行的运行时）、版本、发行形式、来源、
//     指纹与探测状态逐条列出；失效路径也照样出现（并标 missing），不静默消失；
//   - `ready_verified` / `ready_compatible` 之外的候选不能"选它继续用"：
//     低于最低支持版本只能提示升级；
//   - 安装只给版本，下载地址与哈希来自随二进制下发的受信清单；
//   - 进度事件只承载阶段，结论只来自 InstallPi 的返回值；
//   - 只有后端报告 `rollbackAvailable` 时才允许回滚，且把 installationId 原样传回。
//
// V1 只管理宿主 Pi：这里不出现 Codex CLI / Claude Code / 运行时市场等选项。

import { useCallback, useEffect, useMemo, useState } from "react";
import type { CallError } from "../../types";
import { hasKey, t } from "../../i18n";
import { AsyncState } from "../../components/states/AsyncState";
import { toCallError } from "../../app/useBackend";
import {
  onPiInstallProgress,
  settingsApi,
  type PiCandidate,
  type PiDiscoveryResult,
  type PiInstallResult,
} from "./api";
import {
  SettingsSection,
  BUTTON_CLASS,
  INPUT_CLASS,
  LABEL_CLASS,
  MONO_CLASS,
  ROW_CLASS,
  type SectionState,
} from "./Section";

/** 兼容状态的色调（语义 token，不写死颜色）。 */
const STATE_CLASS: Record<string, string> = {
  ready_verified: "border-[color:var(--anvil-success-border)] bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
  ready_compatible: "border-[color:var(--anvil-success-border)] bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
  upgrade_available: "border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
  incompatible: "border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
  failed: "border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
  installing: "border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] text-[color:var(--anvil-accent)]",
  rollback_available: "border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
  discovering: "border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]",
  missing: "border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]",
};

const NEUTRAL_STATE_CLASS =
  "border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]";

/** catalog 里有这条文案就用它，否则原样显示后端给的词（它是数据，不是文案）。 */
function domainLabel(prefix: string, value: string): string {
  const key = `${prefix}.${value}`;
  return hasKey(key) ? t(key) : value;
}

function stateLabel(state: string): string {
  return domainLabel("settings.pi.states", state);
}

function StatePill({ state, testId }: { state: string; testId?: string }) {
  return (
    <span
      data-testid={testId}
      data-state={state}
      className={[
        "inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] leading-none",
        STATE_CLASS[state] ?? NEUTRAL_STATE_CLASS,
      ].join(" ")}
    >
      {stateLabel(state)}
    </span>
  );
}

function isSelectable(state: string): boolean {
  // 只有"能直接跑"的候选可选：升级/兼容失败/失效路径都不是"继续用它"。
  return state === "ready_verified" || state === "ready_compatible";
}

export function PiSettings() {
  const [phase, setPhase] = useState<SectionState>("loading");
  const [error, setError] = useState<CallError | null>(null);
  const [discovery, setDiscovery] = useState<PiDiscoveryResult | null>(null);
  const [selecting, setSelecting] = useState<string | null>(null);
  const [selectError, setSelectError] = useState<CallError | null>(null);

  const [version, setVersion] = useState("");
  const [installing, setInstalling] = useState(false);
  const [stage, setStage] = useState("");
  const [installId, setInstallId] = useState("");
  const [result, setResult] = useState<PiInstallResult | null>(null);
  const [rollbacking, setRollbacking] = useState(false);

  const discover = useCallback(async () => {
    setPhase("loading");
    setError(null);
    try {
      const res = await settingsApi.discoverPi();
      setDiscovery(res ?? null);
      setPhase("success");
    } catch (e) {
      setError(toCallError(e));
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void discover();
  }, [discover]);

  // 进度只更新阶段；结论永远等 InstallPi 的返回值。
  useEffect(() => {
    return onPiInstallProgress((ev) => {
      if (ev?.stage) setStage(ev.stage);
      if (ev?.installId) setInstallId(ev.installId);
    });
  }, []);

  async function select(candidate: PiCandidate) {
    setSelecting(candidate.installation.id);
    setSelectError(null);
    try {
      const res = await settingsApi.selectPi(candidate.installation.id);
      if (res) setDiscovery(res);
    } catch (e) {
      setSelectError(toCallError(e));
    } finally {
      setSelecting(null);
    }
  }

  async function install() {
    const target = version.trim();
    if (!target || installing) return;
    setInstalling(true);
    setStage("");
    setInstallId("");
    setResult(null);
    setError(null);
    try {
      const res = await settingsApi.installPi(target);
      setResult(res ?? null);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setInstalling(false);
    }
  }

  async function rollback() {
    if (!result?.rollbackAvailable || !result.installationId) return;
    setRollbacking(true);
    try {
      const res = await settingsApi.rollbackPi(result.installationId);
      setResult(res ?? null);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setRollbacking(false);
    }
  }

  const candidates = discovery?.candidates ?? [];
  const canRollback = Boolean(result?.rollbackAvailable && result?.installationId);

  /**
   * 推荐项：后端已按 ready_verified → ready_compatible → 其它排好序，且 Selected
   * 是它算出的最可用候选。这里只**标记**，不替用户选：外部二进制要用户点一下
   * 「选用」才算确认（静默选中 = 让用户跑了一个他没确认过的二进制）。
   */
  const recommendedId = useMemo(() => {
    if (discovery?.selected) return discovery.selected.installation.id;
    const firstUsable = candidates.find((c) => isSelectable(c.compatibility));
    return firstUsable?.installation.id ?? "";
  }, [discovery?.selected, candidates]);

  const needsConfirm = candidates.length > 0 && !discovery?.selected;

  return (
    <SettingsSection id="pi" titleKey="settings.pi.title" hintKey="settings.pi.hint" state={phase}>
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className={BUTTON_CLASS} disabled={phase === "loading"} onClick={() => void discover()}>
          {phase === "loading" ? t("settings.pi.discovering") : t("settings.pi.discover")}
        </button>
        {discovery?.state ? <StatePill state={discovery.state} testId="pi-discovery-state" /> : null}
      </div>

      {selectError ? <AsyncState phase="error" error={selectError} /> : null}

      {phase === "error" ? (
        <AsyncState phase="error" error={error} onRetry={() => void discover()} />
      ) : null}

      {phase === "success" ? (
        candidates.length === 0 ? (
          <AsyncState
            phase="empty"
            emptyTitle={t("settings.pi.emptyTitle")}
            emptyHint={t("settings.pi.emptyHint")}
          />
        ) : (
          <>
            {needsConfirm ? (
              <p
                data-testid="pi-confirm-hint"
                className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-warning)]"
              >
                {t("settings.pi.confirmHint")}
              </p>
            ) : null}
            <ul data-testid="pi-candidates" className="mt-2 space-y-1.5">
            {candidates.map((c) => {
              const id = c.installation.id;
              const selected = discovery?.selected?.installation.id === id;
              return (
                <li key={id} data-testid={`pi-candidate-${id}`} className={ROW_CLASS}>
                  <div className="flex flex-wrap items-center gap-2">
                    <StatePill state={c.compatibility} testId="pi-state" />
                    <code className="break-all font-mono text-[11px] text-[color:var(--anvil-foreground)]">
                      {c.launcherPath || c.installation.executable}
                    </code>
                    <span className="text-[11px] text-[color:var(--anvil-muted)]">
                      {t("settings.pi.version")}
                      {": "}
                      {c.installation.version || t("common.unknown")}
                    </span>
                    <span className="text-[10px] text-[color:var(--anvil-muted)]">
                      {domainLabel("settings.pi.origin", c.origin)}
                    </span>
                    <span className="text-[10px] text-[color:var(--anvil-muted)]">
                      {domainLabel("settings.pi.distribution", c.distribution)}
                    </span>
                    {selected ? (
                      <span data-testid="pi-selected" className="text-[10px] text-[color:var(--anvil-success)]">
                        {t("settings.pi.selected")}
                      </span>
                    ) : null}
                    {!selected && id === recommendedId ? (
                      <span
                        data-testid="pi-recommended"
                        title={t("settings.pi.recommendedHint")}
                        className="rounded-full border border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-accent)]"
                      >
                        {t("settings.pi.recommended")}
                      </span>
                    ) : null}
                    <button
                      type="button"
                      className={BUTTON_CLASS}
                      disabled={!isSelectable(c.compatibility) || selecting === id || selected}
                      onClick={() => void select(c)}
                    >
                      {t("settings.pi.select")}
                    </button>
                  </div>

                  <div className="mt-1 space-y-0.5">
                    <div className={MONO_CLASS}>
                      {t("settings.pi.hash")}
                      {": "}
                      <code data-testid="pi-executable-hash">{c.executableHash || t("common.unknown")}</code>
                    </div>
                    <div className={MONO_CLASS} data-testid="pi-runtime">
                      {c.runtimeExecutable || t("common.unknown")}
                      {" · "}
                      <code data-testid="pi-runtime-hash">{c.runtimeHash || t("common.unknown")}</code>
                    </div>
                    {c.diagnostics && c.diagnostics.length > 0 ? (
                      <ul data-testid="pi-diagnostics" className="space-y-0.5">
                        {c.diagnostics.map((d) => (
                          <li key={d} className={MONO_CLASS}>
                            {d}
                          </li>
                        ))}
                      </ul>
                    ) : null}
                  </div>
                </li>
              );
            })}
            </ul>
          </>
        )
      ) : null}

      <div className="mt-3 space-y-2">
        <p className={LABEL_CLASS}>{t("settings.pi.installTitle")}</p>
        <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("settings.pi.installHint")}
        </p>
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1">
            <span className={LABEL_CLASS}>{t("settings.pi.installVersion")}</span>
            <input
              aria-label={t("settings.pi.installVersion")}
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              className={INPUT_CLASS}
              placeholder="1.5.0"
            />
          </label>
          <button
            type="button"
            className={BUTTON_CLASS}
            disabled={installing || version.trim() === ""}
            onClick={() => void install()}
          >
            {installing ? t("settings.pi.installing") : t("settings.pi.install")}
          </button>
          {installing && installId ? (
            <button
              type="button"
              className={BUTTON_CLASS}
              onClick={() => {
                // 取消是一次真实请求：失败要显示 code，不能静默吞掉。
                void settingsApi
                  .cancelPiInstall(installId)
                  .catch((e: unknown) => setError(toCallError(e)));
              }}
            >
              {t("settings.pi.cancelInstall")}
            </button>
          ) : null}
          <button type="button" className={BUTTON_CLASS} disabled={!canRollback || rollbacking} onClick={() => void rollback()}>
            {rollbacking ? t("settings.pi.rollingBack") : t("settings.pi.rollback")}
          </button>
        </div>

        {stage ? (
          <p data-testid="pi-install-stage" className="text-[11px] text-[color:var(--anvil-muted)]">
            {domainLabel("settings.pi.stage", stage)}
          </p>
        ) : null}

        {result ? (
          <div data-testid="pi-install-result" data-state={result.state} className={ROW_CLASS}>
            <div className="flex flex-wrap items-center gap-2">
              <StatePill state={result.state} testId="pi-install-state" />
              {result.version ? (
                <span className="text-[11px] text-[color:var(--anvil-foreground)]">{result.version}</span>
              ) : null}
              {result.previousVersion ? (
                <span data-testid="pi-install-previous" className="text-[10px] text-[color:var(--anvil-muted)]">
                  {t("settings.pi.previousVersion")}
                  {": "}
                  {result.previousVersion}
                </span>
              ) : null}
            </div>
            <div className={MONO_CLASS}>
              {t("settings.pi.managedRoot")}
              {": "}
              {result.managedRoot}
            </div>
            {result.executable ? <div className={MONO_CLASS}>{result.executable}</div> : null}
            {result.executableSha256 ? (
              <div className={MONO_CLASS}>
                <code data-testid="pi-install-hash">{result.executableSha256}</code>
              </div>
            ) : null}
            {result.failedStage ? (
              <div data-testid="pi-install-failed-stage" className="text-[11px] text-[color:var(--anvil-danger)]">
                {t("settings.pi.failedStage")}
                {": "}
                {domainLabel("settings.pi.stage", result.failedStage)}
              </div>
            ) : null}
            {result.error ? (
              <pre
                data-testid="pi-install-detail"
                className="mt-1 whitespace-pre-wrap break-all font-mono text-[10px] text-[color:var(--anvil-muted)]"
              >
                {result.error}
              </pre>
            ) : null}
            <p className="mt-1 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
              {t("settings.pi.rollbackHint")}
            </p>
          </div>
        ) : null}
      </div>
    </SettingsSection>
  );
}
