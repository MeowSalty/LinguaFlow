import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/e2e',
  testMatch: ['startup.spec.ts', 'auth-callback-startup.spec.ts'],
  outputDir: './.test-runtime/startup-results',
  fullyParallel: true,
  workers: 2,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list'], ['json', { outputFile: './.test-runtime/startup-results.json' }]],
  use: { ...devices['Desktop Chrome'], trace: 'retain-on-failure' },
  projects: [
    {
      name: 'dev',
      // Cold Vite transforms can exceed five seconds; callback timing is measured separately.
      expect: { timeout: 15_000 },
      use: { baseURL: 'http://127.0.0.1:5187' },
    },
    { name: 'preview', use: { baseURL: 'http://127.0.0.1:4187' } },
  ],
  webServer: [
    {
      command: 'task test:serve-dev',
      url: 'http://127.0.0.1:5187',
      reuseExistingServer: false,
    },
    {
      command: 'task test:serve-preview',
      url: 'http://127.0.0.1:4187',
      reuseExistingServer: false,
    },
  ],
})
