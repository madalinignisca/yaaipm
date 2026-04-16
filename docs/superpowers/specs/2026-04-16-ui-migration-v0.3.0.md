# v0.3.0 — UI Migration to Tailwind v4 + DaisyUI v5

**Status:** Draft
**Author:** Madalin + Claude
**Created:** 2026-04-16
**Scope:** ForgeDesk repo (`smart.madalin.me`) only. Other `../*` projects migrate separately, later.
**Prior art:** Spike `spike/tailwind-daisyui-debate` (commit `c2c0d1c`), evaluated visually on 2026-04-16 and approved.

## 1. Summary

Replace the ~47KB hand-written `static/css/app.css` with a **Tailwind v4 + DaisyUI v5** stylesheet compiled by the tailwindcss standalone CLI (no Node, no npm). Tune the DaisyUI theme (`forgedesk`) toward the Linear / Notion / Supabase neutral-grayscale aesthetic with a cool indigo primary. Preserve the existing dark sidebar + light content split because it's a distinctive pattern that reads as "professional SaaS" without blending into the generic DaisyUI crowd.

Migrate all 37 templates (1 layout + 8 auth + 14 components + 14 pages) across **6 sequential PRs**, feature-frozen during the ~1-week migration window, reviewed via screenshots in PR descriptions.

Ship as `v0.3.0` with a single rollback target (`v0.2.2`) — the whole migration is one atomic release, even though internally it's six PRs.

## 2. Scope

### In

- Every HTML template under `templates/` migrated to Tailwind utility + DaisyUI component classes
- Custom `forgedesk` DaisyUI theme (OKLCH palette, tighter radii, Linear-style)
- Dark variant via DaisyUI `business` theme, auto-selected via `prefers-color-scheme: dark`
- Self-hosted Inter variable font (~50KB, Latin subset)
- Lucide icons via inline SVG (ship only the icons actually used)
- `scripts/css.sh` wired into `deploy/Containerfile` build stage
- Content-hashed `tw.css` via the existing `internal/static` asset manifest
- Remove `static/css/app.css` entirely at the end of migration
- Remove the spike-specific `cmd/preview` and `?v=2` cache-buster
- Update `deploy/k8s/deployment.yaml` + `orchestrator-deployment.yaml` image pin to `:v0.3.0`
- Full-repo verification per the sequential-PR memory: `go build ./...` + `go vet ./...` + `golangci-lint run ./...` + `go test ./internal/... -p 1 -count=1`

### Out

- **Shared `ui-kit/` across `../*` projects** — user deferred "much later", not v0.3.0
- **User-toggleable theme switcher** — deferred to a follow-up, v0.3.0 auto-respects `prefers-color-scheme` only
- **Brand colors** — user doesn't have finalized brand yet; the `forgedesk` theme uses a generic cool-indigo primary. When brand lands, one CSS block updates
- **New features** — feature freeze for migration duration. No Tier 3 chairman synthesis, no Tier 2 blind-pick, no assistant changes during migration
- **Mobile-first rework** — existing responsive breakpoints preserved; we're refreshing the look, not redesigning the IA
- **Accessibility audit beyond DaisyUI defaults** — DaisyUI ships with reasonable a11y out of the box; formal audit is a follow-up issue
- **Dark-mode polish for every edge case** — auto-variant provides a baseline; some pages may look rough in dark and get polished later

## 3. Architecture Decisions

### 3.1 Framework: Tailwind v4 + DaisyUI v5

Tailwind CSS v4.2.x via the standalone binary. DaisyUI v5.5.x via the `.mjs` bundle. Both downloaded to `bin/` (gitignored) on first build by `scripts/css.sh`. No Node, no npm, no PostCSS config. Matches the existing no-build-step philosophy (`scripts/bundle.sh` for JS).

