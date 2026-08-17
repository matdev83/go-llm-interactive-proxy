package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
	"sync"
)

func TestCallClosureTimesUsesTerminalLegSpan(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	started, finished := callClosureTimes([]billing.LegUsageRecord{
		{StartedAt: time.Unix(100, 0).UTC(), FinishedAt: time.Unix(150, 0).UTC()},
		{StartedAt: time.Unix(120, 0).UTC(), FinishedAt: time.Unix(180, 0).UTC()},
	}, now)
	if !started.Equal(time.Unix(100, 0).UTC()) || !finished.Equal(time.Unix(180, 0).UTC()) {
		t.Fatalf("closure span = %s..%s, want 100..180", started, finished)
	}
}

func TestCallUsageAppenderFreezesAllocatedBLegsAtRequestTerminal(t *testing.T) {
	var got []billing.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		CallUsageAppender: billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
			got = append(got, record)
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		executor:      executor,
		aLegID:        "a-shared",
		billingCallID: callID,
		baseline:      lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-shared"}},
		bleg:          b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-shared", Seq: 1},
		cand:          routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	}
	stampStreamIdentity(stream)
	state := newBillingCallState(callID)
	stream.billingCallState = state
	state.noteAllocatedBLeg("b-2", 2)
	state.noteAllocatedBLeg("b-1", 1)
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(got) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(got))
	}
	if got[0].Key != callID.String() {
		t.Fatalf("call-closure key = %q, want BillingCallID", got[0].Key)
	}
	if strings.Contains(got[0].Key, "a-shared") || strings.Contains(got[0].Key, "sess-shared") || strings.Contains(got[0].Key, "acct") {
		t.Fatal("A-leg/session/account are correlation, not the durable key")
	}
	if got[0].ALegID != "a-shared" || got[0].SessionID != "sess-shared" {
		t.Fatalf("correlation = a=%q sess=%q", got[0].ALegID, got[0].SessionID)
	}
	if len(got[0].ExpectedBLegIDs) != 2 || got[0].ExpectedBLegIDs[0] != "b-1" || got[0].ExpectedBLegIDs[1] != "b-2" {
		t.Fatalf("frozen expected B-legs = %#v", got[0].ExpectedBLegIDs)
	}

	state.noteAllocatedBLeg("b-3", 3)
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	frozen := state.freezeAllocatedBLegs()
	if len(frozen) != 2 || frozen[0] != "b-1" || frozen[1] != "b-2" {
		t.Fatalf("allocated set grew after terminal freeze: %#v", frozen)
	}
	if len(got) != 1 {
		t.Fatalf("identical call-closure replay must not append again: %d", len(got))
	}
	if len(got[0].ExpectedBLegIDs) != 2 {
		t.Fatalf("expected B-leg set grew after seal: %#v", got[0].ExpectedBLegIDs)
	}
}

func TestCallUsageAppenderNilLeavesRuntimeWithoutFinancialHandoff(t *testing.T) {
	var tur []billing.TurnUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		BillingIdentity: testBillingIdentity(),
	}}
	stream := &retryRecvStream{
		executor: executor, aLegID: "a-1",
		baseline: lipapi.Call{},
		bleg:     b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1},
		cand:     routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	}
	stampStreamIdentity(stream)
	stream.recordBillingLeg(context.Background(), sdkterminal.CommandNormalFinish)
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(tur) != 0 {
		t.Fatalf("legacy TUR handoff = %d, want 0", len(tur))
	}
}

