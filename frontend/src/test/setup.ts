import '@testing-library/jest-dom/vitest'

// The UI language follows the operating system, and jsdom reports en-US. These
// suites describe a Chinese machine (every page assertion is written in Chinese),
// so the locale is pinned here; the tests that are about locale detection stub
// `navigator` themselves.
Object.defineProperty(window.navigator, 'languages', {
  value: ['zh-CN', 'zh'],
  configurable: true,
})
Object.defineProperty(window.navigator, 'language', {
  value: 'zh-CN',
  configurable: true,
})
