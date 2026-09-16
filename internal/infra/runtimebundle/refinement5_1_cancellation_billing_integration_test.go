package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/capabilities"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type refinement51BlockingStream struct {
	started chan struct{}
	once    sync.Once
}

func (s *refinement51BlockingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	s.once.Do(func() {
		close(s.started)
	})
	<-ctx.Done()
	return lipapi.Event{}, ctx.Err()
}

func (s *refinement51BlockingStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (s *refinement51BlockingStream) Close() error {
	return nil
}

var _ lipapi.ManagedEventStream = (*refinement51BlockingStream)(nil)

// refinement51ProviderCostStore exposes a test-only committed-leg-key notification
// at the durable provider-cost worker seam. The notification fires only after the
// production DeferProviderCostWork transaction commits, so callers observe the
// worker's actual retry state rather than racing a database poll. Teardown can
// cancel the notification context to release a sender when no receiver remains.
type refinement51ProviderCostStore struct {
	*billingstore.DurableStore
	providerCostWorkDone      chan<- string
	providerCostNotifyContext context.Context
}

var _ billing.AuthoritativeBilling = (*refinement51ProviderCostStore)(nil)
var _ billing.ProviderCostWorkFailureStore = (*refinement51ProviderCostStore)(nil)

func (s *refinement51ProviderCostStore) DeferProviderCostWork(ctx context.Context, work billing.ProviderCostWork, reason string) error {
	err := s.DurableStore.DeferProviderCostWork(ctx, work, reason)
	if err != nil || s.providerCostWorkDone == nil {
		return err
	}
	notifyCtx := s.providerCostNotifyContext
	if notifyCtx == nil {
		notifyCtx = ctx
	}
	select {
	case s.providerCostWorkDone <- work.Leg.Key:
	case <-notifyCtx.Done():
	}
	return err
}

func injectRefinement51BlockingBackend(t *testing.T, executor *coreruntime.Executor, started chan struct{}) *atomic.Int32 {
	t.Helper()
	var opens atomic.Int32
	be := execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opens.Add(1)
			return &refinement51BlockingStream{started: started}, nil
		},
	}
	if executor.Backends == nil {
		executor.Backends = map[string]execbackend.Backend{}
	}
	executor.Backends[billingHostLoopBackendID] = be
	capFn := func(ctx context.Context, cand routing.AttemptCandidate, call lipapi.Call) lipapi.BackendCaps {
		return execbackend.EffectiveCaps(ctx, be, call, cand)
	}
	switch capMap := executor.CapsResolver.(type) {
	case capabilities.MapResolver:
		capMap[billingHostLoopBackendID] = capFn
	case nil:
		executor.CapsResolver = capabilities.MapResolver{billingHostLoopBackendID: capFn}
	default:
		t.Fatalf("CapsResolver type %T cannot accept injected backend caps", executor.CapsResolver)
	}
	return &opens
}

