package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopgate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguard"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stopguardverify"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/streamrecovery"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

// spyTelemetryRecord captures one structured log/telemetry observation.
type spyTelemetryRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// spyTelemetryHandler is a test-local slog.Handler that captures all structured telemetry.
type spyTelemetryHandler struct {
	mu      sync.Mutex
	records []spyTelemetryRecord
}

func newSpyTelemetryHandler() *spyTelemetryHandler {
	return &spyTelemetryHandler{}
}

func (h *spyTelemetryHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *spyTelemetryHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.records = append(h.records, spyTelemetryRecord{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   attrs,
	})
	return nil
}

func (h *spyTelemetryHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *spyTelemetryHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *spyTelemetryHandler) Records() []spyTelemetryRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]spyTelemetryRecord, len(h.records))
	copy(out, h.records)
	return out
}

func (h *spyTelemetryHandler) HasEventWithAttr(msgSubstr, attrKey, attrVal string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	targetMsg := strings.ToLower(msgSubstr)
	targetVal := strings.ToLower(attrVal)
	for _, rec := range h.records {
		if strings.Contains(strings.ToLower(rec.Message), targetMsg) {
			if v, ok := rec.Attrs[attrKey]; ok {
				valStr := strings.ToLower(fmt.Sprint(v))
				if valStr == targetVal || strings.Contains(valStr, targetVal) {
					return true
				}
			}
		}
	}
	return false
}

func (h *spyTelemetryHandler) CountEventsMatching(msgSubstr string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	target := strings.ToLower(msgSubstr)
	count := 0
	for _, rec := range h.records {
		if strings.Contains(strings.ToLower(rec.Message), target) {
			count++
		}
	}
	return count
}

// Bounded enum vocabulary sets for privacy/cardinality validation.
var (
	allowedCauseEnums = map[string]bool{
		string(stopguard.CauseNormalEnd):              true,
		string(stopguard.CauseEmptyNormalEnd):         true,
		string(stopguard.CauseProviderPause):          true,
		string(stopguard.CauseOutputLimit):            true,
		string(stopguard.CauseTransportEOFPreCommit):  true,
		string(stopguard.CauseTransportEOFPostCommit): true,
		string(stopguard.CauseIdlePreCommit):          true,
		string(stopguard.CauseIdlePostCommit):         true,
		string(stopguard.CausePartialToolCall):        true,
		string(stopguard.CauseRefusalOrFilter):        true,
		string(stopguard.CauseClientCancel):           true,
		string(stopguard.CauseUnknownTerminal):        true,
	}

	allowedVerdictEnums = map[string]bool{
		string(stopguard.VerdictAllowStop): true,
		string(stopguard.VerdictContinue):  true,
		string(stopguard.VerdictNeedsUser): true,
		string(stopguard.VerdictBlocked):   true,
		string(stopguard.VerdictUncertain): true,
	}

	allowedActionEnums = map[string]bool{
		string(stopguard.ActionForwardTerminal):           true,
		string(stopguard.ActionDelegatePreOutputRecovery): true,
		string(stopguard.ActionContinueLeg):               true,
		string(stopguard.ActionSurfaceFailure):            true,
	}
)

// -----------------------------------------------------------------------------
// Requirement 11.1 & 11.4: Bounded Enum Telemetry for Cause, Verdict, Action
// -----------------------------------------------------------------------------

// TestAgentLoopGuard_Telemetry_CandidateCauseBoundedEnum asserts that when a terminal
// candidate arrives, telemetry records the candidate evaluation using bounded cause enums.
// Behavioral RED (Task 9.1): Fails until Task 9.2 wires candidate telemetry into the observer.
func TestAgentLoopGuard_Telemetry_CandidateCauseBoundedEnum(t *testing.T) {
	t.Parallel()

	causesToTest := []struct {
		name      string
		cause     stopguard.Cause
		events    []lipapi.Event
		committed bool
	}{
		{
			name:      "normal_end",
			cause:     stopguard.CauseNormalEnd,
			events:    []lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "done"}, {Kind: lipapi.EventResponseFinished}},
			committed: true,
		},
		{
			name:      "empty_normal_end",
			cause:     stopguard.CauseEmptyNormalEnd,
			events:    []lipapi.Event{{Kind: lipapi.EventResponseFinished}},
			committed: false,
		},
	}

	for _, tc := range causesToTest {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spyHandler := newSpyTelemetryHandler()
			log := slog.New(spyHandler)

			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.Log = log

			fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						return lipapi.NewFixedEventStream(tc.events), nil
					},
				},
			}

			call := &lipapi.Call{
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("run task")}}},
			}

			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			defer func() { _ = stream.Close() }()
			_, _ = collectAll(stream)

			// Assertion: telemetry must record candidate evaluation with bounded cause enum.
			found := spyHandler.HasEventWithAttr("agent_loop_guard_candidate", "cause", string(tc.cause))
			if !found {
				t.Errorf("telemetry missing candidate evaluation event for cause=%q (behavioral RED: pending task 9.2 wiring)", tc.cause)
			}

			// Validate that all recorded causes belong strictly to allowed bounded enums.
			for _, rec := range spyHandler.Records() {
				if strings.Contains(strings.ToLower(rec.Message), "candidate") {
					if c, ok := rec.Attrs["cause"].(string); ok && !allowedCauseEnums[c] {
						t.Errorf("unbounded cause label/attr %q detected in candidate telemetry", c)
					}
				}
			}
		})
	}
}

