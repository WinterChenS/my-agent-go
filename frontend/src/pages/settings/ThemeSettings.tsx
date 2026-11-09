import { useRef, type KeyboardEvent } from 'react'
import { useTheme } from '../../preferences/ThemeProvider'
import type { ThemeMode } from '../../preferences/theme'
import './settings.css'

const OPTIONS: Array<{ value: ThemeMode; label: string; hint: string }> = [
  { value: 'system', label: '跟随系统', hint: '跟随 Windows 外观' },
  { value: 'light', label: '浅色', hint: '始终使用浅色' },
  { value: 'dark', label: '深色', hint: '始终使用深色' },
]

export default function ThemeSettings() {
  const { mode, resolved, notice, setMode } = useTheme()
  const buttons = useRef<Array<HTMLInputElement | null>>([])

  const selectAt = (index: number) => {
    const option = OPTIONS[(index + OPTIONS.length) % OPTIONS.length]
    setMode(option.value)
    buttons.current[OPTIONS.indexOf(option)]?.focus()
  }

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const index = OPTIONS.findIndex((option) => option.value === mode)
    switch (event.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        event.preventDefault()
        selectAt(index + 1)
        break
      case 'ArrowLeft':
      case 'ArrowUp':
        event.preventDefault()
        selectAt(index - 1)
        break
      case 'Home':
        event.preventDefault()
        selectAt(0)
        break
      case 'End':
        event.preventDefault()
        selectAt(OPTIONS.length - 1)
        break
      default:
        break
    }
  }

  return (
    <section className="theme-settings" aria-labelledby="theme-settings-title">
      <div className="card">
        <div className="card-head">
          <h3 id="theme-settings-title">主题</h3>
          <span className="card-head-sub">浅色、深色或跟随 Windows 外观</span>
        </div>
        <div className="card-pad">
          <div className="theme-options" role="radiogroup" aria-label="主题" onKeyDown={onKeyDown}>
            {OPTIONS.map((option, index) => (
              <label
                key={option.value}
                className={`theme-option${mode === option.value ? ' is-active' : ''}`}
              >
                <input
                  ref={(element) => {
                    buttons.current[index] = element
                  }}
                  type="radio"
                  name="anvil-theme"
                  value={option.value}
                  checked={mode === option.value}
                  aria-checked={mode === option.value}
                  aria-label={option.label}
                  tabIndex={mode === option.value ? 0 : -1}
                  onChange={() => setMode(option.value)}
                />
                <span>
                  <b>{option.label}</b>
                  <small>{option.hint}</small>
                </span>
              </label>
            ))}
          </div>
          <p className="theme-status" data-testid="theme-status" role="status">
            当前主题：{resolved === 'dark' ? '深色' : '浅色'}（
            {mode === 'system' ? '跟随系统' : mode === 'dark' ? '深色' : '浅色'}）
          </p>
          {notice ? (
            <p className="theme-notice" role="status">
              {notice}
            </p>
          ) : null}
        </div>
      </div>
    </section>
  )
}
