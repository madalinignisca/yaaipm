const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin } = require('./helpers');

const testUser = {
  name: 'Project Admin',
  email: 'e2e-proj@forgedesk.test',
  password: 'E2ETestPassword123!',
};
let totpSecret = '';

test.describe('Projects', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await registerUser(page, testUser);
    await loginUser(page, testUser);
    const result = await setup2FA(page);
    totpSecret = result.secret;

    // Create an organization
    await page.request.post('/orgs', { form: { name: 'Project Org' } });
    await page.close();
  });

  test('create project via form', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });

    // POST to create project
    await page.request.post('/orgs/project-org/projects', {
      form: { name: 'E2E Project' },
    });

    // Navigate to project brief
    await page.goto('/orgs/project-org/projects/e2e-project/brief');
    await page.waitForLoadState('networkidle');

    // Brief page shows tab navigation and brief content area
    await expect(page.locator('body')).toContainText('No brief available yet');
  });

  test('project brief page renders', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/project-org/projects/e2e-project/brief');

    await expect(page.locator('body')).toContainText('Brief');
    // Tab navigation should be visible
    await expect(page.locator('.tab-link')).toHaveCount(4);
  });

  test('project features page renders', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/project-org/projects/e2e-project/features');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Features');
  });

  test('project bugs page renders', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/project-org/projects/e2e-project/bugs');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Bugs');
  });

  test('project gantt/timeline page renders', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });
    await page.goto('/orgs/project-org/projects/e2e-project/gantt');
    await page.waitForLoadState('networkidle');

    await expect(page.locator('body')).toContainText('Timeline');
  });

  test('project tabs navigate correctly', async ({ page }) => {
    await fullLogin(page, { ...testUser, totpSecret });
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
