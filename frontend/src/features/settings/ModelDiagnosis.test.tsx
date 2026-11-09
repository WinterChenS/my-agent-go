// 模型配置诊断（MDIAG）。
//
// 这一块的价值全在"说得清"：保存模型成功、启动却只回「缺少必要的配置」时，
// 用户必须能从界面上看出是哪一步不对。所以这里钉四件事：
//   - 不可用时原因是最醒目的一行（不是埋在列表里的某一项）；
//   - 逐模型状态显示 ok / 跳过：原因，跳过段列出具体条目；
//   - models.json 的路径与是否已生成要说出来（"不写文件"不等于"文件没问题"）；
//   - 可用时不显示跳过段，也不出现任何密钥值。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { setLocale, t } from "../../i18n";

const fake = vi.hoisted(() => ({
  diagnoseModelConfig: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({
  settingsApi: fake,
  onPiInstallProgress: () => () => undefined,
}));

import { ModelDiagnosis } from "./ModelDiagnosis";

function danglingDiagnosis() {
  return {
    selectedRef: "m-cred",
    selectedSource: "resources",
    selected: {
      providerId: "demo",
      modelId: "demo-model",
      baseUrl: "http://127.0.0.1:8080/v1",
      api: "openai-completions",
      credentialRef: "cred-gone",
    },
    models: [
      { modelId: "m-cred", providerId: "demo", status: "skipped", reason: "模型 m-cred 引用的凭证 cred-gone 不可用" },
      { modelId: "m-good", providerId: "demo", status: "ok", reason: "" },
    ],
    compiled: 1,
    skipped: [{ modelId: "m-cred", reason: "模型 m-cred 引用的凭证 cred-gone 不可用" }],
    modelsJsonPath: "C:\\anvil\\agents\\default\\models.json",
    modelsJsonExists: false,
    ready: false,
    reason: "当前模型 m-cred 没有参与编译：模型 m-cred 引用的凭证 cred-gone 不可用",
  };
}

function readyDiagnosis() {
  return {
    ...danglingDiagnosis(),
    selectedRef: "m-good",
    selected: {
      providerId: "demo",
      modelId: "demo-model",
      baseUrl: "http://127.0.0.1:8080/v1",
      api: "openai-completions",
      credentialRef: "",
    },
    models: [{ modelId: "m-good", providerId: "demo", status: "ok", reason: "" }],
    compiled: 1,
    skipped: [],
    modelsJsonExists: true,
    ready: true,
    reason: "",
  };
}

beforeEach(() => {
  fake.diagnoseModelConfig = vi.fn(async () => danglingDiagnosis());
});

afterEach(() => {
  setLocale("zh-CN");
});

describe("ModelDiagnosis", () => {
  it("只在点击后诊断，并把不可用的原因放在最醒目的位置", async () => {
    render(<ModelDiagnosis />);
    // 不挂在挂载上：诊断要读凭证库，不该在打开设置页时静默发生。
    expect(fake.diagnoseModelConfig).not.toHaveBeenCalled();

    await userEvent.click(screen.getByTestId("model-diagnosis-button"));
    expect(fake.diagnoseModelConfig).toHaveBeenCalledTimes(1);

    const reason = await screen.findByTestId("model-diagnosis-reason");
    expect(reason).toHaveAttribute("role", "alert");
    expect(reason.textContent).toContain(t("settings.model.diagnosis.notReady"));
    expect(reason.textContent).toContain("cred-gone");
    expect(screen.queryByTestId("model-diagnosis-ready")).toBeNull();
  });

  it("逐模型状态与跳过段都要显示原因，且不展示原始 JSON", async () => {
    render(<ModelDiagnosis />);
    await userEvent.click(screen.getByTestId("model-diagnosis-button"));

    const skippedModel = await screen.findByTestId("model-diagnosis-model-m-cred");
    expect(skippedModel.textContent).toContain(
      t("settings.model.diagnosis.status.skipped", { reason: "" }).split("{reason}")[0]
    );
    expect(skippedModel.textContent).toContain("cred-gone");

    const okModel = screen.getByTestId("model-diagnosis-model-m-good");
    expect(okModel.textContent).toContain(t("settings.model.diagnosis.status.ok"));

    const skipped = screen.getByTestId("model-diagnosis-skipped");
    expect(within(skipped).getByTestId("model-diagnosis-skip-m-cred").textContent).toContain("cred-gone");

    // models.json 的路径要给出，但整块面板不出现 JSON 正文。
    const path = screen.getByTestId("model-diagnosis-models-json");
    expect(path.textContent).toContain("models.json");
    expect(path.textContent).toContain(t("settings.model.diagnosis.modelsJsonAbsent"));
    expect(document.body.textContent).not.toContain('"providers"');
    // 只给凭证引用，不给密钥。
    expect(document.body.textContent).not.toMatch(/sk-[A-Za-z0-9]/);
  });

  it("选中模型与来源要能看见", async () => {
    render(<ModelDiagnosis />);
    await userEvent.click(screen.getByTestId("model-diagnosis-button"));

    const selected = await screen.findByTestId("model-diagnosis-selected");
    expect(selected.textContent).toContain("m-cred");
    expect(selected.textContent).toContain(t("settings.model.source.resources"));

    const cred = screen.getByTestId("model-diagnosis-field-credentialRef");
    expect(cred.textContent).toContain("cred-gone");
    expect(screen.getByTestId("model-diagnosis-field-baseUrl").textContent).toContain("127.0.0.1:8080");
  });

  it("可用时显示可用结论，且不出现跳过段", async () => {
    fake.diagnoseModelConfig = vi.fn(async () => readyDiagnosis());
    render(<ModelDiagnosis />);
    await userEvent.click(screen.getByTestId("model-diagnosis-button"));

    expect(await screen.findByTestId("model-diagnosis-ready")).toBeInTheDocument();
    expect(screen.queryByTestId("model-diagnosis-reason")).toBeNull();
    expect(screen.queryByTestId("model-diagnosis-skipped")).toBeNull();
    expect(screen.getByTestId("model-diagnosis-model-m-good").textContent).toContain(
      t("settings.model.diagnosis.status.ok")
    );
    expect(screen.getByTestId("model-diagnosis-models-json").textContent).toContain(
      t("settings.model.diagnosis.modelsJsonPresent")
    );
  });

  it("结构化失败时显示诊断失败，不假装配置可用", async () => {
    fake.diagnoseModelConfig = vi.fn(async () => ({
      ...readyDiagnosis(),
      error: { code: "CONFIG_MISSING", message: "config unreadable", retryable: false },
    }));
    render(<ModelDiagnosis />);
    await userEvent.click(screen.getByTestId("model-diagnosis-button"));

    const alert = await screen.findByTestId("model-diagnosis-error");
    expect(alert.textContent).toContain(t("errors.CONFIG_MISSING"));
    expect(screen.queryByTestId("model-diagnosis-ready")).toBeNull();
  });
});
