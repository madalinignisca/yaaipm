// Sessions come from global setup: the app allows exactly one registration
// ever (first-user-only), and logging in per test would replay a TOTP code
// inside its 30s step, which ValidateTOTPOnce rejects (#136).
let rootCookies = [];

const { test, expect } = require('@playwright/test');
const { useSession, rootSession } = require('./helpers');

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
    // Shared root session: see the note at the top of this file (#136).

    rootCookies = rootSession().cookies;

    await useSession(page, rootCookies);

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

  test('create feature ticket', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await useSession(page, rootCookies);

    const response = await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'E2E Epic Feature',
        type: 'feature',
        priority: 'high',
        description: 'This is an E2E test feature',
      },
      headers: { Referer: '/orgs/ticket-org/projects/ticket-project/features' },
    });

    expect(response.status()).toBeLessThan(400);
  });

  test('create bug ticket', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await useSession(page, rootCookies);

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

  test('features page shows features', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await useSession(page, rootCookies);
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('E2E Epic Feature');
  });

  test('bugs page shows bugs', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await useSession(page, rootCookies);
    await page.goto('/orgs/ticket-org/projects/ticket-project/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('E2E Bug Report');
  });

  test('ticket detail page renders', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await useSession(page, rootCookies);
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');

    // Click on the feature ticket link
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

    await useSession(page, rootCookies);
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

  // #95: the ticket page rendered stored comments with a hardcoded "User"
  // byline while the HTMX partial for a just-posted comment showed the real
  // name, so reloading renamed the author. Only a browser sees both states,
  // which is why this assertion lives here and not in a handler test.
  //
  // Deliberately written without the `if (await ...isVisible())` guard used
  // by the tests above: a guard like that turns a missing ticket into a
  // silent pass, and this test exists to catch a rendering regression.
  test('comment author name survives a reload', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await useSession(page, rootCookies);
    await page.goto('/orgs/ticket-org/projects/ticket-project/features');

    await page.click('a:has-text("E2E Epic Feature")');
    await page.waitForLoadState('networkidle');

    const body = 'Comment authored by a named user';
    await page.fill('textarea[name="body"]', body);
    await page.click('button:has-text("Comment")');
    await page.waitForTimeout(1000);

    // Scope to the comment under test rather than counting bylines on the
    // page: every comment on this ticket now has the same author (one shared
    // session), so a global count says nothing about THIS comment.
    const authorName = rootSession().user.name;
    const mine = page.locator('#comments .comment').filter({ hasText: body });

    // Freshly posted, via the HTMX partial.
    await expect(mine).toHaveCount(1);
    await expect(mine.locator('.comment-author')).toHaveText(authorName);

    // Same comment after a full server-side re-render. Before #95 this
    // flipped to the generic "User".
    await page.reload();
    await page.waitForLoadState('networkidle');
    await expect(mine).toHaveCount(1);
    await expect(mine.locator('.comment-author')).toHaveText(authorName);
    await expect(page.locator('#comments .comment-author').filter({ hasText: /^User$/ })).toHaveCount(0);
  });

  test('update ticket status', async ({ page }) => {
    test.skip(!projectId, 'Project ID not found');

    await useSession(page, rootCookies);
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
