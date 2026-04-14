package ai

import "context"

// FakeRefiner is a test double used by the debate handler tests (tasks 7–9)
// to exercise the full round lifecycle without burning real AI API calls.
// Configure NameVal, ModelVal, and OutputFunc; CallCount tracks invocations.
//
// This file intentionally has NO _test.go suffix: Go restricts test-only
// files to their own package, so putting the fakes in refiner_fake_test.go
// would make them inaccessible to handler tests in internal/handlers_test.
// Shipping ~60 lines of test doubles in the production binary is
// negligible; this matches how net/http/httptest exposes its fakes.
type FakeRefiner struct {
	NameVal, ModelVal string
	// OutputFunc returns (text, finishReason, err). Callers drive behavior
	// by setting this: return an empty text to simulate empty output, set
	// finishReason to "length" to simulate truncation, return err to
	// simulate provider failure.
	OutputFunc func(in RefineInput) (text, finishReason string, err error)
	CallCount  int
}

func (f *FakeRefiner) Name() string  { return f.NameVal }
func (f *FakeRefiner) Model() string { return f.ModelVal }

// Refine invokes OutputFunc and wraps the result in a RefineOutput with
// fake-but-reasonable token counts (~4 chars per token) and cost_micros
// computed via the real pricingTable so cost-accumulator tests exercise
// the production conversion path.
func (f *FakeRefiner) Refine(_ context.Context, in RefineInput) (RefineOutput, error) {
	f.CallCount++
	text, finish, err := f.OutputFunc(in)
	if err != nil {
		return RefineOutput{}, err
	}
	inputTok := len(in.CurrentText) / 4
	outputTok := len(text) / 4
	return RefineOutput{
		Text:         text,
		FinishReason: finish,
		Usage: RefineUsage{
			InputTokens:  inputTok,
			OutputTokens: outputTok,
			CostMicros:   computeCostMicros(f.ModelVal, inputTok, outputTok),
			Model:        f.ModelVal,
		},
	}, nil
}

// FakeScorer is a test double for the Scorer interface. Set Result for
// the success path, Err for the failure path. Delay (optional) is called
// inside Score before returning — use it to simulate a slow scorer in
// out-of-order race tests.
type FakeScorer struct {
	Result    ScoreResult
	Err       error
	Delay     func()
	CallCount int
}

func (f *FakeScorer) Score(_ context.Context, _ string) (ScoreResult, error) {
	f.CallCount++
	if f.Delay != nil {
		f.Delay()
	}
	if f.Err != nil {
		return ScoreResult{}, f.Err
	}
	return f.Result, nil
}

// Compile-time interface assertions. If a signature ever drifts these
// fail to build, catching the regression before any test runs.
var (
	_ Refiner = (*FakeRefiner)(nil)
	_ Scorer  = (*FakeScorer)(nil)
)
