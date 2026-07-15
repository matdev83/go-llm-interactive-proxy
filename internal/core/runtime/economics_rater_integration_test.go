package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/accounting"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type injectedRater struct {
	nano     int64
	currency string
	err      error
	calls    atomic.Int32
	last     atomic.Value // economics.RatingRequest

	quoteMax    int64
	quoteStatus economics.OutputLimitStatus
	quoteErr    error
	quoteCalls  atomic.Int32
	lastQuote   atomic.Value // economics.OutputLimitRequest
}

func (r *injectedRater) Rate(_ context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	r.calls.Add(1)
	r.last.Store(req)
	if r.err != nil {
		return economics.RatingResult{}, r.err
	}
	cur := r.currency
	if cur == "" {
		cur = "USD"
	}
	return economics.RatingResult{
		Money:       economics.Money{NanoUnits: r.nano, Currency: cur, Present: true},
		Source:      "injected-test-rater",
		Authority:   "estimated",
		Version:     economics.VersionRef{ID: "injected-rater", Version: "rate-v9"},
		Perspective: req.Perspective,
		RaterID:     "injected-test-rater",
	}, nil
}

func (r *injectedRater) QuoteOutputLimit(_ context.Context, req economics.OutputLimitRequest) (economics.OutputLimitResult, error) {
	r.quoteCalls.Add(1)
	r.lastQuote.Store(req)
	if r.quoteErr != nil {
		return economics.OutputLimitResult{}, r.quoteErr
	}
	status := r.quoteStatus
	if status == "" {
		status = economics.OutputLimitOK
	}
	return economics.OutputLimitResult{
		Status:          status,
		MaxOutputTokens: r.quoteMax,
		Source:          "injected-test-rater",
		Authority:       "estimated",
		Version:         economics.VersionRef{ID: "injected-rater", Version: "rate-v9"},
		Perspective:     req.Perspective,
		RaterID:         "injected-test-rater",
	}, nil
}

// rateOnlyRater implements economics.Rater but not OutputLimitQuoter.
type rateOnlyRater struct {
	err error
}

func (r *rateOnlyRater) Rate(_ context.Context, req economics.RatingRequest) (economics.RatingResult, error) {
	if r.err != nil {
		return economics.RatingResult{}, r.err
	}
	return economics.RatingResult{
		Money:       economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
		Perspective: req.Perspective,
		RaterID:     "rate-only",
	}, nil
}

func catalogThatWouldEstimateLow(t *testing.T) accounting.PriceCatalog {
	t.Helper()
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
		t.Fatalf("catalog: %v", err)
	}
	return catalog
}

func reservedAdmitResult(id string) authorityapp.AdmissionResult {
	return authorityapp.AdmissionResult{
		Allowed:        true,
		Reserved:       true,
		ReservationID:  id,
		ReservedAmount: authorityInputAmount(1),
		Outcome:        authoritydomain.DecisionOutcomeAllow,
	}
}

// Injected EconomicsRater must drive attempt admission Spend, not AccountingPriceCatalog.
func TestInjectedRater_ChangesAttemptAdmissionSpend(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{nano: 42_424_242, currency: "USD"}
	auth := &recordingAuthorityService{admitResult: reservedAdmitResult("res-rated")}
	ex := &Executor{}
	ex.UsageAuthority = auth
	ex.AccountingPriceCatalog = catalogThatWouldEstimateLow(t)
	ex.EconomicsRater = rater

	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 1_000, OutputTokens: 1_000, TotalTokens: 2_000},
	}
	_, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-rate",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-rate", Seq: 1},
		lipapi.Call{ID: "req-rate"},
		authorityCandidate(),
		decision,
		false,
	)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if rater.calls.Load() < 1 {
		t.Fatal("EconomicsRater.Rate must be called during attempt admission")
	}
	got := auth.lastAdmit()
	if got.Spend.Unit != authoritydomain.AmountUnitMoneyNano || got.Spend.Value != 42_424_242 {
		catalogEst := accounting.EstimateCost(accounting.CostInput{
			Backend: "backend-1", Model: "model-1",
			Usage: accounting.TokenUsage{InputTokens: 1000, OutputTokens: 1000},
		}, ex.AccountingPriceCatalog)
		t.Fatalf("Spend=%+v want money_nano 42424242 (catalog would be ~%d)", got.Spend, catalogEst.NanoUnits)
	}
	if got.Spend.Currency != "USD" {
		t.Fatalf("Spend.Currency=%q", got.Spend.Currency)
	}
	req, _ := rater.last.Load().(economics.RatingRequest)
	if req.Perspective != metering.PerspectiveOperator {
		t.Fatalf("perspective=%s want operator", req.Perspective)
	}
}

