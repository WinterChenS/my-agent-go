// 发送前提的判定（UX1）。
//
// 这一层不画界面，只把"现在能不能发一条消息"变成可断言的事实：
//   - 每个不满足项都要给出**后端事实**（路径/版本/工作区/provider/模型），
//     而不是一句"还没配置"；
//   - 每一项都要有跳到对应设置分区的定位串（Pi / 权限（工作区）/ 模型）；
//   - 内置 fixture 不是真模型：它既是一条阻塞原因，也是一个必须显示出来的标记。
//
// 判据来自后端：DiscoverPi 的 compatibility 已经是"版本 + 指纹 + 契约探测"的
// 结论，这里不重新实现一遍；只有最低版本做一层防御性检查，因为界面不能把
// 1.4.0 这种"低于最低支持版本"的候选当成可用。

import { describe, expect, it } from "vitest";
import { compareVersions, evaluateReadiness, MIN_PI_VERSION, type ReadinessInput } from "./readiness";
import type { PiDiscoveryResult } from "../settings/api";
import type { CurrentModelResult, Settings, Workspace } from "../../types";

const WORKSPACE: Workspace = {
  id: "ws-1",
  name: "demo",
  canonicalPath: "D:/work/demo",
  trusted: true,
  createdAt: "2026-11-01T00:00:00Z",
};

const SETTINGS: Settings = {
  piPath: "",
  nodePath: "",
  provider: "openai",
  baseUrl: "https://api.example.com/v1",
  model: "gpt-5",
  apiKey: "",
  modelRef: "model-1",
  workspace: "D:/work/demo",
  shellAllowed: false,
};

function candidate(overrides: Record<string, unknown> = {}) {
  return {
    installation: {
      id: "managed:1.5.0",
      executable: "D:/anvil/pi/1.5.0/pi.exe",
      origin: "managed",
      architecture: "x86_64",
      source: "managed",
      runnable: true,
      version: "1.5.0",
    },
    origin: "managed",
    distribution: "standalone",
    executableHash: "a".repeat(64),
    launcherPath: "D:/anvil/pi/1.5.0/pi.exe",
    runtimeExecutable: "D:/anvil/pi/1.5.0/pi.exe",
    runtimeHash: "b".repeat(64),
    compatibility: "ready_verified",
    diagnostics: [],
    ...overrides,
  } as PiDiscoveryResult["candidates"] extends (infer T)[] | null ? T : never;
}

function discovery(overrides: Partial<PiDiscoveryResult> = {}): PiDiscoveryResult {
  const selected = candidate();
  return {
    state: "ready_verified",
    selected,
    candidates: [selected],
    diagnostics: [],
    ...overrides,
  };
}

const RESOURCE_MODEL: CurrentModelResult = {
  source: "resources",
  ref: "model-1",
  provider: "openai",
  model: "gpt-5",
  item: {
    id: "model-1",
    providerId: "openai",
    modelId: "gpt-5",
    api: "openai-responses",
    baseUrl: "https://api.example.com/v1",
    inputKinds: ["text"],
    contextWindow: 128000,
    maxOutput: 8192,
    credentialRef: "cred-1",
    thinkingLevels: null,
  },
  revision: 3,
};

/**
 * 旧字段路径（source=settings）的当前模型。
 *
 * Go 的 CurrentModel 在这条路径上只填 source/provider/model，item 与 revision
 * 是零值；这里如实表达"没有模型定义"，不编一份假的 item。
 */
function settingsModel(provider: string, model: string): CurrentModelResult {
  return { source: "settings", ref: "", provider, model, item: {} as CurrentModelResult["item"], revision: 0 };
}

function input(overrides: Partial<ReadinessInput> = {}): ReadinessInput {
  return {
    discovery: discovery(),
    discoveryError: null,
    currentWorkspace: WORKSPACE,
    currentModel: RESOURCE_MODEL,
    settings: SETTINGS,
    ...overrides,
  };
}

describe("compareVersions", () => {
  it("按段比较并忽略前导 v/预发布后缀，不靠字符串排序", () => {
    expect(compareVersions("1.4.0", "1.5.0")).toBeLessThan(0);
    expect(compareVersions("v1.5.0", "1.5.0")).toBe(0);
    expect(compareVersions("1.5.1", "1.5.0")).toBeGreaterThan(0);
    expect(compareVersions("1.10.0", "1.9.9")).toBeGreaterThan(0);
    expect(compareVersions("1.5.0-beta.1", "1.5.0")).toBe(0);
  });

  it("读不出数字的版本不当成可用：返回 NaN 让调用方显式处理", () => {
    expect(Number.isNaN(compareVersions("", "1.5.0"))).toBe(true);
    expect(Number.isNaN(compareVersions("unknown", "1.5.0"))).toBe(true);
  });
});

