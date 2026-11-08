// 故意违规夹具：直接写颜色值与 Tailwind 调色板类名（direct-color）。
// 主题切换时这些值不会跟着变，浅色主题下必然对比度失控（THEME01）。
export function DangerCard({ label }: { label: string }) {
  return (
    <div className="rounded border border-white/10 bg-[#0f141b] text-slate-200">
      <span style={{ color: "#ff0000" }}>{label}</span>
      <span className="text-sky-400">accent</span>
    </div>
  );
}
