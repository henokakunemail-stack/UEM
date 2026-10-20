import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// Component tests run in jsdom. Kept apart from vite.config.ts so the
// production build config carries nothing test-only.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: ['src/test/setup.ts'],
  },
})
