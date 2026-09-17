import { defineConfig } from '@playwright/test'

// Two modes:
//  - Local (default): spin up the REAL stack via scripts/e2e-serve.sh
//    (Go Gin + fresh SQLite + vite preview of the production build).
//  - Container (`docker compose run --rm verify`): PLAYWRIGHT_BASE_URL points
//    at the compose "web" service, so no local web server is started here.
const inContainer = !!process.env.PLAYWRIGHT_BASE_URL

export default defineConfig({
  testDir: './test/e2e',
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:4173',
    actionTimeout: 8_000,
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
  webServer: inContainer ? undefined : {
    command: 'bash ../scripts/e2e-serve.sh',
    url: 'http://localhost:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 90_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
