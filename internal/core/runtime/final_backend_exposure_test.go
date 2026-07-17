package runtime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/metering/checkpoint"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type previewClampProvider struct {
	id            string
	previewCalls  atomic.Int32
	admitCalls    atomic.Int32
	releaseCalls  atomic.Int32
	clampValue    int64
	admitValue    int64
	previewHolds  bool
	previewSpend  *economics.Money
	nonconverging bool
}

func (p *previewClampProvider) PreviewAttempt(_ context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	n := p.previewCalls.Add(1)
	d := authority.Decision{Kind: authority.DecisionAllow, ProviderID: p.id}
	if p.previewHolds {
		d.Reservations = []authority.Reservation{{Handle: "bad-hold", Kind: authority.ReservationSpend}}
		return d, nil
	}
	if p.previewSpend != nil {
		d.Clamps = []authority.Clamp{{Kind: authority.ClampMaxSpend, Money: *p.previewSpend, RuleID: "spend"}}
		return d, nil
	}
	val := p.clampValue
	if p.nonconverging {
		val = p.clampValue + int64(n)
	}
	if val > 0 {
		d.Clamps = []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: val}}
	}
	_ = in
	return d, nil
}

func (p *previewClampProvider) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	p.admitCalls.Add(1)
	d := authority.Decision{
		Kind:         authority.DecisionAllow,
		ProviderID:   p.id,
		Reservations: []authority.Reservation{{Handle: "h1", Kind: authority.ReservationSpend}},
	}
	admitVal := p.admitValue
	if admitVal == 0 {
		admitVal = p.clampValue
	}
	if p.previewSpend != nil {
		d.Clamps = []authority.Clamp{{Kind: authority.ClampMaxSpend, Money: *p.previewSpend, RuleID: "spend"}}
	} else if admitVal > 0 {
		d.Clamps = []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: admitVal}}
	}
	return d, nil
}

func (p *previewClampProvider) SettleAttempt(context.Context, authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.Settlement{Kind: authority.SettlementFinal}, nil
}

func (p *previewClampProvider) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	p.releaseCalls.Add(1)
	return nil
}

func TestFinalBackendExposure_RatesFrozenIngressNotStaleDecision(t *testing.T) {
	t.Parallel()
	rater := &injectedRater{nano: 1, currency: "USD"}
	rec := &recordingMeter{}
	attProv := &recordingAttemptProvider{id: "be-rate"}
	ex := &Executor{AccountingRuntime: AccountingRuntime{EconomicsRater: rater, MeteringRecorder: rec}}
	ex.Now = func() time.Time { return time.Unix(100, 0).UTC() }
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "be-rate", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:      lipapi.Call{ID: "req-be", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}}},
		AttemptID: "b-1", BLegID: "b-1", CheckpointID: "be-cp", StreamID: "be-stream",
		Now: time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	holder.MergeBackendIngressQuantities("b-1", []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 777, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 9, Present: true},
	})
	factID, err := ex.persistBackendIngressFact(context.Background(), holder, "b-1")
	if err != nil || factID == "" {
		t.Fatalf("persist fact: id=%q err=%v", factID, err)
	}
	ctx := withMeteringHolder(context.Background(), holder)
	stale := accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 1, OutputTokens: 9, TotalTokens: 10, TotalTokensPresent: true},
	}
	_, err = ex.admitAttemptAuthority(ctx, "trace", "a-1", b2bua.BLegRecord{BLegID: "b-1", Seq: 1},
		lipapi.Call{ID: "req-be"}, routing.AttemptCandidate{Key: "k", Primary: routing.Primary{Backend: "b", Model: "m"}},
		stale, false)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	req, _ := rater.last.Load().(economics.RatingRequest)
	in, ok := checkpoint.QuantityComponentValue(req.Quantities, metering.ComponentInputToken)
	if !ok || in != 777 {
		t.Fatalf("rated input=%d ok=%v want BE 777", in, ok)
	}
	if !req.Output.Present || req.Output.TokenCount != 9 {
		t.Fatalf("conservative output=%+v", req.Output)
	}
	if len(req.FactIDs) != 1 || req.FactIDs[0] != factID {
		t.Fatalf("FactIDs=%v want bound journal fact %q", req.FactIDs, factID)
	}
	if len(rec.facts) != 1 || rec.facts[0].FactID != factID || rec.facts[0].Boundary != metering.BoundaryBackendIngress {
		t.Fatalf("recorder facts=%v", rec.facts)
	}
}

