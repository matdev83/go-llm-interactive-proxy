package runtime

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

type timingReqPartHook struct {
	id    string
	order int
	fn    func(context.Context, *lipapi.Call, sdk.PartMeta) error
}

func (s *timingReqPartHook) ID() string                   { return s.id }
func (s *timingReqPartHook) Order() int                   { return s.order }
func (s *timingReqPartHook) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (s *timingReqPartHook) HandleRequestParts(ctx context.Context, call *lipapi.Call, meta sdk.PartMeta) error {
	return s.fn(ctx, call, meta)
}

// Phase 1.1 characterization (dual-plane-economics-and-concurrency-foundation):
// lock today's attempt-grain usage-authority timing. These tests document current
// behavior (per B-leg admit, mutation after reserve, loser release after Open).
// Phase 6+ will split customer logical-request vs operator attempt lifecycles.

// sequencingAuthorityRecorder assigns a unique reservation ID per authoritative
// (non-estimate) Admit so characterization tests can prove per-attempt grain.
type sequencingAuthorityRecorder struct {
	mu             sync.Mutex
	admitInputsV   []authorityapp.AdmissionInput
	releaseInputsV []authorityapp.ReleaseInput
	settleInputsV  []authorityapp.SettleInput
	admitCalls     atomic.Int64
	releaseCalls   atomic.Int64
	settleCalls    atomic.Int64
	seq            atomic.Int64
}

func (s *sequencingAuthorityRecorder) Admit(_ context.Context, in authorityapp.AdmissionInput) (authorityapp.AdmissionResult, error) {
	s.admitCalls.Add(1)
	s.mu.Lock()
	s.admitInputsV = append(s.admitInputsV, in)
	s.mu.Unlock()
	result := authorityapp.AdmissionResult{
		Allowed:        true,
		Reserved:       !in.EstimateOnly,
		ReservedAmount: authorityInputAmount(7),
		PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
	}
	if !in.EstimateOnly {
		n := s.seq.Add(1)
		result.ReservationID = fmt.Sprintf("res-%d", n)
	}
	return result, nil
}

func (s *sequencingAuthorityRecorder) Settle(_ context.Context, in authorityapp.SettleInput) (authorityapp.SettleResult, error) {
	s.settleCalls.Add(1)
	s.mu.Lock()
	s.settleInputsV = append(s.settleInputsV, in)
	s.mu.Unlock()
	return authorityapp.SettleResult{Applied: true, ReservationID: in.ReservationID}, nil
}

func (s *sequencingAuthorityRecorder) Release(_ context.Context, in authorityapp.ReleaseInput) (authorityapp.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	s.mu.Lock()
	s.releaseInputsV = append(s.releaseInputsV, in)
	s.mu.Unlock()
	return authorityapp.ReleaseResult{Applied: true, ReservationID: in.ReservationID}, nil
}

func (s *sequencingAuthorityRecorder) ApplyUsage(_ context.Context, cmd authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (s *sequencingAuthorityRecorder) Status(context.Context) (controlplane.AccountingAuthorityStatus, error) {
	return controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady}, nil
}

func (s *sequencingAuthorityRecorder) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, nil
}

func (s *sequencingAuthorityRecorder) Decisions(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return controlplane.Page[controlplane.AccountingDecisionRow]{}, nil
}

func (s *sequencingAuthorityRecorder) authoritativeAdmits() []authorityapp.AdmissionInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]authorityapp.AdmissionInput, 0, len(s.admitInputsV))
	for _, in := range s.admitInputsV {
		if !in.EstimateOnly {
			out = append(out, in)
		}
	}
	return out
}

func (s *sequencingAuthorityRecorder) releases() []authorityapp.ReleaseInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]authorityapp.ReleaseInput, len(s.releaseInputsV))
	copy(out, s.releaseInputsV)
	return out
}

func (s *sequencingAuthorityRecorder) settles() []authorityapp.SettleInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]authorityapp.SettleInput, len(s.settleInputsV))
	copy(out, s.settleInputsV)
	return out
}

