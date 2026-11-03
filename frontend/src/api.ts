// 前端到 Go 的唯一通道（Wails v3 绑定调用）。
//
// 绑定名格式是 <Go package path>.<Type>.<Method>：Wails 用 package path 做 FQN，
// 所以名字里带斜杠。集中在这里定义，避免散落各处的字符串拼错。
import { Call, Events } from "@wailsio/runtime";
import type { ProductEvent, RunSnapshot, Settings, Status } from "./types";

const FQN = "my-agent-go/internal/app.Desktop";

export const api = {
  status: (): Promise<Status> => Call.ByName(`${FQN}.Status`),

  settings: (): Promise<Settings> => Call.ByName(`${FQN}.Settings`),

  saveSettings: (s: Settings): Promise<void> =>
    Call.ByName(`${FQN}.SaveSettings`, s),

  start: (message: string): Promise<RunSnapshot> =>
    Call.ByName(`${FQN}.Start`, message),

  abort: (runId: string): Promise<void> => Call.ByName(`${FQN}.Abort`, runId),

  resolveApproval: (
    runId: string,
    approvalId: string,
    choice: string
  ): Promise<void> =>
    Call.ByName(`${FQN}.ResolveApproval`, runId, approvalId, choice),

  lastError: (): Promise<string> => Call.ByName(`${FQN}.LastError`),
};

/** 订阅 run 事件。返回取消订阅函数。 */
export function onRunEvent(fn: (ev: ProductEvent) => void): () => void {
  return Events.On("myagent:run-event", (event: { data: ProductEvent }) => {
    fn(event.data);
  });
}
