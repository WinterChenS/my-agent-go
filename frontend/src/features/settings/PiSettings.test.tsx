// Pi 设置分区的候选展示（UX1）。
//
// 对话页把"还没有可用的 Pi"指向这里，所以这一屏必须做到两件事：
//   - 把 DiscoverPi 的真实候选（路径/版本/来源/探测状态）逐条摆出来，并标出推荐项；
//   - **不**替用户选。检测到 ≠ 已选用：外部二进制要由用户点「选用」确认。
//
// 这条纪律的理由很直接：静默选中一个 PATH 上的外部 Pi，等于让用户在某次运行里
// 执行了一个他没确认过的二进制；那一步必须是显式动作。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { setLocale, t } from "../../i18n";

const fake = vi.hoisted(() => ({
  discoverPi: null as unknown as (...a: any[]) => Promise<any>,
  discoverPiFast: null as unknown as (...a: any[]) => Promise<any>,
  selectPi: null as unknown as (...a: any[]) => Promise<any>,
  installPi: vi.fn(),
  cancelPiInstall: vi.fn(),
  rollbackPi: vi.fn(),
}));

vi.mock("./api", () => ({
  settingsApi: fake,
  onPiInstallProgress: () => () => undefined,
}));

import { PiSettings } from "./PiSettings";

function candidate(overrides: Record<string, any> = {}) {
  return {
    installation: {
      id: "managed:1.5.0",
      executable: "D:/anvil/pi/1.5.0/pi.exe",
      origin: "managed",
      architecture: "x86_64",
      source: "managed",
      runnable: true,
      version: "1.5.0",
    },
    origin: "managed",
    distribution: "standalone",
    executableHash: "a".repeat(64),
    launcherPath: "D:/anvil/pi/1.5.0/pi.exe",
    runtimeExecutable: "D:/anvil/pi/1.5.0/pi.exe",
    runtimeHash: "b".repeat(64),
    compatibility: "ready_compatible",
    diagnostics: [],
    ...overrides,
  };
}

beforeEach(() => {
  // 挂载用快速发现（FIX1-B）：只探版本与指纹，verification=version_only。
  fake.discoverPiFast = vi.fn(async () => ({
    state: "ready_compatible",
    selected: null,
    candidates: [
      candidate({ contractVerified: false }),
      candidate({
        installation: {
          id: "path:1.4.0",
          executable: "C:/tools/pi/pi.exe",
          origin: "external",
          architecture: "x86_64",
          runnable: true,
          version: "1.4.0",
        },
        origin: "path",
        launcherPath: "C:/tools/pi/pi.exe",
        runtimeExecutable: "C:/tools/pi/pi.exe",
        compatibility: "upgrade_available",
        contractVerified: false,
      }),
    ],
    diagnostics: [],
    verification: "version_only",
  }));
  // 完整发现：显式“验证契约”才走这条，契约探测通过。
  fake.discoverPi = vi.fn(async () => ({
    state: "ready_compatible",
    selected: null,
    candidates: [
      candidate(),
      candidate({
        installation: {
          id: "path:1.4.0",
          executable: "C:/tools/pi/pi.exe",
          origin: "external",
          architecture: "x86_64",
          runnable: true,
          version: "1.4.0",
        },
        origin: "path",
        launcherPath: "C:/tools/pi/pi.exe",
        runtimeExecutable: "C:/tools/pi/pi.exe",
        compatibility: "upgrade_available",
      }),
    ],
    diagnostics: [],
    verification: "contract_probed",
  }));
  fake.selectPi = vi.fn(async () => ({
    state: "ready_compatible",
    selected: candidate(),
    candidates: [candidate()],
    diagnostics: [],
  }));
});

afterEach(() => {
  setLocale("zh-CN");
});

