package runtime_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/leglifecycle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type recordingMetricsSink struct {
	mu            sync.Mutex
	cancellations []recordedCancellation
}

type recordedCancellation struct {
	CauseClass string
	Mode       lipapi.CancelMode
	Phase      string
	Fallback   string
}

func (s *recordingMetricsSink) OnAttemptRecorded(outcome lipapi.AttemptOutcome, backend string) {}
func (s *recordingMetricsSink) OnBackendOpenDuration(backend string, seconds float64)           {}
func (s *recordingMetricsSink) OnTransportNegotiation(operation lipapi.Operation, mode lipapi.TransportMode, outcome string) {
}
func (s *recordingMetricsSink) OnCancellation(causeClass string, mode lipapi.CancelMode, phase string, fallback string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancellations = append(s.cancellations, recordedCancellation{
		CauseClass: causeClass,
		Mode:       mode,
		Phase:      phase,
		Fallback:   fallback,
	})
}

func (s *recordingMetricsSink) snapshot() []recordedCancellation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedCancellation(nil), s.cancellations...)
}

type obsTrackingStream struct {
	blockRecv   chan struct{}
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
	mode        lipapi.CancelMode
	cancelErr   error
	outcomeSeen bool
	forcedAbort bool
	fallback    string
}

func (s *obsTrackingStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if s.blockRecv != nil {
		select {
		case <-s.blockRecv:
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		}
	}
	return lipapi.Event{}, io.EOF
}

func (s *obsTrackingStream) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	s.cancelCalls.Add(1)
	mode := s.mode
	if mode == "" {
		mode = lipapi.CancelModeTransport
	}
	return lipapi.CancelResult{Mode: mode, Err: s.cancelErr}
}

func (s *obsTrackingStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *obsTrackingStream) OutcomeSeen() bool {
	return s.outcomeSeen
}

func (s *obsTrackingStream) ForcedAbort() bool {
	return s.forcedAbort
}

func (s *obsTrackingStream) CancellationFallback() string {
	if s.fallback != "" {
		return s.fallback
	}
	return "none"
}

func TestPhase6_Runtime_EmitsBoundedCancellationMetrics(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	sink := &recordingMetricsSink{}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
	aLegID := "aleg-obs-metrics-test"

	openStarted := make(chan struct{}, 1)
	blockRecv := make(chan struct{})
	stream := &obsTrackingStream{blockRecv: blockRecv}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 2
	ex.ALegLifecycle = coord
	ex.Metrics = sink
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				select {
				case openStarted <- struct{}{}:
				default:
				}
				return stream, nil
			},
		},
	}

	secretKey := "sk-ant-SECRET-KEY-12345"
	call := &lipapi.Call{
		ID:    "call-obs",
		Route: lipapi.RouteIntent{Selector: "primary:m"},
		Session: lipapi.SessionRef{
			ALegID: aLegID,
		},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi " + secretKey)},
		}},
	}

	streamRes, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = streamRes.Close() })

	recvDone := make(chan error, 1)
	go func() {
		_, rErr := streamRes.Recv(context.Background())
		recvDone <- rErr
	}()

	select {
	case <-openStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("primary Open was not started")
	}

	// Cancel with explicit cause and sensitive detail
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{
		Kind:   leglifecycle.CancelExplicit,
		Detail: "user cancelled with secret: " + secretKey,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockRecv)

	select {
	case <-recvDone:
	case <-time.After(1 * time.Second):
		t.Fatal("Recv did not finish")
	}

	cancels := sink.snapshot()
	if len(cancels) == 0 {
		t.Fatal("expected at least one cancellation metric recorded on sink")
	}

	for _, c := range cancels {
		if strings.Contains(c.CauseClass, secretKey) {
			t.Fatalf("secret key leaked in CauseClass: %q", c.CauseClass)
		}
		if c.CauseClass != "explicit" && c.CauseClass != "context_done" && c.CauseClass != "client_gone" && c.CauseClass != "race_loser" {
			t.Fatalf("unexpected unbounded CauseClass: %q", c.CauseClass)
		}
		if c.Mode != lipapi.CancelModeTransport && c.Mode != lipapi.CancelModeProvider && c.Mode != lipapi.CancelModeCloseOnly && c.Mode != lipapi.CancelModeNone {
			t.Fatalf("unexpected unbounded Mode: %q", c.Mode)
		}
		if c.Phase != "terminal" && c.Phase != "outcome" && c.Phase != "requested" && c.Phase != "forced" {
			t.Fatalf("unexpected unbounded Phase: %q", c.Phase)
		}
		if c.Fallback != "negotiated" && c.Fallback != "legacy" && c.Fallback != "none" {
			t.Fatalf("unexpected unbounded Fallback: %q", c.Fallback)
		}
	}
}