// TestAgentLoopGuard_Telemetry_VerdictAndRoleBoundedEnums asserts that semantic
// verifier outcomes are emitted with bounded verdict and role labels.
// Behavioral RED (Task 9.1): Fails until Task 9.2 wires verdict telemetry into the observer.
func TestAgentLoopGuard_Telemetry_VerdictAndRoleBoundedEnums(t *testing.T) {
	t.Parallel()

	verdicts := []struct {
		name    string
		verdict stopguard.Verdict
		role    string
	}{
		{
			name:    "allow_stop",
			verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "task complete"},
			role:    "loop_guard",
		},
		{
			name:    "continue",
			verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "run test suite", Reason: "in progress"},
			role:    "loop_guard",
		},
		{
			name:    "needs_user",
			verdict: stopguard.Verdict{Kind: stopguard.VerdictNeedsUser, Reason: "requires user confirmation"},
			role:    "loop_guard",
		},
		{
			name:    "blocked",
			verdict: stopguard.Verdict{Kind: stopguard.VerdictBlocked, Reason: "permission denied externally"},
			role:    "loop_guard",
		},
		{
			name:    "uncertain",
			verdict: stopguard.Verdict{Kind: stopguard.VerdictUncertain, Reason: "unable to determine"},
			role:    "loop_guard",
		},
	}

	for _, tc := range verdicts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spyHandler := newSpyTelemetryHandler()
			log := slog.New(spyHandler)

			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.Log = log

			fv := &fakeGuardVerifier{verdict: tc.verdict}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

			var opens atomic.Int32
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						n := opens.Add(1)
						if n == 1 {
							return lipapi.NewFixedEventStream([]lipapi.Event{
								{Kind: lipapi.EventTextDelta, Delta: "Let me check..."},
								{Kind: lipapi.EventResponseFinished},
							}), nil
						}
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventTextDelta, Delta: " Done."},
							{Kind: lipapi.EventResponseFinished},
						}), nil
					},
				},
			}

			call := &lipapi.Call{
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("check something")}}},
			}

			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			defer func() { _ = stream.Close() }()
			_, _ = collectAll(stream)

			// Assertion: telemetry must record verdict with bounded verdict enum and role.
			foundVerdict := spyHandler.HasEventWithAttr("agent_loop_guard_verdict", "verdict", string(tc.verdict.Kind))
			if !foundVerdict {
				t.Errorf("telemetry missing verdict event for verdict=%q (behavioral RED: pending task 9.2 wiring)", tc.verdict.Kind)
			}

			foundRole := spyHandler.HasEventWithAttr("agent_loop_guard_verdict", "role", tc.role)
			if !foundRole {
				t.Errorf("telemetry missing role=%q in verdict event (behavioral RED: pending task 9.2 wiring)", tc.role)
			}

			// Validate all recorded verdicts belong to allowed bounded set.
			for _, rec := range spyHandler.Records() {
				if strings.Contains(strings.ToLower(rec.Message), "verdict") {
					if v, ok := rec.Attrs["verdict"].(string); ok && !allowedVerdictEnums[v] {
						t.Errorf("unbounded verdict label/attr %q detected in verdict telemetry", v)
					}
				}
			}
		})
	}
}

