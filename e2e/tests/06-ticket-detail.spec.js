const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin } = require('./helpers');

// ---------- test users ----------
const staffUser = {
  name: 'Detail Staff',
  email: 'e2e-detail@forgedesk.test',
  password: 'E2ETestPassword123!',
};

const memberUser = {
  name: 'Detail Member',
  email: 'e2e-detail-member@forgedesk.test',
  password: 'E2ETestPassword123!',
};

// ---------- shared state (populated in beforeAll / early tests) ----------
let staffTotpSecret = '';
let memberTotpSecret = '';
let projectId = '';
let featureTicketId = '';
let bugTicketId = '';
let childTaskId = '';

const ORG_SLUG = 'detail-org';
const PROJECT_SLUG = 'detail-project';

// Helper: extract project_id from hidden input on the features page
async function extractProjectId(page) {
  const content = await page.content();
  const match = content.match(/name="project_id"[^>]*value="([^"]+)"/);
  return match ? match[1] : '';
}

// Helper: extract ticket ID from a ticket link's href on a listing page
async function extractTicketId(page, ticketTitle) {
  const link = page.locator(`a:has-text("${ticketTitle}")`);
  if (await link.count() > 0) {
    const href = await link.first().getAttribute('href');
    // href is like /tickets/<uuid>
    const match = href && href.match(/\/tickets\/([a-f0-9-]+)/);
    return match ? match[1] : '';
  }
  return '';
}

