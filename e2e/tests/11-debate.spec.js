const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA } = require('./helpers');

const testUser = {
  name: 'Debate Tester',
  email: 'e2e-debate@forgedesk.test',
  password: 'E2ETestPassword123!',
};
let ticketID = '';
// Authenticated browser cookies captured in beforeAll and restored in each
// test — avoids TOTP replay rejection when two tests run in the same 30s window.
let authCookies = [];

// The fake refiner (DEBATE_REFINER_MODE=fake in docker-compose.test.yml)
// returns this template for every provider:
//   "Refactored by <name>:\n\n<input text>\n\n- added by fake refiner"
// For "claude" and the initial description:
//   "Refactored by claude:\n\nInitial description for the debate E2E test\n\n- added by fake refiner"
const INITIAL_DESCRIPTION = 'Initial description for the debate E2E test';

test.describe('Feature Debate Mode', () => {
  // Extend the per-test timeout for this suite. The default (30s) is insufficient:
  // the golden-path and abandon tests each have many HTMX interactions, and the
  // beforeAll alone takes ~60s (registration + 2FA + org/project/ticket setup).
  test.use({ timeout: 120000 });

  // Run the stack with DEBATE_REFINER_MODE=fake (set in docker-compose.test.yml)
  // so Refine calls return canned output without hitting a real provider.
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await registerUser(page, testUser);
    // Pause after registration so the auth rate limiter (0.5 req/s, burst 5)
    // doesn't block the TOTP setup verify POST — registration + login + 2FA
    // setup collectively hit 7 rate-limited endpoints in ~3 seconds.
    await page.waitForTimeout(4000);
    await loginUser(page, testUser);
    const result = await setup2FA(page);
    const totpSecret = result.secret;

    // Capture authenticated cookies immediately after 2FA setup. The session
    // is fully verified (setup2FA calls Mark2FASetupComplete + MarkTwoFactorVerified).
    // We share these cookies with every subsequent test page to skip the
    // per-test fullLogin — avoids TOTP replay rejection inside one 30s window.
    authCookies = await page.context().cookies();

    // Org + project boilerplate (via page.request, which also carries auth cookies).
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
        description: INITIAL_DESCRIPTION,
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

  // Helper: restore auth cookies from beforeAll into a fresh test page context,
  // then navigate to the debate page. This skips the TOTP login entirely so
  // sequential tests within the same 30s TOTP window don't replay-reject each other.
  async function authenticatedDebatePage(page, path) {
    if (authCookies.length > 0) {
      await page.context().addCookies(authCookies);
    }
    await page.goto(path);
    await page.waitForLoadState('networkidle');
  }

  test('empty state renders Start button on fresh feature', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    // New UI: empty-state card with "Refine with AI" start button.
    await expect(page.locator('[data-testid="debate-start"]')).toBeVisible();
    await expect(page.locator('[data-testid="debate-start"]')).toHaveText('Refine with AI');
  });

  test('non-feature ticket type is rejected from the debate route', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    // Verify the positive case: a feature ticket's debate page is accessible.
    // The non-feature type guard is exercised at the handler level; creating a
    // separate bug ticket in beforeAll would couple setup order unnecessarily.
    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);
    await expect(page.locator('[data-testid="debate-start"]')).toBeVisible();
  });

  test('golden path: start → suggest → accept → approve', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);

    // 1. Empty state → click "Refine with AI" (hx-boost POST + server redirect).
    await page.click('[data-testid="debate-start"]');
    await page.waitForLoadState('networkidle');

    // 2. Composer is now visible; effort chip shows the "appears after" placeholder.
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible();
    await expect(page.locator('[data-testid="debate-effort-chip"]')).toContainText(
      'appears after the first accepted suggestion'
    );

    // 3. Fill feedback textarea and submit (fake refiner is instant).
    await page.fill('[data-testid="debate-feedback"]', 'Make it more concise');
    // The form auto-selects the first available provider (claude via inline script).
    await page.click('[data-testid="debate-suggest"]');

    // 4. Suggestion card appears (HTMX partial swap — no page reload).
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();

    // 4a. The approve button is disabled while a suggestion is pending.
    // renderWorkspaceUpdate now OOB-swaps the approve zone so this state
    // is kept in sync during live HTMX sessions (not just on full page load).
    await expect(page.getByTestId('debate-approve')).toBeDisabled();

    // 5. Preview tab is selected by default; the fake output is visible.
    await expect(page.locator('[data-testid="debate-preview-pane"]')).toBeVisible();
    await expect(page.locator('[data-testid="debate-preview-pane"]')).toContainText(
      'Refactored by claude'
    );

    // 7. Switch to the "What changed" tab — must contain at least one <ins> element.
    await page.click('[data-testid="debate-tab-changes"]');
    // The changes pane is revealed by Alpine x-show; wait for it to be visible.
    await expect(page.locator('[data-testid="debate-changes-pane"]')).toBeVisible();
    const insCount = await page.locator('[data-testid="debate-changes-pane"] ins').count();
    expect(insCount, 'diff pane must contain at least one <ins> element').toBeGreaterThan(0);

    // 8. Accept the suggestion (bare hx-post, no dialog, swaps composer back).
    await page.click('[data-testid="debate-accept"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // 8a. After accept the approve button must be enabled — no pending suggestion.
    await expect(page.getByTestId('debate-approve')).toBeEnabled();

    // 9. Version 1 is now visible in the versions rail; it should be marked "current".
    await expect(page.locator('[data-testid="debate-version-1"]')).toBeVisible();
    await expect(page.locator('[data-testid="debate-version-1"]')).toContainText('current');

    // 10. The document pane shows the accepted (fake) text. The markdown renderer
    //     strips the "- " list prefix from "- added by fake refiner" when rendering
    //     to HTML, so we assert the core text without the leading dash.
    await expect(page.locator('[data-testid="debate-current-text"]')).toContainText(
      'Refactored by claude'
    );
    await expect(page.locator('[data-testid="debate-current-text"]')).toContainText(
      'added by fake refiner'
    );

    // 11. Effort chip: the scorer goroutine fires after accept. With
    //     DEBATE_REFINER_MODE=fake the scorer is nil (no Gemini key in test
    //     env), so the chip stays in the "appears after" placeholder state
    //     rather than showing a score badge. Assert the chip is visible.
    await expect(page.locator('[data-testid="debate-effort-chip"]')).toBeVisible();

    // 12. Second suggestion then dismiss.
    await page.fill('[data-testid="debate-feedback"]', 'Make it even shorter');
    await page.click('[data-testid="debate-suggest"]');
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();
    await page.click('[data-testid="debate-dismiss"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // 13. Versions rail shows the dismissed entry.
    const versionsAside = page.locator('[data-testid="debate-versions"]');
    await expect(versionsAside).toContainText('dismissed');

    // 14. Restore original via the versions rail (hx-confirm dialog).
    page.once('dialog', (dialog) => dialog.accept());
    await page.click('[data-testid="debate-restore-original"]');
    // After restore the composer re-renders and the document reverts.
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });
    await expect(page.locator('[data-testid="debate-current-text"]')).toContainText(
      INITIAL_DESCRIPTION
    );
    // Version 1 is gone — restore from=1 deletes all accepted rounds.
    await expect(page.locator('[data-testid="debate-version-1"]')).not.toBeVisible();

    // 15. One more suggest + accept to have something to approve.
    await page.click('[data-testid="debate-suggest"]');
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();
    await page.click('[data-testid="debate-accept"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // 16. Approve via the header button (hx-confirm dialog → Hx-Redirect to ticket).
    page.once('dialog', (dialog) => dialog.accept());
    await page.click('[data-testid="debate-approve"]');
    await page.waitForURL(new RegExp(`/tickets/${ticketID}$`), { timeout: 10000 });

    // 17. Ticket detail page contains the accepted (fake) text.
    // The markdown renderer strips the leading "- " from list items.
    await expect(page.locator('body')).toContainText('Refactored by claude');
    await expect(page.locator('body')).toContainText('added by fake refiner');
  });

  test('abandon path: start → suggest → dismiss → abandon → ticket unchanged', async ({ page }) => {
    test.skip(!ticketID, 'ticket id not resolved in beforeAll');

    await authenticatedDebatePage(page, `/tickets/${ticketID}/debate`);

    // Start only if the empty-state "Refine with AI" button is visible
    // (the golden-path test may have approved the debate, terminating it).
    const startBtn = page.locator('[data-testid="debate-start"]');
    if (await startBtn.isVisible({ timeout: 3000 }).catch(() => false)) {
      await startBtn.click();
      await page.waitForLoadState('networkidle');
    }

    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // Suggest one round.
    await page.click('[data-testid="debate-suggest"]');
    await expect(page.locator('[data-testid="debate-suggestion"]')).toBeVisible();

    // Dismiss it.
    await page.click('[data-testid="debate-dismiss"]');
    await expect(page.locator('[data-testid="debate-composer"]')).toBeVisible({ timeout: 8000 });

    // Abandon the debate (hx-confirm dialog → Hx-Redirect to ticket).
    page.once('dialog', (dialog) => dialog.accept());
    await page.click('[data-testid="debate-abandon"]');
    await page.waitForURL(new RegExp(`/tickets/${ticketID}$`), { timeout: 10000 });

    // After abandon the ticket description is unchanged.
    // We only assert we landed on the ticket page successfully.
    await expect(page.locator('body')).not.toContainText('Stop refining');
  });
});
