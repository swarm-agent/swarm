import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const backendTarget = process.env.SWARM_BACKEND_URL || 'http://127.0.0.1:7781'
const desktopPort = Number(process.env.SWARM_DESKTOP_PORT || '5555')

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    host: '127.0.0.1',
    port: Number.isFinite(desktopPort) ? desktopPort : 5555,
    strictPort: true,
    proxy: {
      '/v1': {
        target: backendTarget,
        changeOrigin: false,
        ws: true,
      },
      '/v3': {
        target: backendTarget,
        changeOrigin: false,
      },
      '/healthz': backendTarget,
      '/readyz': backendTarget,
      '/desktop': backendTarget,
      '/ws': {
        target: backendTarget.replace(/^http/i, 'ws'),
        changeOrigin: false,
        ws: true,
      },
    },
  },
})
