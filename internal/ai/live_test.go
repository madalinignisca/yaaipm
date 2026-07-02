//go:build integration_ai

// Live-API integration suite (issue #67, spec §7.5). One tiny billable
// call per provider adapter, verifying the adapters haven't drifted from
// their SDKs/APIs: non-empty output, positive token counts, a normalized
// FinishReason, and (for the scorer) a structured-output JSON payload
// that parses into ScoreResult.
//
// The build tag above keeps this file out of `go test ./...` so the
// standard suite stays fast and free. Run manually before each release:
//
//	ANTHROPIC_API_KEY=… GEMINI_API_KEY=… OPENAI_API_KEY=… \
//	go test -tags=integration_ai -count=1 -v -run TestLive ./internal/ai/
//
// In production the keys live in the cluster Secret `forgedesk-env`
// (namespace smartpm); docker-compose.real-ai.yml is this suite's
// browser-level equivalent (issue #81).
//
// A skipped test is NO pre-release signal: each provider test skips when
// its key is unset so partial local runs stay possible, but the release
// gate should export all three keys. TestLive_AtLeastOneKeyConfigured
// fails loudly if the tag was requested with no keys at all.

package ai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// liveCallTimeout is generous because reasoning-class OpenAI models
// (gpt-5-mini is the default) can spend tens of seconds thinking before
// the first output token.
const liveCallTimeout = 120 * time.Second

// liveText is deliberately tiny — this suite detects adapter drift, not
// model quality. Short input keeps a full three-provider run in the low
// single-digit cents.
const liveText = "Add a CSV export button to the project cost report. " +
	"Staff only. Include a date range filter."

// liveFeedback is non-empty so the round trip also exercises
// buildRefineUserPrompt's feedback block, matching what a real
// second-round debate request sends.
const liveFeedback = "Mention that the export must respect the viewer's role permissions."

// liveKey returns the env value or skips the calling test. Skips rather
// than fails so a developer with only one provider key can still smoke
// that provider locally.
func liveKey(t *testing.T, env string) string {
	t.Helper()
	k := os.Getenv(env)
	if k == "" {
		t.Skipf("%s not set — skipping (a skip is NO pre-release signal for this provider)", env)
	}
	return k
}

// liveEnvOr mirrors config.envOrDefault so the suite tests the same
// model IDs production defaults to, while still honoring explicit
// overrides (e.g. ANTHROPIC_MODEL pinned to a cheaper tier).
func liveEnvOr(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// assertLiveRefine applies the shared Refiner contract to a live
// response. Every provider test funnels through here so the three
// adapters are held to identical expectations.
func assertLiveRefine(t *testing.T, provider string, out RefineOutput, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s live refine: %v", provider, err)
	}
	if strings.TrimSpace(out.Text) == "" {
		t.Fatalf("%s live refine returned empty text", provider)
	}
	if out.Usage.InputTokens <= 0 || out.Usage.OutputTokens <= 0 {
		t.Errorf("%s live refine returned non-positive token counts: in=%d out=%d",
			provider, out.Usage.InputTokens, out.Usage.OutputTokens)
	}
	if out.Usage.Model == "" {
		t.Errorf("%s live refine returned empty Usage.Model", provider)
	}
	// A tiny rewrite with refinerMaxTokens of headroom must finish
	// normally. If a provider renames or restructures its stop reason,
	// the adapter's mapping drifts and this catches it — exactly the
	// failure class this suite exists for.
	if out.FinishReason != FinishReasonStop {
		t.Errorf("%s live refine FinishReason = %q, want %q",
			provider, out.FinishReason, FinishReasonStop)
	}
	checkLivePricing(t, provider, out.Usage)
	t.Logf("%s ok: model=%s in=%d out=%d cost=%dµ$ finish=%s text[:120]=%q",
		provider, out.Usage.Model, out.Usage.InputTokens, out.Usage.OutputTokens,
		out.Usage.CostMicros, out.FinishReason, out.Text[:min(len(out.Text), 120)])
}

// checkLivePricing asserts cost accounting for priced models and warns
// loudly for unpriced ones. The warning cannot be an Errorf: overriding
// *_MODEL to an experimental ID during local runs is legitimate. But an
// unpriced model in production means every call is recorded at $0 in
// project_costs, so the release gate must at least say so in -v output.
func checkLivePricing(t *testing.T, provider string, usage RefineUsage) {
	t.Helper()
	if ComputeCostMicros(usage.Model, 1000, 1000) > 0 {
		if usage.CostMicros <= 0 {
			t.Errorf("%s: model %s is priced but CostMicros = %d",
				provider, usage.Model, usage.CostMicros)
		}
		return
	}
	t.Logf("WARNING: %s model %q has no pricing-table entry — cost tracking "+
		"records $0 for these calls (see internal/ai/pricing.go)", provider, usage.Model)
}