// Injected rater must populate coordinator Exposure.Money so UA adapter admits rated spend.
func TestInjectedRater_ChangesCoordinatorAttemptExposureMoney(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{nano: 77_000_000, currency: "USD"}
	auth := &recordingAuthorityService{admitResult: reservedAdmitResult("res-coord-rated")}
	ex := &Executor{}
	ex.UsageAuthority = auth
	ex.AccountingPriceCatalog = catalogThatWouldEstimateLow(t)
	ex.EconomicsRater = rater
	_, att := BuildAuthorityCoordinators(auth, nil)
	ex.AttemptCoordinator = att

	_, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-coord-rate",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-coord-rate", Seq: 1},
		lipapi.Call{ID: "req-coord-rate"},
		authorityCandidate(),
		accountingpreflight.Decision{
			Count: accountingapp.CountResult{InputTokens: 500, OutputTokens: 500, TotalTokens: 1000},
		},
		false,
	)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if rater.calls.Load() < 1 {
		t.Fatal("Rate must be invoked before AttemptCoordinator.Admit")
	}
	got := auth.lastAdmit()
	if got.Spend.Value != 77_000_000 {
		t.Fatalf("coordinator-path Spend.Value=%d want 77000000 from Exposure.Money", got.Spend.Value)
	}
	if !got.Exposure.Money.Present || got.Exposure.Money.NanoUnits != 77_000_000 {
		t.Fatalf("Exposure.Money=%+v want present 77000000", got.Exposure.Money)
	}
}

func TestInjectedRater_FailureDoesNotFallBackToCatalog(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{err: errors.New("enterprise rater unavailable")}
	auth := &recordingAuthorityService{admitResult: reservedAdmitResult("res-should-not")}
	ex := &Executor{}
	ex.UsageAuthority = auth
	ex.AccountingPriceCatalog = catalogThatWouldEstimateLow(t)
	ex.EconomicsRater = rater

	_, err := ex.admitAttemptAuthority(
		context.Background(),
		"trace-fail",
		"a-1",
		b2bua.BLegRecord{BLegID: "b-fail", Seq: 1},
		lipapi.Call{ID: "req-fail"},
		authorityCandidate(),
		accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 10, OutputTokens: 10}},
		false,
	)
	if err == nil {
		t.Fatal("expected admission failure when injected rater errors")
	}
	if auth.admitCalls.Load() != 0 {
		t.Fatalf("Admit must not run with catalog spend after rater failure; admitCalls=%d", auth.admitCalls.Load())
	}
}

func TestInjectedRater_ChangesSettlementCostEnrichment(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{nano: 9_001, currency: "USD"}
	ex := &Executor{}
	ex.AccountingPriceCatalog = catalogThatWouldEstimateLow(t)
	ex.EconomicsRater = rater
	stream := &retryRecvStream{
		executor: ex,
		cand:     authorityCandidate(),
	}
	ev := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  1_000,
		OutputTokens: 1_000,
		TotalTokens:  2_000,
	}
	got := stream.enrichUsageCost(ev)
	if rater.calls.Load() < 1 {
		t.Fatal("EconomicsRater.Rate must be used for settlement cost enrichment")
	}
	if !got.CostPresent || got.CostNanoUnits != 9_001 {
		catalogEst := accounting.EstimateCost(accounting.CostInput{
			Backend: "backend-1", Model: "model-1",
			Usage: accounting.TokenUsage{InputTokens: 1000, OutputTokens: 1000},
		}, ex.AccountingPriceCatalog)
		t.Fatalf("enriched cost=%d present=%v want injected 9001 (catalog ~%d)", got.CostNanoUnits, got.CostPresent, catalogEst.NanoUnits)
	}
	if got.Currency != "USD" {
		t.Fatalf("currency=%q", got.Currency)
	}
}

