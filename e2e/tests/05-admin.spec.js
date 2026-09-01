const { test, expect } = require('@playwright/test');
const { useSession, rootSession, inviteAndRegisterUser } = require('./helpers');

// Rewritten for #136. Previously this spec read the superadmin's credentials
// out of a JSON file written by 01-auth (tests/auth-state.js), which made it
// silently order-dependent, and skipped itself whenever that file was missing —
// which was always, because 01-auth could not run either.
//
// It now uses the shared root session (the first registered user, therefore
// superadmin) and creates a genuine non-superadmin through the invitation
// flow, which assigns RoleClient.
let rootCookies = [];
let clientCookies = [];

const ORG = { name: 'Admin Org', slug: 'admin-org' };
const CLIENT_USER = {
  name: 'Admin Spec Client',
  email: 'e2e-admin-client@forgedesk.test',
  password: 'E2ETestPassword123!',
};

test.describe.serial('Admin Panel', () => {
  test.beforeAll(async ({ browser }) => {
    rootCookies = rootSession().cookies;

    const page = await browser.newPage();
    await useSession(page, rootCookies);

    // An org to invite into. Created here rather than assumed, so this spec
    // does not depend on any other having run.
    await page.request.post('/orgs', { form: { name: ORG.name } });

    const client = await inviteAndRegisterUser(page, {
      orgSlug: ORG.slug,
      email: CLIENT_USER.email,
      name: CLIENT_USER.name,
      password: CLIENT_USER.password,
      role: 'member',
    });
    clientCookies = client.cookies;

    await page.close();
  });

  test('admin page is accessible by superadmin', async ({ page }) => {
    await useSession(page, rootCookies);
    await page.goto('/admin');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Admin Panel');
  });

  test('admin page lists the signed-in superadmin', async ({ page }) => {
    await useSession(page, rootCookies);
    await page.goto('/admin');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText(rootSession().user.email);
  });

  test('admin page is forbidden for a non-superadmin', async ({ page }) => {
    // The whole point of this test is that the caller is NOT a superadmin.
    // An invited user gets RoleClient (internal/handlers/invitations.go), so
    // this must be the client session and never the root one.
    await useSession(page, clientCookies);
    const response = await page.request.get('/admin');
    expect(response.status()).toBe(403);
  });
});
