package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

type preflightCountFunc func(context.Context, app.CountCallInput) (app.CountResult, error)

func (f preflightCountFunc) CountCall(ctx context.Context, in app.CountCallInput) (app.CountResult, error) {
	return f(ctx, in)
}

type spyAttemptProvider struct {
	mu           sync.Mutex
	id           string
	admitCalls   atomic.Int32
	settleCalls  atomic.Int32
	releaseCalls atomic.Int32
	settledIDs   []string
	releasedIDs  []string
	clampDone    chan struct{}
}

func (p *spyAttemptProvider) getSettled() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.settledIDs)
}

func (p *spyAttemptProvider) getReleased() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.releasedIDs)
}

func (p *spyAttemptProvider) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	p.admitCalls.Add(1)
	if in.BackendID == "loser-clamp" && p.clampDone != nil {
		select {
		case <-p.clampDone:
		default:
			close(p.clampDone)
		}
	}
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Reservations: []authority.Reservation{{
			Handle: p.id + "-" + in.BackendID,
			Kind:   authority.ReservationSpend,
			Money:  &economics.Money{NanoUnits: 1, Currency: "USD", Present: true},
		}},
	}, nil
}

func (p *spyAttemptProvider) SettleAttempt(ctx context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	p.settleCalls.Add(1)
	p.mu.Lock()
	p.settledIDs = append(p.settledIDs, in.Handles...)
	p.mu.Unlock()
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *spyAttemptProvider) ReleaseAttempt(ctx context.Context, in authority.AttemptRelease) error {
	p.releaseCalls.Add(1)
	p.mu.Lock()
	p.releasedIDs = append(p.releasedIDs, in.Handles...)
	p.mu.Unlock()
	return nil
}

type spyMemoStore struct {
	inner       interleavedthinking.MemoStore
	updateCalls atomic.Int32
	putCalls    atomic.Int32
}

func (s *spyMemoStore) Put(ctx context.Context, scope interleavedthinking.Scope, state interleavedthinking.MemoState) (interleavedstate.MemoRef, error) {
	s.putCalls.Add(1)
	return s.inner.Put(ctx, scope, state)
}

func (s *spyMemoStore) Get(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef) (interleavedthinking.MemoState, bool, error) {
	return s.inner.Get(ctx, scope, ref)
}

func (s *spyMemoStore) Update(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef, state interleavedthinking.MemoState) (interleavedstate.MemoRef, error) {
	s.updateCalls.Add(1)
	return s.inner.Update(ctx, scope, ref, state)
}

func (s *spyMemoStore) Delete(ctx context.Context, scope interleavedthinking.Scope, ref interleavedstate.MemoRef) error {
	return s.inner.Delete(ctx, scope, ref)
}

