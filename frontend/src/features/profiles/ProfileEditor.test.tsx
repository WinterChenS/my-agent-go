// Profile（配置档）编辑器的冲突路径。
//
// 这是 CFG02 的前端一半：同 revision 只有一个 writer 能成功。
// 写的是"保存被拒之后界面必须做什么"——
//   1. 说清是磁盘上被改过，并给出当前 revision
//   2. 逐字段列出"我的 / 磁盘上的"，不能只说"冲突了"让人盲选
//   3. 提供两条出路：放弃重载、另存为副本；且**不得**静默覆盖
//
// 真实桌面上已经点过一遍（docs/evidence/c01-workbench.md），
// 这里把行为固定下来，防止以后改动把它做成"弹个红条就完事"。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProfileEditor } from "./ProfileEditor";
import type { Profile, ProfileConflictResult } from "../../types";

const mine: Profile = {
  id: "p-1",
  name: "我的名字",
  modelRef: "m-demo",
  skillIDs: null,
  mcpIDs: null,
  toolPolicy: { filesystem: "none", network: false, shell: false },
  memoryPolicy: { enabled: false, maxEntries: 0 },
  revision: 1,
};

const theirs: Profile = {
  ...mine,
  name: "磁盘上的名字",
  toolPolicy: { filesystem: "read", network: false, shell: false },
  revision: 2,
};

const conflict: ProfileConflictResult = {
  current: theirs,
  diff: [
    { field: "name", mine: "我的名字", theirs: "磁盘上的名字" },
    {
      field: "toolPolicy",
      mine: "filesystem=none, network=false, shell=false",
      theirs: "filesystem=read, network=false, shell=false",
    },
  ],
};

vi.mock("../../api", () => ({
  api: {
    saveProfile: vi.fn(),
    profileConflict: vi.fn(),
    profile: vi.fn(),
  },
}));

describe("ProfileEditor 冲突", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("过期 revision 保存被拒后，逐字段展示差异而不是只报一句冲突", async () => {
    const { api } = await import("../../api");
    const onSaved = vi.fn();
    vi.mocked(api.saveProfile).mockResolvedValueOnce({
      item: null,
      error: { code: "CONFIG_CONFLICT", message: "配置被外部改动", retryable: false },
    } as never);
    vi.mocked(api.profileConflict).mockResolvedValueOnce({ current: theirs, diff: conflict.diff } as never);

    render(
      <ProfileEditor profile={mine} workspaceId="ws-1" onSaved={onSaved} onReloaded={() => {}} />
    );

    // 保存必须带上被编辑时的 revision，而不是 0 或最新值。
    await userEvent.setup().click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(api.saveProfile).toHaveBeenCalled());
    expect(vi.mocked(api.saveProfile).mock.calls[0][2]).toBe(1);

    const panel = await screen.findByRole("alert");
    expect(panel).toHaveTextContent("磁盘上的版本已被改动");
    expect(panel).toHaveTextContent("当前 revision 2");

    // 差异逐字段可见，且两边都写清楚。
    expect(screen.getByText("name")).toBeInTheDocument();
    expect(screen.getByText("磁盘上的名字")).toBeInTheDocument();
    expect(screen.getByText("我的名字")).toBeInTheDocument();

    // 冲突不能当作"保存成功"。
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("选择放弃改动时按 ID 重新读磁盘，恢复到当前版本", async () => {
    const { api } = await import("../../api");
    const onReloaded = vi.fn();
    vi.mocked(api.saveProfile).mockResolvedValueOnce({
      item: null,
      error: { code: "CONFIG_CONFLICT", message: "配置被外部改动", retryable: false },
    } as never);
    vi.mocked(api.profileConflict).mockResolvedValueOnce({ current: theirs, diff: conflict.diff } as never);
    vi.mocked(api.profile).mockResolvedValueOnce({ item: theirs, error: null } as never);

    render(
      <ProfileEditor profile={mine} workspaceId="ws-1" onSaved={() => {}} onReloaded={onReloaded} />
    );

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "保存" }));
    await screen.findByRole("alert");

    await user.click(screen.getByRole("button", { name: /放弃我的改动/ }));

    await waitFor(() => expect(api.profile).toHaveBeenCalledWith("p-1"));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /保存/ })).toBeInTheDocument()
    );
    // 重载后草稿是磁盘版本：名称与 revision 都要跟着变。
    expect(screen.getByDisplayValue("磁盘上的名字")).toBeInTheDocument();
    expect(screen.getByText(/基于 revision 2/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(onReloaded).toHaveBeenCalled();
  });
});