func TestCallUsageAppenderIsTheOnlyRuntimeTerminalBillingSink(t *testing.T) {
	var tur []billing.TurnUsageRecord
	var calls []billing.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		CallUsageAppender: billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
			calls = append(calls, record)
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		executor:      executor,
		aLegID:        "a-1",
		billingCallID: callID,
		baseline:      lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
		bleg:          b2bua.BLegRecord{BLegID: "b-win", ALegID: "a-1", Seq: 1},
		cand:          routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	}
	stampStreamIdentity(stream)
	stream.recordBillingLeg(context.Background(), sdkterminal.CommandNormalFinish)
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(tur) != 0 {
		t.Fatalf("legacy TUR handoff = %d, want 0", len(tur))
	}
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(calls))
	}
	if len(calls[0].ExpectedBLegIDs) != 1 || calls[0].ExpectedBLegIDs[0] != "b-win" {
		t.Fatalf("expected B-legs from recorded allocation = %#v", calls[0].ExpectedBLegIDs)
	}
}

func TestCallUsageAppenderSealsOnRequestOwnerPanicAndGateReplacementWithoutTUR(t *testing.T) {
	commands := []sdkterminal.Command{
		sdkterminal.CommandPanic,
		sdkterminal.CommandGateReplacement,
	}
	for _, command := range commands {
		t.Run(string(command), func(t *testing.T) {
			var tur int
			var got []billing.CallUsageRecord
			executor := &Executor{BillingRuntime: BillingRuntime{
				CallUsageAppender: billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
					got = append(got, record)
					return nil
				}),
				BillingIdentity: testBillingIdentity(),
			}}
			callID, err := billing.NewBillingCallID()
			if err != nil {
				t.Fatal(err)
			}
			stream := &retryRecvStream{
				executor:      executor,
				aLegID:        "a-1",
				billingCallID: callID,
				baseline:      lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
				bleg:          b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1},
				cand:          routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
			}
			stampStreamIdentity(stream)
			result := stream.runStreamTerminal(context.Background(), command, nil)
			if result.Err != nil {
				t.Fatalf("request-owner %s: %v", command, result.Err)
			}
			if !result.Won {
				t.Fatalf("request-owner %s must win", command)
			}
			if tur != 0 {
				t.Fatalf("TUR seals for %s = %d, want 0 (call-closure must not reuse the TUR filter)", command, tur)
			}
			if len(got) != 1 {
				t.Fatalf("call-closure appends for %s = %d, want 1", command, len(got))
			}
			if got[0].Outcome != billing.TurnOutcomeFailed {
				t.Fatalf("call-closure outcome for %s = %q, want failed", command, got[0].Outcome)
			}
			if len(got[0].ExpectedBLegIDs) != 1 || got[0].ExpectedBLegIDs[0] != "b-1" {
				t.Fatalf("expected B-legs for %s = %#v", command, got[0].ExpectedBLegIDs)
			}

			later := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil)
			if later.Won {
				t.Fatalf("later finish must lose after %s already owns the request", command)
			}
			if len(got) != 1 {
				t.Fatalf("later finish after %s appended again: %d", command, len(got))
			}
			if tur != 0 {
				t.Fatalf("later finish after %s must not seal TUR, tur=%d", command, tur)
			}
		})
	}
}

func TestCallUsageAppenderGateReplacementAppendFailureDoesNotRetryProvider(t *testing.T) {
	var opens atomic.Int32
	var got []billing.CallUsageRecord
	var tur int
	executor := TestExecutor()
	executor.SecureSessionRecordingMandatory = true
	executor.BillingIdentity = testBillingIdentity()
	executor.CallUsageAppender = billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
		got = append(got, record)
		return errors.New("durable unavailable")
	})
	executor.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, errors.New("must not open after committed gate-replacement")
			},
		},
	}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		executor:                    executor,
		aLegID:                      "a-1",
		billingCallID:               callID,
		baseline:                    lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
		bleg:                        b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1},
		cand:                        routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "backend", Model: "model"}},
		secureRecvRecordingHardStop: true,
	}
	stampStreamIdentity(stream)
	stream.markCommitted()
	_, err = stream.tryReplacementIteration(context.Background())
	var uf *lipapi.UpstreamFailureError
	if !errors.As(err, &uf) || uf.Phase != lipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("unexpected replacement error: %v", err)
	}
	if stream.requestTerm != nil && stream.requestTerm.Owner().State().IsTerminal() {
		t.Fatal("committed gate-replacement must not take request ownership")
	}
	if len(got) != 1 {
		t.Fatalf("gate-replacement call-closure appends = %d, want 1", len(got))
	}
	if tur != 0 {
		t.Fatalf("committed gate-replacement must not seal TUR, tur=%d", tur)
	}
	if opens.Load() != 0 {
		t.Fatalf("call-closure append must not drive provider retry, opens=%d", opens.Load())
	}
}

