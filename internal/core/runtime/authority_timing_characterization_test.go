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
	admitCalls     atomic.Int64
	releaseCalls   atomic.Int64
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
	out, err := ex.openPlannedCandidate(authorityOpenParams(t, aLegID, budget), authorityCandidate(), nil, "", false)
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
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "winner"},
			{Kind: lipapi.EventResponseFinished},
		}), nil
	}
	ex.Backends["backend-2"] = execbackend.Backend{
		Caps:          lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		TransportCaps: parallelTransportCaps(),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: " "},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
	}

	p := authorityOpenParams(t, aLegID, &attemptBudget{max: 10})
	p.baseline.Route.Selector = "backend-1:model-1!backend-2:model-2"
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(p, candidates, nil, "", false)
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

	out, err := ex.tryOpenParallelGroup(p, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if !out.opened {
		t.Fatal("expected winner to open")
	}
	if out.cand.Primary.Backend != "winner" {
		t.Fatalf("winner backend = %q, want winner", out.cand.Primary.Backend)
	}

	// Wait briefly for loser cancel/release path to finish after winner election.
	deadline := time.Now().Add(2 * time.Second)
	var releases []authorityapp.ReleaseInput
	for time.Now().Before(deadline) {
		releases = auth.releases()
		if len(releases) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(releases) < 1 {
		t.Fatal("expected at least one Losing release for the opened loser")
	}

	foundOpenedLoser := false
	for _, rel := range releases {
		if rel.Kind == authorityapp.ReleaseKindLosing && rel.BackendAttempted {
			foundOpenedLoser = true
			break
		}
	}
	if !foundOpenedLoser {
		t.Fatalf("expected a Losing release with BackendAttempted=true after provider open; releases=%+v", releases)
	}
	// Characterization note: opened losers are released without Settle today
	// (operator incurred cost is erased). Phase 6 settles before residual release.
}

// TestAuthorityTiming_requestHookMutatesAfterAuthoritativeAdmit proves F-04 current
// order: authoritative Admit (reserve) runs, then request-part hooks may mutate the
// call, then Open sees the mutated call while the reservation remains held.
func TestAuthorityTiming_requestHookMutatesAfterAuthoritativeAdmit(t *testing.T) {
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
	admitBeforeHook := atomic.Bool{}
	mutatedMax := 4242
	bus := hooks.New(hooks.Config{
		RequestPartHooks: []sdk.RequestPartHook{
			&timingReqPartHook{
				id:    "mutate-after-admit",
				order: 0,
				fn: func(_ context.Context, call *lipapi.Call, _ sdk.PartMeta) error {
					// Characterize ordering: Admit must already have recorded by the time hooks run.
					if auth.admitCalls.Load() >= 2 { // estimate + authoritative
						admitBeforeHook.Store(true)
					}
					hookRan.Store(true)
					call.Options.MaxOutputTokens = &mutatedMax
					call.Messages = append(call.Messages, lipapi.Message{
						Role:  lipapi.RoleUser,
						Parts: []lipapi.Part{lipapi.TextPart("post-admit mutation")},
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

	out, err := ex.openPlannedCandidate(p, authorityCandidate(), nil, "", false)
	if err != nil {
		t.Fatalf("openPlannedCandidate: %v", err)
	}
	if !out.opened {
		t.Fatal("expected backend to open")
	}
	if !hookRan.Load() {
		t.Fatal("expected request-part hook to run")
	}
	if !admitBeforeHook.Load() {
		t.Fatal("expected authoritative Admit to complete before request-part hook mutation")
	}
	if openedCall.Options.MaxOutputTokens == nil || *openedCall.Options.MaxOutputTokens != mutatedMax {
		t.Fatalf("Open call MaxOutputTokens = %v, want %d (hooks mutate after reserve)", openedCall.Options.MaxOutputTokens, mutatedMax)
	}
	if len(openedCall.Messages) < 2 {
		t.Fatalf("Open call messages = %d, want >= 2 after post-admit mutation", len(openedCall.Messages))
	}
	if out.authority.admissionResult.ReservationID != "reservation-mutate" {
		t.Fatalf("reservation ID = %q, want reservation-mutate still held through Open", out.authority.admissionResult.ReservationID)
	}
	if auth.releaseCalls.Load() != 0 {
		t.Fatalf("release calls = %d, want 0 while opened attempt still owns the reservation", auth.releaseCalls.Load())
	}
}
