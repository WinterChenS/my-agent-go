import { BrowserRouter, Route, Routes, useLocation } from 'react-router-dom'
import { LanguageProvider } from './i18n/LanguageProvider'
import SettingsPage from './pages/settings/SettingsPage'
import WorkbenchPage from './pages/workbench/WorkbenchPage'
import ChatPage from './pages/chat/ChatPage'
import AgentsPage from './pages/agents/AgentsPage'
import SkillsPage from './pages/skills/SkillsPage'
import { ThemeProvider } from './preferences/ThemeProvider'
import PagePlaceholder from './shell/PagePlaceholder'
import Shell from './shell/Shell'
import { SHELL_ROUTES, WORKBENCH_ROUTE } from './shell/routes'

function FallbackPage() {
  const { pathname } = useLocation()
  return <PagePlaceholder route={WORKBENCH_ROUTE} fallbackPath={pathname} />
}

function RootPage() {
  const { search } = useLocation()
  const sample = new URLSearchParams(search).get('sample')
  return sample === 'chat' ? <ChatPage /> : sample === 'agents' ? <AgentsPage /> : sample === 'skills' ? <SkillsPage /> : <WorkbenchPage />
}

export function AppRoutes() {
  return (
    <ThemeProvider>
      <LanguageProvider>
        <Routes>
          <Route element={<Shell />}>
            {SHELL_ROUTES.map((route) => (
              <Route
                key={route.path}
                path={route.path}
                element={
                  route.id === 'settings' ? (
                    <SettingsPage />
                  ) : route.id === 'workbench' ? (
                    <RootPage />
                  ) : route.id === 'chat' ? (
                    <ChatPage />
                  ) : route.id === 'agents' ? (
                    <AgentsPage />
                  ) : route.id === 'skills' ? (
                    <SkillsPage />
                  ) : (
                    <PagePlaceholder route={route} />
                  )
                }
              />
            ))}
            <Route path="/__sample/workbench" element={<WorkbenchPage />} />
            <Route path="/__sample/chat" element={<ChatPage />} />
            <Route path="/__sample/agents" element={<AgentsPage />} />
            <Route path="/__sample/skills" element={<SkillsPage />} />
            <Route path="*" element={<FallbackPage />} />
          </Route>
        </Routes>
      </LanguageProvider>
    </ThemeProvider>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AppRoutes />
    </BrowserRouter>
  )
}
