const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin } = require('./helpers');
const path = require('path');
const fs = require('fs');

const testUser = {
  name: 'Image Test User',
  email: 'e2e-image@forgedesk.test',
  password: 'E2ETestPassword123!',
};
let totpSecret = '';
let projectId = '';

// Create a minimal 1x1 red PNG for upload tests
function createTestImage() {
  const filePath = path.join(__dirname, 'test-image.png');
  if (!fs.existsSync(filePath)) {
    // Minimal valid PNG: 1x1 red pixel
    const png = Buffer.from(
      '89504e470d0a1a0a0000000d49484452000000010000000108020000009001' +
      '2e00000000c4944415478da6260f8cf0000000201014898f4fc00000000' +
      '0049454e44ae426082',
      'hex'
    );
    fs.writeFileSync(filePath, png);
  }
  return filePath;
}

test.describe('Image Insert Modal', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    await registerUser(page, testUser);
    await loginUser(page, testUser);
    const result = await setup2FA(page);
    totpSecret = result.secret;

    // Create org and project
    await page.request.post('/orgs', { form: { name: 'Image Org' } });
    await page.request.post('/orgs/image-org/projects', { form: { name: 'Image Project' } });

    // Get project ID
    await page.goto('/orgs/image-org/projects/image-project/features');
    const bodyText = await page.content();
    const match = bodyText.match(/project_id[^>]*value="([^"]+)"/);
    if (match) {
      projectId = match[1];
    }

    // Create a ticket for ticket-editor tests
    await page.request.post('/tickets', {
      form: {
        project_id: projectId,
        title: 'Image Test Ticket',
        type: 'feature',
        priority: 'medium',
        description: 'Ticket for image modal tests',
      },
      headers: { Referer: '/orgs/image-org/projects/image-project/features' },
    });

    await page.close();
  });

  test.afterAll(() => {
    // Clean up test image
    const filePath = path.join(__dirname, 'test-image.png');
    if (fs.existsSync(filePath)) {
      fs.unlinkSync(filePath);
    }
  });

  // ── Brief Editor Tests ─────────────────────────────────────

  test.describe('Brief editor', () => {
    test('image toolbar button opens modal', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      // Click "Edit" to enter edit mode
      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500); // Wait for EasyMDE to initialize

      // EasyMDE renders toolbar buttons with title attribute
      const imageBtn = page.locator('button[title="Insert Image"]');
      await expect(imageBtn).toBeVisible();
      await imageBtn.click();

      // Modal should appear
      const modal = page.locator('[x-data="imageInsertModal()"]');
      await expect(modal).toBeVisible();
      await expect(page.locator('h3:has-text("Insert Image")')).toBeVisible();
    });

    test('modal has Upload and AI Generate tabs', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      // Both tabs should be visible
      const uploadTab = page.locator('.modal-tab:has-text("Upload")');
      const generateTab = page.locator('.modal-tab:has-text("AI Generate")');
      await expect(uploadTab).toBeVisible();
      await expect(generateTab).toBeVisible();

      // Upload tab should be active by default
      await expect(uploadTab).toHaveClass(/active/);
      await expect(generateTab).not.toHaveClass(/active/);
    });

    test('tab switching works', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      const uploadTab = page.locator('.modal-tab:has-text("Upload")');
      const generateTab = page.locator('.modal-tab:has-text("AI Generate")');

      // Switch to AI Generate tab
      await generateTab.click();
      await expect(generateTab).toHaveClass(/active/);
      await expect(uploadTab).not.toHaveClass(/active/);

      // The prompt textarea should be visible
      await expect(page.locator('.modal-card textarea[x-model="prompt"]')).toBeVisible();

      // Switch back to Upload tab
      await uploadTab.click();
      await expect(uploadTab).toHaveClass(/active/);

      // The drop zone should be visible
      await expect(page.locator('.drop-zone')).toBeVisible();
    });

    test('modal closes on Cancel button', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      const modal = page.locator('[x-data="imageInsertModal()"]');
      await expect(modal).toBeVisible();

      // Click Cancel
      await page.locator('.modal-card button:has-text("Cancel")').click();
      await expect(modal).not.toBeVisible();
    });

    test('modal closes on X button', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      const modal = page.locator('[x-data="imageInsertModal()"]');
      await expect(modal).toBeVisible();

      // Click close X button
      await page.locator('.modal-card .btn-close').click();
      await expect(modal).not.toBeVisible();
    });

    test('modal closes on backdrop click', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      const modal = page.locator('[x-data="imageInsertModal()"]');
      await expect(modal).toBeVisible();

      // Click on the backdrop at a corner (not the card center)
      await page.locator('.modal-backdrop').click({ position: { x: 5, y: 5 } });
      await expect(modal).not.toBeVisible();
    });

    test('modal closes on ESC key', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      const modal = page.locator('[x-data="imageInsertModal()"]');
      await expect(modal).toBeVisible();

      await page.keyboard.press('Escape');
      await expect(modal).not.toBeVisible();
    });

    test('upload tab shows drop zone', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      const dropZone = page.locator('.drop-zone');
      await expect(dropZone).toBeVisible();
      await expect(dropZone).toContainText('Drag & drop an image here');
      await expect(dropZone).toContainText('choose a file');
    });

    test('file upload via picker works and inserts markdown', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      const testImagePath = createTestImage();

      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      // Set file on hidden input
      const fileInput = page.locator('.modal-card input[type="file"]');
      await fileInput.setInputFiles(testImagePath);

      // Wait for upload to complete — the "Insert" button appears in the uploaded state
      // Use .first() because both upload and generate panels have an Insert button (hidden via x-show)
      const insertBtn = page.locator('.modal-card button:has-text("Insert")').first();
      await expect(insertBtn).toBeVisible({ timeout: 10000 });

      // Click Insert
      await insertBtn.click();

      // Modal should close
      const modal = page.locator('[x-data="imageInsertModal()"]');
      await expect(modal).not.toBeVisible();

      // The CodeMirror editor should contain the markdown image link
      const editorContent = await page.evaluate(() => {
        const cm = document.querySelector('.CodeMirror');
        return cm ? cm.CodeMirror.getValue() : '';
      });
      expect(editorContent).toContain('![');
      expect(editorContent).toContain('/files/');
    });

    test('AI generate tab shows prompt textarea', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      // Switch to AI Generate tab
      await page.locator('.modal-tab:has-text("AI Generate")').click();

      const promptTextarea = page.locator('.modal-card textarea');
      await expect(promptTextarea).toBeVisible();
      await expect(promptTextarea).toHaveAttribute('placeholder', /diagram|image|describe/i);

      // Generate button should be visible but disabled (no prompt yet)
      const generateBtn = page.getByRole('button', { name: 'Generate', exact: true });
      await expect(generateBtn).toBeVisible();
      await expect(generateBtn).toBeDisabled();
    });

    test('AI generate button enables when prompt is entered', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      await page.locator('.modal-tab:has-text("AI Generate")').click();

      await page.locator('.modal-card textarea').fill('A test diagram');

      const generateBtn = page.getByRole('button', { name: 'Generate', exact: true });
      await expect(generateBtn).toBeEnabled();
    });

    test('AI generate shows error when service unavailable', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);
      await page.click('button[title="Insert Image"]');

      await page.locator('.modal-tab:has-text("AI Generate")').click();
      await page.locator('.modal-card textarea').fill('A test diagram');

      // Click generate — should fail since no GEMINI_API_KEY in test env
      await page.getByRole('button', { name: 'Generate', exact: true }).click();

      // Wait for error message
      const errorMsg = page.locator('.text-danger');
      await expect(errorMsg).toBeVisible({ timeout: 10000 });
    });

    test('old generate-image toolbar button is removed', async ({ page }) => {
      test.skip(!projectId, 'Project ID not found');
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/brief');
      await page.waitForLoadState('networkidle');

      await page.click('button:has-text("Edit")');
      await page.waitForTimeout(500);

      // Old AI button should not exist
      const oldBtn = page.locator('button[title="Generate image with AI"]');
      await expect(oldBtn).toHaveCount(0);
    });
  });

  // ── Ticket Editor Tests ────────────────────────────────────
  // NOTE: We navigate to the ticket via page.goto() (full page load) rather than
  // clicking the link on the features page, because hx-boost="true" on <body>
  // intercepts link clicks as AJAX and only swaps the body — the <head> block
  // (which loads EasyMDE via page_head) would NOT be refreshed, so the editor
  // would never initialize.

  test.describe('Ticket editor', () => {
    let ticketUrl = '';

    test.beforeAll(async ({ browser }) => {
      const page = await browser.newPage();
      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto('/orgs/image-org/projects/image-project/features');
      await page.waitForLoadState('networkidle');

      // Find the ticket link href so we can navigate directly
      const ticketLink = page.locator('a:has-text("Image Test Ticket")');
      ticketUrl = await ticketLink.getAttribute('href');
      await page.close();
    });

    test('image toolbar button opens modal on ticket detail', async ({ page }) => {
      test.skip(!ticketUrl, 'Ticket URL not found');
      await fullLogin(page, { ...testUser, totpSecret });

      // Full page load ensures EasyMDE script in page_head is loaded
      await page.goto(ticketUrl);
      await page.waitForLoadState('networkidle');

      // Click Edit on ticket
      await page.locator('#ticket-header button:has-text("Edit")').click();

      // Wait for EasyMDE toolbar to render (replaces fixed timeout)
      await page.locator('.editor-toolbar').waitFor({ state: 'visible', timeout: 5000 });

      // Click image toolbar button
      const imageBtn = page.locator('button[title="Insert Image"]');
      await expect(imageBtn).toBeVisible();
      await imageBtn.click();

      // Modal should appear
      await expect(page.locator('h3:has-text("Insert Image")')).toBeVisible();
      await expect(page.locator('.modal-tab:has-text("Upload")')).toBeVisible();
      await expect(page.locator('.modal-tab:has-text("AI Generate")')).toBeVisible();
    });

    test('file upload works on ticket editor', async ({ page }) => {
      test.skip(!ticketUrl, 'Ticket URL not found');
      const testImagePath = createTestImage();

      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto(ticketUrl);
      await page.waitForLoadState('networkidle');

      await page.locator('#ticket-header button:has-text("Edit")').click();
      await page.locator('.editor-toolbar').waitFor({ state: 'visible', timeout: 5000 });
      await page.click('button[title="Insert Image"]');

      // Upload file
      const fileInput = page.locator('.modal-card input[type="file"]');
      await fileInput.setInputFiles(testImagePath);

      // Wait for upload and Insert button
      const insertBtn = page.locator('.modal-card button:has-text("Insert")').first();
      await expect(insertBtn).toBeVisible({ timeout: 10000 });

      await insertBtn.click();

      // Modal should close
      await expect(page.locator('[x-data="imageInsertModal()"]')).not.toBeVisible();

      // Editor should contain the image markdown
      const editorContent = await page.evaluate(() => {
        const cm = document.querySelector('.CodeMirror');
        return cm ? cm.CodeMirror.getValue() : '';
      });
      expect(editorContent).toContain('![');
      expect(editorContent).toContain('/files/');
    });

    test('upload another button resets for new upload', async ({ page }) => {
      test.skip(!ticketUrl, 'Ticket URL not found');
      const testImagePath = createTestImage();

      await fullLogin(page, { ...testUser, totpSecret });
      await page.goto(ticketUrl);
      await page.waitForLoadState('networkidle');

      await page.locator('#ticket-header button:has-text("Edit")').click();
      await page.locator('.editor-toolbar').waitFor({ state: 'visible', timeout: 5000 });
      await page.click('button[title="Insert Image"]');

      // Upload file
      const fileInput = page.locator('.modal-card input[type="file"]');
      await fileInput.setInputFiles(testImagePath);

      // Wait for upload and "Upload Another" button
      const uploadAnotherBtn = page.locator('.modal-card button:has-text("Upload Another")');
      await expect(uploadAnotherBtn).toBeVisible({ timeout: 10000 });

      // Click "Upload Another" — should reset to drop zone
      await uploadAnotherBtn.click();

      await expect(page.locator('.drop-zone')).toBeVisible();
      await expect(page.locator('.image-preview').first()).not.toBeVisible();
    });
  });
});
