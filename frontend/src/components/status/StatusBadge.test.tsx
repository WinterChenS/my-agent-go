// StatusBadge 的测试（U00）。
//
// 这个组件的输入只有类型化领域状态，没有"刚点过按钮"这类事件信息，
// 所以它不可能把点击当成 connected —— 这里把这条约束钉在类型与测试上。

import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { StatusBadge, statusTone, type DomainStatus } from "./StatusBadge";
import { DEFAULT_LOCALE, setLocale } from "../../i18n";

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("StatusBadge", () => {
  it("labels every domain status from the catalog", () => {
    const statuses: DomainStatus[] = [
      "idle",
      "queued",
      "starting",
      "running",
      "waiting_approval",
      "stopping",
      "completed",
      "failed",
      "cancelled",
      "interrupted",
      "connected",
      "disconnected",
      "degraded",
      "unknown",
      "unavailable",
      "not_implemented",
    ];
    for (const status of statuses) {
      const { unmount } = render(<StatusBadge status={status} />);
      const el = screen.getByRole("status");
      expect(el.getAttribute("data-status")).toBe(status);
      expect(el.textContent ?? "").not.toBe("");
      expect(el.textContent ?? "").not.toContain("status.");
      unmount();
    }
  });

  it("maps tones without inventing success", () => {
    expect(statusTone("failed")).toBe("danger");
    expect(statusTone("waiting_approval")).toBe("warning");
    expect(statusTone("completed")).toBe("success");
    // 未知/不可用一律中性：不知道的事情不能显示成"一切正常"。
    expect(statusTone("unknown")).toBe("neutral");
    expect(statusTone("unavailable")).toBe("neutral");
    expect(statusTone("not_implemented")).toBe("neutral");
  });

  it("localizes labels", () => {
    setLocale("en-US");
    render(<StatusBadge status="running" />);
    expect(screen.getByRole("status")).toHaveTextContent("Running");
    setLocale("zh-CN");
    render(<StatusBadge status="running" />);
    expect(screen.getAllByRole("status")[1]).toHaveTextContent("运行中");
  });
});
