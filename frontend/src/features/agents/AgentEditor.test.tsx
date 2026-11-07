// Agent 编辑器（U03 / UI07）的关键断言。
//
// 这里守的不是"字段画全了"，而是五件会真的伤到用户的事：
//
//  1. 基于过期 revision 的保存被拒绝，且界面明确说"什么都没写进去"；
//  2. 复制只发引用（模型/技能/MCP）与目标 ID，不带任何秘密字段；
//  3. 删除必须先确认，确认后按住 revision 删；
//  4. 超过上限的权限选项被禁用，被后端拒绝时把结构化原因显示出来；
//  5. 重新挂载（模拟重启）后显示的是后端读回的值，不是内存里的草稿。
//
// 文案一律从 catalog 取：测试里不手写中文，避免"文案改了测试还在按旧字面量过"。

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";
import type {
  AgentCeiling,
  AgentEditorState,
  AgentSource,
  AgentResources,
} from "./api";
import type { CallError, Profile } from "../../types";

// 每个绑定都被测试替换成 mock；类型标成 any 是为了能直接读 `.mock.calls`
// （断言"调用参数是什么"是这里的重点）。
const backend = vi.hoisted(() => ({
  state: null as any,
  ceiling: null as any,
  save: null as any,
  duplicate: null as any,
  rename: null as any,
  remove: null as any,
  conflict: null as any,
  resources: null as any,
}));

vi.mock("./api", () => ({
  agentsApi: {
    state: (...a: any[]) => backend.state(...a),
    ceiling: (...a: any[]) => backend.ceiling(...a),
    save: (...a: any[]) => backend.save(...a),
    duplicate: (...a: any[]) => backend.duplicate(...a),
    rename: (...a: any[]) => backend.rename(...a),
    remove: (...a: any[]) => backend.remove(...a),
    conflict: (...a: any[]) => backend.conflict(...a),
    resources: (...a: any[]) => backend.resources(...a),
  },
}));

import { AgentEditor } from "./AgentEditor";

// ---------------------------------------------------------------- fixtures

function profile(over: Partial<Profile> = {}): Profile {
  return {
    id: "agent-1",
    name: "助手",
    modelRef: "m-gpt",
    skillIDs: ["sk-a"],
    mcpIDs: ["mcp-a"],
    toolPolicy: { filesystem: "read", network: false, shell: false },
    memoryPolicy: { enabled: true, maxEntries: 20 },
    revision: 3,
    ...over,
  };
}

function ceiling(over: Partial<AgentCeiling> = {}): AgentCeiling {
  return {
    filesystem: "read",
    network: false,
    shell: false,
    sources: {
      "permissions.filesystem": "workspace",
      "permissions.network": "system",
      "permissions.shell": "system",
    },
    available: true,
    ...over,
  };
}

function sources(over: AgentSource[] = []): AgentSource[] {
  const base: AgentSource[] = [
    { field: "modelRef", value: "m-gpt", source: "user", available: true },
    { field: "skillIDs", value: "sk-a", source: "user", available: true },
    { field: "mcpIDs", value: "mcp-a", source: "workspace", available: true },
  ];
  return over.length ? over : base;
}

function state(over: Partial<AgentEditorState> = {}): AgentEditorState {
  return {
    profile: profile(),
    ceiling: ceiling(),
    sources: sources(),
    unavailableFields: ["description", "systemPrompt", "reasoningLevel", "toolIDs"],
    ...over,
  };
}

function resources(over: Partial<AgentResources> = {}): AgentResources {
  return {
    models: [
      {
        id: "m-gpt",
        providerId: "openai",
        modelId: "gpt-4o",
        api: "openai-completions",
        baseUrl: "",
        inputKinds: ["text"],
        contextWindow: 128000,
        maxOutput: 4096,
        credentialRef: "cred-1",
        thinkingLevels: ["low", "high"],
        enabled: true,
      },
    ],
    skills: [
      {
        id: "sk-a",
        name: "sk-a",
        description: "",
        path: "/skills/sk-a",
        scope: "user",
        enabled: true,
        origin: "user",
        revision: 1,
        hash: "h",
        dependencies: null,
        missingDependencies: null,
        available: true,
        disableModelInvocation: false,
        bytes: 10,
      },
    ],
    mcp: [{ id: "mcp-a", transport: "stdio", command: "npx", enabled: true, exposure: "direct" }],
    tools: [{ name: "read_file", source: "builtin", enabled: true, policy: "approval", capabilityState: "available" }],
    memoryScopes: [
      { scope: "user", workspaceId: "", enabled: true },
      { scope: "workspace", workspaceId: "ws-1", enabled: false },
    ],
    ...over,
  };
}