func TestCallUsageAppenderIncludesNeverOpenedNextBLegAtRequestTerminal(t *testing.T) {
	var got []billing.CallUsageRecord
	var legs []billing.CallLegUsageRecord
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingAuthoritative = true
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: billing.VersionRef{ID: "pricing:test", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "policy:test", Version: "1"}, Status: billing.ExposureOpen}, nil
	})
	ex.CallUsageAppender = billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
		got = append(got, record)
		return nil
	})
	ex.CallLegUsageAppender = billing.CallLegUsageAppenderFunc(func(_ context.Context, record billing.CallLegUsageRecord) error {
		sealed, err := record.Seal()
		if err != nil {
			return err
		}
		legs = append(legs, sealed)
		return nil
	})
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, lipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-nextbleg", ContinuityKey: "sess-nextbleg"},
		Route:   lipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	aLegID := strings.TrimSpace(call.Session.ALegID)
	if aLegID == "" {
		t.Fatal("expected A-leg after execute")
	}
	atts, err := st.LoadAttempts(context.Background(), aLegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("attempts = %d, want never-opened + winner", len(atts))
	}
	if atts[0].Outcome != lipapi.AttemptSwallowedFailure || atts[1].Outcome != lipapi.AttemptSuccess {
		t.Fatalf("attempt outcomes = %s, %s", atts[0].Outcome, atts[1].Outcome)
	}
	if len(got) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(got))
	}
	seen := map[string]bool{}
	for _, id := range got[0].ExpectedBLegIDs {
		seen[id] = true
	}
	if len(got[0].ExpectedBLegIDs) != 2 || !seen[atts[0].BLegID] {
		t.Fatalf("never-opened NextBLeg id %q missing from frozen expected set %#v", atts[0].BLegID, got[0].ExpectedBLegIDs)
	}
	if !seen[atts[1].BLegID] {
		t.Fatalf("winner NextBLeg id %q missing from frozen expected set %#v", atts[1].BLegID, got[0].ExpectedBLegIDs)
	}
	if len(legs) != 2 {
		t.Fatalf("independent terminal leg rows = %d, want never-started + winner", len(legs))
	}
	if _, err := billing.JoinCompleteCall(got[0], legs); err != nil {
		t.Fatalf("call closure with never-started leg must join complete: %v", err)
	}
}

func TestCallUsageAppenderSwallowedAttemptDoesNotFreezeUntilRequestTerminal(t *testing.T) {
	var got []billing.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		CallUsageAppender: billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
			got = append(got, record)
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	stream := &retryRecvStream{
		executor:      executor,
		aLegID:        "a-1",
		billingCallID: callID,
		baseline:      lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
		bleg:          b2bua.BLegRecord{BLegID: "b-swallowed", ALegID: "a-1", Seq: 1},
		cand:          routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}},
	}
	stampStreamIdentity(stream)
	if result := stream.runAttemptTerminal(context.Background(), sdkterminal.CommandSwallowedAttempt, nil); result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(got) != 0 {
		t.Fatalf("swallowed attempt-terminal must not append call-closure, got %d", len(got))
	}
	if stream.billingCallState != nil && stream.billingCallState.hasFrozen {
		t.Fatal("swallowed attempt-terminal must not freeze allocated B-legs")
	}

	stream.bleg = b2bua.BLegRecord{BLegID: "b-replacement", ALegID: "a-1", Seq: 2}
	stream.cand = routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}
	stream.resetAttemptTerminal()
	if result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(got) != 1 {
		t.Fatalf("request-owner call-closure appends = %d, want 1", len(got))
	}
	if len(got[0].ExpectedBLegIDs) != 2 || got[0].ExpectedBLegIDs[0] != "b-replacement" || got[0].ExpectedBLegIDs[1] != "b-swallowed" {
		t.Fatalf("sealed expected B-legs = %#v, want swallowed + replacement", got[0].ExpectedBLegIDs)
	}
}

