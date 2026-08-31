import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const apiProxy = env.VITE_AERO_VAULT_API_PROXY ?? 'http://127.0.0.1:8080'
  return {
    base: '/ui/app/',
    plugins: [react()],
    server: {
      port: 5181,
      strictPort: true,
      proxy: {
        '/v1': apiProxy,
        '/auth': apiProxy,
      },
    },
    build: {
      outDir: '../internal/webui/static/app',
      emptyOutDir: true,
    },
    test: {
      environment: 'jsdom',
    },
  }
})
