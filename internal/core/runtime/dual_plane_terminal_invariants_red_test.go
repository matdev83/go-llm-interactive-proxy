package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	corestate "github.com/matdev83/go-llm-interactive-proxy/internal/core/state"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// Phase 1.1 RED contracts: customer-versus-operator terminal invariants
// (requirements 1.1–1.9, 2.1–2.9, 13.1–13.2; design D1, D2, D3, D13, D17).

func TestDualPlaneTerminalInvariants_ProviderUsageMustNotEnterCustomerSettlement(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "customer-settle"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "customer-settle", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-cust-usage"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-cust-usage", "a-1", "trace-cust-usage", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	provider := lipapi.ScopedUsageDelta{
		InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	delivered := lipapi.ScopedUsageDelta{
		InputTokens: 40, OutputTokens: 12, TotalTokens: 52,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated,
		},
	}
	raw := lipapi.Event{
		Kind:        lipapi.EventUsageDelta,
		UsageScopes: []lipapi.ScopedUsageDelta{delivered, provider},
	}
	authorityEv := authorityUsageEvent([]lipapi.Event{raw})
	if authorityEv.InputTokens != provider.InputTokens {
		t.Fatalf("precondition: authorityUsageEvent must prefer provider input; got %d", authorityEv.InputTokens)
	}

	call, output := clientVisibleCount(delivered.InputTokens, delivered.OutputTokens)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: call, output: output}, accountingstream.Config{})
	stream := &retryRecvStream{
		executor: ex, traceID: "trace-cust-usage",
		customer: newCustomerEvidenceAccumulator(),
		baseline: lipapi.Call{ID: "req-cust-usage"},
	}
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "cust-delivered"})
	stream.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv)

	if prov.settleCalls.Load() != 1 {
		t.Fatalf("customer SettleRequest calls=%d want 1", prov.settleCalls.Load())
	}
	facts, _ := prov.lastFacts.Load().([]metering.Fact)
	if len(facts) != 1 {
		t.Fatalf("customer settle facts=%d want 1", len(facts))
	}
	fact := facts[0]
	if fact.Perspective != metering.PerspectiveCustomer || fact.Boundary != metering.BoundaryFrontendEgress {
		t.Fatalf("customer fact plane=%s/%s", fact.Perspective, fact.Boundary)
	}
	in, ok := checkpoint.QuantityComponentValue(fact.Quantities, metering.ComponentInputToken)
	if !ok {
		t.Fatal("customer FE egress must carry input_token")
	}
	if in != int64(delivered.InputTokens) {
		t.Fatalf("customer settlement input_token=%d want delivered %d (must not import provider usage %d)", in, delivered.InputTokens, provider.InputTokens)
	}
	out, ok := checkpoint.QuantityComponentValue(fact.Quantities, metering.ComponentOutputToken)
	if !ok || out != int64(delivered.OutputTokens) {
		t.Fatalf("customer settlement output_token=%d ok=%v want delivered %d", out, ok, delivered.OutputTokens)
	}
}

func TestDualPlaneTerminalInvariants_ProviderCostMustNotEnterFrontendFacts(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         lipapi.Call{ID: "req-fe-cost"},
		CheckpointID: "fe",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)

	usage := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   3,
		OutputTokens:  1,
		TotalTokens:   4,
		CostNanoUnits: 42_000,
		Currency:      "USD",
		CostSource:    string(lipapi.UsageSourceProviderReported),
		CostPresent:   true,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	fact, ok := ex.emitFrontendEgressMeteringFact(ctx, "trace-fe-cost", usage)
	if !ok {
		t.Fatal("expected frontend-egress fact")
	}
	if fact.Perspective != metering.PerspectiveCustomer {
		t.Fatalf("perspective=%s", fact.Perspective)
	}
	if fact.Money != nil {
		t.Fatalf("customer FE egress must not carry provider money; got %+v", fact.Money)
	}
}

