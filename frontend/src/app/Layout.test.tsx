// 壳层渲染测试（U01 / UI05）。
//
// 断言的是"人在界面上能不能到达"：三个主入口与工具分组都在、当前页只有一个、
// 工具分组能收起、搜索框与左栏历史都在 Tab 序列里，右栏开关是可识别的切换按钮。
// 不断言 class 名或 DOM 层级——那些是实现细节，改了不该让测试红。

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Layout } from "./Layout";
import { navGroups } from "./navigation";
import { t } from "../i18n";
import { readContract } from "../design/tokens.files.mjs";

type LayoutArgs = Parameters<typeof Layout>[0];

function renderLayout(overrides: Partial<LayoutArgs> = {}) {
  const onNavigate = vi.fn();
  const onToggleRight = vi.fn();
  const utils = render(
    <Layout
      status={null}
      current="workbench"
      onNavigate={onNavigate}
      search={<input aria-label={t("search.title")} />}
      history={<button>{t("shell.historyTitle")}</button>}
      footer={<span>{t("shell.piStatus")}</span>}
      right={<div>{t("common.details")}</div>}
      rightOpen={false}
      onToggleRight={onToggleRight}
      {...overrides}
    >
      {overrides.children ?? <button>{t("common.details")}</button>}
    </Layout>
  );
  return { ...utils, onNavigate, onToggleRight };
}

describe("Layout", () => {
  it("三个主入口与工具分组都在导航里", () => {
    renderLayout();
    for (const group of navGroups()) {
      for (const item of group.items) {
        expect(screen.getByRole("button", { name: t(item.labelKey) })).toBeInTheDocument();
      }
    }
  });

  it("只有当前页面被标记为当前位置", () => {
    renderLayout({ current: "settings" });
    const marked = screen
      .getAllByRole("button")
      .filter((el) => el.getAttribute("aria-current") === "page");
    expect(marked).toHaveLength(1);
    expect(marked[0]).toHaveTextContent(t("nav.settings"));
  });

  it("点击导航项会把目标路径交给上层", async () => {
    const user = userEvent.setup();
    const { onNavigate } = renderLayout();
    await user.click(screen.getByRole("button", { name: t("nav.connectors") }));
    expect(onNavigate).toHaveBeenCalledWith("/connectors");
  });

  it("工具分组可以收起，收起后入口不再可达", async () => {
    const user = userEvent.setup();
    renderLayout();
    const toggle = screen.getByRole("button", { name: t("nav.toolsLabel") });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: t("nav.schedules") })).toBeInTheDocument();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("button", { name: t("nav.schedules") })).toBeNull();

    await user.click(toggle);
    expect(screen.getByRole("button", { name: t("nav.schedules") })).toBeInTheDocument();
  });

  it("Tab 顺序覆盖搜索、导航、历史与主内容", async () => {
    const user = userEvent.setup();
    renderLayout({ history: <button>{t("shell.loadMore")}</button> });

    // 记录实际焦点序列再断言相对顺序：焦点能在 30 次 Tab 内走完整个壳层，
    // 且搜索在前、导航次之、历史再次、主内容最后——没有一段被导航吞掉。
    const focused: (Element | null)[] = [];
    for (let i = 0; i < 30; i++) {
      await user.tab();
      focused.push(document.activeElement);
    }
    const indexOf = (predicate: (el: Element) => boolean) =>
      focused.findIndex((el) => (el ? predicate(el) : false));

    const searchIdx = indexOf((el) => el.getAttribute("aria-label") === t("search.title"));
    const navIdx = indexOf((el) => el.textContent === t("nav.workbench"));
    const historyIdx = indexOf((el) => el.textContent === t("shell.loadMore"));
    const mainIdx = indexOf((el) => el.textContent === t("common.details"));

    expect(searchIdx).toBeGreaterThanOrEqual(0);
    expect(navIdx).toBeGreaterThan(searchIdx);
    expect(historyIdx).toBeGreaterThan(navIdx);
    expect(mainIdx).toBeGreaterThan(historyIdx);
  });

  it("右栏开关是可识别的切换按钮", async () => {
    const user = userEvent.setup();
    const { onToggleRight } = renderLayout();
    const toggle = screen.getByRole("button", { name: t("shell.showDetails") });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await user.click(toggle);
    expect(onToggleRight).toHaveBeenCalledTimes(1);
  });

  it("主内容区域有来自导航文案的可访问名称", () => {
    renderLayout({ current: "agents" });
    expect(screen.getByRole("main", { name: t("nav.agents") })).toBeInTheDocument();
  });
});

