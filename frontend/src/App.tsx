import { BrowserRouter, Route, Routes, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { LanguageProvider } from './i18n/LanguageProvider'
import SettingsPage from './pages/settings/SettingsPage'
import WorkbenchPage from './pages/workbench/WorkbenchPage'
import ChatPage from './pages/chat/ChatPage'
import AgentsPage from './pages/agents/AgentsPage'
import SkillsPage from './pages/skills/SkillsPage'
import McpPage from './pages/mcp/McpPage'
import ConnectorsPage from './pages/connectors/ConnectorsPage'
import SchedulesPage from './pages/schedules/SchedulesPage'
import LibraryPage from './pages/library/LibraryPage'
import { ThemeProvider } from './preferences/ThemeProvider'
import OnboardingDialog from './onboarding/OnboardingDialog'
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
  return sample === 'chat' ? <ChatPage /> : sample === 'agents' ? <AgentsPage /> : sample === 'skills' ? <SkillsPage /> : sample === 'mcp' ? <McpPage /> : sample === 'connectors' ? <ConnectorsPage /> : sample === 'schedules' ? <SchedulesPage /> : sample === 'library' ? <LibraryPage /> : sample === 'settings' ? <SettingsPage /> : <WorkbenchPage />
}

/** Theme + language, the two providers every screen sits inside. */
function AppProviders({ children }: { children: ReactNode }) {
  return (
    <ThemeProvider>
      <LanguageProvider>{children}</LanguageProvider>
    </ThemeProvider>
  )
}

function RoutesTree() {
  return (
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
              ) : route.id === 'mcp' ? (
                <McpPage />
              ) : route.id === 'connectors' ? (
                <ConnectorsPage />
              ) : route.id === 'schedules' ? (
                <SchedulesPage />
              ) : route.id === 'library' ? (
                <LibraryPage />
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
        <Route path="/__sample/mcp" element={<McpPage />} />
        <Route path="/__sample/connectors" element={<ConnectorsPage />} />
        <Route path="/__sample/schedules" element={<SchedulesPage />} />
        <Route path="/__sample/library" element={<LibraryPage />} />
        <Route path="/__sample/settings" element={<SettingsPage />} />
        <Route path="*" element={<FallbackPage />} />
      </Route>
    </Routes>
  )
}

/** Routes with their providers — what tests and pages mount. */
export function AppRoutes() {
  return (
    <AppProviders>
      <RoutesTree />
    </AppProviders>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      {/*
        The wizard lives beside the routes, inside the same providers: it offers
        a theme switch, so it must share the app's ThemeProvider instead of
        starting a second one. Keeping it out of `AppRoutes` also keeps it out
        of the pages' test tree, where it has nothing to say.
      */}
      <AppProviders>
        <OnboardingDialog />
        <RoutesTree />
      </AppProviders>
    </BrowserRouter>
  )
}
