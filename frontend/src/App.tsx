// R0-00 only delivers a launchable shell. The nine prototype pages, theme and
// i18n arrive in R0-01..R0-12; everything here is placeholder empty state.
export default function App() {
  return (
    <div className="anvil-shell" data-testid="anvil-shell">
      <header className="anvil-header">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            A
          </span>
          <div className="brand-text">
            <h1>Anvil</h1>
            <p className="tagline">本地 Agent 工作台</p>
          </div>
        </div>
        <span className="stage-badge">阶段 0 · 骨架</span>
      </header>

      <main className="anvil-main">
        <section className="empty-state" aria-labelledby="empty-title">
          <h2 id="empty-title">界面骨架已启动</h2>
          <p>
            Task R0-00 只交付可启动的桌面窗口。九页原型、主题与国际化由后续任务实现。
          </p>
          <p className="note">当前页面为占位空态，所有业务操作尚未接入。</p>
        </section>
      </main>

      <footer className="anvil-footer">
        <span>R0-00 · win-x64</span>
        <span className="build-marker" data-testid="build-marker">
          anvil-rebuild-shell
        </span>
      </footer>
    </div>
  )
}
