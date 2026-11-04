// 资源库：配置档列表与"编辑前重读磁盘"。
//
// 这条测试守的是真机上抓到的一个缺陷：列表里的配置档是加载时的快照，
// 磁盘被外部改过之后点"编辑"，编辑器里显示的是旧名字、旧策略、旧 revision。
// 保存时 CAS 会拦住覆盖（没丢数据），但用户看到的不是真的。
// 因此编辑前必须先 api.profile(id) 重新读一次。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ResourcesView } from "./ResourcesView";
import type { Profile } from "../../types";

const stale: Profile = {
  id: "p-1",
  name: "旧名字",
  modelRef: "m-demo",
  skillIDs: null,
  mcpIDs: null,
  toolPolicy: { filesystem: "none", network: false, shell: false },
  memoryPolicy: { enabled: false, maxEntries: 0 },
  revision: 1,
};

const fresh: Profile = {
  ...stale,
  name: "磁盘上的名字",
  toolPolicy: { filesystem: "read", network: false, shell: false },
  revision: 7,
};

vi.mock("../../api", () => ({
  api: {
    profile: vi.fn(async () => ({ item: fresh, error: null })),
    saveProfile: vi.fn(async () => ({ item: fresh, error: null })),
    profileConflict: vi.fn(async () => ({ error: null })),
    // C04 的 MCP/工具区块会自己加载；这里给空集合，避免把"缺 mock"
    // 变成测试里的另一个错误弹层（真实失败会被重复 alert 掩盖）。
    mcpServers: vi.fn(async () => ({
      items: [],
      revision: 0,
      runtimeServers: [],
      pending: false,
      activeRun: false,
      diagnoseReady: false,
    })),
    tools: vi.fn(async () => ({ items: [] })),
  },
}));

// C03 之后资源库还包含技能与扩展；这几条测试只关心配置档，
// 但仍要满足 props（否则"缺字段"会掩盖真正的断言失败）。
const modelProps = {
  models: [],
  modelOptions: { apis: ["openai-completions"], inputKinds: ["text"], thinkingLevels: ["low"] },
  modelRevision: 0,
  currentModel: null,
  skills: [],
  skillRevision: 0,
  skillsPending: false,
  skillsActiveRun: false,
  runtimeSkills: [],
  extensions: [],
  extensionRevision: 0,
  onModelsChanged: () => {},
  onSkillsChanged: () => {},
};

describe("ResourcesView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("编辑已有配置档时先读磁盘，而不是用列表快照", async () => {
    const { api } = await import("../../api");
    render(
      <ResourcesView profiles={[stale]} workspaceId="ws-1" onChanged={() => {}} {...modelProps} />
    );

    // 列表本身显示的是快照（外部改动不该凭空出现）。
    expect(screen.getByText("旧名字")).toBeInTheDocument();

    await userEvent.setup().click(screen.getByRole("button", { name: "编辑" }));

    await waitFor(() => expect(api.profile).toHaveBeenCalledWith("p-1"));
    // 编辑器拿到的是磁盘版本：名字、策略、revision 都必须是新的。
    await waitFor(() => expect(screen.getByDisplayValue("磁盘上的名字")).toBeInTheDocument());
    expect(screen.getByText(/基于 revision 7/)).toBeInTheDocument();
  });

  it("读取失败时给出错误码，不静默打开一个空编辑器", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.profile).mockResolvedValueOnce({
      item: null as unknown as Profile,
      error: { code: "CONFIG_CONFLICT", message: "配置被外部编辑", retryable: false },
    } as never);

    render(
      <ResourcesView profiles={[stale]} workspaceId="ws-1" onChanged={() => {}} {...modelProps} />
    );
    await userEvent.setup().click(screen.getByRole("button", { name: "编辑" }));

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("CONFIG_CONFLICT"));
    expect(screen.queryByText("新建配置档")).not.toBeInTheDocument();
  });
});
