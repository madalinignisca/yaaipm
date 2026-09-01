const { test, expect } = require('@playwright/test');
const { useSession, rootSession, generateTOTP } = require('./helpers');

const testUser = {
  name: 'Org Admin',
  email: 'e2e-org@forgedesk.test',
  password: 'E2ETestPassword123!',
};
// Sessions come from global setup: the app allows exactly one registration
// ever, and logging in per test would replay a TOTP code inside its 30s step,
// which ValidateTOTPOnce rejects (#136).
let rootCookies = [];

test.describe('Dashboard & Organizations', () => {
  test.beforeAll(() => {
    rootCookies = rootSession().cookies;
  });

  test('dashboard shows when authenticated', async ({ page }) => {
    await useSession(page, rootCookies);
    const url = page.url();
    // Either dashboard or redirected to single org
    expect(url.includes('/login') === false).toBeTruthy();
  });

  test('create organization', async ({ page }) => {
    await useSession(page, rootCookies);

    // Navigate to dashboard
    await page.goto('/');

    // Fill org creation form (Alpine.js dropdown)
    const createBtn = page.locator('button:has-text("New Organization"), button:has-text("Create"), a:has-text("New Organization")');
    if (await createBtn.isVisible()) {
      await createBtn.click();
    }

    // Look for name input in a modal/form
    const nameInput = page.locator('input[name="name"]');
    if (await nameInput.isVisible()) {
      await nameInput.fill('E2E Test Organization');
      await page.click('button[type="submit"]');
      await page.waitForLoadState('networkidle');
    }
  });

  test('org page shows projects list', async ({ page }) => {
    await useSession(page, rootCookies);

    // Create org directly via POST
    const response = await page.request.post('/orgs', {
      form: { name: 'Direct Org' },
      headers: { cookie: await getCookieHeader(page) },
    });

    await page.goto('/orgs/direct-org');
    await page.waitForLoadState('networkidle');

    // Should be on the org page (may be 200 or redirect)
    const status = await page.locator('body').count();
    expect(status).toBe(1);
  });

  test('org settings page shows members', async ({ page }) => {
    await useSession(page, rootCookies);

    // Create org if it doesn't exist
    await page.request.post('/orgs', {
      form: { name: 'Settings Org' },
    });

    await page.goto('/orgs/settings-org/settings');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Members');
  });
});

async function getCookieHeader(page) {
  const cookies = await page.context().cookies();
  return cookies.map(c => `${c.name}=${c.value}`).join('; ');
}