describe("发送前提（readiness）", () => {
  it("全新状态：Pi 缺失、工作区未信任、内置 fixture 三条都列出来，并给出跳转分区", () => {
    const report = evaluateReadiness(
      input({
        discovery: { state: "missing", selected: null, candidates: [], diagnostics: [] },
        discoveryError: null,
        currentWorkspace: { ...WORKSPACE, trusted: false },
        currentModel: settingsModel("fixture", "fixture-model"),
        settings: { ...SETTINGS, provider: "fixture", baseUrl: "http://127.0.0.1:0/v1", model: "fixture-model", modelRef: "" },
      })
    );

    expect(report.ready).toBe(false);
    expect(report.blockers.map((b) => b.code)).toEqual(["PI_MISSING", "WORKSPACE_UNTRUSTED", "MODEL_FIXTURE"]);
    expect(report.blockers.map((b) => b.target.route)).toEqual([
      "/settings?section=pi",
      "/settings?section=permissions",
      "/settings?section=model",
    ]);
    // 当前值来自后端事实：未信任的工作区要说清是哪个目录。
    expect(report.blockers[1].params.path).toBe("D:/work/demo");
    // 工作区未信任可以就地解决：动作带工作区 id 与绝对路径（不是只有一句“去设置”）。
    expect(report.blockers[1].action).toEqual({
      kind: "trust_workspace",
      workspaceId: WORKSPACE.id,
      path: WORKSPACE.canonicalPath,
    });
    // 其它阻塞项没有就地动作：不发明后端没有的绑定。
    expect(report.blockers[0].action).toBeUndefined();
    expect(report.blockers[2].action).toBeUndefined();
  });

  it("Pi 低于最低支持版本（upgrade_available）：原因里带版本与最低版本", () => {
    const old = candidate({
      installation: {
        id: "path:1.4.0",
        executable: "C:/tools/pi/pi.exe",
        origin: "external",
        architecture: "x86_64",
        runnable: true,
        version: "1.4.0",
      },
      origin: "path",
      compatibility: "upgrade_available",
    });
    const report = evaluateReadiness(
      input({
        discovery: { state: "upgrade_available", selected: null, candidates: [old], diagnostics: [] },
      })
    );

    expect(report.ready).toBe(false);
    const pi = report.blockers.find((b) => b.id === "pi");
    expect(pi?.code).toBe("PI_TOO_OLD");
    expect(pi?.params.version).toBe("1.4.0");
    expect(pi?.params.minimum).toBe(MIN_PI_VERSION);
    expect(pi?.target.route).toBe("/settings?section=pi");
  });

  it("后端标成 ready 但版本低于最低版本：界面不放行（防御性下限）", () => {
    const below = candidate({
      installation: {
        id: "path:1.4.0",
        executable: "C:/tools/pi/pi.exe",
        origin: "external",
        architecture: "x86_64",
        runnable: true,
        version: "1.4.0",
      },
      compatibility: "ready_compatible",
    });
    const report = evaluateReadiness(
      input({ discovery: { state: "ready_compatible", selected: below, candidates: [below], diagnostics: [] } })
    );

    expect(report.ready).toBe(false);
    expect(report.blockers[0].code).toBe("PI_TOO_OLD");
  });

  it("Pi 探测失败：不假装可用，给出可重试的原因", () => {
    const report = evaluateReadiness(
      input({
        discovery: null,
        discoveryError: { code: "INTERNAL_ERROR", message: "probe failed", retryable: true },
      })
    );

    expect(report.ready).toBe(false);
    expect(report.blockers[0].code).toBe("PI_UNKNOWN");
    expect(report.blockers[0].params.code).toBe("INTERNAL_ERROR");
  });

  it("工作区未登记是一条独立的阻塞原因（不是「未信任」）", () => {
    const report = evaluateReadiness(input({ currentWorkspace: null }));
    expect(report.blockers.find((b) => b.id === "workspace")?.code).toBe("WORKSPACE_MISSING");
  });

  it("一切满足时没有阻塞，fixture 标记为空", () => {
    const report = evaluateReadiness(input());
    expect(report.blockers).toEqual([]);
    expect(report.ready).toBe(true);
    expect(report.fixture).toBeNull();
  });

  it("provider 为空只在内置 fixture 端点算「脚本化回放」；真 provider 的端口 0 不算", () => {
    const fixture = evaluateReadiness(
      input({
        currentModel: settingsModel("fixture", "fixture-model"),
        settings: { ...SETTINGS, provider: "fixture", baseUrl: "http://127.0.0.1:0/v1", modelRef: "" },
      })
    );
    expect(fixture.fixture).toMatchObject({ provider: "fixture", model: "fixture-model" });
    expect(fixture.blockers.map((b) => b.code)).toContain("MODEL_FIXTURE");

    // 端口 0 但不是内置 fixture provider：这是用户自己写错的端点，
    // 不能悄悄当成 fixture（那会把真实失败说成"回放"），但它同样发不出去，
    // 所以是一条独立的「占位端点」阻塞原因。
    const portZero = evaluateReadiness(
      input({
        currentModel: settingsModel("openai", "gpt-5"),
        settings: { ...SETTINGS, provider: "openai", baseUrl: "http://127.0.0.1:0/v1", modelRef: "" },
      })
    );
    expect(portZero.fixture).toBeNull();
    expect(portZero.blockers.map((b) => b.code)).not.toContain("MODEL_FIXTURE");
    expect(portZero.blockers.map((b) => b.code)).toContain("MODEL_ENDPOINT");
  });

  it("没有模型（source=none）是 MODEL_MISSING，不是 fixture", () => {
    const report = evaluateReadiness(
      input({
        currentModel: { source: "none", ref: "", provider: "", model: "", item: {} as CurrentModelResult["item"], revision: 0 },
        settings: { ...SETTINGS, provider: "", baseUrl: "", model: "", modelRef: "" },
      })
    );
    expect(report.blockers.find((b) => b.id === "model")?.code).toBe("MODEL_MISSING");
    expect(report.fixture).toBeNull();
  });
});
