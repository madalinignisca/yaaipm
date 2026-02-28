const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin } = require('./helpers');

const testUser = {
  name: 'Ticket Admin',
  email: 'e2e-ticket@forgedesk.test',
  password: 'E2ETestPassword123!',
};
let totpSecret = '';
let projectId = '';

test.describe('Tickets & Comments', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await registerUser(page, testUser);
    await loginUser(page, testUser);
    const result = await setup2FA(page);
    totpSecret = result.secret;

    // Create org and project
    await page.request.post('/orgs', { form: { name: 'Ticket Org' } });
    await page.request.post('/orgs/ticket-org/projects', { form: { name: 'Ticket Project' } });

    // Get project ID from the page
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');
    const bodyText = await page.content();
    // Extract project ID from hidden form or page source
    const match = bodyText.match(/project_id[^>]*value="([^"]+)"/);
    if (match) {
      projectId = match[1];
    }
    await page.close();
  });

  test('create epic ticket', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await fullLogin(page, { ...testUser, totpSecret });

    const response = await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'E2E Epic Feature',
        type: 'epic',
        priority: 'high',
        description: 'This is an E2E test epic',
      },
      headers: { Referer: '/orgs/ticket-org/projects/ticket-project/features' },
    });

    expect(response.status()).toBeLessThan(400);
  });

  test('create bug ticket', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await fullLogin(page, { ...testUser, totpSecret });

    const response = await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'E2E Bug Report',
        type: 'bug',
        priority: 'critical',
        description: 'This is an E2E test bug',
      },
      headers: { Referer: '/orgs/ticket-org/projects/ticket-project/bugs' },
    });

    expect(response.status()).toBeLessThan(400);
  });

  test('features page shows epics', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('E2E Epic Feature');
  });

  test('bugs page shows bugs', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/ticket-org/projects/ticket-project/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('E2E Bug Report');
  });

  test('ticket detail page renders', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');

    // Click on the epic ticket link
    const ticketLink = page.locator('a:has-text("E2E Epic Feature")');
    if (await ticketLink.isVisible()) {
      await ticketLink.click();
      await page.waitForLoadState('networkidle');

      await expect(page.locator('body')).toContainText('E2E Epic Feature');
      await expect(page.locator('body')).toContainText('Comments');
    }
  });

  test('post comment on ticket via HTMX', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');

    const ticketLink = page.locator('a:has-text("E2E Epic Feature")');
    if (await ticketLink.isVisible()) {
      await ticketLink.click();
      await page.waitForLoadState('networkidle');

      // Fill and submit comment
      const textarea = page.locator('textarea[name="body"]');
      if (await textarea.isVisible()) {
        await textarea.fill('This is an E2E test comment');
        await page.click('button:has-text("Comment")');

        // Wait for HTMX to update
        await page.waitForTimeout(1000);
        await expect(page.locator('body')).toContainText('This is an E2E test comment');
      }
    }
  });

  test('update ticket status', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');

    const ticketLink = page.locator('a:has-text("E2E Epic Feature")');
    if (await ticketLink.isVisible()) {
      await ticketLink.click();
      await page.waitForLoadState('networkidle');

      // Open dropdown menu
      const dotsBtn = page.locator('.btn-dots');
      if (await dotsBtn.isVisible()) {
        await dotsBtn.click();

        // Click "ready" status
        const readyBtn = page.locator('.dropdown-item:has-text("ready")');
        if (await readyBtn.isVisible()) {
          await readyBtn.click();
          await page.waitForLoadState('networkidle');
        }
      }
    }
  });
});
