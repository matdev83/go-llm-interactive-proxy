package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// Phase 7.1: cross-protocol economic conformance — FE ingress/egress customer
// semantics must be identical across bundled frontends for every legal mode
// (requirements 1.2, 1.3, 5.1, 5.4–5.7, 13.2, 13.9; design D2, D13, D14).
// Frontend IDs mirror conformance.BundledFrontendIDs (cannot import that package:
// conformance → runtime import cycle).

var phase71BundledFrontends = []string{
	"openai-responses",
	"openai-legacy",
	"anthropic",
	"gemini",
}

type phase71EconomicMode string

const (
	phase71ModeStream          phase71EconomicMode = "stream"
	phase71ModeNonStream       phase71EconomicMode = "nonstream"
	phase71ModeProtocolError   phase71EconomicMode = "protocol_error"
	phase71ModeCancel          phase71EconomicMode = "cancel"
	phase71ModeEncodingFailure phase71EconomicMode = "encoding_failure"
)

func phase71EconomicModes() []phase71EconomicMode {
	return []phase71EconomicMode{
		phase71ModeStream,
		phase71ModeNonStream,
		phase71ModeProtocolError,
		phase71ModeCancel,
		phase71ModeEncodingFailure,
	}
}

func TestPhase71_DualPlaneEconomicModesCoverRequiredPaths(t *testing.T) {
	t.Parallel()
	modes := phase71EconomicModes()
	want := map[phase71EconomicMode]bool{
		phase71ModeStream:          false,
		phase71ModeNonStream:       false,
		phase71ModeProtocolError:   false,
		phase71ModeCancel:          false,
		phase71ModeEncodingFailure: false,
	}
	if len(modes) != len(want) {
		t.Fatalf("modes=%d want %d", len(modes), len(want))
	}
	for _, m := range modes {
		if _, ok := want[m]; !ok {
			t.Fatalf("unexpected mode %q", m)
		}
		want[m] = true
	}
	for m, seen := range want {
		if !seen {
			t.Fatalf("missing mode %q", m)
		}
	}
}

func TestPhase71_FrontendIngressQuantitiesIdenticalAcrossFrontends(t *testing.T) {
	t.Parallel()
	call := lipapi.Call{
		ID:      "req-phase71-ingress",
		Session: lipapi.SessionRef{ALegID: "a-phase71"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("phase71 shared canonical body")},
		}},
	}
	type row struct {
		fe string
		qs []metering.Quantity
	}
	rows := make([]row, 0, len(phase71BundledFrontends))
	for _, fe := range phase71BundledFrontends {
		ctx := execview.WithFrontendID(context.Background(), fe)
		_, holder, err := captureFrontendIngressBeforeSubmit(
			ctx, call, scope.PrincipalScopeView{}, time.Unix(71, 0).UTC(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if holder.FrontendIngress == nil {
			t.Fatal("expected FE ingress")
		}
		pub := holder.FrontendIngress.Public
		if pub.Boundary != metering.BoundaryFrontendIngress {
			t.Fatalf("%s boundary=%q", fe, pub.Boundary)
		}
		if pub.FrontendID != fe {
			t.Fatalf("FrontendID=%q want %q (attribution only)", pub.FrontendID, fe)
		}
		if pub.Perspective != metering.PerspectiveCustomer {
			t.Fatalf("%s perspective=%q", fe, pub.Perspective)
		}
		rows = append(rows, row{fe: fe, qs: append([]metering.Quantity(nil), pub.Quantities...)})
	}
	baseline := rows[0].qs
	for _, r := range rows[1:] {
		if !quantitiesEqual(r.qs, baseline) {
			t.Fatalf("FE ingress quantities differ for frontend %q: got %#v want %#v", r.fe, r.qs, baseline)
		}
	}
}

func TestPhase71_FrontendEgressCustomerSemanticsAcrossModesAndFrontends(t *testing.T) {
	t.Parallel()

	type modeCase struct {
		mode     phase71EconomicMode
		cmd      sdkterminal.Command
		released bool
	}
	modes := []modeCase{
		{mode: phase71ModeStream, cmd: sdkterminal.CommandNormalFinish, released: true},
		{mode: phase71ModeNonStream, cmd: sdkterminal.CommandNormalFinish, released: true},
		{mode: phase71ModeProtocolError, cmd: sdkterminal.CommandPartialError, released: true},
		{mode: phase71ModeCancel, cmd: sdkterminal.CommandCancel, released: true},
		{mode: phase71ModeEncodingFailure, cmd: sdkterminal.CommandFrontendEncoderFailure, released: true},
	}

	const wantIn, wantOut = int64(40), int64(12)
	for _, mc := range modes {
		for _, fe := range phase71BundledFrontends {
			name := string(mc.mode) + "__" + fe
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				in, out, fact := phase71SettleCustomerFE(t, fe, mc.released, wantIn, wantOut)
				if fact.Boundary != metering.BoundaryFrontendEgress {
					t.Fatalf("boundary=%q want frontend_egress", fact.Boundary)
				}
				if fact.Perspective != metering.PerspectiveCustomer {
					t.Fatalf("perspective=%q", fact.Perspective)
				}
				if fact.FrontendID != fe {
					t.Fatalf("FrontendID=%q want %q", fact.FrontendID, fe)
				}
				if fact.Money != nil {
					t.Fatalf("customer FE egress must not carry money; got %+v", fact.Money)
				}
				if in != wantIn || out != wantOut {
					t.Fatalf("customer quantities in=%d out=%d want %d/%d (provider must not win; mode=%s)",
						in, out, wantIn, wantOut, mc.mode)
				}
				_ = mc.cmd
			})
		}
	}
}