// TestAuthorityTiming_failoverIssuesAuthoritativeAdmitPerAttempt proves that a
// logical request currently runs authoritative Admit once per backend attempt
// under recv-phase failover (initial open + replacement), with distinct B-leg IDs.
func TestAuthorityTiming_failoverIssuesAuthoritativeAdmitPerAttempt(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	budget := &attemptBudget{max: 5}
	out, err := ex.openPlannedCandidate(context.Background(), authorityOpenParams(t, aLegID, budget), authorityCandidate(), nil, "", false)
	if err != nil {
		t.Fatalf("initial openPlannedCandidate: %v", err)
	}
	if !out.opened {
		t.Fatal("expected initial attempt to open")
	}

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}
	priorState := out.authority
	priorCand := out.cand
	rs := &retryRecvStream{
		executor:  ex,
		bus:       hooks.New(hooks.Config{}),
		baseline:  authorityOpenParams(t, aLegID, budget).baseline,
		budget:    budget,
		aLegID:    aLegID,
		traceID:   "trace-1",
		sel:       sel,
		session:   &routing.SessionRoutingState{},
		excluded:  map[string]struct{}{},
		rng:       routing.NewSeededRng(1),
		bleg:      out.bleg,
		cand:      priorCand,
		authority: testAuthorityLifecycle(ex, priorState, priorCand),
	}

	opened, err := rs.tryReplacementIteration(context.Background())
	if err != nil {
		t.Fatalf("tryReplacementIteration: %v", err)
	}
	if !opened {
		t.Fatal("expected replacement attempt to open")
	}

	authoritative := auth.authoritativeAdmits()
	if got, want := len(authoritative), 2; got != want {
		t.Fatalf("authoritative admits = %d, want %d (one per backend attempt under failover)", got, want)
	}
	if authoritative[0].ReservationKey.BLegID == "" || authoritative[1].ReservationKey.BLegID == "" {
		t.Fatal("expected both authoritative admits to carry B-leg IDs")
	}
	if authoritative[0].ReservationKey.BLegID == authoritative[1].ReservationKey.BLegID {
		t.Fatalf("failover attempts shared BLegID %q; rules currently execute per backend attempt", authoritative[0].ReservationKey.BLegID)
	}
	if authoritative[0].ReservationKey.LogicalRequestID != authoritative[1].ReservationKey.LogicalRequestID {
		t.Fatalf("logical request IDs diverged across failover attempts: %q vs %q",
			authoritative[0].ReservationKey.LogicalRequestID, authoritative[1].ReservationKey.LogicalRequestID)
	}
}

// TestAuthorityTiming_parallelRaceIssuesAuthoritativeAdmitPerLeg proves that a
// parallel race currently issues one authoritative Admit per opened leg (not one
// per logical request), with distinct B-leg reservation keys.
func TestAuthorityTiming_parallelRaceIssuesAuthoritativeAdmitPerLeg(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	// Force both legs through Open (and therefore through authoritative Admit)
	// before either can win, so the characterization is not racy under -parallel load.
	leg2OpenedCh := make(chan struct{}, 1)
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	ex.Backends["backend-1"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: leg2OpenedCh,
				events: []lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventTextDelta, Delta: "winner"},
					{Kind: lipapi.EventResponseFinished},
				},
			}, nil
		},
	}
	ex.Backends["backend-2"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &signalOnceBlockStream{openedCh: leg2OpenedCh}, nil
		},
	}

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.aScope = aScope
	p.baseline.Route.Selector = "backend-1:model-1!backend-2:model-2"
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(context.Background(), p, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if !out.opened {
		t.Fatal("expected parallel race to open a backend")
	}

	authoritative := auth.authoritativeAdmits()
	if got, want := len(authoritative), 2; got != want {
		t.Fatalf("authoritative admits = %d, want %d (one per parallel leg)", got, want)
	}
	if authoritative[0].ReservationKey.BLegID == authoritative[1].ReservationKey.BLegID {
		t.Fatalf("parallel legs shared BLegID %q", authoritative[0].ReservationKey.BLegID)
	}
	keys := map[string]struct{}{}
	for _, in := range authoritative {
		keys[in.ReservationKey.String()] = struct{}{}
	}
	if len(keys) != 2 {
		t.Fatalf("distinct reservation keys = %d, want 2; keys=%v", len(keys), keys)
	}
}