**Why:** Tailwind + DaisyUI is the dominant modern CSS stack in 2026 B2B SaaS. Every new component library (Flowbite, Preline, shadcn-ports) is Tailwind-based. DaisyUI's semantic class names (`btn btn-primary`, `badge badge-info`) keep HTML readable and match our existing hand-CSS mental model.

### 3.2 Theme: custom `forgedesk` (light default + `business` dark variant)

Custom theme defined in `static/css/tw-input.css` via DaisyUI's `@plugin "daisyui/theme"` directive. Near-white base, cool indigo primary, tighter radii (0.375rem fields, 0.5rem boxes), OKLCH-native palette. Paired with DaisyUI's built-in `business` theme as the dark variant, auto-selected via `prefers-color-scheme: dark`.

```css
@plugin "./daisyui.mjs" {
  themes: forgedesk --default, business --prefersdark;
}
```

**Why:** custom theme is already tuned from the spike. Built-in `corporate` was close but slightly too "banking" — our theme is ~5 OKLCH tweaks away from `corporate`, worth keeping as our identity. Will be refined when brand colors land (one `@plugin` block, ~20 lines).

### 3.3 Sidebar: keep dark, migrate content to theme

The existing dark sidebar (`app.css` rules from `.sidebar`, `.sidebar-link`, etc.) is re-implemented using Tailwind utilities but **kept dark regardless of theme**. The content area flips with the theme.

**Why:** Linear / Vercel / Supabase all use dark-chrome + light-content. Distinctive vs generic DaisyUI apps. Works in light mode (dark sidebar + light content = contrast) and dark mode (dark sidebar + dark content = subtle tone difference).

**How:** Apply `data-theme="business"` on the `<aside class="sidebar">` element specifically. DaisyUI scopes theme colours by the nearest `data-theme` ancestor, so the sidebar sees `business` tokens while everything else sees `forgedesk` (or `business` in dark mode).

### 3.4 Typography: Inter variable, self-hosted

Inter 4.0 variable font (Latin subset only, ~50KB woff2), self-hosted under `static/fonts/`. Loaded via `@font-face` in `tw-input.css` before `@import "tailwindcss"`. Applied via Tailwind's default `font-sans` stack (already includes `Inter` first in v4).

**Why:** self-hosted avoids Google Fonts privacy/GDPR issues. Inter is the SaaS standard in 2026 (Linear, Notion, Supabase, Vercel). Clients subconsciously register "this looks professional" from consistent typography. Single file, no external requests.

### 3.5 Icons: Lucide via inline SVG sprite

Download Lucide icon subset (the ~20 icons actually used across the app). Build once into a single SVG sprite file at `static/icons/sprite.svg`. Reference with `<svg><use href="/static/icons/sprite.svg#icon-name"/></svg>`.

**Why:** no JavaScript icon library, no CDN, no runtime overhead. SVG sprite is cacheable for a year. Consistent visual language (Lucide pairs well with the Tailwind aesthetic).

**In spec:** build script `scripts/icons.sh` that pulls from Lucide's npm package source via curl, filters to our used-icons list, assembles the sprite.

## 4. Template Inventory

37 templates grouped by page type for migration ordering:

### 4.1 Shared chrome (migrated first — everything depends on it)

- `layouts/base.html` — page shell, sidebar, flash, assistant FAB, auth fallback
- `components/modal.html` — generic modal
- `components/image_modal.html` — image preview modal
- `components/member_list.html`, `invitation_list.html`, `invite_result.html` — shared org/project partials

### 4.2 Auth pages (8)

- `auth/login.html`
- `auth/register.html`
- `auth/invite_register.html`
- `auth/setup_2fa.html`, `setup_2fa_totp.html`, `setup_2fa_webauthn.html`
- `auth/verify_2fa.html`
- `auth/recovery_codes.html`

### 4.3 Dashboard + org (3)

- `pages/dashboard.html`
- `pages/org_settings.html`
- `pages/org_costs.html`

### 4.4 Project pages (7)