// TestRefinement51InFlightCancellationAfterBLegStartPostsFixedFeeWithoutPhantomEconomics
// executes an authentic in-flight runtime execution wired to production durable billing seams
// (real runtime Executor, B2BUA allocation, durable exposure admission, durable terminal usage sink,
// controllable blocking stream, and host post-turn settlement loop). Cancellation after B-leg start
// preserves local call closure, posts the configured fixed call fee once, and creates no phantom
// token or provider economics.
func TestRefinement51InFlightCancellationAfterBLegStartPostsFixedFeeWithoutPhantomEconomics(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store := openBillingHostLoopStore(t)
	catalog, _, _, operator := seedBillingHostLoopCatalog(t)
	if err := catalog.SetOperatorRateBinding(billingHostLoopBackendID, billingHostLoopModelID, operator.Ref); err != nil {
		t.Fatalf("SetOperatorRateBinding: %v", err)
	}

	accountID := fmt.Sprintf("ref51-cancel-%d", billingHostLoopSeq.Add(1))
	provisionBillingHostLoopAccount(t, store, accountID)
	providerCostNotifyCtx, cancelProviderCostNotify := context.WithCancel(ctx)
	defer cancelProviderCostNotify()
	providerCostWorkDone := make(chan string)
	providerStore := &refinement51ProviderCostStore{
		DurableStore:              store,
		providerCostWorkDone:      providerCostWorkDone,
		providerCostNotifyContext: providerCostNotifyCtx,
	}

	ceiling := billing.Money{Nano: billingHostLoopHoldNano, Currency: "USD"}
	prod, err := runtimebundle.ComposeBilling(runtimebundle.ComposeBillingInput{
		Store:             providerStore,
		TerminalUsageSink: store,
		Catalog:           catalog,
		Identity:          nil,
		Currency:          "USD",
		ModelMaxOutput: func(context.Context, string, string) (int64, bool, error) {
			return 128000, true, nil
		},
		Strict:              true,
		ConservativeCeiling: &ceiling,
		PostTurnBatchSize:   1,
	})
	if err != nil {
		t.Fatalf("ComposeBilling: %v", err)
	}

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      writeBillingHostLoopConfig(t),
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		Production:      prod,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	executor := hostActiveExecutor(t, host)
	if executor.BillingExposureAdmission == nil {
		t.Fatal("exposure generation must expose operational admission")
	}

	started := make(chan struct{})
	opens := injectRefinement51BlockingBackend(t, executor, started)

	execCtx := scope.WithScope(ctx, scope.PrincipalScopeView{
		PrincipalID: scope.Known(accountID),
	})

	acctBefore, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}

	callCtx, cancelCall := context.WithCancel(execCtx)
	defer cancelCall()

	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: billingHostLoopBackendID + ":" + billingHostLoopModelID},
		Session: lipapi.SessionRef{
			ClientSessionID: "ref51-cancel-sess",
		},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("cancel during stream")},
		}},
	}

	stream, err := executor.Execute(callCtx, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	collectDone := make(chan struct{})
	var collectErr error
	go func() {
		_, collectErr = lipapi.Collect(callCtx, stream)
		close(collectDone)
	}()
	t.Cleanup(func() {
		refinement51JoinCollect(t, collectDone, cancelCall)
	})

	startupCtx, cancelStartup := context.WithTimeout(callCtx, 5*time.Second)
	defer cancelStartup()
	select {
	case <-started:
	case <-collectDone:
		t.Fatalf("Collect returned before backend stream started: %v", collectErr)
	case <-startupCtx.Done():
		t.Fatalf("timed out waiting for backend stream to start: %v", startupCtx.Err())
	}

	// Cancel request context mid-flight after execution and B-leg start
	cancelCall()

	joinCtx, cancelJoin := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelJoin()
	select {
	case <-collectDone:
	case <-joinCtx.Done():
		t.Fatalf("timed out joining Collect after cancel: %v", joinCtx.Err())
	}
	returnedError := collectErr

	// Cancellation remains visible at the stream boundary.
	if returnedError == nil {
		t.Fatal("expected non-nil error on canceled stream")
	}
	if !errors.Is(returnedError, context.Canceled) {
		t.Fatalf("Collect returned %v, want errors.Is context.Canceled", returnedError)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opened %d times, want 1", opens.Load())
	}

	// Await durable closure and exposure closure via post-turn settlement loop
	closure, exposure, complete := waitBillingHostLoopCall(t, store, accountID)

	// 1. Call outcome == billing.TurnOutcomeCanceled
	if closure.Outcome != billing.TurnOutcomeCanceled {
		t.Fatalf("call closure outcome = %q, want %q", closure.Outcome, billing.TurnOutcomeCanceled)
	}

	// 2. Exactly one durable canceled leg
	legs, err := store.ListCallLegUsage(ctx, closure.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 1 {
		t.Fatalf("durable call legs = %d, want 1", len(legs))
	}
	leg := legs[0]
	if leg.Outcome != billing.LegOutcomeCanceled {
		t.Fatalf("call leg outcome = %q, want %q", leg.Outcome, billing.LegOutcomeCanceled)
	}

	// 3. Call/leg A-leg and BillingCallID correlation
	if leg.CallID != closure.CallID {
		t.Fatalf("leg CallID %q != closure CallID %q", leg.CallID, closure.CallID)
	}
	if strings.TrimSpace(closure.ALegID) == "" {
		t.Fatal("closure ALegID is empty")
	}
	if leg.ALegID != closure.ALegID {
		t.Fatalf("leg ALegID %q != closure ALegID %q", leg.ALegID, closure.ALegID)
	}
	if len(closure.ExpectedBLegIDs) != 1 || closure.ExpectedBLegIDs[0] != leg.BLegID {
		t.Fatalf("closure ExpectedBLegIDs = %v, want [%s]", closure.ExpectedBLegIDs, leg.BLegID)
	}

	// 4. A real B-leg attempt exists with AttemptCancelled and exact reason
	attempts, err := executor.Store.LoadAttempts(ctx, closure.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("persisted attempts = %d, want 1", len(attempts))
	}
	attempt := attempts[0]
	if attempt.Outcome != lipapi.AttemptCancelled {
		t.Fatalf("attempt outcome = %q, want %q", attempt.Outcome, lipapi.AttemptCancelled)
	}
	if attempt.Reason != context.Canceled.Error() {
		t.Fatalf("attempt reason = %q, want %q", attempt.Reason, context.Canceled.Error())
	}
	if leg.AttemptSeq != attempt.Seq {
		t.Fatalf("leg AttemptSeq = %d, want attempt.Seq = %d", leg.AttemptSeq, attempt.Seq)
	}
	if leg.BLegID != attempt.BLegID {
		t.Fatalf("leg BLegID %q != attempt.BLegID %q", leg.BLegID, attempt.BLegID)
	}

	// 5. Cancellation has no phantom token/provider economics and retains only local boundary evidence.
	assertRefinement51NoPhantomTokenEconomics(t, leg)
	if len(leg.Observations) != 3 {
		t.Fatalf("expected 3 local boundary observations on canceled leg, got %d", len(leg.Observations))
	}
	expectedCallID := closure.CallID.String()
	expectedRequestID := strings.TrimSpace(call.ID)
	if expectedRequestID == "" {
		t.Fatal("runtime did not stamp a request ID on the call")
	}
	expectedStoreID := store.StoreID()
	expectedSubject := metering.SubjectRef{
		Kind:          metering.SubjectBLeg,
		StoreID:       expectedStoreID,
		RequestID:     expectedRequestID,
		CallID:        expectedCallID,
		BillingCallID: expectedCallID,
		ALegID:        closure.ALegID,
		BLegID:        leg.BLegID,
		AttemptID:     attempt.BLegID,
		AttemptSeq:    uint64(attempt.Seq),
	}
	expectedCorrelation := metering.CorrelationV2{
		StoreID:       expectedStoreID,
		RequestID:     expectedRequestID,
		CallID:        expectedCallID,
		BillingCallID: expectedCallID,
		ALegID:        closure.ALegID,
		BLegID:        leg.BLegID,
		AttemptID:     attempt.BLegID,
		AttemptSeq:    uint64(attempt.Seq),
	}
	expectedBoundaries := map[metering.Boundary]struct {
		perspective metering.EconomicPerspective
		authority   string
	}{
		metering.BoundaryBackendEgress:  {perspective: metering.PerspectiveOperator, authority: metering.AuthorityObservedClaim},
		metering.BoundaryBackendIngress: {perspective: metering.PerspectiveOperator, authority: metering.AuthorityUnavailableClaim},
		metering.BoundaryFrontendEgress: {perspective: metering.PerspectiveCustomer, authority: metering.AuthorityUnavailableClaim},
	}
	seenBoundaries := make(map[metering.Boundary]struct{}, len(leg.Observations))
	for _, obs := range leg.Observations {
		expected, ok := expectedBoundaries[obs.Boundary]
		if !ok {
			t.Fatalf("unexpected boundary observation %q; provider/remote observations are not allowed", obs.Boundary)
		}
		if _, duplicate := seenBoundaries[obs.Boundary]; duplicate {
			t.Fatalf("duplicate local boundary observation %q", obs.Boundary)
		}
		seenBoundaries[obs.Boundary] = struct{}{}
		if obs.Origin != metering.OriginLocal {
			t.Fatalf("observation %q origin = %q, want %q", obs.Boundary, obs.Origin, metering.OriginLocal)
		}
		if obs.Acquisition != metering.AcquisitionLocalMeasurement {
			t.Fatalf("observation %q acquisition = %q, want %q", obs.Boundary, obs.Acquisition, metering.AcquisitionLocalMeasurement)
		}
		if obs.Authority != expected.authority {
			t.Fatalf("observation %q authority = %q, want %q", obs.Boundary, obs.Authority, expected.authority)
		}
		if obs.Perspective != expected.perspective {
			t.Fatalf("observation %q perspective = %q, want %q", obs.Boundary, obs.Perspective, expected.perspective)
		}
		if obs.Lifecycle != metering.LifecycleBackendAttempt {
			t.Fatalf("observation %q lifecycle = %q, want %q", obs.Boundary, obs.Lifecycle, metering.LifecycleBackendAttempt)
		}
		if obs.MappingRef != coremetering.BoundaryMappingRef {
			t.Fatalf("observation %q mapping = %q, want %q", obs.Boundary, obs.MappingRef, coremetering.BoundaryMappingRef)
		}
		if obs.Subject.Kind != metering.SubjectBLeg {
			t.Fatalf("observation %q subject kind = %q, want %q", obs.Boundary, obs.Subject.Kind, metering.SubjectBLeg)
		}
		if obs.Subject != expectedSubject {
			t.Fatalf("observation %q subject = %+v, want %+v", obs.Boundary, obs.Subject, expectedSubject)
		}
		if obs.Correlation != expectedCorrelation {
			t.Fatalf("observation %q correlation = %+v, want %+v", obs.Boundary, obs.Correlation, expectedCorrelation)
		}
		if obs.Subject.ProviderAccountKey != "" || obs.Subject.ProviderRequestID != "" || obs.Subject.ProviderChargeID != "" {
			t.Fatalf("observation %q reports provider subject economics: %+v", obs.Boundary, obs.Subject)
		}
		if obs.Correlation.ProviderAccountKey != "" || obs.Correlation.ProviderRequestID != "" || obs.Correlation.ProviderChargeID != "" {
			t.Fatalf("observation %q reports provider correlation economics: %+v", obs.Boundary, obs.Correlation)
		}
		if len(obs.Charges) != 0 || len(obs.Evidence) != 0 {
			t.Fatalf("observation %q reports charges/provider economic evidence: charges=%d evidence=%d", obs.Boundary, len(obs.Charges), len(obs.Evidence))
		}
	}
	for boundary := range expectedBoundaries {
		if _, ok := seenBoundaries[boundary]; !ok {
			t.Fatalf("missing local boundary observation %q", boundary)
		}
	}
	if len(leg.EconomicDispositions) != 0 {
		t.Fatalf("phantom economic dispositions on canceled leg: %d", len(leg.EconomicDispositions))
	}

	// 6. Account balance reflects the legitimate configured fixed request charge (10 nano), with no token charge.
	acctAfter, err := store.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	const expectedFixedFeeNano = int64(10)
	if acctAfter.BalanceNano != acctBefore.BalanceNano-expectedFixedFeeNano {
		t.Fatalf("account balance mismatch: before=%d after=%d want=%d", acctBefore.BalanceNano, acctAfter.BalanceNano, acctBefore.BalanceNano-expectedFixedFeeNano)
	}
	if acctAfter.Version != acctBefore.Version+1 {
		t.Fatalf("account version mismatch: before=%d after=%d want=%d", acctBefore.Version, acctAfter.Version, acctBefore.Version+1)
	}

	// 7. Exactly one customer_call_settlement transaction for fixed request fee, no provider_call_cogs
	txs, err := store.JournalTransactions(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	var customerSettlements int
	for _, tx := range txs {
		if tx.OperationKind == "provider_call_cogs" {
			t.Fatalf("unexpected provider_call_cogs transaction: %+v", tx)
		}
		if tx.OperationKind == "customer_call_settlement" {
			customerSettlements++
			if charge := tx.BalanceBefore - tx.BalanceAfter; charge != expectedFixedFeeNano {
				t.Fatalf("customer settlement charge = %d, want %d", charge, expectedFixedFeeNano)
			}
		}
	}
	if customerSettlements != 1 {
		t.Fatalf("expected 1 customer_call_settlement transaction, got %d", customerSettlements)
	}

	// 8. Provider work is queried by the sealed canceled leg key, independently of due status.
	sealedLeg, err := leg.Seal()
	if err != nil {
		t.Fatalf("seal canceled leg for provider-cost lookup: %v", err)
	}
	durableLeg, err := store.GetCallLegUsage(ctx, sealedLeg.Key)
	if err != nil {
		t.Fatalf("GetCallLegUsage(%q): %v", sealedLeg.Key, err)
	}
	if durableLeg.CallID != closure.CallID || durableLeg.BLegID != leg.BLegID {
		t.Fatalf("provider-cost lookup leg identity = call %q/b-leg %q, want call %q/b-leg %q", durableLeg.CallID, durableLeg.BLegID, closure.CallID, leg.BLegID)
	}
	providerState := waitRefinement51ProviderCostWorkState(t, ctx, store, sealedLeg.Key, providerCostWorkDone)
	const expectedProviderCostFailure = "billing: provider cost is unreconciled: provider_evidence_unavailable"
	if providerState.Status != "pending" {
		t.Fatalf("provider-cost work status = %q, want pending/unreconciled", providerState.Status)
	}
	if providerState.AttemptCount != 1 {
		t.Fatalf("provider-cost work attempt count = %d, want 1", providerState.AttemptCount)
	}
	if providerState.NextAttemptAt.IsZero() {
		t.Fatal("provider-cost work next attempt metadata is zero")
	}
	if providerState.LastError != expectedProviderCostFailure {
		t.Fatalf("provider-cost work failure reason = %q, want %q", providerState.LastError, expectedProviderCostFailure)
	}
	t.Logf("provider-cost state: status=%q attempt_count=%d next_attempt_at_nonzero=%t last_error=%q", providerState.Status, providerState.AttemptCount, !providerState.NextAttemptAt.IsZero(), providerState.LastError)
	operatorReport, err := store.OperatorCostReport(ctx, billing.ReportFilter{AccountID: accountID, Currency: "USD", Book: billing.JournalBookFinancial})
	if err != nil {
		t.Fatalf("OperatorCostReport: %v", err)
	}
	if operatorReport.UnreconciledCosts != 1 {
		t.Fatalf("OperatorCostReport.UnreconciledCosts = %d, want 1", operatorReport.UnreconciledCosts)
	}
	if operatorReport.ProviderCost.Nano != 0 {
		t.Fatalf("OperatorCostReport.ProviderCost = %d, want 0 without provider_call_cogs", operatorReport.ProviderCost.Nano)
	}
	var unreconciledIssue bool
	for _, issue := range operatorReport.Issues {
		if issue.Code == "unreconciled_cost" {
			unreconciledIssue = true
			if issue.Detail != accountID {
				t.Fatalf("unreconciled issue detail = %q, want account %q", issue.Detail, accountID)
			}
		}
	}
	if !unreconciledIssue {
		t.Fatalf("OperatorCostReport.Issues = %+v, want unreconciled_cost diagnostic", operatorReport.Issues)
	}
	t.Logf("operator cost report: unreconciled_costs=%d provider_cost_nano=%d unreconciled_issue=%t", operatorReport.UnreconciledCosts, operatorReport.ProviderCost.Nano, unreconciledIssue)

	// 9. Exposure closes for the actual account/call and retains the B-leg correlations.
	if exposure.AccountID != accountID {
		t.Fatalf("exposure AccountID = %q, want %q", exposure.AccountID, accountID)
	}
	if exposure.CallID != expectedCallID {
		t.Fatalf("exposure CallID = %q, want %q", exposure.CallID, expectedCallID)
	}
	if exposure.Status != billing.ExposureClosed {
		t.Fatalf("exposure status = %q, want %q", exposure.Status, billing.ExposureClosed)
	}
	if exposure.ClosedAt.IsZero() {
		t.Fatal("exposure ClosedAt is zero")
	}

	if complete.Closure.CallID != closure.CallID {
		t.Fatalf("complete CallID %q != closure CallID %q", complete.Closure.CallID, closure.CallID)
	}
	if len(complete.Legs) != 1 || complete.Legs[0].BLegID != leg.BLegID {
		t.Fatalf("complete legs mismatch: %+v", complete.Legs)
	}
}

func refinement51JoinCollect(t *testing.T, done <-chan struct{}, cancel context.CancelFunc) {
	t.Helper()
	if cancel != nil {
		cancel()
	}
	joinCtx, joinCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer joinCancel()
	select {
	case <-done:
	case <-joinCtx.Done():
		t.Errorf("Collect goroutine did not terminate after cancellation: %v", joinCtx.Err())
	}
}

func assertRefinement51NoPhantomTokenEconomics(t *testing.T, leg billing.CallLegUsageRecord) {
	t.Helper()
	quantities := []struct {
		name  string
		value billing.Quantity
	}{
		{"input tokens", leg.Evidence.InputTokens},
		{"output tokens", leg.Evidence.OutputTokens},
		{"reasoning tokens", leg.Evidence.ReasoningTokens},
		{"cache-read tokens", leg.Evidence.CacheReadTokens},
		{"cache-write tokens", leg.Evidence.CacheWriteTokens},
		{"total tokens", leg.Evidence.TotalTokens},
	}
	for _, quantity := range quantities {
		if quantity.value.Value != 0 {
			t.Fatalf("phantom %s billed: value=%d present=%t", quantity.name, quantity.value.Value, quantity.value.Present)
		}
	}
	if leg.Evidence.Cost.NanoUnits != 0 {
		t.Fatalf("phantom provider cost billed: nano=%d present=%t currency=%q", leg.Evidence.Cost.NanoUnits, leg.Evidence.Cost.Present, leg.Evidence.Cost.Currency)
	}
	if len(leg.ObservationRefs) != 0 || len(leg.EvidenceConflicts) != 0 || len(leg.EconomicDispositions) != 0 {
		t.Fatalf("phantom economic carriers: observation_refs=%d conflicts=%d dispositions=%d", len(leg.ObservationRefs), len(leg.EvidenceConflicts), len(leg.EconomicDispositions))
	}
	for _, observation := range leg.Observations {
		if len(observation.Charges) != 0 || len(observation.Evidence) != 0 {
			t.Fatalf("phantom observation economics: boundary=%q charges=%d evidence=%d", observation.Boundary, len(observation.Charges), len(observation.Evidence))
		}
		for _, measure := range observation.Measures {
			if !refinement51CanonicalProviderTokenComponent(measure.Key.Component) || measure.Value == nil {
				continue
			}
			normalized, err := measure.Value.Normalize()
			if err != nil {
				t.Fatalf("invalid token measure %q: %v", measure.Key.Component, err)
			}
			if normalized.Coefficient != "0" {
				t.Fatalf("phantom token measure: key=%+v value=%s", measure.Key, normalized.CanonicalString())
			}
		}
	}
}

func refinement51CanonicalProviderTokenComponent(component string) bool {
	switch component {
	case metering.ComponentInputToken,
		metering.ComponentInputTokenUncached,
		metering.ComponentInputTokenTotal,
		metering.ComponentCacheReadInputToken,
		metering.ComponentCacheWriteInputToken,
		metering.ComponentOutputToken,
		metering.ComponentReasoningOutputToken,
		metering.ComponentTotalToken:
		return true
	default:
		// Customer-boundary text_token measures from the local tokenizer are
		// service observations, not provider inference economics.
		return false
	}
}

func waitRefinement51ProviderCostWorkState(t *testing.T, ctx context.Context, store *billingstore.DurableStore, legKey string, done <-chan string) billingstore.ProviderCostWorkState {
	t.Helper()
	waitCtx, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()
	for {
		select {
		case completedLegKey, ok := <-done:
			if !ok {
				t.Fatalf("provider-cost worker completion notification closed before leg %q", legKey)
			}
			if completedLegKey != legKey {
				continue
			}
			// Read the state by the exact committed key carried by the notification.
			state, err := store.GetProviderCostWorkState(ctx, completedLegKey)
			if err != nil {
				if errors.Is(err, billingstore.ErrUsageRecordNotFound) {
					t.Fatalf("provider-cost worker completed without durable state for leg %q", completedLegKey)
				}
				t.Fatalf("GetProviderCostWorkState(%q): %v", completedLegKey, err)
			}
			return state
		case <-waitCtx.Done():
			t.Fatalf("timed out waiting for provider-cost worker completion for leg %q: %v", legKey, waitCtx.Err())
		}
	}
}
