package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestExecutorAuthorityDisabledAllowsOpenWithoutAdmission(t *testing.T) {
	t.Parallel()

	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	out, err := openAuthorityCandidate(t, ex, aLegID)
	if err != nil {
		t.Fatalf("openPlannedCandidate: %v", err)
	}
	if !out.opened {
		t.Fatal("expected backend to open when authority is disabled")
	}
	if backend.openCalls.Load() != 1 {
		t.Fatalf("backend open calls = %d, want 1", backend.openCalls.Load())
	}
}

func TestExecutorAuthorityDeniedBlocksOpenBeforeBackend(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed: false,
			Outcome: authoritydomain.DecisionOutcomeDeny,
			PolicyRecord: policydecision.Record{
				ReasonCode: "quota_exceeded",
			},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	_, err := openAuthorityCandidate(t, ex, aLegID)
	if err == nil {
		t.Fatal("expected authority denial error")
	}
	if !lipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied, got %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatalf("backend open calls = %d, want 0", backend.openCalls.Load())
	}
	if auth.admitCalls.Load() != 1 {
		t.Fatalf("admit calls = %d, want 1", auth.admitCalls.Load())
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0", auth.releaseCalls.Load())
	}
}

func TestExecutorAuthorityReservationConflictIsPolicyDenied(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitErr: authorityapp.ErrReservationConflict,
		status:   controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	_, err := openAuthorityCandidate(t, ex, aLegID)
	if err == nil {
		t.Fatal("expected authority denial error")
	}
	if !lipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied, got %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatalf("backend open calls = %d, want 0", backend.openCalls.Load())
	}
}

func TestExecutorAuthorityAdmitPopulatesRequestCount(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-1",
			ReservedAmount: authorityInputAmount(12),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	out, err := openAuthorityCandidate(t, ex, aLegID)
	if err != nil {
		t.Fatalf("openAuthorityCandidate: %v", err)
	}
	if auth.admitCalls.Load() != 2 {
		t.Fatalf("admit calls = %d, want 2", auth.admitCalls.Load())
	}
	calls := auth.admitInputs()
	if len(calls) != 2 {
		t.Fatalf("admit inputs = %d, want 2", len(calls))
	}
	if !calls[0].EstimateOnly {
		t.Fatal("precheck admission must be estimate-only")
	}
	in := calls[1]
	if in.EstimateOnly {
		t.Fatal("real admission must not be estimate-only")
	}
	if in.Correlation.BLegID == "" || in.ReservationKey.BLegID == "" {
		t.Fatal("real admission must carry the allocated B-leg")
	}
	if in.Correlation.BLegID != out.bleg.BLegID {
		t.Fatalf("real admission BLegID = %q, want %q", in.Correlation.BLegID, out.bleg.BLegID)
	}
	if in.Correlation.BLegID != in.ReservationKey.BLegID {
		t.Fatalf("real admission BLegID = %q, want reservation key BLegID %q", in.Correlation.BLegID, in.ReservationKey.BLegID)
	}
	if in.RequestCount.Unit != authoritydomain.AmountUnitRequests || in.RequestCount.Value != 1 {
		t.Fatalf("admit RequestCount = %v, want 1 requests", in.RequestCount)
	}
	if in.Request.Unit != authoritydomain.AmountUnitInputTokens {
		t.Fatalf("admit Request unit = %q, want input_tokens", in.Request.Unit)
	}
}

