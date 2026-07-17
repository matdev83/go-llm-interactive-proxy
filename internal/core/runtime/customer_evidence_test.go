package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type capturingStreamCounter struct {
	call         accountingapp.CountResult
	output       accountingapp.CountResult
	lastText     string
	lastEventN   int
	outputCalls  int
}

func (c *capturingStreamCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return c.call, nil
}

func (c *capturingStreamCounter) CountOutput(_ context.Context, in accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	c.outputCalls++
	c.lastText = in.Text
	c.lastEventN = len(in.Events)
	return c.output, nil
}

func clientVisibleCount(in, out int) (accountingapp.CountResult, accountingapp.CountResult) {
	meta := lipapi.UsageAccountingMetadata{
		Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated,
	}
	return accountingapp.CountResult{InputTokens: in, TotalTokens: in, Accounting: meta},
		accountingapp.CountResult{OutputTokens: out, TotalTokens: out, Accounting: meta}
}

func TestCustomerEvidenceAccumulator_ObservesReleasedContentOnly(t *testing.T) {
	t.Parallel()

	acc := newCustomerEvidenceAccumulator()
	acc.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "hello"})
	acc.ObserveReleased(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "think"})
	acc.ObserveReleased(lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, Delta: "{\"a\":1}"})
	acc.ObserveReleased(lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 9, OutputTokens: 9})
	acc.ObserveReleased(lipapi.Event{Kind: lipapi.EventWarning, WarningCode: "keepalive"})
	acc.ObserveReleased(lipapi.Event{Kind: lipapi.EventResponseFinished})

	text, reasoning, toolArgs, events := acc.Snapshot()
	if text != "hello" || reasoning != "think" || toolArgs != "{\"a\":1}" || events != 3 {
		t.Fatalf("snapshot text=%q reasoning=%q tool=%q events=%d", text, reasoning, toolArgs, events)
	}
	if got := len(acc.contentEvents()); got != 3 {
		t.Fatalf("contentEvents=%d want 3", got)
	}
}

func TestCustomerEvidenceAccumulator_MarkSettledOnce(t *testing.T) {
	t.Parallel()
	acc := newCustomerEvidenceAccumulator()
	if !acc.MarkSettled() {
		t.Fatal("first settle must succeed")
	}
	if acc.MarkSettled() {
		t.Fatal("second settle must be rejected")
	}
}

func TestCustomerPlaneUsageEvent_DropsProviderScopesAndMoney(t *testing.T) {
	t.Parallel()

	ev := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		CostNanoUnits: 99,
		Currency:      "USD",
		CostPresent:   true,
		CostSource:    string(lipapi.UsageSourceProviderReported),
		UsageScopes: []lipapi.ScopedUsageDelta{
			{
				InputTokens: 40, OutputTokens: 12, TotalTokens: 52,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalTokenizer, Authority: lipapi.UsageAuthorityEstimated,
				},
			},
			{
				InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
				Accounting: lipapi.UsageAccountingMetadata{
					Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
				},
			},
		},
	}
	got := customerPlaneUsageEvent(ev)
	if got.InputTokens != 40 || got.OutputTokens != 12 {
		t.Fatalf("customer plane tokens in=%d out=%d", got.InputTokens, got.OutputTokens)
	}
	if got.CostPresent || got.CostNanoUnits != 0 || got.Currency != "" {
		t.Fatalf("customer plane must strip money; got %+v", got)
	}
}

func TestCustomerSettlement_AccumulatorDrivesOutputWhenSettleInputIsProviderAuthority(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "cust-acc-auth"}
	rec := &recordingMeter{}
	call, output := clientVisibleCount(40, 12)
	counter := &capturingStreamCounter{call: call, output: output}
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.StreamUsage = accountingstream.New(counter, accountingstream.Config{})
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "cust-acc-auth", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{ID: "req-acc-auth"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-acc-auth", "a-1", "trace-acc-auth", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	provider := lipapi.ScopedUsageDelta{
		InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	raw := lipapi.Event{Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider}}
	authorityEv := authorityUsageEvent([]lipapi.Event{raw})
	if authorityEv.InputTokens != provider.InputTokens {
		t.Fatalf("precondition: authority input=%d", authorityEv.InputTokens)
	}

	stream := &retryRecvStream{
		executor: ex, traceID: "trace-acc-auth",
		customer: newCustomerEvidenceAccumulator(),
		baseline: lipapi.Call{ID: "req-acc-auth"},
	}
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "released-body"})
	stream.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv)

	if counter.outputCalls < 1 || counter.lastText != "released-body" {
		t.Fatalf("CountOutput must see accumulator text; calls=%d text=%q", counter.outputCalls, counter.lastText)
	}
	facts, _ := prov.lastFacts.Load().([]metering.Fact)
	if len(facts) != 1 {
		t.Fatalf("facts=%d", len(facts))
	}
	in, _ := checkpoint.QuantityComponentValue(facts[0].Quantities, metering.ComponentInputToken)
	out, _ := checkpoint.QuantityComponentValue(facts[0].Quantities, metering.ComponentOutputToken)
	if in != 40 || out != 12 {
		t.Fatalf("customer quantities in=%d out=%d want 40/12 from StreamUsage (not authority %d/%d)",
			in, out, authorityEv.InputTokens, authorityEv.OutputTokens)
	}
}

