const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA } = require('./helpers');

// Fake-backed debate coverage that complements 11-debate.spec.js. Spec 11
// drives the happy path (start → suggest → accept → approve, dismiss,
// restore-original, abandon). This spec covers the branches it doesn't:
//   - reject (dismiss) leaves current_text untouched + pre-fills feedback
//   - a suggestion with an empty feedback box still works
//   - the multi-round undo cascade via the versions rail (restore vN)
//
// Runs against DEBATE_REFINER_MODE=fake (docker-compose.test.yml), so the
// refiner returns canned output:
//   "Refactored by <provider>:\n\n<input>\n\n- added by fake refiner"
const testUser = {
  name: 'Debate Fake Tester',
  email: 'e2e-debate-fake@forgedesk.test',
  password: 'E2ETestPassword123!',
};
const INITIAL_DESCRIPTION = 'Initial description for the fake debate E2E test';
let ticketID = '';
let authCookies = [];

test.describe('Feature Debate Mode — fake refiner branches', () => {
  test.use({ timeout: 120000 });

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await registerUser(page, testUser);
    // Pause so the auth rate limiter (0.5 req/s, burst 5) doesn't block the
    // TOTP verify POST — registration + login + 2FA hit several limited routes.
    await page.waitForTimeout(4000);
    await loginUser(page, testUser);
    await setup2FA(page);
    authCookies = await page.context().cookies();

    await page.request.post('/orgs', { form: { name: 'Debate Fake Org' } });
    await page.request.post('/orgs/debate-fake-org/projects', { form: { name: 'Debate Fake Project' } });

    await page.goto('/orgs/debate-fake-org/projects/debate-fake-project/features');
    const projectID = await page.locator("input[name='project_id']").first().getAttribute('value');
    expect(projectID, 'project id must be discoverable').toBeTruthy();

    const resp = await page.request.post('/tickets', {
      form: {
        project_id: projectID,
        title: 'E2E fake debate feature',
        type: 'feature',
        priority: 'medium',
        description: INITIAL_DESCRIPTION,
      },
      headers: { Referer: '/orgs/debate-fake-org/projects/debate-fake-project/features' },
    });
    expect(resp.status(), 'create-ticket response').toBeLessThan(400);

    await page.goto('/orgs/debate-fake-org/projects/debate-fake-project/features');
    const href = await page.locator("a[href^='/tickets/']").first().getAttribute('href');
    ticketID = href ? href.split('/').pop() : '';
    expect(ticketID, 'ticket id must be discoverable on features tab').not.toEqual('');

    await page.close();
  });

  async function authenticatedDebatePage(page, path) {
    if (authCookies.length > 0) {
      await page.context().addCookies(authCookies);
    }
    await page.goto(path);
    await page.waitForLoadState('networkidle');
  }

  // Ensure the debate is in the composer state (active, no pending suggestion),
  // starting it if the page is still showing the empty state.
  async function ensureComposer(page) {
    const startBtn = page.locator('[data-testid="debate-start"]');
    if (await startBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
      await startBtn.click();
      await page.waitForLoadState('networkidle');
    }
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });
  }

  test('reject (dismiss) leaves current text unchanged and pre-fills feedback', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await ensureComposer(page);

    // Document starts at the original text (no accepted rounds yet).
    await expect(page.locator('[data-testid="debate-current-text"]')).toContainText(INITIAL_DESCRIPTION);

    // Suggest with a distinctive feedback string, then dismiss (= reject).
    const feedback = 'Tighten the wording please';
    await page.fill('[data-testid="debate-feedback"]', feedback);
    await page.click('[data-testid="debate-suggest"]');
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();

    await page.click('[data-testid="debate-dismiss"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // Reject must NOT touch the document — current text is still the original.
    await expect(page.locator('[data-testid="debate-current-text"]')).toContainText(INITIAL_DESCRIPTION);
    await expect(page.locator('[data-testid="debate-current-text"]')).not.toContainText('added by fake refiner');

    // The rejected round's feedback is pre-filled back into the composer so the
    // user can tweak and retry (RejectRound → renderWorkspaceUpdate).
    await expect(page.locator('[data-testid="debate-feedback"]')).toHaveValue(feedback);

    // Versions rail records the dismissed suggestion.
    await expect(page.locator('[data-testid="debate-versions"]')).toContainText('dismissed');

    // Approve stays enabled — a dismissed suggestion is not pending.
    await expect(page.getByTestId('debate-approve')).toBeEnabled();
  });

  test('a suggestion with empty feedback still works', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await ensureComposer(page);

    // Clear any pre-filled feedback and suggest with an empty box.
    await page.fill('[data-testid="debate-feedback"]', '');
    await page.click('[data-testid="debate-suggest"]');

    // The fake refiner returns output regardless of feedback — suggestion shows.
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();
    await expect(page.locator('[data-testid="debate-preview-pane"]')).toContainText('Refactored by');

    // Clean up so the next test starts from the composer.
    await page.click('[data-testid="debate-dismiss"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });
  });

  test('undo cascade: accept 3 rounds then restore v1 removes v2 and v3', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await ensureComposer(page);

    // Accept three rounds in sequence. Each suggest uses the fake refiner; each
    // accept swaps the composer back so the next suggest can fire.
    for (let i = 1; i <= 3; i++) {
      await page.fill('[data-testid="debate-feedback"]', `round ${i}`);
      await page.click('[data-testid="debate-suggest"]');
      await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();
      await page.click('[data-testid="debate-accept"]');
      await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });
    }

    // Three accepted versions; v3 is current.
    await expect(page.locator('[data-testid="debate-version-3"]')).toContainText('current');
    await expect(page.locator('[data-testid="debate-version-1"]')).toBeVisible();
    await expect(page.locator('[data-testid="debate-version-2"]')).toBeVisible();

    // Restore v1 (hx-confirm → undo?from=2). Deletes rounds 2 and 3, leaving v1.
    page.once('dialog', (dialog) => dialog.accept());
    await page.click('[data-testid="debate-restore-1"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // v1 is now the current/only accepted version; v2 and v3 are gone (undo
    // deletes the rolled-back rounds, it does not mark them rejected).
    await expect(page.locator('[data-testid="debate-version-1"]')).toContainText('current');
    await expect(page.locator('[data-testid="debate-version-2"]')).toHaveCount(0);
    await expect(page.locator('[data-testid="debate-version-3"]')).toHaveCount(0);
  });

  test('feedback draft is auto-saved to localStorage and restored after reload', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await ensureComposer(page);

    // Type a draft, then reload — issue #65 stashes it to localStorage so a
    // failed AI call or an accidental navigation doesn't lose typed feedback.
    const draft = 'remember this feedback draft';
    await page.fill('[data-testid="debate-feedback"]', draft);
    await page.reload();
    await page.waitForLoadState('networkidle');
    await ensureComposer(page);
    await expect(page.locator('[data-testid="debate-feedback"]')).toHaveValue(draft);

    // Emptying the box clears the stored draft, so it does not restore later.
    await page.fill('[data-testid="debate-feedback"]', '');
    await page.reload();
    await page.waitForLoadState('networkidle');
    await ensureComposer(page);
    await expect(page.locator('[data-testid="debate-feedback"]')).toHaveValue('');
  });

  test('a successful suggestion clears the saved feedback draft', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await ensureComposer(page);

    // Type feedback and send it. On a SUCCESSFUL suggest the draft must be
    // cleared, so consumed feedback doesn't resurface on a later reload.
    await page.fill('[data-testid="debate-feedback"]', 'sent feedback that should clear');
    await page.click('[data-testid="debate-suggest"]');
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();
    await page.click('[data-testid="debate-dismiss"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // Reload from a clean GET: the consumed draft is gone (cleared on the
    // successful suggest), so nothing is restored into the empty composer.
    await page.reload();
    await page.waitForLoadState('networkidle');
    await ensureComposer(page);
    await expect(page.locator('[data-testid="debate-feedback"]')).toHaveValue('');
  });
});