func TestExecutorAuthorityAdmissionDenialStopsBudgetAndBackend(t *testing.T) {
	t.Parallel()

	t.Run("request-rate", func(t *testing.T) {
		t.Parallel()
		auth := newRecordedAuthorityService(t, authoritydomain.Rule{
			ID:   "tenant.rate",
			Kind: authoritydomain.RuleKindRate,
			Mode: authoritydomain.RuleModeStrict,
			Unit: authoritydomain.AmountUnitInputTokens,
			Limit: authoritydomain.Amount{
				Unit:  authoritydomain.AmountUnitInputTokens,
				Value: 10,
			},
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
				Model:   authoritydomain.DimensionMatcher{Value: scope.Known("model-1")},
			},
		})
		ex, store, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
		ex.Preflight = accountingpreflight.NewChecker(authorityAdmissionCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			return accountingapp.CountResult{InputTokens: 11, OutputTokens: 11, TotalTokens: 22}, nil
		}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})

		budget := &attemptBudget{max: 0}
		_, err := ex.openPlannedCandidate(authorityOpenParams(t, aLegID, budget), authorityCandidate(), nil, "", false)
		if err == nil {
			t.Fatal("expected authority denial error")
		}
		if !lipapi.IsPolicyDenied(err) {
			t.Fatalf("expected policy denied error, got %v", err)
		}
		if backend.openCalls.Load() != 0 {
			t.Fatalf("backend open calls = %d, want 0", backend.openCalls.Load())
		}
		if budget.usedNow() != 0 {
			t.Fatalf("budget used = %d, want 0", budget.usedNow())
		}
		if auth.admitCalls.Load() != 1 {
			t.Fatalf("admit calls = %d, want 1", auth.admitCalls.Load())
		}
		if got := auth.lastAdmit(); !got.EstimateOnly {
			t.Fatal("denial precheck must be estimate-only")
		}
		leg, err := store.FetchALeg(context.Background(), aLegID)
		if err != nil {
			t.Fatalf("fetch a-leg: %v", err)
		}
		if leg.WeightedFirstConsumed {
			t.Fatal("weighted first consumed must remain untouched on denial")
		}
		next, err := store.NextBLeg(context.Background(), aLegID)
		if err != nil {
			t.Fatalf("next b-leg after denial: %v", err)
		}
		if next.Seq != 1 {
			t.Fatalf("next b-leg seq = %d, want 1", next.Seq)
		}
	})

	t.Run("output-tokens", func(t *testing.T) {
		t.Parallel()
		auth := newRecordedAuthorityService(t, authoritydomain.Rule{
			ID:   "tenant.output-quota",
			Kind: authoritydomain.RuleKindQuota,
			Mode: authoritydomain.RuleModeStrict,
			Unit: authoritydomain.AmountUnitOutputTokens,
			Limit: authoritydomain.Amount{
				Unit:  authoritydomain.AmountUnitOutputTokens,
				Value: 10,
			},
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
				Model:   authoritydomain.DimensionMatcher{Value: scope.Known("model-1")},
			},
		})
		ex, store, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
		ex.Preflight = accountingpreflight.NewChecker(authorityAdmissionCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			return accountingapp.CountResult{InputTokens: 3, OutputTokens: 11, TotalTokens: 14}, nil
		}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})

		budget := &attemptBudget{max: 0}
		_, err := ex.openPlannedCandidate(authorityOpenParams(t, aLegID, budget), authorityCandidate(), nil, "", false)
		if err == nil {
			t.Fatal("expected authority denial error")
		}
		if !lipapi.IsPolicyDenied(err) {
			t.Fatalf("expected policy denied error, got %v", err)
		}
		if backend.openCalls.Load() != 0 {
			t.Fatalf("backend open calls = %d, want 0", backend.openCalls.Load())
		}
		if auth.admitCalls.Load() != 1 {
			t.Fatalf("admit calls = %d, want 1", auth.admitCalls.Load())
		}
		got := auth.lastAdmit()
		if got.PreflightUsage.OutputTokens != 11 {
			t.Fatalf("preflight output tokens = %d, want 11", got.PreflightUsage.OutputTokens)
		}
		leg, err := store.FetchALeg(context.Background(), aLegID)
		if err != nil {
			t.Fatalf("fetch a-leg: %v", err)
		}
		if leg.WeightedFirstConsumed {
			t.Fatal("weighted first consumed must remain untouched on denial")
		}
	})

	t.Run("budget", func(t *testing.T) {
		t.Parallel()
		auth := newRecordedAuthorityService(t, authoritydomain.Rule{
			ID:   "tenant.budget",
			Kind: authoritydomain.RuleKindBudget,
			Mode: authoritydomain.RuleModeStrict,
			Unit: authoritydomain.AmountUnitMoneyNano,
			Limit: authoritydomain.Amount{
				Unit:     authoritydomain.AmountUnitMoneyNano,
				Value:    1_000,
				Currency: "USD",
			},
			Currency: "USD",
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
				Model:   authoritydomain.DimensionMatcher{Value: scope.Known("model-1")},
			},
		})
		catalog, err := accounting.NewPriceCatalog(accounting.PriceCatalogConfig{
			Currency: "USD",
			Models: []accounting.ModelPriceConfig{{
				Backend:     "backend-1",
				Model:       "model-1",
				InputPer1M:  "1",
				OutputPer1M: "1",
			}},
		})
		if err != nil {
			t.Fatalf("new price catalog: %v", err)
		}
		ex, store, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
		ex.AccountingPriceCatalog = catalog
		ex.Preflight = accountingpreflight.NewChecker(authorityAdmissionCountFunc(func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
			return accountingapp.CountResult{InputTokens: 11, OutputTokens: 11, TotalTokens: 22}, nil
		}), accountingpreflight.Config{Enabled: true, Mode: accountingpreflight.ModeAdvisory})

		budget := &attemptBudget{max: 0}
		_, err = ex.openPlannedCandidate(authorityOpenParams(t, aLegID, budget), authorityCandidate(), nil, "", false)
		if err == nil {
			t.Fatal("expected authority denial error")
		}
		if !lipapi.IsPolicyDenied(err) {
			t.Fatalf("expected policy denied error, got %v", err)
		}
		if backend.openCalls.Load() != 0 {
			t.Fatalf("backend open calls = %d, want 0", backend.openCalls.Load())
		}
		if budget.usedNow() != 0 {
			t.Fatalf("budget used = %d, want 0", budget.usedNow())
		}
		if auth.admitCalls.Load() != 1 {
			t.Fatalf("admit calls = %d, want 1", auth.admitCalls.Load())
		}
		leg, err := store.FetchALeg(context.Background(), aLegID)
		if err != nil {
			t.Fatalf("fetch a-leg: %v", err)
		}
		if leg.WeightedFirstConsumed {
			t.Fatal("weighted first consumed must remain untouched on denial")
		}
		next, err := store.NextBLeg(context.Background(), aLegID)
		if err != nil {
			t.Fatalf("next b-leg after denial: %v", err)
		}
		if next.Seq != 1 {
			t.Fatalf("next b-leg seq = %d, want 1", next.Seq)
		}
	})
}