func TestDualPlaneTerminalInvariants_AttemptMoneyRatedFromFinalBackendIngressNotStalePreflight(t *testing.T) {
	t.Parallel()

	rater := &injectedRater{nano: 1, currency: "USD"}
	attProv := &recordingAttemptProvider{id: "rate-att"}
	ex := &Executor{}
	ex.EconomicsRater = rater
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "rate-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	const beAttempt = "b-leg-final"
	_, err := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID: "req-rate-final",
			Messages: []lipapi.Message{{
				Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("compressed")},
			}},
		},
		AttemptID: beAttempt, BLegID: beAttempt, ALegID: "a-1",
		BackendID: "backend-1", Model: "model-1",
		CheckpointID: "be", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeBackendIngressQuantities(beAttempt, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 500, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 64, Present: true},
	})
	be := holder.BackendIngressFor(beAttempt)
	finalIn, ok := checkpoint.QuantityComponentValue(be.Public.Quantities, metering.ComponentInputToken)
	if !ok || finalIn != 500 {
		t.Fatalf("precondition: BE input=%d ok=%v", finalIn, ok)
	}

	staleDecision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 10, OutputTokens: 64, TotalTokens: 74, TotalTokensPresent: true},
	}
	ctx := withMeteringHolder(context.Background(), holder)
	_, err = ex.admitAttemptAuthority(
		ctx, "trace-rate-final", "a-1",
		b2bua.BLegRecord{BLegID: beAttempt, Seq: 1},
		lipapi.Call{ID: "req-rate-final"},
		routing.AttemptCandidate{Key: "backend-1:model-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		staleDecision, false,
	)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if rater.calls.Load() < 1 {
		t.Fatal("operator rater must be invoked")
	}
	req, _ := rater.last.Load().(economics.RatingRequest)
	ratedIn, ok := checkpoint.QuantityComponentValue(req.Quantities, metering.ComponentInputToken)
	if !ok {
		t.Fatal("rating request must include input_token")
	}
	if ratedIn != finalIn {
		t.Fatalf("operator rating input_token=%d want final backend-ingress %d (stale preflight was %d)", ratedIn, finalIn, staleDecision.Count.InputTokens)
	}
}

func TestDualPlaneTerminalInvariants_CompressionIngressPlanesAndDeliveredEgressOutput(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{
			ID: "req-compress",
			Messages: []lipapi.Message{{
				Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("original long prompt")},
			}},
		},
		CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeFrontendIngressQuantities([]metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1000, Present: true},
	})
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID: "req-compress",
			Messages: []lipapi.Message{{
				Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("compressed")},
			}},
		},
		AttemptID: "b-1", BLegID: "b-1", ALegID: "a-1",
		BackendID: "backend-1", Model: "model-1",
		CheckpointID: "be", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeBackendIngressQuantities("b-1", []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 200, Present: true},
	})

	feIn, _ := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken)
	beIn, _ := checkpoint.QuantityComponentValue(holder.BackendIngressFor("b-1").Public.Quantities, metering.ComponentInputToken)
	if feIn == beIn {
		t.Fatal("precondition: compression requires diverging FE-ingress vs BE-ingress input")
	}
	if feIn != 1000 || beIn != 200 {
		t.Fatalf("ingress inputs fe=%d be=%d", feIn, beIn)
	}

	const deliveredOut = 12
	provider := lipapi.ScopedUsageDelta{
		InputTokens: int(beIn), OutputTokens: 90, TotalTokens: int(beIn) + 90,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	delivered := lipapi.ScopedUsageDelta{
		InputTokens: 0, OutputTokens: deliveredOut, TotalTokens: deliveredOut,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated,
		},
	}
	beUsage := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: int(beIn), OutputTokens: 90, TotalTokens: int(beIn) + 90,
		Accounting: provider.Accounting,
	}
	raw := lipapi.Event{
		Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{delivered, provider},
	}
	authorityEv := authorityUsageEvent([]lipapi.Event{raw})
	if authorityEv.OutputTokens != provider.OutputTokens {
		t.Fatalf("precondition: authorityUsageEvent prefers provider output; got %d", authorityEv.OutputTokens)
	}

	ctx := withMeteringHolder(context.Background(), holder)
	_, output := clientVisibleCount(0, deliveredOut)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{},
		output: output,
	}, accountingstream.Config{})
	stream := &retryRecvStream{
		executor: ex, traceID: "trace-compress", bleg: b2bua.BLegRecord{BLegID: "b-1"},
		customer: newCustomerEvidenceAccumulator(),
		baseline: lipapi.Call{ID: "req-compress"},
	}
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "compressed-out"})
	stream.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeWinner, metering.SurfacedYes, beUsage)
	stream.emitFrontendEgressMeteringFact(ctx, authorityEv)

	if len(rec.facts) != 2 {
		t.Fatalf("facts=%d want BE+FE", len(rec.facts))
	}
	beFact, feFact := rec.facts[0], rec.facts[1]
	beFactIn, _ := checkpoint.QuantityComponentValue(beFact.Quantities, metering.ComponentInputToken)
	if beFactIn != beIn {
		t.Fatalf("operator BE egress input=%d want BE-ingress %d", beFactIn, beIn)
	}
	feOut, ok := checkpoint.QuantityComponentValue(feFact.Quantities, metering.ComponentOutputToken)
	if !ok {
		t.Fatal("customer FE egress must carry delivered output_token")
	}
	if feOut != deliveredOut {
		t.Fatalf("customer FE egress output=%d want delivered %d (provider was %d)", feOut, deliveredOut, provider.OutputTokens)
	}
}

