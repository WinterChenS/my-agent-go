// 共享 not_implemented 页面的测试（U01 / UI05）。
//
// 这条断言是规格的直接要求（§1、§5.6、§5.7）：C08/C09 合入前，连接器与定时任务
// 页面只能显示真实状态，**不得出现任何会伪造成功的动作**。原型里的
// "连接"按钮、"立即运行"按钮、"connected"徽标都会让用户以为功能已经可用。
//
// 所以这里不只断言"有未实现文案"，还断言页面上一个按钮都没有。

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { NotImplementedPage } from "./index";
import { t } from "../../i18n";

describe("NotImplementedPage", () => {
  it.each(["connectors", "schedules"] as const)("%s 显示共享的未实现状态", (feature) => {
    render(<NotImplementedPage feature={feature} />);
    expect(screen.getByText(t(`nav.${feature}`))).toBeInTheDocument();
    // 徽标必须是类型化领域状态 not_implemented，而不是从点击推出的"已连接"。
    const badge = document.querySelector('[data-status="not_implemented"]');
    expect(badge).not.toBeNull();
    expect(badge).toHaveTextContent(t("status.not_implemented"));
    expect(screen.getByText(t(`notImplemented.${feature}`))).toBeInTheDocument();
    expect(screen.getByText(t("notImplemented.hint"))).toBeInTheDocument();
  });

  it.each(["connectors", "schedules"] as const)("%s 没有任何动作按钮", (feature) => {
    render(<NotImplementedPage feature={feature} />);
    // 一个按钮都不允许：没有"连接"、没有"立即运行"、没有重试。
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("不出现任何连接成功或计数类的文案", () => {
    render(<NotImplementedPage feature="connectors" />);
    // connected 徽标与"已连接"文案都是假状态的典型表现。
    expect(screen.queryByText(t("status.connected"))).toBeNull();
    expect(screen.queryByText(t("status.running"))).toBeNull();
  });
});
