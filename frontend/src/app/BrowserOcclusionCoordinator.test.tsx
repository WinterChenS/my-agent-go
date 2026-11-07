// 遮挡协调器（B07 / UI04）测试。
//
// 原生网页是盖在右栏 slot 上的子窗口：任何应用浮层（设置 dialog、命令面板、
// 审批框）都要先让原生视图让位，否则浮层会被网页盖住，而"看起来能用"的界面里
// 用户根本点不到按钮。
//
// 三条纪律：
//   1. 每个浮层用**有名字**的来源登记；关闭后必须恢复（不能永久隐藏）；
//   2. 同一来源可能有多个实例（例如两个确认框），引用计数归零才恢复；
//   3. 协调器只调 setOverlay。它**不**接管控制权、不停止 run：
//      隐藏面板 ≠ 暂停 agent。

import { useCallback, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  BrowserOcclusionLayer,
  BrowserOcclusionProvider,
  useBrowserOcclusionLayer,
} from "./BrowserOcclusionCoordinator";

function fakeOcclusionApi() {
  const calls: Array<[string, boolean]> = [];
  const api = {
    setOverlay: vi.fn(async (source: string, open: boolean) => {
      calls.push([source, open]);
      return null;
    }),
  };
  return { api, calls };
}

describe("BrowserOcclusionCoordinator", () => {
  it("设置 dialog → 命令面板 → 审批框依次弹出时，网页隐藏与恢复各一次", async () => {
    const { api, calls } = fakeOcclusionApi();
    function Dialogs() {
      const settings = useDialogState();
      const palette = useDialogState();
      const approval = useDialogState();
      return (
        <div>
          <BrowserOcclusionLayer source="settings-dialog" open={settings.open}>
            <button onClick={settings.close}>settings open</button>
          </BrowserOcclusionLayer>
          <BrowserOcclusionLayer source="command-palette" open={palette.open}>
            <button onClick={palette.close}>palette open</button>
          </BrowserOcclusionLayer>
          <BrowserOcclusionLayer source="approval-dialog" open={approval.open}>
            <button onClick={approval.close}>approval open</button>
          </BrowserOcclusionLayer>
          <button onClick={settings.show}>open settings</button>
          <button onClick={palette.show}>open palette</button>
          <button onClick={approval.show}>open approval</button>
        </div>
      );
    }

    render(
      <BrowserOcclusionProvider api={api}>
        <Dialogs />
      </BrowserOcclusionProvider>
    );

    for (const name of ["settings", "palette", "approval"]) {
      await userEvent.click(screen.getByRole("button", { name: `open ${name}` }));
      await userEvent.click(screen.getByRole("button", { name: `${name} open` }));
    }

    expect(calls).toEqual([
      ["settings-dialog", true],
      ["settings-dialog", false],
      ["command-palette", true],
      ["command-palette", false],
      ["approval-dialog", true],
      ["approval-dialog", false],
    ]);
  });

  it("同一来源的两个实例：都关闭才恢复（引用计数）", async () => {
    const { api, calls } = fakeOcclusionApi();
    function Twin({ open }: { open: boolean }) {
      useBrowserOcclusionLayer("approval-dialog", open);
      return null;
    }
    const { rerender } = render(
      <BrowserOcclusionProvider api={api}>
        <Twin open />
        <Twin open />
      </BrowserOcclusionProvider>
    );
    expect(calls).toEqual([["approval-dialog", true]]);

    rerender(
      <BrowserOcclusionProvider api={api}>
        <Twin open />
        <Twin open={false} />
      </BrowserOcclusionProvider>
    );
    expect(calls).toEqual([["approval-dialog", true]]);

    rerender(
      <BrowserOcclusionProvider api={api}>
        <Twin open={false} />
        <Twin open={false} />
      </BrowserOcclusionProvider>
    );
    expect(calls).toEqual([
      ["approval-dialog", true],
      ["approval-dialog", false],
    ]);
  });

  it("协调器只报告遮挡，不接管控制权、不停止 run", async () => {
    const { api } = fakeOcclusionApi();
    function Dialog() {
      useBrowserOcclusionLayer("settings-dialog", true);
      return null;
    }
    render(
      <BrowserOcclusionProvider api={api}>
        <Dialog />
      </BrowserOcclusionProvider>
    );
    // 只有 setOverlay 被调用；没有任何 takeOver/resume/close/abort 通道。
    expect(Object.keys(api)).toEqual(["setOverlay"]);
    expect(api.setOverlay).toHaveBeenCalledTimes(1);
  });
});

// useDialogState 是一个最小的受控开关（测试里代表"用户打开/关闭浮层"）。

function useDialogState() {
  const [open, setOpen] = useState(false);
  const show = useCallback(() => setOpen(true), []);
  const close = useCallback(() => setOpen(false), []);
  return { open, show, close };
}