func TestDualPlaneTerminalInvariants_ResponseFilteringCustomerOutputFromDelivered(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "filter-settle"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "filter-settle", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{ID: "req-filter"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      lipapi.Call{ID: "req-filter", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
		AttemptID: "b-1", BLegID: "b-1", CheckpointID: "be", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-filter", "a-1", "trace-filter", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	const providerOut, deliveredOut = 90, 25
	provider := lipapi.ScopedUsageDelta{
		InputTokens: 10, OutputTokens: providerOut, TotalTokens: 10 + providerOut,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	delivered := lipapi.ScopedUsageDelta{
		InputTokens: 10, OutputTokens: deliveredOut, TotalTokens: 10 + deliveredOut,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated,
		},
	}
	raw := lipapi.Event{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{delivered, provider}}
	authorityEv := authorityUsageEvent([]lipapi.Event{raw})
	if authorityEv.OutputTokens != providerOut {
		t.Fatalf("precondition: authorityUsageEvent prefers provider output; got %d", authorityEv.OutputTokens)
	}

	call, output := clientVisibleCount(delivered.InputTokens, deliveredOut)
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: call, output: output}, accountingstream.Config{})
	stream := &retryRecvStream{
		executor: ex, traceID: "trace-filter", bleg: b2bua.BLegRecord{BLegID: "b-1"},
		customer: newCustomerEvidenceAccumulator(),
		baseline: lipapi.Call{ID: "req-filter"},
	}
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "filtered-out"})
	stream.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeWinner, metering.SurfacedYes, authorityEv)
	stream.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv)

	beOut, _ := checkpoint.QuantityComponentValue(rec.facts[0].Quantities, metering.ComponentOutputToken)
	if beOut != providerOut {
		t.Fatalf("precondition: operator BE egress output=%d want provider %d", beOut, providerOut)
	}
	facts, _ := prov.lastFacts.Load().([]metering.Fact)
	if len(facts) != 1 {
		t.Fatalf("customer facts=%d", len(facts))
	}
	out, ok := checkpoint.QuantityComponentValue(facts[0].Quantities, metering.ComponentOutputToken)
	if !ok {
		t.Fatal("customer fact missing output_token")
	}
	if out != deliveredOut {
		t.Fatalf("customer output_token=%d want delivered/filtered %d (provider was %d)", out, deliveredOut, providerOut)
	}
}

