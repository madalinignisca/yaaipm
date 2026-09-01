# Debate UI Refactor — "Document Workspace" Design Spec

**Date:** 2026-06-12
**Target release:** v0.4.0 (minor bump; UI refactor + one handler behavior change, no schema migration)
**Status:** Approved design; ready for writing-plans.
**Supersedes:** the UI portion (§3.3 templates, HTMX flow, CSS) of `2026-04-14-feature-debate-mode-design.md`. All backend invariants, routes, and data-layer behavior from that spec remain in force except where §5.2 (async scorer) amends them.

## 1. Summary

The v0.2.0 debate UI is a timeline of raw unified diffs — machine-functional but hostile to the humans (clients) who actually use it. This refactor rebuilds the debate page as a **document-first workspace**: the rendered current description is the permanent centerpiece; AI proposals arrive as a single review panel with **Preview** and **What changed** (word-level highlight) tabs; history shrinks to a compact versions rail; the effort gauge becomes a header chip; "Approve final" becomes the prominent header action. All client-facing text moves to plain language ("suggestion", "version", "Use this version") — no "seed", "rounds", "in_review", or `@@` hunks.

The backend keeps its routes, tables, invariants, and error-status discipline. Behind the UI: `internal/diff` gains a word-level inline renderer alongside the existing unified one, the `Hx-Refresh` reload hack after Accept is removed in favor of partial swaps, and three small read-only GET partial endpoints are added. The effort scorer **already runs asynchronously** after Accept (`scoreAfterAccept` fire-and-forget — the shipped handler evolved past §4.3 of the 2026-04-14 spec); this refactor finally gives that async result a UI via a self-polling effort chip.

## 2. Problems this fixes (review findings, 2026-06-12)

