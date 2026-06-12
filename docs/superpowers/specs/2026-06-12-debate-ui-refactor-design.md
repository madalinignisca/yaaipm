# Debate UI Refactor — "Document Workspace" Design Spec

**Date:** 2026-06-12
**Target release:** v0.4.0 (minor bump; UI refactor + one handler behavior change, no schema migration)
**Status:** Approved design; ready for writing-plans.
**Supersedes:** the UI portion (§3.3 templates, HTMX flow, CSS) of `2026-04-14-feature-debate-mode-design.md`. All backend invariants, routes, and data-layer behavior from that spec remain in force except where §5.2 (async scorer) amends them.

## 1. Summary

The v0.2.0 debate UI is a timeline of raw unified diffs — machine-functional but hostile to the humans (clients) who actually use it. This refactor rebuilds the debate page as a **document-first workspace**: the rendered current description is the permanent centerpiece; AI proposals arrive as a single review panel with **Preview** and **What changed** (word-level highlight) tabs; history shrinks to a compact versions rail; the effort gauge becomes a header chip; "Approve final" becomes the prominent header action. All client-facing text moves to plain language ("suggestion", "version", "Use this version") — no "seed", "rounds", "in_review", or `@@` hunks.

The backend keeps its routes, tables, invariants, and error-status discipline. Two things change behind the UI: the effort scorer runs asynchronously after Accept (instead of blocking the response for 2–10 s), and `internal/diff` gains a word-level inline renderer alongside the existing unified one.

## 2. Problems this fixes (review findings, 2026-06-12)

