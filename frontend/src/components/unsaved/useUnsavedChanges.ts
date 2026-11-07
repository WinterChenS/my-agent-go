// 未保存改动的上报钩子（U00，F09 消费）。
//
// 用法：编辑器把 `dirty` 传进来，钩子在变化时上报、卸载时清除。
//
// 为什么"卸载时清除"必须是默认行为：页面切换不会触发任何保存动作，
// 如果只在 dirty=false 时清除，切走再切回来之前宿主会一直以为还有草稿，
// F09 的"退出并更新"就会被一个已经消失的编辑器永久阻塞。

import { useEffect } from "react";
import { api } from "../../api";

export function useUnsavedChanges(scope: string, dirty: boolean): void {
  useEffect(() => {
    if (!scope) return;
    // 上报失败只记日志：这是"声明"而不是保存，不该因为宿主还没就绪就打断编辑。
    const report = (nextDirty: boolean) => {
      void api.reportUnsavedChanges(scope, nextDirty).catch((e: unknown) => {
        console.warn("上报未保存改动失败", scope, e); // ui-copy: dev-only
      });
    };
    report(dirty);
    return () => {
      // 卸载一律清除：见文件头注释。
      report(false);
    };
  }, [scope, dirty]);
}