// TestAgentLoopGuard_Telemetry_ActionDecisionBoundedEnums asserts that guard action
// decisions emit bounded action and reason codes.
// Behavioral RED (Task 9.1): Fails until Task 9.2 wires action telemetry into the observer.
func TestAgentLoopGuard_Telemetry_ActionDecisionBoundedEnums(t *testing.T) {
	t.Parallel()

	actions := []struct {
		name       string
		verdict    stopguard.Verdict
		wantAction stopguard.Action
	}{
		{
			name:       "continue_leg",
			verdict:    stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "continue work", Reason: "in progress"},
			wantAction: stopguard.ActionContinueLeg,
		},
		{
			name:       "forward_terminal",
			verdict:    stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "completed"},
			wantAction: stopguard.ActionForwardTerminal,
		},
	}

	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spyHandler := newSpyTelemetryHandler()
			log := slog.New(spyHandler)

			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.Log = log

			fv := &fakeGuardVerifier{verdict: tc.verdict}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

			var opens atomic.Int32
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						n := opens.Add(1)
						if n == 1 {
							return lipapi.NewFixedEventStream([]lipapi.Event{
								{Kind: lipapi.EventTextDelta, Delta: "Initial text"},
								{Kind: lipapi.EventResponseFinished},
							}), nil
						}
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventTextDelta, Delta: " Continued text"},
							{Kind: lipapi.EventResponseFinished},
						}), nil
					},
				},
			}

			call := &lipapi.Call{
				Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
				Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("work")}}},
			}

			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			defer func() { _ = stream.Close() }()
			_, _ = collectAll(stream)

			// Assertion: telemetry must record action with bounded action enum.
			foundAction := spyHandler.HasEventWithAttr("agent_loop_guard_action", "action", string(tc.wantAction))
			if !foundAction {
				t.Errorf("telemetry missing action event for action=%q (behavioral RED: pending task 9.2 wiring)", tc.wantAction)
			}

			// Validate all recorded actions belong to allowed bounded set.
			for _, rec := range spyHandler.Records() {
				if strings.Contains(strings.ToLower(rec.Message), "action") {
					if a, ok := rec.Attrs["action"].(string); ok && !allowedActionEnums[a] {
						t.Errorf("unbounded action label/attr %q detected in action telemetry", a)
					}
				}
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Requirement 11.4: Reason Codes for No-Progress, Budget Exhaustion & Replay Suppression
// -----------------------------------------------------------------------------

// TestAgentLoopGuard_Telemetry_NoProgressBreakerReasonCode asserts that when repeated
// continuation attempts produce no progress, runtime orchestration telemetry records the no_progress reason code.
// Behavioral RED (Task 9.1): Fails until Task 9.2 wires breaker telemetry into the runtime log.
func TestAgentLoopGuard_Telemetry_NoProgressBreakerReasonCode(t *testing.T) {
	t.Parallel()

	spyHandler := newSpyTelemetryHandler()
	log := slog.New(spyHandler)

	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Log = log

	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{
		Kind:               stopguard.VerdictContinue,
		RemainingObjective: "same work",
		Reason:             "stuck",
	}}
	ex.LoopGuardFactory = NewLoopGuardFactory(stopgate.Ports{Verifier: fv, Now: time.Now}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 5,
		NoProgressLimit:          2,
	})

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventTextDelta, Delta: "same output"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				}
				// Repeated continuation attempts produce identical / empty delta
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventTextDelta, Delta: ""},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("work")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	_, _ = collectAll(stream)

	// Verification of runtime behavior: breaker tripped after NoProgressLimit attempts
	if opens.Load() < 3 {
		t.Fatalf("expected at least 3 opens to trigger no_progress breaker, got %d", opens.Load())
	}

	// Assertion: runtime telemetry must record no_progress reason code upon breaker trip.
	foundNoProgress := spyHandler.HasEventWithAttr("agent_loop_guard_breaker", "reason", "no_progress") ||
		spyHandler.HasEventWithAttr("agent_loop_guard_action", "reason", "no_progress")
	if !foundNoProgress {
		t.Errorf("telemetry missing no_progress breaker reason code event (behavioral RED: pending task 9.2 wiring)")
	}
}

