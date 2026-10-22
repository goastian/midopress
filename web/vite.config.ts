import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// The admin server of MidoPress listens on 127.0.0.1:9998 by default. In
// development the pages come from Vite, so every change shows at once, and the
// API calls are forwarded to the real server.
const admin = process.env.MIDOPRESS_ADMIN ?? 'http://127.0.0.1:9998'

export default defineConfig({
  plugins: [vue()],
  server: {
    host: true,
    port: 5173,
    proxy: {
      '/api': { target: admin, changeOrigin: false },
    },
  },
  build: {
    // the Go binary embeds what is built here
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
})