// TestExecutorAuthorityRealAdmitFailureReleasesBudget pins the fix for the leak
// where the estimate-only authority precheck passes, budget.tryAcquire and
// NextBLeg run, and then the real (non-estimate) admitAttemptAuthority fails
// (e.g. a strict store returning ErrReservationConflict when the live window is
// full). The routing attempt slot acquired between the two admits must be
// released so a backend that never opens does not permanently consume a budget
// slot.
//
// The b2bua Store exposes no B-leg sequence rollback API, so the seq allocated by
// NextBLeg before the real admit is NOT restored; this test asserts that honestly
// (the next B-leg seq is 2, not 1) to document the limitation in code.
func TestExecutorAuthorityRealAdmitFailureReleasesBudget(t *testing.T) {
	t.Parallel()

	auth := &estimateThenFailAuthority{
		estimateResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-precheck",
			ReservedAmount: authorityInputAmount(12),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		realErr: authorityapp.ErrReservationConflict,
	}
	ex, store, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	budget := &attemptBudget{max: 1}
	_, err := ex.openPlannedCandidate(authorityOpenParams(t, aLegID, budget), authorityCandidate(), nil, "", false)
	if err == nil {
		t.Fatal("expected authority denial error from real admit")
	}
	if !lipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied, got %v", err)
	}
	if backend.openCalls.Load() != 0 {
		t.Fatalf("backend open calls = %d, want 0 (backend must never open)", backend.openCalls.Load())
	}
	if got, want := auth.admitCalls.Load(), int64(2); got != want {
		t.Fatalf("admit calls = %d, want %d (precheck + real)", got, want)
	}
	if budget.usedNow() != 0 {
		t.Fatalf("budget used = %d, want 0 (slot must be released on real admit failure)", budget.usedNow())
	}
	// A second acquire must succeed because the leaked slot was refunded.
	if !budget.tryAcquire() {
		t.Fatal("budget tryAcquire after failure must succeed when slot was released")
	}
	if budget.usedNow() != 1 {
		t.Fatalf("budget used after re-acquire = %d, want 1", budget.usedNow())
	}

	// B-leg sequence is NOT restorable: the b2bua Store has no rollback API, so
	// the seq consumed before the real admit stays consumed (next seq is 2).
	// Orphaned seqToBLeg entries are functionally invisible (only RecordAttempt
	// reads them, never for a rolled-back b-leg; LoadAttempts needs no contiguous
	// seqs) and are reclaimed on A-leg eviction; a rollback API would break the
	// stable continuity.Store contract (contract test pins the method set) and
	// nextSeq-- is ABA-unsafe under concurrent allocators.
	next, err := store.NextBLeg(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("next b-leg after failure: %v", err)
	}
	if next.Seq != 2 {
		t.Fatalf("next b-leg seq = %d, want 2 (B-leg sequence is not restorable; no rollback API)", next.Seq)
	}
}

