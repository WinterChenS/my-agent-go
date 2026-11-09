// 设置 · 权限 → 工作区信任（WTRUST）。
//
// 这一组用例守的是规格 docs/specs/2026-11-08-workspace-trust.md 的第 ②③④ 条：
//   - 已信任与未信任**都在列表里**（只列已信任会让人以为其它目录不存在）；
//   - 每行给名称 / 绝对路径 /（已信任时的）信任时间 / 信任或撤销；
//   - 空态说“还没有工作区”，不能长得像加载失败；
//   - 撤销后状态翻转，并说明“当前运行不受影响，下一条消息需要重新信任”；
//   - 添加目录**不要求信任**：选目录只是“我要在这里工作”，不是“允许执行”。
//
// 壳层用替身（与 SettingsPage.test 同一模式）：这里测的是这一节怎么读事实、
// 怎么转发动作，不是 Wails 绑定本身。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { formatDate, t } from "../../i18n";
import type { Workspace } from "../../types";

const fake = vi.hoisted(() => ({
  agentProfileCeiling: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({ settingsApi: fake }));

const shellState = vi.hoisted(() => ({ value: null as any }));
vi.mock("../../app/shell", () => ({ useShell: () => shellState.value }));

import { PermissionSettings } from "./PermissionSettings";
import { recordWorkspaceTrust, resetWorkspaceTrustTimes } from "../../app/trustLog";

const TRUSTED: Workspace = {
  id: "ws-trusted",
  name: "生产项目",
  canonicalPath: "D:/work/prod",
  trusted: true,
  createdAt: "2026-11-01T00:00:00Z",
};

const UNTRUSTED: Workspace = {
  id: "ws-fresh",
  name: "新目录",
  canonicalPath: "D:/work/fresh",
  trusted: false,
  createdAt: "2026-11-02T00:00:00Z",
};

/** 壳层替身：trustWorkspace/addWorkspace 由用例安排，列表由用例摆事实。 */
function shell(overrides: Record<string, any> = {}) {
  return {
    settings: null,
    status: null,
    workspaces: [TRUSTED, UNTRUSTED],
    currentWorkspace: UNTRUSTED,
    savingSettings: false,
    actionError: null,
    saveSettings: vi.fn(async () => {}),
    trustWorkspace: vi.fn(async (_id: string, _trusted: boolean) => {}),
    addWorkspace: vi.fn(async (_path: string) => {}),
    ...overrides,
  };
}

async function renderSection(overrides: Record<string, any> = {}) {
  shellState.value = shell(overrides);
  const view = render(<PermissionSettings />);
  await screen.findByTestId("settings-permissions");
  return view;
}

beforeEach(() => {
  fake.agentProfileCeiling = vi.fn(async () => ({
    ceiling: { filesystem: "workspace", network: false, shell: true, sources: {}, available: true },
    unavailableFields: null,
  }));
  resetWorkspaceTrustTimes();
});

afterEach(() => {
  vi.clearAllMocks();
  resetWorkspaceTrustTimes();
});

describe("设置 · 工作区：列出已信任与未信任", () => {
  it("两种状态都在列表里，带名称、绝对路径与撤销/信任动作", async () => {
    await renderSection();

    const trustedRow = screen.getByTestId("workspace-ws-trusted");
    expect(trustedRow).toHaveTextContent(TRUSTED.name);
    expect(trustedRow).toHaveTextContent(TRUSTED.canonicalPath);
    expect(trustedRow).toHaveTextContent(t("settings.permissions.trusted"));
    // 已信任的行给“撤销”，未信任的行给“信任”：动作名带路径，两行不会混淆。
    expect(
      within(trustedRow).getByRole("button", {
        name: t("settings.permissions.untrustAria", { path: TRUSTED.canonicalPath }),
      })
    ).toBeInTheDocument();

    const freshRow = screen.getByTestId("workspace-ws-fresh");
    expect(freshRow).toHaveTextContent(UNTRUSTED.canonicalPath);
    expect(freshRow).toHaveTextContent(t("settings.permissions.untrusted"));
    expect(
      within(freshRow).getByRole("button", {
        name: t("settings.permissions.trustAria", { path: UNTRUSTED.canonicalPath }),
      })
    ).toBeInTheDocument();
  });

  it("信任时间只在界面真的观察到这次信任动作时显示，并写明口径", async () => {
    // 后端 Workspace 没有 trustedAt：界面只记自己看到的那次动作。
    const at = new Date("2026-11-08T10:30:00Z");
    recordWorkspaceTrust(TRUSTED.id, true, at);
    await renderSection();

    const row = screen.getByTestId("workspace-ws-trusted");
    expect(row).toHaveTextContent(t("settings.permissions.trustedAt", { time: formatDate(at) }));
    // 没有观察记录的（这里用一个同样已信任、但界面没见过的条目）如实说明。
    const other: Workspace = { ...TRUSTED, id: "ws-before", name: "旧项目", canonicalPath: "D:/work/old" };
    shellState.value = shell({ workspaces: [other] });
    render(<PermissionSettings />);
    const beforeRow = await screen.findByTestId("workspace-ws-before");
    expect(beforeRow).toHaveTextContent(t("settings.permissions.trustedAtUnknown"));
  });

  it("空态说“还没有工作区”，不是加载失败的样子", async () => {
    await renderSection({ workspaces: [], currentWorkspace: null });

    expect(screen.getByText(t("settings.permissions.workspacesEmptyTitle"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.permissions.workspacesEmptyHint"))).toBeInTheDocument();
    // 空 ≠ 失败：既不显示重试，也不显示错误。
    expect(screen.queryByRole("button", { name: t("common.retry") })).toBeNull();
    expect(screen.queryByText(t("errors.INTERNAL_ERROR"))).toBeNull();
  });
});

describe("设置 · 工作区：撤销语义", () => {
  it("撤销后状态翻转，并说明当前运行不受影响、下一条消息要重新信任", async () => {
    const user = userEvent.setup();
    // 后端事实：撤销之后这个工作区就是未信任。
    const trustWorkspace = vi.fn(async (id: string, trusted: boolean) => {
      shellState.value = shell({
        trustWorkspace,
        workspaces: [id === TRUSTED.id ? { ...TRUSTED, trusted } : UNTRUSTED],
        currentWorkspace: id === TRUSTED.id ? { ...TRUSTED, trusted } : UNTRUSTED,
      });
    });
    const view = await renderSection({ trustWorkspace });

    const row = screen.getByTestId("workspace-ws-trusted");
    expect(row).toHaveAttribute("data-workspace-trusted", "true");
    await user.click(
      within(row).getByRole("button", {
        name: t("settings.permissions.untrustAria", { path: TRUSTED.canonicalPath }),
      })
    );

    expect(trustWorkspace).toHaveBeenCalledTimes(1);
    expect(trustWorkspace).toHaveBeenCalledWith(TRUSTED.id, false);

    // 壳层重读之后列表按后端事实翻面。
    view.rerender(<PermissionSettings />);
    await waitFor(() =>
      expect(screen.getByTestId("workspace-ws-trusted")).toHaveAttribute("data-workspace-trusted", "false")
    );
    expect(screen.getByTestId("workspace-ws-trusted")).toHaveTextContent(
      t("settings.permissions.untrusted")
    );
    // 撤销的后果必须写清楚（否则“撤销了还在跑”会被当成 bug）。
    expect(screen.getByText(t("settings.permissions.revokedNote"))).toBeInTheDocument();
    expect(t("settings.permissions.revokedNote")).toContain("下一条消息");
  });

  it("撤销失败时不说“已撤销”：说明只在列表真的翻面后才出现", async () => {
    const user = userEvent.setup();
    // 后端拒绝（CAS 冲突 / 无权限）：列表事实仍然是“已信任”。
    const trustWorkspace = vi.fn(async (_id: string, _trusted: boolean) => undefined);
    const view = await renderSection({ trustWorkspace });

    await user.click(
      within(screen.getByTestId("workspace-ws-trusted")).getByRole("button", {
        name: t("settings.permissions.untrustAria", { path: TRUSTED.canonicalPath }),
      })
    );
    view.rerender(<PermissionSettings />);

    expect(screen.getByTestId("workspace-ws-trusted")).toHaveAttribute("data-workspace-trusted", "true");
    expect(screen.queryByText(t("settings.permissions.revokedNote"))).toBeNull();
  });
});

describe("设置 · 工作区：选目录不等于执行（WTRUST 第 ③ 条）", () => {
  it("未信任状态下添加目录成功，且不会顺手信任它", async () => {
    const user = userEvent.setup();
    const added: Workspace = {
      id: "ws-added",
      name: "added",
      canonicalPath: "D:/work/added",
      trusted: false,
      createdAt: "2026-11-08T00:00:00Z",
    };
    // 后端事实：添加只登记目录，Trusted 仍是 false（internal/workspace 的默认值）。
    const addWorkspace = vi.fn(async (_path: string) => {
      shellState.value = shell({
        addWorkspace,
        trustWorkspace: shellState.value.trustWorkspace,
        workspaces: [TRUSTED, UNTRUSTED, added],
        currentWorkspace: added,
      });
    });
    const view = await renderSection({ addWorkspace });

    const input = screen.getByLabelText(t("settings.permissions.workspacePath"));
    await user.type(input, added.canonicalPath);
    await user.click(screen.getByRole("button", { name: t("settings.permissions.add") }));

    expect(addWorkspace).toHaveBeenCalledTimes(1);
    expect(addWorkspace).toHaveBeenCalledWith(added.canonicalPath);
    // 关键断言：选目录不是一个授权动作。
    expect(shellState.value.trustWorkspace).not.toHaveBeenCalled();

    // 新目录按后端事实出现在列表里，且状态是“未信任”（没有偷偷变成已信任）。
    view.rerender(<PermissionSettings />);
    const row = await screen.findByTestId("workspace-ws-added");
    expect(row).toHaveAttribute("data-workspace-trusted", "false");
    expect(row).toHaveTextContent(added.canonicalPath);
  });

  it("添加失败时把结构化原因留在界面上，不假装添加成功", async () => {
    const user = userEvent.setup();
    const addWorkspace = vi.fn(async () => {
      throw { code: "NOT_FOUND", message: "目录不存在: D:/work/nope", retryable: false };
    });
    await renderSection({ addWorkspace });

    await user.type(screen.getByLabelText(t("settings.permissions.workspacePath")), "D:/work/nope");
    await user.click(screen.getByRole("button", { name: t("settings.permissions.add") }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveAttribute("data-workspace-add-error");
    expect(alert).toHaveTextContent(t("errors.NOT_FOUND"));
  });
});
