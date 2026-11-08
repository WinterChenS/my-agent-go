// 模型编辑器：定义 + 凭证 + 连接测试。
//
// 两条不能妥协的规则：
//  1. 密钥是**独立入参**，不放进 ModelDefinition。模型定义会被导出、被贴进
//     issue、被日志打印，它里面不能有放密钥的位置。
//  2. 连接测试会真实调用模型（可能计费），只在用户点击时发生，
//     并且界面必须先把这句话说出来。

import { useState } from "react";
import { PlugZap, Save, X } from "lucide-react";
import { api } from "../../api";
import type {
  CallError,
  ModelDefinition,
  ModelOptions,
  ModelProbeResponse,
} from "../../types";
import { toCallError } from "../../app/useBackend";

export interface ModelEditorProps {
  /** null 表示新建。 */
  model: ModelDefinition | null;
  options: ModelOptions;
  /** 模型配置文档的 revision（CAS 用）。 */
  revision: number;
  onSaved: () => void;
  onClose: () => void;
}

interface FormState {
  id: string;
  providerId: string;
  modelId: string;
  api: string;
  baseUrl: string;
  contextWindow: string;
  maxOutput: string;
  credentialRef: string;
  inputKinds: string[];
  thinkingLevels: string[];
}

function initialForm(model: ModelDefinition | null, options: ModelOptions): FormState {
  // 旧格式条目（C01 的 {provider, baseUrl, model}）读进来时 contextWindow/maxOutput
  // 是 0。这里按"新建"的默认值预填，而不是把 0 原样带进表单——0 会被校验拒绝，
  // 用户看到的却是一个自己没填过的数字。
  const positive = (v: number | undefined, fallback: number) => (v && v > 0 ? v : fallback);
  return {
    id: model?.id ?? "",
    providerId: model?.providerId ?? "",
    modelId: model?.modelId ?? "",
    api: model?.api ?? options.apis[0] ?? "",
    baseUrl: model?.baseUrl ?? "",
    contextWindow: String(positive(model?.contextWindow, 128000)),
    maxOutput: String(positive(model?.maxOutput, 8192)),
    credentialRef: model?.credentialRef ?? "",
    inputKinds: model?.inputKinds?.length ? model.inputKinds : ["text"],
    thinkingLevels: model?.thinkingLevels ?? [],
  };
}

const CAPABILITY_LABEL: Record<string, string> = {
  unknown: "未测",
  supported: "支持",
  unsupported: "不支持",
};

