package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/largebody"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
)

// item5RecordingSpy captures RecordPostHookStreamEvent calls and allows injecting errors.
type item5RecordingSpy struct {
	mu          sync.Mutex
	events      []app.StreamEventRecordInput
	recordErr   error
	failOnIndex int
	callCount   int
}

func (s *item5RecordingSpy) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return nil
}

func (s *item5RecordingSpy) RecordPostHookStreamEvent(_ context.Context, in app.StreamEventRecordInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.callCount
	s.callCount++
	if s.recordErr != nil && (s.failOnIndex < 0 || idx == s.failOnIndex) {
		return s.recordErr
	}
	s.events = append(s.events, in)
	return nil
}

func (s *item5RecordingSpy) recordedEvents() []app.StreamEventRecordInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]app.StreamEventRecordInput, len(s.events))
	copy(copied, s.events)
	return copied
}

func (s *item5RecordingSpy) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

// TestItem5_SecureRecorder_Differential verifies Item 5:
// 1. Secure recorder active + wire-eligible large request:
//   - Pre-fix: wire declines with DeclineReasonAuthorityBlocker (proves always-ineligible production state).
//   - Post-fix: wire accepts, and recorder observes the identical client-facing event sequence as canonical path.
//
// 2. Pre-output failure policy:
//   - When mandatory recorder fails pre-output, both wire and canonical abort identically with the recorder error.
//
// 3. Recorder absent (nil):
//   - When recorder is nil, wire remains eligible (no-op path) and runs successfully.
func TestItem5_SecureRecorder_Differential(t *testing.T) {
	testScript := []lipapi.Event{
		{Kind: lipapi.EventResponseStarted},
		{Kind: lipapi.EventMessageStarted},
		{Kind: lipapi.EventTextDelta, Delta: "hello "},
		{Kind: lipapi.EventTextDelta, Delta: "world"},
		{Kind: lipapi.EventResponseFinished},
	}

	t.Run("SecureRecorder_Active_WireAcceptsAndRecordsIdenticalToCanonical", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)

		spyWire := &item5RecordingSpy{}
		ex.SecureSessionRecorder = spyWire
		ex.SecureSessionRecordingMandatory = true

		ex.Backends = map[string]execbackend.Backend{
			"default": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					return lipapi.CloseOnlyManagedStream{Stream: lipapi.NewFixedEventStream(testScript)}, nil
				},
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.CloseOnlyManagedStream{Stream: lipapi.NewFixedEventStream(testScript)}, nil
				},
			},
		}
		ex.Rand = routing.NewSeededRng(1)

		census := largebody.NewStandardDependencyCensus("gen-1")
		census.AddPort("security.session_recorder", true)
		ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-item5"})
		ctx = largebody.WithWireIdentity(ctx, "req-item5-wire", "trace-item5-wire")
		rawJSON := `{"model":"gpt-4o","messages":[{"role":"user","content":"item5 test"}]}`
		proof := makeBlocker2ValidProof("openai-chat", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)
		proof.Source = largebody.NewSourceDigest(sha256.Sum256([]byte(rawJSON)))
		proof.BodyBytes = int64(len(rawJSON))

		assessment, err := ex.AssessLargeBody(ctx, proof)
		require.NoError(t, err)

		// RED assertion: Under pre-fix (Blocker 2 conservative fix in HEAD), wire declines because
		// security.session_recorder is DependencyClassBlocker.
		// Under post-fix, wire accepts because security.session_recorder is reclassified as DependencyClassWireSafe.
		require.Equal(t, largebody.AssessmentDecisionAccept, assessment.Decision,
			"wire assessment must accept when only security.session_recorder port is occupied (item 5)")
		require.Equal(t, largebody.DeclineReasonNone, assessment.Reason)

		// Execute via wire path
		src := newTestSource(rawJSON)
		assessment.WireRequest.CandidateModel = "default:gpt-4o"
		res, err := ex.ExecuteLargeBody(ctx, assessment, src)
		require.NoError(t, err)
		defer res.Stream.Close()

		var wireEvents []lipapi.Event
		for {
			ev, rerr := res.Stream.Recv(ctx)
			if rerr != nil {
				break
			}
			wireEvents = append(wireEvents, ev)
		}
		require.NotEmpty(t, wireEvents, "wire execution must produce events")
		wireRecorded := spyWire.recordedEvents()
		require.NotEmpty(t, wireRecorded, "wire execution must exercise secure session recorder")

		// Now execute canonical path with identical backend script
		spyCanonical := &item5RecordingSpy{}
		ex.SecureSessionRecorder = spyCanonical
		canonicalCtx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-item5"})
		canonicalCall := &lipapi.Call{
			Route: lipapi.RouteIntent{Selector: "default:gpt-4o"},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("item5 test")},
			}},
		}
		cStream, err := ex.Execute(canonicalCtx, canonicalCall)
		require.NoError(t, err)
		defer cStream.Close()

		var canonicalEvents []lipapi.Event
		for {
			ev, rerr := cStream.Recv(canonicalCtx)
			if rerr != nil {
				break
			}
			canonicalEvents = append(canonicalEvents, ev)
		}
		require.NotEmpty(t, canonicalEvents, "canonical execution must produce events")
		canonicalRecorded := spyCanonical.recordedEvents()
		require.NotEmpty(t, canonicalRecorded, "canonical execution must exercise secure session recorder")

		// Differential assertion: recorder observes identical event sequence
		require.Equal(t, len(canonicalRecorded), len(wireRecorded),
			"wire recorder call count must match canonical recorder call count")

		require.NotEmpty(t, wireRecorded[0].SessionID, "wire session ID must be set")
		require.NotEmpty(t, canonicalRecorded[0].SessionID, "canonical session ID must be set")

		for i := range canonicalRecorded {
			cRec := canonicalRecorded[i]
			wRec := wireRecorded[i]
			assert.Equal(t, cRec.EventKind, wRec.EventKind, "event[%d] kind mismatch", i)
			assert.Equal(t, cRec.EventPayloadJSON, wRec.EventPayloadJSON, "event[%d] payload mismatch", i)
			assert.Equal(t, cRec.BackendID, wRec.BackendID, "event[%d] backend mismatch", i)
			assert.Equal(t, wireRecorded[0].SessionID, wRec.SessionID, "wire event[%d] session ID must match attempt session", i)
			assert.Equal(t, canonicalRecorded[0].SessionID, cRec.SessionID, "canonical event[%d] session ID must match attempt session", i)
			assert.Equal(t, cRec.Policy, wRec.Policy, "event[%d] policy mismatch", i)
			assert.Equal(t, cRec.IsUsageEvent, wRec.IsUsageEvent, "event[%d] isUsageEvent mismatch", i)
		}
	})

	t.Run("SecureRecorder_MandatoryFailure_PreOutputAbortsIdentically", func(t *testing.T) {
		recErr := errors.New("mandatory secure recorder pre-output storage failure")

		// Wire path failure
		exWire, _, _ := setupTestExecutor(t)
		spyWire := &item5RecordingSpy{recordErr: recErr, failOnIndex: 0}
		exWire.SecureSessionRecorder = spyWire
		exWire.SecureSessionRecordingMandatory = true
		exWire.Backends = map[string]execbackend.Backend{
			"default": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					return lipapi.CloseOnlyManagedStream{Stream: lipapi.NewFixedEventStream(testScript)}, nil
				},
			},
		}

		census := largebody.NewStandardDependencyCensus("gen-1")
		census.AddPort("security.session_recorder", true)
		exWire.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-item5-fail"})
		ctx = largebody.WithWireIdentity(ctx, "req-item5-fail", "trace-item5-fail")
		rawJSON := `{"model":"gpt-4o","messages":[{"role":"user","content":"fail test"}]}`
		proof := makeBlocker2ValidProof("openai-chat", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)
		proof.Source = largebody.NewSourceDigest(sha256.Sum256([]byte(rawJSON)))
		proof.BodyBytes = int64(len(rawJSON))

		assessment, err := exWire.AssessLargeBody(ctx, proof)
		require.NoError(t, err)
		require.Equal(t, largebody.AssessmentDecisionAccept, assessment.Decision)

		src := newTestSource(rawJSON)
		assessment.WireRequest.CandidateModel = "default:gpt-4o"
		res, err := exWire.ExecuteLargeBody(ctx, assessment, src)
		require.NoError(t, err)
		defer res.Stream.Close()

		_, wireRecvErr := res.Stream.Recv(ctx)
		require.Error(t, wireRecvErr, "wire execution must abort on mandatory recorder failure")
		require.ErrorIs(t, wireRecvErr, recErr)

		// Canonical path failure
		exCanonical, _, _ := setupTestExecutor(t)
		spyCanonical := &item5RecordingSpy{recordErr: recErr, failOnIndex: 0}
		exCanonical.SecureSessionRecorder = spyCanonical
		exCanonical.SecureSessionRecordingMandatory = true
		exCanonical.Backends = map[string]execbackend.Backend{
			"default": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call lipapi.Call, cand routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
					return lipapi.CloseOnlyManagedStream{Stream: lipapi.NewFixedEventStream(testScript)}, nil
				},
			},
		}

		cCall := &lipapi.Call{
			Route: lipapi.RouteIntent{Selector: "default:gpt-4o"},
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart("fail test")},
			}},
		}
		cStream, err := exCanonical.Execute(ctx, cCall)
		require.NoError(t, err)
		defer cStream.Close()

		_, canonicalRecvErr := cStream.Recv(ctx)
		require.Error(t, canonicalRecvErr, "canonical execution must abort on mandatory recorder failure")
		require.ErrorIs(t, canonicalRecvErr, recErr)
	})

	t.Run("SecureRecorder_Nil_WireEligibleAndNoOp", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		ex.SecureSessionRecorder = nil

		ex.Backends = map[string]execbackend.Backend{
			"default": {
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (lipapi.ManagedEventStream, error) {
					return lipapi.CloseOnlyManagedStream{Stream: lipapi.NewFixedEventStream(testScript)}, nil
				},
			},
		}

		census := largebody.NewStandardDependencyCensus("gen-1")
		census.AddPort("security.session_recorder", false)
		ex.LargeBodyAssessor = makeBlocker2Assessor(t, "gen-1", "dom-gen-1", census)

		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-item5-nil"})
		ctx = largebody.WithWireIdentity(ctx, "req-item5-nil", "trace-item5-nil")
		rawJSON := `{"model":"gpt-4o","messages":[{"role":"user","content":"nil test"}]}`
		proof := makeBlocker2ValidProof("openai-chat", lipapi.OperationOpenAIChatCompletions, lipapi.DeliveryModeStreaming)
		proof.Source = largebody.NewSourceDigest(sha256.Sum256([]byte(rawJSON)))
		proof.BodyBytes = int64(len(rawJSON))

		assessment, err := ex.AssessLargeBody(ctx, proof)
		require.NoError(t, err)
		require.Equal(t, largebody.AssessmentDecisionAccept, assessment.Decision)

		src := newTestSource(rawJSON)
		assessment.WireRequest.CandidateModel = "default:gpt-4o"
		res, err := ex.ExecuteLargeBody(ctx, assessment, src)
		require.NoError(t, err)
		defer res.Stream.Close()

		var received []lipapi.Event
		for {
			ev, rerr := res.Stream.Recv(ctx)
			if rerr != nil {
				break
			}
			received = append(received, ev)
		}
		require.NotEmpty(t, received, "wire execution must produce events with nil recorder")
	})
}
