import { defineConfig, devices } from '@playwright/test'

// Smoke tests run against a live stack: the Go backend (AUTH_DISABLED=true,
// fresh PostgreSQL) and either `npm run dev` or `npm run build && npm run
// preview` on port 3000. See README.md. Not part of CI yet.
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_URL ?? 'http://localhost:3000',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
