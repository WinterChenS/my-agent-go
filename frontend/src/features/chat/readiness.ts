// 发送前的前提判定（UX1）。
//
// 为什么要有这一层：全新数据目录下发送必然失败——没有 settings.piPath、
// 工作区未信任、默认 provider 是内置 fixture（127.0.0.1:0）——但界面只在
// 点完发送之后才报错。用户看到的是一次"点了没反应"或一句后端内部措辞，
// 而真正缺的东西（Pi / 信任 / 模型）根本没有出现在屏幕上。
//
// 这里只做判定，不画界面、不发请求：
//   - 判据全部来自后端事实（DiscoverPi 的 compatibility、当前工作区、当前模型）；
//   - 每个不满足项都带一个"跳到哪个设置分区"的定位串；
//   - 能就地解决的（工作区信任）另带一个 action：界面直接给按钮，
//     而不是把人赶去另一个页面（WTRUST，2026-11-08）；
//   - 内置 fixture 既算一条阻塞原因，也单独作为标记返回（界面要显示徽标）。
//
// 不重新实现一遍兼容性判定：ready_verified / ready_compatible 是后端结合
// 版本、指纹与契约探测得出的结论。这里只额外守一条最低版本下限，因为
// 1.4.0 这种"低于最低支持版本"的候选用起来必然失败。

import type { CallError, CurrentModelResult, Settings, Workspace } from "../../types";
import type { PiCandidate, PiDiscoveryResult } from "../settings/api";

/** 与 internal/runtime/pi 的 DefaultPolicy.Minimum 对齐：低于它的 Pi 不能用。 */
export const MIN_PI_VERSION = "1.5.0";

/** 可直接使用的兼容状态（其余状态都意味着"还不能跑"）。 */
const USABLE_STATES = new Set(["ready_verified", "ready_compatible"]);

export type ReadinessId = "pi" | "workspace" | "model";

/** 跳转目标：设置页的分区 id（与 SettingsSection 的 id 一致）。 */
export type ReadinessSection = "pi" | "permissions" | "model";

export type ReadinessCode =
  | "PI_MISSING"
  | "PI_TOO_OLD"
  | "PI_UNUSABLE"
  | "PI_UNKNOWN"
  | "WORKSPACE_MISSING"
  | "WORKSPACE_UNTRUSTED"
  | "MODEL_MISSING"
  | "MODEL_FIXTURE"
  | "MODEL_ENDPOINT";

export interface ReadinessTarget {
  /** 逻辑路由 + 查询串：设置页据此定位分区。 */
  route: string;
  section: ReadinessSection;
}

export interface ReadinessBlocker {
  /** 是哪一类前提（工作区归到 workspace，跳转分区是 permissions）。 */
  id: ReadinessId;
  code: ReadinessCode;
  /** 文案参数：全部是后端事实（路径/版本/provider/模型/端点）。 */
  params: Record<string, string>;
  target: ReadinessTarget;
  /**
   * 可以**就地**解决的动作（不是"跳到别处自己找"）。
   *
   * 只有后端真有的绑定才放进这里：界面不发明动作，也不在点击后假装置位。
   */
  action?: ReadinessAction;
}

/**
 * 就绪阻断项的就地动作。
 *
 * `path` 必须带上：按钮的可访问名要写清"信任的是哪个绝对路径"，
 * 否则同一句话在两个不同目录下看起来一模一样，用户无从核对。
 */
export interface ReadinessAction {
  kind: "trust_workspace";
  /** 要信任的工作区 id（后端事实，不是前端从路径推的）。 */
  workspaceId: string;
  /** 该工作区的绝对路径。 */
  path: string;
}

/** 内置 fixture 的信号：脚本化回放，不是真模型。 */
export interface FixtureSignal {
  provider: string;
  model: string;
  baseUrl: string;
}

export interface ReadinessReport {
  ready: boolean;
  blockers: ReadinessBlocker[];
  fixture: FixtureSignal | null;
}

export interface ReadinessInput {
  discovery: PiDiscoveryResult | null;
  discoveryError: CallError | null;
  currentWorkspace: Workspace | null;
  currentModel: CurrentModelResult | null;
  settings: Settings | null;
}

function target(section: ReadinessSection): ReadinessTarget {
  return { route: `/settings?section=${section}`, section };
}

function blocker(
  id: ReadinessId,
  code: ReadinessCode,
  params: Record<string, string>,
  section: ReadinessSection
): ReadinessBlocker {
  return { id, code, params, target: target(section) };
}

/**
 * 按段比较版本号（`v1.5.0` / `1.5.0-beta.1` 都按 1.5.0 处理）。
 *
 * 读不出数字时返回 NaN：调用方必须显式决定"未知版本"怎么算，
 * 而不是让 `"" < "1.5.0"` 这种字符串比较偶然给出结论。
 */
export function compareVersions(a: string, b: string): number {
  const parse = (value: string): [number, number, number] | null => {
    const m = /^[vV]?(\d+)\.(\d+)\.(\d+)/.exec((value ?? "").trim());
    if (!m) return null;
    return [Number(m[1]), Number(m[2]), Number(m[3])];
  };
  const left = parse(a);
  const right = parse(b);
  if (!left || !right) return Number.NaN;
  for (let i = 0; i < 3; i++) {
    if (left[i] !== right[i]) return left[i] < right[i] ? -1 : 1;
  }
  return 0;
}

