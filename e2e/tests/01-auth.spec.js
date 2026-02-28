const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, verify2FA, fullLogin } = require('./helpers');
const { saveState, clearState } = require('./auth-state');

// Shared state across tests in this file (sequential)
let totpSecret = '';
const testUser = {
  name: 'E2E Admin',
  email: 'e2e-admin@forgedesk.test',
  password: 'E2ETestPassword123!',
};

test.describe('Authentication Flow', () => {
  test('login page renders', async ({ page }) => {
    // Clear shared state from previous runs
    clearState();

    await page.goto('/login');
    await expect(page.locator('h1')).toContainText('Sign in');
    await expect(page.locator('input[name="email"]')).toBeVisible();
    await expect(page.locator('input[name="password"]')).toBeVisible();
  });

  test('register page renders', async ({ page }) => {
    await page.goto('/register');
    await expect(page.locator('input[name="name"]')).toBeVisible();
    await expect(page.locator('input[name="email"]')).toBeVisible();
    await expect(page.locator('input[name="password"]')).toBeVisible();
  });

  test('register rejects short password', async ({ page }) => {
    await registerUser(page, { name: 'Short', email: 'short@test.com', password: 'short' });

    await expect(page.locator('.flash-error')).toContainText('at least 12 characters');
  });

  test('register new user (first user = superadmin)', async ({ page }) => {
    await registerUser(page, testUser);

    // Should show success flash and login form
    await expect(page.locator('.flash-success')).toContainText('Account created');
    await expect(page.locator('input[name="email"]')).toBeVisible();
  });

  test('register duplicate email is rejected', async ({ page }) => {
    await registerUser(page, testUser);
    await expect(page.locator('.flash-error')).toContainText('already registered');
  });

  test('login with correct credentials redirects to 2FA setup', async ({ page }) => {
    await loginUser(page, testUser);
    await expect(page).toHaveURL(/\/setup-2fa/);
  });

  test('login with wrong password shows error', async ({ page }) => {
    await loginUser(page, { email: testUser.email, password: 'WrongPassword123!' });
    await expect(page.locator('.flash-error')).toContainText('Invalid email or password');
  });

  test('complete 2FA TOTP setup', async ({ page }) => {
    await loginUser(page, testUser);
    await expect(page).toHaveURL(/\/setup-2fa/);

    const result = await setup2FA(page);
    totpSecret = result.secret;

    // Save to shared state for other test files
    saveState('superadmin', {
      email: testUser.email,
      password: testUser.password,
      totpSecret: totpSecret,
    });

    // Should show recovery codes or redirect to dashboard
    const url = page.url();
    const hasRecoveryCodes = url.includes('recovery') || await page.locator('.recovery-code').count() > 0;
    const isDashboard = url === '/' || url.endsWith('/') || !url.includes('setup');

    expect(hasRecoveryCodes || isDashboard).toBeTruthy();
  });

  test('returning login with 2FA verification', async ({ page }) => {
    // Skip if we don't have a TOTP secret from the previous test
    test.skip(!totpSecret, 'TOTP secret not available from setup test');

    await loginUser(page, testUser);
    await expect(page).toHaveURL(/\/verify-2fa/);

    await verify2FA(page, totpSecret);

    // Should be on dashboard or org page
    await page.waitForURL(url => !url.href.includes('verify-2fa') && !url.href.includes('setup-2fa'), { timeout: 5000 });
  });

  test('logout works', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    await fullLogin(page, { ...testUser, totpSecret });

    // Submit logout form directly (bypasses Alpine.js dropdown timing)
    await page.locator('form[action="/logout"]').evaluate(form => form.submit());
    await page.waitForLoadState('networkidle');

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