func TestDualPlaneTerminalInvariants_ExplicitZeroCostOperatorPresentCustomerAbsent(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{ID: "req-zero"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      lipapi.Call{ID: "req-zero", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
		AttemptID: "b-1", BLegID: "b-1", CheckpointID: "be", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	stream := &retryRecvStream{executor: ex, traceID: "trace-zero", bleg: b2bua.BLegRecord{BLegID: "b-1"}}

	t.Run("absent_cost_must_not_synthesize_present_money_on_customer", func(t *testing.T) {
		absent := lipapi.Event{
			Kind: lipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 1, TotalTokens: 2,
			Currency: "USD", CostNanoUnits: 0, CostPresent: false,
		}
		fe, ok := stream.emitFrontendEgressMeteringFact(ctx, absent)
		if !ok {
			t.Fatal("expected FE fact")
		}
		if fe.Money != nil && fe.Money.Present {
			t.Fatalf("absent cost must not become Present money on customer fact; got %+v", fe.Money)
		}
	})

	t.Run("provider_explicit_zero_operator_keeps_customer_omits", func(t *testing.T) {
		zero := lipapi.Event{
			Kind: lipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 1, TotalTokens: 2,
			Currency: "USD", CostNanoUnits: 0, CostPresent: true,
			CostSource: string(lipapi.UsageSourceProviderReported),
		}
		stream.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeWinner, metering.SurfacedYes, zero)
		fe, ok := stream.emitFrontendEgressMeteringFact(ctx, zero)
		if !ok {
			t.Fatal("expected FE fact")
		}
		var be *metering.Fact
		for i := range rec.facts {
			if rec.facts[i].Boundary == metering.BoundaryBackendEgress {
				be = &rec.facts[i]
			}
		}
		if be == nil || be.Money == nil || !be.Money.Present || be.Money.NanoUnits != 0 {
			t.Fatalf("operator BE must retain authoritative zero money; got %+v", be)
		}
		if fe.Money != nil {
			t.Fatalf("customer FE must omit provider money including explicit zero; got %+v", fe.Money)
		}
	})
}

func TestDualPlaneTerminalInvariants_SequentialFailoverOperatorSettlePerIncurredAttempt(t *testing.T) {
	t.Parallel()

	attProv := &recordingAttemptProvider{id: "failover-att"}
	reqProv := &settleRecordingRequestProvider{id: "failover-req"}
	ex := &Executor{}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "failover-req", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: reqProv, Strength: authority.StrengthRequired,
		}},
	}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "failover-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	ctx, err := ex.admitRequestAuthorityOnce(context.Background(), "req-failover", "a-1", "trace-failover", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit request: %v", err)
	}
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 5, OutputTokens: 5, TotalTokens: 10, TotalTokensPresent: true},
	}
	failState, err := ex.admitAttemptAuthority(ctx, "trace-failover", "a-1", b2bua.BLegRecord{BLegID: "b-fail", Seq: 1},
		lipapi.Call{ID: "req-failover"},
		routing.AttemptCandidate{Key: "b-fail", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit fail: %v", err)
	}
	winState, err := ex.admitAttemptAuthority(ctx, "trace-failover", "a-1", b2bua.BLegRecord{BLegID: "b-win", Seq: 2},
		lipapi.Call{ID: "req-failover"},
		routing.AttemptCandidate{Key: "b-win", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit win: %v", err)
	}
	if attProv.admitCalls.Load() != 2 {
		t.Fatalf("admit calls=%d want 2", attProv.admitCalls.Load())
	}

	failLife := ex.newAttemptAuthorityLifecycle(failState, routing.AttemptCandidate{Key: "b-fail"})
	winLife := ex.newAttemptAuthorityLifecycle(winState, routing.AttemptCandidate{Key: "b-win"})
	failLife.backendAttempted.Store(true)
	winLife.backendAttempted.Store(true)
	usage := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 5, OutputTokens: 5, TotalTokens: 10}

	// Sequential failover terminal path: incurred failed attempt settles; winner settles.
	failLife.finalizeIncurredOrRelease(ctx, authorityapp.ReleaseKindLosing, usage)
	if !winLife.Settle(ctx, authorityapp.SettlementKindFinal, usage, false) {
		t.Fatal("winner settle must apply")
	}
	ex.settleRequestAuthority(ctx, nil)

	if reqProv.settleCalls.Load() != 1 {
		t.Fatalf("customer SettleRequest=%d want 1", reqProv.settleCalls.Load())
	}
	// Every incurred attempt settles operator authority (req 1.5, 1.7).
	if attProv.settleCalls.Load() != 2 {
		t.Fatalf("operator SettleAttempt=%d want 2 (one per incurred attempt)", attProv.settleCalls.Load())
	}
}

