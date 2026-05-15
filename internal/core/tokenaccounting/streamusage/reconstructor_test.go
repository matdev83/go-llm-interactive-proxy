package streamusage

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestReconstructor_preservesProviderBillableAndAddsClientVisibleOutput(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(11, 7, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 10, TotalTokens: 10, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 4, TotalTokens: 4, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result := reconstructForTest(t, reconstructor, Input{
		Backend:    "openai",
		Model:      "gpt-test",
		Call:       testCall(),
		OutputText: "visible output",
		Events: []lipapi.Event{
			{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider}},
		},
	})

	if len(result.Events) != 2 {
		t.Fatalf("len(result.Events) = %d, want 2", len(result.Events))
	}
	if got, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneProviderBillable); !ok || got.InputTokens != 11 || got.OutputTokens != 7 {
		t.Fatalf("provider_billable usage = %+v, %v; want preserved provider usage", got, ok)
	}
	if got, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneClientVisible); !ok || got.InputTokens != 10 || got.OutputTokens != 4 {
		t.Fatalf("client_visible usage = %+v, %v; want reconstructed 10/4", got, ok)
	}
	if result.Reconciled.BillablePlane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("billable plane = %q, want provider_billable", result.Reconciled.BillablePlane)
	}
}

func TestReconstructor_missingProviderUsageReconstructsClientVisibleInputAndOutput(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 5, TotalTokens: 5, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyBillClientVisible})

	result := reconstructForTest(t, reconstructor, Input{
		Backend:    "local",
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "abc",
	})

	if len(result.Events) != 1 {
		t.Fatalf("len(result.Events) = %d, want 1", len(result.Events))
	}
	usage, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneClientVisible)
	if !ok {
		t.Fatal("missing client_visible usage")
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 3 || usage.TotalTokens != 8 {
		t.Fatalf("client_visible usage = %+v, want input=5 output=3 total=8", usage)
	}
	if _, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneProviderBillable); ok {
		t.Fatal("unexpected provider_billable usage from local reconstruction")
	}
}

func TestReconstructor_normalizesLegacyProviderUsageDelta(t *testing.T) {
	t.Parallel()

	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 5, TotalTokens: 5, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result := reconstructForTest(t, reconstructor, Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "abc",
		Events: []lipapi.Event{{
			Kind:         lipapi.EventUsageDelta,
			InputTokens:  11,
			OutputTokens: 7,
			TotalTokens:  18,
		}},
	})

	usage, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneProviderBillable)
	if !ok {
		t.Fatal("missing normalized provider_billable usage")
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 || usage.TotalTokens != 18 {
		t.Fatalf("provider usage = %+v, want legacy totals 11/7/18", usage)
	}
	if usage.Accounting.Source != lipapi.UsageSourceProviderReported || usage.Accounting.Authority != lipapi.UsageAuthorityAuthoritative {
		t.Fatalf("provider accounting = %+v, want provider_reported/authoritative", usage.Accounting)
	}
	if len(result.Reconciled.Warnings) != 0 {
		t.Fatalf("domain warnings = %+v, want none after legacy normalization", result.Reconciled.Warnings)
	}
}

func TestReconstructor_outputCountFailureWarnsUnavailableAndPreservesProviderUsage(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(8, 2, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult: app.CountResult{InputTokens: 8, TotalTokens: 8, Accounting: localAccounting()},
		outputErr:  errors.New("tokenizer unavailable"),
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result, err := reconstructor.Reconstruct(context.Background(), Input{
		Backend:    "openai",
		Model:      "gpt-test",
		Call:       testCall(),
		OutputText: "will fail",
		Events: []lipapi.Event{
			{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider}},
		},
	})
	if err == nil {
		t.Fatal("Reconstruct error = nil, want count failure")
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningOutputCountUnavailable {
		t.Fatalf("warnings = %+v, want output unavailable warning", result.Warnings)
	}
	if got, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneProviderBillable); !ok || got.InputTokens != 8 || got.OutputTokens != 2 {
		t.Fatalf("provider_billable usage = %+v, %v; want preserved provider usage", got, ok)
	}
	if got, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneClientVisible); !ok || got.InputTokens != 8 || got.OutputTokens != 0 {
		t.Fatalf("client_visible usage = %+v, %v; want partial input-only usage", got, ok)
	}
}

func TestReconstructor_inputCountFailurePreservesProviderReconciliation(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(9, 4, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{callErr: errors.New("input tokenizer failed")}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	result, err := reconstructor.Reconstruct(context.Background(), Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "ignored",
		Events:     []lipapi.Event{{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider}}},
	})
	if err == nil {
		t.Fatal("Reconstruct error = nil, want input count failure")
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningInputCountUnavailable {
		t.Fatalf("warnings = %+v, want input unavailable warning", result.Warnings)
	}
	if got, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneProviderBillable); !ok || got.InputTokens != 9 || got.OutputTokens != 4 {
		t.Fatalf("provider_billable usage = %+v, %v; want preserved provider usage", got, ok)
	}
	if result.Reconciled.BillablePlane != lipapi.UsagePlaneProviderBillable {
		t.Fatalf("billable plane = %q, want provider_billable", result.Reconciled.BillablePlane)
	}
}

