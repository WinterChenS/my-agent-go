// 模型编辑器测试。
//
// 守三条不能退让的规则：
//  1. API 下拉必须来自后端白名单（前端不能自己发明枚举）；
//  2. 连接测试会真实计费，必须只在点击时发生，并先说明；
//  3. 密钥是独立入参，不进入 ModelDefinition（那份结构会被导出与展示）。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ModelEditor } from "./ModelEditor";
import type { ModelDefinition, ModelOptions } from "../../types";

const options: ModelOptions = {
  apis: ["openai-completions", "anthropic-messages"],
  inputKinds: ["text", "image"],
  thinkingLevels: ["off", "low", "high"],
};

const existing: ModelDefinition = {
  id: "m-demo",
  providerId: "demo",
  modelId: "demo-model",
  api: "openai-completions",
  baseUrl: "http://127.0.0.1:8080/v1",
  inputKinds: ["text"],
  contextWindow: 128000,
  maxOutput: 8192,
  credentialRef: "cred-demo",
  thinkingLevels: null,
};

vi.mock("./api", () => ({
  modelsApi: {
    save: vi.fn(async () => ({ item: existing, revision: 2, error: null })),
    test: vi.fn(async () => ({
      result: {
        reachable: true,
        toolCalling: "supported",
        vision: "unknown",
        latencyMs: 321,
        providerId: "demo",
        modelId: "demo-model",
      },
      error: null,
    })),
  },
}));

function renderEditor(model: ModelDefinition | null, onSaved = () => {}) {
  return render(
    <ModelEditor
      model={model}
      options={options}
      revision={1}
      onSaved={onSaved}
      onClose={() => {}}
    />
  );
}

describe("ModelEditor", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("API 下拉来自后端白名单", async () => {
    renderEditor(null);
    const select = screen.getByLabelText("API 类型") as HTMLSelectElement;
    const values = Array.from(select.options).map((o) => o.value);
    expect(values).toEqual(options.apis);
    expect(values).not.toContain("openai-chat");
  });

  it("未保存时点测试连接只提示先保存，不发请求", async () => {
    const { modelsApi } = await import("./api");
    renderEditor(null);

    await userEvent.setup().click(screen.getByRole("button", { name: /测试连接/ }));

    expect(modelsApi.test).not.toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent("请先保存");
  });

  it("测试连接按钮旁明确写出可能计费", () => {
    renderEditor(existing);
    expect(screen.getByText(/可能产生费用/)).toBeInTheDocument();
    // 没有点击就不该有任何真实调用。
    expect(screen.queryByText("可达")).not.toBeInTheDocument();
  });

  it("401 被展示为鉴权失败，而不是笼统的连接失败", async () => {
    const { modelsApi } = await import("./api");
    vi.mocked(modelsApi.test).mockResolvedValueOnce({
      result: {
        reachable: false,
        toolCalling: "unknown",
        vision: "unknown",
        latencyMs: 0,
        providerId: "demo",
        modelId: "demo-model",
        error: { code: "UNAUTHORIZED", message: "鉴权失败：请检查凭证是否正确或已过期", retryable: false },
      },
    });

    renderEditor(existing);
    // existing 已带凭证引用：测试前不需要重新输入密钥。
    await userEvent.setup().click(screen.getByRole("button", { name: /测试连接/ }));

    await waitFor(() => expect(screen.getByText("UNAUTHORIZED")).toBeInTheDocument());
    expect(screen.getByText(/鉴权失败/)).toBeInTheDocument();
  });

  it("可达时展示延迟，未测的视觉能力显示未测", async () => {
    renderEditor(existing);
    await userEvent.setup().click(screen.getByRole("button", { name: /测试连接/ }));

    await waitFor(() => expect(screen.getByText("可达")).toBeInTheDocument());
    expect(screen.getByText(/321 ms/)).toBeInTheDocument();
    expect(screen.getByText(/工具调用：支持/)).toBeInTheDocument();
    // 视觉没测过：必须显示"未测"，不能显示"支持"。
    expect(screen.getByText(/视觉：未测/)).toBeInTheDocument();
  });

  it("保存时密钥是独立入参，模型定义里没有密钥字段", async () => {
    const { modelsApi } = await import("./api");
    const onSaved = vi.fn();
    renderEditor(null, onSaved);

    const user = userEvent.setup();
    await user.type(screen.getByLabelText("模型 ID"), "m-new");
    await user.type(screen.getByLabelText("Provider 名"), "newprov");
    await user.type(screen.getByLabelText("模型名（发给 API）"), "new-model");
    await user.type(screen.getByLabelText("Base URL"), "https://api.example.com/v1");
    await user.type(screen.getByLabelText("凭证引用"), "cred-new");
    await user.type(screen.getByLabelText("API Key"), "sk-sentinel-123");

    await user.click(screen.getByRole("button", { name: /保存/ }));

    await waitFor(() => expect(modelsApi.save).toHaveBeenCalledTimes(1));
    const [revision, def, secret] = vi.mocked(modelsApi.save).mock.calls[0];
    expect(revision).toBe(1);
    expect(secret).toBe("sk-sentinel-123");
    expect(def.id).toBe("m-new");
    expect(def.credentialRef).toBe("cred-new");
    // 结构上就不能有放密钥的位置。
    expect(Object.keys(def)).not.toContain("apiKey");
    expect(JSON.stringify(def)).not.toContain("sk-sentinel-123");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
  });
});