func TestDualPlaneTerminalInvariants_ParallelLoserOperatorSettlePerIncurredAttempt(t *testing.T) {
	t.Parallel()

	attProv := &recordingAttemptProvider{id: "parallel-att"}
	reqProv := &settleRecordingRequestProvider{id: "parallel-req"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "parallel-req", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: reqProv, Strength: authority.StrengthRequired,
		}},
	}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "parallel-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{ID: "req-parallel"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b-loser", "b-winner"} {
		_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
			Call:      lipapi.Call{ID: "req-parallel", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(id)}}}},
			AttemptID: id, BLegID: id, CheckpointID: "be-" + id, StreamID: "be-" + id,
			Now: time.Unix(2, 0).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-parallel", "a-1", "trace-parallel", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit request: %v", err)
	}
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 7, OutputTokens: 3, TotalTokens: 10, TotalTokensPresent: true},
	}
	loserState, err := ex.admitAttemptAuthority(ctx, "trace-parallel", "a-1", b2bua.BLegRecord{BLegID: "b-loser", Seq: 1},
		lipapi.Call{ID: "req-parallel"},
		routing.AttemptCandidate{Key: "b-loser", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit loser: %v", err)
	}
	winnerState, err := ex.admitAttemptAuthority(ctx, "trace-parallel", "a-1", b2bua.BLegRecord{BLegID: "b-winner", Seq: 2},
		lipapi.Call{ID: "req-parallel"},
		routing.AttemptCandidate{Key: "b-winner", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit winner: %v", err)
	}

	loserLife := ex.newAttemptAuthorityLifecycle(loserState, routing.AttemptCandidate{Key: "b-loser"})
	winnerLife := ex.newAttemptAuthorityLifecycle(winnerState, routing.AttemptCandidate{Key: "b-winner"})
	loserLife.backendAttempted.Store(true)
	winnerLife.backendAttempted.Store(true)
	usage := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 7, OutputTokens: 3, TotalTokens: 10,
		CostNanoUnits: 55, Currency: "USD", CostPresent: true, CostSource: string(lipapi.UsageSourceProviderReported),
	}
	loserObserved := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 2, TotalTokens: 13,
		CostNanoUnits: 77, Currency: "USD", CostPresent: true, CostSource: string(lipapi.UsageSourceProviderReported),
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}

	legs := []*parallelLeg{{
		authority: loserLife,
		bleg:      b2bua.BLegRecord{BLegID: "b-loser"},
	}}
	legs[0].observedUsage.Store(loserObserved)
	_ = ex.releaseLosers(ctx, nil, legs)
	if !winnerLife.Settle(ctx, authorityapp.SettlementKindFinal, usage, false) {
		t.Fatal("winner settle must apply")
	}
	stream := &retryRecvStream{executor: ex, traceID: "trace-parallel", bleg: b2bua.BLegRecord{BLegID: "b-winner"}}
	stream.settleRequestAuthorityWithFrontendEgress(ctx, usage)

	if reqProv.settleCalls.Load() != 1 {
		t.Fatalf("customer settlements=%d want 1", reqProv.settleCalls.Load())
	}
	if attProv.releaseCalls.Load() != 0 {
		t.Fatalf("incurred loser must SettleAttempt, not ReleaseAttempt; releaseCalls=%d", attProv.releaseCalls.Load())
	}
	if attProv.settleCalls.Load() != 2 {
		t.Fatalf("operator SettleAttempt=%d want 2 (winner + incurred loser)", attProv.settleCalls.Load())
	}
	var loserBE *metering.Fact
	var feMoney *metering.MoneyObservation
	for i := range rec.facts {
		f := &rec.facts[i]
		if f.Boundary == metering.BoundaryBackendEgress && f.AttemptOutcome == metering.AttemptOutcomeLoser {
			loserBE = f
		}
		if f.Boundary == metering.BoundaryFrontendEgress {
			feMoney = f.Money
		}
	}
	if loserBE == nil {
		t.Fatal("loser BE egress fact missing")
	}
	in, ok := checkpoint.QuantityComponentValue(loserBE.Quantities, metering.ComponentInputToken)
	if !ok || in != 11 {
		t.Fatalf("loser BE input_token=%d ok=%v want observed 11", in, ok)
	}
	out, ok := checkpoint.QuantityComponentValue(loserBE.Quantities, metering.ComponentOutputToken)
	if !ok || out != 2 {
		t.Fatalf("loser BE output_token=%d ok=%v want observed 2", out, ok)
	}
	if loserBE.Money == nil || !loserBE.Money.Present || loserBE.Money.NanoUnits != 77 {
		t.Fatalf("loser BE money=%+v want observed 77 USD", loserBE.Money)
	}
	if feMoney != nil {
		t.Fatalf("customer FE must not inherit provider money; got %+v", feMoney)
	}
}

