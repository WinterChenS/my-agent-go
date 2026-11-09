import { BrowserRouter, Route, Routes, useLocation } from 'react-router-dom'
import { ThemeProvider } from './preferences/ThemeProvider'
import SettingsPage from './pages/settings/SettingsPage'
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
