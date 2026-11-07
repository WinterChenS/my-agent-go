// AsyncState 的渲染测试（U00）。
//
// 最关键的一条是"加载中不显示旧内容"：
// 刷新时把上一次的数据留在屏幕上，用户会对着过期数据操作。

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { AsyncState } from "./AsyncState";
import { DEFAULT_LOCALE, setLocale } from "../../i18n";

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("AsyncState", () => {
  it("renders loading and never shows stale children", () => {
    render(
      <AsyncState phase="loading" loadingLabel="正在读取会话…">
        <p>上一次的内容</p>
      </AsyncState>
    );

    expect(screen.getByRole("status")).toHaveTextContent("正在读取会话…");
    expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite");
    // 旧内容不能进 DOM：留着它等于让用户以为数据已经刷新过了。
    expect(screen.queryByText("上一次的内容")).toBeNull();
  });

  it("renders success children without inventing a status", () => {
    render(
      <AsyncState phase="success">
        <p>真实数据</p>
      </AsyncState>
    );
    expect(screen.getByText("真实数据")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("distinguishes empty from loading", () => {
    render(<AsyncState phase="empty" emptyTitle="还没有会话" emptyHint="先发起一次对话" />);
    expect(screen.getByText("还没有会话")).toBeInTheDocument();
    expect(screen.getByText("先发起一次对话")).toBeInTheDocument();
    // 空态不是"正在加载"，两者语义必须可分辨。
    expect(screen.queryByRole("status")).toBeNull();
    expect(document.querySelector('[data-phase="empty"]')).not.toBeNull();
  });

  it("shows code plus localized text for errors, and only wires retry when given", () => {
    const onRetry = vi.fn();
    const { rerender } = render(
      <AsyncState
        phase="error"
        error={{ code: "INVALID_INPUT", message: "Go 侧的中文原文", params: { field: "locale", value: "fr-FR", supported: "zh-CN, en-US" } }}
        onRetry={onRetry}
      />
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("INVALID_INPUT");
    expect(alert).toHaveTextContent("fr-FR");
    screen.getByRole("button", { name: "重试" }).click();
    expect(onRetry).toHaveBeenCalledTimes(1);

    rerender(<AsyncState phase="error" error={{ code: "INVALID_INPUT" }} />);
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("does not leak the Go message in en-US", () => {
    setLocale("en-US");
    render(<AsyncState phase="error" error={{ code: "NOT_FOUND", message: "找不到目标" }} />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("NOT_FOUND");
    expect(/[\u3400-\u9fff]/.test(alert.textContent ?? "")).toBe(false);
  });

  it("separates blocked and permission-denied from failure", () => {
    const { rerender } = render(<AsyncState phase="blocked" reason="工作区尚未信任" />);
    expect(screen.getByRole("alert")).toHaveTextContent("工作区尚未信任");
    expect(document.querySelector('[data-phase="blocked"]')).not.toBeNull();

    rerender(<AsyncState phase="permission-denied" reason="需要先在设置里填入凭证" />);
    expect(screen.getByRole("alert")).toHaveTextContent("需要先在设置里填入凭证");
    expect(document.querySelector('[data-phase="permission-denied"]')).not.toBeNull();
    // 阻塞与失败必须是两种呈现：前者要用户先做一步，后者是这次操作错了。
    expect(document.querySelector('[data-phase="error"]')).toBeNull();
  });

  it("falls back to a generic message for unknown codes", () => {
    render(<AsyncState phase="error" error={{ code: "BRAND_NEW_CODE" }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("BRAND_NEW_CODE");
  });
});