func TestInterleavedThinkerBillingCorrectness(t *testing.T) {
	t.Parallel()

	// 1. Verify isInterleavedThinker controls request terminal claim and call closure.
	var closures []billing.CallUsageRecord
	executor := &Executor{BillingRuntime: BillingRuntime{
		CallUsageAppender: billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
			closures = append(closures, record)
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	callID, err := billing.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	stream := &retryRecvStream{
		executor:             executor,
		aLegID:               "a-shared",
		billingCallID:        callID,
		baseline:             lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-shared"}},
		bleg:                 b2bua.BLegRecord{BLegID: "b-thinker", ALegID: "a-shared", Seq: 1},
		cand:                 routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
		isInterleavedThinker: true,
	}
	stampStreamIdentity(stream)
	stream.ensureTerminals()

	// A normal finish of a thinker must only claim attempt terminal and NOT write call closure
	ev := lipapi.Event{Kind: lipapi.EventResponseFinished}
	_, _, err = stream.finalizeResponseFinishedAuthority(context.Background(), ev)
	if err != nil {
		t.Fatalf("finalizeResponseFinishedAuthority: %v", err)
	}

	if len(closures) != 0 {
		t.Fatalf("thinker B-leg normal finish must not write call closure, got %+v", closures)
	}
	if stream.requestTerm.Owner().State() != sdkterminal.StateOpen {
		t.Fatalf("thinker B-leg normal finish must keep request terminal Open, got %s", stream.requestTerm.Owner().State())
	}
	if stream.attemptTerm.Owner().State() != sdkterminal.StateReleased {
		t.Fatalf("thinker B-leg normal finish must release attempt terminal, got %s", stream.attemptTerm.Owner().State())
	}

	// 2. Verify continuation stream carries billing identity/snapshot fields from thinker.
	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:       true,
			Reserved:      true,
			ReservationID: "reservation-executor",
			PolicyRecord:  policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, thinker := setupInterleavedAuthorityContinuation(t, auth, "hidden")

	// Set billing identity fields on the thinker stream
	thinker.billingCallID = callID
	thinker.billingAccountID = "acct-cont"
	thinker.billingCustomerPricing = billing.VersionRef{ID: "pricing:cont", Version: "1"}
	thinker.billingChargePolicy = billing.VersionRef{ID: "policy:cont", Version: "1"}
	thinker.billingIdentityStamped = true
	thinker.customer = newCustomerEvidenceAccumulator()

	state := interleavedstate.State{}
	cont, err := ex.openInterleavedExecutorContinuation(context.Background(), thinker, state)
	if err != nil {
		t.Fatalf("openInterleavedExecutorContinuation: %v", err)
	}

	if cont.billingCallID != thinker.billingCallID {
		t.Errorf("continuation missing billingCallID")
	}
	if cont.billingAccountID != thinker.billingAccountID {
		t.Errorf("continuation missing billingAccountID")
	}
	if cont.billingCustomerPricing != thinker.billingCustomerPricing {
		t.Errorf("continuation missing billingCustomerPricing")
	}
	if cont.billingChargePolicy != thinker.billingChargePolicy {
		t.Errorf("continuation missing billingChargePolicy")
	}
	if cont.billingIdentityStamped != thinker.billingIdentityStamped {
		t.Errorf("continuation missing billingIdentityStamped")
	}
	if cont.customer == nil {
		t.Errorf("continuation missing customer accumulator")
	}
	if cont.isInterleavedThinker {
		t.Errorf("continuation must not be marked isInterleavedThinker")
	}
}

func testInterleavedExecutor(t *testing.T, backends map[string]execbackend.Backend) (*Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = backends
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}
	ex.MemoStore = memoStore
	return ex, st
}

func testThinkerMemoStream(memoBody string) lipapi.ManagedEventStream {
	return lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag + memoBody + interleavedthinking.MemoCloseTag},
		{Kind: lipapi.EventResponseFinished},
	})
}

