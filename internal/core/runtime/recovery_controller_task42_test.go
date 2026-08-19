package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

var (
	errTask42Admission = errors.New("task42: admission failed")
	errTask42Parallel  = errors.New("task42: parallel arms failed")
)

func TestTask42RecoveryReplacementErrorPrecedence(t *testing.T) {
	t.Parallel()

	const selector = "backend:model"
	transportWant := &lipapi.TransportRejectError{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Mode:      lipapi.TransportModeStreaming,
	}
	capabilityWant := &lipapi.RejectError{Missing: []lipapi.Capability{lipapi.CapabilityVision}}

	cases := []struct {
		name      string
		configure func(*recoveryController)
		check     func(*testing.T, error)
	}{
		{
			name: "transport rejection wins",
			configure: func(r *recoveryController) {
				r.lastHardTransportReject = lipapi.TransportNegotiationResult{
					Kind:      lipapi.NegotiationReject,
					Operation: transportWant.Operation,
					Mode:      transportWant.Mode,
				}
				r.lastAdmissionErr = errTask42Admission
				r.lastHardReject = lipapi.NegotiationResult{Kind: lipapi.NegotiationReject, Missing: capabilityWant.Missing}
				r.isContextLimitExhaustion = true
				r.transformExcludes.noteTransform("not-representable")
				r.lastParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				var got *lipapi.TransportRejectError
				if !errors.As(err, &got) || !reflect.DeepEqual(got, transportWant) {
					t.Fatalf("error = %v, want transport reject %+v", err, transportWant)
				}
				if !errors.Is(err, lipapi.ErrTransportReject) {
					t.Fatalf("error = %v, want ErrTransportReject", err)
				}
			},
		},
		{
			name: "admission failure follows transport",
			configure: func(r *recoveryController) {
				r.lastAdmissionErr = errTask42Admission
				r.lastHardReject = lipapi.NegotiationResult{Kind: lipapi.NegotiationReject, Missing: capabilityWant.Missing}
				r.isContextLimitExhaustion = true
				r.transformExcludes.noteTransform("not-representable")
				r.lastParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				if err != errTask42Admission {
					t.Fatalf("error = %v, want independent admission error %v", err, errTask42Admission)
				}
			},
		},
		{
			name: "capability rejection follows admission",
			configure: func(r *recoveryController) {
				r.lastHardReject = lipapi.NegotiationResult{Kind: lipapi.NegotiationReject, Missing: capabilityWant.Missing}
				r.isContextLimitExhaustion = true
				r.transformExcludes.noteTransform("not-representable")
				r.lastParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				var got *lipapi.RejectError
				if !errors.As(err, &got) || !reflect.DeepEqual(got, capabilityWant) {
					t.Fatalf("error = %v, want capability reject %+v", err, capabilityWant)
				}
				if !errors.Is(err, lipapi.ErrCapabilityReject) {
					t.Fatalf("error = %v, want ErrCapabilityReject", err)
				}
			},
		},
		{
			name: "context limit exhaustion follows capability",
			configure: func(r *recoveryController) {
				r.isContextLimitExhaustion = true
				r.transformExcludes.noteTransform("not-representable")
				r.lastParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				if err != lipapi.ErrAllCandidatesContextLimitExceeded {
					t.Fatalf("error = %v, want context-limit sentinel", err)
				}
			},
		},
		{
			name: "transform exclusion follows context limit",
			configure: func(r *recoveryController) {
				r.transformExcludes.noteTransform("not-representable")
				r.lastParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				if err != lipapi.ErrAllCandidatesExcluded {
					t.Fatalf("error = %v, want aggregate transform-exclusion sentinel", err)
				}
			},
		},
		{
			name: "parallel failure follows transform exclusion",
			configure: func(r *recoveryController) {
				r.lastParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				if err != errTask42Parallel {
					t.Fatalf("error = %v, want independent parallel failure %v", err, errTask42Parallel)
				}
			},
		},
		{
			name: "routing no-eligible error is final fallback",
			check: func(t *testing.T, err error) {
				if !errors.Is(err, routing.ErrNoEligibleCandidate) {
					t.Fatalf("error = %v, want wrapped routing.ErrNoEligibleCandidate", err)
				}
				want := "executor: expand failover: routing: no eligible route candidate"
				if err.Error() != want {
					t.Fatalf("error = %q, want exact wrapper %q", err, want)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := routing.Parse(selector)
			if err != nil {
				t.Fatalf("parse selector: %v", err)
			}
			ex := TestExecutor()
			var opens atomic.Int32
			ex.Backends = map[string]execbackend.Backend{
				"backend": {
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						opens.Add(1)
						return nil, errors.New("task42: backend opened unexpectedly")
					},
				},
			}
			r := &recoveryController{
				opener:            newReplacementOpener(ex, nil, nil),
				streamRecovery:    ex.StreamRecovery,
				sel:               sel,
				session:           &routing.SessionRoutingState{},
				excluded:          map[string]struct{}{selector: {}},
				rng:               routing.NewSeededRng(42),
				transformExcludes: transformExcludeTracker{},
			}
			if tc.configure != nil {
				tc.configure(r)
			}
			_, gotErr := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), nil, false)
			if gotErr == nil {
				t.Fatal("openReplacement error = nil, want no-eligible precedence result")
			}
			tc.check(t, gotErr)
			if got := opens.Load(); got != 0 {
				t.Fatalf("backend opens = %d, want 0 after replacement exhaustion", got)
			}
		})
	}
}

