import LanguageSettings from '../../i18n/LanguageSettings'
import PagePlaceholder from '../../shell/PagePlaceholder'
import { ROUTES_BY_ID } from '../../shell/routes'
import ThemeSettings from './ThemeSettings'

const settings = ROUTES_BY_ID.get('settings')

export default function SettingsPage() {
  if (!settings) throw new Error('settings route is missing from routes.json')
  return (
    <>
      <ThemeSettings />
      <LanguageSettings />
      <PagePlaceholder route={settings} />
    </>
  )
}
