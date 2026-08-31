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
 * Reuse an already-authenticated session instead of logging in again.
 *
 * Why this is not optional: `ValidateTOTPOnce` (migration 000027) rejects a
 * TOTP code that was already used in the same 30-second step. Tests run
 * back-to-back, so a spec that calls `fullLogin` per test feeds the server the
 * same code twice and gets bounced to /verify-2fa — which then surfaces as an
 * unrelated "element not found" further down the test (#136). Log in once per
 * user in beforeAll, capture the cookies, and apply them here.
 */
async function useSession(page, cookies) {
  if (!cookies || cookies.length === 0) {
    throw new Error('useSession called with no cookies — the beforeAll login did not complete');
  }
  await page.context().addCookies(cookies);
}

/**
 * Create an ADDITIONAL user through the invitation flow.
 *
 * Why this exists: `registerUser` only works for the very first account.
 * `RegisterPage`/`Register` (internal/handlers/auth.go) redirect or 403 once
 * `CountUsers > 0`, so any spec needing a second or third user cannot use the
 * registration form. Invitation registration is deliberately NOT gated that
 * way, so it is the only supported path to a second account — and it is a real
 * product flow, so exercising it is a bonus rather than a workaround (#136).
 *
 * `page` must already be authenticated as someone who can manage `orgSlug`.
 * Returns the new user's TOTP secret for later `fullLogin` calls.
 */
async function inviteAndRegisterUser(page, { orgSlug, email, name, password, role = 'member' }) {
  // POST directly: the handler answers with the invite_result partial, which
  // carries the raw token. The token is stored only as a hash server-side, so
  // this response is the single opportunity to capture it.
  const res = await page.request.post(`/orgs/${orgSlug}/invitations`, {
    form: { email, role },
  });
  if (!res.ok()) {
    throw new Error(`inviting ${email} failed: HTTP ${res.status()}`);
  }
  // Match the URL by its shape rather than by attribute order inside the
  // input tag — the partial's markup is free to change around it.
  const match = (await res.text()).match(/https?:\/\/[^"\s]+\/invite\/[^"\s]+/);
  if (!match) {
    throw new Error(`invite response for ${email} contained no invite URL`);
  }

  // Redeem the invitation in a FRESH context. `page` is signed in as the
  // inviter, and opening an invite link while already authenticated bounces to
  // the app instead of the registration form — which is also what a real
  // invitee's browser looks like: a different person, signed in as nobody.
  const invitee = await page.context().browser().newContext();
  try {
    const inviteePage = await invitee.newPage();
    await inviteePage.goto(match[0]);

    const nameField = inviteePage.locator('input[name="name"]');
    if ((await nameField.count()) === 0) {
      throw new Error(
        `invite page for ${email} has no registration form (url: ${inviteePage.url()}) — ` +
          'the token may be invalid, expired, or the account may already exist',
      );
    }

    await inviteePage.fill('input[name="name"]', name);
    await inviteePage.$eval('input[name="password"]', (el) => el.removeAttribute('minlength'));
    await inviteePage.fill('input[name="password"]', password);
    await inviteePage.click('button[type="submit"]');
    await inviteePage.waitForLoadState('networkidle');

    // Redeeming an invitation signs the new user in, landing them on 2FA setup.
    // If that ever changes, log in explicitly rather than assuming.
    if (!inviteePage.url().includes('/setup-2fa')) {
      await loginUser(inviteePage, { email, password });
    }
    const { secret } = await setup2FA(inviteePage);
    if (!secret) {
      throw new Error(`2FA setup produced no TOTP secret for ${email}`);
    }
    // Hand back the live session as well as the secret: callers should reuse
    // these cookies rather than logging in again (see useSession).
    return { secret, cookies: await invitee.cookies() };
  } finally {
    await invitee.close();
  }
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
  // Record when, so awaitTOTPReuseWindow can wait out the server's 30s guard.
  lastTOTPVerifyAt = Date.now();
}

/**
 * Full login flow: login + 2FA verification.
 */
async function fullLogin(page, { email, password, totpSecret }) {
  await loginUser(page, { email, password });
  await verify2FA(page, totpSecret);
}

module.exports = {
  awaitTOTPReuseWindow,
  rootSession,
  inviteAndRegisterUser,
  useSession,
  generateTOTP,
  registerUser,
  loginUser,
  setup2FA,
  verify2FA,
  fullLogin,
};

/**
 * The shared root session created once by global-setup.js.
 *
 * Specs must NOT call registerUser: the application only permits one
 * registration ever (first-user-only), so a spec that registers its own user
 * works alone and silently skips behind any other spec (#136).
 */
function rootSession() {
  const file = require('path').join(__dirname, '..', '.auth', 'root.json');
  let raw;
  try {
    raw = require('fs').readFileSync(file, 'utf-8');
  } catch (err) {
    throw new Error(
      `no root session at ${file}: global setup did not run or failed (${err.message})`,
    );
  }
  const state = JSON.parse(raw);
  if (!state.cookies || state.cookies.length === 0) {
    throw new Error('root session file contains no cookies');
  }
  return state;
}

// When 2FA was last verified successfully, so awaitTOTPReuseWindow knows how
// long to wait. Set by verify2FA.
let lastTOTPVerifyAt = 0;

/**
 * Wait out the server's TOTP reuse guard.
 *
 * ValidateTOTPOnce (internal/auth/totp.go) rejects on ELAPSED TIME, not on the
 * code:
 *
 *     if lastUsedAt != nil && time.Since(*lastUsedAt) < 30*time.Second
 *
 * so ANY verification within 30 seconds of the last successful one is refused,
 * even with a different code. Waiting for the next TOTP window boundary is not
 * enough — that can be a second later. Tests that must sign in twice wait here
 * instead (#136).
 */
async function awaitTOTPReuseWindow(page) {
  const guard = 30_000;
  const slack = 1_500;
  const elapsed = Date.now() - lastTOTPVerifyAt;
  if (lastTOTPVerifyAt === 0 || elapsed >= guard + slack) {
    return;
  }
  await page.waitForTimeout(guard + slack - elapsed);
}
