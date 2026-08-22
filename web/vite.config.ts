import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  base: '/ui/app/',
  plugins: [react()],
  server: {
    port: 5181,
    strictPort: true,
    proxy: {
      '/v1': 'http://127.0.0.1:8080',
      '/auth': 'http://127.0.0.1:8080',
    },
  },
  build: {
    outDir: '../internal/webui/static/app',
    emptyOutDir: true,
  },
  test: {
    environment: 'jsdom',
  },
})