func testExecutorTextStream(text string) lipapi.ManagedEventStream {
	return lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: text},
		{Kind: lipapi.EventResponseFinished},
	})
}

func resumeTestInterleavedCall(first, second *lipapi.Call) {
	second.Session = lipapi.SessionRef{
		AuthoritativeSessionID: first.Session.AuthoritativeSessionID,
		ALegID:                 first.Session.ALegID,
		ClientSessionID:        first.Session.ClientSessionID,
		ResumeToken:            first.Session.ResumeToken,
	}
}

func TestExecutor_InterleavedHandoffAborted_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []billing.CallUsageRecord
	var gotLegs []billing.CallLegUsageRecord
	var mu sync.Mutex

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(octx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("first exec"), nil
				}
				cancel()
				return testExecutorTextStream("handoff exec"), nil
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingAuthoritative = true
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: billing.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "1"}, Status: billing.ExposureOpen}, nil
	})
	ex.CallUsageAppender = billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	})
	ex.CallLegUsageAppender = billing.CallLegUsageAppenderFunc(func(_ context.Context, record billing.CallLegUsageRecord) error {
		sealed, _ := record.Seal()
		mu.Lock()
		gotLegs = append(gotLegs, sealed)
		mu.Unlock()
		return nil
	})

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-handoff-abort", ContinuityKey: "sess-handoff-abort"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(ctx, first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(ctx, firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-handoff-abort", ContinuityKey: "sess-handoff-abort"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	stream, err := ex.Execute(ctx, second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	_, err = lipapi.Collect(ctx, stream)
	if err == nil {
		t.Fatal("expected handoff abortion error")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	legsLen := len(gotLegs)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
	if legsLen != 3 { // first exec, thinker, continuation exec
		t.Fatalf("expected exactly 3 leg usage records, got %d", legsLen)
	}

	mu.Lock()
	var handoffLegFound bool
	for _, leg := range gotLegs {
		t.Logf("Found leg: BLegID=%q, BackendID=%q, Outcome=%v", leg.BLegID, leg.BackendID, leg.Outcome)
		if leg.BackendID == "exec-be" && leg.Outcome == billing.LegOutcomeCanceled {
			handoffLegFound = true
		}
	}
	mu.Unlock()
	if !handoffLegFound {
		t.Fatal("continuation B-leg not found in leg usage records")
	}
}

func TestExecutor_InterleavedCancelDuringTransition_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []billing.CallUsageRecord
	var mu sync.Mutex

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32
	blockChan := make(chan struct{})

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(octx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("first exec"), nil
				}
				<-blockChan
				return testExecutorTextStream("exec"), nil
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingAuthoritative = true
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: billing.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "1"}, Status: billing.ExposureOpen}, nil
	})
	ex.CallUsageAppender = billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	})

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-cancel-trans", ContinuityKey: "sess-cancel-trans"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-cancel-trans", ContinuityKey: "sess-cancel-trans"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	secondStream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := lipapi.Collect(context.Background(), secondStream)
		errChan <- err
	}()

	time.Sleep(100 * time.Millisecond)

	_ = secondStream.(lipapi.ManagedEventStream).Cancel(context.Background(), lipapi.CancelCause{Kind: lipapi.CancelContextDone})

	close(blockChan)

	collectErr := <-errChan
	if collectErr == nil {
		t.Fatal("expected cancellation error during collect")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
}