describe("设置 · Pi 候选", () => {
  it("列出候选的路径/版本/来源/探测状态，并标出推荐项但不自动选用", async () => {
    render(<PiSettings />);

    const list = await screen.findByTestId("pi-candidates");
    const rows = within(list).getAllByTestId(/^pi-candidate-/);
    expect(rows).toHaveLength(2);

    const ready = await within(list).findByTestId("pi-candidate-managed:1.5.0");
    expect(ready.textContent).toContain("D:/anvil/pi/1.5.0/pi.exe");
    expect(ready.textContent).toContain("1.5.0");
    expect(ready.textContent).toContain(t("settings.pi.origin.managed"));
    expect(within(ready).getByTestId("pi-state").textContent).toBe(t("settings.pi.states.ready_compatible"));

    const old = await within(list).findByTestId("pi-candidate-path:1.4.0");
    expect(within(old).getByTestId("pi-state").textContent).toBe(t("settings.pi.states.upgrade_available"));

    // 推荐项只是标记，不是"已选用"。
    expect(within(ready).queryByTestId("pi-recommended")).not.toBeNull();
    expect(within(old).queryByTestId("pi-recommended")).toBeNull();
    expect(within(ready).queryByTestId("pi-selected")).toBeNull();
    expect(screen.getByText(t("settings.pi.confirmHint"))).toBeInTheDocument();

    // 挂载后不替用户做选择：没有调用，就没有静默选中。
    expect(fake.selectPi).not.toHaveBeenCalled();
  });

  it("用户点「选用」才真的选择那个候选", async () => {
    const user = userEvent.setup();
    render(<PiSettings />);

    const ready = await screen.findByTestId("pi-candidate-managed:1.5.0");
    await user.click(within(ready).getByRole("button", { name: t("settings.pi.select") }));

    await waitFor(() => expect(fake.selectPi).toHaveBeenCalledWith("managed:1.5.0"));
  });

  it("挂载走快速发现：不跑完整契约探测，并在界面上标明契约未验证", async () => {
    render(<PiSettings />);

    await screen.findByTestId("pi-candidates");
    // 挂载只允许调用快速发现；完整发现（会 spawn 真实 Pi）不许被挂载触发。
    expect(fake.discoverPiFast).toHaveBeenCalledTimes(1);
    expect(fake.discoverPi).not.toHaveBeenCalled();

    expect(screen.getByTestId("pi-fast-hint").textContent).toBe(t("settings.pi.fastHint"));
    const contract = within(screen.getByTestId("pi-candidate-managed:1.5.0")).getByTestId("pi-contract");
    expect(contract.getAttribute("data-verified")).toBe("false");
    expect(contract.textContent).toBe(t("settings.pi.contractUnverified"));
  });

  it("「验证契约」才跑完整发现，并显示耗时与验证结论", async () => {
    const user = userEvent.setup();
    render(<PiSettings />);

    await screen.findByTestId("pi-candidates");
    await user.click(screen.getByRole("button", { name: t("settings.pi.verify") }));

    await waitFor(() => expect(fake.discoverPi).toHaveBeenCalledTimes(1));
    const result = await screen.findByTestId("pi-verify-result");
    expect(result.getAttribute("data-verification")).toBe("contract_probed");
    // 耗时是本地计时，只要它是可解析的非负整数（不去卡某个具体毫秒数）。
    const elapsed = Number(result.getAttribute("data-elapsed-ms"));
    expect(Number.isInteger(elapsed)).toBe(true);
    expect(elapsed).toBeGreaterThanOrEqual(0);
    expect(result.textContent).toContain(t("settings.pi.verification.contract_probed"));
    // 验证之后列表就是完整发现的事实，快速发现的提示必须消失。
    expect(screen.queryByTestId("pi-fast-hint")).toBeNull();
  });

  it("低于最低版本的候选不能被选用（只能升级）", async () => {
    render(<PiSettings />);

    const old = await screen.findByTestId("pi-candidate-path:1.4.0");
    expect(within(old).getByRole("button", { name: t("settings.pi.select") })).toBeDisabled();
  });
});
