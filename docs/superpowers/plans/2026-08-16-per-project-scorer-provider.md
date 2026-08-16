# Per-Project Scorer Provider Implementation Plan (issue #63)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. TDD is mandatory: write the failing test, watch it fail, then implement.

**Goal:** Let project admins choose which AI provider runs the debate effort scorer, per project. v1 hardcodes Gemini (`cmd/server/main.go:180-185`). Closes phase-2 issue A (`docs/superpowers/specs/2026-04-14-feature-debate-mode-design.md` §8) / GitHub issue #63.

**Architecture:** Mirror the existing refiner-registry pattern one layer down. `DebateHandler.scorer ai.Scorer` (a single instance) becomes `scorers map[string]ai.Scorer` keyed by provider name, built in `main.go` from whichever API keys are configured — structurally identical to `debateRefiners` (`main.go:161-179`) and to the runtime lookup at `debate.go:344`. Per-project selection is one TEXT column with an inline CHECK. No queue, no cache, no new service; scoring stays a fire-and-forget goroutine plus the existing #68 retry sweep.

**The real work is two new adapters.** Only `GeminiScorer` exists today, and neither `internal/ai/openai.go` nor `anthropic.go` has any structured-output code. Each provider expresses structured output differently: Gemini takes a `ResponseSchema`, OpenAI wants `response_format` with a JSON schema, Anthropic uses a forced tool call.

**Tech Stack:** Go 1.25, chi, pgx (raw SQL), `html/template` SSR, HTMX (`hx-boost`), Alpine.js inline, **Tailwind v4 + DaisyUI v5 via `bash scripts/css.sh`** (standalone binary, no Node — source `static/css/tw-input.css`, output `static/css/tw.css`), Playwright E2E.

---

## PR split

Four PRs, sequential (one in flight at a time):

| PR | Contents | Behavior change? |
|----|----------|------------------|
| 1 | This plan doc + `CLAUDE.md` CSS correction | none |
| 2 | Config + `Scorer` interface + `OpenAIScorer` + `AnthropicScorer` | none (purely additive) |
| 3 | Migration 000034 + models + handler resolution + `main.go` wiring | yes (per-project selection live) |
| 4 | Settings dropdown + effort chip + E2E | yes (admins can change it) |

**Branch per PR**, off latest `main`: `feature/63-scorer-adapters`, `feature/63-scorer-per-project`, `feature/63-scorer-settings-ui`.

**Test environment (needed for all `go test` steps):**
```bash
docker compose -f docker-compose.test.yml up -d postgres
docker compose -f docker-compose.test.yml up migrate
# Always: go test ./internal/... -p 1 -count=1 -timeout 120s   (-p 1 is REQUIRED — shared test DB)
```
`go` lives at `/usr/local/go/bin/go`; `export PATH="/usr/local/go/bin:$PATH"` in non-interactive shells.

---

## Decisions (settled — do not re-litigate)

**Maintainer decisions:**

1. **Full scope, all three providers.** Write real `OpenAIScorer` and `AnthropicScorer` adapters with genuine structured output, not a model swap.
2. **Dedicated cheap scorer models** via new env vars, falling back to the refiner model when unset:
   - `SCORER_MODEL_GEMINI` → defaults to `GEMINI_MODEL` (prod: `gemini-3-flash-preview`, already cheap)
   - `SCORER_MODEL_OPENAI` → defaults to `gpt-5-mini` (`ai.ModelGPT5Mini`, 500/2000 micros per 1k)
   - `SCORER_MODEL_CLAUDE` → defaults to `claude-sonnet-4-6` (`ai.ModelClaudeSonnet46`, 3000/15000)
   - All must go through the trimming helpers from issue #111 (`envOrDefault` trims; `envTrimmed` for raw reads).
3. **Missing provider = skip + WARN, no fallback.** The dropdown offers only providers whose API key is configured; saving an unconfigured or unknown value is rejected with 400. At score time, if the project's provider has no registered scorer: log a WARN, write no score. No silent fallback to Gemini. Matches spec §3.2 ("not a silent fallback") and §11 ("silent for scorer").

**Design decisions made during planning:**