// TestAuthorityTiming_loserReleaseAfterOpenSetsBackendAttempted proves that when a
// parallel loser has already opened (provider work begun), its Losing release reports
// BackendAttempted=true. Today cleanup is release-only (no settle of incurred cost).
func TestAuthorityTiming_loserReleaseAfterOpenSetsBackendAttempted(t *testing.T) {
	t.Parallel()

	auth := &sequencingAuthorityRecorder{}
	ex, _, _, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)

	loserOpenedCh := make(chan struct{}, 1)
	caps := lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
	ex.Backends["loser"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &signalOnceBlockStream{openedCh: loserOpenedCh}, nil
		},
	}
	ex.Backends["winner"] = execbackend.Backend{
		Caps:          caps,
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return &waitThenWinStream{
				waitCh: loserOpenedCh,
				events: []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "fast"}, {Kind: lipapi.EventResponseFinished}},
			}, nil
		},
	}

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.aScope = aScope
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "loser", Model: "m"}, Key: "loser:m"},
		{Primary: routing.Primary{Backend: "winner", Model: "m"}, Key: "winner:m"},
	}

	out, err := ex.tryOpenParallelGroup(context.Background(), p, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if !out.opened {
		t.Fatal("expected winner to open")
	}
	if out.cand.Primary.Backend != "winner" {
		t.Fatalf("winner backend = %q, want winner", out.cand.Primary.Backend)
	}

	// Wait briefly for loser cancel/settle path to finish after winner election.
	deadline := time.Now().Add(2 * time.Second)
	var settles []authorityapp.SettleInput
	for time.Now().Before(deadline) {
		settles = auth.settles()
		if len(settles) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(settles) < 1 {
		t.Fatal("expected at least one Losing settle for the opened loser")
	}

	foundOpenedLoser := false
	for _, st := range settles {
		if st.Kind == authorityapp.SettlementKindLosing && st.BackendAttempted {
			foundOpenedLoser = true
			break
		}
	}
	if !foundOpenedLoser {
		t.Fatalf("expected a Losing settle with BackendAttempted=true after provider open; settles=%+v", settles)
	}
}

// TestAuthorityTiming_requestHookMutatesBeforeAuthoritativeAdmit locks the
// approved dual-plane Backend Ingress order: request-part hooks / route shaping
// run, BE freeze+count, then authoritative Admit, then Open sees the mutated
// call while the reservation remains held (design Lifecycle Placement).
func TestAuthorityTiming_requestHookMutatesBeforeAuthoritativeAdmit(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-mutate",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	var openedCall lipapi.Call
	backend.openFn = func(_ context.Context, call lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		openedCall = call
		return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
	}

	hookRan := atomic.Bool{}
	hookBeforeAuthoritativeAdmit := atomic.Bool{}
	mutatedMax := 4242
	bus := hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			&timingReqPartHook{
				id:    "mutate-before-admit",
				order: 0,
				fn: func(_ context.Context, call *lipapi.Call, _ sdk.PartMeta) error {
					// Estimate-only precheck may have run; authoritative admit (2nd) must not.
					if auth.admitCalls.Load() < 2 {
						hookBeforeAuthoritativeAdmit.Store(true)
					}
					hookRan.Store(true)
					call.Options.MaxOutputTokens = &mutatedMax
					call.Messages = append(call.Messages, lipapi.Message{
						Role:  lipapi.RoleUser,
						Parts: []lipapi.Part{lipapi.TextPart("pre-admit mutation")},
					})
					return nil
				},
			},
		},
	})

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 3})
	p.bus = bus
	p.baseline.Messages = []lipapi.Message{{
		Role:  lipapi.RoleUser,
		Parts: []lipapi.Part{lipapi.TextPart("hello")},
	}}

	out, err := ex.openPlannedCandidate(context.Background(), p, authorityCandidate(), nil, "", false)
	if err != nil {
		t.Fatalf("openPlannedCandidate: %v", err)
	}
	if !out.opened {
		t.Fatal("expected backend to open")
	}
	if !hookRan.Load() {
		t.Fatal("expected request-part hook to run")
	}
	if !hookBeforeAuthoritativeAdmit.Load() {
		t.Fatal("expected request-part hook mutation before authoritative Admit")
	}
	if auth.admitCalls.Load() < 2 {
		t.Fatalf("admit calls = %d, want estimate + authoritative", auth.admitCalls.Load())
	}
	if openedCall.Options.MaxOutputTokens == nil || *openedCall.Options.MaxOutputTokens != mutatedMax {
		t.Fatalf("Open call MaxOutputTokens = %v, want %d (hooks mutate before reserve)", openedCall.Options.MaxOutputTokens, mutatedMax)
	}
	if len(openedCall.Messages) < 2 {
		t.Fatalf("Open call messages = %d, want >= 2 after pre-admit mutation", len(openedCall.Messages))
	}
	if out.authority.admissionResult.ReservationID != "reservation-mutate" {
		t.Fatalf("reservation ID = %q, want reservation-mutate still held through Open", out.authority.admissionResult.ReservationID)
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 while opened attempt still owns the reservation", auth.releaseCalls.Load())
	}
}
