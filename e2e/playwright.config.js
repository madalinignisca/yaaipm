const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: './tests',
  // Base timeout per test AND for beforeAll/afterAll hooks. The debate specs'
  // bootstrap (register → rate-limit pause → login → 2FA → org/project/ticket)
  // runs well past the old 30s on a loaded machine or a slow CI runner, and
  // test.use({timeout}) does NOT extend hook timeouts — only this does.
  timeout: 120000,
  retries: 0,
  workers: 1,  // Sequential — tests share state (auth cookies)
  use: {
    baseURL: process.env.BASE_URL || 'http://localhost:8081',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { browserName: 'chromium' },
    },
  ],
});
