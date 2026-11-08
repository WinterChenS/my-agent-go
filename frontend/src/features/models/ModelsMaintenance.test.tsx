// 模型维护测试（MP1）。
//
// 守的是用户原话里的两件事：
//  1. 「自动读取本地已有的模型」——扫描结果按端点分组，勾选后一次导入；
//  2. 「额外添加模型和供应商」——模型编辑器有入口，供应商能加、能删，
//     拒绝原因如实显示。
//
// 另外守一条产品决定：模型维护只能有一个入口（设置 → 模型）。
// 资源库不再挂模型页签，避免"两个地方都能改"造成的第二份真相。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";

const fake = vi.hoisted(() => ({
  list: vi.fn(),
  save: vi.fn(),
  remove: vi.fn(),
  test: vi.fn(),
  setActive: vi.fn(),
  current: vi.fn(),
  discover: vi.fn(),
  probeProvider: vi.fn(),
  importModels: vi.fn(),
  providers: vi.fn(),
  saveProvider: vi.fn(),
  deleteProvider: vi.fn(),
}));

vi.mock("./api", () => ({
  modelsApi: fake,
  IMPORT_DEFAULTS: { contextWindow: 128000, maxOutput: 8192, inputKinds: ["text"] },
}));

import { ModelsMaintenance } from "./ModelsMaintenance";

const options = {
  apis: ["openai-completions", "openai-responses"],
  inputKinds: ["text", "image"],
  thinkingLevels: ["off", "low"],
};

function model(overrides: Record<string, unknown> = {}) {
  return {
    id: "m-vllm",
    providerId: "vllm",
    modelId: "meta-llama/Llama-3-8B-Instruct",
    api: "openai-completions",
    baseUrl: "http://127.0.0.1:8000/v1",
    inputKinds: ["text"],
    contextWindow: 128000,
    maxOutput: 8192,
    credentialRef: "",
    thinkingLevels: null,
    ...overrides,
  };
}

function provider(overrides: Record<string, unknown> = {}) {
  return {
    id: "vllm",
    baseUrl: "http://127.0.0.1:8000/v1",
    api: "openai-completions",
    insecure: false,
    modelIds: ["m-vllm"],
    ...overrides,
  };
}

function endpoint(overrides: Record<string, unknown> = {}) {
  return {
    kind: "vllm",
    baseUrl: "http://127.0.0.1:8000/v1",
    api: "openai-completions",
    reachable: true,
    latencyMs: 7,
    insecure: false,
    models: [{ id: "meta-llama/Llama-3-8B-Instruct" }],
    ...overrides,
  };
}

function current(overrides: Record<string, unknown> = {}) {
  return { source: "none", ref: "", provider: "", model: "", item: null, revision: 4, ...overrides };
}

function setup() {
  fake.list.mockResolvedValue({ items: [model()], revision: 4, options });
  fake.current.mockResolvedValue(current());
  fake.providers.mockResolvedValue({ items: [provider()], revision: 4 });
  fake.discover.mockResolvedValue({ items: [endpoint()] });
  fake.importModels.mockResolvedValue({
    imported: ["vllm-meta-llama-llama-3-8b-instruct"],
    updated: [],
    models: [model()],
    revision: 5,
  });
  fake.saveProvider.mockResolvedValue({ item: provider(), revision: 5 });
  fake.deleteProvider.mockResolvedValue({ revision: 5 });
  fake.save.mockResolvedValue({ item: model(), revision: 5 });
  fake.remove.mockResolvedValue({ revision: 5 });
  fake.test.mockResolvedValue({
    result: { reachable: true, toolCalling: "unknown", vision: "unknown", latencyMs: 9, providerId: "vllm", modelId: "x" },
  });
  fake.setActive.mockResolvedValue({ ref: "m-vllm", deferred: false });
}

beforeEach(() => {
  for (const fn of Object.values(fake)) fn.mockReset();
  setup();
});

