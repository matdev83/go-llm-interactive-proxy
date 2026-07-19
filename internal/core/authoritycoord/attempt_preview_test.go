package authoritycoord_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// fakePreviewAttemptProvider optionally implements AttemptClampPreviewer.
type fakePreviewAttemptProvider struct {
	fakeAttemptProvider
	preview      func(context.Context, authority.AttemptAdmission) (authority.Decision, error)
	previewCalls atomic.Int32
	admitCalls   atomic.Int32
}

func (f *fakePreviewAttemptProvider) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	f.admitCalls.Add(1)
	return f.fakeAttemptProvider.AdmitAttempt(ctx, in)
}

func (f *fakePreviewAttemptProvider) PreviewAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	f.previewCalls.Add(1)
	if f.preview != nil {
		return f.preview(ctx, in)
	}
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func TestAttemptCoordinator_Preview_CallsPreviewAttemptWhenImplemented(t *testing.T) {
	t.Parallel()
	previewer := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "spend"}}
	previewer.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind: authority.DecisionAllow,
			Clamps: []authority.Clamp{{
				Kind:  authority.ClampMaxOutputTokens,
				Value: 100,
			}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: previewer},
		},
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-preview"))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if previewer.previewCalls.Load() != 1 {
		t.Fatalf("PreviewAttempt calls=%d want 1", previewer.previewCalls.Load())
	}
	if previewer.admitCalls.Load() != 0 {
		t.Fatalf("AdmitAttempt must not run during Preview; calls=%d", previewer.admitCalls.Load())
	}
	if len(d.Clamps) != 1 || d.Clamps[0].Value != 100 {
		t.Fatalf("clamps=%+v want max_output_tokens=100", d.Clamps)
	}
	if len(d.Stack.Entries()) != 0 {
		t.Fatalf("preview must not record holds; stack=%+v", d.Stack.Entries())
	}
}

func TestAttemptCoordinator_Preview_SkipsNonPreviewers(t *testing.T) {
	t.Parallel()
	nonPreview := &fakeAttemptProvider{id: "quota"}
	previewer := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "spend"}}
	previewer.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind: authority.DecisionAllow,
			Clamps: []authority.Clamp{{
				Kind:  authority.ClampMaxOutputTokens,
				Value: 50,
			}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: previewer},
			{ID: "quota", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: nonPreview},
		},
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-skip"))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if previewer.previewCalls.Load() != 1 {
		t.Fatalf("previewer calls=%d want 1", previewer.previewCalls.Load())
	}
	if nonPreview.released.Load() != 0 {
		t.Fatalf("non-previewer must be skipped (noop); released=%d", nonPreview.released.Load())
	}
	if len(d.Clamps) != 1 {
		t.Fatalf("clamps=%+v", d.Clamps)
	}
}

func TestAttemptCoordinator_Preview_MergesNonWideningClamps(t *testing.T) {
	t.Parallel()
	wide := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "wide"}}
	wide.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind: authority.DecisionAllow,
			Clamps: []authority.Clamp{
				{Kind: authority.ClampMaxOutputTokens, Value: 200},
				{Kind: authority.ClampMaxSpend, Money: economics.Money{NanoUnits: 1000, Currency: "USD", Present: true}},
			},
		}, nil
	}
	tight := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "tight"}}
	tight.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind: authority.DecisionAllow,
			Clamps: []authority.Clamp{
				{Kind: authority.ClampMaxOutputTokens, Value: 80},
				{Kind: authority.ClampMaxSpend, Money: economics.Money{NanoUnits: 400, Currency: "USD", Present: true}},
			},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "wide", Class: authoritycoord.AttemptPriorityHardSpend, Provider: wide},
			{ID: "tight", Class: authoritycoord.AttemptPriorityQuotaRate, Provider: tight},
		},
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-merge"))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(d.Clamps) != 2 {
		t.Fatalf("clamps=%+v want 2", d.Clamps)
	}
	var tokens, spend *authority.Clamp
	for i := range d.Clamps {
		switch d.Clamps[i].Kind {
		case authority.ClampMaxOutputTokens:
			tokens = &d.Clamps[i]
		case authority.ClampMaxSpend:
			spend = &d.Clamps[i]
		}
	}
	if tokens == nil || tokens.Value != 80 {
		t.Fatalf("max_output_tokens=%+v want 80", tokens)
	}
	if spend == nil || !spend.Money.Present || spend.Money.NanoUnits != 400 {
		t.Fatalf("max_spend=%+v want 400", spend)
	}
}

