// Sessions come from global setup: the app allows exactly one registration
// ever (first-user-only), and logging in per test would replay a TOTP code
// inside its 30s step, which ValidateTOTPOnce rejects (#136).
let rootCookies = [];

const { test, expect } = require('@playwright/test');
const { useSession, rootSession } = require('./helpers');

const testUser = {
  name: 'Project Admin',
  email: 'e2e-proj@forgedesk.test',
  password: 'E2ETestPassword123!',
};
let totpSecret = '';

test.describe('Projects', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    // Shared root session: see the note at the top of this file (#136).

    rootCookies = rootSession().cookies;

    await useSession(page, rootCookies);

    // Create an organization
    await page.request.post('/orgs', { form: { name: 'Project Org' } });
    await page.close();
  });

  test('create project via form', async ({ page }) => {
    await useSession(page, rootCookies);

    // POST to create project
    await page.request.post('/orgs/project-org/projects', {
      form: { name: 'E2E Project' },
    });

    // Navigate to project brief
    await page.goto('/orgs/project-org/projects/e2e-project/brief');
    await page.waitForLoadState('networkidle');

    // Brief page shows tab navigation and brief content area
    // The empty-brief copy is "No project brief yet" — the old string had
    // drifted and nothing could catch it while this spec never ran (#136).
    await expect(page.locator('body')).toContainText('No project brief yet');
  });

  test('project brief page renders', async ({ page }) => {
    await useSession(page, rootCookies);
    await page.goto('/orgs/project-org/projects/e2e-project/brief');

    await expect(page.locator('body')).toContainText('Brief');
    // Assert the tabs by name rather than by count: a bare number goes stale
    // every time a tab is added, and says nothing about what is missing.
    const tabs = page.locator('.tab-link');
    for (const name of ['Brief', 'Features', 'Bugs', 'Timeline', 'Settings']) {
      await expect(tabs.filter({ hasText: name })).toHaveCount(1);
    }
  });

  test('project features page renders', async ({ page }) => {
    await useSession(page, rootCookies);
    await page.goto('/orgs/project-org/projects/e2e-project/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Features');
  });

  test('project bugs page renders', async ({ page }) => {
    await useSession(page, rootCookies);
    await page.goto('/orgs/project-org/projects/e2e-project/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Bugs');
  });

  test('project gantt/timeline page renders', async ({ page }) => {
    await useSession(page, rootCookies);
    await page.goto('/orgs/project-org/projects/e2e-project/gantt');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Timeline');
  });

  test('project tabs navigate correctly', async ({ page }) => {
    await useSession(page, rootCookies);
    await page.goto('/orgs/project-org/projects/e2e-project/brief');

    // Click Features tab
    const featTab = page.locator('a.tab-link:has-text("Features")');
    if (await featTab.isVisible()) {
      await featTab.click();
      await page.waitForLoadState('networkidle');
      await expect(page).toHaveURL(/\/features/);
    }

    // Click Bugs tab
    const bugsTab = page.locator('a.tab-link:has-text("Bugs")');
    if (await bugsTab.isVisible()) {
      await bugsTab.click();
      await page.waitForLoadState('networkidle');
      await expect(page).toHaveURL(/\/bugs/);
    }

    // Click Timeline tab
    const ganttTab = page.locator('a.tab-link:has-text("Timeline")');
    if (await ganttTab.isVisible()) {
      await ganttTab.click();
      await page.waitForLoadState('networkidle');
      await expect(page).toHaveURL(/\/gantt/);
    }
  });
});