func TestParallelLoser_Strengthened(t *testing.T) {
	t.Parallel()

	// 1. Setup spy providers and executor
	attProv := &spyAttemptProvider{id: "spy-att"}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, nil)

	// Attach coordinators
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "spy-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	// Setup spy memo store
	innerMemo := interleavedthinking.NewMemoStore(4096)
	memoStore := &spyMemoStore{inner: innerMemo}
	ex.MemoStore = memoStore
	ex.InterleavedConfig = interleavedthinking.ShapeConfig{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}

	// Configure preflight checker to require max output clamp enforcement
	// MaxOutputTokens is clamped to 100, and client requests 500.
	ex.Preflight = preflight.NewChecker(preflightCountFunc(func(_ context.Context, in app.CountCallInput) (app.CountResult, error) {
		return app.CountResult{InputTokens: 5, OutputTokens: 10, TotalTokens: 15, TotalTokensPresent: true}, nil
	}), preflight.Config{
		Enabled:              true,
		Mode:                 preflight.ModeAdvisory,
		ClampMaxOutputTokens: true,
		MaxOutputTokens:      100,
	})

	// 2. Setup Backends
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	tcaps := parallelTransportCaps()
	loserOpenStartedCh := make(chan struct{}, 1)
	loserClampAdmissionDone := make(chan struct{})
	attProv.clampDone = loserClampAdmissionDone

	combinedWaitCh := make(chan struct{})
	go func() {
		<-loserOpenStartedCh
		<-loserClampAdmissionDone
		close(combinedWaitCh)
	}()

	ex.Backends["winner"] = execbackend.Backend{
		Caps: caps, TransportCaps: tcaps,
		EnforcesMaxOutputTokens: true,
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: combinedWaitCh,
				events: []lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "winner response"},
					{Kind: lipapi.EventResponseFinished},
				},
			}, nil
		},
	}

	ex.Backends["loser-open"] = execbackend.Backend{
		Caps: caps, TransportCaps: tcaps,
		EnforcesMaxOutputTokens: true,
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &signalOnceBlockStream{openedCh: loserOpenStartedCh}, nil
		},
	}

	ex.Backends["loser-clamp"] = execbackend.Backend{
		Caps: caps, TransportCaps: tcaps,
		EnforcesMaxOutputTokens: false, // Will fail clamp check pre-open!
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			t.Fatal("loser-clamp should not be opened!")
			return nil, errors.New("should not be called")
		},
	}

	// Seed memo in store
	ctx := context.Background()
	memoRef, err := innerMemo.Put(ctx, interleavedthinking.Scope(aLegID), interleavedthinking.MemoState{
		Memo:                  "original memo guidance",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Setup A-Leg Scope
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	// Candidates
	winnerCand := routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "winner", Model: "m"},
		Key:             "winner:m",
		InterleavedRole: interleavedstate.RoleExecutor,
	}
	loserOpenCand := routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "loser-open", Model: "m"},
		Key:             "loser-open:m",
		InterleavedRole: interleavedstate.RoleExecutor,
	}
	loserClampCand := routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "loser-clamp", Model: "m"},
		Key:             "loser-clamp:m",
		InterleavedRole: interleavedstate.RoleExecutor,
	}

	// Budget and request
	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope
	req.interleaved = interleavedstate.State{MemoRef: &memoRef}
	req.reqFacts.baseline.Messages = []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("hello")},
	}}

	clampedMax := 500
	req.reqFacts.baseline.Options.MaxOutputTokens = &clampedMax

	// Run the parallel race
	out, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{winnerCand, loserOpenCand, loserClampCand}, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup failed: %v", err)
	}
	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected a winner session")
	}
	if out.ready.Candidate().Primary.Backend != "winner" {
		t.Fatalf("expected winner to be 'winner', got %s", out.ready.Candidate().Primary.Backend)
	}

	// Settle the winner manually to simulate winner committing/settling
	var authState attemptAuthorityState
	if out.ready.session.authority.control != nil {
		authState = out.ready.session.authority.control.state
	}
	life := ex.newAttemptAuthorityLifecycle(authState, out.ready.Candidate())
	usage := lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 6, TotalTokens: 10,
		CostNanoUnits: 99, Currency: "USD", CostPresent: true, CostSource: string(lipapi.UsageSourceProviderReported),
	}
	ex.Log = slog.Default()
	t.Logf("winner cand key: %s", out.ready.Candidate().Key)
	t.Logf("winner authState: viaCoord=%t, stack=%+v", authState.viaCoordinator, authState.stack)
	if !life.Settle(ctx, authorityapp.SettlementKindFinal, usage, false) {
		t.Fatal("winner settle must apply")
	}

	if out.ready.session.inner != nil {
		_ = out.ready.session.inner.Close()
	}

	// Assertions:
	// 1. Settle calls:
	// - "winner" should be settled.
	// - "loser-open" should be settled (incurred).
	// - "loser-clamp" should NOT be settled.
	settled := attProv.getSettled()
	t.Logf("settled handles: %v", settled)
	hasWinnerSettle := false
	hasLoserOpenSettle := false
	hasLoserClampSettle := false
	for _, h := range settled {
		if strings.HasSuffix(h, "-winner") {
			hasWinnerSettle = true
		}
		if strings.HasSuffix(h, "-loser-open") {
			hasLoserOpenSettle = true
		}
		if strings.HasSuffix(h, "-loser-clamp") {
			hasLoserClampSettle = true
		}
	}
	if !hasWinnerSettle {
		t.Error("expected winner to be settled")
	}
	if !hasLoserOpenSettle {
		t.Error("expected loser-open to be settled")
	}
	if hasLoserClampSettle {
		t.Error("expected loser-clamp NOT to be settled")
	}

	// 2. Release calls:
	// - "loser-clamp" should be released (admission/clamp failure).
	released := attProv.getReleased()
	t.Logf("released handles: %v", released)
	hasLoserClampRelease := false
	for _, h := range released {
		if strings.HasSuffix(h, "-loser-clamp") {
			hasLoserClampRelease = true
		}
	}
	if !hasLoserClampRelease {
		t.Error("expected loser-clamp to be released")
	}

	// 3. Winner-only memo update assertion:
	// - MemoStore.Update must be called exactly once (for the winner).
	// - MemoStore.Put must not be called.
	if got := memoStore.updateCalls.Load(); got != 1 {
		t.Errorf("expected exactly 1 MemoStore.Update call, got %d", got)
	}
	if got := memoStore.putCalls.Load(); got != 0 {
		t.Errorf("expected 0 MemoStore.Put calls, got %d", got)
	}
}