func TestDualPlaneTerminalInvariants_SwallowedFinalizeRetainsSeenUsage(t *testing.T) {
	t.Parallel()

	attProv := &recordingAttemptProvider{id: "swallow-att"}
	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "swallow-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{ID: "req-swallow"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      lipapi.Call{ID: "req-swallow", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
		AttemptID: "b-sw", BLegID: "b-sw", CheckpointID: "be-sw", StreamID: "be-sw", Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	decision := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 3, OutputTokens: 1, TotalTokens: 4, TotalTokensPresent: true},
	}
	state, err := ex.admitAttemptAuthority(ctx, "trace-sw", "a-1", b2bua.BLegRecord{BLegID: "b-sw", Seq: 1},
		lipapi.Call{ID: "req-swallow"},
		routing.AttemptCandidate{Key: "b-sw", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		decision, false)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	life := ex.newAttemptAuthorityLifecycle(state, routing.AttemptCandidate{Key: "b-sw"})
	life.backendAttempted.Store(true)

	stream := &retryRecvStream{
		executor:  ex,
		traceID:   "trace-sw",
		bleg:      b2bua.BLegRecord{BLegID: "b-sw"},
		authority: life,
		seenEvents: []lipapi.Event{{
			Kind: lipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 4, TotalTokens: 13,
			CostNanoUnits: 33, Currency: "USD", CostPresent: true, CostSource: string(lipapi.UsageSourceProviderReported),
			Accounting: lipapi.UsageAccountingMetadata{
				Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
			},
		}},
	}
	usage := stream.operatorUsageForFinalize()
	stream.authority.finalizeIncurredOrRelease(ctx, authorityapp.ReleaseKindSwallowed, usage)
	stream.emitBackendEgressMeteringFact(ctx, metering.AttemptOutcomeFailed, metering.SurfacedNo, usage)

	if attProv.settleCalls.Load() != 1 {
		t.Fatalf("swallowed incurred SettleAttempt=%d want 1", attProv.settleCalls.Load())
	}
	var be *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryBackendEgress {
			be = &rec.facts[i]
			break
		}
	}
	if be == nil {
		t.Fatal("BE egress fact missing")
	}
	in, ok := checkpoint.QuantityComponentValue(be.Quantities, metering.ComponentInputToken)
	if !ok || in != 9 {
		t.Fatalf("BE input_token=%d ok=%v want seen 9", in, ok)
	}
	if be.Money == nil || be.Money.NanoUnits != 33 {
		t.Fatalf("BE money=%+v want seen 33", be.Money)
	}
}