// TestAgentLoopGuard_Telemetry_BudgetExhaustionReasonCode asserts that when max
// semantic continuations is reached, runtime orchestration telemetry records the budget_exhausted reason code.
// Behavioral RED (Task 9.1): Fails until Task 9.2 wires budget exhaustion telemetry into the runtime log.
func TestAgentLoopGuard_Telemetry_BudgetExhaustionReasonCode(t *testing.T) {
	t.Parallel()

	spyHandler := newSpyTelemetryHandler()
	log := slog.New(spyHandler)

	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Log = log

	var verifierCalls atomic.Int32
	fv := &fakeGuardVerifierWithCount{
		fn: func() (stopguard.Verdict, error) {
			n := verifierCalls.Add(1)
			return stopguard.Verdict{
				Kind:               stopguard.VerdictContinue,
				RemainingObjective: fmt.Sprintf("step %d", n),
				Reason:             "progressing",
			}, nil
		},
	}
	ex.LoopGuardFactory = NewLoopGuardFactory(stopgate.Ports{Verifier: fv, Now: time.Now}, stopgate.Config{
		Enabled:                  true,
		ExplicitCompletionPolicy: stopguard.PolicyTrust,
		MaxSemanticContinuations: 1, // Budget of exactly 1 hidden continuation leg
		NoProgressLimit:          5,
	})

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventTextDelta, Delta: fmt.Sprintf("progress delta %d", n)},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("work")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	_, _ = collectAll(stream)

	if opens.Load() != 2 {
		t.Fatalf("expected exactly 2 backend opens (B1 + 1 continuation leg), got %d", opens.Load())
	}

	// Assertion: runtime telemetry must record budget_exhausted reason code.
	foundBudgetExhausted := spyHandler.HasEventWithAttr("agent_loop_guard_action", "reason", "budget_exhausted") ||
		spyHandler.HasEventWithAttr("agent_loop_guard_breaker", "reason", "budget_exhausted")
	if !foundBudgetExhausted {
		t.Errorf("telemetry missing budget_exhausted reason code event (behavioral RED: pending task 9.2 wiring)")
	}
}