- `pages/project_brief.html`
- `pages/project_features.html`
- `pages/project_bugs.html`
- `pages/project_gantt.html`
- `pages/project_costs.html`
- `pages/project_archived.html`
- `pages/project_settings.html`
- `components/project_tabs.html`, `ticket_card.html`

### 4.5 Ticket + debate (3 pages + 6 debate partials)

- `pages/ticket_detail.html`
- `pages/debate.html` (already done in spike — forward-port from spike branch)
- `components/debate_*.html` + `provider_chip.html` (6 partials, already done in spike)
- `components/comment.html`, `reactions.html`

### 4.6 Admin + account (2)

- `pages/admin.html`
- `pages/account_settings.html`

## 5. Build Pipeline

### 5.1 Local development

```bash
bash scripts/css.sh           # one-shot minified build
bash scripts/css.sh --watch   # rebuilds on template save, ~50ms per change
```

First run downloads `tailwindcss` binary (~50MB) + `daisyui.mjs` + `daisyui-theme.mjs` into `bin/` (gitignored). Cached across subsequent runs.

### 5.2 Production build (Containerfile)

Add a new stage to `deploy/Containerfile`:

```dockerfile
# --- CSS build stage --------------------------------------------
FROM alpine:3.20 AS css-builder
RUN apk add --no-cache bash curl
WORKDIR /src
COPY scripts/css.sh ./scripts/
COPY static/css/tw-input.css ./static/css/
COPY templates/ ./templates/
RUN bash scripts/css.sh

# --- Existing go builder ----------------------------------------
FROM golang:1.24-alpine AS go-builder
# ... existing steps ...
COPY --from=css-builder /src/static/css/tw.css ./static/css/tw.css
# ... continue existing build ...
```

### 5.3 Asset manifest (content hashing)

`internal/static` already computes SHA-256 hashes for assets and rewrites filenames to `<name>.<hash8>.<ext>`. The build pipeline invokes this post-CSS-compile. Templates reference `{{asset "css/tw.css"}}` which resolves to `/static/css/tw.<hash>.css` in production, or plain `/static/css/tw.css` in tests.

**Remove:** the `?v=2` cache-buster in the spike's `debate.html` — replaced by content-hashed filename.

## 6. Phased Rollout — 6 PRs

Each PR:
- Branches from main (post-previous-merge)
- Full-repo verification locally before push
- Screenshot in PR description for each migrated template (before + after)
- Squash-merged after review
- **2+ rule** applies to bot-review findings: ship at MVP when 0–1 high findings

### PR-1 — Build pipeline + shared chrome

- `scripts/css.sh`, `static/css/tw-input.css` with `forgedesk` theme
- `deploy/Containerfile` CSS build stage
- Asset manifest integration for `tw.css`
- Inter font self-hosted under `static/fonts/`
- Lucide icon sprite at `static/icons/sprite.svg`
- `layouts/base.html` migrated (sidebar dark-themed, main area theme-aware, flash, assistant FAB)
- Shared component partials: `modal.html`, `image_modal.html`, `member_list.html`, `invitation_list.html`, `invite_result.html`
- **After this merges:** base chrome is DaisyUI-themed, but pages still use `app.css` — they'll look mixed. That's expected mid-migration.

### PR-2 — Auth pages (8)

- `auth/login.html`, `register.html`, `invite_register.html`
- `auth/setup_2fa.html`, `setup_2fa_totp.html`, `setup_2fa_webauthn.html`
- `auth/verify_2fa.html`, `recovery_codes.html`
- Auth card styling (consistent max-width, shadow, spacing)
- Form inputs using DaisyUI `input input-bordered`, `btn btn-primary`, etc.

### PR-3 — Dashboard + org

- `pages/dashboard.html`, `org_settings.html`, `org_costs.html`
- Org switcher dropdown in sidebar (if not already covered in PR-1 sidebar work)

### PR-4 — Project pages

- All 7 `pages/project_*.html`
- `components/project_tabs.html`, `ticket_card.html`
- Gantt chart CSS left untouched (vanilla JS, self-contained) unless trivial adjustments

