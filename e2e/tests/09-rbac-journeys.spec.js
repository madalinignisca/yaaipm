const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, verify2FA, fullLogin, generateTOTP } = require('./helpers');

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

let superadminTotpSecret = '';
let clientTotpSecret = '';
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

    // Register superadmin (first user in this test run)
    await registerUser(page, superadminUser);
    await loginUser(page, superadminUser);
    const result = await setup2FA(page);
    superadminTotpSecret = result.secret;

    // Wait for dashboard/redirect after 2FA setup
    await page.waitForURL(url => !url.href.includes('setup-2fa'), { timeout: 10000 }).catch(() => {});

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

    // Invite the client user to the org
    const inviteResp = await page.request.post('/orgs/' + orgSlug + '/invitations', {
      form: { email: clientUser.email, role: 'member' },
    });
    expect(inviteResp.status()).toBeLessThan(400);

    // Parse invite URL from the response HTML
    const inviteHtml = await inviteResp.text();
    const inviteMatch = inviteHtml.match(/value="([^"]*\/invite\/[^"]+)"/);
    let inviteUrl = '';
    if (inviteMatch) {
      inviteUrl = inviteMatch[1];
    }

    await page.close();

    // Register client user via the invite link
    const clientPage = await browser.newPage();
    if (inviteUrl) {
      // Navigate to the invite URL to register
      await clientPage.goto(inviteUrl);
      await clientPage.waitForLoadState('networkidle');

      // Fill the invite registration form (email is pre-filled from invitation)
      await clientPage.fill('input[name="name"]', clientUser.name);
      await clientPage.locator('input[name="password"]').evaluate(el => el.removeAttribute('minlength'));
      await clientPage.fill('input[name="password"]', clientUser.password);
      await clientPage.click('button[type="submit"]');
      await clientPage.waitForLoadState('networkidle');

      // Should redirect to 2FA setup
      const clientResult = await setup2FA(clientPage);
      clientTotpSecret = clientResult.secret;
    } else {
      // Fallback: register normally (client won't be in the org, tests will adapt)
      await registerUser(clientPage, clientUser);
      await loginUser(clientPage, clientUser);
      const clientResult = await setup2FA(clientPage);
      clientTotpSecret = clientResult.secret;
    }
    await clientPage.close();
  });

  // ──────────────────────────────────────────────
  // Superadmin journeys
  // ──────────────────────────────────────────────
  test('superadmin: can access admin panel', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/admin');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Admin Panel');
  });

  test('superadmin: can access org page', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/orgs/' + orgSlug);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Org');
  });

  test('superadmin: can access project brief', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/brief');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Brief');
  });

  test('superadmin: can access project features', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Features');
    await expect(page.locator('body')).toContainText('RBAC Test Feature');
  });

  test('superadmin: can access project bugs', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Bugs');
    await expect(page.locator('body')).toContainText('RBAC Test Bug');
  });

  test('superadmin: can access project gantt', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/gantt');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Timeline');
  });

  test('superadmin: can access project archived tab', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/archived');
    await page.waitForLoadState('networkidle');

    // Archived page should load (200) for staff
    await expect(page.locator('body')).toContainText('Archived');
  });

  test('superadmin: can access project costs', async ({ page }) => {
    test.skip(!superadminTotpSecret, 'Superadmin not set up');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/costs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Costs');
  });

  test('superadmin: ticket detail shows staff controls', async ({ page }) => {
    test.skip(!superadminTotpSecret || !featureTicketId, 'Setup incomplete');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });
    await page.goto('/tickets/' + featureTicketId);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Test Feature');

    // Staff should see the dropdown button for status/agent controls
    const dotsBtn = page.locator('.btn-dots');
    await expect(dotsBtn).toBeVisible();
  });

  test('superadmin: can create tickets', async ({ page }) => {
    test.skip(!superadminTotpSecret || !projectId, 'Setup incomplete');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

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
    test.skip(!superadminTotpSecret || !featureTicketId, 'Setup incomplete');

    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

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
    test.skip(!clientTotpSecret, 'Client not set up');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });

    const response = await page.request.get('/admin');
    expect(response.status()).toBe(403);
  });

  test('client: can access their org page', async ({ page }) => {
    test.skip(!clientTotpSecret, 'Client not set up');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
    await page.goto('/orgs/' + orgSlug);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Org');
  });

  test('client: can access project brief', async ({ page }) => {
    test.skip(!clientTotpSecret, 'Client not set up');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/brief');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Brief');
  });

  test('client: can access project features', async ({ page }) => {
    test.skip(!clientTotpSecret, 'Client not set up');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Features');
    await expect(page.locator('body')).toContainText('RBAC Test Feature');
  });

  test('client: can access project bugs', async ({ page }) => {
    test.skip(!clientTotpSecret, 'Client not set up');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Bugs');
    await expect(page.locator('body')).toContainText('RBAC Test Bug');
  });

  test('client: can access project gantt', async ({ page }) => {
    test.skip(!clientTotpSecret, 'Client not set up');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
    await page.goto('/orgs/' + orgSlug + '/projects/' + projSlug + '/gantt');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Timeline');
  });

  test('client: cannot access archived tab (403)', async ({ page }) => {
    test.skip(!clientTotpSecret, 'Client not set up');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });

    const response = await page.request.get('/orgs/' + orgSlug + '/projects/' + projSlug + '/archived');
    expect(response.status()).toBe(403);
  });

  test('client: ticket detail does NOT show staff dropdown', async ({ page }) => {
    test.skip(!clientTotpSecret || !featureTicketId, 'Setup incomplete');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
    await page.goto('/tickets/' + featureTicketId);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('RBAC Test Feature');

    // Client should NOT see the staff dropdown button
    const dotsBtn = page.locator('.btn-dots');
    await expect(dotsBtn).toHaveCount(0);
  });

  test('client: can create tickets', async ({ page }) => {
    test.skip(!clientTotpSecret || !projectId, 'Setup incomplete');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });

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
    test.skip(!clientTotpSecret || !featureTicketId, 'Setup incomplete');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });
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
    test.skip(!clientTotpSecret || !featureTicketId, 'Setup incomplete');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });

    const response = await page.request.post('/tickets/' + featureTicketId + '/archive');
    expect(response.status()).toBe(403);
  });

  test('client: cannot delete tickets (403)', async ({ page }) => {
    test.skip(!clientTotpSecret || !featureTicketId, 'Setup incomplete');

    await fullLogin(page, { ...clientUser, totpSecret: clientTotpSecret });

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

  test('unauthenticated: register page is accessible', async ({ browser }) => {
    const context = await browser.newContext();
    const page = await context.newPage();

    await page.goto('/register');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('input[name="name"]')).toBeVisible();
    await expect(page.locator('input[name="email"]')).toBeVisible();

    await page.close();
    await context.close();
  });

  test('unauthenticated: health endpoint returns 200', async ({ page }) => {
    const response = await page.request.get('/health');
    expect(response.status()).toBe(200);
    expect(await response.text()).toBe('ok');
  });
});
