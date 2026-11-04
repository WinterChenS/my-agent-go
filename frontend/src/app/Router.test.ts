// 导航逻辑的纯函数测试。
//
// 导航看起来"不可能出错"，但它决定了用户能不能到达设置页。
// 而设置页是修复"工作区未信任"的唯一入口——导航坏了，
// 整个应用就锁死了，这类错误必须能在不渲染 UI 的情况下被捕获。

import { describe, expect, it } from "vitest";
import {
  VIEWS,
  initialRouterState,
  isViewId,
  navReducer,
  nextView,
} from "./Router";

describe("navReducer", () => {
  it("默认停在工作台", () => {
    expect(initialRouterState.view).toBe("workbench");
  });

  it("切视图会清掉上一个视图的二级选择", () => {
    let s = navReducer(initialRouterState, { kind: "go", view: "runs", selection: "run-1" });
    expect(s).toEqual({ view: "runs", selection: "run-1" });

    // 切到别的视图却没指定选择：不能把 run-1 带过去。
    s = navReducer(s, { kind: "go", view: "settings" });
    expect(s.view).toBe("settings");
    expect(s.selection).toBeUndefined();
  });

  it("切到同一个视图不会丢掉已有选择", () => {
    let s = navReducer(initialRouterState, { kind: "go", view: "runs", selection: "run-1" });
    s = navReducer(s, { kind: "go", view: "runs" });
    expect(s.selection).toBe("run-1");
  });

  it("select 只改选择，不改视图", () => {
    const s = navReducer({ view: "runs" }, { kind: "select", selection: "run-9" });
    expect(s).toEqual({ view: "runs", selection: "run-9" });
  });
});

describe("nextView", () => {
  it("左右循环移动", () => {
    expect(nextView("workbench", 1)).toBe("resources");
    expect(nextView("resources", -1)).toBe("workbench");
    // 首尾相接，不能卡在两端。
    expect(nextView(VIEWS[VIEWS.length - 1].id, 1)).toBe(VIEWS[0].id);
    expect(nextView(VIEWS[0].id, -1)).toBe(VIEWS[VIEWS.length - 1].id);
  });

  it("五入口都在，且 id 唯一", () => {
    expect(VIEWS).toHaveLength(5);
    const ids = new Set(VIEWS.map((v) => v.id));
    expect(ids.size).toBe(5);
  });

  it("每个入口都有说明文案，空文案会让悬停提示变成空框", () => {
    for (const v of VIEWS) {
      expect(v.label.trim()).not.toBe("");
      expect(v.hint.trim()).not.toBe("");
    }
  });
});

describe("isViewId", () => {
  it("只接受五个已知入口", () => {
    expect(isViewId("workbench")).toBe(true);
    expect(isViewId("settings")).toBe(true);
    // 从绑定层/URL 传来的任意字符串不能当视图用。
    expect(isViewId("browser")).toBe(false);
    expect(isViewId("")).toBe(false);
  });
});
