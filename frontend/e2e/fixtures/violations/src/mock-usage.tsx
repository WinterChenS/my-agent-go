// 故意违规夹具：生产组件直接引用演示数据模块（mock-import）。
import { demoRuns } from "./mock/demo-runs";

export function DemoRunsBadge() {
  return <span>{demoRuns.length} runs</span>;
}
