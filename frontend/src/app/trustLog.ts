// 信任动作的界面观察记录（WTRUST）。
//
// 为什么要这一层：设置页要列出"信任时间"，但后端 Workspace 只有 createdAt，
// 没有 trustedAt（要真的落库得改 internal/workspace 的域模型，不在本任务
// 的文件范围内）。所以这里记的**只是界面观察到这次信任动作的时刻**，
// 不冒充后端事实，界面上也把两种情况分开写：
//   有记录 → "信任时间：<时间>"（本次界面里点过信任）
//   没记录 → "信任时间：本次界面未记录（该项在界面启动前已信任）"
//
// 只存不推断：撤销时把记录删掉，不保留"上次信任于"这种会误导的残留。

const trustTimes = new Map<string, string>();

/** 记录一次信任动作（`trusted=false` 即撤销，同时清掉记录）。 */
export function recordWorkspaceTrust(id: string, trusted: boolean, at: Date = new Date()): void {
  if (!id) return;
  if (!trusted) {
    trustTimes.delete(id);
    return;
  }
  trustTimes.set(id, at.toISOString());
}

/** 本次界面里观察到的工作区信任时间（ISO）；没有记录时返回空串。 */
export function workspaceTrustTime(id: string): string {
  return trustTimes.get(id) ?? "";
}

/** 仅供测试：清空记录，避免用例之间互相污染。 */
export function resetWorkspaceTrustTimes(): void {
  trustTimes.clear();
}