1. **Proposals are unreadable** — the only view of a proposal is a line-based unified diff of markdown source; `+` markers glue onto markdown `-` bullets; long lines clip in a non-wrapping `<pre>`.
2. **Current state is invisible** — `feature_debates.current_text` is rendered nowhere; after a few rounds the user cannot tell what the description currently says.
3. **Decisions sink below the fold** — round cards stack downward; Accept/Reject and the next-round form drift ever lower.
4. **Dead interaction** — Accept blocks 2–10 s on the synchronous scorer, then `HX-Refresh: true` (#97) forces a full reload; errors (409/429/502) produce no visible feedback at all.
5. **Visual noise** — a ~200 px effort gauge for one number, reasoning hidden in a hover tooltip, sidebar that wraps below the timeline on real viewports so `sticky` never engages.
6. **Developer-facing language** shown to non-technical clients.

## 3. In scope / Out of scope

**In scope:**
- Full rewrite of the 5 debate templates into the workspace layout (page + 6 components).
- Word-level inline diff renderer in `internal/diff` (same `sergi/go-diff` dependency).
- HTMX partial/OOB response plumbing in `internal/handlers/debate.go`; removal of the `HX-Refresh` hack from Accept (#97).
- Async effort scorer + self-polling effort chip; two new read-only GET endpoints (effort chip partial, view-older-version partial).
- Human-readable error banners for 4xx/5xx on debate actions (status codes unchanged).
- Plain-language client terminology; staff/superadmin-only extra detail (model, cost) in the versions rail.
- Responsive collapse of the versions rail below `lg`.
- Updated Playwright suite (`e2e/tests/11-debate.spec.js`) and Go tests for the new renderer/handler behavior.

**Out of scope:**
- Any schema migration. (`feature_debate_rounds.diff_unified` keeps being written for audit/back-compat; the UI stops reading it.)
- Changes to debate lifecycle invariants, locking, caps, cost accounting, CAS approve guard.
- Streaming AI output (still not worth it for 5–10 s calls).
- Inline editing of AI output before accept (still phase-2 item D).
- Renaming routes, Go identifiers, DB columns, or the "debate" feature name internally.

## 4. Page design

### 4.1 Regions

The workspace (`templates/pages/debate.html`) renders four addressable regions inside `#debate-workspace`:

| Region id | Component | Content |
|---|---|---|
| header (static) | in page template | back link · "Refine description — {title}" · `#effort-chip` · **✓ Use this version** (primary) · "Stop refining" (subtle) |
| `#document` | `debate_document.html` | rendered markdown of `current_text`, labeled "Current description · version N" with last-change attribution. At version 0 (no accepted rounds) shows "Starting text" + an **Edit** affordance (same rule as today's seed edit: only while no rounds exist and nothing in flight) |
| `#stage` | one of `debate_suggestion.html` / `debate_composer.html` / thinking skeleton | exactly one of: pending suggestion panel, composer, or in-flight skeleton |
| `#versions` | `debate_versions.html` | rail: newest-first entries — accepted rounds as **v1, v2…**, dismissed rounds struck-through, "Original" at the bottom. Each older accepted version: **View · Restore**. Entries show the feedback snippet that produced them (if any); staff/superadmin additionally see model + per-round cost |

Layout: `lg:grid-cols-[minmax(0,1fr)_260px]` like today, but the rail is part of the same card surface; below `lg` the rail collapses into a "Versions ▾" accordion placed between `#document` and `#stage`. Prose is always rendered wrapped — no `<pre>` for proposal text anywhere.

### 4.2 Suggestion panel (`debate_suggestion.html`)

Rendered when an `in_review` round exists. Purple-accent card directly below `#document`:

- Header: "**{Provider}** suggests an improvement" + "would become version N+1".
- Tabs (Alpine inline `x-data`, no new JS file): **Preview** (default) — the proposal's markdown rendered through the existing `markdown` FuncMap pipeline; **What changed** — word-level inline diff (§5.1) of `input_text` → `output_text`, additions as green `<ins>`, removals as red struck `<del>`, wrapped prose.
- Action bar: **Accept — make this version N+1** (success) · **Dismiss** (ghost).

While a suggestion is pending, the header's "✓ Use this version" renders `disabled` with the explanation "Accept or dismiss the pending suggestion first" — approve always operates on an unambiguous `current_text`.

### 4.3 Composer (`debate_composer.html`)

Shown when no round is in review. One card: optional feedback input ("Tell the AI what to focus on…"), provider chips (keep `providerLabel`/`providerBadgeClass` helpers and the **inline first-radio-checked script** — the conditional `checked` attribute truncation bug from PR #80 still applies), and **Suggest improvements** submit. Below the card, one quiet line: "Happy with it as-is? Use ✓ Use this version in the header."

If no providers are configured, the composer states it plainly (same as today).

### 4.4 Effort chip (`debate_effort_chip.html`)

`Effort 6/10 · ~32h ⓘ` — bucket-colored number (success ≤5, warning 6–8, error ≥9), DaisyUI popover on click showing the scorer's reasoning, the bucket guidance line ("Needs sub-tasks from start", …), "full-stack, mid-senior" qualifier, and "Updated {relTime} · via Gemini". Before the first accepted round: "Effort — appears after the first accepted suggestion". Polling behavior in §5.2.

### 4.5 Pre-debate (empty) state

Two-step entry is unchanged (GET never creates rows). Copy becomes client-first: "Refine this description with AI. While refining, the ticket description is locked — finish or stop refining to edit it directly." Button: **Refine with AI** (POSTs to existing `/start`).

### 4.6 Terminology map (template text only)

| Today | Becomes |
|---|---|
| Debate — {title} / Start debate | Refine description — {title} / Refine with AI |
| Seed description · frozen | Starting text (Edit link only pre-round-1) |
| Round N · in_review | a pending "suggestion" |
| Round N accepted | version N (accepted rounds numbered 1..N in acceptance order) |
| Round N rejected | struck-through dismissed entry in rail |
| Accept / Reject | Accept — make this version N / Dismiss |
| Undo from here | Restore (on an older version; confirm: "Versions after this one will be removed.") |
| Approve final / Abandon | ✓ Use this version / Stop refining |

Routes, handler names, DB columns, and test identifiers are **not** renamed.

## 5. Behavior changes

### 5.1 Word-level diff renderer

New in `internal/diff`:

```go
// RenderInlineHTML diffs before→after at word granularity and returns
// sanitized HTML: unchanged text plain, insertions in <ins>, deletions
// in <del>. Every text node is HTML-escaped; AI text is never trusted.
func RenderInlineHTML(before, after string) template.HTML
```

Implementation: `diffmatchpatch.DiffMain` + `DiffCleanupSemantic` (semantic cleanup merges character-level noise into word/phrase-level runs — that is what makes prose diffs readable). Exposed as FuncMap helper `renderInlineDiff before after`. The old `renderDiff` helper and its template call sites are removed; `ComputeUnified` stays (still persisted to `diff_unified` on round creation for audit/back-compat — cheap, and removing the write would be backend churn for no UI gain).

Escape-proof tests mirror the existing pinned XSS proofs (`TestRenderInlineHTML_EscapesScriptTags`, `_EscapesAttributeInjection`) plus word-level semantics cases.

### 5.2 Async effort scorer (amends §4.3 of the v0.2.0 spec)

Accept's handler commits the accept tx (steps 1–6 unchanged), then **returns immediately**; the scorer (former steps 7–11: score, conditional `last_scored_round_id`-guarded update, scorer-cost persistence, `project_costs` rollup) moves into a goroutine using a context **detached from the request** (`context.WithTimeout(context.WithoutCancel(r.Context()), 60s)`) so it survives the handler returning. The existing out-of-order guard (`last_scored_round_id` round-number comparison) already makes late results safe; scorer failure keeps being non-fatal WARN.

**Effort chip polling:** the chip partial is "stale" while `last_scored_round_id` doesn't match the latest accepted round. A stale chip renders "rescoring…" and self-polls `GET …/debate/effort` via `hx-get hx-trigger="load delay:3s" hx-swap="outerHTML"`. The server includes the polling trigger **only if** the latest accept was < 90 s ago (60 s scorer timeout + buffer, matching the stale-reservation constant); after that the chip settles on the previous score (or "—") without polling forever — covers permanent scorer failure without client-side timers.

### 5.3 HTMX flow (replaces the v0.2.0 HTMX table; kills the #97 `HX-Refresh` hack)

| Action | Endpoint (existing unless noted) | Primary target | OOB swaps |
|---|---|---|---|
| Suggest improvements | `POST …/rounds` | `#stage` → suggestion panel | — |
| (while waiting) | — | thinking skeleton via `hx-indicator` on `#stage`; submit buttons `hx-disabled-elt` | — |
| Accept | `POST …/rounds/:rid/accept` | `#stage` → composer | `#document`, `#versions`, `#effort-chip` (stale/polling state) |
| Dismiss | `POST …/rounds/:rid/reject` | `#stage` → composer (previous feedback preserved in the textarea) | `#versions` |
| Restore version N | `POST …/undo?from={roundNumber+1}` (`from=1` for Original) | `#stage` → composer | `#document`, `#versions`, `#effort-chip` |
| View version N | **new** `GET …/debate/versions/:rid` | `#document` → read-only view with "Viewing version N — Back to current · Restore" banner | — |
| Effort chip poll | **new** `GET …/debate/effort` | `#effort-chip` | — |
| Use this version | `POST …/approve` | `HX-Redirect` to ticket (unchanged) | — |
| Stop refining | `POST …/abandon` | `HX-Redirect` to ticket | — |

Both new GET endpoints go through `requireDebateContext` (tenant-scoped, feature-only, 404 discipline) and are read-only plain `SELECT`s.

### 5.4 Error presentation

Status-code discipline from the v0.2.0 spec is unchanged. For HTMX requests, debate handlers additionally render `debate_error.html` — a dismissible banner partial — as the response body. A small `htmx:beforeSwap` listener (added to `static/js/app/init.js`, scoped to targets inside `#debate-workspace`) sets `shouldSwap = true` for status ≥ 400 so the banner lands in `#stage` above the composer. Client-language messages:

| Status | Message |
|---|---|
| 409 in-flight | "A suggestion is already being written — give it a few seconds." |
| 409 stale/terminal | "This page is out of date — reload to see the latest state." (link reloads) |
| 429 caps | "You've reached the suggestion limit (10 per feature / 50 per day). Ask staff if you need more." |
| 502 AI failure | "{Provider} couldn't produce a suggestion. Nothing was changed — try again." |
| 503 provider unconfigured | "{Provider} isn't available on this server." |
| 409 CAS on approve | unchanged recovery copy from v0.2.0 §4.5, reworded to client language |

Non-HTMX fallbacks keep today's behavior.

## 6. View model

`internal/handlers/debate.go` builds an explicit view model instead of passing raw rows to templates:

```go
type debateView struct {
    Ticket    *models.Ticket
    Debate    *models.FeatureDebate
    Versions  []versionEntry   // newest-first; accepted rounds numbered 1..N in acceptance order
    Pending   *suggestionView  // nil unless an in_review round exists
    CanEditSeed bool           // no rounds and nothing in flight
    IsStaff   bool             // gates model+cost detail in the rail
    Providers []string
}
```

Version numbering (the "v1, v2…" the client sees) is **derived at render time** by walking rounds in `round_number` order and counting accepted ones — it is not stored. Restore maps version N to the existing undo endpoint via the underlying `round_number` (+1).

## 7. Files touched

| Path | Change |
|---|---|
| `templates/pages/debate.html` | rewritten — workspace layout, header actions, regions |
| `templates/components/debate_document.html`, `debate_suggestion.html`, `debate_composer.html`, `debate_versions.html`, `debate_effort_chip.html`, `debate_error.html` | new |
| `templates/components/debate_seed.html`, `debate_round.html`, `debate_next_round.html`, `debate_sidebar.html` | deleted |
| `internal/diff/diff.go` + tests | add `RenderInlineHTML` |
| `internal/render/render.go` | swap `renderDiff` → `renderInlineDiff` FuncMap helper |
| `internal/handlers/debate.go` + tests | view model, OOB partial responses, async scorer, 2 GET endpoints, error partials, remove `HX-Refresh` |
| `static/js/app/init.js` | `htmx:beforeSwap` error-banner listener (then `sh scripts/bundle.sh`) |
| `e2e/tests/11-debate.spec.js` | rewritten for new flow |

## 8. Testing

**Unit (`internal/diff`):** word-level correctness (insert-only, delete-only, mixed, markdown punctuation), XSS escape proofs for `<ins>`/`<del>` paths, empty/identical inputs.

**Handler integration (real Postgres, `-p 1`):**
- `TestAccept_ReturnsBeforeScorerCompletes` — fake scorer blocks on a channel; accept response arrives with composer partial + OOB document while scorer is still blocked; after release, `effort_*` lands (poll the row).
- `TestAccept_ScorerSurvivesRequestCancellation` — request context cancelled after response; detached scorer still writes the score.
- `TestEffortChipPartial_PollsWhileStale_SettlesAfter90s` — trigger attribute present when stale & recent, absent when stale & old or fresh.
- `TestVersionViewPartial_TenantScoped` / `_404OnForeignRound` — new GET discipline.
- `TestAccept_NoHXRefreshHeader` — regression on the #97 hack removal.
- Existing accept/reject/undo/approve/abandon invariant tests unchanged and must stay green.

**E2E (Playwright, fake refiner):** golden path through the new UI — start, suggest, tab between Preview/What changed, accept (chip shows rescoring → score), dismiss, restore, approve; error banner on a forced 502; "Use this version" disabled while suggestion pending.

## 9. Rollback

Pure code release: revert the image pin to the previous tag. No migration, no data shape change. Late async-scorer goroutines from the new pods finish or time out harmlessly (the `last_scored_round_id` guard and status filters already tolerate them).