func TestFinalBackendExposure_PreviewForbidsHolds(t *testing.T) {
	t.Parallel()
	bad := &previewClampProvider{id: "preview-holds", previewHolds: true}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "preview-holds", Class: authoritycoord.AttemptPriorityHardSpend, Provider: bad, Strength: authority.StrengthRequired,
		}},
	}
	_, err := coord.PreviewClamps(context.Background(), authority.AttemptAdmission{
		RequestID: "r", AttemptID: "a", BLegID: "b",
		Perspective: metering.PerspectiveOperator,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	})
	if err == nil {
		t.Fatal("expected preview holds rejection")
	}
	if bad.admitCalls.Load() != 0 {
		t.Fatal("preview must not call AdmitAttempt")
	}
}

func TestFinalBackendExposure_PreviewAppliesTokenClamp(t *testing.T) {
	t.Parallel()
	prov := &previewClampProvider{id: "preview-clamp", clampValue: 42}
	ex := &Executor{}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "preview-clamp", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	call := lipapi.Call{ID: "req-preview"}
	clamps, ran, err := ex.previewAndApplyAttemptClamps(context.Background(), &call,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, "a-1", "b-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ran || len(clamps) != 1 || clamps[0].Value != 42 {
		t.Fatalf("clamps=%v ran=%v", clamps, ran)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 42 {
		t.Fatalf("MaxOutputTokens=%v", call.Options.MaxOutputTokens)
	}
	if prov.previewCalls.Load() < 1 || prov.previewCalls.Load() > 4 {
		t.Fatalf("preview calls=%d", prov.previewCalls.Load())
	}
	if prov.admitCalls.Load() != 0 {
		t.Fatal("preview must not admit")
	}
}

func TestFinalBackendExposure_PreviewNonConvergenceFails(t *testing.T) {
	t.Parallel()
	prov := &previewClampProvider{id: "nc", clampValue: 10, nonconverging: true}
	ex := &Executor{}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "nc", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	call := lipapi.Call{ID: "req-nc"}
	_, _, err := ex.previewAndApplyAttemptClamps(context.Background(), &call,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, "a-1", "b-1")
	if err == nil {
		t.Fatal("expected non-convergence error")
	}
	if prov.previewCalls.Load() != 4 {
		t.Fatalf("preview calls=%d want 4", prov.previewCalls.Load())
	}
}

func TestFinalBackendExposure_UnknownOutputOmittedNotPresentZero(t *testing.T) {
	t.Parallel()
	qs := attemptRatingQuantities(accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 3, OutputTokens: 0},
	})
	if quantityComponentPresent(qs, metering.ComponentOutputToken) {
		t.Fatalf("unknown output must be omitted, got %v", qs)
	}
	out := conservativeOutputAssumption(accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 3},
	}, qs)
	if out.Present {
		t.Fatalf("conservative output must not claim present zero: %+v", out)
	}
}

func TestFinalBackendExposure_PostAdmitMismatchCompensates(t *testing.T) {
	t.Parallel()
	prov := &previewClampProvider{id: "mismatch", clampValue: 40, admitValue: 99}
	rater := &injectedRater{nano: 1, currency: "USD"}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EconomicsRater = rater
	ex.Now = func() time.Time { return time.Unix(1, 0).UTC() }
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "mismatch", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 5})
	p.ctx = withMeteringHolder(p.ctx, holder)
	p.baseline = lipapi.Call{ID: "req-mismatch", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	_, err := ex.openPlannedCandidate(p, authorityCandidate(), nil, "", false)
	if err == nil {
		t.Fatal("expected unpreviewed clamp rejection")
	}
	if backend.openCalls.Load() != 0 {
		t.Fatal("Open must not run after mismatch")
	}
	if prov.releaseCalls.Load() < 1 {
		t.Fatal("expected compensation ReleaseAttempt")
	}
	if prov.admitCalls.Load() != 1 {
		t.Fatalf("admit calls=%d want 1", prov.admitCalls.Load())
	}
}

