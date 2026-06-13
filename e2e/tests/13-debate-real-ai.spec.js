const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA } = require('./helpers');

// Opt-in real-AI smoke for Feature Debate Mode (issue #81, Part B).
//
// Unlike 12-debate-fake.spec.js, this drives the debate UI against LIVE
// provider APIs (Anthropic / OpenAI / Gemini) to catch vendor-side drift —
// SDK/response-shape changes that the fake refiner can't surface. It is
// gated behind DEBATE_REAL_AI=1 so it never runs in the per-PR CI suite:
//
//   # bring the app up with real provider keys (NOT fake mode), then:
//   npm run e2e:real-ai     # (sets DEBATE_REAL_AI=1)
//
// Cost is a few cents per run on the cheapest model tier per provider
// (Gemini Flash, GPT-5-mini, Claude Haiku). Serialized (workers:1).
const REAL = process.env.DEBATE_REAL_AI === '1';

const testUser = {
  name: 'Debate Real Tester',
  email: 'e2e-debate-real@forgedesk.test',
  password: 'E2ETestPassword123!',
};
const INITIAL_DESCRIPTION = 'Add a CSV export button to the invoices list page.';
let ticketID = '';
let authCookies = [];

test.describe('Feature Debate Mode — real AI smoke', () => {
  // Live API calls are slow; allow generous per-test budget.
  test.use({ timeout: 240000 });

  test.skip(!REAL, 'real-AI smoke is opt-in — set DEBATE_REAL_AI=1 and run against an app with live provider keys');

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await registerUser(page, testUser);
    await page.waitForTimeout(4000);
    await loginUser(page, testUser);
    await setup2FA(page);
    authCookies = await page.context().cookies();

    await page.request.post('/orgs', { form: { name: 'Debate Real Org' } });
    await page.request.post('/orgs/debate-real-org/projects', { form: { name: 'Debate Real Project' } });

    await page.goto('/orgs/debate-real-org/projects/debate-real-project/features');
    const projectID = await page.locator("input[name='project_id']").first().getAttribute('value');
    expect(projectID, 'project id must be discoverable').toBeTruthy();

    const resp = await page.request.post('/tickets', {
      form: {
        project_id: projectID,
        title: 'E2E real-AI debate feature',
        type: 'feature',
        priority: 'medium',
        description: INITIAL_DESCRIPTION,
      },
      headers: { Referer: '/orgs/debate-real-org/projects/debate-real-project/features' },
    });
    expect(resp.status(), 'create-ticket response').toBeLessThan(400);

    await page.goto('/orgs/debate-real-org/projects/debate-real-project/features');
    const href = await page.locator("a[href^='/tickets/']").first().getAttribute('href');
    ticketID = href ? href.split('/').pop() : '';
    expect(ticketID, 'ticket id must be discoverable').not.toEqual('');

    await page.close();
  });

  async function authenticatedDebatePage(page, path) {
    if (authCookies.length > 0) {
      await page.context().addCookies(authCookies);
    }
    await page.goto(path);
    await page.waitForLoadState('networkidle');
  }

  async function ensureComposer(page) {
    const startBtn = page.locator('[data-testid="debate-start"]');
    if (await startBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
      await startBtn.click();
      await page.waitForLoadState('networkidle');
    }
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });
  }

  // Select a provider radio (visually hidden; click its label) and suggest.
  async function suggestWith(page, provider, feedback) {
    await page.locator(`label[data-provider="${provider}"]`).click();
    if (feedback !== undefined) {
      await page.fill('[data-testid="debate-feedback"]', feedback);
    }
    await page.click('[data-testid="debate-suggest"]');
    // Real provider latency — wait up to 90s for the suggestion card.
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible({ timeout: 90000 });
  }

  test('each configured provider returns a non-empty real suggestion', async ({ page }) => {
    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await ensureComposer(page);

    const providers = await page.locator('input[name="provider"]').evaluateAll((els) => els.map((e) => e.value));
    expect(providers.length, 'at least one provider must be configured').toBeGreaterThan(0);

    for (const provider of providers) {
      await suggestWith(page, provider, 'Add acceptance criteria and edge cases.');

      // Preview pane must contain real, non-trivial output.
      const preview = page.locator('[data-testid="debate-preview-pane"]');
      await expect(preview).toBeVisible();
      const text = (await preview.innerText()).trim();
      expect(text.length, `provider ${provider} returned empty output`).toBeGreaterThan(20);

      // The word-level diff must render at least one change.
      await page.click('[data-testid="debate-tab-changes"]');
      await expect(page.locator('[data-testid="debate-changes-pane"]')).toBeVisible();
      const changes = await page.locator('[data-testid="debate-changes-pane"] ins, [data-testid="debate-changes-pane"] del').count();
      expect(changes, `provider ${provider} produced no diff`).toBeGreaterThan(0);

      // Dismiss so the next provider starts from the composer.
      await page.click('[data-testid="debate-dismiss"]');
      await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });
    }
  });

  test('golden journey with provider rotation + scorer populates the effort chip', async ({ page }) => {
    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await ensureComposer(page);

    const providers = await page.locator('input[name="provider"]').evaluateAll((els) => els.map((e) => e.value));
    // Rotate through up to two distinct providers across two accepted rounds.
    const rotation = providers.slice(0, 2);

    for (const provider of rotation) {
      await suggestWith(page, provider, `Refine using ${provider}.`);
      await page.click('[data-testid="debate-accept"]');
      await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 12000 });
    }

    // Scorer integration: after a real accept the effort chip self-polls
    // (hx-get every 3s) until the Gemini scorer returns. Wait for the score
    // badge to replace the "appears after" / "estimating…" states.
    const chip = page.locator('[data-testid="debate-effort-chip"]');
    await expect(chip).toContainText('/10', { timeout: 120000 });
    const chipText = await chip.innerText();
    const m = chipText.match(/Effort\s+(\d+)\/10/);
    expect(m, `effort chip text was: ${chipText}`).not.toBeNull();
    const score = parseInt(m[1], 10);
    expect(score, 'effort score must be 1..10').toBeGreaterThanOrEqual(1);
    expect(score, 'effort score must be 1..10').toBeLessThanOrEqual(10);
    expect(chipText, 'effort chip must include an hours estimate').toMatch(/~\d+h/);

    // Approve writes the accepted text back to the ticket.
    page.once('dialog', (dialog) => dialog.accept());
    await page.click('[data-testid="debate-approve"]');
    await page.waitForURL(new RegExp(`/tickets/${ticketID}$`), { timeout: 15000 });
    // The ticket body now reflects refined (not the original) text.
    await expect(page.locator('body')).not.toContainText('Stop refining');
  });
});
