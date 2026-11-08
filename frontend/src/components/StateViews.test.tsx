// 四类状态的渲染测试。
//
// 要守住的核心是"可分辨"：加载中、没有数据、出错、没权限，
// 如果视觉与可访问性语义混在一起，用户会把失败当成空列表，
// 然后在错误的地方找问题。

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { EmptyState, ErrorState, LoadingState, NoPermissionState } from "./StateViews";
import { t } from "../i18n";

describe("LoadingState", () => {
  it("有 role=status 与 aria-live，读屏能感知加载", () => {
    render(<LoadingState label="正在读取工作区…" />);
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("正在读取工作区…");
    expect(status).toHaveAttribute("aria-live", "polite");
  });
});

describe("EmptyState", () => {
  it("只显示标题与说明，不冒充加载中", () => {
    render(<EmptyState title="还没有配置档" hint="先添加一个模型" />);
    expect(screen.getByText("还没有配置档")).toBeInTheDocument();
    expect(screen.getByText("先添加一个模型")).toBeInTheDocument();
    // 关键的负例：空态不该同时给出"加载中"的语义。
    expect(screen.queryByRole("status")).toBeNull();
  });
});

describe("ErrorState", () => {
  it("同时给出 code 与 message", () => {
    render(<ErrorState error={{ code: "CONFIG_CONFLICT", message: "revision 冲突" }} />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("CONFIG_CONFLICT");
    expect(alert).toHaveTextContent("revision 冲突");
  });

  it("有重试回调时才出现重试按钮", () => {
    const onRetry = vi.fn();
    const { rerender } = render(
      <ErrorState error={{ code: "INTERNAL_ERROR", message: "x" }} onRetry={onRetry} />
    );
    screen.getByRole("button", { name: t("common.retry") }).click();
    expect(onRetry).toHaveBeenCalledTimes(1);

    rerender(<ErrorState error={{ code: "INTERNAL_ERROR", message: "x" }} />);
    expect(screen.queryByRole("button", { name: t("common.retry") })).toBeNull();
  });

  it("message 不会被截断成只剩 code", () => {
    // 只显示 code 等于把原因藏起来，这是最常见的"看起来专业但没法排查"。
    const long = "引用的模型 m-gpt 不存在；请先在资源库中定义它";
    render(<ErrorState error={{ code: "DEPENDENCY_MISSING", message: long }} />);
    expect(screen.getByText(long)).toBeInTheDocument();
  });
});

describe("NoPermissionState", () => {
  it("与错误态语义不同：用 alert 但不标失败，且带动作", () => {
    render(
      <NoPermissionState
        message="工作区尚未信任"
        action={<button>去设置</button>}
      />
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("需要先完成一步");
    expect(alert).toHaveTextContent("工作区尚未信任");
    expect(screen.getByRole("button", { name: "去设置" })).toBeInTheDocument();
  });
});
