import { BrowserRouter, Route, Routes, useLocation } from 'react-router-dom'
import { LanguageProvider } from './i18n/LanguageProvider'
import SettingsPage from './pages/settings/SettingsPage'
import { ThemeProvider } from './preferences/ThemeProvider'
import PagePlaceholder from './shell/PagePlaceholder'
import Shell from './shell/Shell'
import { SHELL_ROUTES, WORKBENCH_ROUTE } from './shell/routes'

function FallbackPage() {
  const { pathname } = useLocation()
  return <PagePlaceholder route={WORKBENCH_ROUTE} fallbackPath={pathname} />
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
                element={route.id === 'settings' ? <SettingsPage /> : <PagePlaceholder route={route} />}
              />
            ))}
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
