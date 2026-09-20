const { test, expect } = require('@playwright/test');

// #159 — route placement, which no Go handler test can cover.
//
// The defect was not in ServeFile's logic but in WHERE the route was
// registered: `r.Get("/files/*", ...)` sat on the root router, above the
// group that applies AuthMiddleware, so the handler was reachable with no
// session at all. Every handler-level test in internal/handlers invokes the
// handler function directly and never consults the router, so all eleven of
// them passed throughout — the same blind spot that shipped a 404 on a
// button with four green checks.
//
// This asserts the property at the only layer that can see it: a real HTTP
// request, through the real router, with no cookie.
//
// The discriminator is precise. Before the fix an anonymous request for a
// nonexistent key returned 404 — the handler ran, looked in S3, found
// nothing. After the fix it must never reach the handler at all, and the
// auth middleware redirects. 404 here means the route escaped the group.
test.describe('unauthenticated /files/* access', () => {
  // Explicitly drop the shared session that global-setup saves.
  test.use({ storageState: { cookies: [], origins: [] } });

  const bogusKey =
    'orgs/00000000-0000-4000-8000-000000000000' +
    '/projects/00000000-0000-4000-8000-000000000001' +
    '/attachments/00000000-0000-4000-8000-000000000002.pdf';

  test('redirects to login instead of reaching the file handler', async ({ page }) => {
    const res = await page.request.get(`/files/${bogusKey}`, { maxRedirects: 0 });

    expect(
      res.status(),
      'a 404 means the request reached ServeFile, i.e. /files/* is outside the authenticated group again'
    ).toBe(303);
    expect(res.headers()['location']).toContain('/login');
  });

  test('behaves like every other protected route', async ({ page }) => {
    const protectedRes = await page.request.get('/', { maxRedirects: 0 });
    const filesRes = await page.request.get(`/files/${bogusKey}`, { maxRedirects: 0 });

    expect(
      filesRes.status(),
      '/files/* must be as protected as the dashboard, not more permissive'
    ).toBe(protectedRes.status());
  });
});
