// 记忆。
//
// 整个功能属于 C05，一行都还没实现。这一页存在的意义只有两个：
// 入口在（五入口之一，用户不会以为它藏在别处），
// 以及把"为什么这里是空的"说清楚。

import { BrainCircuit } from "lucide-react";
import { EmptyState } from "../../components/StateViews";

export function MemoryView() {
  return (
    <div className="mx-auto max-w-3xl px-4 py-5">
      <header className="mb-3">
        <h2 className="text-sm font-medium text-slate-200">记忆</h2>
        <p className="mt-0.5 text-[11px] text-slate-500">
          跨会话保留的上下文。项目之间互相隔离，不会把 A 项目的记忆带进 B 项目。
        </p>
      </header>

      <EmptyState
        title="记忆功能尚未实现"
        hint="存储、检索与项目间隔离属于 C05。现在每一次 run 都是从零开始的上下文，不会记住上一轮说过的话之外的内容。"
        icon={<BrainCircuit className="h-4 w-4" aria-hidden />}
      />
    </div>
  );
}