func TestExecutor_InterleavedCloseDuringTransition_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []billing.CallUsageRecord
	var mu sync.Mutex

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32
	blockChan := make(chan struct{})

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(octx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("first exec"), nil
				}
				<-blockChan
				return testExecutorTextStream("exec"), nil
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingAuthoritative = true
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: billing.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "1"}, Status: billing.ExposureOpen}, nil
	})
	ex.CallUsageAppender = billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	})

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-close-trans", ContinuityKey: "sess-close-trans"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-close-trans", ContinuityKey: "sess-close-trans"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	secondStream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := lipapi.Collect(context.Background(), secondStream)
		errChan <- err
	}()

	time.Sleep(100 * time.Millisecond)

	_ = secondStream.(lipapi.ManagedEventStream).Close()

	close(blockChan)

	collectErr := <-errChan
	if collectErr == nil {
		t.Fatal("expected close error during collect")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
}

func TestExecutor_InterleavedOpenFailureDuringTransition_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []billing.CallUsageRecord
	var mu sync.Mutex

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	var execOpens atomic.Int32

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return testThinkerMemoStream("plan"), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				if execOpens.Add(1) == 1 {
					return testExecutorTextStream("exec-first"), nil
				}
				return nil, lipapi.RecoverablePreOutputError(errors.New("executor unavailable"))
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingAuthoritative = true
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: billing.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "1"}, Status: billing.ExposureOpen}, nil
	})
	ex.CallUsageAppender = billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	})

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-open-fail", ContinuityKey: "sess-open-fail"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-open-fail", ContinuityKey: "sess-open-fail"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	stream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	_, err = lipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected continuation open failure error")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}
}

func TestExecutor_InterleavedThinkerError_ClosureSealed(t *testing.T) {
	t.Parallel()

	var gotClosures []billing.CallUsageRecord
	var mu sync.Mutex

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	transport := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	var thinkerOpens atomic.Int32
	var execOpens atomic.Int32

	backends := map[string]execbackend.Backend{
		"thinker-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				thinkerOpens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventError, ErrorCode: "thinker_failed", ErrorMessage: "backend thinker failed midstream"},
				}), nil
			},
		},
		"exec-be": {
			Caps: caps, TransportCaps: transport,
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				val := execOpens.Add(1)
				if val == 1 {
					return testExecutorTextStream("exec"), nil
				}
				t.Error("exec-be should not be opened on thinker error in second turn")
				return nil, errors.New("exec-be should not be opened")
			},
		},
	}

	ex, _ := testInterleavedExecutor(t, backends)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingAuthoritative = true
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
		return billing.CallExposure{AccountID: "acct", CallID: in.CallID, PricingRef: billing.VersionRef{ID: "pricing", Version: "1"}, ChargePolicyRef: billing.VersionRef{ID: "policy", Version: "1"}, Status: billing.ExposureOpen}, nil
	})
	ex.CallUsageAppender = billing.CallUsageAppenderFunc(func(_ context.Context, record billing.CallUsageRecord) error {
		mu.Lock()
		gotClosures = append(gotClosures, record)
		mu.Unlock()
		return nil
	})

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-thinker-err", ContinuityKey: "sess-thinker-err"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	firstStream, err := ex.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if _, err := lipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("first collect: %v", err)
	}

	second := &lipapi.Call{
		Session: lipapi.SessionRef{AuthoritativeSessionID: "sess-thinker-err", ContinuityKey: "sess-thinker-err"},
		Route:   lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}
	resumeTestInterleavedCall(first, second)
	secondStream, err := ex.Execute(context.Background(), second)
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}

	_, err = lipapi.Collect(context.Background(), secondStream)
	if err == nil {
		t.Fatal("expected midstream thinker error during collect")
	}

	mu.Lock()
	closuresLen := len(gotClosures)
	mu.Unlock()

	if closuresLen != 2 {
		t.Fatalf("expected exactly 2 call closures, got %d", closuresLen)
	}

	if execOpens.Load() != 1 {
		t.Errorf("expected exec-be to open exactly once, got %d", execOpens.Load())
	}
}