type spyLoserStream struct {
	mu          sync.Mutex
	cancelCalls int
	closeCalls  int
	cancelCause lipapi.CancelCause
	blockCh     chan struct{}
}

func (s *spyLoserStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.blockCh != nil {
		select {
		case <-s.blockCh:
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	return lipapi.Event{}, io.EOF
}

func (s *spyLoserStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelCalls++
	s.cancelCause = cause
	return lipapi.CancelResult{}
}

func (s *spyLoserStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	return nil
}

func (s *spyLoserStream) counts() (int, int, lipapi.CancelCause) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelCalls, s.closeCalls, s.cancelCause
}

func TestParallelLoser_SingleOwnerTerminalization_ExactOnce(t *testing.T) {
	t.Parallel()

	attProv := &spyAttemptProvider{id: "spy-att"}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, nil)

	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: "spy-att", Class: authoritycoord.AttemptPriorityHardSpend, Provider: attProv, Strength: authority.StrengthRequired,
		}},
	}

	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	tcaps := parallelTransportCaps()
	loserOpenStartedCh := make(chan struct{}, 1)
	loserStream := &spyLoserStream{blockCh: make(chan struct{})}

	ex.Backends["winner"] = execbackend.Backend{
		Caps: caps, TransportCaps: tcaps,
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: loserOpenStartedCh,
				events: []lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "winner delta"},
					{Kind: lipapi.EventResponseFinished},
				},
			}, nil
		},
	}

	ex.Backends["loser"] = execbackend.Backend{
		Caps: caps, TransportCaps: tcaps,
		Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			select {
			case loserOpenStartedCh <- struct{}{}:
			default:
			}
			return loserStream, nil
		},
	}

	var (
		logMu         sync.Mutex
		loggedRecords []lipapi.AttemptRecord
	)
	ex.Store = &recordingAttemptStore{
		Store: ex.Store,
		onRecord: func(ctx context.Context, rec lipapi.AttemptRecord) {
			logMu.Lock()
			loggedRecords = append(loggedRecords, rec)
			logMu.Unlock()
		},
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	winnerCand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "winner", Model: "m"},
		Key:     "winner:m",
	}
	loserCand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "loser", Model: "m"},
		Key:     "loser:m",
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope

	ctx := context.Background()
	out, err := ex.tryOpenParallelGroup(ctx, req, []routing.AttemptCandidate{winnerCand, loserCand}, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup failed: %v", err)
	}
	if out.ready == nil {
		t.Fatal("expected winner readyAttempt")
	}

	// Winner bridge close triggers wait for loser cleanup
	bridgeStream, ok := out.ready.session.inner.(*parallelBridgeStream)
	if ok && bridgeStream != nil {
		_ = bridgeStream.Close()
	} else if out.ready.session.inner != nil {
		_ = out.ready.session.inner.Close()
	}

	// 1. Verify loser stream Cancel & Close called exactly once
	cancelCalls, closeCalls, cancelCause := loserStream.counts()
	if cancelCalls != 1 {
		t.Errorf("expected exactly 1 cancel on loser stream, got %d", cancelCalls)
	}
	if closeCalls != 1 {
		t.Errorf("expected exactly 1 close on loser stream, got %d", closeCalls)
	}
	if cancelCause.Kind != lipapi.CancelRaceLoser {
		t.Errorf("expected CancelRaceLoser cause, got %v", cancelCause.Kind)
	}

	// 2. Verify B-leg was released from aScope so subsequent cancel doesn't touch it
	if err := coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	// After CancelALeg, loser stream cancelCalls must STILL be 1 (not canceled again)
	cancelCallsAfter, _, _ := loserStream.counts()
	if cancelCallsAfter != 1 {
		t.Errorf("loser stream canceled again after ALeg cancellation, cancel count = %d", cancelCallsAfter)
	}

	// 3. Verify attempt was logged exactly once with AttemptCancelled and 'parallel race loser'
	logMu.Lock()
	var loserLogs []lipapi.AttemptRecord
	for _, rec := range loggedRecords {
		if rec.BackendID == "loser" {
			loserLogs = append(loserLogs, rec)
		}
	}
	logMu.Unlock()
	if len(loserLogs) != 1 {
		t.Errorf("expected exactly 1 attempt log for loser, got %d", len(loserLogs))
	} else {
		if loserLogs[0].Outcome != lipapi.AttemptCancelled {
			t.Errorf("expected AttemptCancelled outcome, got %v", loserLogs[0].Outcome)
		}
		if loserLogs[0].Reason != "parallel race loser" {
			t.Errorf("expected 'parallel race loser' reason, got %q", loserLogs[0].Reason)
		}
	}
}
