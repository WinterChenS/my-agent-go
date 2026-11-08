// 故意违规夹具：把产品指标写死在源码里（hardcoded-metric）。
const STATS = { runsToday: 1284, successRate: "98%" };

export function StatsStrip() {
  return (
    <div>
      <span>1,284 runs today</span>
      <span>98% success</span>
      <span>12 次运行</span>
      <span>{STATS.runsToday} runs</span>
    </div>
  );
}