export function ModelEditor({ model, options, revision, onSaved, onClose }: ModelEditorProps) {
  const [form, setForm] = useState<FormState>(() => initialForm(model, options));
  const [secret, setSecret] = useState("");
  const [error, setError] = useState<CallError | null>(null);
  const [saving, setSaving] = useState(false);
  const [probing, setProbing] = useState(false);
  const [probe, setProbe] = useState<ModelProbeResponse | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }));

  const toggle = (key: "inputKinds" | "thinkingLevels", value: string) =>
    setForm((prev) => {
      const cur = prev[key];
      const next = cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value];
      return { ...prev, [key]: next };
    });

  const buildDefinition = (): ModelDefinition => ({
    id: form.id.trim(),
    providerId: form.providerId.trim(),
    modelId: form.modelId.trim(),
    api: form.api,
    baseUrl: form.baseUrl.trim(),
    inputKinds: form.inputKinds.length > 0 ? form.inputKinds : ["text"],
    contextWindow: Number(form.contextWindow),
    maxOutput: Number(form.maxOutput),
    credentialRef: form.credentialRef.trim(),
    thinkingLevels: form.thinkingLevels,
    // 编辑时保留原有的启用状态；新建默认启用（省略字段）。
    enabled: model?.enabled ?? undefined,
  });

  async function save() {
    setSaving(true);
    setError(null);
    setNotice(null);
    try {
      const res = await api.saveModel(revision, buildDefinition(), secret);
      if (res.error) {
        setError(res.error);
        return;
      }
      setSecret("");
      onSaved();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setSaving(false);
    }
  }

  async function probeNow() {
    // 未保存的模型没有 ID 可测；先保存再测，避免"测的是屏幕上还没落盘的那份"。
    if (!model) {
      setNotice("请先保存，再测试连接。测试用的是已保存的配置。");
      return;
    }
    setProbing(true);
    setProbe(null);
    setNotice(null);
    setError(null);
    try {
      const res = await api.testModel(model.id);
      setProbe(res);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setProbing(false);
    }
  }

  const probeError = probe?.result?.error ?? probe?.error ?? null;

  return (
    <div className="space-y-3">
      {model !== null && (!model.providerId || !model.modelId) && (
        <p className="rounded border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2 py-1 text-[10px] text-[color:var(--anvil-warning)]">
          这是旧版本保存的形状（只有 provider/baseUrl/model）。请补齐 Provider 名与模型名后保存，保存后才会被编译进
          models.json。
        </p>
      )}
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <Field label="模型 ID" hint="唯一标识，小写字母/数字/.-_">
          <input
            aria-label="模型 ID"
            value={form.id}
            onChange={(e) => set("id", e.target.value)}
            disabled={model !== null}
            placeholder="m-demo"
            className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] disabled:opacity-60 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
          />
        </Field>
        <Field label="Provider 名" hint="models.json 里的键名">
          <input
            aria-label="Provider 名"
            value={form.providerId}
            onChange={(e) => set("providerId", e.target.value)}
            placeholder="demo"
            className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
          />
        </Field>
        <Field label="模型名（发给 API）" hint="例如 deepseek-chat">
          <input
            aria-label="模型名"
            value={form.modelId}
            onChange={(e) => set("modelId", e.target.value)}
            placeholder="demo-model"
            className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
          />
        </Field>
        <Field label="API 类型" hint="来自后端白名单（已认证 Pi 的分发能力）">
          <select
            aria-label="API 类型"
            value={form.api}
            onChange={(e) => set("api", e.target.value)}
            className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            {options.apis.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Base URL" hint="OpenAI 兼容端点，http/https">
          <input
            aria-label="Base URL"
            value={form.baseUrl}
            onChange={(e) => set("baseUrl", e.target.value)}
            placeholder="https://api.example.com/v1"
            className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
          />
        </Field>
        <div className="grid grid-cols-2 gap-2">
          <Field label="上下文窗口">
            <input
              aria-label="上下文窗口"
              value={form.contextWindow}
              onChange={(e) => set("contextWindow", e.target.value)}
              inputMode="numeric"
              className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
            />
          </Field>
          <Field label="最大输出">
            <input
              aria-label="最大输出"
              value={form.maxOutput}
              onChange={(e) => set("maxOutput", e.target.value)}
              inputMode="numeric"
              className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
            />
          </Field>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <Field label="凭证引用" hint="密钥存在 OS 凭证库，这里只存引用名">
          <input
            aria-label="凭证引用"
            value={form.credentialRef}
            onChange={(e) => set("credentialRef", e.target.value)}
            placeholder="cred-demo"
            className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
          />
        </Field>
        <Field label="API Key" hint="只写不读；留空表示不修改已保存的密钥">
          <input
            aria-label="API Key"
            type="password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            placeholder={model?.credentialRef ? "已保存（不回显）" : "输入后随保存写入凭证库"}
            className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-[color:var(--anvil-focus)]"
          />
        </Field>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <fieldset className="flex items-center gap-2">
          <legend className="sr-only">输入类型</legend>
          <span className="text-[11px] text-[color:var(--anvil-muted)]">输入</span>
          {options.inputKinds.map((kind) => (
            <label key={kind} className="flex items-center gap-1 text-[11px] text-[color:var(--anvil-foreground)]">
              <input
                type="checkbox"
                checked={form.inputKinds.includes(kind)}
                onChange={() => toggle("inputKinds", kind)}
                className="h-3 w-3 accent-sky-500"
              />
              {kind}
            </label>
          ))}
        </fieldset>
        <fieldset className="flex flex-wrap items-center gap-2">
          <legend className="sr-only">思考等级</legend>
          <span className="text-[11px] text-[color:var(--anvil-muted)]">思考</span>
          {options.thinkingLevels.map((lv) => (
            <label key={lv} className="flex items-center gap-1 text-[11px] text-[color:var(--anvil-foreground)]">
              <input
                type="checkbox"
                checked={form.thinkingLevels.includes(lv)}
                onChange={() => toggle("thinkingLevels", lv)}
                className="h-3 w-3 accent-sky-500"
              />
              {lv}
            </label>
          ))}
        </fieldset>
      </div>

      {error && (
        <p role="alert" className="text-[11px] text-[color:var(--anvil-danger)]">
          <span className="font-mono text-[10px]">{error.code}</span> {error.message}
        </p>
      )}
      {notice && (
        <p role="status" className="text-[11px] text-[color:var(--anvil-warning)]">
          {notice}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button
          onClick={() => void save()}
          disabled={saving}
          className="flex items-center gap-1 rounded-md bg-[color:var(--anvil-accent)] px-2.5 py-1 text-[11px] font-medium text-[color:var(--anvil-background)] hover:bg-[color:var(--anvil-accent)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <Save className="h-3 w-3" aria-hidden /> {saving ? "保存中…" : "保存"}
        </button>
        <button
          onClick={() => void probeNow()}
          disabled={probing}
          className="flex items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <PlugZap className="h-3 w-3" aria-hidden /> {probing ? "测试中…" : "测试连接"}
        </button>
        <button
          onClick={onClose}
          className="flex items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <X className="h-3 w-3" aria-hidden /> 收起
        </button>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">
          连接测试会真实调用该模型，可能产生费用；只有点击时才会发送请求。
        </span>
      </div>

      {probe && (
        <div
          role="status"
          className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2.5 py-2 text-[11px] text-[color:var(--anvil-foreground)]"
        >
          {probeError ? (
            <div className="text-[color:var(--anvil-danger)]">
              <span className="font-mono text-[10px]">{probeError.code}</span>{" "}
              {probeError.message}
              {probeError.retryable && (
                <span className="ml-1 text-[color:var(--anvil-muted)]">（可重试）</span>
              )}
            </div>
          ) : (
            <div className="flex flex-wrap items-center gap-3">
              <span className="text-[color:var(--anvil-success)]">可达</span>
              <span className="text-[color:var(--anvil-muted)]">{probe.result.latencyMs} ms</span>
              <span>
                工具调用：{CAPABILITY_LABEL[probe.result.toolCalling] ?? probe.result.toolCalling}
              </span>
              <span>
                视觉：{CAPABILITY_LABEL[probe.result.vision] ?? probe.result.vision}
              </span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="mb-0.5 flex items-baseline gap-1">
        <span className="text-[11px] text-[color:var(--anvil-muted)]">{label}</span>
        {hint && <span className="truncate text-[9px] text-[color:var(--anvil-muted)]">{hint}</span>}
      </span>
      {children}
    </label>
  );
}
