// 技能页面（U04 / UI08、RES05）：托管技能 + 扩展。
//
// 重点不是"能不能渲染"，而是几条容易骗人的事：
//   - 依赖缺失要说清原因，而不是显示成"已启用"；
//   - 改动还没被运行中的 run 用上时，界面要明说；
//   - 导入空路径不该发出请求（否则会拿到一个看不懂的后端错误）；
//   - 系统策略扩展不提供关闭入口，第三方扩展启用要二次确认。
//
// 断言用 `t()` 取 catalog 文案：文案在两种语言之间搬动时，这些用例仍然成立，
// 而"界面直接漏出中文"由 scripts/check-ui-contract.mjs 与 I18N04 负责。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SkillsPage } from "./SkillsPage";
import { t } from "../../i18n";
import type { Extension, SkillResource } from "../../types";

function skill(over: Partial<SkillResource> = {}): SkillResource {
  return {
    id: "demo-skill",
    name: "demo-skill",
    description: "演示技能",
    path: "C:\\app\\skills\\demo-skill\\SKILL.md",
    scope: "user",
    enabled: true,
    origin: "imported",
    revision: 3,
    hash: "abc123",
    dependencies: null,
    missingDependencies: null,
    available: true,
    disableModelInvocation: false,
    bytes: 2048,
    ...over,
  };
}

function ext(over: Partial<Extension> = {}): Extension {
  return {
    id: "harness-policy",
    name: "策略扩展（执行前授权）",
    path: "C:\\app\\pi-extensions\\harness-policy.ts",
    system: true,
    executable: true,
    enabled: true,
    origin: "system",
    canDisable: false,
    note: "系统资源，不可关闭。",
    revision: 2,
    hash: "ff00",
    ...over,
  };
}

const baseProps = {
  revision: 3,
  pending: false,
  activeRun: false,
  runtimeSkills: [] as string[],
  workspaceId: "ws-1",
  onChanged: () => {},
  extensions: [] as Extension[],
  extensionRevision: 0,
};

vi.mock("../../api", () => ({
  api: {
    importSkill: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
    setSkillEnabled: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
    deleteSkill: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
    readSkill: vi.fn(async () => ({ item: skill(), content: "---\nname: demo-skill\n---\n正文", error: null })),
    saveSkill: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
    setExtensionEnabled: vi.fn(async () => ({ item: ext(), revision: 3, error: null })),
  },
}));