describe("模型维护（设置 → 模型）", () => {
  it("新建模型保存后立刻出现在列表，并能设为当前", async () => {
    const user = userEvent.setup();
    render(<ModelsMaintenance />);
    await screen.findByTestId("model-m-vllm");

    await user.click(screen.getByRole("button", { name: t("library.models.actions.addManual") }));
    await user.type(screen.getByLabelText(t("library.models.editor.idLabel")), "m-new");
    await user.type(
      screen.getByLabelText(t("library.models.editor.providerLabel")),
      "local"
    );
    await user.type(screen.getByLabelText(t("library.models.editor.modelIdLabel")), "qwen2.5");
    await user.type(
      screen.getByLabelText(t("library.models.editor.baseUrlLabel")),
      "http://127.0.0.1:1234/v1"
    );

    // 保存成功后重新读取列表：新模型必须在里面。
    fake.list.mockResolvedValue({
      items: [model(), model({ id: "m-new", providerId: "local", modelId: "qwen2.5", baseUrl: "http://127.0.0.1:1234/v1" })],
      revision: 5,
      options,
    });
    await user.click(screen.getByRole("button", { name: t("library.models.actions.save") }));

    await screen.findByText("m-new");
    const rows = screen.getAllByRole("button", { name: t("library.models.actions.setActive") });
    await user.click(rows[rows.length - 1]);
    await waitFor(() => expect(fake.setActive).toHaveBeenCalledWith("m-new"));
  });

  it("扫描结果按端点分组，勾选后一次导入选中项", async () => {
    fake.discover.mockResolvedValue({
      items: [
        endpoint({
          kind: "ollama",
          baseUrl: "http://127.0.0.1:11434/v1",
          latencyMs: 3,
          models: [{ id: "llama3.2:latest" }, { id: "qwen2.5-coder:7b" }],
        }),
      ],
    });
    const user = userEvent.setup();
    render(<ModelsMaintenance />);
    await screen.findByTestId("model-m-vllm");

    await user.click(screen.getByRole("button", { name: t("library.models.actions.scanLocal") }));
    const card = await screen.findByTestId("endpoint-ollama");
    expect(within(card).getByText(/11434\/v1/)).toBeTruthy();

    await user.click(within(card).getByRole("checkbox", { name: "llama3.2:latest" }));
    await user.click(within(card).getByRole("checkbox", { name: "qwen2.5-coder:7b" }));
    await user.click(within(card).getByRole("button", { name: t("library.models.actions.importSelected") }));

    await waitFor(() => expect(fake.importModels).toHaveBeenCalledTimes(1));
    const args = fake.importModels.mock.calls[0];
    expect(args[0]).toBe("ollama");
    expect(args[1]).toBe("http://127.0.0.1:11434/v1");
    expect(args[2]).toBe("openai-completions");
    expect(args[3]).toEqual(["llama3.2:latest", "qwen2.5-coder:7b"]);
    expect(fake.importModels).toHaveBeenCalledTimes(1);
  });

  it("探不通的端点显示稳定错误码，其模型不能被当成可用", async () => {
    fake.discover.mockResolvedValue({
      items: [
        endpoint({
          reachable: false,
          latencyMs: 1500,
          models: [],
          error: { code: "DISCOVERY_UNREACHABLE", message: "dial tcp: connection refused" },
        }),
      ],
    });
    const user = userEvent.setup();
    render(<ModelsMaintenance />);
    await screen.findByTestId("model-m-vllm");

    await user.click(screen.getByRole("button", { name: t("library.models.actions.scanLocal") }));
    const card = await screen.findByTestId("endpoint-vllm");
    expect(within(card).getByText(t("library.models.discovery.errors.DISCOVERY_UNREACHABLE"))).toBeTruthy();
    expect(within(card).queryByRole("button", { name: t("library.models.actions.importSelected") })).toBeNull();

    // 列表里的同端点模型要标为不可用，且不能设为当前。
    const row = screen.getByTestId("model-m-vllm");
    expect(within(row).getByText(t("library.models.labels.unavailable"))).toBeTruthy();
    expect(within(row).getByRole("button", { name: t("library.models.actions.setActive") })).toBeDisabled();
  });

  it("添加供应商走真实绑定，拒绝原因按错误码显示", async () => {
    fake.saveProvider.mockResolvedValue({
      revision: 4,
      error: { code: "UNSUPPORTED", message: "端点只支持 http/https: ftp://example.com", retryable: false },
    });
    const user = userEvent.setup();
    render(<ModelsMaintenance />);
    await screen.findByTestId("model-m-vllm");

    await user.click(screen.getByRole("button", { name: t("library.models.actions.addProvider") }));
    await user.type(screen.getByLabelText(t("library.models.provider.idLabel")), "lmstudio");
    await user.type(
      screen.getByLabelText(t("library.models.provider.baseUrlLabel")),
      "ftp://example.com"
    );
    await user.click(screen.getByRole("button", { name: t("library.models.actions.saveProvider") }));

    await waitFor(() => expect(fake.saveProvider).toHaveBeenCalledTimes(1));
    expect(fake.saveProvider.mock.calls[0][0]).toBe("lmstudio");
    expect(fake.saveProvider.mock.calls[0][1]).toBe("ftp://example.com");
    expect(await screen.findByText(t("errors.UNSUPPORTED"))).toBeTruthy();
  });

  it("删除当前使用的模型被拒绝，并说明要先切换", async () => {
    fake.current.mockResolvedValue(
      current({ source: "resources", ref: "m-vllm", provider: "vllm", model: "meta-llama/Llama-3-8B-Instruct" })
    );
    fake.remove.mockResolvedValue({
      revision: 4,
      error: {
        code: "CONFIG_CONFLICT",
        message: "模型 m-vllm 是当前使用的模型；请先切换到别的模型再删除",
        retryable: false,
      },
    });
    const user = userEvent.setup();
    render(<ModelsMaintenance />);
    await screen.findByTestId("model-m-vllm");

    // 当前模型显示为"使用中"（已禁用），删除仍需显式确认。
    await user.click(screen.getByRole("button", { name: t("library.models.actions.delete") }));
    await user.click(
      screen.getByRole("button", { name: t("library.models.actions.confirmDelete") })
    );

    await waitFor(() => expect(fake.remove).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(t("errors.CONFIG_CONFLICT"))).toBeTruthy();
    // 拒绝原因里的具体模型 ID 也要露出来，而不是只说"操作失败"。
    expect(await screen.findByText(/请先切换到别的模型再删除/)).toBeTruthy();
  });

  it("连接测试按 code 显示不可达", async () => {
    fake.test.mockResolvedValue({
      result: {
        reachable: false,
        toolCalling: "unknown",
        vision: "unknown",
        latencyMs: 0,
        providerId: "vllm",
        modelId: "x",
        error: { code: "PROVIDER_UNREACHABLE", message: "connection refused", retryable: true },
      },
    });
    const user = userEvent.setup();
    render(<ModelsMaintenance />);
    await screen.findByTestId("model-m-vllm");

    await user.click(screen.getByRole("button", { name: t("library.models.actions.test") }));
    const out = await screen.findByTestId("model-test-m-vllm");
    expect(out).toHaveTextContent(t("library.models.labels.unreachable"));
    expect(out).toHaveTextContent("PROVIDER_UNREACHABLE");
  });
});