// TestExecutorAuthorityRealAdmitFailureHoldsNoReservation pins the L9 invariant:
// reserve-then-deny is impossible under the current authority-app semantics. A
// reservation is applied only when Admit returns Allowed with no error
// (admission.go:70); on every error path admitAttemptAuthority returns an EMPTY
// attemptAuthorityState (Reserved=false), so an authorityLifecycle owner built
// over it would be inactive and its Release a no-op, so nothing can leak. This drives the real (non-estimate) admit failure
// path directly — a stub authority service whose Admit returns
// ErrReservationConflict (deny) — and asserts the returned authority state is
// empty. It locks the invariant so a future authority-app change cannot silently
// reintroduce a leak on the real-admit failure path in openPlannedCandidate.
func TestExecutorAuthorityRealAdmitFailureHoldsNoReservation(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitErr: authorityapp.ErrReservationConflict,
		status:   controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	state, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-pin",
		aLegID,
		b2bua.BLegRecord{BLegID: "bleg-pin", Seq: 1},
		lipapi.Call{ID: "request-pin", Route: lipapi.RouteIntent{Selector: "backend-1:model-1"}},
		authorityCandidate(),
		accountingpreflight.Decision{},
		false, // real (non-estimate) admit — the only path that allocates reservations
	)
	if err == nil {
		t.Fatal("expected authority denial error from real admit")
	}
	if !lipapi.IsPolicyDenied(err) {
		t.Fatalf("err must be policy denied (ErrReservationConflict maps to Deny), got %v", err)
	}
	// L9 invariant: on an admit error the returned state is the zero value, so
	// an authorityLifecycle owner built over it is inactive (its Release no-ops
	// when TraceID=="" || !Reserved) and has nothing to release. Nothing was
	// reserved, so nothing can leak.
	if state.admissionResult.Reserved {
		t.Fatalf("admissionResult.Reserved = true, want false (reserve-then-deny must be impossible)")
	}
	if state.admissionInput.Correlation.TraceID != "" {
		t.Fatalf("admissionInput.Correlation.TraceID = %q, want empty (zero-value state on admit error)", state.admissionInput.Correlation.TraceID)
	}
	if got, want := auth.admitCalls.Load(), int64(1); got != want {
		t.Fatalf("admit calls = %d, want %d", got, want)
	}
}