func TestTask42RecoveryBoundaryPassesRetiredPriorBeforeOpen(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	r := &recoveryController{opener: func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		if !req.prior.retired {
			t.Fatal("replacement opener received an unretired prior attempt")
		}
		opens.Add(1)
		return replacementOpenResult{opened: true}, nil
	}}
	prior := &attemptSession{authority: authorityLifecycle{
		control: &authorityLifecycleControl{terminal: authorityTerminalReleased},
	}}
	if _, err := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), prior, false); err != nil {
		t.Fatalf("openReplacement: %v", err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("replacement opener calls = %d, want 1", got)
	}
}

func TestTask42RecoveryBoundaryNeverOpensCommittedOrUnretired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		terminal *turnTerminal
		prior    *attemptSession
		wantErr  error
	}{
		{
			name:     "committed turn",
			terminal: func() *turnTerminal { terminal := newTurnTerminal(); terminal.markCommitted(nil); return terminal }(),
			wantErr:  errRecoveryTurnCommitted,
		},
		{
			name: "unretired prior attempt",
			prior: &attemptSession{authority: newAuthorityLifecycle(nil, nil, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(1),
				admissionResult: authorityapp.AdmissionResult{Allowed: true, Reserved: true},
			}, authorityCandidate())},
			wantErr: errRecoveryPriorAttemptNotRetired,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opens atomic.Int32
			r := &recoveryController{opener: func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
				opens.Add(1)
				return replacementOpenResult{opened: true}, nil
			}}
			_, err := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), tc.prior, tc.terminal != nil && tc.terminal.committed())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("openReplacement error = %v, want %v", err, tc.wantErr)
			}
			if got := opens.Load(); got != 0 {
				t.Fatalf("opener calls = %d, want 0 for %s", got, tc.name)
			}
		})
	}
}

func TestTask42MandatoryRecorderCommittedNoReplacementOpen(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.SecureSessionRecordingMandatory = true
	var opens atomic.Int32
	recovery := &recoveryController{opener: func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
		opens.Add(1)
		return replacementOpenResult{opened: true}, nil
	}}
	s := &retryRecvStream{
		terminal:         newTurnTerminal(),
		recovery:         recovery,
		responsePipeline: &responsePipeline{recordingOutcome: responseRecordingMandatoryPostCommitFailure},
		attempt:          testAttemptSlot(b2bua.BLegRecord{}, routing.AttemptCandidate{Key: "committed-candidate"}, authorityLifecycle{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID: "task42-mandatory",
			aLegID:  "task42-a-leg",
		}),
	}
	bindTestRuntimeOwners(s, ex)
	s.terminal.markCommitted(s.attempt.snapshot())
	request := s.facts.terminalFacts()
	request.replacementBlocked = true
	_, err := s.recovery.tryReplacementIteration(context.Background(), request, s.attempt.require(), s.terminal.committed())
	if err == nil {
		t.Fatal("tryReplacementIteration error = nil, want committed mandatory-recorder failure")
	}
	var got *lipapi.UpstreamFailureError
	if !errors.As(err, &got) || got.Phase != lipapi.PhasePostOutput || got.Recoverable {
		t.Fatalf("error = %v, want non-recoverable post-output UpstreamFailureError", err)
	}
	if got.Reason != "secure session mandatory recorder failure after committed output" {
		t.Fatalf("error reason = %q, want mandatory recorder reason", got.Reason)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("replacement opener calls = %d, want 0 after committed mandatory-recorder failure", got)
	}
}