function callError(over: Partial<CallError> = {}): CallError {
  return { code: "INVALID_INPUT", message: "Go 侧原文", retryable: false, ...over };
}

beforeEach(() => {
  backend.state = vi.fn(async () => state());
  backend.ceiling = vi.fn(async () => ({ ceiling: ceiling(), unavailableFields: state().unavailableFields }));
  backend.save = vi.fn(async () => ({ item: profile({ revision: 4 }) }));
  backend.duplicate = vi.fn(async () => ({ item: profile({ id: "agent-copy", revision: 1 }) }));
  backend.rename = vi.fn(async () => ({ item: profile({ name: "新名", revision: 4 }) }));
  backend.remove = vi.fn(async () => ({ item: profile() }));
  backend.conflict = vi.fn(async () => ({
    current: profile({ name: "别人的改动", revision: 5 }),
    diff: [{ field: "name", mine: "我的改动", theirs: "别人的改动" }],
  }));
  backend.resources = vi.fn(async () => resources());
});

function renderEditor(props: Partial<Parameters<typeof AgentEditor>[0]> = {}) {
  return render(
    <AgentEditor profileId="agent-1" workspaceId="ws-1" {...props} />
  );
}

// ready 等到"编辑区真的出现"：StatusBadge 也带 role=status，
// 用"没有 role=status"判断就绪会把不可用徽标误当成加载中。
async function ready() {
  await screen.findByLabelText(t("agents.name"));
}

// ---------------------------------------------------------------- 1. CAS

describe("AgentEditor: 并发编辑", () => {
  it("stale revision surfaces a conflict and overwrites nothing", async () => {
    const onSaved = vi.fn();
    backend.save = vi.fn(async () => ({ error: callError({ code: "CONFIG_CONFLICT" }) }));
    renderEditor({ onSaved });
    await ready();

    const nameInput = screen.getByLabelText(t("agents.name"));
    await userEvent.clear(nameInput);
    await userEvent.type(nameInput, "我的改动");
    await userEvent.click(screen.getByRole("button", { name: t("agents.save") }));

    // 保存必须带上"我读到的那一版"，而不是 body 里的 revision。
    await waitFor(() => expect(backend.save).toHaveBeenCalledTimes(1));
    expect(backend.save.mock.calls[0][2]).toBe(3);
    expect(backend.conflict).toHaveBeenCalledTimes(1);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(t("agents.conflictTitle"));
    expect(alert).toHaveTextContent(t("agents.conflictNothingWritten"));
    expect(within(alert).getByText("别人的改动")).toBeInTheDocument();

    // 冲突不是"保存成功"，两个方向都不能出现成功标记。
    expect(screen.queryByText(t("agents.saved", { revision: 5 }))).toBeNull();
    expect(onSaved).not.toHaveBeenCalled();
  });

  it("reloading after a conflict takes the disk values", async () => {
    backend.save = vi.fn(async () => ({ error: callError({ code: "CONFIG_CONFLICT" }) }));
    renderEditor();
    await ready();

    const nameInput = screen.getByLabelText(t("agents.name"));
    await userEvent.clear(nameInput);
    await userEvent.type(nameInput, "我的改动");
    await userEvent.click(screen.getByRole("button", { name: t("agents.save") }));

    backend.state = vi.fn(async () => state({ profile: profile({ name: "别人的改动", revision: 5 }) }));
    await userEvent.click(await screen.findByRole("button", { name: t("agents.conflictReload") }));

    await waitFor(() =>
      expect(screen.getByLabelText(t("agents.name"))).toHaveValue("别人的改动")
    );
  });
});