func TestInjectedRater_SettlementFailureDoesNotUseCatalog(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{err: errors.New("rate boom")}
	ex := &Executor{}
	ex.AccountingPriceCatalog = catalogThatWouldEstimateLow(t)
	ex.EconomicsRater = rater
	stream := &retryRecvStream{executor: ex, cand: authorityCandidate()}
	ev := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}
	got := stream.enrichUsageCost(ev)
	if got.CostPresent {
		t.Fatalf("rater failure must not invent CostPresent from catalog; got cost=%d source=%q", got.CostNanoUnits, got.CostSource)
	}
	if got.CostSource != accounting.CostSourceUnavailable {
		t.Fatalf("CostSource=%q want %q", got.CostSource, accounting.CostSourceUnavailable)
	}
}

func TestInjectedRater_ClampUsesQuoteNotCatalog(t *testing.T) {
	t.Parallel()

	// Catalog is empty: catalog-based clamp would be unavailable. Injected quote
	// must still produce the enforceable output bound (requirements 6.1, 6.5, 12.1).
	rater := &injectedRater{quoteMax: 777}
	ex := &Executor{AccountingRuntime: AccountingRuntime{EconomicsRater: rater}}
	call := lipapi.Call{}
	err := ex.applyAuthorityClamp(context.Background(), &call, authorityCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 6_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000)
	if err != nil {
		t.Fatalf("applyAuthorityClamp: %v", err)
	}
	if rater.quoteCalls.Load() < 1 {
		t.Fatal("QuoteOutputLimit must be used when EconomicsRater is injected")
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 777 {
		t.Fatalf("MaxOutputTokens=%v want 777 from injected OutputLimitQuoter (empty catalog must not decide)", call.Options.MaxOutputTokens)
	}
	req, _ := rater.lastQuote.Load().(economics.OutputLimitRequest)
	if req.Perspective != metering.PerspectiveOperator {
		t.Fatalf("perspective=%s want operator", req.Perspective)
	}
	if !req.MaxMoney.Present || req.MaxMoney.NanoUnits != 6_000_000_000 || req.MaxMoney.Currency != "USD" {
		t.Fatalf("MaxMoney=%+v", req.MaxMoney)
	}
}

func TestInjectedRater_ClampIgnoresMismatchedCatalog(t *testing.T) {
	t.Parallel()

	// Catalog would convert remaining money into 1_000_000 output tokens; injected
	// quote returns a different bound and must win exclusively.
	rater := &injectedRater{quoteMax: 42}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		AccountingPriceCatalog: clampTestCatalog(t),
		EconomicsRater:         rater,
	}}
	call := lipapi.Call{}
	err := ex.applyAuthorityClamp(context.Background(), &call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 6_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000_000)
	if err != nil {
		t.Fatalf("applyAuthorityClamp: %v", err)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 42 {
		t.Fatalf("MaxOutputTokens=%v want 42 from rater (catalog would yield 1000000)", call.Options.MaxOutputTokens)
	}
}