describe("SkillsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("没有技能时说清下一步，而不是空白", () => {
    render(<SkillsPage skills={[]} {...baseProps} />);
    expect(screen.getByText(t("skills.empty.title"))).toBeInTheDocument();
    expect(screen.getByText(t("skills.empty.hint"))).toBeInTheDocument();
  });

  it("依赖缺失显示不可用与原因，而不是照常显示已启用", () => {
    render(
      <SkillsPage
        skills={[
          skill({
            available: false,
            missingDependencies: ["python3"],
            unavailableReason: "缺少依赖命令: python3",
          }),
        ]}
        {...baseProps}
      />
    );
    expect(screen.getByText(t("status.unavailable"))).toBeInTheDocument();
    expect(screen.getByText("缺少依赖命令: python3")).toBeInTheDocument();
  });

  it("禁用技能会调用后端并且带上当前 revision", async () => {
    const { api } = await import("../../api");
    render(<SkillsPage skills={[skill()]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: t("skills.actions.disable") }));
    await waitFor(() => expect(api.setSkillEnabled).toHaveBeenCalledWith("demo-skill", 3, false));
  });

  it("改动还没生效时明说，而不是让人以为已经加载", () => {
    render(
      <SkillsPage
        skills={[skill({ enabled: false })]}
        {...baseProps}
        pending={true}
        activeRun={true}
        runtimeSkills={["demo-skill"]}
      />
    );
    expect(screen.getByRole("status")).toHaveTextContent(t("skills.pending.activeRun"));
    expect(screen.getByText(t("skills.state.staleInRun"))).toBeInTheDocument();
  });

  it("导入空路径只提示，不发请求", async () => {
    const { api } = await import("../../api");
    render(<SkillsPage skills={[]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: t("skills.import.submit") }));
    await waitFor(() => expect(screen.getByText(t("skills.import.pathRequired"))).toBeInTheDocument());
    expect(api.importSkill).not.toHaveBeenCalled();
  });

  it("导入路径会连同作用域与工作区一起交给后端", async () => {
    const { api } = await import("../../api");
    render(<SkillsPage skills={[]} {...baseProps} />);
    await userEvent.type(screen.getByLabelText(t("skills.import.pathAria")), "D:\\skills\\x");
    await userEvent.click(screen.getByRole("button", { name: t("skills.import.submit") }));
    await waitFor(() =>
      expect(api.importSkill).toHaveBeenCalledWith("D:\\skills\\x", "user", "ws-1")
    );
    expect(
      await screen.findByText(t("skills.import.done", { name: "demo-skill" }))
    ).toBeInTheDocument();
  });

  it("编辑内容来自磁盘快照，保存时带上 revision 与全文", async () => {
    const { api } = await import("../../api");
    render(<SkillsPage skills={[skill()]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: t("skills.actions.edit") }));
    const box = await screen.findByLabelText(t("skills.editor.contentAria"));
    expect(box).toHaveValue("---\nname: demo-skill\n---\n正文");

    await userEvent.type(box, "\n补充");
    await userEvent.click(screen.getByRole("button", { name: t("common.save") }));
    await waitFor(() =>
      expect(api.saveSkill).toHaveBeenCalledWith(
        "demo-skill",
        3,
        "---\nname: demo-skill\n---\n正文\n补充"
      )
    );
  });

  it("删除要二次确认", async () => {
    const { api } = await import("../../api");
    render(<SkillsPage skills={[skill()]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: t("skills.actions.delete") }));
    expect(api.deleteSkill).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: t("skills.actions.deleteConfirm") }));
    await waitFor(() => expect(api.deleteSkill).toHaveBeenCalledWith(3, "demo-skill"));
  });
});

describe("SkillsPage 的扩展区块", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("系统策略扩展不提供关闭入口", () => {
    render(<SkillsPage skills={[]} {...baseProps} extensions={[ext()]} extensionRevision={2} />);
    expect(screen.getByText(t("extensions.systemBadge"))).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: t("skills.actions.disable") })
    ).not.toBeInTheDocument();
  });

  it("第三方扩展默认显示未启用，且说明启用不等于免审批", () => {
    render(
      <SkillsPage
        skills={[]}
        {...baseProps}
        extensions={[
          ext({
            id: "hello",
            name: "hello",
            path: "C:\\app\\extensions\\hello.ts",
            system: false,
            enabled: false,
            origin: "local",
            canDisable: true,
            note: "第三方可执行扩展：启用即信任，但不会让它的工具调用免审批。",
          }),
        ]}
        extensionRevision={2}
      />
    );
    expect(screen.getByText(t("extensions.disabledBadge"))).toBeInTheDocument();
    expect(
      screen.getByText("第三方可执行扩展：启用即信任，但不会让它的工具调用免审批。")
    ).toBeInTheDocument();
  });

  it("启用第三方扩展需要二次确认，确认后才调用后端", async () => {
    const { api } = await import("../../api");
    render(
      <SkillsPage
        skills={[]}
        {...baseProps}
        extensions={[
          ext({
            id: "hello",
            name: "hello",
            system: false,
            enabled: false,
            origin: "local",
            canDisable: true,
          }),
        ]}
        extensionRevision={2}
      />
    );
    await userEvent.click(screen.getByRole("button", { name: t("skills.actions.enable") }));
    expect(api.setExtensionEnabled).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: t("extensions.confirmEnable") }));
    await waitFor(() => expect(api.setExtensionEnabled).toHaveBeenCalledWith("hello", 2, true));
  });

  it("没有扩展时说明空列表的含义，而不是让人以为功能不存在", () => {
    render(<SkillsPage skills={[]} {...baseProps} extensions={[]} />);
    expect(screen.getByText(t("extensions.empty"))).toBeInTheDocument();
  });
});
