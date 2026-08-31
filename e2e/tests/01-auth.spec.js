const { test, expect } = require('@playwright/test');
const { loginUser, verify2FA, fullLogin, rootSession } = require('./helpers');

// Rewritten for #136.
//
// This spec used to register its own users and write the superadmin's
// credentials to a JSON file for 05-admin to read. Registration is
// first-user-only, so it could only ever work as the very first spec against
// an empty database — and it wrote a cross-file dependency that made the suite
// order-dependent.
//
// The registration-form tests are gone, and NOT because they were
// inconvenient: global setup performs the one registration the application
// permits, and fails the entire suite loudly if it breaks, so first-user
// registration and TOTP setup are still covered. That registration is closed
// afterwards is asserted in 09-rbac-journeys.
//
// What remains here is the part nothing else covers end to end: signing in,
// passing 2FA, and signing out.
const ROOT = rootSession();

test.describe('Authentication', () => {
  test('login page renders', async ({ page }) => {
    await page.goto('/login');
    await expect(page.locator('input[name="email"]')).toBeVisible();
    await expect(page.locator('input[name="password"]')).toBeVisible();
  });

  test('login with correct credentials reaches 2FA verification', async ({ page }) => {
    await loginUser(page, ROOT.user);
    await expect(page).toHaveURL(/\/verify-2fa/);
  });

  test('login with wrong password shows an error and does not sign in', async ({ page }) => {
    await loginUser(page, { email: ROOT.user.email, password: 'WrongPassword123!' });

    await expect(page.locator('body')).toContainText('Invalid email or password');
    expect(page.url()).toContain('/login');
  });

  test('login with an unknown address is rejected identically', async ({ page }) => {
    // Same wording as a wrong password: the response must not reveal whether
    // the address is registered.
    await loginUser(page, { email: 'no-such-account@forgedesk.test', password: 'E2ETestPassword123!' });

    await expect(page.locator('body')).toContainText('Invalid email or password');
    expect(page.url()).toContain('/login');
  });

  // Sign-in through sign-out as ONE journey, deliberately.
  //
  // Two tests each doing a full login inside the same 30-second window replay
  // the same TOTP code, which ValidateTOTPOnce (migration 000027) rejects — so
  // the second one failed for a reason that had nothing to do with logout. One
  // login also makes this the flow a user actually performs.
  test('sign in, pass 2FA, and sign out', async ({ page }) => {
    await fullLogin(page, { ...ROOT.user, totpSecret: ROOT.totpSecret });

    await page.waitForURL(
      (url) => !url.href.includes('verify-2fa') && !url.href.includes('setup-2fa'),
      { timeout: 10000 },
    );
    expect(page.url()).not.toContain('/login');

    // Submit the form directly, bypassing Alpine.js dropdown timing.
    await page.locator('form[action="/logout"]').evaluate((form) => form.submit());
    await page.waitForLoadState('networkidle');
    await expect(page).toHaveURL(/\/login/);

    // Signing out must actually end the session, not just navigate.
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/);
  });

  test('protected routes redirect to login when not authenticated', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/);
  });

  test('health endpoint returns ok', async ({ page }) => {
    const response = await page.request.get('/health');
    expect(response.status()).toBe(200);
    expect(await response.text()).toBe('ok');
  });
});
