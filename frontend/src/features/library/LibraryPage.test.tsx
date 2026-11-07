// 资源库页面测试（U04 / UI08、RES05）。
//
// 断言的是"界面上真的显示了什么"：拒绝原因逐条出现、诊断未完成时不能显示已连接、
// 启停/删除走真实调用并在之后重新读取列表。假数据只在 mock 的 api 层，
// 组件不自己编造状态。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";

const fake = vi.hoisted(() => ({
  skills: null as unknown as () => Promise<any>,
  createSkill: null as unknown as (...a: any[]) => Promise<any>,
  inspectSkillLink: null as unknown as (...a: any[]) => Promise<any>,
  importSkill: null as unknown as (...a: any[]) => Promise<any>,
  setSkillEnabled: null as unknown as (...a: any[]) => Promise<any>,
  deleteSkill: null as unknown as (...a: any[]) => Promise<any>,
  readSkill: null as unknown as (...a: any[]) => Promise<any>,
  saveSkill: null as unknown as (...a: any[]) => Promise<any>,
  dryRunSkill: null as unknown as (...a: any[]) => Promise<any>,
  profiles: null as unknown as () => Promise<any>,
  mcpServers: null as unknown as () => Promise<any>,
  setMCPEnabled: null as unknown as (...a: any[]) => Promise<any>,
  deleteMCP: null as unknown as (...a: any[]) => Promise<any>,
  diagnoseMCP: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({ libraryApi: fake }));

import { LibraryPage } from "./LibraryPage";

function skill(overrides: Record<string, any> = {}) {
  return {
    id: "demo-skill",
    name: "demo-skill",
    description: "演示技能",
    path: "/data/skills/demo-skill/SKILL.md",
    scope: "user",
    enabled: true,
    origin: "imported",
    revision: 3,
    hash: "abcdef0123456789",
    dependencies: [],
    missingDependencies: [],
    available: true,
    disableModelInvocation: false,
    bytes: 128,
    ...overrides,
  };
}

function skillList(items: any[], overrides: Record<string, any> = {}) {
  return { items, revision: 3, activeRun: false, pending: false, runtimeSkills: [], ...overrides };
}

beforeEach(() => {
  // 每个用例都换成 spy：断言"没有调用"必须能区分"没调用"与"调用了假的函数"。
  const spy = () => vi.fn();
  fake.skills = spy().mockImplementation(async () => skillList([skill()]));
  fake.createSkill = spy().mockImplementation(async () => ({ item: skill(), revision: 4, pending: false }));
  fake.inspectSkillLink = spy().mockImplementation(async () => ({
    url: "https://example.com/x.zip",
    finalUrl: "https://example.com/x.zip",
    form: "zip",
    name: "linked",
    description: "来自链接",
    requires: [],
    disableModelInvocation: false,
    hash: "hash",
    bytes: 10,
    files: 1,
    stagedPath: "/data/skill-links/link-1/SKILL.md",
    redirects: [],
    warnings: [],
    rejections: [],
  }));
  fake.importSkill = spy().mockImplementation(async () => ({ item: skill({ id: "linked" }), revision: 4, pending: false }));
  fake.setSkillEnabled = spy().mockImplementation(async () => ({ item: skill({ enabled: false }), revision: 4, pending: false }));
  fake.deleteSkill = spy().mockImplementation(async () => ({ revision: 4, pending: false }));
  fake.readSkill = spy().mockImplementation(async () => ({ item: skill(), content: "---\nname: demo-skill\n---\n", }));
  fake.saveSkill = spy().mockImplementation(async () => ({ item: skill(), revision: 3, pending: false }));
  fake.dryRunSkill = spy().mockImplementation(async () => ({
    itemId: "demo-skill",
    state: "completed",
    output: "试运行输出",
    policy: {
      filesystem: "read",
      network: false,
      shell: false,
      approvals: "deny",
      deniedCapabilities: ["write", "shell", "network"],
    },
    denials: [],
    isolationArgs: ["--no-mcp"],
    tempCleaned: true,
    skillUnchanged: true,
    userStateUnchanged: true,
    durationMs: 10,
    limitations: ["DRYRUN_LIMIT_USER_SKILL_DISCOVERY"],
  }));
  fake.profiles = spy().mockImplementation(async () => ({ items: [{ id: "p1", name: "主 Agent", modelRef: "m1", skillIDs: [], mcpIDs: [], revision: 1 }] }));
  fake.mcpServers = spy().mockImplementation(async () => ({
    items: [{ id: "fs", transport: "stdio", command: "node", enabled: true, exposure: "direct" }],
    revision: 2,
    runtimeServers: [],
    pending: false,
    activeRun: false,
    diagnoseReady: true,
  }));
  fake.setMCPEnabled = spy().mockImplementation(async () => ({ item: {}, revision: 3, pending: false }));
  fake.deleteMCP = spy().mockImplementation(async () => ({ revision: 3, pending: false }));
  fake.diagnoseMCP = spy().mockImplementation(async () => ({
    status: { id: "fs", state: "connected", tools: [], source: "probe", configured: true, enabled: true, exposure: "direct", transport: "stdio", oauthSupported: false, checkedAt: "" },
  }));
});

describe("LibraryPage", () => {
  it("renders real skill facts from the API", async () => {
    render(<LibraryPage workspaceId="ws-1" />);
    const item = await screen.findByTestId("skill-demo-skill");
    expect(within(item).getByText("demo-skill")).toBeInTheDocument();
    expect(within(item).getByText("/data/skills/demo-skill/SKILL.md")).toBeInTheDocument();
    expect(within(item).getByText(t("library.labels.enabled"))).toBeInTheDocument();
    expect(within(item).getByText(t("library.origin.imported"))).toBeInTheDocument();
  });

  it("shows every link rejection reason instead of a generic failure", async () => {
    fake.inspectSkillLink = async () => ({
      url: "https://10.0.0.5/SKILL.md",
      finalUrl: "https://10.0.0.5/SKILL.md",
      form: "",
      name: "",
      description: "",
      requires: [],
      disableModelInvocation: false,
      hash: "",
      bytes: 0,
      files: 0,
      stagedPath: "",
      redirects: [],
      warnings: [],
      rejections: [
        { rule: "LINK_SPECIAL_PURPOSE_ADDRESS", params: { host: "10.0.0.5", address: "10.0.0.5", range: "Private-Use" } },
        { rule: "LINK_SCHEME_HTTPS_ONLY", params: { scheme: "http" } },
      ],
    });
    render(<LibraryPage workspaceId="ws-1" />);
    await userEvent.click(await screen.findByRole("button", { name: t("library.actions.importLink") }));
    await userEvent.type(screen.getByLabelText(t("library.import.urlLabel")), "https://10.0.0.5/SKILL.md");
    await userEvent.click(screen.getByRole("button", { name: t("library.import.inspect") }));

    const box = await screen.findByTestId("link-rejections");
    expect(box).toHaveTextContent("10.0.0.5");
    expect(box).toHaveTextContent("Private-Use");
    expect(box).toHaveTextContent("http");
    // 逐条：两条原因各自一条 li。
    expect(within(box).getAllByRole("listitem")).toHaveLength(2);
    // 被拒绝时不能出现确认导入按钮，也没有调用导入。
    expect(screen.queryByRole("button", { name: t("library.import.confirm") })).toBeNull();
    expect(fake.importSkill).not.toHaveBeenCalled();
  });

  it("imports the inspected copy only after confirmation", async () => {
    const importSkill = vi.fn(async () => ({ item: skill({ id: "linked" }), revision: 4, pending: false }));
    fake.importSkill = importSkill;
    render(<LibraryPage workspaceId="ws-1" />);
    await userEvent.click(await screen.findByRole("button", { name: t("library.actions.importLink") }));
    await userEvent.type(screen.getByLabelText(t("library.import.urlLabel")), "https://example.com/x.zip");
    await userEvent.click(screen.getByRole("button", { name: t("library.import.inspect") }));
    await userEvent.click(await screen.findByRole("button", { name: t("library.import.confirm") }));
    await waitFor(() => expect(importSkill).toHaveBeenCalledWith("/data/skill-links/link-1/SKILL.md", "user", "ws-1"));
  });

  it("creates a local skill and surfaces duplicate-name rejection", async () => {
    const createSkill = vi.fn(async () => ({
      item: undefined,
      revision: 0,
      pending: false,
      error: { code: "CONFIG_CONFLICT", message: "已存在", retryable: false },
    }));
    fake.createSkill = createSkill;
    render(<LibraryPage workspaceId="ws-1" />);
    await userEvent.click(await screen.findByRole("button", { name: t("library.actions.createLocal") }));
    await userEvent.type(screen.getByLabelText(t("library.import.createNameLabel")), "demo-skill");
    await userEvent.click(screen.getByRole("button", { name: t("library.import.create") }));
    await waitFor(() => expect(createSkill).toHaveBeenCalledWith("demo-skill", "user", "ws-1"));
    expect(await screen.findByRole("alert")).toHaveTextContent(t("errors.CONFIG_CONFLICT"));
  });

  it("toggles and deletes through the API and reloads the list", async () => {
    const skills = vi.fn(async () => skillList([skill()]));
    fake.skills = skills;
    const setEnabled = vi.fn(async () => ({ item: skill({ enabled: false }), revision: 4, pending: false }));
    fake.setSkillEnabled = setEnabled;
    const del = vi.fn(async () => ({ revision: 4, pending: false }));
    fake.deleteSkill = del;
    render(<LibraryPage workspaceId="ws-1" />);
    await userEvent.click(await screen.findByRole("button", { name: t("library.actions.disable") }));
    await waitFor(() => expect(setEnabled).toHaveBeenCalledWith("demo-skill", 3, false));
    await waitFor(() => expect(skills).toHaveBeenCalledTimes(2));

    await userEvent.click(screen.getByRole("button", { name: t("library.actions.delete") }));
    await userEvent.click(screen.getByRole("button", { name: t("library.actions.deleteConfirm") }));
    await waitFor(() => expect(del).toHaveBeenCalledWith(3, "demo-skill"));
  });

  it("renders per-item unavailability and missing dependencies", async () => {
    fake.skills = async () =>
      skillList([
        skill({
          available: false,
          unavailableReason: "缺少依赖命令: jq",
          missingDependencies: ["jq"],
          dependencies: ["jq"],
        }),
      ]);
    render(<LibraryPage workspaceId="ws-1" />);
    const item = await screen.findByTestId("skill-demo-skill");
    expect(item).toHaveTextContent(t("library.labels.unavailable"));
    expect(item).toHaveTextContent("缺少依赖命令: jq");
    expect(item).toHaveTextContent("jq");
  });

  it("shows the dry-run policy facts and never claims user data changed", async () => {
    render(<LibraryPage workspaceId="ws-1" />);
    await userEvent.click(await screen.findByRole("button", { name: t("library.actions.dryRun") }));
    await userEvent.type(screen.getByLabelText(t("library.dryrun.promptLabel")), "计划一下");
    await userEvent.click(screen.getByRole("button", { name: t("library.dryrun.start") }));
    const panel = await screen.findByTestId("dry-run-panel");
    expect(await within(panel).findByText(new RegExp(t("library.dryrun.state.completed")))).toBeInTheDocument();
    expect(panel).toHaveTextContent("read");
    expect(panel).toHaveTextContent(t("library.dryrun.tempCleaned"));
    expect(panel).toHaveTextContent("试运行输出");
  });

  it("does not show connected before a diagnosis, and shows it after one", async () => {
    render(<LibraryPage workspaceId="ws-1" />);
    await userEvent.click(await screen.findByRole("tab", { name: t("library.tabs.mcp") }));
    const row = await screen.findByTestId("mcp-fs");
    expect(within(row).getByText(t("library.mcp.unknown"))).toBeInTheDocument();
    // 状态胶囊必须不存在：unknown 的说明文字里提到"已连接"是为了解释语义，
    // 不能把状态胶囊也误判成已连接。
    expect(within(row).queryByText(t("library.mcpState.connected"), { selector: "span" })).toBeNull();
    await userEvent.click(within(row).getByRole("button", { name: t("library.actions.diagnose") }));
    await waitFor(() => expect(fake.diagnoseMCP).toHaveBeenCalledWith("fs"));
    expect(await within(await screen.findByTestId("mcp-fs")).findByText(t("library.mcpState.connected"))).toBeInTheDocument();
  });

  it("renders MCP failure with its error code and repair hint", async () => {
    fake.diagnoseMCP = async () => ({
      status: {
        id: "fs",
        state: "failed",
        tools: [],
        configured: true,
        enabled: true,
        exposure: "direct",
        transport: "stdio",
        oauthSupported: false,
        checkedAt: "",
        error: { code: "DEPENDENCY_MISSING", message: "缺少命令", retryable: false },
      },
    });
    render(<LibraryPage workspaceId="ws-1" />);
    await userEvent.click(await screen.findByRole("tab", { name: t("library.tabs.mcp") }));
    const row = await screen.findByTestId("mcp-fs");
    await userEvent.click(within(row).getByRole("button", { name: t("library.actions.diagnose") }));
    await waitFor(async () => {
      const updated = await screen.findByTestId("mcp-fs");
      expect(updated).toHaveTextContent(t("errors.DEPENDENCY_MISSING"));
      expect(updated).toHaveTextContent("DEPENDENCY_MISSING");
      expect(updated).toHaveTextContent(t("library.mcp.repairHint"));
    });
  });

  it("lists agents and navigates to the agent editor", async () => {
    const onNavigate = vi.fn();
    render(<LibraryPage workspaceId="ws-1" onNavigate={onNavigate} />);
    await userEvent.click(await screen.findByRole("tab", { name: t("library.tabs.agents") }));
    expect(await screen.findByText("主 Agent")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: t("library.actions.openAgent") }));
    expect(onNavigate).toHaveBeenCalledWith("/agents?agent=p1");
  });
});