func TestDualPlaneTerminalInvariants_UnobservedIncurredKeepsEmptyShell(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	holder := &checkpoint.RequestHolder{}
	_, err := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      lipapi.Call{ID: "req-empty", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
		AttemptID: "b-empty", BLegID: "b-empty", CheckpointID: "be-empty", StreamID: "be-empty", Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ex.emitBackendEgressMeteringFact(ctx, "b-empty", metering.AttemptOutcomeLoser, metering.SurfacedNo, emptyOperatorUsageShell())
	if len(rec.facts) != 1 {
		t.Fatalf("facts=%d want 1", len(rec.facts))
	}
	if len(rec.facts[0].Quantities) != 0 || rec.facts[0].Money != nil {
		t.Fatalf("unobserved shell must omit quantities/money; got q=%v money=%+v", rec.facts[0].Quantities, rec.facts[0].Money)
	}
}

func TestDualPlaneTerminalInvariants_AuxiliaryCallParentScopeSeparatesPlanes(t *testing.T) {
	t.Parallel()

	rec := &recordingMeter{}
	var ex *Executor
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		State: corestate.NewMem(nil),
		Aux: auxreq.NewClient(func() auxreq.ExecutorRunner {
			return ex
		}),
	})
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, st)
	var openScope atomic.Value
	ex = TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(2000, 0).UTC() }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				if sc, ok := scope.ScopeFromContext(ctx); ok {
					openScope.Store(sc)
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "aux-out"},
					{
						Kind: lipapi.EventUsageDelta, InputTokens: 2, OutputTokens: 1, TotalTokens: 3,
						CostNanoUnits: 99, Currency: "USD", CostPresent: true,
						CostSource: string(lipapi.UsageSourceProviderReported),
						Accounting: lipapi.UsageAccountingMetadata{
							Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
						},
					},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)

	parent := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("parent-user"),
		Origin:      scope.OriginClient,
	}
	ctx := scope.WithScope(context.Background(), parent)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("aux")},
		}},
	}
	stream, err := snap.Aux().Stream(ctx, auxiliary.Request{
		ParentTraceID: "trace-parent",
		Call:          call,
	})
	if err != nil {
		t.Fatalf("aux Stream: %v", err)
	}
	for {
		_, err := stream.Recv(ctx)
		if err != nil {
			break
		}
	}
	_ = stream.Close()

	got, ok := openScope.Load().(scope.PrincipalScopeView)
	if !ok {
		t.Fatal("expected auxiliary Open to observe parent-derived scope")
	}
	if got.Origin != scope.OriginInternal {
		t.Fatalf("aux origin=%q want internal", got.Origin)
	}
	if got.ParentTraceID.String() != "trace-parent" {
		t.Fatalf("ParentTraceID=%q want trace-parent", got.ParentTraceID.String())
	}

	var fe *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryFrontendEgress &&
			rec.facts[i].Perspective == metering.PerspectiveCustomer {
			fe = &rec.facts[i]
			break
		}
	}
	if fe == nil {
		t.Fatal("auxiliary child ResponseFinished terminalization must record customer FE egress fact")
	}
	if fe.Money != nil {
		t.Fatalf("auxiliary customer FE fact must omit provider money; got %+v", fe.Money)
	}
}

func TestDualPlaneTerminalInvariants_CaptureFrontendIngressPropagatesCallIDAsTrace(t *testing.T) {
	t.Parallel()

	const traceID = "trace-corr-real"
	call := lipapi.Call{
		ID:      traceID,
		Session: lipapi.SessionRef{ALegID: "a-leg-corr"},
	}
	ctx, holder, err := captureFrontendIngressBeforeSubmit(context.Background(), call, scope.PrincipalScopeView{}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx
	if holder == nil || holder.FrontendIngress == nil {
		t.Fatal("expected FE ingress holder")
	}
	fe := holder.FrontendIngress.Public
	if fe.Correlation.RequestID != traceID || fe.Correlation.ALegID != "a-leg-corr" {
		t.Fatalf("correlation=%+v", fe.Correlation)
	}
	// Legal seam already receives call.ID (runtime trace/request identity). Task 1.4
	// must propagate it into Correlation.TraceID; today it stays empty.
	if fe.Correlation.TraceID != traceID {
		t.Fatalf("FE ingress TraceID=%q want call.ID %q (captureFrontendIngressBeforeSubmit omits TraceID)", fe.Correlation.TraceID, traceID)
	}

	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID: traceID, Session: lipapi.SessionRef{ALegID: "a-leg-corr"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
		},
		AttemptID: "b-1", BLegID: "b-1", ALegID: "a-leg-corr",
		BackendID: "backend-1", Model: "model-1",
		CheckpointID: "be", StreamID: "be-ingress:b-1",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	be := holder.BackendIngressFor("b-1").Public
	if be.Correlation.TraceID == fe.StreamID {
		t.Fatalf("backend TraceID must not reuse FE stream id %q", fe.StreamID)
	}
}
