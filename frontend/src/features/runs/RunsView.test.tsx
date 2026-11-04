// 运行记录「技能」行的口径测试（C03）。
//
// 2026-11-04 起 Pi 的本地技能发现保持打开：Pi 会自己加载 $HOME/.agents/skills
// 等位置里它发现的技能，那份清单产品拿不到。所以运行记录必须把两段事实分开说：
//
//   - 托管：这一轮通过 --skill 显式加载了哪些 ID（空 = 未显式加载）；
//   - Pi 本地自动发现：开关状态（清单未知）。
//
// 这里同时守一条**旧文案的禁令**：任何情况下都不能再出现「未加载任何技能」。
// 自动发现开着时那句话是假的——托管为空不代表 Pi 没加载技能。

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { RunsView } from "./RunsView";
import type { RunMeta, UiState } from "../../runState";
import type { Workspace } from "../../types";

const workspace: Workspace = {
  id: "ws-1",
  name: "proj",
  canonicalPath: "D:\\work\\proj",
  trusted: true,
  createdAt: "2026-11-04T00:00:00Z",
};

const FORBIDDEN = "未加载任何技能";

function uiWithMeta(meta: RunMeta | null): UiState {
  return {
    phase: meta ? "completed" : "idle",
    runId: meta ? "run-1" : null,
    messages: [],
    approvals: [],
    error: null,
    lastSeqByRun: meta ? { "run-1": 12 } : {},
    msgSeq: 0,
    runMeta: meta,
  };
}

function baseMeta(overrides: Partial<RunMeta> = {}): RunMeta {
  return {
    provider: "deepseek",
    model: "deepseek-v4.1-flash",
    configRevision: 9,
    managedSkills: [],
    skillAutoDiscovery: "enabled",
    ...overrides,
  };
}

describe("RunsView 技能行", () => {
  it("有托管技能时：列出 ID，并标注自动发现开着（清单未知）", () => {
    render(
      <RunsView
        ui={uiWithMeta(baseMeta({ managedSkills: ["demo-skill", "review-skill"] }))}
        workspace={workspace}
      />
    );

    expect(screen.getByTestId("run-managed-skills")).toHaveTextContent(
      "托管：demo-skill、review-skill"
    );
    expect(screen.getByTestId("run-skill-autodiscovery")).toHaveTextContent(
      "Pi 本地自动发现：已开启（清单未知）"
    );
    expect(screen.queryByText(FORBIDDEN)).toBeNull();
  });

  it("托管为空但自动发现开着：说「未显式加载」，不能说没有技能", () => {
    render(<RunsView ui={uiWithMeta(baseMeta({ managedSkills: [] }))} workspace={workspace} />);

    // 空数组只说明托管这一半：确实没有 --skill 传下去。
    expect(screen.getByTestId("run-managed-skills")).toHaveTextContent("托管：未显式加载");
    // 自动发现仍然开着，清单未知。
    expect(screen.getByTestId("run-skill-autodiscovery")).toHaveTextContent(
      "Pi 本地自动发现：已开启（清单未知）"
    );
    // 旧文案在自动发现开启时不成立，必须消失。
    expect(screen.queryByText(FORBIDDEN)).toBeNull();
    expect(screen.queryByText(/未加载任何技能/)).toBeNull();
  });

  it("自动发现关闭时：明确写「已关闭」，仍然不出现旧文案", () => {
    render(
      <RunsView
        ui={uiWithMeta(baseMeta({ managedSkills: [], skillAutoDiscovery: "disabled" }))}
        workspace={workspace}
      />
    );

    expect(screen.getByTestId("run-skill-autodiscovery")).toHaveTextContent(
      "Pi 本地自动发现：已关闭"
    );
    expect(screen.getByTestId("run-managed-skills")).toHaveTextContent("托管：未显式加载");
    expect(screen.queryByText(FORBIDDEN)).toBeNull();
  });

  it("自动发现状态没记录时显示「未知」，不默认成任何一边", () => {
    const meta = baseMeta();
    delete meta.skillAutoDiscovery;
    render(<RunsView ui={uiWithMeta(meta)} workspace={workspace} />);

    expect(screen.getByTestId("run-skill-autodiscovery")).toHaveTextContent(
      "Pi 本地自动发现：未知"
    );
  });

  it("run 已建立但快照里没有技能记录时，技能行写「尚未启动」而不猜", () => {
    render(
      <RunsView
        ui={{ ...uiWithMeta(null), runId: "run-1", phase: "running" }}
        workspace={workspace}
      />
    );

    expect(screen.getByTestId("run-skills")).toHaveTextContent("技能尚未启动");
    // 没有快照就不画托管/自动发现两段，避免用默认值冒充事实。
    expect(screen.queryByTestId("run-managed-skills")).toBeNull();
    expect(screen.queryByTestId("run-skill-autodiscovery")).toBeNull();
    expect(screen.queryByText(FORBIDDEN)).toBeNull();
  });

  it("还没启动任何 run 时只显示空态，不画技能行", () => {
    render(<RunsView ui={uiWithMeta(null)} workspace={null} />);

    expect(screen.getByText("还没有运行记录")).toBeInTheDocument();
    expect(screen.queryByTestId("run-skills")).toBeNull();
  });
});