func TestCustomerSettlement_CountOutputUsesTextNotReasoningBuffer(t *testing.T) {
	t.Parallel()

	call, output := clientVisibleCount(1, 2)
	counter := &capturingStreamCounter{call: call, output: output}
	ex := &Executor{}
	ex.StreamUsage = accountingstream.New(counter, accountingstream.Config{})
	stream := &retryRecvStream{executor: ex, customer: newCustomerEvidenceAccumulator(), baseline: lipapi.Call{ID: "c"}}
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "ab"})
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "REASONING-NOT-IN-TEXT"})
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventToolCallArgsDelta, Delta: "{\"x\":1}"})

	ev := stream.customerUsageFromReleased(context.Background())
	if counter.lastText != "ab" {
		t.Fatalf("OutputText must be released text only; got %q", counter.lastText)
	}
	if counter.lastEventN != 3 {
		t.Fatalf("CountOutput Events must include text+reasoning+tool; got %d", counter.lastEventN)
	}
	if ev.OutputTokens != 2 {
		t.Fatalf("output=%d want stub CountOutput 2", ev.OutputTokens)
	}
}

func TestCustomerSettlement_UsesAccumulatorOrderingAndOnceOnly(t *testing.T) {
	t.Parallel()

	prov := &settleRecordingRequestProvider{id: "cust-acc-once"}
	rec := &recordingMeter{}
	call, output := clientVisibleCount(2, 4)
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: call, output: output}, accountingstream.Config{})
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "cust-acc-once", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: lipapi.Call{ID: "req-acc"}, CheckpointID: "fe", StreamID: "fe-stream", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	ctx, err = ex.admitRequestAuthorityOnce(ctx, "req-acc", "a-1", "trace-acc", scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	stream := &retryRecvStream{executor: ex, traceID: "trace-acc", customer: newCustomerEvidenceAccumulator(), baseline: lipapi.Call{ID: "req-acc"}}
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "ab"})
	stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "cd"})

	authorityEv := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 99, OutputTokens: 99, TotalTokens: 198,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	stream.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv)
	stream.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv)

	if prov.settleCalls.Load() != 1 {
		t.Fatalf("SettleRequest calls=%d want 1", prov.settleCalls.Load())
	}
	text, _, _, events := stream.customer.Snapshot()
	if text != "abcd" || events != 2 {
		t.Fatalf("accumulator text=%q events=%d", text, events)
	}
	var fe *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryFrontendEgress {
			fe = &rec.facts[i]
		}
	}
	if fe == nil || fe.Money != nil {
		t.Fatalf("FE fact=%+v", fe)
	}
	out, ok := checkpoint.QuantityComponentValue(fe.Quantities, metering.ComponentOutputToken)
	if !ok || out != 4 {
		t.Fatalf("customer output=%d ok=%v", out, ok)
	}
}

func TestRememberClientEvent_ObservesGateReplacementPath(t *testing.T) {
	t.Parallel()

	stream := &retryRecvStream{customer: newCustomerEvidenceAccumulator()}
	// Mirrors handleGatedPath / popGateDrainHead: observe only the event actually returned.
	stream.rememberClientEvent(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "gated-out"})
	stream.rememberClientEvent(lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 1})
	text, _, _, n := stream.customer.Snapshot()
	if text != "gated-out" || n != 1 {
		t.Fatalf("gated release text=%q n=%d", text, n)
	}
	if stream.releasedOutputText() != "gated-out" {
		t.Fatalf("releasedOutputText=%q", stream.releasedOutputText())
	}
}
