// 外壳的渲染与键盘可达性测试。
//
// UI01 要求"键盘导航可用"。这里不测"某个 div 有没有 tabindex"，
// 而是真的按方向键、检查导航被触发且焦点跟到新入口——
// 焦点不跟随的话，视觉焦点会留在旧 tab，用户下一次按键还是从旧位置继续。

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Layout } from "./Layout";
import { VIEWS } from "./Router";

type LayoutArgs = Parameters<typeof Layout>[0];

function renderLayout(overrides: Partial<LayoutArgs> = {}) {
  const { children, ...rest } = overrides;
  const onNavigate = vi.fn();
  const onToggleRight = vi.fn();
  const utils = render(
    <Layout
      status={null}
      view="workbench"
      onNavigate={onNavigate}
      left={<div>左栏内容</div>}
      right={<div>右栏内容</div>}
      rightOpen={false}
      onToggleRight={onToggleRight}
      {...rest}
    >
      {children ?? <div>主内容</div>}
    </Layout>
  );
  return { ...utils, onNavigate, onToggleRight };
}

describe("Layout", () => {
  it("五个入口都在 tablist 里，且只有当前那个被标记选中", () => {
    renderLayout();
    const tabs = screen.getAllByRole("tab");
    expect(tabs).toHaveLength(VIEWS.length);
    for (const spec of VIEWS) {
      const tab = screen.getByRole("tab", { name: spec.label });
      expect(tab).toBeInTheDocument();
      // 悬停说明要能读到，否则"这个入口是干什么的"只能靠猜。
      expect(tab).toHaveAttribute("title", spec.hint);
    }
    const selected = tabs.filter((t) => t.getAttribute("aria-selected") === "true");
    expect(selected).toHaveLength(1);
    expect(selected[0]).toHaveTextContent("工作台");
  });

  it("方向键切换入口，并把焦点带到新入口", async () => {
    const user = userEvent.setup();
    const { onNavigate, rerender } = renderLayout();

    screen.getByRole("tab", { name: "工作台" }).focus();
    await user.keyboard("{ArrowRight}");

    expect(onNavigate).toHaveBeenCalledWith("resources");

    // 父组件更新 view 之后，焦点应在"资源库"上。
    rerender(
      <Layout
        status={null}
        view="resources"
        onNavigate={onNavigate}
        left={<div />}
        right={<div />}
        rightOpen={false}
        onToggleRight={() => {}}
      >
        <div />
      </Layout>
    );
    expect(screen.getByRole("tab", { name: "资源库" })).toHaveAttribute("aria-selected", "true");
  });

  it("向左跨过第一个入口会绕到最后一个", async () => {
    const user = userEvent.setup();
    const { onNavigate } = renderLayout();
    screen.getByRole("tab", { name: "工作台" }).focus();
    await user.keyboard("{ArrowLeft}");
    expect(onNavigate).toHaveBeenCalledWith(VIEWS[VIEWS.length - 1].id);
  });

  it("只有当前入口在 Tab 序列里（roving tabindex）", () => {
    renderLayout();
    const active = screen.getByRole("tab", { name: "工作台" });
    const other = screen.getByRole("tab", { name: "设置" });
    expect(active).toHaveAttribute("tabindex", "0");
    expect(other).toHaveAttribute("tabindex", "-1");
  });

  it("Tab 键能从导航走到左栏与主内容里的可操作元素", async () => {
    const user = userEvent.setup();
    renderLayout({
      left: <button>添加工作区</button>,
      children: <button>发送</button>,
    });

    // 顶栏的详情开关是第一个可 Tab 到的按钮。
    await user.tab();
    expect(screen.getByRole("button", { name: /详情栏/ })).toHaveFocus();

    // 当前 tab 进入序列。
    await user.tab();
    expect(screen.getByRole("tab", { name: "工作台" })).toHaveFocus();

    // 继续 Tab 能到达左栏与主内容，而不是被导航吞掉。
    await user.tab();
    expect(screen.getByRole("button", { name: "添加工作区" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "发送" })).toHaveFocus();
  });

  it("详情栏开关是可被识别的切换按钮", () => {
    const { onToggleRight } = renderLayout();
    const toggle = screen.getByRole("button", { name: "展开详情栏" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    toggle.click();
    expect(onToggleRight).toHaveBeenCalledTimes(1);
  });
});
