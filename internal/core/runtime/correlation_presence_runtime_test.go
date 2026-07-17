package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestRuntimeCorrelation_FEAndBEIngressEgressShareIdentities(t *testing.T) {
	t.Parallel()

	const (
		traceID    = "trace-runtime-corr"
		aLeg       = "a-leg-corr"
		bLeg       = "b-leg-corr"
		frontendID = "openai-responses"
		backendID  = "backend-corr"
		modelID    = "model-corr"
		feStream   = "fe-ingress:" + traceID
	)

	call := lipapi.Call{
		ID:      traceID,
		Session: lipapi.SessionRef{ALegID: aLeg},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("original-prompt")},
		}},
	}
	ctx := execview.WithFrontendID(context.Background(), frontendID)
	ctx, holder, err := captureFrontendIngressBeforeSubmit(ctx, call, scope.PrincipalScopeView{}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	feIn := holder.FrontendIngress.Public
	if feIn.Correlation.TraceID != traceID || feIn.Correlation.RequestID != traceID ||
		feIn.Correlation.ALegID != aLeg || feIn.FrontendID != frontendID {
		t.Fatalf("FE ingress correlation=%+v frontend=%q", feIn.Correlation, feIn.FrontendID)
	}
	if feIn.StreamID != feStream || feIn.Correlation.TraceID == feIn.StreamID {
		// StreamID is "fe-ingress:<id>" while TraceID is bare call.ID — must stay distinct.
		if feIn.Correlation.TraceID == feIn.StreamID {
			t.Fatalf("TraceID must not equal StreamID: %q", feIn.StreamID)
		}
	}

	// Hook/route mutation after immutable capture must not rewrite FE ingress evidence.
	call.Messages[0].Parts[0].Text = "mutated-after-capture"
	call.Session.ALegID = "mutated-aleg"
	if holder.FrontendIngress.Call.Messages[0].Parts[0].Text != "original-prompt" {
		t.Fatal("FE ingress call clone rewritten by later mutation")
	}
	if holder.FrontendIngress.Public.Correlation.ALegID != aLeg {
		t.Fatal("FE ingress correlation rewritten by later mutation")
	}

	holder.FrontendIngress.BindScope(scope.PrincipalScopeView{})
	if holder.FrontendIngress.Call.Messages[0].Parts[0].Text != "original-prompt" {
		t.Fatal("BindScope must not mutate frozen call")
	}

	_, err = holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call: lipapi.Call{
			ID: traceID, Session: lipapi.SessionRef{ALegID: aLeg},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
		},
		AttemptID: bLeg, BLegID: bLeg, ALegID: aLeg,
		BackendID: backendID, Model: modelID,
		CheckpointID: "be-ingress:" + bLeg,
		StreamID:     "be-ingress:" + bLeg,
		TraceID:      traceID,
		Now:          time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	beIn := holder.BackendIngressFor(bLeg).Public
	if beIn.Correlation.TraceID != traceID || beIn.Correlation.RequestID != traceID ||
		beIn.Correlation.ALegID != aLeg || beIn.Correlation.BLegID != bLeg ||
		beIn.Correlation.AttemptID != bLeg || beIn.BackendID != backendID || beIn.Model != modelID {
		t.Fatalf("BE ingress correlation incomplete: %+v backend=%s model=%s", beIn.Correlation, beIn.BackendID, beIn.Model)
	}
	if beIn.Correlation.TraceID == feIn.StreamID {
		t.Fatalf("BE TraceID reused FE StreamID %q", feIn.StreamID)
	}

	feEgress, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.FrontendEgressCheckpoint(*holder.FrontendIngress),
		FactID:     "fe-egress:" + traceID + ":1",
		Sequence:   1,
		Quantities: []metering.Quantity{
			{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 0, Present: true},
		},
		Money: nil,
		Now:   time.Unix(3, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if feEgress.Correlation.TraceID != traceID || feEgress.Correlation.RequestID != traceID ||
		feEgress.Correlation.ALegID != aLeg || feEgress.FrontendID != frontendID {
		t.Fatalf("FE egress correlation=%+v frontend=%q", feEgress.Correlation, feEgress.FrontendID)
	}
	if feEgress.Money != nil {
		t.Fatalf("customer FE egress must omit provider money: %+v", feEgress.Money)
	}

	beEgress, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.BackendEgressCheckpoint(*holder.BackendIngressFor(bLeg), metering.AttemptOutcomeWinner, metering.SurfacedYes),
		FactID:     "be-egress:" + bLeg + ":2",
		Sequence:   2,
		Quantities: []metering.Quantity{
			{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 0, Present: true},
		},
		Money: &metering.MoneyObservation{NanoUnits: 0, Currency: "USD", Present: true, Source: metering.SourceProviderReported},
		Now:   time.Unix(4, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if beEgress.Correlation.TraceID != traceID || beEgress.Correlation.RequestID != traceID ||
		beEgress.Correlation.ALegID != aLeg || beEgress.Correlation.BLegID != bLeg ||
		beEgress.Correlation.AttemptID != bLeg || beEgress.BackendID != backendID || beEgress.Model != modelID {
		t.Fatalf("BE egress correlation incomplete: %+v", beEgress.Correlation)
	}
	if beEgress.Money == nil || !beEgress.Money.Present || beEgress.Money.NanoUnits != 0 {
		t.Fatalf("operator explicit zero cost not retained: %+v", beEgress.Money)
	}

	_ = ctx
}
func TestMoneyFromUsageEvent_ExplicitPresenceOnly(t *testing.T) {
	t.Parallel()
	if moneyFromUsageEvent(lipapi.Event{CostNanoUnits: 99, Currency: "USD", CostSource: "provider_reported"}) != nil {
		t.Fatal("nonzero cost without CostPresent must stay absent")
	}
	got := moneyFromUsageEvent(lipapi.Event{CostPresent: true, CostNanoUnits: 0, Currency: "EUR"})
	if got == nil || !got.Present || got.NanoUnits != 0 || got.Currency != "EUR" {
		t.Fatalf("authoritative zero lost: %+v", got)
	}
}

func TestQuantitiesFromUsageEvent_EmptyUnmarkedUsageDeltaOmits(t *testing.T) {
	t.Parallel()
	qs := quantitiesFromUsageEvent(lipapi.Event{Kind: lipapi.EventUsageDelta})
	if len(qs) != 0 {
		t.Fatalf("all-zero unmarked UsageDelta must omit quantities; got %+v", qs)
	}
}

func TestQuantitiesFromUsageEvent_LegacyUnmarkedNonzeroOnly(t *testing.T) {
	t.Parallel()
	qs := quantitiesFromUsageEvent(lipapi.Event{
		Kind:            lipapi.EventUsageDelta,
		InputTokens:     3,
		OutputTokens:    0,
		CacheReadTokens: 0,
		ReasoningTokens: 0,
		TotalTokens:     0,
	})
	if len(qs) != 2 {
		t.Fatalf("want input + derived total only; got %+v", qs)
	}
	in, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentInputToken)
	if !ok || in != 3 {
		t.Fatalf("input=%d ok=%v", in, ok)
	}
	if _, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentCacheReadInputToken); ok {
		t.Fatal("must not invent present cache_read zero")
	}
	if _, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentReasoningOutputToken); ok {
		t.Fatal("must not invent present reasoning zero")
	}
	total, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentTotalToken)
	if !ok || total != 3 {
		t.Fatalf("derived total=%d ok=%v", total, ok)
	}
}

