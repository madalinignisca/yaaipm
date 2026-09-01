const { chromium } = require('@playwright/test');
const fs = require('fs');
const path = require('path');
const { registerUser, loginUser, setup2FA } = require('./tests/helpers');

const STATE_DIR = path.join(__dirname, '.auth');
const STATE_FILE = path.join(STATE_DIR, 'root.json');

// The single account the application will ever let us register.
//
// RegisterPage/Register (internal/handlers/auth.go) redirect or 403 once
// CountUsers > 0, so registration is first-user-only. Every spec used to
// register its own user in beforeAll, which meant only the first spec to touch
// a database could provision itself and the rest silently skipped (#136).
//
// Registering once here and sharing the session lets the whole suite run in a
// single invocation. Specs that need a second or third identity create it
// through the invitation flow, which is deliberately not gated that way.
const ROOT_USER = {
  name: 'E2E Root',
  email: 'e2e-root@forgedesk.test',
  password: 'E2ETestPassword123!',
};

// This doubles as the registration test. If first-user registration or TOTP
// setup breaks, global setup throws and the entire suite fails loudly — which
// is the behaviour we want from something no other spec can cover any more.
module.exports = async (config) => {
  const baseURL = process.env.BASE_URL || config?.projects?.[0]?.use?.baseURL || 'http://localhost:8081';

  const browser = await chromium.launch();
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();

  try {
    await registerUser(page, ROOT_USER);
    // Registration does not sign you in; it leaves you on the form. Logging in
    // is also the check that the account was really created — if registration
    // was closed (a database that already has users), login fails here.
    await loginUser(page, ROOT_USER);
    if (!page.url().includes('/setup-2fa')) {
      throw new Error(
        `global setup: sign-in after registration did not reach 2FA setup (landed on ${page.url()}). ` +
          'Registration is first-user-only — the database must be empty. ' +
          'Run: docker compose -f docker-compose.test.yml down -v && up -d',
      );
    }

    const { secret } = await setup2FA(page);
    if (!secret) {
      throw new Error('global setup: TOTP setup produced no secret');
    }

    await page.goto('/dashboard');
    if (page.url().includes('/login') || page.url().includes('/verify-2fa')) {
      throw new Error(`global setup: not authenticated after 2FA setup (landed on ${page.url()})`);
    }

    fs.mkdirSync(STATE_DIR, { recursive: true });
    fs.writeFileSync(
      STATE_FILE,
      JSON.stringify({ user: ROOT_USER, totpSecret: secret, cookies: await context.cookies() }, null, 2),
    );
  } finally {
    await browser.close();
  }
};

module.exports.ROOT_USER = ROOT_USER;
module.exports.STATE_FILE = STATE_FILE;