// D1：外壳的几何与结构来自原型（docs/ui/Agent-客户端产品原型 的 studio.css）。
//
// 这里的断言分两层：
//   1. DOM 层：品牌头、两组导航、历史容器、页脚容器、主内容、详情栏都在，且
//      详情栏随开关出现/消失（结构不能靠"看起来有"）；
//   2. 源码层：外壳的版式值确实来自令牌（`--anvil-sidebar-w` / `--anvil-topbar-h`），
//      而不是组件里另写一个 236px / 52px。jsdom 不算样式，所以读 CSS 文本是
//      唯一诚实的判据；真实像素由 D1 的 shell-diff 证据（1366×768 / 1440×900）给出。
describe("外壳版式（D1）", () => {
  const { indexCss } = readContract();

  it("侧栏宽度与顶栏高度来自原型令牌，且几何真的消费它们", () => {
    expect(indexCss).toContain("--anvil-sidebar-w: 236px;");
    expect(indexCss).toContain("--anvil-topbar-h: 52px;");
    expect(indexCss).toContain("grid-template-columns: var(--anvil-sidebar-w) minmax(0, 1fr);");
    expect(indexCss).toContain("height: var(--anvil-topbar-h);");
    expect(indexCss).toContain("padding: 0 20px;");
  });

  it("品牌头、两组导航、历史、页脚、主内容与详情栏都在结构里", () => {
    const { container } = renderLayout();
    expect(container.querySelector('[data-shell-region="sidebar"]')).not.toBeNull();
    expect(container.querySelector('[data-shell-region="topbar"]')).not.toBeNull();
    expect(container.querySelector('[data-shell-region="history"]')).not.toBeNull();
    expect(container.querySelector('[data-shell-region="footer"]')).not.toBeNull();
    expect(container.querySelector('[data-shell-region="page"]')).not.toBeNull();
    // 详情栏默认关闭：占位栏不能凭空出现。
    expect(container.querySelector('[data-shell-region="detail"]')).toBeNull();

    // 品牌头显示产品名与副标题，而不是把 logo 当装饰。
    const brand = screen.getByRole("button", { name: t("app.name") });
    expect(brand).toHaveTextContent(t("app.name"));
    expect(brand).toHaveTextContent(t("shell.brandSub"));

    // 两组导航：主入口组有标签，工具组是可展开的组标题。
    expect(screen.getByText(t("nav.startLabel"))).toBeInTheDocument();
    expect(screen.getByText(t("nav.toolsLabel"))).toBeInTheDocument();
  });

  it("详情栏打开时作为外壳的第三列出现", () => {
    const { container } = renderLayout({ rightOpen: true });
    expect(container.querySelector('[data-shell-region="detail"]')).not.toBeNull();
    expect(container.querySelector(".anvil-shell")).toHaveAttribute("data-detail", "open");
    expect(indexCss).toContain(
      "grid-template-columns: var(--anvil-sidebar-w) minmax(0, 1fr) var(--anvil-detail-w);"
    );
  });

  it("焦点环与卡片圆角走令牌，焦点环来自 accent", () => {
    expect(indexCss).toContain("outline: 2px solid var(--anvil-focus);");
    expect(indexCss).toContain("outline-offset: 2px;");
    expect(indexCss).toContain("--anvil-radius: var(--anvil-r-md);");
  });
});