### PR-5 — Ticket + debate

- `pages/ticket_detail.html` — the biggest single page, has tabs, comments, reactions, agent controls, assistant
- Forward-port the debate partials from the spike branch (minus the `cmd/preview` scaffolding and `?v=2`)
- `components/comment.html`, `reactions.html`

### PR-6 — Admin + account + cleanup

- `pages/admin.html`, `account_settings.html`
- **Remove `static/css/app.css` entirely** — nothing should reference it after this PR
- **Remove** `cmd/preview/` and `?v=2` cache-buster (if not removed in PR-5)
- Final full-app screenshot audit across all routes
- Tag `v0.3.0`, build image, deploy

## 7. Testing Strategy

### 7.1 Automated tests

- All existing handler tests continue to pass (they assert on HTTP/DB state, not CSS classes)
- The regression test `TestShowDebate_ActiveRendersProviderPicker` from PR #80 stays — it uses data attributes and button text, not class names
- No new automated CSS regression tests. Visual review via screenshots.

### 7.2 Screenshot review

Each PR description includes:
- Before screenshot (current production)
- After screenshot (migrated)
- At least two viewport sizes: 1440px (desktop default) and 768px (tablet breakpoint)
- Dark-mode screenshot for one representative page per PR (if the PR touches a page that supports dark)

### 7.3 E2E tests (Playwright)

Existing `e2e/tests/01-auth.spec.js` through `e2e/tests/11-debate.spec.js` continue to run. Any that assert on CSS class names get updated in the same PR that breaks them. Coverage gap documented in issue #81 (fake-backed + real-AI e2e) is orthogonal — pursued separately.

### 7.4 Manual smoke

Before PR merge:
- Log in, navigate to at least one migrated page
- Check dark mode (system preference flip)
- Verify the assistant FAB still opens/closes
- Verify HTMX swaps still work (post a comment, accept a round, etc.)

## 8. Dependencies

### Added

- `tailwindcss` (standalone binary, v4.2.x) — downloaded in `scripts/css.sh`, not vendored
- `daisyui` + `daisyui-theme` (v5.5.x .mjs bundles) — downloaded in `scripts/css.sh`
- Inter variable font (Latin subset) — committed to `static/fonts/`
- Lucide icons subset — committed to `static/icons/sprite.svg`

### Removed (end of migration)

- `static/css/app.css` (entirely, all ~47KB / ~2000 lines)
- The spike-only `cmd/preview/` directory
- The spike-only `?v=2` cache-buster

### Unchanged

- Go 1.24 / golangci-lint 2.11
- chi, pgx, html/template, HTMX, Alpine, Marked, EasyMDE, Mermaid
- JS bundle pipeline (`scripts/bundle.sh`)
- All handler code (backend is pure refactor-free)

## 9. Release Plan

After PR-6 merges to main:

1. `git tag -a v0.3.0 -m "..."` with release notes summarizing the UI refresh
2. `docker build -t registry.k3s.vlah.sh/smartpm:v0.3.0 -t :latest .` (Containerfile now runs `scripts/css.sh` during build)
3. `docker push` both tags
4. Update `deploy/k8s/deployment.yaml` + `orchestrator-deployment.yaml` image pin to `:v0.3.0`, commit direct to main (per v0.2.0 pattern)
5. `kubectl apply -f deploy/k8s/`
6. `kubectl rollout status` for both deployments
7. `curl https://smart.madalin.me/health` and `/login` to verify

**DB backup:** not required — no schema change in this release.

## 10. Rollback Plan

### Fast rollback (< 2 min)

```bash
kubectl set image deployment/forgedesk-server server=registry.k3s.vlah.sh/smartpm:v0.2.2 -n smartpm
kubectl set image deployment/forgedesk-orchestrator orchestrator=registry.k3s.vlah.sh/smartpm:v0.2.2 -n smartpm
```