// safeLogBuffer is a thread-safe bytes.Buffer wrapper.
type safeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeLogBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPhase6_Observability_DiagnosticLogs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	logBuf := &safeLogBuffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
	aLegID := "aleg-diag-logs-test"

	openStarted := make(chan struct{}, 1)
	blockRecv := make(chan struct{})
	stream := &obsTrackingStream{blockRecv: blockRecv}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 2
	ex.ALegLifecycle = coord
	ex.Log = logger
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				select {
				case openStarted <- struct{}{}:
				default:
				}
				return stream, nil
			},
		},
	}

	secretKey := "sk-ant-SECRET-BEARER-TOKEN-999"
	rawPrompt := "my super confidential prompt body with password123"
	call := &lipapi.Call{
		ID:    "call-diag",
		Route: lipapi.RouteIntent{Selector: "primary:m"},
		Session: lipapi.SessionRef{
			ALegID: aLegID,
		},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(rawPrompt)},
		}},
	}

	streamRes, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = streamRes.Close() })

	recvDone := make(chan error, 1)
	go func() {
		_, rErr := streamRes.Recv(context.Background())
		recvDone <- rErr
	}()

	select {
	case <-openStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("primary Open was not started")
	}

	// Cancel with explicit cause and sensitive detail
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{
		Kind:   leglifecycle.CancelExplicit,
		Detail: "cancelled with " + secretKey,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockRecv)

	select {
	case <-recvDone:
	case <-time.After(1 * time.Second):
		t.Fatal("Recv did not finish")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "attempt_canceled") {
		t.Fatalf("expected 'attempt_canceled' decision log, got:\n%s", logs)
	}
	if strings.Contains(logs, secretKey) {
		t.Fatalf("secret key leaked in logs: %q", logs)
	}
	if strings.Contains(logs, rawPrompt) {
		t.Fatalf("raw prompt leaked in logs: %q", logs)
	}
	if !strings.Contains(logs, `"cause_class":"explicit"`) {
		t.Fatalf("expected cause_class: explicit in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"cancel_mode":"transport"`) {
		t.Fatalf("expected cancel_mode: transport in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"phase":"terminal"`) {
		t.Fatalf("expected phase: terminal in logs, got:\n%s", logs)
	}
}

// TestPhase6_Observability_ActualModesAndCauses_TableDriven tests that
// provider, transport, and close-only modes, as well as explicit, client-gone,
// context-done, and race-loser causes are observed truthfully with appropriate phases and fallbacks.
func TestPhase6_Observability_ActualModesAndCauses_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		streamMode       lipapi.CancelMode
		outcomeSeen      bool
		forcedAbort      bool
		fallback         string
		cause            lipapi.CancelCause
		wantCauseClass   string
		wantTerminalMode lipapi.CancelMode
		wantFallback     string
		wantPhases       []string
	}{
		{
			name:             "provider_mode_explicit_negotiated",
			streamMode:       lipapi.CancelModeProvider,
			outcomeSeen:      true,
			fallback:         "negotiated",
			cause:            lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: "user cancelled"},
			wantCauseClass:   "explicit",
			wantTerminalMode: lipapi.CancelModeProvider,
			wantFallback:     "negotiated",
			wantPhases:       []string{"requested", "outcome", "terminal"},
		},
		{
			name:             "transport_mode_client_gone_legacy",
			streamMode:       lipapi.CancelModeTransport,
			fallback:         "legacy",
			cause:            lipapi.CancelCause{Kind: lipapi.CancelClientGone, Detail: "client disconnected"},
			wantCauseClass:   "client_gone",
			wantTerminalMode: lipapi.CancelModeTransport,
			wantFallback:     "legacy",
			wantPhases:       []string{"requested", "terminal"},
		},
		{
			name:             "close_only_mode_context_done",
			streamMode:       lipapi.CancelModeCloseOnly,
			fallback:         "none",
			cause:            lipapi.CancelCause{Kind: lipapi.CancelContextDone, Detail: "deadline expired"},
			wantCauseClass:   "context_done",
			wantTerminalMode: lipapi.CancelModeCloseOnly,
			wantFallback:     "none",
			wantPhases:       []string{"requested", "terminal"},
		},
		{
			name:             "forced_abort_mode_explicit",
			streamMode:       lipapi.CancelModeTransport,
			forcedAbort:      true,
			fallback:         "negotiated",
			cause:            lipapi.CancelCause{Kind: lipapi.CancelExplicit, Detail: "forced stop"},
			wantCauseClass:   "explicit",
			wantTerminalMode: lipapi.CancelModeTransport,
			wantFallback:     "negotiated",
			wantPhases:       []string{"requested", "forced", "terminal"},
		},
		{
			name:             "race_loser_cause_transport",
			streamMode:       lipapi.CancelModeTransport,
			fallback:         "none",
			cause:            lipapi.CancelCause{Kind: lipapi.CancelRaceLoser, Detail: "parallel arm lost"},
			wantCauseClass:   "race_loser",
			wantTerminalMode: lipapi.CancelModeTransport,
			wantFallback:     "none",
			wantPhases:       []string{"requested", "terminal"},
		},
		{
			name:             "unknown_cause_maps_to_other",
			streamMode:       lipapi.CancelModeTransport,
			fallback:         "none",
			cause:            lipapi.CancelCause{Kind: lipapi.CancelKind("arbitrary_unknown_kind"), Detail: "unknown reason"},
			wantCauseClass:   "other",
			wantTerminalMode: lipapi.CancelModeTransport,
			wantFallback:     "none",
			wantPhases:       []string{"requested", "terminal"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}

			sink := &recordingMetricsSink{}
			coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
			aLegID := fmt.Sprintf("aleg-%s", tc.name)

			openStarted := make(chan struct{}, 1)
			blockRecv := make(chan struct{})
			stream := &obsTrackingStream{
				blockRecv:   blockRecv,
				mode:        tc.streamMode,
				outcomeSeen: tc.outcomeSeen,
				forcedAbort: tc.forcedAbort,
				fallback:    tc.fallback,
			}

			ex := runtime.TestExecutor()
			ex.Store = st
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.MaxAttempts = 2
			ex.ALegLifecycle = coord
			ex.Metrics = sink
			ex.Backends = map[string]execbackend.Backend{
				"primary": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						select {
						case openStarted <- struct{}{}:
						default:
						}
						return stream, nil
					},
				},
			}

			call := &lipapi.Call{
				ID:      "call-" + tc.name,
				Route:   lipapi.RouteIntent{Selector: "primary:m"},
				Session: lipapi.SessionRef{ALegID: aLegID},
				Messages: []lipapi.Message{{
					Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
				}},
			}

			streamRes, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = streamRes.Close() })

			recvDone := make(chan error, 1)
			go func() {
				_, rErr := streamRes.Recv(context.Background())
				recvDone <- rErr
			}()

			select {
			case <-openStarted:
			case <-time.After(1 * time.Second):
				t.Fatal("primary Open was not started")
			}

			if err := coord.CancelALeg(context.Background(), aLegID, tc.cause); err != nil {
				t.Fatalf("CancelALeg failed: %v", err)
			}

			close(blockRecv)

			select {
			case <-recvDone:
			case <-time.After(1 * time.Second):
				t.Fatal("Recv did not finish")
			}

			cancels := sink.snapshot()
			if len(cancels) == 0 {
				t.Fatal("expected cancellation metrics recorded on sink")
			}

			observedPhases := make(map[string]recordedCancellation)
			phaseCounts := make(map[string]int)
			for _, c := range cancels {
				phaseCounts[c.Phase]++
				observedPhases[c.Phase] = c
				if c.CauseClass != tc.wantCauseClass {
					t.Errorf("got CauseClass %q, want %q", c.CauseClass, tc.wantCauseClass)
				}
			}

			// Ensure no duplicate phase emissions
			for phase, count := range phaseCounts {
				if count > 1 {
					t.Errorf("phase %q emitted %d times, want exactly 1", phase, count)
				}
			}

			// Check all expected phases are present
			for _, wantPhase := range tc.wantPhases {
				c, ok := observedPhases[wantPhase]
				if !ok {
					t.Errorf("expected phase %q was not observed in %v", wantPhase, cancels)
					continue
				}
				if wantPhase == "terminal" {
					if c.Mode != tc.wantTerminalMode {
						t.Errorf("terminal phase got Mode %q, want %q", c.Mode, tc.wantTerminalMode)
					}
					if c.Fallback != tc.wantFallback {
						t.Errorf("terminal phase got Fallback %q, want %q", c.Fallback, tc.wantFallback)
					}
				}
			}
		})
	}
}