func TestInjectedRater_ClampErrorDoesNotFallBackToCatalog(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{quoteErr: errors.New("enterprise output-limit quote failed")}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		AccountingPriceCatalog: clampTestCatalog(t),
		EconomicsRater:         rater,
	}}
	call := lipapi.Call{}
	err := ex.applyAuthorityClamp(context.Background(), &call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 6_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000_000)
	if err == nil || !lipapi.IsPolicyDenied(err) {
		t.Fatalf("error=%v want policy denial when QuoteOutputLimit fails", err)
	}
	if call.Options.MaxOutputTokens != nil {
		t.Fatalf("must not apply catalog clamp after rater error; MaxOutputTokens=%v", call.Options.MaxOutputTokens)
	}
	if rater.quoteCalls.Load() < 1 {
		t.Fatal("QuoteOutputLimit must be attempted")
	}
}

func TestInjectedRater_ClampUnsupportedDoesNotFallBackToCatalog(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{quoteStatus: economics.OutputLimitUnsupported}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		AccountingPriceCatalog: clampTestCatalog(t),
		EconomicsRater:         rater,
	}}
	call := lipapi.Call{}
	err := ex.applyAuthorityClamp(context.Background(), &call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 6_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000_000)
	if err == nil || !lipapi.IsPolicyDenied(err) {
		t.Fatalf("error=%v want explicit unavailable denial (no catalog fallback)", err)
	}
	if call.Options.MaxOutputTokens != nil {
		t.Fatalf("unsupported quote must not fall back to catalog; MaxOutputTokens=%v", call.Options.MaxOutputTokens)
	}
}

func TestInjectedRater_ClampRateOnlyDoesNotFallBackToCatalog(t *testing.T) {
	t.Parallel()

	ex := &Executor{AccountingRuntime: AccountingRuntime{
		AccountingPriceCatalog: clampTestCatalog(t),
		EconomicsRater:         &rateOnlyRater{},
	}}
	call := lipapi.Call{}
	err := ex.applyAuthorityClamp(context.Background(), &call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 6_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailClosed,
	}, 1_000_000)
	if err == nil || !lipapi.IsPolicyDenied(err) {
		t.Fatalf("error=%v want explicit denial when rater lacks OutputLimitQuoter", err)
	}
	if call.Options.MaxOutputTokens != nil {
		t.Fatalf("rate-only rater must not fall back to catalog; MaxOutputTokens=%v", call.Options.MaxOutputTokens)
	}
}

func TestInjectedRater_ClampCapacityExhaustedDoesNotFallBackToCatalog(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{quoteStatus: economics.OutputLimitCapacityExhausted}
	ex := &Executor{AccountingRuntime: AccountingRuntime{
		AccountingPriceCatalog: clampTestCatalog(t),
		EconomicsRater:         rater,
	}}
	call := lipapi.Call{}
	err := ex.applyAuthorityClamp(context.Background(), &call, clampTestCandidate(), &authorityapp.AdmissionClamp{
		EffectiveMax:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 6_000_000_000, Currency: "USD"},
		FailureBehavior: authoritydomain.FailureBehaviorFailOpen,
	}, 1_000_000)
	if err == nil || !lipapi.IsPolicyDenied(err) {
		t.Fatalf("error=%v want deterministic capacity denial even under fail-open", err)
	}
	if call.Options.MaxOutputTokens != nil {
		t.Fatalf("capacity exhausted must not fall back to catalog; MaxOutputTokens=%v", call.Options.MaxOutputTokens)
	}
}

func TestInjectedRater_RatesCustomerRequestAdmission(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{nano: 12_000, currency: "USD"}
	ex := &Executor{}
	ex.EconomicsRater = rater
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID:       "usage-authority-request",
			Class:    authoritycoord.PriorityQuotaBudgetRate,
			Provider: &settleRecordingRequestProvider{id: "usage-authority-request"},
			Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-cust-rate", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	_, err = ex.admitRequestAuthorityOnce(ctx, "req-cust-rate", "a-1", "trace-cust", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admitRequest: %v", err)
	}
	if rater.calls.Load() < 1 {
		t.Fatal("customer logical-request admit must Rate when EconomicsRater is attached")
	}
	req, _ := rater.last.Load().(economics.RatingRequest)
	if req.Perspective != metering.PerspectiveCustomer {
		t.Fatalf("perspective=%s want customer", req.Perspective)
	}
}
