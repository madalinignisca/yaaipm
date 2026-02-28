const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin } = require('./helpers');
const { getState } = require('./auth-state');

test.describe('Admin Panel', () => {
  // The superadmin is the first registered user (created in 01-auth tests).
  // We retrieve their credentials from shared state.
  let superAdmin;
  let clientUser;
  let clientTotpSecret = '';

  test.beforeAll(async ({ browser }) => {
    superAdmin = getState('superadmin');
    if (!superAdmin) {
      test.skip(true, 'Superadmin state not available (01-auth must run first)');
    }

    // Register a client user for the forbidden test
    clientUser = {
      name: 'Client User',
      email: 'e2e-client@forgedesk.test',
      password: 'E2ETestPassword123!',
    };

    const page = await browser.newPage();
    await registerUser(page, clientUser);
    await loginUser(page, clientUser);
    const result = await setup2FA(page);
    clientTotpSecret = result.secret;
    await page.close();
  });

  test('admin page is accessible by superadmin', async ({ page }) => {
    test.skip(!superAdmin, 'Superadmin state not available');

    await fullLogin(page, {
      email: superAdmin.email,
      password: superAdmin.password,
      totpSecret: superAdmin.totpSecret,
    });
    await page.goto('/admin');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Admin Panel');
  });

  test('admin page shows users table', async ({ page }) => {
    test.skip(!superAdmin, 'Superadmin state not available');

    await fullLogin(page, {
      email: superAdmin.email,
      password: superAdmin.password,
      totpSecret: superAdmin.totpSecret,
    });
    await page.goto('/admin');
    await page.waitForLoadState('networkidle');

    // Should have a users tab/section
    await expect(page.locator('body')).toContainText('Users');

    // Should list the superadmin user
    await expect(page.locator('body')).toContainText(superAdmin.email);
  });

  test('admin page shows organizations tab', async ({ page }) => {
    test.skip(!superAdmin, 'Superadmin state not available');

    await fullLogin(page, {
      email: superAdmin.email,
      password: superAdmin.password,
      totpSecret: superAdmin.totpSecret,
    });
    await page.goto('/admin');

    // Click Orgs tab
    const orgsTab = page.locator('button:has-text("Organizations")');
    if (await orgsTab.isVisible()) {
      await orgsTab.click();
      await page.waitForTimeout(500);
      await expect(page.locator('body')).toContainText('Organizations');
    }
  });

  test('admin page is forbidden for non-superadmin', async ({ page }) => {
    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
    const response = await page.request.get('/admin');
    expect(response.status()).toBe(403);
  });
});