// TestPhase6_Observability_NoDuplicatePhaseEvents verifies that each lifecycle point
// emits its corresponding phase exactly once.
func TestPhase6_Observability_NoDuplicatePhaseEvents(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	sink := &recordingMetricsSink{}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
	aLegID := "aleg-no-dup-phases"

	openStarted := make(chan struct{}, 1)
	blockRecv := make(chan struct{})
	stream := &obsTrackingStream{
		blockRecv:   blockRecv,
		mode:        lipapi.CancelModeProvider,
		outcomeSeen: true,
		fallback:    "negotiated",
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 2
	ex.ALegLifecycle = coord
	ex.Metrics = sink
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				select {
				case openStarted <- struct{}{}:
				default:
				}
				return stream, nil
			},
		},
	}

	call := &lipapi.Call{
		ID:      "call-no-dup",
		Route:   lipapi.RouteIntent{Selector: "primary:m"},
		Session: lipapi.SessionRef{ALegID: aLegID},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	streamRes, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = streamRes.Close() })

	recvDone := make(chan error, 1)
	go func() {
		_, rErr := streamRes.Recv(context.Background())
		recvDone <- rErr
	}()

	select {
	case <-openStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("primary Open was not started")
	}

	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{
		Kind: leglifecycle.CancelExplicit,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockRecv)

	select {
	case <-recvDone:
	case <-time.After(1 * time.Second):
		t.Fatal("Recv did not finish")
	}

	cancels := sink.snapshot()
	phaseCounts := make(map[string]int)
	for _, c := range cancels {
		phaseCounts[c.Phase]++
	}

	if phaseCounts["requested"] != 1 {
		t.Errorf("requested phase count = %d, want 1", phaseCounts["requested"])
	}
	if phaseCounts["outcome"] != 1 {
		t.Errorf("outcome phase count = %d, want 1", phaseCounts["outcome"])
	}
	if phaseCounts["terminal"] != 1 {
		t.Errorf("terminal phase count = %d, want 1", phaseCounts["terminal"])
	}
	if phaseCounts["forced"] != 0 {
		t.Errorf("forced phase count = %d, want 0", phaseCounts["forced"])
	}
}

// TestPhase6_Observability_UnopenedAttemptCancellation_EmitsNoneMode verifies that
// an attempt canceled before backend open records mode None and phase requested/terminal.
func TestPhase6_Observability_UnopenedAttemptCancellation_EmitsNoneMode(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	sink := &recordingMetricsSink{}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: 50 * time.Millisecond})
	aLegID := "aleg-unopened-cancel"

	// Pre-cancel ALeg before execute
	if err := coord.CancelALeg(context.Background(), aLegID, leglifecycle.CancelCause{
		Kind: leglifecycle.CancelRaceLoser,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	openCalled := atomic.Bool{}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 2
	ex.ALegLifecycle = coord
	ex.Metrics = sink
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				openCalled.Store(true)
				return &obsTrackingStream{}, nil
			},
		},
	}

	call := &lipapi.Call{
		ID:      "call-unopened",
		Route:   lipapi.RouteIntent{Selector: "primary:m"},
		Session: lipapi.SessionRef{ALegID: aLegID},
		Messages: []lipapi.Message{{
			Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hi")},
		}},
	}

	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error on pre-canceled execute")
	}

	if openCalled.Load() {
		t.Fatal("backend Open must not be called when ALeg is pre-canceled")
	}
}