func TestQuantitiesFromUsageEvent_ExplicitZeroPresenceRetained(t *testing.T) {
	t.Parallel()
	qs := quantitiesFromUsageEvent(lipapi.Event{
		Kind: lipapi.EventUsageDelta,
		UsagePresence: lipapi.UsagePresence{
			InputTokens: true, OutputTokens: true, TotalTokens: true,
		},
		InputTokens: 0, OutputTokens: 0, TotalTokens: 0,
	})
	in, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentInputToken)
	if !ok || in != 0 {
		t.Fatalf("authoritative zero input lost: ok=%v v=%d", ok, in)
	}
	out, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentOutputToken)
	if !ok || out != 0 {
		t.Fatalf("authoritative zero output lost: ok=%v v=%d", ok, out)
	}
	if _, ok := checkpoint.QuantityComponentValue(qs, metering.ComponentCacheReadInputToken); ok {
		t.Fatal("absent cache_read must stay omitted")
	}
}

func TestEmitFrontendEgress_ProviderOnlyShellOmitsQuantities(t *testing.T) {
	t.Parallel()
	const traceID = "trace-provider-only-shell"
	call := lipapi.Call{ID: traceID, Session: lipapi.SessionRef{ALegID: "a-1"}}
	ctx, holder, err := captureFrontendIngressBeforeSubmit(context.Background(), call, scope.PrincipalScopeView{}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingMeter{}
	e := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	e.Now = func() time.Time { return time.Unix(2, 0).UTC() }
	fact, ok := e.emitFrontendEgressMeteringFact(ctx, traceID, lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   99,
		OutputTokens:  7,
		CostPresent:   true,
		CostNanoUnits: 50,
		Currency:      "USD",
		Accounting:    lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable},
	})
	if !ok {
		t.Fatal("expected FE egress fact")
	}
	_ = holder
	if fact.Money != nil {
		t.Fatalf("customer FE must omit provider money: %+v", fact.Money)
	}
	if len(fact.Quantities) != 0 {
		t.Fatalf("provider-only shell must omit quantities; got %+v", fact.Quantities)
	}
	if len(rec.facts) != 1 {
		t.Fatalf("recorder facts=%d", len(rec.facts))
	}
}
