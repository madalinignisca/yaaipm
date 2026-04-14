const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin } = require('./helpers');

const testUser = {
  name: 'Debate Tester',
  email: 'e2e-debate@forgedesk.test',
  password: 'E2ETestPassword123!',
};
let totpSecret = '';
let ticketID = '';

test.describe('Feature Debate Mode', () => {
  // Run the stack with DEBATE_REFINER_MODE=fake so Refine calls return
  // canned output without hitting a real provider. cmd/server/main.go
  // panics at startup if this env var is set against a non-local
  // BaseURL, so prod is safe from accidentally inheriting the fake.
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await registerUser(page, testUser);
    await loginUser(page, testUser);
    const result = await setup2FA(page);
    totpSecret = result.secret;

    // Org + project boilerplate.
    await page.request.post('/orgs', { form: { name: 'Debate Org' } });
    await page.request.post('/orgs/debate-org/projects', { form: { name: 'Debate Project' } });

    // Resolve project id from the features page.
    await page.goto('/orgs/debate-org/projects/debate-project/features');
    const html = await page.content();
    const match = html.match(/project_id[^>]*value="([^"]+)"/);
    const projectID = match ? match[1] : '';
    expect(projectID, 'project id must be discoverable').not.toEqual('');

    // Create a feature ticket and remember its id.
    const resp = await page.request.post('/tickets', {
      form: {
        project_id: projectID,
        title: 'E2E debate feature',
        type: 'feature',
        priority: 'medium',
        description: 'Initial description for the debate E2E test',
      },
      headers: { Referer: '/orgs/debate-org/projects/debate-project/features' },
    });
    expect(resp.status(), 'create-ticket response').toBeLessThan(400);

    // Find the ticket ID by navigating the features page and pulling
    // the first ticket link.
    await page.goto('/orgs/debate-org/projects/debate-project/features');
    const linkHTML = await page.content();
    const linkMatch = linkHTML.match(/href="\/tickets\/([0-9a-f-]{36})"/);
    ticketID = linkMatch ? linkMatch[1] : '';
    expect(ticketID, 'ticket id must be discoverable on features tab').not.toEqual('');

    await page.close();
  });

  test('empty state renders Start button on fresh feature', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');
    await fullLogin(page, { ...testUser, totpSecret });

    await page.goto(`/tickets/${ticketID}/debate`);
    // Skeleton/real template both surface the empty-state copy.
    await expect(page.locator('text=No active debate')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Start debate' })).toBeVisible();
  });

  test('golden path: start → round → accept → approve', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');
    test.skip(
      !process.env.DEBATE_REFINER_MODE || process.env.DEBATE_REFINER_MODE !== 'fake',
      'this test requires DEBATE_REFINER_MODE=fake in the server env'
    );
    await fullLogin(page, { ...testUser, totpSecret });

    // 1. Empty state → Start.
    await page.goto(`/tickets/${ticketID}/debate`);
    await page.click('button:has-text("Start debate")');
    await page.waitForLoadState('networkidle');

    // 2. Seed card present, AI-picker visible.
    await expect(page.locator('.debate-seed')).toBeVisible();
    await expect(page.locator('.next-round')).toBeVisible();

    // 3. Click Claude → fake refiner returns immediately.
    await page.click('button[name="provider"][value="claude"]');
    await page.waitForSelector('.round-in_review', { timeout: 15000 });

    // 4. In-review diff rendered server-side, visible.
    await expect(page.locator('.diff-block')).toBeVisible();

    // 5. Accept → round flips to accepted, sidebar may update.
    await page.click('button:has-text("Accept")');
    await page.waitForSelector('.round-accepted, .round.round-accepted', {
      timeout: 10000,
    });

    // 6. Approve final → Hx-Redirect back to ticket detail.
    await page.click('button:has-text("Approve final")');
    await page.waitForURL(new RegExp(`/tickets/${ticketID}$`));

    // 7. Ticket description now contains the fake refactor marker.
    await expect(page.locator('body')).toContainText('Refactored by claude');
  });
});
