package runtime

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

// TestTurnRecvDomainReplacementPreservesProviderEvidenceAndObserver exercises
// the real Recv replacement path. The first B-leg publishes provider evidence
// only through the usage sideband and is swallowed; the replacement publishes
// client-visible output. Durable evidence must remain associated with each
// B-leg, while the observer must see only the released winner trajectory.
func TestTurnRecvDomainReplacementPreservesProviderEvidenceAndObserver(t *testing.T) {
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var legs []billing.CallLegUsageRecord
	var calls []billing.CallUsageRecord
	observerFactory := &emitTestObserverFactory{}

	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{
			AccountID:       "acct",
			CallID:          in.CallID,
			PricingRef:      billing.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: billing.VersionRef{ID: "policy:test", Version: "1"},
			Status:          billing.ExposureOpen,
		}, nil
	})
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(_ context.Context, leg billing.CallLegUsageRecord) error {
			mu.Lock()
			legs = append(legs, leg)
			mu.Unlock()
			return nil
		},
		appendCall: func(_ context.Context, call billing.CallUsageRecord) error {
			mu.Lock()
			calls = append(calls, call)
			mu.Unlock()
			return nil
		},
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{observerFactory},
		}),
	})

	failedEvidence := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   17,
		OutputTokens:  4,
		TotalTokens:   21,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
			DedupeKey: "failed-sideband",
		},
	}
	winnerEvidence := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		InputTokens:   31,
		OutputTokens:  7,
		TotalTokens:   38,
		UsagePresence: lipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
			DedupeKey: "winner-canonical",
		},
	}
	var opens int
	ex.Backends = map[string]execbackend.Backend{
		"first": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens++
				return &turnRecvSidebandFailureStream{evidence: failedEvidence}, nil
			},
		},
		"second": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens++
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "winner"},
					winnerEvidence,
					{Kind: lipapi.EventResponseFinished, FinishReason: "stop"},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "session-domain", ContinuityKey: "session-domain"},
		Route:   lipapi.RouteIntent{Selector: "first:model|second:model"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hello")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := billingCallIDFromStream(stream)
	if !ok {
		t.Fatal("replacement stream did not retain BillingCallID")
	}
	events := make([]lipapi.Event, 0, 8)
	for {
		ev, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv: %v", recvErr)
		}
		events = append(events, ev)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if opens != 2 {
		t.Fatalf("backend opens = %d, want swallowed first leg plus replacement", opens)
	}
	if len(events) == 0 || !containsTurnRecvText(events, "winner") {
		t.Fatalf("released events = %+v, want winner text", events)
	}
	if !turnRecvHasKind(events, lipapi.EventResponseFinished) {
		t.Fatalf("released events = %+v, want response_finished", events)
	}

	aLegID := call.Session.ALegID
	attempts, err := store.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want swallowed first plus winner replacement", len(attempts))
	}
	if attempts[0].Outcome != lipapi.AttemptSwallowedFailure || attempts[1].Outcome != lipapi.AttemptSuccess {
		t.Fatalf("attempt outcomes = %q, %q", attempts[0].Outcome, attempts[1].Outcome)
	}

	mu.Lock()
	gotLegs := append([]billing.CallLegUsageRecord(nil), legs...)
	gotCalls := append([]billing.CallUsageRecord(nil), calls...)
	mu.Unlock()
	if len(gotLegs) != 2 {
		t.Fatalf("billing legs = %d, want one record per B-leg", len(gotLegs))
	}
	if len(gotCalls) != 1 {
		t.Fatalf("billing call closures = %d, want one logical invocation closure", len(gotCalls))
	}
	if gotCalls[0].CallID != id {
		t.Fatalf("call closure BillingCallID = %q, want %q", gotCalls[0].CallID, id)
	}
	if gotCalls[0].ALegID != aLegID {
		t.Fatalf("call closure A-leg = %q, want %q", gotCalls[0].ALegID, aLegID)
	}
	expectedBLegIDs := []string{attempts[0].BLegID, attempts[1].BLegID}
	sort.Strings(expectedBLegIDs)
	if len(gotCalls[0].ExpectedBLegIDs) != 2 || gotCalls[0].ExpectedBLegIDs[0] != expectedBLegIDs[0] || gotCalls[0].ExpectedBLegIDs[1] != expectedBLegIDs[1] {
		t.Fatalf("call closure B-leg set = %q, want canonical order %q", gotCalls[0].ExpectedBLegIDs, expectedBLegIDs)
	}

	for i, want := range []struct {
		attempt  lipapi.AttemptRecord
		backend  string
		input    int
		output   int
		total    int
		key      string
		outcome  billing.LegOutcome
		surfaced billing.SurfacedState
	}{
		{attempt: attempts[0], backend: "first", input: 17, output: 4, total: 21, key: "failed-sideband", outcome: billing.LegOutcomeSwallowed, surfaced: billing.SurfacedNo},
		{attempt: attempts[1], backend: "second", input: 31, output: 7, total: 38, key: "winner-canonical", outcome: billing.LegOutcomeWinner, surfaced: billing.SurfacedYes},
	} {
		leg := gotLegs[i]
		if leg.CallID != id || leg.ALegID != aLegID || leg.BLegID != want.attempt.BLegID || leg.AttemptSeq != want.attempt.Seq {
			t.Fatalf("leg[%d] lineage = %+v, want call=%q A=%q B=%q seq=%d", i, leg, id, aLegID, want.attempt.BLegID, want.attempt.Seq)
		}
		if leg.BackendID != want.backend || leg.ProviderID != want.backend || leg.ModelID != "model" {
			t.Fatalf("leg[%d] provider attribution = %+v", i, leg)
		}
		if leg.Outcome != want.outcome || leg.Surfaced != want.surfaced {
			t.Fatalf("leg[%d] terminal state = %q/%q", i, leg.Outcome, leg.Surfaced)
		}
		if got := leg.Evidence.InputTokens; !got.Present || got.Value != int64(want.input) {
			t.Fatalf("leg[%d] input evidence = %+v", i, got)
		}
		if got := leg.Evidence.OutputTokens; !got.Present || got.Value != int64(want.output) {
			t.Fatalf("leg[%d] output evidence = %+v", i, got)
		}
		if got := leg.Evidence.TotalTokens; !got.Present || got.Value != int64(want.total) {
			t.Fatalf("leg[%d] total evidence = %+v", i, got)
		}
		if leg.Evidence.DedupeKey != want.key || leg.Evidence.Source != billing.EvidenceSourceProviderReported || leg.Evidence.Authority != billing.EvidenceAuthorityAuthoritative {
			t.Fatalf("leg[%d] evidence identity = %+v", i, leg.Evidence)
		}
	}

	observers := observerFactory.snapshot()
	if len(observers) != 2 {
		t.Fatalf("observer sessions = %d, want one opened session per B-leg", len(observers))
	}
	last := observers[len(observers)-1]
	if last.finishN.Load() != 1 {
		t.Fatalf("winner observer finishes = %d, want one", last.finishN.Load())
	}
	last.mu.Lock()
	observed := append([]lipapi.Event(nil), last.events...)
	last.mu.Unlock()
	if !containsTurnRecvText(observed, "winner") || !turnRecvHasKind(observed, lipapi.EventResponseFinished) {
		t.Fatalf("winner observer events = %+v", observed)
	}
}

type turnRecvSidebandFailureStream struct {
	evidence lipapi.Event
	drained  bool
}

func (s *turnRecvSidebandFailureStream) Recv(context.Context) (lipapi.Event, error) {
	return lipapi.Event{}, &lipapi.UpstreamFailureError{Phase: lipapi.PhasePreOutput, Recoverable: true, Reason: "swallowed sideband attempt"}
}

func (s *turnRecvSidebandFailureStream) Close() error { return nil }

func (s *turnRecvSidebandFailureStream) Cancel(context.Context, lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
}

func (s *turnRecvSidebandFailureStream) DrainUsageEvidence() []lipapi.Event {
	if s.drained {
		return nil
	}
	s.drained = true
	return []lipapi.Event{s.evidence}
}

func containsTurnRecvText(events []lipapi.Event, text string) bool {
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta && ev.Delta == text {
			return true
		}
	}
	return false
}

func turnRecvHasKind(events []lipapi.Event, kind lipapi.EventKind) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}
