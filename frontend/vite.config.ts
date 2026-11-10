import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The Wails host serves the built files from dist/ through
// application.AssetFileServerFS, so the build is a plain static bundle.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    strictPort: true,
  },
  test: {
    // jsdom + 16 React test files can exhaust the default forked worker pool
    // on this Windows host (SIGABRT 134). Run one file at a time in a single
    // worker so gate runs are deterministic; this does not change any test.
    pool: 'forks',
    fileParallelism: false,
    maxWorkers: 1,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    restoreMocks: true,
  },
})
