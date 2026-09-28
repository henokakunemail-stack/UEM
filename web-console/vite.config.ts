import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

export default defineConfig({
  // SoftwarePage and RemoteControlModal are written against Tailwind utilities
  // that had no build step at all — 242 class names resolved to nothing. This
  // plugin is what makes them real.
  plugins: [react(), tailwindcss()],
  server: {
    port: 3000,
    proxy: {
      '/api': {
        target: 'http://localhost:8443',
        changeOrigin: true,
        ws: true,
      },
      '/healthz': 'http://localhost:8443',
    },
  },
  build: {
    // Build straight into the Go embed directory. Vite clears the outDir on
    // every build, so the stale-asset pile-up that used to sit here is gone by
    // construction: server/cmd/server/dist can never hold a bundle older than
    // the last `npm run build`.
    //
    // This was a real bug: the console was built into web-console/dist while
    // the server embedded server/cmd/server/dist, and nothing ever copied one
    // to the other. The running binary served months-old HTML while every
    // frontend change appeared to do nothing. README and CI both claimed the
    // sync was automatic; it was not.
    outDir: '../server/cmd/server/dist',
    emptyOutDir: true,
  },
})
