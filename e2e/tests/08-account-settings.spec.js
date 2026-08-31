// Sessions come from global setup: the app allows exactly one registration
// ever (first-user-only), and logging in per test would replay a TOTP code
// inside its 30s step, which ValidateTOTPOnce rejects (#136).
let rootCookies = [];

const { test, expect } = require('@playwright/test');
const { useSession, rootSession, generateTOTP } = require('./helpers');

const testUser = {
  name: 'Account Settings User',
  email: 'e2e-account@forgedesk.test',
  password: 'E2ETestPassword123!',
};
const newPassword = 'NewPassword456!!';
const newEmail = 'e2e-account-new@forgedesk.test';
let totpSecret = '';

test.describe.serial('Account Settings', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    // Shared root session: see the note at the top of this file (#136).

    rootCookies = rootSession().cookies;

    await useSession(page, rootCookies);
    await page.close();
  });

  test('account settings page renders', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    await useSession(page, rootCookies);
    await page.goto('/account/settings');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('h1')).toContainText('Account Settings');
    await expect(page.locator('h2:has-text("Change Password")')).toBeVisible();
    await expect(page.locator('h2:has-text("Change Email")')).toBeVisible();
    await expect(page.locator('input[name="current_password"]')).toBeVisible();
    await expect(page.locator('input[name="new_password"]')).toBeVisible();
    await expect(page.locator('input[name="confirm_password"]')).toBeVisible();
    await expect(page.locator('input[name="new_email"]')).toBeVisible();
  });

  test('change password - wrong old password shows error', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    await useSession(page, rootCookies);
    await page.goto('/account/settings');
    await page.waitForLoadState('networkidle');

    await page.fill('input[name="current_password"]', 'WrongOldPassword!!');
    // Remove minlength to bypass browser HTML5 validation
    await page.locator('input[name="new_password"]').evaluate(el => el.removeAttribute('minlength'));
    await page.locator('input[name="confirm_password"]').evaluate(el => el.removeAttribute('minlength'));
    await page.fill('input[name="new_password"]', 'ValidNewPass123!');
    await page.fill('input[name="confirm_password"]', 'ValidNewPass123!');

    // Submit the change password form
    await page.locator('form[action="/account/password"] button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Current password is incorrect');
  });

  test('change password - too short new password shows error', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    await useSession(page, rootCookies);
    await page.goto('/account/settings');
    await page.waitForLoadState('networkidle');

    await page.fill('input[name="current_password"]', testUser.password);
    // Remove minlength to bypass browser HTML5 validation and test server-side
    await page.locator('input[name="new_password"]').evaluate(el => el.removeAttribute('minlength'));
    await page.locator('input[name="confirm_password"]').evaluate(el => el.removeAttribute('minlength'));
    await page.fill('input[name="new_password"]', 'short');
    await page.fill('input[name="confirm_password"]', 'short');

    await page.locator('form[action="/account/password"] button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('at least 12 characters');
  });

  test('change password - success', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    await useSession(page, rootCookies);
    await page.goto('/account/settings');
    await page.waitForLoadState('networkidle');

    await page.fill('input[name="current_password"]', testUser.password);
    await page.locator('input[name="new_password"]').evaluate(el => el.removeAttribute('minlength'));
    await page.locator('input[name="confirm_password"]').evaluate(el => el.removeAttribute('minlength'));
    await page.fill('input[name="new_password"]', newPassword);
    await page.fill('input[name="confirm_password"]', newPassword);

    await page.locator('form[action="/account/password"] button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Password updated successfully');
  });

  test('login with new password works', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    // Login with the new password
    await loginUser(page, { email: testUser.email, password: newPassword });
    await verify2FA(page, totpSecret);

    // Should reach a page that is not login or verify-2fa
    await page.waitForURL(url => !url.href.includes('verify-2fa') && !url.href.includes('login'), { timeout: 5000 });
    const currentUrl = page.url();
    expect(currentUrl).not.toContain('/login');
  });

  test('change email - success', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    // Login with the new password (changed in the previous test)
    await useSession(page, rootCookies);
    await page.goto('/account/settings');
    await page.waitForLoadState('networkidle');

    await page.fill('input[name="new_email"]', newEmail);
    await page.fill('input[name="password"]', newPassword);

    await page.locator('form[action="/account/email"] button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Email updated successfully');
  });

  test('login with new email works', async ({ page }) => {
    test.skip(!totpSecret, 'TOTP secret not available');

    await loginUser(page, { email: newEmail, password: newPassword });
    await verify2FA(page, totpSecret);

    await page.waitForURL(url => !url.href.includes('verify-2fa') && !url.href.includes('login'), { timeout: 5000 });
    const currentUrl = page.url();
    expect(currentUrl).not.toContain('/login');
  });
});