Since v0.3.0 is CSS/template-only (no schema, no config), rolling back is stateless. Users mid-session re-render against the old templates — no data loss, no re-auth required.

### Git revert

If v0.3.0 is merged to main but we want it out of main history:
- **Preferred:** revert the 6 migration PRs in reverse order (6 → 5 → 4 → 3 → 2 → 1), each in its own revert PR. Preserves historical integrity.
- **Emergency:** `git revert --no-commit <PR-6-squash-SHA>..<PR-1-squash-SHA>` + commit, if time-critical.

## 11. Open Risks

1. **Tailwind Preflight cascade conflicts** — when PR-1 merges, `tw.css` loads globally but unmigrated pages still rely on `app.css` styles that Preflight might reset. Mitigation: Preflight resets are surgical, not destructive — most breakage is "heading margin collapsed", fixable with utility overrides. If too noisy, we gate Preflight behind a CSS scope.

2. **DaisyUI `.btn` / `.card` class name collisions with existing `app.css`** — both stylesheets define these. DaisyUI wins tiebreakers if loaded after, but specificity battles could produce weird rendering on half-migrated pages. Mitigation: PR-1 removes the collision-prone rules from `app.css` (just those specific selectors), keeps the rest for pages not yet migrated.

3. **Manifest hash drift between CSS compile and Go build** — if the Containerfile's CSS stage runs before Go build but the manifest is computed during Go's embed step, we could ship a template that references a hash the build doesn't know about. Mitigation: extend `internal/static` manifest computation to include `tw.css`, verified by a build-time check in `cmd/server/main.go`.

4. **Dark-mode regressions in pages with heavy color usage** — charts, Gantt, cost tables with inline styles. Mitigation: PR-4 (project pages) is the riskiest; allocate extra review time. Dark-mode edge cases documented as follow-up issues, not blockers.

5. **Font FOUT (flash of unstyled text)** — Inter loading late creates a visible font swap on slow connections. Mitigation: `font-display: swap` on `@font-face`, preload link in `<head>`, fallback stack uses system sans-serif so layout doesn't shift.

## 12. Decision Log (from 2026-04-16 discussion)

| # | Decision | Rationale |
|---|---|---|
| 1 | Dark sidebar + light content | Linear/Vercel/Supabase pattern; distinctive vs generic DaisyUI apps |
| 2 | OS-preference dark mode only (no user toggle) | Reduces v0.3.0 scope; toggle can ship as follow-up if clients ask |
| 3 | 6 grouped PRs | Balances reviewer fatigue vs deploy-between-merges flexibility |
| 4 | Inter variable, self-hosted | SaaS standard; avoids Google Fonts privacy issues |
| 5 | Custom `forgedesk` theme | Pre-tuned to Linear aesthetic; one block to update when brand colors land |
| 6 | Feature freeze during migration | Prevents merge conflicts on every migration PR |
| 7 | Screenshot review, no preview env | Solo operator; preview env is overkill |
| 8 | Lucide icons via SVG sprite | No JS dependency; matches aesthetic; cacheable |
| 9 | Remove `app.css` entirely at end | No mixed paradigm in production |

## 13. Acceptance Criteria

v0.3.0 is complete when:

- [ ] All 37 templates render with Tailwind + DaisyUI classes; `app.css` is deleted
- [ ] `forgedesk` light theme + `business` dark theme both work end-to-end
- [ ] `prefers-color-scheme: dark` auto-switches content area; sidebar stays dark
- [ ] All existing handler tests pass
- [ ] `golangci-lint run ./...` clean
- [ ] Production build via `deploy/Containerfile` produces a working image
- [ ] Image `registry.k3s.vlah.sh/smartpm:v0.3.0` deployed to K3S
- [ ] https://smart.madalin.me health check 200
- [ ] Smoke test: log in, dashboard, create ticket, start debate, submit round, approve — all work
- [ ] Rollback to `v0.2.2` verified working from manifest edit alone (not tested in prod, verified by dry-run)
