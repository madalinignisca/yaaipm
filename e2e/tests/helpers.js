const OTPAuth = require('otpauth');

/**
 * Generate a TOTP code from a secret.
 * @param {string} secret - Base32 encoded TOTP secret
 * @returns {string} 6-digit TOTP code
 */
function generateTOTP(secret) {
  const totp = new OTPAuth.TOTP({
    issuer: 'ForgeDesk',
    algorithm: 'SHA1',
    digits: 6,
    period: 30,
    secret: secret,
  });
  return totp.generate();
}

/**
 * Register a new user via the registration form.
 * Waits for HTMX to complete the request (hx-boost intercepts form submissions).
 */
async function registerUser(page, { name, email, password }) {
  await page.goto('/register');
  await page.fill('input[name="name"]', name);
  await page.fill('input[name="email"]', email);
  // Remove minlength to avoid browser validation blocking server-side checks
  await page.$eval('input[name="password"]', el => el.removeAttribute('minlength'));
  await page.fill('input[name="password"]', password);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

/**
 * Log in a user via the login form.
 * Waits for HTMX to complete the request and follow any redirects.
 */
async function loginUser(page, { email, password }) {
  await page.goto('/login');
  await page.fill('input[name="email"]', email);
  await page.fill('input[name="password"]', password);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

/**
 * Complete the full 2FA TOTP setup flow.
 * Returns the TOTP secret and recovery codes.
 */
async function setup2FA(page) {
  // Should be on /setup-2fa page
  await page.waitForURL('**/setup-2fa', { timeout: 10000 });

  // Click "Authenticator App" to start TOTP setup
  const totpLink = page.locator('a[href="/setup-2fa/totp"]');
  if (await totpLink.isVisible()) {
    await totpLink.click();
    await page.waitForLoadState('networkidle');
  }

  await page.waitForURL('**/setup-2fa/totp', { timeout: 10000 });

  // Extract the manual key (TOTP secret)
  const manualKeyEl = page.locator('.manual-key code, [data-manual-key], code');
  let secret = '';
  if (await manualKeyEl.count() > 0) {
    secret = (await manualKeyEl.first().textContent()).trim().replace(/\s/g, '');
  }

  if (!secret) {
    // Try to find it in page content via regex
    const pageContent = await page.content();
    const match = pageContent.match(/ManualKey[^>]*>([A-Z2-7]+)/i);
    if (match) {
      secret = match[1].trim();
    }
  }

  if (!secret) {
    throw new Error('Could not extract TOTP secret from setup page');
  }

  // Generate and submit TOTP code
  const code = generateTOTP(secret);
  await page.fill('input[name="code"]', code);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');

  // Should show recovery codes
  await page.waitForURL('**/setup-2fa/totp/verify', { timeout: 5000 }).catch(() => {});

  // Extract recovery codes if visible
  const recoveryCodes = [];
  const codeElements = page.locator('.recovery-code');
  const count = await codeElements.count();
  for (let i = 0; i < count; i++) {
    recoveryCodes.push(await codeElements.nth(i).textContent());
  }

  return { secret, recoveryCodes };
}

/**
 * Verify 2FA with a TOTP code on returning login.
 */
async function verify2FA(page, secret) {
  await page.waitForURL('**/verify-2fa', { timeout: 10000 });
  const code = generateTOTP(secret);
  await page.fill('input[name="code"]', code);
  await page.click('button[type="submit"]');
  await page.waitForLoadState('networkidle');
}

/**
 * Full login flow: login + 2FA verification.
 */
async function fullLogin(page, { email, password, totpSecret }) {
  await loginUser(page, { email, password });
  await verify2FA(page, totpSecret);
}

module.exports = {
  generateTOTP,
  registerUser,
  loginUser,
  setup2FA,
  verify2FA,
  fullLogin,
};