// ---------------------------------------------------------------- 2. 复制

describe("AgentEditor: 复制", () => {
  it("duplicates references only, never a secret", async () => {
    renderEditor();
    await ready();

    await userEvent.type(screen.getByLabelText(t("agents.duplicateId")), "agent-copy");
    await userEvent.click(screen.getByRole("button", { name: t("agents.duplicateAction") }));

    await waitFor(() => expect(backend.duplicate).toHaveBeenCalledTimes(1));
    const args = backend.duplicate.mock.calls[0];
    expect(args[0]).toBe("agent-1");
    expect(args[1]).toBe("agent-copy");
    expect(args[2]).toBe("ws-1");
    // 复制请求里不能出现任何"秘密形状"的键或值：领域文档本来就没有秘密，
    // 界面也就不该有机会把秘密塞进去。
    for (const arg of args) {
      if (typeof arg === "string") {
        expect(arg).not.toMatch(/api[-_]?key|secret|token|password|authorization/i);
      }
    }

    // 界面上没有任何可以填密钥的字段。
    expect(screen.queryByLabelText(/api[-_]?key|secret|token/i)).toBeNull();
  });
});

// ---------------------------------------------------------------- 3. 删除

describe("AgentEditor: 删除", () => {
  it("asks for confirmation before removing anything", async () => {
    const onDeleted = vi.fn();
    renderEditor({ onDeleted });
    await ready();

    await userEvent.click(screen.getByRole("button", { name: t("agents.delete") }));
    // 第一次点击只是"问一句"：什么都没删。
    expect(backend.remove).not.toHaveBeenCalled();
    expect(screen.getByText(t("agents.deleteConfirm", { name: "助手" }))).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: t("common.cancel") }));
    expect(backend.remove).not.toHaveBeenCalled();
    expect(onDeleted).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: t("agents.delete") }));
    await userEvent.click(screen.getByRole("button", { name: t("agents.deleteConfirmYes") }));

    await waitFor(() => expect(backend.remove).toHaveBeenCalledWith("agent-1", 3));
    await waitFor(() => expect(onDeleted).toHaveBeenCalledWith("agent-1"));
  });

  it("a rejected delete keeps the editor on the profile", async () => {
    backend.remove = vi.fn(async () => ({ error: callError({ code: "CONFIG_CONFLICT" }) }));
    const onDeleted = vi.fn();
    renderEditor({ onDeleted });
    await ready();

    await userEvent.click(screen.getByRole("button", { name: t("agents.delete") }));
    await userEvent.click(screen.getByRole("button", { name: t("agents.deleteConfirmYes") }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(t("agents.deleteFailed"));
    expect(onDeleted).not.toHaveBeenCalled();
    // 失败后编辑区还在：不能"删失败但界面当成删掉了"。
    expect(screen.getByLabelText(t("agents.name"))).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------- 4. 权限上限

describe("AgentEditor: 权限上限", () => {
  it("disables options beyond the ceiling and shows where the ceiling comes from", async () => {
    renderEditor();
    await ready();

    const select = screen.getByLabelText(t("agents.filesystem"));
    const workspaceOption = within(select).getByRole("option", {
      name: t("agents.filesystemOptions.workspace"),
    }) as HTMLOptionElement;
    const unrestricted = within(select).getByRole("option", {
      name: t("agents.filesystemOptions.unrestricted"),
    }) as HTMLOptionElement;
    const readOption = within(select).getByRole("option", {
      name: t("agents.filesystemOptions.read"),
    }) as HTMLOptionElement;

    expect(workspaceOption.disabled).toBe(true);
    expect(unrestricted.disabled).toBe(true);
    // 上限本身（read）与更严的取值仍然可选。
    expect(readOption.disabled).toBe(false);

    // 网络/shell 在系统上限下不可勾选，且说明了是谁定的。
    expect(screen.getByLabelText(t("agents.network"))).toBeDisabled();
    expect(screen.getByLabelText(t("agents.shell"))).toBeDisabled();
    expect(screen.getByText(t("agents.ceilingSource", { source: t("agents.sourceLayers.workspace") }))).toBeInTheDocument();
  });

  it("shows the structured reason when the backend rejects a widening", async () => {
    backend.save = vi.fn(async () => ({
      error: callError({
        params: {
          field: "permissions.filesystem",
          requested: "workspace",
          ceiling: "read",
          source: "workspace",
        },
        operation: "agent.save",
      }),
    }));
    renderEditor();
    await ready();

    await userEvent.type(screen.getByLabelText(t("agents.name")), "!");
    await userEvent.click(screen.getByRole("button", { name: t("agents.save") }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(
      t("agents.ceilingRejected", {
        field: t("agents.fields.filesystem"),
        requested: t("agents.filesystemOptions.workspace"),
        ceiling: t("agents.filesystemOptions.read"),
        source: t("agents.sourceLayers.workspace"),
      })
    );
    // 被拒绝 ≠ 已保存。
    expect(screen.queryByText(t("agents.saved", { revision: 4 }))).toBeNull();
  });
});

// ---------------------------------------------------------------- 5. 读回与 unavailable

describe("AgentEditor: 读回与 unavailable", () => {
  it("renders the values the backend returns on a fresh mount", async () => {
    const first = renderEditor();
    await ready();
    expect(screen.getByLabelText(t("agents.name"))).toHaveValue("助手");
    expect(screen.getByLabelText(t("agents.model"))).toHaveValue("m-gpt");
    expect(screen.getByText(t("agents.revision", { revision: 3 }))).toBeInTheDocument();
    first.unmount();

    // 重新挂载 = 重启后重新读取：值来自后端，不来自上一次的内存草稿。
    backend.state = vi.fn(async () => state({ profile: profile({ name: "重启后读回的名字", revision: 7 }) }));
    renderEditor();
    await ready();
    expect(screen.getByLabelText(t("agents.name"))).toHaveValue("重启后读回的名字");
    expect(screen.getByText(t("agents.revision", { revision: 7 }))).toBeInTheDocument();
  });

  it("marks fields the domain does not carry as unavailable instead of faking them", async () => {
    renderEditor();
    await ready();

    for (const field of ["description", "systemPrompt", "reasoningLevel", "toolIDs"]) {
      const block = screen.getByTestId(`unavailable-${field}`);
      expect(within(block).getByText(t("agents.unavailableField"))).toBeInTheDocument();
    }
    // 思考级别的可选值来自所选模型，但仍然是"不可用"状态：
    // 领域模型不承载它，写进去会被丢掉。
    expect(screen.getByTestId("unavailable-reasoningLevel")).toHaveTextContent("low");
    expect(screen.getByTestId("unavailable-reasoningLevel")).toHaveTextContent("high");
  });

  it("lists references that no longer resolve", async () => {
    backend.state = vi.fn(async () =>
      state({
        sources: sources([
          { field: "modelRef", value: "m-gpt", source: "user", available: true },
          { field: "skillIDs", value: "sk-a", source: "unavailable", available: false },
        ]),
        missing: [{ kind: "skill", id: "sk-a" }],
      })
    );
    renderEditor();
    await ready();

    expect(screen.getByText(t("agents.missingRefs", { refs: "sk-a" }))).toBeInTheDocument();
  });

  it("does not show a success marker before the backend answers", async () => {
    let resolveSave: (v: unknown) => void = () => {};
    backend.save = vi.fn(
      () =>
        new Promise((resolve) => {
          resolveSave = resolve;
        })
    );
    renderEditor();
    await ready();

    await userEvent.click(screen.getByRole("button", { name: t("agents.save") }));
    expect(screen.getByRole("button", { name: t("agents.saving") })).toBeDisabled();
    expect(screen.queryByText(t("agents.saved", { revision: 4 }))).toBeNull();

    resolveSave({ item: profile({ revision: 4 }) });
    await waitFor(() => expect(screen.getByText(t("agents.saved", { revision: 4 }))).toBeInTheDocument());
  });
});