/**
 * 内置 fixture 的判定。
 *
 * 判据是 provider（默认设置就是 `fixture`），不是端口：端口 0 只说明
 * "这个端点写错了"，把它当成 fixture 会把真实失败说成"回放"。
 */
export function fixtureSignal(
  currentModel: CurrentModelResult | null,
  settings: Settings | null
): FixtureSignal | null {
  if (!currentModel || currentModel.source !== "settings") return null;
  const provider = (currentModel.provider ?? "").trim();
  if (provider.toLowerCase() !== "fixture") return null;
  return {
    provider,
    model: (currentModel.model ?? "").trim(),
    baseUrl: (settings?.baseUrl ?? "").trim(),
  };
}

/** `http://127.0.0.1:0/v1`：端口 0 的端点在客户端永远连不上。 */
function isPlaceholderEndpoint(baseUrl: string): boolean {
  const value = (baseUrl ?? "").trim();
  if (!value) return false;
  const match = /^(?:[a-z][a-z0-9+.-]*):\/\/[^/]*?:(\d+)(?:[/?#]|$)/i.exec(value);
  return match !== null && match[1] === "0";
}

/** 最接近可用的那个候选（后端已按 ready_verified → ready_compatible → 其它排序）。 */
function closestCandidate(discovery: PiDiscoveryResult): PiCandidate | null {
  if (discovery.selected) return discovery.selected;
  const candidates = discovery.candidates ?? [];
  return candidates[0] ?? null;
}

function evaluatePi(input: ReadinessInput): ReadinessBlocker | null {
  if (input.discoveryError) {
    return blocker("pi", "PI_UNKNOWN", { code: input.discoveryError.code }, "pi");
  }
  if (!input.discovery) {
    // 没有探测结果也没有错误：这是"还没检查"，界面用 checking 状态表达；
    // 判定层保守地不放行，并且不编造一个后端事实。
    return blocker("pi", "PI_UNKNOWN", { code: "UNKNOWN" }, "pi");
  }
  const probe = closestCandidate(input.discovery);
  if (!probe) return blocker("pi", "PI_MISSING", {}, "pi");

  const version = (probe.installation.version ?? "").trim();
  const belowMinimum = compareVersions(version, MIN_PI_VERSION) < 0;

  if (probe.compatibility === "upgrade_available" || belowMinimum) {
    return blocker("pi", "PI_TOO_OLD", { version, minimum: MIN_PI_VERSION }, "pi");
  }
  if (!USABLE_STATES.has(probe.compatibility)) {
    return blocker(
      "pi",
      "PI_UNUSABLE",
      {
        version,
        state: probe.compatibility,
        path: probe.launcherPath || probe.installation.executable || "",
      },
      "pi"
    );
  }
  return null;
}

function evaluateWorkspace(workspace: Workspace | null): ReadinessBlocker | null {
  if (!workspace) return blocker("workspace", "WORKSPACE_MISSING", {}, "permissions");
  if (!workspace.trusted) {
    // 就地可以授权：信任只覆盖"执行"，所以这一步不要求用户先去别处翻设置。
    return {
      ...blocker("workspace", "WORKSPACE_UNTRUSTED", { path: workspace.canonicalPath }, "permissions"),
      action: { kind: "trust_workspace", workspaceId: workspace.id, path: workspace.canonicalPath },
    };
  }
  return null;
}

function evaluateModel(input: ReadinessInput): ReadinessBlocker | null {
  const model = input.currentModel;
  const provider = (model?.provider ?? "").trim();
  const modelId = (model?.model ?? "").trim();
  if (!model || model.source === "none" || (provider === "" && modelId === "")) {
    return blocker("model", "MODEL_MISSING", {}, "model");
  }
  // 内置 fixture：**端点还活着就允许发送**，只打徽标（脚本化回放）。
  // 只有端点本身是占位（:0 之类，客户端永远连不上）才阻塞——否则"用本地
  // fixture 验证整条链路"这条路径会被自己的阻塞面板挡死（2026-11-08 实测：
  // run-fixture.bat 起的一键演示因此发不出消息）。
  if (fixtureSignal(model, input.settings) && isPlaceholderEndpoint(input.settings?.baseUrl ?? "")) {
    return blocker("model", "MODEL_FIXTURE", { provider, model: modelId }, "model");
  }
  // 旧字段兜底路径（settings）才会用到 baseUrl；资源库里的端点由模型定义给出。
  if (model.source === "settings" && isPlaceholderEndpoint(input.settings?.baseUrl ?? "")) {
    return blocker(
      "model",
      "MODEL_ENDPOINT",
      { baseUrl: (input.settings?.baseUrl ?? "").trim() },
      "model"
    );
  }
  return null;
}

/**
 * 判定当前能不能发送一条消息。
 *
 * 顺序固定（Pi → 工作区 → 模型）：界面按这个顺序列阻塞原因，
 * 用户从上到下修，第一屏就能看到最先要解决的那件事。
 */
export function evaluateReadiness(input: ReadinessInput): ReadinessReport {
  const blockers = [evaluatePi(input), evaluateWorkspace(input.currentWorkspace), evaluateModel(input)].filter(
    (item): item is ReadinessBlocker => item !== null
  );
  return {
    ready: blockers.length === 0,
    blockers,
    fixture: fixtureSignal(input.currentModel, input.settings),
  };
}