// TestAgentLoopGuard_Telemetry_ReplaySuppressionReasonCodes asserts that when replay
// is suppressed for incomplete tool args, opaque thinking state, or unsupported continuation frontend,
// runtime orchestration telemetry records bounded suppression reason codes.
// Behavioral RED (Task 9.1): Fails until Task 9.2 wires replay suppression telemetry into the runtime log.
func TestAgentLoopGuard_Telemetry_ReplaySuppressionReasonCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		backendFn  func(attemptNum int32) lipapi.ManagedEventStream
		callFn     func() *lipapi.Call
		wantReason string
	}{
		{
			name: "incomplete_tool_args",
			backendFn: func(attemptNum int32) lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
					{Kind: lipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "bash"},
					{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"cmd":`},
				})
			},
			callFn: func() *lipapi.Call {
				return &lipapi.Call{
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("task")}}},
				}
			},
			wantReason: "incomplete_args",
		},
		{
			name: "unsupported_opaque_thinking",
			backendFn: func(attemptNum int32) lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
					{Kind: lipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking"}`)},
				})
			},
			callFn: func() *lipapi.Call {
				return &lipapi.Call{
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("task")}}},
				}
			},
			wantReason: "opaque",
		},
		{
			name: "unsupported_continuation_frontend",
			backendFn: func(attemptNum int32) lipapi.ManagedEventStream {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
				})
			},
			callFn: func() *lipapi.Call {
				return &lipapi.Call{
					Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Invocation: lipapi.Invocation{Operation: lipapi.Operation("unknown_custom_op"), DeliveryMode: lipapi.DeliveryModeStreaming},
					Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("task")}}},
				}
			},
			wantReason: "unsupported_continuation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spyHandler := newSpyTelemetryHandler()
			log := slog.New(spyHandler)

			ex := TestExecutor()
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.Log = log
			ex.StreamRecovery = streamrecovery.Config{Enabled: true, EmitWarning: true, AllowPostOutputContinuation: true}

			fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictContinue, RemainingObjective: "work", Reason: "pending"}}
			ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

			var opens atomic.Int32
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					Open: func(_ context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						n := opens.Add(1)
						return tc.backendFn(n), nil
					},
				},
			}

			call := tc.callFn()
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			defer func() { _ = stream.Close() }()
			_, _ = collectAll(stream)

			if opens.Load() != 1 {
				t.Fatalf("%s must not open continuation B2 (opens=%d want 1)", tc.name, opens.Load())
			}

			// Assertion: telemetry must record replay suppression event with bounded reason code.
			foundSuppression := spyHandler.HasEventWithAttr("agent_loop_guard_replay_suppressed", "reason", tc.wantReason) ||
				spyHandler.HasEventWithAttr("agent_loop_guard_action", "reason", tc.wantReason)
			if !foundSuppression {
				t.Errorf("telemetry missing replay suppression event for reason=%q (behavioral RED: pending task 9.2 wiring)", tc.wantReason)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Requirement 11.2: Verifier Latency and Usage/Cost Accounting (Honest Values)
// -----------------------------------------------------------------------------

// TestAgentLoopGuard_Telemetry_VerifierUsageAndLatencyHonestReporting proves that
// verifier observations carry measured latency, actual tokens, actual cost, and honest zero on error.
func TestAgentLoopGuard_Telemetry_VerifierUsageAndLatencyHonestReporting(t *testing.T) {
	t.Parallel()

	t.Run("success_populates_honest_usage_and_latency", func(t *testing.T) {
		t.Parallel()
		var capturedObs stopguardverify.VerifyObservation
		var observerCalls int

		cfg := stopguardverify.AdapterConfig{
			Role:    "loop_guard",
			Timeout: 4 * time.Second,
			Observer: func(obs stopguardverify.VerifyObservation) {
				capturedObs = obs
				observerCalls++
			},
		}

		collected := lipapi.Collected{
			InputTokens:   42,
			OutputTokens:  18,
			TotalTokens:   60,
			CostNanoUnits: 12345,
		}
		collected.Text.WriteString(`{"kind":"continue","remaining_objective":"run tests","reason":"need tests"}`)

		fakeClient := &fakeAuxUsageClient{collected: collected}
		adapter := stopguardverify.NewAdapter(fakeClient, cfg)

		verdict, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
		if err != nil {
			t.Fatalf("Verify error: %v", err)
		}
		if verdict.Kind != stopguard.VerdictContinue {
			t.Fatalf("verdict = %v, want continue", verdict.Kind)
		}

		if observerCalls != 1 {
			t.Fatalf("observer called %d times, want 1", observerCalls)
		}
		if capturedObs.Latency < 0 {
			t.Errorf("latency must be non-negative, got %v", capturedObs.Latency)
		}
		if capturedObs.InputTokens != 42 || capturedObs.OutputTokens != 18 || capturedObs.TotalTokens != 60 {
			t.Errorf("tokens = (%d, %d, %d), want (42, 18, 60)", capturedObs.InputTokens, capturedObs.OutputTokens, capturedObs.TotalTokens)
		}
		if capturedObs.CostNanoUnits != 12345 {
			t.Errorf("cost = %d, want 12345", capturedObs.CostNanoUnits)
		}
		if capturedObs.Err != nil {
			t.Errorf("unexpected error in observation: %v", capturedObs.Err)
		}
	})

	t.Run("failure_reports_honest_zero_usage_and_err", func(t *testing.T) {
		t.Parallel()
		var capturedObs stopguardverify.VerifyObservation
		var observerCalls int

		cfg := stopguardverify.AdapterConfig{
			Role:    "loop_guard",
			Timeout: 4 * time.Second,
			Observer: func(obs stopguardverify.VerifyObservation) {
				capturedObs = obs
				observerCalls++
			},
		}

		fakeClient := &fakeAuxUsageClient{err: errors.New("upstream timeout")}
		adapter := stopguardverify.NewAdapter(fakeClient, cfg)

		verdict, err := adapter.Verify(context.Background(), stopguard.Evidence{Cause: stopguard.CauseNormalEnd})
		if err == nil {
			t.Fatal("expected error from failed verifier")
		}
		if verdict.Kind != stopguard.VerdictUncertain {
			t.Fatalf("failed verifier verdict = %v, want uncertain", verdict.Kind)
		}

		if observerCalls != 1 {
			t.Fatalf("observer called %d times, want 1", observerCalls)
		}
		if capturedObs.InputTokens != 0 || capturedObs.OutputTokens != 0 || capturedObs.TotalTokens != 0 {
			t.Errorf("failure must report 0 tokens, got (%d, %d, %d)", capturedObs.InputTokens, capturedObs.OutputTokens, capturedObs.TotalTokens)
		}
		if capturedObs.CostNanoUnits != 0 {
			t.Errorf("failure must report 0 cost, got %d", capturedObs.CostNanoUnits)
		}
		if capturedObs.Err == nil {
			t.Error("failure must report non-nil error in observation")
		}
	})
}

// -----------------------------------------------------------------------------
// Requirement 9.6 & 11.3: Lineage & Auxiliary Accounting Preservation
// -----------------------------------------------------------------------------

// TestAgentLoopGuard_Telemetry_LineageAndAttributableAccounting asserts that:
// 1. Hidden continuation B-leg retains TraceID and ALegID with a distinct BLegID and incremented Seq in B2BUA attempts store.
// 2. Billing leg observer receives distinct attributable usage records for B1 and B2 (not merged/concealed).
func TestAgentLoopGuard_Telemetry_LineageAndAttributableAccounting(t *testing.T) {
	t.Parallel()

	type billingRecord struct {
		ALegID    string
		BLegID    string
		Seq       int
		Outcome   billing.LegOutcome
		BackendID string
		ModelID   string
	}

	var mu sync.Mutex
	var observedBilling []billingRecord

	billingObs := BillingLegObserverFunc(func(_ context.Context, rec billing.CallLegUsageRecord) {
		mu.Lock()
		defer mu.Unlock()
		observedBilling = append(observedBilling, billingRecord{
			ALegID:    rec.ALegID,
			BLegID:    rec.BLegID,
			Seq:       rec.AttemptSeq,
			Outcome:   rec.Outcome,
			BackendID: rec.BackendID,
			ModelID:   rec.ModelID,
		})
	})

	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingLegObserver = billingObs

	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{Kind: stopguard.VerdictAllowStop, Reason: "complete"}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)
	ex.StreamRecovery = streamrecovery.Config{Enabled: true, IdleTimeout: 500 * time.Millisecond, GracePeriod: 0, EmitWarning: true, AllowPostOutputContinuation: true}

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					// Leg 1: output some text then EOF post-commit
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventResponseStarted},
						{Kind: lipapi.EventMessageStarted},
						{Kind: lipapi.EventTextDelta, Delta: "partial work"},
					}), nil
				}
				// Leg 2: continuation completes
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: " finished"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("do multi-step")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	_, _ = collectAll(stream)

	if opens.Load() != 2 {
		t.Fatalf("expected 2 backend opens (B1 and B2), got %d", opens.Load())
	}

	// 1. Verify B2BUA attempts store preserved distinct lineage for each attempt.
	atts, err := store.LoadAttempts(context.Background(), call.Session.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("expected 2 attempts stored in B2BUA, got %d", len(atts))
	}
	sort.Slice(atts, func(i, j int) bool { return atts[i].Seq < atts[j].Seq })

	if atts[0].BLegID == atts[1].BLegID {
		t.Errorf("B1 and B2 must have distinct BLegIDs in B2BUA, got %q for both", atts[0].BLegID)
	}
	if atts[1].Seq <= atts[0].Seq {
		t.Errorf("B2 Seq (%d) must be greater than B1 Seq (%d)", atts[1].Seq, atts[0].Seq)
	}

	// 2. Verify BillingLegObserver captured distinct attributable records for B1 and B2.
	mu.Lock()
	records := make([]billingRecord, len(observedBilling))
	copy(records, observedBilling)
	mu.Unlock()

	if len(records) != 2 {
		t.Fatalf("expected 2 billing leg records (B1 and B2), got %d", len(records))
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })

	b1 := records[0]
	b2 := records[1]

	if b1.BLegID == "" || b2.BLegID == "" {
		t.Fatalf("billing records must have non-empty BLegIDs: b1=%q, b2=%q", b1.BLegID, b2.BLegID)
	}
	if b1.BLegID == b2.BLegID {
		t.Errorf("B1 and B2 billing records must have distinct BLegIDs, got %q for both", b1.BLegID)
	}
	if b1.ALegID != call.Session.ALegID || b2.ALegID != call.Session.ALegID {
		t.Errorf("billing records ALegID must match call ALegID %q, got b1=%q, b2=%q", call.Session.ALegID, b1.ALegID, b2.ALegID)
	}
	if b2.Seq <= b1.Seq {
		t.Errorf("B2 billing seq (%d) must be greater than B1 billing seq (%d)", b2.Seq, b1.Seq)
	}
	if b1.Outcome == "" || b2.Outcome == "" {
		t.Errorf("billing records must report attributable non-empty outcomes: b1=%v, b2=%v", b1.Outcome, b2.Outcome)
	}
}