func TestReconstructor_nonUsageEventsIgnoredForProviderUsageButPassedToOutputCounter(t *testing.T) {
	t.Parallel()

	events := []lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "hello"},
		{Kind: lipapi.EventWarning, WarningCode: "provider_notice", WarningMessage: "ignored for usage"},
	}
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 2, TotalTokens: 2, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 1, TotalTokens: 1, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyBillClientVisible})

	result := reconstructForTest(t, reconstructor, Input{Model: "test-model", Call: testCall(), OutputText: "hello", Events: events})

	if counter.outputInput.Text != "hello" {
		t.Fatalf("output count text = %q, want %q", counter.outputInput.Text, "hello")
	}
	if len(counter.outputInput.Events) != len(events) || counter.outputInput.Events[0].Kind != lipapi.EventTextDelta {
		t.Fatalf("output count events = %+v, want original non-usage events", counter.outputInput.Events)
	}
	if _, ok := result.Reconciled.UsageForPlane(lipapi.UsagePlaneProviderBillable); ok {
		t.Fatal("non-usage events produced provider_billable usage")
	}
}

func TestReconstructor_excludesUsageDeltaEventsFromOutputCounter(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(11, 7, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 5, TotalTokens: 5, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyProviderThenClient})

	_ = reconstructForTest(t, reconstructor, Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "abc",
		Events: []lipapi.Event{
			{Kind: lipapi.EventTextDelta, Delta: "abc"},
			{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider}},
			{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
		},
	})

	if len(counter.outputInput.Events) != 2 {
		t.Fatalf("len(output events) = %d, want 2 non-usage events", len(counter.outputInput.Events))
	}
	for _, ev := range counter.outputInput.Events {
		if ev.Kind == lipapi.EventUsageDelta {
			t.Fatalf("output counter received usage_delta event: %+v", ev)
		}
	}
}

func TestReconstructor_reconciliationDetectsTransformedUsageMismatch(t *testing.T) {
	t.Parallel()

	provider := scopedUsage(6, 10, lipapi.UsagePlaneProviderBillable, lipapi.UsageSourceProviderReported, lipapi.UsageAuthorityAuthoritative)
	counter := &fakeCounter{
		callResult:   app.CountResult{InputTokens: 6, TotalTokens: 6, Accounting: localAccounting()},
		outputResult: app.CountResult{OutputTokens: 4, TotalTokens: 4, Accounting: localAccounting()},
	}
	reconstructor := New(counter, Config{Policy: domain.PolicyRequireSamePlaneExactness})

	result := reconstructForTest(t, reconstructor, Input{
		Model:      "test-model",
		Call:       testCall(),
		OutputText: "shorter transformed text",
		Events:     []lipapi.Event{{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider}}},
	})

	if len(result.Reconciled.Conflicts) != 1 || result.Reconciled.Conflicts[0].Code != domain.ConflictTransformedUsage {
		t.Fatalf("conflicts = %+v, want transformed usage conflict", result.Reconciled.Conflicts)
	}
	if result.Reconciled.Complete {
		t.Fatal("reconciled result complete = true, want false under exactness policy")
	}
}

func TestReconstructor_contextCancellationReturnsWarningAndError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reconstructor := New(&fakeCounter{}, Config{Policy: domain.PolicyBillClientVisible})

	result, err := reconstructor.Reconstruct(ctx, Input{Model: "test-model", Call: testCall(), OutputText: "ignored"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Reconstruct error = %v, want context.Canceled", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Code != WarningContextCanceled {
		t.Fatalf("warnings = %+v, want context canceled warning", result.Warnings)
	}
}

func reconstructForTest(t *testing.T, reconstructor *Reconstructor, input Input) Result {
	t.Helper()
	result, err := reconstructor.Reconstruct(context.Background(), input)
	if err != nil {
		t.Fatalf("Reconstruct error = %v", err)
	}
	return result
}

type fakeCounter struct {
	callResult   app.CountResult
	callErr      error
	outputResult app.CountResult
	outputErr    error
	outputInput  app.CountOutputInput
}

func (f *fakeCounter) CountCall(ctx context.Context, input app.CountCallInput) (app.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	return f.callResult, f.callErr
}

func (f *fakeCounter) CountOutput(ctx context.Context, input app.CountOutputInput) (app.CountResult, error) {
	f.outputInput = input
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	return f.outputResult, f.outputErr
}

func testCall() lipapi.Call {
	return lipapi.Call{
		ID: "call-test",
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
}

func scopedUsage(input, output int, plane lipapi.UsagePlane, source lipapi.UsageSource, authority lipapi.UsageAuthority) lipapi.ScopedUsageDelta {
	return lipapi.ScopedUsageDelta{
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  input + output,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     plane,
			Source:    source,
			Authority: authority,
		},
	}
}

func localAccounting() lipapi.UsageAccountingMetadata {
	return lipapi.UsageAccountingMetadata{Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated}
}
