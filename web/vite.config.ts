import react from '@vitejs/plugin-react'
import { loadEnv } from 'vite'
import { defineConfig } from 'vitest/config'

export default defineConfig(({ mode }) => {
  // `npm run dev` serves the frontend with hot reload and passes API calls,
  // WebSockets included, to a panel running elsewhere: by default the one
  // `make run` starts on port 8080. Set DOCKPANEL_API to point at another.
  const env = loadEnv(mode, '.', 'DOCKPANEL_')
  const api = env.DOCKPANEL_API ?? 'http://localhost:8080'

  return {
    plugins: [react()],
    server: {
      proxy: {
        '/api': { target: api, ws: true },
      },
    },
    build: {
      outDir: 'dist',
    },
    test: {
      environment: 'jsdom',
      setupFiles: ['./src/test-setup.ts'],
      include: ['src/**/*.test.{ts,tsx}'],
      restoreMocks: true,
      unstubGlobals: true,
    },
  }
})
