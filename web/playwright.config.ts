import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: 'e2e',
  testMatch: '*.spec.ts',
  workers: 1,
  timeout: 30_000,
  reporter: [['list']],
  use: { baseURL: 'http://127.0.0.1:29998', viewport: { width: 1280, height: 800 } },
  projects: [
    { name: 'desktop' },
    { name: 'phone', use: { viewport: { width: 390, height: 780 }, hasTouch: true } },
  ],
  webServer: {
    command: 'node e2e/start.mjs',
    url: 'http://127.0.0.1:29998/api/v1/health',
    reuseExistingServer: false,
    timeout: 30_000,
  },
})
