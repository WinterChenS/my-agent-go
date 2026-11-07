// 壳层的共享上下文（U01）。
//
// 为什么用 context 而不是一路透传 props：路由组件由 navigation.ts 静态装配，
// 它们拿不到 App 的局部变量。把"当前后端快照 + 当前会话动作 + 导航"收进一个
// 只读上下文，页面就能只关心自己要渲染什么。
//
// 纪律：这里只放**从后端读到的值**与**转发给后端的动作**。任何"点了就算成功"
// 的状态都不得进入这个上下文——那种状态属于后端事件（比如 run 结算）。

import { createContext, useContext, type ReactNode } from "react";
import type {
  ApprovalChoice,
  ApprovalItem,
  CallError,
  CurrentModelResult,
  Extension,
  ModelDefinition,
  ModelOptions,
  Profile,
  Settings,
  SkillResource,
  Status,
  Workspace,
} from "../types";
import type { UiState } from "../runState";
import type { BackendState } from "./useBackend";

export interface ShellValue {
  /** 后端公开的快照（工作区/Profile/模型/技能/扩展）。 */
  backend: BackendState;
  status: Status | null;
  settings: Settings | null;
  workspaces: Workspace[];
  currentWorkspace: Workspace | null;
  profiles: Profile[];
  models: ModelDefinition[];
  modelOptions: ModelOptions;
  modelRevision: number;
  currentModel: CurrentModelResult | null;
  skills: SkillResource[];
  skillRevision: number;
  skillsPending: boolean;
  skillsActiveRun: boolean;
  extensions: Extension[];
  extensionRevision: number;

  /** 当前会话的界面状态（由 run 事件推进，不由点击推进）。 */
  ui: UiState;
  input: string;
  onInput: (value: string) => void;
  onSend: () => void;
  onStop: () => void;
  onDecide: (item: ApprovalItem, choice: ApprovalChoice) => void;
  busy: boolean;

  savingSettings: boolean;
  actionError: CallError | null;
  saveSettings(settings: Settings): Promise<void>;
  trustWorkspace(id: string, trusted: boolean): Promise<void>;
  addWorkspace(path: string): Promise<void>;
  refreshProfiles(): void;
  refreshModels(): void;
  refreshSkills(): void;

  /** 新建一个普通会话并导航到它（失败时返回结构化错误）。 */
  onNewSession(): Promise<void>;
}

const ShellContext = createContext<ShellValue | null>(null);

export function ShellProvider({ value, children }: { value: ShellValue; children: ReactNode }) {
  return <ShellContext.Provider value={value}>{children}</ShellContext.Provider>;
}

/**
 * 取壳层上下文。
 *
 * 缺 Provider 时直接抛错，而不是返回一份空数据：空数据会让页面显示成
 * "什么都没有"，那是最难查的一类 bug（代码明明接上了，界面永远空）。
 */
export function useShell(): ShellValue {
  const value = useContext(ShellContext);
  if (!value) {
    throw new Error("useShell must be used inside ShellProvider");
  }
  return value;
}
