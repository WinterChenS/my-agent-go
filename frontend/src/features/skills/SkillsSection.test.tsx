// 技能区块的关键断言（C03 / RES02–RES03）。
//
// 重点不是"能不能渲染"，而是几条容易骗人的事：
//   - 依赖缺失要说清原因，而不是显示成"已启用"；
//   - 改动还没被运行中的 run 用上时，界面要明说；
//   - 导入空路径不该发出请求（否则会拿到一个看不懂的后端错误）。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SkillsSection } from "./SkillsSection";
import type { SkillResource } from "../../types";

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

const baseProps = {
  revision: 3,
  pending: false,
  activeRun: false,
  runtimeSkills: [] as string[],
  workspaceId: "ws-1",
  onChanged: () => {},
};

vi.mock("../../api", () => ({
  api: {
    importSkill: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
    setSkillEnabled: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
    deleteSkill: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
    readSkill: vi.fn(async () => ({ item: skill(), content: "---\nname: demo-skill\n---\n正文", error: null })),
    saveSkill: vi.fn(async () => ({ item: skill(), revision: 4, pending: false, error: null })),
  },
}));

describe("SkillsSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("没有技能时说清下一步，而不是空白", () => {
    render(<SkillsSection skills={[]} {...baseProps} />);
    expect(screen.getByText("还没有技能")).toBeInTheDocument();
    expect(screen.getByText(/frontmatter/)).toBeInTheDocument();
  });

  it("依赖缺失显示不可用与原因，而不是照常显示已启用", () => {
    render(
      <SkillsSection
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
    expect(screen.getByText("不可用")).toBeInTheDocument();
    expect(screen.getByText("缺少依赖命令: python3")).toBeInTheDocument();
  });

  it("禁用技能会调用后端并且带上当前 revision", async () => {
    const { api } = await import("../../api");
    render(<SkillsSection skills={[skill()]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: "禁用" }));
    await waitFor(() => expect(api.setSkillEnabled).toHaveBeenCalledWith("demo-skill", 3, false));
  });

  it("改动还没生效时明说，而不是让人以为已经加载", () => {
    render(
      <SkillsSection
        skills={[skill({ enabled: false })]}
        {...baseProps}
        pending={true}
        activeRun={true}
        runtimeSkills={["demo-skill"]}
      />
    );
    expect(screen.getByRole("status")).toHaveTextContent(/当前还有 run 在跑/);
    expect(screen.getByText("运行中仍是旧集合")).toBeInTheDocument();
  });

  it("导入空路径只提示，不发请求", async () => {
    const { api } = await import("../../api");
    render(<SkillsSection skills={[]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: /导入/ }));
    await waitFor(() => expect(screen.getByText(/请填写本地技能目录/)).toBeInTheDocument());
    expect(api.importSkill).not.toHaveBeenCalled();
  });

  it("编辑内容来自磁盘快照，保存时带上 revision 与全文", async () => {
    const { api } = await import("../../api");
    render(<SkillsSection skills={[skill()]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: "编辑" }));
    const box = await screen.findByLabelText("SKILL.md 内容");
    expect(box).toHaveValue("---\nname: demo-skill\n---\n正文");

    await userEvent.type(box, "\n补充");
    await userEvent.click(screen.getByRole("button", { name: "保存" }));
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
    render(<SkillsSection skills={[skill()]} {...baseProps} />);
    await userEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(api.deleteSkill).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "确认删除" }));
    await waitFor(() => expect(api.deleteSkill).toHaveBeenCalledWith(3, "demo-skill"));
  });
});