func TestAttemptCoordinator_Preview_NeverRecordsHolds(t *testing.T) {
	t.Parallel()
	previewer := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "spend"}}
	previewer.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:               authority.DecisionAllow,
			CompensationHandle: "should-be-stripped",
			Reservations: []authority.Reservation{{
				Handle: "leaked-hold",
				Kind:   authority.ReservationSpend,
			}},
			Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 10}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: previewer},
		},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-nohold"))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(d.Stack.Entries()) != 0 {
		t.Fatalf("stack must be empty, got %+v", d.Stack.Entries())
	}
	if previewer.released.Load() != 0 {
		t.Fatalf("preview must not release; released=%d", previewer.released.Load())
	}
	for _, pd := range d.ProviderDecisions {
		if len(pd.Reservations) != 0 || pd.CompensationHandle != "" {
			t.Fatalf("provider decision must strip holds: %+v", pd)
		}
	}
}

func TestAttemptCoordinator_Preview_RequiredDeny(t *testing.T) {
	t.Parallel()
	deny := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "spend"}}
	deny.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionDeny}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: deny, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-deny"))
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("want denied, got err=%v d=%+v", err, d)
	}
	if d.Kind != authority.DecisionDeny || d.DeniedBy != "spend" {
		t.Fatalf("decision=%+v", d)
	}
	if len(d.Stack.Entries()) != 0 {
		t.Fatalf("deny preview must not leave holds")
	}
}

func TestAttemptCoordinator_Preview_AdvisoryDenyStillDenies(t *testing.T) {
	t.Parallel()
	// Match Admit: DecisionDeny is never fail-open, even for advisory strength.
	advisory := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "advisory"}}
	advisory.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{Kind: authority.DecisionDeny}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "advisory", Class: authoritycoord.AttemptPriorityAdvisory, Provider: advisory, Strength: authority.StrengthAdvisory},
		},
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-adv-deny"))
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("want denied, got err=%v d=%+v", err, d)
	}
}

func TestAttemptCoordinator_Preview_AdvisoryUnavailableContinues(t *testing.T) {
	t.Parallel()
	advisory := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "advisory"}}
	advisory.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{}, errors.New("advisory preview fault")
	}
	ok := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "spend"}}
	ok.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{
			Kind:   authority.DecisionAllow,
			Clamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 12}},
		}, nil
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: ok, Strength: authority.StrengthRequired},
			{ID: "advisory", Class: authoritycoord.AttemptPriorityAdvisory, Provider: advisory, Strength: authority.StrengthAdvisory},
		},
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-adv"))
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if d.Kind != authority.DecisionAllow {
		t.Fatalf("kind=%s want allow (advisory unavailable)", d.Kind)
	}
	if d.Readiness != authority.ReadinessDegraded {
		t.Fatalf("readiness=%s want degraded", d.Readiness)
	}
	if len(d.Clamps) != 1 || d.Clamps[0].Value != 12 {
		t.Fatalf("clamps=%+v", d.Clamps)
	}
}

func TestAttemptCoordinator_Preview_RequiredUnavailable(t *testing.T) {
	t.Parallel()
	boom := &fakePreviewAttemptProvider{fakeAttemptProvider: fakeAttemptProvider{id: "spend"}}
	boom.preview = func(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
		return authority.Decision{}, errors.New("preview unavailable")
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{
			{ID: "spend", Class: authoritycoord.AttemptPriorityHardSpend, Provider: boom, Strength: authority.StrengthRequired},
		},
	}
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-unavail"))
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	var unavail *authoritycoord.ErrUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("want ErrUnavailable, got %T %v", err, err)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
}

func TestAttemptCoordinator_Preview_NilCoordinator(t *testing.T) {
	t.Parallel()
	var coord *authoritycoord.AttemptCoordinator
	d, err := coord.Preview(context.Background(), validAttemptAdmission("b-nil"))
	if err != nil {
		t.Fatalf("nil Preview: %v", err)
	}
	if d.Kind != authority.DecisionAllow || d.Readiness != authority.ReadinessDisabled {
		t.Fatalf("decision=%+v", d)
	}
}