func TestPhase71_DualPlaneEconomicCellsCoverAllFrontends(t *testing.T) {
	t.Parallel()
	want := len(phase71BundledFrontends) * len(phase71EconomicModes())
	seen := map[string]bool{}
	for _, fe := range phase71BundledFrontends {
		for _, m := range phase71EconomicModes() {
			key := fe + "/" + string(m)
			if seen[key] {
				t.Fatalf("duplicate cell %q", key)
			}
			seen[key] = true
		}
	}
	if len(seen) != want {
		t.Fatalf("cells=%d want %d", len(seen), want)
	}
}

func phase71SettleCustomerFE(t *testing.T, frontendID string, released bool, wantIn, wantOut int64) (in, out int64, fact metering.Fact) {
	t.Helper()
	prov := &settleRecordingRequestProvider{id: "phase71-" + frontendID}
	rec := &recordingMeter{}
	callCount, outCount := clientVisibleCount(int(wantIn), int(wantOut))
	ex := &Executor{AccountingRuntime: AccountingRuntime{MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(7100, 0).UTC() }
	ex.StreamUsage = accountingstream.New(&stubStreamCounter{call: callCount, output: outCount}, accountingstream.Config{})
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "phase71-" + frontendID, Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}

	reqID := "req-phase71-" + frontendID
	call := lipapi.Call{
		ID:      reqID,
		Session: lipapi.SessionRef{ALegID: "a-" + frontendID},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("phase71 shared canonical body")},
		}},
	}
	ctx := execview.WithFrontendID(context.Background(), frontendID)
	ctx, holder, err := captureFrontendIngressBeforeSubmit(ctx, call, scope.PrincipalScopeView{}, time.Unix(71, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = ex.admitRequestAuthorityOnce(ctx, reqID, "a-"+frontendID, "trace-"+frontendID, scope.PrincipalScopeView{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	_ = holder

	provider := lipapi.ScopedUsageDelta{
		InputTokens: 200, OutputTokens: 80, TotalTokens: 280,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	authorityEv := authorityUsageEvent([]lipapi.Event{{
		Kind: lipapi.EventUsageDelta, UsageScopes: []lipapi.ScopedUsageDelta{provider},
	}})

	stream := &retryRecvStream{
		executor: ex, facts: testRecvTurnFacts(recvTurnFacts{
			traceID:  "trace-" + frontendID,
			baseline: call,
		}),
		responsePipeline: &responsePipeline{customer: newCustomerEvidenceAccumulator()},
	}
	if released {
		stream.customer.ObserveReleased(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "cust-delivered"})
	}
	stream.bindResponsePipeline()
	if err := stream.settleRequestAuthorityWithFrontendEgress(ctx, authorityEv); err != nil {
		t.Fatalf("settle: %v", err)
	}

	var fe *metering.Fact
	for i := range rec.facts {
		if rec.facts[i].Boundary == metering.BoundaryFrontendEgress {
			fe = &rec.facts[i]
			break
		}
	}
	if fe == nil {
		t.Fatal("expected FE egress fact")
	}
	in, _ = checkpoint.QuantityComponentValue(fe.Quantities, metering.ComponentInputToken)
	out, _ = checkpoint.QuantityComponentValue(fe.Quantities, metering.ComponentOutputToken)
	return in, out, *fe
}

func quantitiesEqual(a, b []metering.Quantity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Component != b[i].Component || a[i].Value != b[i].Value || a[i].Unit != b[i].Unit {
			return false
		}
	}
	return true
}