4. **Widen the `Scorer` interface to `Name() / Model() / Score()`,** mirroring `Refiner` (`refiner.go:15-19`). Rationale: the startup pricing guard (`main.go:195-208`, added for #108) must iterate every configured scorer model *before any call is made*. The alternative — a parallel `map[string]string` of models in `main.go` — drifts from the registry silently, which is precisely the #108 failure mode. Cost: two trivial methods on three adapters plus `ai.FakeScorer`.
5. **Record which provider/model produced each score** on `feature_debates` (`effort_scorer_provider`, `effort_scorer_model`), with a backfill. Rationale: `templates/components/debate_effort_chip.html:21` hardcodes "· via Gemini". Reading the project's *current* setting would make the UI lie the moment an admin flips the dropdown — an old Gemini score would render "via ChatGPT". The score snapshot columns (`effort_score/hours/reasoning/scored_at`) already live on `feature_debates`, so these belong beside them and are written in the same conditional UPDATE.

---

## Codebase facts (read before starting)

**Scorer layer:**
- `Scorer` interface: `internal/ai/refiner.go:94-96`. `ScoreResult{Score, Hours, Reasoning, Usage RefineUsage}` at `refiner.go:101-106`; `RefineUsage` at `refiner.go:83-88`.
- `GeminiScorer` at `internal/ai/gemini_scorer.go`: struct 16-19, `NewGeminiScorer(c, model)` 25-27, `Score` 33-101 (`ResponseMIMEType: "application/json"` + `ResponseSchema`, Temperature 0.2, defensive clamps 81-82, `ComputeCostMicros` 97). **Touches the unexported `s.client.client` field — all scorers must live in package `ai`.**
- Shared prompt: `internal/ai/prompts.go:21-22` embeds `prompts/debate_score_system.md` as `debateScoreSystemPrompt`. Provider-agnostic; reuse for all three.
- Test double: `ai.FakeScorer` at `internal/ai/refiner_fake.go:55-71`; compile-time assertions at 75-78.

**Wiring (`cmd/server/main.go`):**
- Refiner registry to mirror: 161-179 (`map[string]ai.Refiner`, keys `claude`/`gemini`/`openai`, each guarded by a non-empty API key). Fake variant `buildFakeDebateRefiners()` at 460-477, swapped in when `DEBATE_REFINER_MODE=fake`.
- Current scorer construction: 180-185 (carries a comment already pointing at #63). **Fake mode does not currently cover the scorer.**
- Injection: `handlers.NewDebateHandler(db, engine, debateRefiners, debateScorer, debateCfg)` at 186-188.
- Startup pricing guard: 195-208 (scorer checked at 202-204 via `ai.HasPricing`).
- Sweep gate: 394-402 (`if debateScorer != nil { go ticker → debateH.RetryStaleEffortScores(ctx) }`).
- Log lines reporting `scorer=%v`: 188 and 214.

**Scorer call sites — both already have `ProjectID` in scope:**
1. Accept: `AcceptRound` at `debate.go:613` → `go h.scoreAfterAccept(ctx, deb.ID, round.ID, dctx.ticket.ProjectID, round.OutputText)` at 656-659 → `scoreAfterAccept` 729-750 → `h.scorer.Score(...)` at **735**.
2. Sweep: `RetryStaleEffortScores` 824-842 (nil-scorer no-op 825-827) → `retryOneEffortScore` 846-863 → `h.scorer.Score(...)` at **850**. `models.StaleEffortDebate` (`queries.go:2750-2755`) already carries `ProjectID`.
- Shared write path `applyScoreResult` 763-806. `effortScoreWriteTimeout` 15s at 754.
- Refiner lookup precedent (no fallback, 400 on miss): `debate.go:344`. Fixed display order in `providerNames()` 204-213.

**Models:**
- `Project` struct `models.go:72-81`. Four query functions enumerate columns explicitly and **must change in lockstep**: `CreateProject` 820-831, `GetProject` 833-843, `GetProjectByID` 845-855, `ListProjects` 857-875.
- UPDATE precedent: `UpdateProjectRepoURL` `queries.go:886-893`.
- `FeatureDebate` `models.go:240-260`; `featureDebateColumns` `queries.go:2186`; `scanFeatureDebate` 2197; `UpdateEffortScoreCondTx` 2711-2732; `ClaimStaleEffortScores` RETURNING 2828+.

**UI:**
- `templates/pages/project_settings.html` (94 lines): repo card 9-43 (POSTs to `/orgs/{{$.Org.Slug}}/projects/{{.Project.Slug}}/settings/repo`, `{{csrfField}}` at 20), transfer card 45-90.
- `ProjectSettings` handler `internal/handlers/projects.go:235-257` — **staff gate is in Go** (`auth.IsStaffOrAbove` 237-240), passes `IsStaff: true` at 255. Same guard in `UpdateRepoURL` 309-337 and `TransferProject` 261-264. Page data struct 24-32.
- Routes `main.go:308-310`, inside the auth + CSRF group.
- Template staff-only precedent: `templates/pages/org_settings.html:155`.
- FuncMap already has `providerLabel` (`render.go:270-280`, maps `openai` → "ChatGPT"), `derefStr` (220), `relTime`, `dict`, `csrfField`.
- **PR #80 gotcha:** a conditional bare attribute inside a tag (`{{if X}}selected{{end}}`) can silently truncate `html/template` output. For a conditional `selected`, duplicate the whole `<option>` tag in `{{if}}…{{else}}…{{end}}`.

**Migrations:** highest is `000033_effort_score_retry`. House style opens with a rationale comment block referencing the issue. Enum style is **TEXT + inline CHECK** (no PG `CREATE TYPE`) per `000032_feature_debates.up.sql:38`. Defaulted-column precedents: `000025`, `000021`, `000014`.

**Verified SDK APIs (pinned versions — do not substitute other shapes):**
- go-openai **v1.41.2**: `ChatCompletionResponseFormatJSONSchema{Name string; Description string; Schema json.Marshaler; Strict bool}` at `chat.go:219-224`, referenced from `ChatCompletionResponseFormat.JSONSchema` (`chat.go:216`). `Schema` is a `json.Marshaler` — `json.RawMessage` satisfies it. `Refusal` field at `chat.go:100`.
- anthropic-sdk-go **v1.26.0**: `ToolChoiceToolParam{Name; DisableParallelToolUse param.Opt[bool]; Type constant.Tool}` at `message.go:6636-6647`; `ToolParam` 6390; `ToolInputSchemaParam` 6442; `ToolUnionParam` 7365; `ToolChoiceUnionParam` 6505; tool-use `Input` is `json.RawMessage` at `message.go:1458`.
- **OpenAI reasoning-model gotcha:** go-openai's `ReasoningValidator` rejects `MaxTokens` on `gpt-5*`/`o1*`/`o3*`/`o4*` and requires Temperature 0 or exactly 1. Those models need `MaxCompletionTokens` and no Temperature. The default scorer model `gpt-5-mini` **is** a reasoning model. `OpenAIRefiner.Refine` (`openai.go:57-77`) already handles this — reuse `isReasoningOpenAIModel` (`openai.go:114`), do not re-implement.

---

# PR 2 — Scorer adapters (no behavior change)

Branch `feature/63-scorer-adapters`. Nothing in this PR changes runtime behavior: the new adapters are constructed nowhere until PR 3.

### Task 2.1: Config — three scorer model vars

**Files:** modify `internal/config/config.go`, `internal/config/config_test.go`

- [ ] **RED** — in `config_test.go`, assert each var falls back correctly when unset (including `SCORER_MODEL_GEMINI` → a *customised* `GEMINI_MODEL`, not just the literal default) and that whitespace is trimmed. Follow the style of `TestLoad_Defaults` / `TestLoad_CustomOverrides`.
- [ ] **GREEN** — add `ScorerModelGemini`, `ScorerModelOpenAI`, `ScorerModelClaude` to the `Config` struct near the other model fields. `Load()` builds `&Config{…}` as a single literal, so hoist the Gemini model to a local first:

```go
// before the &Config{...} literal:
geminiModel := envOrDefault("GEMINI_MODEL", "gemini-2.5-flash")
// inside the literal:
GeminiModel:       geminiModel,
ScorerModelGemini: envOrDefault("SCORER_MODEL_GEMINI", geminiModel),
ScorerModelOpenAI: envOrDefault("SCORER_MODEL_OPENAI", "gpt-5-mini"),       // ai.ModelGPT5Mini
ScorerModelClaude: envOrDefault("SCORER_MODEL_CLAUDE", "claude-sonnet-4-6"), // ai.ModelClaudeSonnet46
```

Use string literals, not `ai.Model*` constants — `internal/config` does not import `internal/ai`, and the existing model defaults are literals (`config.go:172,191,194`). Name the constant in a comment. `envOrDefault` already trims (#111).

### Task 2.2: Widen the `Scorer` interface + shared helpers

**Files:** modify `internal/ai/refiner.go`, `internal/ai/gemini_scorer.go`, `internal/ai/refiner_fake.go`

- [ ] **RED** — table test for `clampScoreFields`; tests asserting `Name()`/`Model()` on `GeminiScorer` and `FakeScorer`.
- [ ] **GREEN**:

```go
type Scorer interface {
    Name() string  // "claude" | "gemini" | "openai"
    Model() string // specific model ID, for the pricing guard and audit trail
    Score(ctx context.Context, text string) (ScoreResult, error)
}

const (
    scorerMaxTokens   = 512 // {score,hours,reasoning} is tiny; reasoning capped at 25 words
    scorerTemperature = 0.2 // matches GeminiScorer today — stable re-scores
)

// clampScoreFields applies the defensive bounds every adapter shares.
// Gemini enforces them server-side via ResponseSchema; OpenAI strict mode
// and Anthropic tool schemas do NOT support numeric minimum/maximum, so for
// those two this is the only enforcement.
func clampScoreFields(score, hours int) (int, int)
```

- [ ] Add `Name()`/`Model()` to `GeminiScorer`; replace its inline clamps (`gemini_scorer.go:81-82`) with `clampScoreFields`.
- [ ] Add `NameVal`/`ModelVal` to `FakeScorer` with the corresponding methods. The compile-time assertions at `refiner_fake.go:75-78` are the guard that the whole repo still builds.

### Task 2.3: `OpenAIScorer`

**Files:** create `internal/ai/openai_scorer.go`, add tests to `internal/ai/scorer_test.go`

`OpenAIClient` is model-bound at construction (`openai.go:14-28`) and its doc comment sanctions building multiple clients — PR 3 will build a second one bound to `cfg.ScorerModelOpenAI`.

- [ ] **RED** — `httptest.Server` based tests (see "Test plan" below for the full case list). **Verify first** how to point go-openai at a test base URL in v1.41.2 (`ClientConfig.BaseURL` via `openai.NewClientWithConfig`); if injecting it into the existing constructor is awkward, add a small unexported test-only constructor rather than contorting production code.
- [ ] **GREEN**:

```go
type OpenAIScorer struct{ c *OpenAIClient }

func NewOpenAIScorer(c *OpenAIClient) *OpenAIScorer
func (s *OpenAIScorer) Name() string  { return "openai" }
func (s *OpenAIScorer) Model() string // "" if c == nil, mirroring OpenAIRefiner.Model()
func (s *OpenAIScorer) Score(ctx context.Context, text string) (ScoreResult, error)
```

Request shape:

```go
req := openai.ChatCompletionRequest{
    Model: s.c.model,
    Messages: []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleSystem, Content: debateScoreSystemPrompt},
        {Role: openai.ChatMessageRoleUser,   Content: text},
    },
    ResponseFormat: &openai.ChatCompletionResponseFormat{
        Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
        JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
            Name:   "debate_effort_score",
            Schema: json.RawMessage(scorerOpenAISchema),
            Strict: true,
        },
    },
}
if isReasoningOpenAIModel(s.c.model) { // openai.go:114 — reuse
    req.MaxCompletionTokens = scorerMaxTokens
} else {
    req.MaxTokens   = scorerMaxTokens
    req.Temperature = scorerTemperature
}
```

- [ ] Keep these error paths **distinct**, not collapsed into one branch (this repo's reviewers consistently reject merged error conditions): nil client; API error; `len(resp.Choices) == 0`; non-empty `choice.Message.Refusal`; empty `Content`; JSON unmarshal failure (include truncated raw text in the wrapped error, as `gemini_scorer.go:74` does).
- [ ] Apply `clampScoreFields`; compute cost via `ComputeCostMicros(s.c.model, …)`; populate `Usage.Model`.

⚠️ **Verify during implementation:** OpenAI strict structured outputs support only a subset of JSON Schema — `minimum`/`maximum`/`maxLength` are believed **unsupported**, and strict mode requires `"additionalProperties": false` with every property listed in `required`. Confirm against current OpenAI docs. If correct, the schema is type-only and `clampScoreFields` plus the prompt's stated bands are the only enforcement — which is exactly why the clamps are shared rather than Gemini-local.

### Task 2.4: `AnthropicScorer`

**Files:** create `internal/ai/anthropic_scorer.go`, add tests to `internal/ai/scorer_test.go`

Forced single-tool-use is the idiomatic structured-output path; Anthropic has no JSON-mode equivalent.

- [ ] **RED** — `httptest.Server` tests. **Verify** how to set a base URL in anthropic-sdk-go v1.26.0 (`option.WithBaseURL`).
- [ ] **GREEN**:

```go
type AnthropicScorer struct {
    client *AnthropicClient
    model  string
}

func NewAnthropicScorer(c *AnthropicClient, model string) *AnthropicScorer
func (s *AnthropicScorer) Name() string  { return "claude" }
func (s *AnthropicScorer) Model() string { return s.model }
func (s *AnthropicScorer) Score(ctx context.Context, text string) (ScoreResult, error)
```

```go
const scorerToolName = "record_effort_score"

resp, err := s.client.client.Messages.New(ctx, anthropic.MessageNewParams{
    Model:       anthropic.Model(s.model),
    MaxTokens:   scorerMaxTokens,
    Temperature: anthropic.Float(scorerTemperature),
    System:      []anthropic.TextBlockParam{{Text: debateScoreSystemPrompt}},
    Messages:    []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(text))},
    Tools: []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
        Name:        scorerToolName,
        Description: anthropic.String("Record the effort score for the feature description."),
        InputSchema: anthropic.ToolInputSchemaParam{
            Properties: scorerAnthropicProperties, // map[string]any
            Required:   []string{"score", "hours", "reasoning"},
        },
    }}},
    ToolChoice: anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{
        Name:                   scorerToolName,
        DisableParallelToolUse: anthropic.Bool(true),
    }},
})
```

- [ ] Extraction: iterate `resp.Content`, take the first block where `block.Type == "tool_use" && block.Name == scorerToolName`, then `json.Unmarshal(block.Input, &out)`.
- [ ] Distinct errors for: nil client; API error; **no tool_use block found** (model returned prose despite forced tool choice); unmarshal failure.
- [ ] Usage from `resp.Usage.InputTokens/OutputTokens` (as `anthropic.go:131-132`); cost via `ComputeCostMicros(s.model, …)`.

### Task 2.5: PR 2 verification

- [ ] `go build ./... && go vet ./... && gofmt -l internal/`
- [ ] `go test ./internal/ai/ ./internal/config/ -count=1`
- [ ] `golangci-lint run ./...` (full repo, 0 issues) before pushing
- [ ] Optionally extend `internal/ai/live_test.go` (behind `//go:build integration_ai`) with one billable call per new adapter — never runs in per-PR CI.

---

# PR 3 — Per-project selection (migration + wiring)

Branch `feature/63-scorer-per-project`.

### Task 3.1: Migration 000034

**Files:** create `migrations/000034_project_scorer_provider.up.sql` / `.down.sql`

```sql
-- Phase-2 issue #63: per-project configurable debate scorer provider.
--
-- v1 hardcoded Gemini as the scorer (spec §3.2, cmd/server/main.go). This
-- migration adds the two pieces of state that per-project selection needs:
--
--   projects.scorer_provider          which provider runs the scorer for
--                                     debates on this project. Staff-only
--                                     setting; clients never see it.
--   feature_debates.effort_scorer_*   which provider/model actually produced
--                                     the CURRENT effort_* snapshot. Without
--                                     this the effort chip would have to read
--                                     the project's live setting and would
--                                     mislabel every score taken before an
--                                     admin last changed the dropdown.
--
-- TEXT + inline CHECK rather than a PG enum, matching the provider column on
-- feature_debate_rounds (000032, line 38): adding a fourth provider later is
-- an ALTER of the constraint, not a type migration.
--
-- Existing projects default to 'gemini', which is exactly what v1 did, so
-- behaviour is unchanged until an admin opts in. The backfill below stamps
-- already-scored debates as gemini-scored for the same reason — it is a
-- statement of historical fact, not a guess.

ALTER TABLE projects
    ADD COLUMN scorer_provider TEXT NOT NULL DEFAULT 'gemini'
        CHECK (scorer_provider IN ('claude', 'gemini', 'openai'));

ALTER TABLE feature_debates
    ADD COLUMN effort_scorer_provider TEXT
        CHECK (effort_scorer_provider IS NULL
               OR effort_scorer_provider IN ('claude', 'gemini', 'openai')),
    ADD COLUMN effort_scorer_model TEXT;

-- Every score that exists today was produced by GeminiScorer. Stamping the
-- provider (but not the model — the exact GEMINI_MODEL in force at the time
-- is not recoverable) keeps the effort chip's "via Gemini" label correct for
-- historical debates instead of silently dropping it after deploy.
UPDATE feature_debates
   SET effort_scorer_provider = 'gemini'
 WHERE effort_scored_at IS NOT NULL;
```

```sql
-- down
ALTER TABLE feature_debates
    DROP COLUMN IF EXISTS effort_scorer_model,
    DROP COLUMN IF EXISTS effort_scorer_provider;

ALTER TABLE projects
    DROP COLUMN IF EXISTS scorer_provider;
```

- [ ] Verify `migrate up` → `migrate down 1` → `up` against the compose Postgres.

**Live-DB safety:** both tables are small (tens of projects, low-hundreds of debates), so the ACCESS EXCLUSIVE lock is held for milliseconds. Deploy order is **migration first (purely additive), then image** — the new columns are invisible to v0.4.0 pods because every debate SELECT goes through the explicit `featureDebateColumns` list.

### Task 3.2: Models layer (keep atomic)

**Files:** modify `internal/models/models.go`, `internal/models/queries.go` + tests

Splitting this is worse than keeping it in one commit: the column list and scan order must move together.

| Symbol | Change |
|---|---|
| `Project` (`models.go:72-81`) | `+ ScorerProvider string \`db:"scorer_provider"\`` (preserve field alignment) |
| `CreateProject`/`GetProject`/`GetProjectByID`/`ListProjects` (820-875) | add `scorer_provider` to the column list **and** the `Scan` target — all four |
| new `UpdateProjectScorerProvider(ctx, projectID, provider string) error` | modelled on `UpdateProjectRepoURL` (886-893) |
| new `GetProjectScorerProvider(ctx, projectID string) (string, error)` | one-column SELECT for the accept path — avoids pulling `brief_markdown` into a goroutine that needs one word |
| `FeatureDebate` (`models.go:240-260`) | `+ EffortScorerProvider *string`, `+ EffortScorerModel *string` |
| `featureDebateColumns` (2186) + `scanFeatureDebate` (2197) | add both columns in matching order |
| `UpdateEffortScoreCondTx` (2711-2732) | new params `provider, model string`; set both new columns |
| `StaleEffortDebate` (2750-2755) | `+ ScorerProvider string` |
| `ClaimStaleEffortScores` RETURNING (2828+) | add `(SELECT p.scorer_provider FROM projects p WHERE p.id = fd.project_id)` — a correlated scalar subquery, the same shape already used there for round id / output_text |

- [ ] **RED then GREEN** per the test plan below. The four-scan-sites change is the highest-risk item in this PR: a miss is a **runtime** scan error, not a compile error.

The correlated subquery gives the sweep per-project provider selection at **zero extra round-trips**, and because it reads `projects` (which the claim does not lock), the `FOR UPDATE SKIP LOCKED` semantics that prevent double-billing across two replicas are untouched.

### Task 3.3: `DebateHandler` resolution

**Files:** modify `internal/handlers/debate.go`; update `debate_test.go:31,50`, `debate_retry_test.go:19`, `debate_empty_providers_test.go:40`

- [ ] Field becomes `scorers map[string]ai.Scorer`; constructor:

```go
func NewDebateHandler(db *models.DB, engine *render.Engine,
    refiners map[string]ai.Refiner, scorers map[string]ai.Scorer, cfg DebateConfig) *DebateHandler
```

- [ ] Add resolution helper:

```go
var errScorerNotConfigured = errors.New("no scorer registered for provider")

// scorerForProject loads the project's configured scorer provider and returns
// the matching Scorer. Three distinct failures, deliberately not merged: a DB
// error is an infra problem, an unknown/unregistered provider is an operator
// configuration problem, and they get different log lines.
func (h *DebateHandler) scorerForProject(ctx context.Context, projectID string) (ai.Scorer, string, error)
```

- [ ] **Accept path:** the cheap gate at 654 becomes `if len(h.scorers) > 0`; resolution happens **inside** `scoreAfterAccept` so the user's request still returns immediately. Before the `Score` call at 735:

```go
scorer, provider, err := h.scorerForProject(callCtx, projectID)
switch {
case errors.Is(err, errScorerNotConfigured):
    log.Printf("WARNING: debate scoreAfterAccept: project %s is configured for scorer provider %q "+
        "but no such scorer is registered (missing API key?) — debate %s round %s left unscored",
        projectID, provider, debateID, roundID)
    return
case err != nil:
    log.Printf("debate scoreAfterAccept: resolving scorer for project %s: %v", projectID, err)
    return
}
```

- [ ] **Sweep path:** `RetryStaleEffortScores` nil-check at 825 becomes `if len(h.scorers) == 0 { return }`; `retryOneEffortScore` resolves by map lookup only (no query — the provider arrived with the claim):

```go
scorer, ok := h.scorers[d.ScorerProvider]
if !ok {
    log.Printf("WARNING: debate RetryStaleEffortScores: project %s configured for scorer provider %q "+
        "with no registered scorer — debate %s round %s left unscored",
        d.ProjectID, d.ScorerProvider, d.DebateID, d.RoundID)
    return
}
```

- [ ] `applyScoreResult` (763-806) gains a `provider string` parameter, passing `provider` + `res.Usage.Model` to `UpdateEffortScoreCondTx`. Both callers already know the provider from resolution.
- [ ] **No fallback to Gemini anywhere.**

### Task 3.4: `main.go` wiring

**Files:** modify `cmd/server/main.go`

- [ ] Replace 180-185:

```go
debateScorers := map[string]ai.Scorer{}
if cfg.AnthropicAPIKey != "" {
    // Separate client: the scorer model is independently configurable
    // (SCORER_MODEL_CLAUDE) and may differ from the refiner's.
    debateScorers["claude"] = ai.NewAnthropicScorer(
        ai.NewAnthropicClient(cfg.AnthropicAPIKey, ai.AnthropicModels{
            Default: cfg.ScorerModelClaude, Content: cfg.AnthropicModelContent,
        }), cfg.ScorerModelClaude)
}
if geminiClient != nil {
    debateScorers["gemini"] = ai.NewGeminiScorer(geminiClient, cfg.ScorerModelGemini)
}
if cfg.OpenAIAPIKey != "" {
    debateScorers["openai"] = ai.NewOpenAIScorer(ai.NewOpenAIClient(cfg.OpenAIAPIKey, cfg.ScorerModelOpenAI))
}
if debateRefinerMode == debateRefinerModeFake {
    debateScorers = buildFakeDebateScorers()
}
```

- [ ] Add `buildFakeDebateScorers() map[string]ai.Scorer` mirroring `buildFakeDebateRefiners()` (460-477): three `*ai.FakeScorer` with distinguishable `Result.Reasoning` ("scored by claude" / …) and `Usage.Model` set to the matching `ai.Model*` constant. **Required** — without it the fake-backed E2E stack cannot exercise per-project selection at all, which is the whole feature.
- [ ] **Pricing guard (195-208):** replace the single `cfg.GeminiModel` check at 202-204 with an iteration over the registry:
  `for _, s := range debateScorers { if m := s.Model(); m != "" && !ai.HasPricing(m) { unpriced[m] = struct{}{} } }`
  This is the concrete payoff of widening the interface (decision 4).
- [ ] **Sweep gate (394):** `if debateScorer != nil` → `if len(debateScorers) > 0`.
- [ ] **Log lines (188, 214):** `scorer=%v` → `scorers=%d`.

### Task 3.5: PR 3 verification

- [ ] Full test suite: `go test ./internal/... -p 1 -count=1 -timeout 120s`
- [ ] `go build ./... && go vet ./... && gofmt -l .` and full-repo `golangci-lint run ./...`
- [ ] Boot locally with `DEBATE_REFINER_MODE=fake`; confirm the startup log reports `scorers=3` and emits no unpriced-model WARNING.

---

# PR 4 — Settings UI + effort chip

Branch `feature/63-scorer-settings-ui`.

### Task 4.1: Settings backend

**Files:** modify `internal/handlers/projects.go`, `cmd/server/main.go`; create `internal/handlers/projects_scorer_test.go`

- [ ] Constructor gains the configured provider list (3 call sites: `projects.go:20`, `main.go:113`, `handlers_test.go:47`):

```go
func NewProjectHandler(db *models.DB, engine *render.Engine, scorerProviders []string) *ProjectHandler
```

`main.go` passes the registry keys in the same fixed `claude, gemini, openai` order as `providerNames()` (`debate.go:204-213`) so the dropdown never shuffles.

- [ ] New route beside the repo route at `main.go:309`, inside the existing auth + CSRF group:
  `r.Post("/orgs/{orgSlug}/projects/{projSlug}/settings/scorer", projH.UpdateScorerProvider)`
- [ ] `func (h *ProjectHandler) UpdateScorerProvider(w http.ResponseWriter, r *http.Request)`, checks in the same order as `UpdateRepoURL` (309-337):
  1. `if !auth.IsStaffOrAbove(user.Role)` → **403** (the Go gate is authoritative; the template guard is cosmetic).
  2. `getOrgAndProject` → 404 on error.
  3. `provider := strings.TrimSpace(r.FormValue("scorer_provider"))`; **400** if not in `h.scorerProviders`. One check covers both "unknown value" and "known provider, no API key configured".
  4. `UpdateProjectScorerProvider` → 500 on error.
  5. `Hx-Redirect` back to settings, as `UpdateRepoURL:335-336`.
- [ ] `projectPageData` (24-32) gains `ScorerProviders []string`; `ProjectSettings` (235-257) populates it.

### Task 4.2: Settings template

**Files:** modify `templates/pages/project_settings.html`

- [ ] Insert a card between the repo card (9-43) and the transfer card, same card/form/`{{csrfField}}` structure, using a DaisyUI `<select class="select select-bordered w-full">` with `{{providerLabel $p}}` for option text.
- [ ] Two states to get right:
  - **Stored provider no longer configured** (key removed from the cluster Secret): render it as an extra `selected` option labelled e.g. `Gemini (API key not configured)`. Showing the truth beats a select that silently appears to say something else. Saving it is rejected by check 3, which is fine — an admin only saves when changing.
  - **Zero configured providers:** disabled select plus a short note, mirroring the "No AI providers are configured" composer path already covered by `debate_empty_providers_test.go`.
- [ ] Add a one-line hint under the dropdown noting Claude is the most expensive option (see risks).
- [ ] Remember the PR #80 conditional-attribute gotcha for `selected`.
- [ ] Rebuild CSS if any new utility classes are introduced: `bash scripts/css.sh`.

### Task 4.3: Effort chip

**Files:** modify `templates/components/debate_effort_chip.html`; extend the chip test in `debate_view_test.go`

- [ ] Replace the hardcoded `· via Gemini` at line 21:

```gotemplate
{{if .Debate.EffortScoredAt}}Updated {{relTime .Debate.EffortScoredAt}}{{if .Debate.EffortScorerProvider}} · via {{providerLabel (derefStr .Debate.EffortScorerProvider)}}{{end}}{{end}}
```

Both helpers already exist (`render.go:220, 270`). The migration backfill means existing debates keep saying "via Gemini"; only a NULL (impossible for new scores) drops the suffix.

### Task 4.4: E2E — one spec only

**Files:** create/extend a spec under `e2e/tests/`

- [ ] Staff logs in → project settings → change scorer dropdown to Claude → save → open a feature debate → create + accept a round → effort chip settles showing "via Claude". Requires `buildFakeDebateScorers()` from Task 3.4.
- [ ] **Do not** add E2E coverage for the 403 path, the validation errors, or the other two providers — those are functional-test territory.

---

## Test plan (~70/100 thoroughness)

**`internal/ai` (unit, no DB, no network) — `scorer_test.go`:**
- Both new adapters against an `httptest.Server`:
  - happy path → correct `Score`/`Hours`/`Reasoning`, token counts, `Usage.Model`, non-zero `CostMicros`
  - **clamps**: model returns `score: 47, hours: 0` → clamped to `10` / `1`
  - malformed JSON → wrapped parse error, no partial `ScoreResult`
  - API 500 → wrapped error
  - OpenAI-specific: empty `Choices`; non-empty `Refusal`; **request-shape assertion** that a `gpt-5*` model sends `max_completion_tokens` and no `temperature`, while a `gpt-4*` model sends `max_tokens` + temperature (this is the regression that bites)
  - Anthropic-specific: response with only a text block (no `tool_use`) → distinct error; assert the outgoing request carries `tool_choice.type=tool` with the right name
- `clampScoreFields` table test; `Name()`/`Model()` for all three adapters.
- Skip: exhaustive JSON-schema literal assertions.

**`internal/config`:** each `SCORER_MODEL_*` falls back correctly when unset (including `SCORER_MODEL_GEMINI` → a customised `GEMINI_MODEL`) and trims whitespace.

**`internal/models` (integration, `-p 1`):**
- `CreateProject` defaults `ScorerProvider` to `"gemini"`; all four read paths round-trip it.
- `UpdateProjectScorerProvider` persists; an invalid value is rejected by the CHECK (assert the DB error — cheap proof the constraint is live).
- `GetProjectScorerProvider` returns the value; `pgx.ErrNoRows` for an unknown project.
- `UpdateEffortScoreCondTx` writes provider + model; the out-of-order-discard behaviour still holds (extend the existing test).
- `ClaimStaleEffortScores` returns `ScorerProvider` from the joined project (extend `queries_debate_retry_test.go`).

**`internal/handlers` — `debate_scorer_provider_test.go`:**
- Accept path picks the project's provider: two distinguishable `FakeScorer`s, project set to `"openai"` → openai fake `CallCount == 1`, claude fake `== 0`, `effort_scorer_provider == "openai"`.
- **Skip+WARN, no fallback:** project `"claude"`, registry has only `"gemini"` → accept still succeeds, `effort_score` stays NULL, gemini fake `CallCount == 0`.
- Both cases again for `RetryStaleEffortScores` via `newScorerHandler`.
- Empty registry → sweep no-ops, accept still succeeds (guards the `len()` gate that replaced the nil check).

**`internal/handlers` — `projects_scorer_test.go`:**
- Client role POSTs the scorer route → 403.
- Staff POSTs `openai` when configured → 200 + persisted.
- Staff POSTs `openai` when **not** configured → 400 + unchanged in DB.
- Staff POSTs garbage (`"llama"`) → 400 + unchanged.
- `ProjectSettings` rendered for staff contains the select.

**Render:** the chip test in `debate_view_test.go` gains an assertion that a claude-scored debate renders "via Claude" — this is the assertion that catches the hardcoded-Gemini regression.

**Manual check before deploy:** on staging, run migration 000034, restart, confirm the startup log reports `scorers=3` with no unpriced-model WARNING, then flip one project to Claude and accept a round.

---

## Risks and edge cases

- **Scan misalignment.** Adding a column to `projects` means four `Scan` call sites change in lockstep (`queries.go:820-875`). A missed one is a runtime error, not a compile error. Task 3.2 tests must cover all four.
- **Provider configured but key later removed.** Every accept and every sweep pass for that project logs a WARN and writes nothing. The sweep's exponential backoff (capped at 1h) bounds this to ~1 line/hour/debate. Self-healing, but note it in the deploy notes — a misconfigured project looks like "the effort chip never fills in", and the WARN is the only signal.
- **Provider changed mid-flight.** An admin flips the dropdown while a `scoreAfterAccept` goroutine is running: the old provider's result lands with `effort_scorer_provider` = old provider. That is correct — it *is* who scored it — and honest in the UI.
- **`gpt-5-mini` reasoning-model request shape.** Getting this wrong fails **client-side** in go-openai's validator, so it surfaces as a hard test failure rather than a production mystery. Still the single most likely implementation slip.
- **Anthropic scorer cost.** Defaulting to `claude-sonnet-4-6` (3000/15000 micros per 1k) makes Claude-scored projects roughly 6× the input / 7.5× the output cost of `gpt-5-mini`, and far more than Gemini flash. A deliberate consequence of there being no verified Haiku pricing entry — hence the hint under the dropdown.
- **Two-replica sweep.** Unchanged: `FOR UPDATE SKIP LOCKED` + backoff lease still prevent double-billing. The added scalar subquery doesn't affect locking semantics.
- **CHECK constraint drift.** The provider set now appears in four places: migration CHECK, `providerNames()` order, `providerLabel`, registry keys. Adding a fourth provider means touching all four. Not worth abstracting for three values, but note it.
- **GDPR / data residency:** no new PII. The change routes *feature description text* to Anthropic/OpenAI, which the refiner already does for the same text on the same projects — no new residency surface. Worth stating explicitly: a project can now be configured so all its debate AI traffic goes to a US vendor, which was already true via the refiner picker.
- **Prod PostgreSQL major version is unverified** (10.0.0.6); CI pins `postgres:17-alpine`. The migration uses nothing version-specific beyond `ADD COLUMN … DEFAULT` (PG 11+).

---

## Out of scope

1. **A cheap Claude/Haiku pricing entry.** No verified Haiku model ID or rate exists in `internal/ai/pricing.go`, and inventing one is not acceptable. Claude scoring therefore defaults to the priced `claude-sonnet-4-6` and is the expensive option. → **Follow-up issue: add a verified Haiku pricing entry and re-default `SCORER_MODEL_CLAUDE`.**
2. **Per-round scorer provider/model audit** on `feature_debate_rounds` (beside `scorer_cost_micros`). The snapshot on `feature_debates` covers the UI need; a full per-call audit trail is a separate, larger change.
3. **Re-scoring existing debates when the dropdown changes.** Affects future accepts only.
4. **Org-level default** that new projects inherit. The column default is a global `'gemini'`.
5. **Making the *refiner* per-project configurable** — that stays a per-round user choice.
6. **Budget enforcement** (phase-2 issue B / #64) — untouched.
7. **Ripping out the `filippo.io/csrf/gorilla` shim** — the new form uses `{{csrfField}}` exactly like the existing two; that debt stays where it is.

---

## Unverified — confirm during implementation

- OpenAI **strict** structured-output JSON Schema subset: whether `minimum`/`maximum`/`maxLength` are permitted, and the `additionalProperties: false` + all-fields-required requirement. Affects the schema literal only; the Go-side clamps make either answer safe.
- How to point go-openai and anthropic-sdk-go at an `httptest.Server` base URL in these exact pinned versions (`ClientConfig.BaseURL` / `option.WithBaseURL`) — needed for adapter unit tests, not for production code.
- Production PostgreSQL major version on 10.0.0.6 (CI is pinned to 17).

Everything else in this plan — SDK type shapes and field names, line numbers, existing patterns — was read directly from the repo or the pinned module cache on 2026-08-16 against `main` @ `f4a7ddf`.
