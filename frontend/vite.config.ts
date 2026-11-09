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
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    restoreMocks: true,
  },
})