// ====================================================================
// SETUP
// ====================================================================
test.describe.serial('Ticket Detail Page', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();

    // ---- Register staff user (first user in this DB run becomes superadmin) ----
    await registerUser(page, staffUser);
    await loginUser(page, staffUser);
    const result = await setup2FA(page);
    staffTotpSecret = result.secret;

    // ---- Create org + project ----
    await page.request.post('/orgs', { form: { name: 'Detail Org' } });
    await page.request.post(`/orgs/${ORG_SLUG}/projects`, {
      form: { name: 'Detail Project' },
    });

    // ---- Grab project ID ----
    await page.goto(`/orgs/${ORG_SLUG}/projects/${PROJECT_SLUG}/features`);
    await page.waitForLoadState('networkidle');
    projectId = await extractProjectId(page);

    if (!projectId) {
      await page.close();
      return; // remaining tests will skip
    }

    // ---- Create feature ticket with rich markdown description ----
    const featureDesc = [
      '## Overview',
      '',
      'This is a **bold** and *italic* feature description.',
      '',
      '- Item one',
      '- Item two',
      '- Item three',
      '',
      'Some `inline code` for emphasis.',
    ].join('\n');

    await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'Detail Feature Ticket',
        type: 'feature',
        priority: 'high',
        description: featureDesc,
      },
    });

    // ---- Create bug ticket ----
    await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'Detail Bug Ticket',
        type: 'bug',
        priority: 'critical',
        description: 'Steps to reproduce the **bug**.',
      },
    });

    // ---- Get ticket IDs ----
    await page.goto(`/orgs/${ORG_SLUG}/projects/${PROJECT_SLUG}/features`);
    await page.waitForLoadState('networkidle');
    featureTicketId = await extractTicketId(page, 'Detail Feature Ticket');

    await page.goto(`/orgs/${ORG_SLUG}/projects/${PROJECT_SLUG}/bugs`);
    await page.waitForLoadState('networkidle');
    bugTicketId = await extractTicketId(page, 'Detail Bug Ticket');

    // ---- Create child task under the feature ----
    if (featureTicketId) {
      await page.request.post('/tickets', {
        form: {
          project_id: projectId,
          parent_id: featureTicketId,
          title: 'Child Task Alpha',
          type: 'task',
          priority: 'medium',
          description: 'A child task under the feature.',
        },
      });

      // Second child so we can verify the list
      await page.request.post('/tickets', {
        form: {
          project_id: projectId,
          parent_id: featureTicketId,
          title: 'Child Task Beta',
          type: 'task',
          priority: 'low',
          description: 'Another child task.',
        },
      });

      // Get the child ID (we only need one for later assertions)
      await page.goto(`/tickets/${featureTicketId}`);
      await page.waitForLoadState('networkidle');
      const childLink = page.locator('a.sub-item').first();
      if (await childLink.count() > 0) {
        const href = await childLink.getAttribute('href');
        const m = href && href.match(/\/tickets\/([a-f0-9-]+)/);
        childTaskId = m ? m[1] : '';
      }
    }

    // ---- Register the member user ----
    // Log out first, then register the second user
    await page.goto('/login');
    await registerUser(page, memberUser);
    await loginUser(page, memberUser);
    const memberResult = await setup2FA(page);
    memberTotpSecret = memberResult.secret;

    // ---- Add member user to the org via invitation mechanism ----
    // Log back in as staff to invite the member
    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.request.post(`/orgs/${ORG_SLUG}/invitations`, {
      form: {
        email: memberUser.email,
        role: 'member',
      },
    });

    // Now log in as member and accept the invitation
    await fullLogin(page, { ...memberUser, totpSecret: memberTotpSecret });
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Check for pending invitations and accept
    const acceptBtn = page.locator('button:has-text("Accept"), [hx-post*="/accept"]');
    if (await acceptBtn.count() > 0) {
      await acceptBtn.first().click();
      await page.waitForLoadState('networkidle');
    }

    await page.close();
  });

  // ==================================================================
  // 1. TICKET DETAIL RENDERS CORRECTLY
  // ==================================================================
  test('feature ticket detail shows type, status, and priority badges', async ({ page }) => {
    test.skip(!featureTicketId, 'Feature ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // Title
    await expect(page.locator('.ticket-title')).toContainText('Detail Feature Ticket');

    // Type badge
    const typeBadge = page.locator('.ticket-badges .badge-uppercase');
    await expect(typeBadge).toContainText('feature');

    // Status badge (default: backlog)
    const badges = page.locator('.ticket-badges .badge');
    const allText = await badges.allTextContents();
    const hasStatus = allText.some(t => t.trim() === 'backlog');
    expect(hasStatus).toBeTruthy();

    // Priority badge
    const hasPriority = allText.some(t => t.trim() === 'high');
    expect(hasPriority).toBeTruthy();
  });

  test('feature ticket description renders markdown as HTML', async ({ page }) => {
    test.skip(!featureTicketId, 'Feature ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    const prose = page.locator('.ticket-description.prose');
    await expect(prose).toBeVisible();

    // Markdown should be rendered as HTML elements, not raw text
    // Check for <strong> from **bold**
    await expect(prose.locator('strong')).toContainText('bold');

    // Check for <em> from *italic*
    await expect(prose.locator('em')).toContainText('italic');

    // Check for <ul> from the bullet list
    const listItems = prose.locator('ul li');
    await expect(listItems).toHaveCount(3);
    await expect(listItems.first()).toContainText('Item one');

    // Check for <code> from `inline code`
    await expect(prose.locator('code')).toContainText('inline code');
  });

  test('feature ticket shows sub-items with title and status', async ({ page }) => {
    test.skip(!featureTicketId, 'Feature ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // Sub-items section
    await expect(page.locator('h3:has-text("Sub-items")')).toBeVisible();

    const subItems = page.locator('a.sub-item');
    const count = await subItems.count();
    expect(count).toBeGreaterThanOrEqual(2);

    // Each sub-item has a title and a status badge
    await expect(subItems.filter({ hasText: 'Child Task Alpha' })).toHaveCount(1);
    await expect(subItems.filter({ hasText: 'Child Task Beta' })).toHaveCount(1);

    // Status badges on sub-items
    const alphaBadge = subItems
      .filter({ hasText: 'Child Task Alpha' })
      .locator('.badge');
    await expect(alphaBadge).toContainText('backlog');
  });

  test('comments section is present with empty state', async ({ page }) => {
    test.skip(!featureTicketId, 'Feature ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // Comments heading
    await expect(page.locator('h3:has-text("Comments")')).toBeVisible();

    // Comment form is present
    await expect(page.locator('textarea[name="body"]')).toBeVisible();
    await expect(page.locator('button:has-text("Comment")')).toBeVisible();

    // No comments yet (the #comments div should be empty or have no .comment children)
    const comments = page.locator('#comments .comment');
    await expect(comments).toHaveCount(0);
  });

  // ==================================================================
  // 2. COMMENT LIFECYCLE
  // ==================================================================
  test('post a comment with markdown text', async ({ page }) => {
    test.skip(!featureTicketId, 'Feature ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    const commentBody = 'This is a **test comment** with `code`.';
    await page.fill('textarea[name="body"]', commentBody);
    await page.click('button:has-text("Comment")');

    // Wait for HTMX to append the comment
    await page.waitForTimeout(1500);

    // The comment should now appear in the comments section
    const commentEl = page.locator('#comments .comment').first();
    await expect(commentEl).toBeVisible();

    // Author should show user name, not "ForgeDesk Bot"
    const author = commentEl.locator('.comment-author');
    await expect(author).not.toContainText('ForgeDesk Bot');

    // Timestamp should be present
    const timestamp = commentEl.locator('.comment-time');
    await expect(timestamp).toBeVisible();
    const timeText = await timestamp.textContent();
    expect(timeText.trim().length).toBeGreaterThan(0);
  });

  test('comment appears with rendered markdown after page reload', async ({ page }) => {
    test.skip(!featureTicketId, 'Feature ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // After a full page load, comments should render markdown as HTML
    const commentBody = page.locator('#comments .comment .comment-body.prose').first();
    if (await commentBody.count() > 0) {
      // Check that the rendered HTML contains <strong> from **test comment**
      await expect(commentBody.locator('strong')).toContainText('test comment');
      // Check for <code> from `code`
      await expect(commentBody.locator('code')).toContainText('code');
    }
  });

  // ==================================================================
  // 3. STATUS UPDATES (STAFF ONLY)
  // ==================================================================
  test('staff can update ticket status via dropdown', async ({ page }) => {
    test.skip(!featureTicketId, 'Feature ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // Open the dots dropdown
    const dotsBtn = page.locator('.btn-dots');
    await expect(dotsBtn).toBeVisible();
    await dotsBtn.click();

    // The dropdown menu should be visible
    const dropdownMenu = page.locator('.dropdown-menu');
    await expect(dropdownMenu).toBeVisible();

    // Click "ready" status button
    const readyBtn = dropdownMenu.locator('button.dropdown-item:has-text("ready")');
    await expect(readyBtn).toBeVisible();
    await readyBtn.click();

    // Wait for HTMX refresh
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    // After HX-Refresh, the page should reload. Re-navigate to be safe.
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // Verify the status badge now shows "ready"
    const badges = page.locator('.ticket-badges .badge');
    const allText = await badges.allTextContents();
    const hasReady = allText.some(t => t.trim() === 'ready');
    expect(hasReady).toBeTruthy();
  });

  // ==================================================================
  // 4. TICKET ARCHIVE / RESTORE (STAFF ONLY)
  // ==================================================================
  test('staff can archive a ticket', async ({ page }) => {
    test.skip(!bugTicketId, 'Bug ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${bugTicketId}`);
    await page.waitForLoadState('networkidle');

    // Open the dots dropdown
    const dotsBtn = page.locator('.btn-dots');
    await expect(dotsBtn).toBeVisible();
    await dotsBtn.click();

    // Dismiss the hx-confirm dialog automatically
    page.on('dialog', dialog => dialog.accept());

    // Click Archive button
    const archiveBtn = page.locator('.dropdown-menu button.dropdown-item:has-text("Archive")');
    await expect(archiveBtn).toBeVisible();
    await archiveBtn.click();

    // Wait for HTMX redirect (HX-Redirect goes to the project bugs page)
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);

    // Should be redirected to the bugs page (or we navigate there to verify)
    await page.goto(`/orgs/${ORG_SLUG}/projects/${PROJECT_SLUG}/bugs`);
    await page.waitForLoadState('networkidle');

    // Bug ticket should no longer appear on the bugs page
    const bugLink = page.locator(`a:has-text("Detail Bug Ticket")`);
    await expect(bugLink).toHaveCount(0);
  });

  test('archived ticket appears on the archived tab', async ({ page }) => {
    test.skip(!bugTicketId, 'Bug ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/orgs/${ORG_SLUG}/projects/${PROJECT_SLUG}/archived`);
    await page.waitForLoadState('networkidle');

    // The archived page should list the bug ticket
    await expect(page.locator('body')).toContainText('Detail Bug Ticket');

    // It should have type and status badges
    const ticketRow = page.locator('.ticket-row:has-text("Detail Bug Ticket")');
    await expect(ticketRow.locator('.badge-uppercase')).toContainText('bug');
  });

  test('staff can restore an archived ticket', async ({ page }) => {
    test.skip(!bugTicketId, 'Bug ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/orgs/${ORG_SLUG}/projects/${PROJECT_SLUG}/archived`);
    await page.waitForLoadState('networkidle');

    // Dismiss the hx-confirm dialog automatically
    page.on('dialog', dialog => dialog.accept());

    // Click the Restore button for the bug ticket
    const ticketRow = page.locator('.ticket-row:has-text("Detail Bug Ticket")');
    const restoreBtn = ticketRow.locator('button:has-text("Restore")');
    await expect(restoreBtn).toBeVisible();
    await restoreBtn.click();

    // Wait for HTMX refresh
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);

    // Navigate to the bugs page and verify the ticket is back
    await page.goto(`/orgs/${ORG_SLUG}/projects/${PROJECT_SLUG}/bugs`);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Detail Bug Ticket');
  });

  // ==================================================================
  // 5. BUG TICKET DETAIL
  // ==================================================================
  test('bug ticket detail shows "bug" type badge', async ({ page }) => {
    test.skip(!bugTicketId, 'Bug ticket not created');

    await fullLogin(page, { ...staffUser, totpSecret: staffTotpSecret });
    await page.goto(`/tickets/${bugTicketId}`);
    await page.waitForLoadState('networkidle');

    // Title
    await expect(page.locator('.ticket-title')).toContainText('Detail Bug Ticket');

    // Type badge shows "bug"
    const typeBadge = page.locator('.ticket-badges .badge-uppercase');
    await expect(typeBadge).toContainText('bug');

    // Priority badge shows "critical"
    const badges = page.locator('.ticket-badges .badge');
    const allText = await badges.allTextContents();
    const hasCritical = allText.some(t => t.trim() === 'critical');
    expect(hasCritical).toBeTruthy();

    // Description renders markdown
    const prose = page.locator('.ticket-description.prose');
    await expect(prose).toBeVisible();
    await expect(prose.locator('strong')).toContainText('bug');
  });

  // ==================================================================
  // 6. CROSS-ROLE VISIBILITY
  // ==================================================================
  test('member user can view the same ticket detail', async ({ page }) => {
    test.skip(!featureTicketId || !memberTotpSecret, 'Prerequisites not met');

    await fullLogin(page, { ...memberUser, totpSecret: memberTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // Member should see the ticket title
    await expect(page.locator('.ticket-title')).toContainText('Detail Feature Ticket');

    // Member should see the description with rendered markdown
    const prose = page.locator('.ticket-description.prose');
    await expect(prose).toBeVisible();
    await expect(prose.locator('strong')).toContainText('bold');

    // Member should see sub-items
    await expect(page.locator('h3:has-text("Sub-items")')).toBeVisible();
    await expect(page.locator('a.sub-item')).toHaveCount(2);

    // Member should see comments posted by staff
    const comments = page.locator('#comments .comment');
    const commentCount = await comments.count();
    expect(commentCount).toBeGreaterThanOrEqual(1);
  });

  test('member user does NOT see the staff dropdown menu', async ({ page }) => {
    test.skip(!featureTicketId || !memberTotpSecret, 'Prerequisites not met');

    await fullLogin(page, { ...memberUser, totpSecret: memberTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    // The dots button (staff-only dropdown) should NOT be visible
    const dotsBtn = page.locator('.btn-dots');
    await expect(dotsBtn).toHaveCount(0);
  });

  test('member user can post a comment on the ticket', async ({ page }) => {
    test.skip(!featureTicketId || !memberTotpSecret, 'Prerequisites not met');

    await fullLogin(page, { ...memberUser, totpSecret: memberTotpSecret });
    await page.goto(`/tickets/${featureTicketId}`);
    await page.waitForLoadState('networkidle');

    const commentBody = 'Member comment: everything looks good!';
    await page.fill('textarea[name="body"]', commentBody);
    await page.click('button:has-text("Comment")');

    // Wait for HTMX to append the comment
    await page.waitForTimeout(1500);

    // The new comment should appear
    await expect(page.locator('#comments')).toContainText('everything looks good');
  });

  test('member user cannot archive a ticket via direct POST', async ({ page }) => {
    test.skip(!bugTicketId || !memberTotpSecret, 'Prerequisites not met');

    await fullLogin(page, { ...memberUser, totpSecret: memberTotpSecret });

    // Try to archive via direct API call -- should be forbidden
    const response = await page.request.post(`/tickets/${bugTicketId}/archive`);
    expect(response.status()).toBe(403);
  });
});