1. **Proposals are unreadable** — the only view of a proposal is a line-based unified diff of markdown source; `+` markers glue onto markdown `-` bullets; long lines clip in a non-wrapping `<pre>`.
2. **Current state is invisible** — `feature_debates.current_text` is rendered nowhere; after a few rounds the user cannot tell what the description currently says.
3. **Decisions sink below the fold** — round cards stack downward; Accept/Reject and the next-round form drift ever lower.
4. **Dead interaction** — Accept ends in `Hx-Refresh: true` (#97), a full page reload; the asynchronously-computed effort score is invisible until that reload; errors (409/429/502) produce no visible feedback at all.
5. **Visual noise** — a ~200 px effort gauge for one number, reasoning hidden in a hover tooltip, sidebar that wraps below the timeline on real viewports so `sticky` never engages.
6. **Developer-facing language** shown to non-technical clients.

## 3. In scope / Out of scope

**In scope:**
- Full rewrite of the 5 debate templates into the workspace layout (page + 6 components).
- Word-level inline diff renderer in `internal/diff` (same `sergi/go-diff` dependency).
- HTMX partial/OOB response plumbing in `internal/handlers/debate.go`; removal of the `HX-Refresh` hack from Accept (#97).
- Self-polling effort chip surfacing the already-async scorer result; three new read-only GET endpoints (effort chip partial, view-older-version partial, current-document partial).
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
| header (static) | in page template | back link · "Refine description — {title}" · `#effort-chip` · **✓ Use this version** (primary; subtext "Saves this text to the ticket and finishes refining") · "Stop refining" (subtle) |
| `#document` | `debate_document.html` | rendered markdown of `current_text`, labeled "Current description · version N" with last-change attribution. At version 0 (no accepted rounds) shows "Starting text" + an **Edit** affordance (same rule as the v0.2.0 seed edit: only while no rounds exist — of any status, dismissed included — and nothing is in flight) |
| `#stage` | one of `debate_suggestion.html` / `debate_composer.html` / thinking skeleton | exactly one of: pending suggestion panel, composer, or in-flight skeleton |
| `#versions` | `debate_versions.html` | rail: newest-first entries — accepted rounds as **v1, v2…**, dismissed rounds struck-through, "Original" at the bottom. **View · Restore** exist only on accepted versions (and Original); dismissed entries are context-only, not viewable in v1. Entries show the feedback snippet that produced them (if any); staff/superadmin additionally see model + per-round cost |

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

Implementation: `diffmatchpatch.DiffMain` + `DiffCleanupSemantic` (semantic cleanup merges character-level noise into word/phrase-level runs — that is what makes prose diffs readable). Exposed as FuncMap helper `renderInlineDiff before after`. The "What changed" container renders with `whitespace-pre-wrap` so escaped newlines preserve markdown's line structure (lists, headings, paragraphs) without any `<br>` rewriting in the renderer. During implementation, validate cleanup quality on real ticket prose; if punctuation-heavy text produces noisy fragments, tune with `DiffCleanupEfficiency` before shipping. The old `renderDiff` helper and its template call sites are removed; `ComputeUnified` stays (still persisted to `diff_unified` on round creation for audit/back-compat — cheap, and removing the write would be backend churn for no UI gain).

Escape-proof tests mirror the existing pinned XSS proofs (`TestRenderInlineHTML_EscapesScriptTags`, `_EscapesAttributeInjection`) plus word-level semantics, multiline markdown, and Unicode cases.

### 5.2 Effort chip polling (correction: the scorer is already async)

**Premise correction (2026-06-12 review):** the shipped handler already evolved past §4.3 of the v0.2.0 spec. `internal/handlers/debate.go` commits the accept tx, then fires `scoreAfterAccept` as a **fire-and-forget goroutine** on a request-detached context (`context.WithoutCancel`) bounded by `cfg.AICallTimeout`, applying the scorer result through the single-statement conditional update (`UpdateEffortScoreCondTx` — the `last_scored_round_id` ordering guard) plus cost persistence. **None of that changes.** What changes is the surface: today the fresh score is invisible until the `Hx-Refresh: true` full reload (`debate.go:566`); the reload goes away, so the chip fetches the score itself.

Process-loss semantics are unchanged and accepted: a score lost to pod shutdown mid-goroutine is the same non-fatal class as a scorer failure (WARN logged, previous score retained, next accept rescores). No goroutine tracking or shutdown-drain machinery is added.

**Effort chip polling:** the chip partial is "stale" while `last_scored_round_id` doesn't match the latest accepted round. A stale chip renders "rescoring…" and self-polls `GET …/debate/effort` via `hx-get hx-trigger="load delay:3s" hx-swap="outerHTML"`. The server (never the client) decides per response whether to include the polling trigger: present **only if** the debate is active **and** the latest accept was < 90 s ago (scorer timeout + buffer, matching the stale-reservation constant); after that the chip settles on the previous score (or "—") without polling forever — covers permanent scorer failure and terminal debates without client-side timers.

### 5.3 HTMX flow (replaces the v0.2.0 HTMX table; kills the #97 `HX-Refresh` hack)

| Action | Endpoint (existing unless noted) | Primary target | OOB swaps |
|---|---|---|---|
| Suggest improvements | `POST …/rounds` | `#stage` → suggestion panel | — |
| (while waiting) | — | thinking skeleton via `hx-indicator` on `#stage`; submit buttons `hx-disabled-elt` | — |
| Accept | `POST …/rounds/:rid/accept` | `#stage` → composer | `#document`, `#versions`, `#effort-chip` (stale/polling state) |
| Dismiss | `POST …/rounds/:rid/reject` | `#stage` → composer (previous feedback preserved in the textarea) | `#versions` |
| Restore version N | `POST …/undo?from={roundNumber+1}` (`from=1` for Original) | `#stage` → composer | `#document`, `#versions`, `#effort-chip` |
| View version N | **new** `GET …/debate/versions/:rid` | `#document` → read-only view with "Viewing version N — Back to current · Restore" banner | — |
| Back to current | **new** `GET …/debate/document` | `#document` → current rendered description | — |
| Effort chip poll | **new** `GET …/debate/effort` | `#effort-chip` | — |
| Use this version | `POST …/approve` · `hx-confirm` "Save this text to the ticket and finish refining?" | `HX-Redirect` to ticket (unchanged) | — |
| Stop refining | `POST …/abandon` · `hx-confirm` "Stop refining? The ticket description stays unchanged and this session is archived." | `HX-Redirect` to ticket | — |

All three new GET endpoints go through `requireDebateContext` (tenant-scoped, feature-only, 404 discipline) and are read-only plain `SELECT`s. `…/versions/:rid` serves **accepted** rounds only — a dismissed or unknown round id is a 404. If the debate is no longer active (approved/abandoned in another tab), the document/version GETs return the 409 "out of date" banner of §5.4 and the effort chip renders static with no polling trigger.

Version labels (v1, v2…) are **display-relative and can renumber after a Restore**; every action — View, Restore, Accept, Dismiss — keys on the underlying round UUID (or its `round_number` for undo), never on the label.

### 5.4 Error presentation

Status-code discipline from the v0.2.0 spec is unchanged. For HTMX requests, debate handlers **always render** `debate_error.html` — a dismissible banner partial — as the response body on 4xx/5xx. A small `htmx:beforeSwap` listener (added to `static/js/app/init.js`) sets `shouldSwap = true` for status ≥ 400 when `event.detail.elt.closest('#debate-workspace')` matches; the listener only permits the swap — it cannot supply a body, which is why the handler side is mandatory. The banner lands in `#stage` above the composer. Client-language messages:

| Status | Message |
|---|---|
| 409 in-flight | "A suggestion is already being written — give it a few seconds." |
| 409 stale/terminal | "This page is out of date — reload to see the latest state." (link reloads) |
| 429 caps | clients: "You've reached the suggestion limit for now — ask us if you need more." (no numeric limits exposed); staff/superadmin see the concrete caps |
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

type versionEntry struct {
    RoundID      uuid.UUID // all actions key on this (or RoundNumber) — never on the display label
    RoundNumber  int       // backend ordinal; Restore posts from=RoundNumber+1 (Original: from=1)
    VersionLabel int       // display-only "vN" in acceptance order; 0 for dismissed entries
    Provider     string
    Feedback     string    // snippet shown in the rail, if any
    Accepted     bool      // false → struck-through, context-only entry
    DecidedAt    time.Time
    Model        string    // populated only when IsStaff
    CostMicros   int64     // populated only when IsStaff
}
```

Version numbering (the "v1, v2…" the client sees) is **derived in the handler at render time** by walking rounds in `round_number` order and counting accepted ones — it is not stored, and templates never compute it. Restore maps version N to the existing undo endpoint via the underlying `round_number` (+1).

## 7. Files touched

| Path | Change |
|---|---|
| `templates/pages/debate.html` | rewritten — workspace layout, header actions, regions |
| `templates/components/debate_document.html`, `debate_suggestion.html`, `debate_composer.html`, `debate_versions.html`, `debate_effort_chip.html`, `debate_error.html` | new |
| `templates/components/debate_seed.html`, `debate_round.html`, `debate_next_round.html`, `debate_sidebar.html` | deleted |
| `internal/diff/diff.go` + tests | add `RenderInlineHTML` |
| `internal/render/render.go` | swap `renderDiff` → `renderInlineDiff` FuncMap helper |
| `internal/handlers/debate.go` + tests | view model, OOB partial responses, 3 GET partial endpoints, error partials, remove `Hx-Refresh` (scorer goroutine already exists — untouched) |
| `static/js/app/init.js` | `htmx:beforeSwap` error-banner listener (then `sh scripts/bundle.sh`) |
| `e2e/tests/11-debate.spec.js` | rewritten for new flow |

## 8. Testing

**Unit (`internal/diff`):** word-level correctness (insert-only, delete-only, mixed, markdown punctuation), XSS escape proofs for `<ins>`/`<del>` paths, empty/identical inputs, multiline markdown (lists, headings — asserting newlines survive for `pre-wrap` rendering), Unicode.

**Handler integration (real Postgres, `-p 1`):**
- `TestAccept_ReturnsBeforeScorerCompletes` — pins the existing fire-and-forget behavior: fake scorer blocks on a channel; accept response arrives with composer partial + OOB document while scorer is still blocked; after release, `effort_*` lands (poll the row).
- `TestEffortChipPartial_PollsWhileStale_SettlesAfter90s` — trigger attribute present when stale & recent, absent when stale & old or fresh.
- `TestEffortChipPartial_NoPollingWhenDebateTerminal` — approved/abandoned debate → static chip, no trigger.
- `TestVersionViewPartial_TenantScoped` / `_404OnForeignRound` / `_404OnDismissedRound` — new GET discipline; dismissed rounds are not viewable.
- `TestDocumentPartial_RendersCurrentText` — "Back to current" endpoint.
- `TestStaleRoundAction_RendersErrorBanner409` — accept/dismiss on an already-decided round returns 409 **with the `debate_error.html` body** (the status-only behavior is already pinned by v0.2.0 tests; this asserts the new banner).
- `TestAccept_NoHXRefreshHeader` — regression on the #97 hack removal.
- Existing accept/reject/undo/approve/abandon invariant and concurrency tests (including `TestScorer_StaleResponseDiscarded`, `TestConcurrentAcceptAndUndo_Serialized`) unchanged and must stay green.

**E2E (Playwright, fake refiner):** golden path through the new UI — start, suggest, tab between Preview/What changed, accept (chip shows rescoring → score), dismiss, restore, approve; error banner on a forced 502; "Use this version" disabled while suggestion pending.

## 9. Rollback

Pure code release: revert the image pin to the previous tag. No migration, no data shape change. Late async-scorer goroutines from the new pods finish or time out harmlessly (the `last_scored_round_id` guard and status filters already tolerate them).
