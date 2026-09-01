const { test, expect } = require('@playwright/test');
const { useSession, rootSession, inviteAndRegisterUser, generateTOTP } = require('./helpers');

const superadminUser = {
  name: 'RBAC Superadmin',
  email: 'e2e-rbac-super@forgedesk.test',
  password: 'E2ETestPassword123!',
};

const clientUser = {
  name: 'RBAC Client',
  email: 'e2e-rbac-client@forgedesk.test',
  password: 'E2ETestPassword123!',
};

// The shared root session is the first registered user, therefore superadmin.
// The client is created through an invite, which assigns RoleClient — the two
// identities this spec exists to contrast (#136).
let superadminCookies = [];
let clientCookies = [];
let projectId = '';
let featureTicketId = '';
let bugTicketId = '';

const orgSlug = 'rbac-org';
const projSlug = 'rbac-project';

test.describe.serial('RBAC Journeys', () => {
  // ──────────────────────────────────────────────
  // Setup: register superadmin, create org/project/tickets, register client user
  // ──────────────────────────────────────────────
  test('setup: register superadmin and create data', async ({ browser }) => {
    const page = await browser.newPage();

    // The shared root session: first registered user, therefore superadmin.
    superadminCookies = rootSession().cookies;
    await useSession(page, superadminCookies);

    // Create org
    await page.request.post('/orgs', { form: { name: 'RBAC Org' } });

    // Create project
    await page.request.post('/orgs/' + orgSlug + '/projects', { form: { name: 'RBAC Project' } });

    // Get project ID from the features page
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/features');
    await page.waitForLoadState('networkidle');
    const bodyContent = await page.content();
    const pidMatch = bodyContent.match(/project_id[^>]*value="([^"]+)"/);
    if (pidMatch) {
      projectId = pidMatch[1];
    }

    // Create a feature ticket
    if (projectId) {
      const featureResp = await page.request.post('/tickets', {
        form: {
          project_id: projectId,
          title: 'RBAC Test Feature',
          type: 'feature',
          priority: 'high',
          description: 'Feature for RBAC testing',
        },
        headers: { Referer: '/orgs/' + orgSlug + '/projects/' + projSlug + '/features' },
      });
      expect(featureResp.status()).toBeLessThan(400);

      // Create a bug ticket
      const bugResp = await page.request.post('/tickets', {
        form: {
          project_id: projectId,
          title: 'RBAC Test Bug',
          type: 'bug',
          priority: 'medium',
          description: 'Bug for RBAC testing',
        },
        headers: { Referer: '/orgs/' + orgSlug + '/projects/' + projSlug + '/bugs' },
      });
      expect(bugResp.status()).toBeLessThan(400);

      // Extract ticket IDs from the features page
      await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/features');
      await page.waitForLoadState('networkidle');
      const featureLink = page.locator('a:has-text("RBAC Test Feature")');
      if (await featureLink.count() > 0) {
        const href = await featureLink.first().getAttribute('href');
        if (href) {
          const tidMatch = href.match(/\/tickets\/([a-f0-9-]+)/);
          if (tidMatch) featureTicketId = tidMatch[1];
        }
      }

      // Extract bug ticket ID
      await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/bugs');
      await page.waitForLoadState('networkidle');
      const bugLink = page.locator('a:has-text("RBAC Test Bug")');
      if (await bugLink.count() > 0) {
        const href = await bugLink.first().getAttribute('href');
        if (href) {
          const tidMatch = href.match(/\/tickets\/([a-f0-9-]+)/);
          if (tidMatch) bugTicketId = tidMatch[1];
        }
      }
    }

    // Invite the client. inviteAndRegisterUser redeems the invite in a fresh
    // browser context and returns the new session; the old hand-rolled version
    // had a "register normally" fallback that could not work (registration is
    // first-user-only) and would have quietly produced a user OUTSIDE the org,
    // making every membership assertion below meaningless.
    const client = await inviteAndRegisterUser(page, {
      orgSlug,
      email: clientUser.email,
      name: clientUser.name,
      password: clientUser.password,
      role: 'member',
    });
    clientCookies = client.cookies;

    await page.close();
  });

  // ──────────────────────────────────────────────
  // Superadmin journeys
  // ──────────────────────────────────────────────
  test('superadmin: can access admin panel', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/admin');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Admin Panel');
  });

  test('superadmin: can access org page', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/orgs/' + orgSlug);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Org');
  });

  test('superadmin: can access project brief', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/brief');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Brief');
  });

  test('superadmin: can access project features', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Features');
    await expect(page.locator('body')).toContainText('RBAC Test Feature');
  });

  test('superadmin: can access project bugs', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Bugs');
    await expect(page.locator('body')).toContainText('RBAC Test Bug');
  });

  test('superadmin: can access project gantt', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/gantt');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Timeline');
  });

  test('superadmin: can access project archived tab', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/archived');
    await page.waitForLoadState('networkidle');

    // Archived page should load (200) for staff
    await expect(page.locator('body')).toContainText('Archived');
  });

  test('superadmin: can access project costs', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/costs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Costs');
  });

  test('superadmin: ticket detail shows staff controls', async ({ page }) => {

    await useSession(page, superadminCookies);
    await page.goto('/tickets/' + featureTicketId);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Test Feature');

    // Staff should see the dropdown button for status/agent controls
    const dotsBtn = page.locator('.btn-dots');
    await expect(dotsBtn).toBeVisible();
  });

  test('superadmin: can create tickets', async ({ page }) => {

    await useSession(page, superadminCookies);

    const response = await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'Superadmin Created Ticket',
        type: 'task',
        priority: 'low',
        description: 'Created by superadmin',
      },
      headers: { Referer: '/orgs/' + orgSlug + '/projects/' + projSlug + '/features' },
    });

    expect(response.status()).toBeLessThan(400);
  });

  test('superadmin: can update ticket status', async ({ page }) => {

    await useSession(page, superadminCookies);

    const response = await page.request.fetch('/tickets/' + featureTicketId + '/status', {
      method: 'PATCH',
      form: { status: 'ready' },
    });

    expect(response.status()).toBeLessThan(400);
  });

  // ──────────────────────────────────────────────
  // Client/member journeys
  // ──────────────────────────────────────────────
  test('client: cannot access admin panel (403)', async ({ page }) => {

    await useSession(page, clientCookies);

    const response = await page.request.get('/admin');
    expect(response.status()).toBe(403);
  });

  test('client: can access their org page', async ({ page }) => {

    await useSession(page, clientCookies);
    await page.goto('/orgs/' + orgSlug);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Org');
  });

  test('client: can access project brief', async ({ page }) => {

    await useSession(page, clientCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/brief');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Brief');
  });

  test('client: can access project features', async ({ page }) => {

    await useSession(page, clientCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Features');
    await expect(page.locator('body')).toContainText('RBAC Test Feature');
  });

  test('client: can access project bugs', async ({ page }) => {

    await useSession(page, clientCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Bugs');
    await expect(page.locator('body')).toContainText('RBAC Test Bug');
  });

  test('client: can access project gantt', async ({ page }) => {

    await useSession(page, clientCookies);
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/gantt');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Timeline');
  });

  test('client: cannot access archived tab (403)', async ({ page }) => {

    await useSession(page, clientCookies);

    const response = await page.request.get('/orgs/' + orgSlug + '/projects/' + projSlug + '/archived');
    expect(response.status()).toBe(403);
  });

  test('client: ticket detail does NOT show staff dropdown', async ({ page }) => {

    await useSession(page, clientCookies);
    await page.goto('/tickets/' + featureTicketId);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Test Feature');

    // Client should NOT see the staff dropdown button
    const dotsBtn = page.locator('.btn-dots');
    await expect(dotsBtn).toHaveCount(0);
  });

  test('client: can create tickets', async ({ page }) => {

    await useSession(page, clientCookies);

    const response = await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'Client Created Ticket',
        type: 'bug',
        priority: 'medium',
        description: 'Created by client member',
      },
      headers: { Referer: '/orgs/' + orgSlug + '/projects/' + projSlug + '/bugs' },
    });

    expect(response.status()).toBeLessThan(400);
  });

  test('client: can post comments', async ({ page }) => {

    await useSession(page, clientCookies);
    await page.goto('/tickets/' + featureTicketId);
    await page.waitForLoadState('networkidle');

    const textarea = page.locator('textarea[name="body"]');
    if (await textarea.isVisible()) {
      await textarea.fill('Comment from client member');
      await page.click('button:has-text("Comment")');
      await page.waitForTimeout(1000);

      await expect(page.locator('body')).toContainText('Comment from client member');
    }
  });

  test('client: cannot archive tickets (403)', async ({ page }) => {

    await useSession(page, clientCookies);

    const response = await page.request.post('/tickets/' + featureTicketId + '/archive');
    expect(response.status()).toBe(403);
  });

  test('client: cannot delete tickets (403)', async ({ page }) => {

    await useSession(page, clientCookies);

    const response = await page.request.fetch('/tickets/' + featureTicketId, {
      method: 'DELETE',
    });
    expect(response.status()).toBe(403);
  });

  // ──────────────────────────────────────────────
  // Unauthenticated journeys
  // ──────────────────────────────────────────────
  test('unauthenticated: protected routes redirect to login', async ({ browser }) => {
    // Use a fresh context with no cookies
    const context = await browser.newContext();
    const page = await context.newPage();

    const protectedRoutes = [
      '/',
      '/account/settings',
      '/orgs/' + orgSlug,
      '/orgs/' + orgSlug + '/projects/' + projSlug + '/features',
      '/admin',
    ];

    for (const route of protectedRoutes) {
      await page.goto(route);
      await page.waitForLoadState('networkidle');
      await expect(page).toHaveURL(/\/login/);
    }

    await page.close();
    await context.close();
  });

  test('unauthenticated: login page is accessible', async ({ browser }) => {
    const context = await browser.newContext();
    const page = await context.newPage();

    await page.goto('/login');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('h1')).toContainText('Sign in');
    await expect(page.locator('input[name="email"]')).toBeVisible();

    await page.close();
    await context.close();
  });

  // Was "register page is accessible", which was only ever true on an empty
  // database. Registration is FIRST-USER-ONLY: RegisterPage redirects and
  // Register 403s once CountUsers > 0. That is a real access-control rule and
  // this asserts it, rather than asserting a form that must not be reachable.
  test('unauthenticated: registration is closed once an account exists', async ({ browser }) => {
    const context = await browser.newContext();
    const page = await context.newPage();

    await page.goto('/register');
    await page.waitForLoadState('networkidle');

    expect(page.url()).toContain('/login');
    await expect(page.locator('input[name="name"]')).toHaveCount(0);

    // The POST must be refused too, not merely hidden from the UI.
    const resp = await page.request.post('/register', {
      form: { name: 'Interloper', email: 'interloper@forgedesk.test', password: 'E2ETestPassword123!' },
    });
    expect(resp.status()).toBe(403);

    await page.close();
    await context.close();
  });

  test('unauthenticated: health endpoint returns 200', async ({ page }) => {
    const response = await page.request.get('/health');
    expect(response.status()).toBe(200);
    expect(await response.text()).toBe('ok');
  });
});
