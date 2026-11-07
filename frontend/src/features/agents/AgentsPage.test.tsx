// Agent 页（U03）：列表、空态、创建与选中态。
//
// 页面本身很薄，但有两条不能含糊：
//   - 列表与错误来自壳层的后端快照，不是前端自造；
//   - 创建同样走 SaveAgentProfile（expectedRevision=0），成功后导航到新 ID。

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";
import { ShellProvider, type ShellValue } from "../../app/shell";
import type { BackendState } from "../../app/useBackend";
import type { Profile } from "../../types";

const backend = vi.hoisted(() => ({
  resources: null as any,
  save: null as any,
  state: null as any,
  ceiling: null as any,
  duplicate: null as any,
  rename: null as any,
  remove: null as any,
  conflict: null as any,
}));

vi.mock("./api", () => ({
  agentsApi: {
    resources: (...a: any[]) => backend.resources(...a),
    save: (...a: any[]) => backend.save(...a),
    state: (...a: any[]) => backend.state(...a),
    ceiling: (...a: any[]) => backend.ceiling(...a),
    duplicate: (...a: any[]) => backend.duplicate(...a),
    rename: (...a: any[]) => backend.rename(...a),
    remove: (...a: any[]) => backend.remove(...a),
    conflict: (...a: any[]) => backend.conflict(...a),
  },
}));

import { AgentsPage } from "./AgentsPage";

function profile(over: Partial<Profile> = {}): Profile {
  return {
    id: "agent-1",
    name: "助手",
    modelRef: "m-gpt",
    skillIDs: null,
    mcpIDs: null,
    toolPolicy: { filesystem: "read", network: false, shell: false },
    memoryPolicy: { enabled: false, maxEntries: 0 },
    revision: 2,
    ...over,
  };
}

function shellValue(over: Partial<ShellValue> = {}): ShellValue {
  const backendState: BackendState = {
    loading: false,
    error: null,
    status: null,
    settings: null,
    workspaces: [],
    currentWorkspace: {
      id: "ws-1",
      name: "项目",
      canonicalPath: "/work",
      trusted: true,
      createdAt: "2026-11-05T00:00:00Z",
    },
    profiles: [],
    models: [],
    modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
    modelRevision: 0,
    currentModel: null,
    skills: [],
    skillRevision: 0,
    skillsPending: false,
    skillsActiveRun: false,
    extensions: [],
    extensionRevision: 0,
  } as unknown as BackendState;

  return {
    backend: backendState,
    status: null,
    settings: null,
    workspaces: [],
    currentWorkspace: backendState.currentWorkspace,
    profiles: [],
    models: [],
    modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
    modelRevision: 0,
    currentModel: null,
    skills: [],
    skillRevision: 0,
    skillsPending: false,
    skillsActiveRun: false,
    extensions: [],
    extensionRevision: 0,
    ui: {} as ShellValue["ui"],
    input: "",
    onInput: () => {},
    onSend: () => {},
    onStop: () => {},
    onDecide: () => {},
    onSendToSession: () => {},
    onAnswerClarification: () => {},
    onReplayRun: () => {},
    busy: false,
    savingSettings: false,
    actionError: null,
    saveSettings: async () => {},
    trustWorkspace: async () => {},
    addWorkspace: async () => {},
    refreshProfiles: vi.fn(),
    refreshModels: vi.fn(),
    refreshSkills: vi.fn(),
    onNewSession: async () => {},
    ...over,
  } as ShellValue;
}

function renderPage(shell: ShellValue, route?: { query: URLSearchParams }) {
  return render(
    <ShellProvider value={shell}>
      <AgentsPage route={route} />
    </ShellProvider>
  );
}

beforeEach(() => {
  backend.resources = vi.fn(async () => ({
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
        thinkingLevels: ["low"],
        enabled: true,
      },
    ],
    skills: [],
    mcp: [],
    tools: [],
    memoryScopes: [],
  }));
  backend.save = vi.fn(async () => ({ item: profile({ id: "agent-new", name: "agent-new", revision: 1 }) }));
  backend.state = vi.fn(async () => ({
    profile: profile(),
    ceiling: { filesystem: "read", network: false, shell: false, sources: {}, available: true },
    sources: [],
    unavailableFields: ["description", "systemPrompt", "reasoningLevel", "toolIDs"],
  }));
});

describe("AgentsPage", () => {
  it("shows the empty state when there is no Agent", async () => {
    renderPage(shellValue());
    expect(await screen.findByText(t("agents.emptyTitle"))).toBeInTheDocument();
  });

  it("creates through the CAS API and navigates to the new id", async () => {
    const navigate = vi.fn();
    render(
      <ShellProvider value={shellValue()}>
        <AgentsPage onNavigate={navigate} />
      </ShellProvider>
    );

    const idInput = screen.getByLabelText(t("agents.newId"));
    const createButton = screen.getByRole("button", { name: t("agents.create") });
    // ID 没填时不能创建：否则会先失败再让用户猜为什么。
    expect(createButton).toBeDisabled();

    await userEvent.type(idInput, "agent-new");
    await waitFor(() => expect(screen.getByRole("button", { name: t("agents.create") })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: t("agents.create") }));

    await waitFor(() => expect(backend.save).toHaveBeenCalledTimes(1));
    const [id, workspaceId, expectedRevision, draft] = backend.save.mock.calls[0];
    expect(id).toBe("agent-new");
    expect(workspaceId).toBe("ws-1");
    expect(expectedRevision).toBe(0);
    expect(draft.modelRef).toBe("m-gpt");
    expect(draft.toolPolicy.filesystem).toBe("none");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/agents?agent=agent-new"));
  });

  it("shows a failed create as a structured error, not a silent no-op", async () => {
    backend.save = vi.fn(async () => ({
      error: { code: "INVALID_INPUT", message: "", retryable: false, params: { field: "id" } },
    }));
    renderPage(shellValue());

    await userEvent.type(screen.getByLabelText(t("agents.newId")), "agent-x");
    await waitFor(() => expect(screen.getByRole("button", { name: t("agents.create") })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: t("agents.create") }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("INVALID_INPUT");
  });

  it("renders the shell error with a retry", async () => {
    const refreshProfiles = vi.fn();
    const shell = shellValue({ refreshProfiles });
    shell.backend = {
      ...shell.backend,
      error: { code: "INTERNAL_ERROR", message: "", retryable: false },
    } as BackendState;
    renderPage(shell);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("INTERNAL_ERROR");
    await userEvent.click(screen.getByRole("button", { name: t("common.retry") }));
    expect(refreshProfiles).toHaveBeenCalled();
  });

  it("lists real profiles and selects one into the editor", async () => {
    const navigate = vi.fn();
    render(
      <ShellProvider value={shellValue({ profiles: [profile()] })}>
        <AgentsPage onNavigate={navigate} route={{ query: new URLSearchParams("agent=agent-1") }} />
      </ShellProvider>
    );

    expect(await screen.findByText("助手")).toBeInTheDocument();
    // 选中态由路由决定，不由"点过没点过"决定。
    const selected = screen.getByRole("button", { name: /助手/ });
    expect(selected).toHaveAttribute("aria-current", "true");

    // 编辑器读到后端现场后显示真实值。
    expect(await screen.findByLabelText(t("agents.name"))).toHaveValue("助手");
    expect(navigate).not.toHaveBeenCalled();
  });
});