// TestLive_AtLeastOneKeyConfigured fails — not skips — when the suite
// was explicitly requested via -tags=integration_ai but no provider key
// is set. Without this guard every test would skip and the run would
// report an all-green "ok" that verified nothing.
func TestLive_AtLeastOneKeyConfigured(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" &&
		os.Getenv("GEMINI_API_KEY") == "" &&
		os.Getenv("OPENAI_API_KEY") == "" {
		t.Fatal("integration_ai tag set but no provider API key in env — " +
			"export ANTHROPIC_API_KEY / GEMINI_API_KEY / OPENAI_API_KEY (see file comment)")
	}
}

func TestLive_AnthropicRefiner(t *testing.T) {
	key := liveKey(t, "ANTHROPIC_API_KEY")
	model := liveEnvOr("ANTHROPIC_MODEL", ModelClaudeSonnet46)
	client := NewAnthropicClient(key, AnthropicModels{Default: model, Content: model})
	r := NewAnthropicRefiner(client, model)

	ctx, cancel := context.WithTimeout(t.Context(), liveCallTimeout)
	defer cancel()
	out, err := r.Refine(ctx, RefineInput{CurrentText: liveText, Feedback: liveFeedback})
	assertLiveRefine(t, r.Name(), out, err)
}

func TestLive_GeminiRefiner(t *testing.T) {
	key := liveKey(t, "GEMINI_API_KEY")
	model := liveEnvOr("GEMINI_MODEL", ModelGeminiFlash)

	ctx, cancel := context.WithTimeout(t.Context(), liveCallTimeout)
	defer cancel()
	client, err := NewGeminiClient(ctx, key, GeminiModels{Default: model})
	if err != nil {
		t.Fatalf("gemini client: %v", err)
	}
	r := NewGeminiRefiner(client, model)
	out, err := r.Refine(ctx, RefineInput{CurrentText: liveText, Feedback: liveFeedback})
	assertLiveRefine(t, r.Name(), out, err)
}

func TestLive_OpenAIRefiner(t *testing.T) {
	key := liveKey(t, "OPENAI_API_KEY")
	model := liveEnvOr("OPENAI_MODEL", ModelGPT5Mini)
	r := NewOpenAIRefiner(NewOpenAIClient(key, model))

	ctx, cancel := context.WithTimeout(t.Context(), liveCallTimeout)
	defer cancel()
	out, err := r.Refine(ctx, RefineInput{CurrentText: liveText, Feedback: liveFeedback})
	assertLiveRefine(t, r.Name(), out, err)
}

func TestLive_GeminiScorer(t *testing.T) {
	key := liveKey(t, "GEMINI_API_KEY")
	model := liveEnvOr("GEMINI_MODEL", ModelGeminiFlash)

	ctx, cancel := context.WithTimeout(t.Context(), liveCallTimeout)
	defer cancel()
	client, err := NewGeminiClient(ctx, key, GeminiModels{Default: model})
	if err != nil {
		t.Fatalf("gemini client: %v", err)
	}
	s := NewGeminiScorer(client, model)

	res, err := s.Score(ctx, liveText)
	if err != nil {
		// This branch also covers the structured-output JSON failing to
		// parse — Score wraps json.Unmarshal errors with the raw payload.
		t.Fatalf("gemini live score: %v", err)
	}
	// Score/Hours are clamped in the adapter, so the range checks below
	// are documentation rather than live signal; the real assertions are
	// that the schema-enforced JSON parsed (err == nil above), Reasoning
	// survived the round trip, and usage metadata is populated.
	if res.Score < 1 || res.Score > 10 {
		t.Errorf("score out of range: %d", res.Score)
	}
	if res.Hours < 1 {
		t.Errorf("hours out of range: %d", res.Hours)
	}
	if strings.TrimSpace(res.Reasoning) == "" {
		t.Error("empty reasoning in live scorer response")
	}
	if res.Usage.InputTokens <= 0 || res.Usage.OutputTokens <= 0 {
		t.Errorf("non-positive scorer token counts: in=%d out=%d",
			res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	checkLivePricing(t, "scorer", res.Usage)
	t.Logf("scorer ok: model=%s score=%d hours=%d in=%d out=%d cost=%dµ$ reasoning=%q",
		res.Usage.Model, res.Score, res.Hours,
		res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CostMicros, res.Reasoning)
}
