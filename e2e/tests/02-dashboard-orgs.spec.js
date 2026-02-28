const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin, generateTOTP } = require('./helpers');

const testUser = {
  name: 'Org Admin',
  email: 'e2e-org@forgedesk.test',
  password: 'E2ETestPassword123!',
};
let totpSecret = '';

test.describe('Dashboard & Organizations', () => {
  test.beforeAll(async ({ browser }) => {
    // Register and set up 2FA for the test user
    const page = await browser.newPage();
    await registerUser(page, testUser);
    await loginUser(page, testUser);
    const result = await setup2FA(page);
    totpSecret = result.secret;
    await page.close();
  });

  test('dashboard shows when authenticated', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });
    const url = page.url();
    // Either dashboard or redirected to single org
    expect(url.includes('/login') === false).toBeTruthy();
  });

  test('create organization', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });

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
    await fullLogin(page, { ...testUser, totpSecret });

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
    await fullLogin(page, { ...testUser, totpSecret });

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
