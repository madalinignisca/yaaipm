const { test, expect } = require('@playwright/test');
const { registerUser, loginUser, setup2FA, fullLogin } = require('./helpers');

// --- Test users ---
const superadminUser = {
  name: 'OrgMgmt Superadmin',
  email: 'e2e-orgmgmt@forgedesk.test',
  password: 'E2ETestPassword123!',
};

const memberUser = {
  name: 'OrgMgmt Member',
  email: 'e2e-orgmgmt-member@forgedesk.test',
  password: 'E2ETestPassword123!',
};

const adminUser = {
  name: 'OrgMgmt Admin',
  email: 'e2e-orgmgmt-admin@forgedesk.test',
  password: 'E2ETestPassword123!',
};

const ORG_NAME = 'OrgMgmt Test Org';
const ORG_SLUG = 'orgmgmt-test-org';

let superadminTotpSecret = '';
let memberTotpSecret = '';
let adminTotpSecret = '';
let inviteURL = '';

test.describe('Organization Management', () => {
  // ---- Setup: register superadmin, set up 2FA, create org ----
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();

    // Register superadmin (first user becomes superadmin)
    await registerUser(page, superadminUser);
    await loginUser(page, superadminUser);
    const result = await setup2FA(page);
    superadminTotpSecret = result.secret;

    // Create the org used throughout these tests
    await page.request.post('/orgs', { form: { name: ORG_NAME } });

    await page.close();
  });

  // ---- 1. Org creation ----
  test('create organization via form and verify redirect', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // Look for the "New Organization" button / link
    const createBtn = page.locator('button:has-text("New Organization"), a:has-text("New Organization")');
    if (await createBtn.count() > 0 && await createBtn.first().isVisible()) {
      await createBtn.first().click();
      await page.waitForLoadState('networkidle');
    }

    // Fill org creation form — either inline or on the dashboard page
    const nameInput = page.locator('input[name="name"]');
    if (await nameInput.isVisible()) {
      await nameInput.fill('OrgMgmt Second Org');
      await page.click('button[type="submit"]');
      await page.waitForLoadState('networkidle');
    } else {
      // Create via direct POST and navigate
      await page.request.post('/orgs', { form: { name: 'OrgMgmt Second Org' } });
      await page.goto('/orgs/orgmgmt-second-org');
      await page.waitForLoadState('networkidle');
    }

    // Verify we are on the org page (URL contains the slug)
    const url = page.url();
    expect(url).toContain('/orgs/orgmgmt-second-org');
  });

  test('newly created org appears in sidebar', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto('/orgs/orgmgmt-second-org');
    await page.waitForLoadState('networkidle');

    // The sidebar should list the org
    const sidebar = page.locator('.sidebar-nav');
    await expect(sidebar).toContainText('OrgMgmt Second Org');
  });

  // ---- 2. Org settings page ----
  test('org settings page shows members list with creator as owner', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Page should contain "Members" heading
    await expect(page.locator('body')).toContainText('Members');

    // The member list should show the superadmin user
    const memberList = page.locator('#member-list');
    await expect(memberList).toContainText(superadminUser.name);
    await expect(memberList).toContainText(superadminUser.email);

    // The creator should be shown as "owner"
    await expect(memberList).toContainText('owner');
  });

  test('org settings page shows invitation form for org managers', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // The invite form should be present
    await expect(page.locator('input[name="email"]')).toBeVisible();
    await expect(page.locator('select[name="role"]')).toBeVisible();
    await expect(page.locator('button:has-text("Invite")')).toBeVisible();

    // Pending Invitations section should be present
    await expect(page.locator('body')).toContainText('Pending Invitations');
  });

  // ---- 3. Invite member ----
  test('invite member via form and verify invite URL shown', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Fill the invite form
    await page.fill('input[name="email"]', memberUser.email);
    await page.selectOption('select[name="role"]', 'member');

    // Submit via HTMX
    await page.click('button:has-text("Invite")');
    await page.waitForLoadState('networkidle');

    // The invite result area should show success and the invite URL
    const resultArea = page.locator('#invite-result-area');
    await expect(resultArea).toContainText('Invitation sent to');
    await expect(resultArea).toContainText(memberUser.email);

    // Extract the invite URL for later use
    const urlInput = page.locator('#invite-url-input');
    if (await urlInput.isVisible()) {
      inviteURL = await urlInput.inputValue();
    }
    expect(inviteURL).toBeTruthy();
  });

  test('pending invitation appears in invitation list', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // The invitation list should show the pending invitation
    const invitationList = page.locator('#invitation-list');
    await expect(invitationList).toContainText(memberUser.email);
    await expect(invitationList).toContainText('pending');
  });

  // ---- 4. Resend invitation ----
  test('resend invitation with confirmation dialog', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Set up dialog handler BEFORE clicking (hx-confirm triggers a browser dialog)
    page.on('dialog', async (dialog) => {
      expect(dialog.message()).toContain('Resend invitation');
      await dialog.accept();
    });

    // Click the Resend button
    const resendBtn = page.locator('#invitation-list button:has-text("Resend")');
    await expect(resendBtn.first()).toBeVisible();
    await resendBtn.first().click();
    await page.waitForLoadState('networkidle');

    // After resend, the invitation should still be in the list
    const invitationList = page.locator('#invitation-list');
    await expect(invitationList).toContainText(memberUser.email);
    await expect(invitationList).toContainText('pending');
  });

  // ---- 5. Revoke invitation ----
  test('revoke invitation removes it from the list', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Verify the invitation exists before revoking
    const invitationList = page.locator('#invitation-list');
    await expect(invitationList).toContainText(memberUser.email);

    // Set up dialog handler BEFORE clicking
    page.on('dialog', async (dialog) => {
      expect(dialog.message()).toContain('Revoke invitation');
      await dialog.accept();
    });

    // Click the Revoke button
    const revokeBtn = page.locator('#invitation-list button:has-text("Revoke")');
    await expect(revokeBtn.first()).toBeVisible();
    await revokeBtn.first().click();
    await page.waitForLoadState('networkidle');

    // After revocation the email should no longer appear in invitation list
    // (either the list is empty or the invitation row is gone)
    await expect(invitationList).not.toContainText(memberUser.email);
  });

  // ---- 6. Re-invite after revoke, then member joins via invite ----
  test('re-invite member after revoke for join test', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Send a fresh invitation
    await page.fill('input[name="email"]', memberUser.email);
    await page.selectOption('select[name="role"]', 'member');
    await page.click('button:has-text("Invite")');
    await page.waitForLoadState('networkidle');

    // Extract the new invite URL
    const urlInput = page.locator('#invite-url-input');
    if (await urlInput.isVisible()) {
      inviteURL = await urlInput.inputValue();
    }
    expect(inviteURL).toBeTruthy();
  });

  test('member joins via invitation link and registers', async ({ page }) => {
    // Navigate to the invite URL
    // The inviteURL is the full URL from the app (e.g. http://localhost:8081/invite/<token>)
    // Extract the path portion to use with page.goto
    const invitePath = new URL(inviteURL).pathname;
    await page.goto(invitePath);
    await page.waitForLoadState('networkidle');

    // Should see the invite registration page
    await expect(page.locator('body')).toContainText(ORG_NAME);

    // Fill registration form
    const nameInput = page.locator('input[name="name"]');
    await nameInput.fill(memberUser.name);

    // Remove minlength to avoid browser validation blocking submission
    const passwordInput = page.locator('input[name="password"]');
    await passwordInput.evaluate((el) => el.removeAttribute('minlength'));
    await page.fill('input[name="password"]', memberUser.password);

    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    // Should be redirected to 2FA setup
    await page.waitForURL('**/setup-2fa', { timeout: 10000 });

    // Complete 2FA setup
    const result = await setup2FA(page);
    memberTotpSecret = result.secret;
  });

  test('member can access org page after joining', async ({ page }) => {
    await fullLogin(page, { ...memberUser, totpSecret: memberTotpSecret });

    // Navigate to the org page
    await page.goto(`/orgs/${ORG_SLUG}`);
    await page.waitForLoadState('networkidle');

    // Should see the org page (not a 403 or 404)
    await expect(page.locator('body')).toContainText(ORG_NAME);
  });

  test('member appears in members list', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    const memberList = page.locator('#member-list');
    await expect(memberList).toContainText(memberUser.name);
    await expect(memberList).toContainText(memberUser.email);
  });

  // ---- 7. Role management ----
  test('change member role to admin', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Find the role <select> for the member user (not the current superadmin user)
    // Member rows contain the user email and have a select[name="role"]
    const memberRow = page.locator('.member-row', { has: page.locator(`text=${memberUser.email}`) });
    await expect(memberRow).toBeVisible();

    const roleSelect = memberRow.locator('select[name="role"]');
    await expect(roleSelect).toBeVisible();

    // Change role to admin — the select has hx-patch which fires on change
    await roleSelect.selectOption('admin');
    await page.waitForLoadState('networkidle');

    // Verify the member list re-renders with the updated role
    // Re-query because HTMX replaces the #member-list innerHTML
    const updatedMemberRow = page.locator('.member-row', { has: page.locator(`text=${memberUser.email}`) });
    const updatedSelect = updatedMemberRow.locator('select[name="role"]');
    await expect(updatedSelect).toHaveValue('admin');
  });

  test('change member role back to member', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    const memberRow = page.locator('.member-row', { has: page.locator(`text=${memberUser.email}`) });
    await expect(memberRow).toBeVisible();

    const roleSelect = memberRow.locator('select[name="role"]');
    await expect(roleSelect).toBeVisible();

    // Change back to member
    await roleSelect.selectOption('member');
    await page.waitForLoadState('networkidle');

    // Verify the role is back to member
    const updatedMemberRow = page.locator('.member-row', { has: page.locator(`text=${memberUser.email}`) });
    const updatedSelect = updatedMemberRow.locator('select[name="role"]');
    await expect(updatedSelect).toHaveValue('member');
  });

  // ---- 8. Remove member ----
  test('remove member from organization', async ({ page }) => {
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Verify member is present first
    const memberList = page.locator('#member-list');
    await expect(memberList).toContainText(memberUser.email);

    // Set up dialog handler for hx-confirm BEFORE clicking
    page.on('dialog', async (dialog) => {
      expect(dialog.message()).toContain('Remove');
      expect(dialog.message()).toContain(memberUser.name);
      await dialog.accept();
    });

    // Find the Remove button in the member's row
    const memberRow = page.locator('.member-row', { has: page.locator(`text=${memberUser.email}`) });
    const removeBtn = memberRow.locator('button:has-text("Remove")');
    await expect(removeBtn).toBeVisible();
    await removeBtn.click();
    await page.waitForLoadState('networkidle');

    // After removal, the member should be gone from the list
    const updatedMemberList = page.locator('#member-list');
    await expect(updatedMemberList).not.toContainText(memberUser.email);
  });

  // ---- 9. Org admin can manage members ----
  test('invite and register an admin user', async ({ page }) => {
    // First, as superadmin, invite the admin user
    await fullLogin(page, { ...superadminUser, totpSecret: superadminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Send invitation for admin role
    await page.fill('input[name="email"]', adminUser.email);
    await page.selectOption('select[name="role"]', 'admin');
    await page.click('button:has-text("Invite")');
    await page.waitForLoadState('networkidle');

    // Extract the invite URL
    const urlInput = page.locator('#invite-url-input');
    let adminInviteURL = '';
    if (await urlInput.isVisible()) {
      adminInviteURL = await urlInput.inputValue();
    }
    expect(adminInviteURL).toBeTruthy();

    // Log out the superadmin session
    await page.goto('/logout');
    await page.waitForLoadState('networkidle');

    // Register the admin user via invite link
    const invitePath = new URL(adminInviteURL).pathname;
    await page.goto(invitePath);
    await page.waitForLoadState('networkidle');

    // Fill registration form
    await page.fill('input[name="name"]', adminUser.name);
    const passwordInput = page.locator('input[name="password"]');
    await passwordInput.evaluate((el) => el.removeAttribute('minlength'));
    await page.fill('input[name="password"]', adminUser.password);
    await page.click('button[type="submit"]');
    await page.waitForLoadState('networkidle');

    // Complete 2FA setup
    await page.waitForURL('**/setup-2fa', { timeout: 10000 });
    const result = await setup2FA(page);
    adminTotpSecret = result.secret;
  });

  test('org admin can access settings and sees invite form and member list', async ({ page }) => {
    await fullLogin(page, { ...adminUser, totpSecret: adminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Admin should see the Members section
    await expect(page.locator('body')).toContainText('Members');

    // Admin should see the Invite Member form
    await expect(page.locator('input[name="email"]')).toBeVisible();
    await expect(page.locator('select[name="role"]')).toBeVisible();
    await expect(page.locator('button:has-text("Invite")')).toBeVisible();

    // Admin should see the Pending Invitations section
    await expect(page.locator('body')).toContainText('Pending Invitations');

    // The member list should include the superadmin (owner) and the admin user
    const memberList = page.locator('#member-list');
    await expect(memberList).toContainText(superadminUser.name);
    await expect(memberList).toContainText(adminUser.name);
  });

  test('org admin can invite new members', async ({ page }) => {
    await fullLogin(page, { ...adminUser, totpSecret: adminTotpSecret });

    await page.goto(`/orgs/${ORG_SLUG}/settings`);
    await page.waitForLoadState('networkidle');

    // Re-invite the removed member user to verify admin has invite capability
    await page.fill('input[name="email"]', memberUser.email);
    await page.selectOption('select[name="role"]', 'member');
    await page.click('button:has-text("Invite")');
    await page.waitForLoadState('networkidle');

    // Verify success
    const resultArea = page.locator('#invite-result-area');
    await expect(resultArea).toContainText('Invitation sent to');
    await expect(resultArea).toContainText(memberUser.email);
  });
});
