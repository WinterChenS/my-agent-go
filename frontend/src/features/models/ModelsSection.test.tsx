// 模型列表测试。
//
// 守一件事：旧格式条目（C01 的 {provider, baseUrl, model}）不能被当成
// "上下文 0 · 输出 0" 展示，也不能被设为当前——它既编译不出合法的
// models.json，也说不清自己的上下文窗口有多大。把不知道的数字显示成 0，
// 比不显示更容易让人当真。

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ModelsSection } from "./ModelsSection";
import type { ModelDefinition, ModelOptions } from "../../types";

vi.mock("./api", () => ({
  modelsApi: {
    test: vi.fn(async () => ({ result: null, error: null })),
    setActive: vi.fn(async () => ({ ref: "m-demo", deferred: false, error: null })),
    remove: vi.fn(async () => ({ revision: 2, error: null })),
    save: vi.fn(async () => ({ revision: 2, error: null })),
  },
}));

const options: ModelOptions = {
  apis: ["openai-completions"],
  inputKinds: ["text"],
  thinkingLevels: ["off", "low"],
};

const modern: ModelDefinition = {
  id: "m-deepseek",
  providerId: "deepseek",
  modelId: "deepseek-chat",
  api: "openai-completions",
  baseUrl: "http://127.0.0.1:8080/v1",
  inputKinds: ["text"],
  contextWindow: 128000,
  maxOutput: 8192,
  credentialRef: "cred-deepseek",
  thinkingLevels: null,
};

// C01 期间存下来的形状：只有 provider/baseUrl/model。
const legacy = {
  id: "m-old",
  providerId: "",
  modelId: "",
  api: "",
  baseUrl: "http://127.0.0.1:8080/v1",
  inputKinds: [],
  contextWindow: 0,
  maxOutput: 0,
  credentialRef: "",
  thinkingLevels: null,
} as ModelDefinition;

function renderSection(models: ModelDefinition[]) {
  return render(
    <ModelsSection
      models={models}
      options={options}
      revision={1}
      currentRef=""
      currentSource="none"
      currentProvider=""
      currentModelName=""
      onChanged={() => {}}
    />,
  );
}

describe("ModelsSection", () => {
  it("正常条目显示端点与容量", () => {
    renderSection([modern]);
    expect(screen.getByText(/deepseek\/deepseek-chat/)).toBeTruthy();
    expect(screen.getByText(/上下文 128000 · 输出 8192 · 凭证 cred-deepseek/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "设为当前" })).toBeEnabled();
  });

  it("旧格式条目标注需重建，且不能设为当前", () => {
    renderSection([legacy]);
    expect(screen.getByText("旧格式")).toBeTruthy();
    expect(screen.queryByText(/上下文 0/)).toBeNull();
    expect(screen.getByText(/缺 provider 名与模型名/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "设为当前" })).toBeDisabled();
  });
});