// -----------------------------------------------------------------------------
// Requirement 11.5 & Privacy: No High-Cardinality Payload in Labels/Attributes
// -----------------------------------------------------------------------------

// TestAgentLoopGuard_Telemetry_PrivacyHighCardinalityProhibition verifies that:
// 1. Candidate, verdict, and action telemetry events are emitted into the runtime log.
// 2. User prompt text is NEVER used as a metric label or telemetry attribute key/value.
// 3. Candidate assistant text is NEVER used as a metric label or telemetry attribute key/value.
// 4. Tool arguments (which may contain API keys/secrets) NEVER appear in telemetry.
// 5. Verifier reason text and remaining objective NEVER appear in metric labels.
// 6. Recovery instruction prompt NEVER appears in metric labels.
func TestAgentLoopGuard_Telemetry_PrivacyHighCardinalityProhibition(t *testing.T) {
	t.Parallel()

	const (
		secretPrompt             = "SECRET_PROMPT_PAYLOAD_xyz123"
		secretAssistant          = "SECRET_ASSISTANT_PAYLOAD_abc456"
		secretToolArgs           = `{"token":"SECRET_TOOL_TOKEN_789"}`
		secretVerifierReason     = "SECRET_VERIFIER_REASON_REDACT_ME"
		secretRemainingObjective = "SECRET_REMAINING_OBJECTIVE_REDACT_ME"
		secretRecoveryPrompt     = "SECRET_RECOVERY_INSTRUCTION_REDACT_ME"
	)

	sensitivePayloads := []string{
		secretPrompt,
		secretAssistant,
		secretToolArgs,
		"SECRET_TOOL_TOKEN_789",
	}

	sensitiveReasonTexts := []string{
		secretVerifierReason,
		secretRemainingObjective,
		secretRecoveryPrompt,
	}

	spyHandler := newSpyTelemetryHandler()
	log := slog.New(spyHandler)

	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Log = log

	fv := &fakeGuardVerifier{verdict: stopguard.Verdict{
		Kind:               stopguard.VerdictContinue,
		Reason:             secretVerifierReason,
		RemainingObjective: secretRemainingObjective,
	}}
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(fv)

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventTextDelta, Delta: secretAssistant},
						{Kind: lipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "test_tool"},
						{Kind: lipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: secretToolArgs},
						{Kind: lipapi.EventToolCallFinished, ToolCallID: "c1"},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventTextDelta, Delta: " done"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart(secretPrompt)}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()
	_, _ = collectAll(stream)

	// Privacy requirement: The test must require that guard telemetry events WERE emitted
	// (candidate, verdict, action) before verifying that forbidden content is absent.
	// This ensures the test is behavioral RED now (pending Task 9.2 telemetry wiring)
	// and becomes a non-vacuous GREEN once Task 9.2 emits safe telemetry.
	candidateEvents := spyHandler.CountEventsMatching("agent_loop_guard_candidate")
	verdictEvents := spyHandler.CountEventsMatching("agent_loop_guard_verdict")
	actionEvents := spyHandler.CountEventsMatching("agent_loop_guard_action")

	if candidateEvents == 0 {
		t.Errorf("privacy audit requires candidate telemetry event emission (behavioral RED: pending task 9.2 wiring)")
	}
	if verdictEvents == 0 {
		t.Errorf("privacy audit requires verdict telemetry event emission (behavioral RED: pending task 9.2 wiring)")
	}
	if actionEvents == 0 {
		t.Errorf("privacy audit requires action telemetry event emission (behavioral RED: pending task 9.2 wiring)")
	}

	// Audit all telemetry/metric records for leakage of forbidden high-cardinality/sensitive strings.
	records := spyHandler.Records()
	for _, rec := range records {
		// 1. Sensitive payloads (prompts, assistant outputs, tool args, keys) must never appear in any message or attribute
		for _, secret := range sensitivePayloads {
			if strings.Contains(rec.Message, secret) {
				t.Fatalf("privacy violation: payload secret %q found in telemetry message %q", secret, rec.Message)
			}
			for k, v := range rec.Attrs {
				if strings.Contains(k, secret) {
					t.Fatalf("privacy violation: payload secret %q found in attribute key %q", secret, k)
				}
				vs := fmt.Sprint(v)
				if strings.Contains(vs, secret) {
					t.Fatalf("privacy violation: payload secret %q leaked into attribute %s=%q", secret, k, vs)
				}
			}
		}

		// 2. Sensitive reason text / remaining objective / recovery prompt must never be used as metric labels
		for k, v := range rec.Attrs {
			for _, secret := range sensitiveReasonTexts {
				if strings.Contains(k, secret) {
					t.Fatalf("privacy violation: reason secret %q found in attribute key %q", secret, k)
				}
				if k == "cause" || k == "verdict" || k == "role" || k == "action" || k == "outcome" || k == "reason_code" {
					vs := fmt.Sprint(v)
					if strings.Contains(vs, secret) {
						t.Fatalf("privacy violation: reason secret %q leaked into metric label %s=%q", secret, k, vs)
					}
				}
			}
		}
	}
}