func TestFinalBackendExposure_OpenPathPreviewThenFactRateAdmitOnce(t *testing.T) {
	t.Parallel()
	prov := &previewClampProvider{id: "path", clampValue: 55}
	rater := &injectedRater{nano: 5, currency: "USD"}
	rec := &recordingMeter{}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EconomicsRater = rater
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return time.Unix(1, 0).UTC() }
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "path", Class: authoritycoord.AttemptPriorityHardSpend, Provider: prov, Strength: authority.StrengthRequired,
		}},
	}
	holder := &checkpoint.RequestHolder{}
	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 5})
	p.ctx = withMeteringHolder(p.ctx, holder)
	p.baseline = lipapi.Call{ID: "req-path", Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")}}}}
	out, err := ex.openPlannedCandidate(p, authorityCandidate(), nil, "", false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !out.opened {
		t.Fatal("expected open")
	}
	if backend.openCalls.Load() != 1 {
		t.Fatalf("open calls=%d", backend.openCalls.Load())
	}
	if prov.previewCalls.Load() < 1 {
		t.Fatal("expected preview before open")
	}
	if prov.admitCalls.Load() != 1 {
		t.Fatalf("AdmitAttempt calls=%d want 1", prov.admitCalls.Load())
	}
	if rater.calls.Load() < 1 {
		t.Fatal("expected rating")
	}
	req, _ := rater.last.Load().(economics.RatingRequest)
	if len(req.FactIDs) != 1 || !strings.HasPrefix(req.FactIDs[0], "be-ingress:") {
		t.Fatalf("FactIDs=%v", req.FactIDs)
	}
	if len(rec.facts) < 1 || rec.facts[0].Boundary != metering.BoundaryBackendIngress {
		t.Fatalf("facts=%v", rec.facts)
	}
	outQ, ok := checkpoint.QuantityComponentValue(req.Quantities, metering.ComponentOutputToken)
	if !ok || outQ != 55 {
		t.Fatalf("rated output=%d ok=%v want preview clamp 55", outQ, ok)
	}
}

func TestFinalBackendExposure_RaterAbsentMoneyFailsClosed(t *testing.T) {
	t.Parallel()
	rater := &injectedRater{nano: 0, currency: "USD"}
	rater.absentMoney = true
	ex := &Executor{AccountingRuntime: AccountingRuntime{EconomicsRater: rater}}
	ex.Now = func() time.Time { return time.Unix(1, 0).UTC() }
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "abs", Class: authoritycoord.AttemptPriorityHardSpend,
			Provider: &recordingAttemptProvider{id: "abs"}, Strength: authority.StrengthRequired,
		}},
	}
	_, err := ex.admitAttemptAuthority(context.Background(), "t", "a", b2bua.BLegRecord{BLegID: "b"},
		lipapi.Call{ID: "r"}, routing.AttemptCandidate{Key: "k", Primary: routing.Primary{Backend: "b", Model: "m"}},
		accountingpreflight.Decision{Count: accountingapp.CountResult{InputTokens: 1, OutputTokens: 1, TotalTokens: 2, TotalTokensPresent: true}},
		false)
	if err == nil {
		t.Fatal("absent money must fail closed before Open")
	}
}

func TestFinalBackendExposure_ExactClampMatchNoPostAdmitMutate(t *testing.T) {
	t.Parallel()
	previewed := []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 20}}
	state := attemptAuthorityState{
		admitClamps: []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: 20}},
	}
	frozen := lipapi.Call{ID: "r"}
	max := 20
	frozen.Options.MaxOutputTokens = &max
	live := lipapi.CloneCall(frozen)
	ex := &Executor{}
	if err := ex.enforcePostAdmitClamps(context.Background(), &live, frozen, previewed, true, state,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, 1); err != nil {
		t.Fatal(err)
	}
	if !maxOutputEqual(frozen, live) {
		t.Fatal("exact match must not mutate call")
	}
	state.admitClamps[0].Value = 7
	if err := ex.enforcePostAdmitClamps(context.Background(), &live, frozen, previewed, true, state,
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, 1); err == nil {
		t.Fatal("mismatch must reject")
	}
}
