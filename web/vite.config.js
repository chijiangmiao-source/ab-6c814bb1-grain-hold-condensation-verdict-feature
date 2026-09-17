import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// The dev server proxies /api to the Go service so the browser talks to the
// real Gin API in development. In production nginx does the same proxying.
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '0.0.0.0',
    proxy: {
      '/api': {
        target: env('VITE_API_ORIGIN', 'http://localhost:8080'),
        changeOrigin: true,
      },
    },
  },
  preview: {
    host: '0.0.0.0',
    port: 4173,
    proxy: {
      '/api': {
        target: env('VITE_API_ORIGIN', 'http://localhost:8080'),
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['test/unit/**/*.test.js'],
    coverage: { enabled: false },
  },
})

function env(key, fallback) {
  return key in process.env ? process.env[key] : fallback
}