// -----------------------------------------------------------------------------
// Requirement 11.5: Operator Visibility vs Single A-Side User Turn Integrity
// -----------------------------------------------------------------------------

// TestAgentLoopGuard_Telemetry_OperatorVisibilityNotFabricatedATurns asserts that:
// 1. Internal verifier calls and continuation B-legs are operator-visible (internal role/lineage).
// 2. The logical A-side client observes exactly 1 continuous response with exactly 1 final terminal.
// 3. No synthetic user-authored turn is fabricated or exposed on the A-side stream.
func TestAgentLoopGuard_Telemetry_OperatorVisibilityNotFabricatedATurns(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)

	var verifierCallCount atomic.Int32
	var capturedAuxRole string
	var capturedAuxSessionMode auxiliary.SessionMode
	var capturedAuxVisibility string

	fakeAuxClient := &fakeAuxClientFunc{
		collectFn: func(ctx context.Context, req auxiliary.Request) (lipapi.Collected, error) {
			n := verifierCallCount.Add(1)
			capturedAuxRole = req.Role
			capturedAuxSessionMode = req.SessionMode
			capturedAuxVisibility = req.Visibility

			var c lipapi.Collected
			c.InputTokens = 10
			c.OutputTokens = 5
			if n == 1 {
				c.Text.WriteString(`{"kind":"continue","remaining_objective":"complete step 2","reason":"in progress"}`)
			} else {
				c.Text.WriteString(`{"kind":"allow_stop","reason":"step 2 done"}`)
			}
			return c, nil
		},
	}

	adapter := stopguardverify.NewAdapter(fakeAuxClient, stopguardverify.AdapterConfig{
		Role:    "loop_guard",
		Timeout: 4 * time.Second,
	})
	ex.LoopGuardFactory = newLoopGuardFactoryForTest(adapter)

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ lipapi.Call, _ routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				n := opens.Add(1)
				if n == 1 {
					return lipapi.NewFixedEventStream([]lipapi.Event{
						{Kind: lipapi.EventTextDelta, Delta: "Step 1 done."},
						{Kind: lipapi.EventResponseFinished},
					}), nil
				}
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventTextDelta, Delta: " Step 2 done."},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &lipapi.Call{
		Route:      lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions, DeliveryMode: lipapi.DeliveryModeStreaming},
		Messages:   []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("run both steps")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	defer func() { _ = stream.Close() }()

	events, err := collectAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collectAll: %v", err)
	}

	// 1. Verifier was invoked internally with proper operator visibility contracts.
	if verifierCallCount.Load() != 2 {
		t.Fatalf("expected 2 verifier calls (1 for B1 continue, 1 for B2 allow_stop), got %d", verifierCallCount.Load())
	}
	if capturedAuxRole != "loop_guard" {
		t.Errorf("verifier role = %q, want %q", capturedAuxRole, "loop_guard")
	}
	if capturedAuxSessionMode != auxiliary.SessionModeDetached {
		t.Errorf("verifier session mode = %v, want %v", capturedAuxSessionMode, auxiliary.SessionModeDetached)
	}
	if capturedAuxVisibility != stopguardverify.Visibility {
		t.Errorf("verifier visibility = %q, want %q", capturedAuxVisibility, stopguardverify.Visibility)
	}

	// 2. Exactly 1 final terminal event reached the A-side client.
	terminals := countTerminal(events)
	if terminals != 1 {
		t.Fatalf("A-side client must observe exactly 1 final terminal, got %d", terminals)
	}

	// 3. Output from B1 and B2 was combined seamlessly for the A-side.
	var fullText strings.Builder
	for _, ev := range events {
		if ev.Kind == lipapi.EventTextDelta {
			fullText.WriteString(ev.Delta)
		}
	}
	if fullText.String() != "Step 1 done. Step 2 done." {
		t.Fatalf("combined text = %q, want %q", fullText.String(), "Step 1 done. Step 2 done.")
	}

	// 4. Verify no synthetic user message or recovery instruction was exposed in A-side stream events.
	for _, ev := range events {
		if ev.Item != nil && ev.Item.Role == lipapi.RoleUser {
			t.Fatalf("A-side stream leaked internal message as a user turn: %+v", ev.Item)
		}
	}
}

// fakeAuxUsageClient implements auxiliary.Client for testing usage/latency observation.
type fakeAuxUsageClient struct {
	collected lipapi.Collected
	err       error
}

func (f *fakeAuxUsageClient) Collect(context.Context, auxiliary.Request) (lipapi.Collected, error) {
	return f.collected, f.err
}

func (f *fakeAuxUsageClient) Stream(context.Context, auxiliary.Request) (lipapi.EventStream, error) {
	return nil, f.err
}

// fakeAuxClientFunc is a test helper adapting a function to auxiliary.Client.
type fakeAuxClientFunc struct {
	collectFn func(ctx context.Context, req auxiliary.Request) (lipapi.Collected, error)
}

func (f *fakeAuxClientFunc) Collect(ctx context.Context, req auxiliary.Request) (lipapi.Collected, error) {
	if f.collectFn != nil {
		return f.collectFn(ctx, req)
	}
	return lipapi.Collected{}, nil
}

func (f *fakeAuxClientFunc) Stream(ctx context.Context, req auxiliary.Request) (lipapi.EventStream, error) {
	return nil, errors.New("not implemented in fakeAuxClientFunc")
}
