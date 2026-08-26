package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/completion"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/localturn"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/prerequest"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/routehint"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcatalog"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolpolicy"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/usage"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// Minimal test stubs for candidate freeze tests

type freezeTestSubmitHook struct {
	id  string
	ord int
}

func (h freezeTestSubmitHook) ID() string                     { return h.id }
func (h freezeTestSubmitHook) Order() int                     { return h.ord }
func (h freezeTestSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h freezeTestSubmitHook) Handle(context.Context, *lipapi.Call, *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type freezeTestGate struct {
	id  string
	ord int
}

func (g freezeTestGate) ID() string                     { return g.id }
func (g freezeTestGate) Order() int                     { return g.ord }
func (g freezeTestGate) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (g freezeTestGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type freezeTestTerminalProvider struct {
	id string
}

func (p freezeTestTerminalProvider) ID() string { return p.id }
func (p freezeTestTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

type freezeTestLocalTurnHandler struct {
	id  string
	ord int
}

func (h freezeTestLocalTurnHandler) ID() string                     { return h.id }
func (h freezeTestLocalTurnHandler) Order() int                     { return h.ord }
func (h freezeTestLocalTurnHandler) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h freezeTestLocalTurnHandler) Match(context.Context, lipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h freezeTestLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

// TestContribute_FailBeforeMutate_TableDriven proves that any failed contribution
// (validation, conflict, combiner, nil reject, unhandled source, empty plugin ID)
// leaves the ContributionSet state byte-for-byte identical before and after.
func TestContribute_FailBeforeMutate_TableDriven(t *testing.T) {
	t.Parallel()

	testMutPlane := feature.Plane[[]string]{
		ID:           "test.fail_before_mutate_table",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			if len(inc) > 0 && inc[0] == "trigger_error" {
				if len(cur) > 0 {
					cur[0] = "TAMPERED_IN_COMBINER"
				}
				return nil, errors.New("combiner intentional error")
			}
			return append(cur, inc...), nil
		},
	}

	testHostOnlyPlane := feature.Plane[[]string]{
		ID:           "test.host_only_plane",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Host: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	}

	tests := []struct {
		name          string
		setup         func(s *feature.ContributionSet)
		contributeFn  func(s *feature.ContributionSet) error
		assertState   func(t *testing.T, f feature.FrozenPlaneSet)
		wantErrTarget error
		wantErrSubstr string
	}{
		{
			name: "validation failure on PlaneToolCallFinalizationMaxArgsBytes leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				require.NoError(t, feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 1024))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				// Negative bytes fails Validate
				return feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-2", -50)
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				assert.Equal(t, 1024, feature.Get(f, feature.PlaneToolCallFinalizationMaxArgsBytes))
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "must be >= 0",
		},
		{
			name: "exclusive conflict on PlaneTerminalDecisionProvider leaves initial provider unchanged",
			setup: func(s *feature.ContributionSet) {
				require.NoError(t, feature.Contribute(s, feature.PlaneTerminalDecisionProvider, "plugin-alpha", terminaldecision.Provider(freezeTestTerminalProvider{id: "provider-alpha"})))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, feature.PlaneTerminalDecisionProvider, "plugin-beta", terminaldecision.Provider(freezeTestTerminalProvider{id: "provider-beta"}))
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				assert.Equal(t, "provider-alpha", feature.Get(f, feature.PlaneTerminalDecisionProvider).ID())
				id, ok := feature.FrozenIdentity(f, feature.PlaneTerminalDecisionProvider)
				assert.True(t, ok)
				assert.Equal(t, "provider-alpha", id)
			},
			wantErrTarget: feature.ErrExclusiveConflict,
			wantErrSubstr: `"provider-alpha" and "provider-beta"`,
		},
		{
			name: "NilReject on PlaneLocalTurnHandlers with nil entry leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				h := freezeTestLocalTurnHandler{id: "turn-h1", ord: 1}
				require.NoError(t, feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-1", []localturn.Handler{h}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-2", []localturn.Handler{nil})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				handlers := feature.Get(f, feature.PlaneLocalTurnHandlers)
				require.Len(t, handlers, 1)
				assert.Equal(t, "turn-h1", handlers[0].ID())
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "must not be nil",
		},
		{
			name: "empty plugin ID leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				h := freezeTestSubmitHook{id: "hook-1", ord: 1}
				require.NoError(t, feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{h}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				h2 := freezeTestSubmitHook{id: "hook-2", ord: 2}
				return feature.Contribute(s, feature.PlaneSubmitHooks, "", []hooks.SubmitHook{h2})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				hooks := feature.Get(f, feature.PlaneSubmitHooks)
				require.Len(t, hooks, 1)
				assert.Equal(t, "hook-1", hooks[0].ID())
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "plugin ID must not be empty",
		},
		{
			name: "mutating combiner failure leaves accumulated slice untampered",
			setup: func(s *feature.ContributionSet) {
				require.NoError(t, feature.Contribute(s, testMutPlane, "plugin-1", []string{"good-1", "good-2"}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, testMutPlane, "plugin-2", []string{"trigger_error"})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				got := feature.Get(f, testMutPlane)
				assert.Equal(t, []string{"good-1", "good-2"}, got)
			},
			wantErrTarget: feature.ErrInvalidContribution,
			wantErrSubstr: "combiner intentional error",
		},
		{
			name: "unsupported source leaves set unchanged",
			setup: func(s *feature.ContributionSet) {
				h := freezeTestSubmitHook{id: "hook-1", ord: 1}
				require.NoError(t, feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{h}))
			},
			contributeFn: func(s *feature.ContributionSet) error {
				return feature.Contribute(s, testHostOnlyPlane, "plugin-2", []string{"bad_source"})
			},
			assertState: func(t *testing.T, f feature.FrozenPlaneSet) {
				t.Helper()
				hooks := feature.Get(f, feature.PlaneSubmitHooks)
				require.Len(t, hooks, 1)
				assert.Equal(t, "hook-1", hooks[0].ID())
			},
			wantErrTarget: feature.ErrUnsupportedSource,
			wantErrSubstr: "source feature is not supported on plane",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := feature.NewContributionSet()
			tc.setup(s)

			frozenBefore := s.Freeze()
			tc.assertState(t, frozenBefore)

			err := tc.contributeFn(s)
			require.Error(t, err)
			if tc.wantErrTarget != nil {
				require.ErrorIs(t, err, tc.wantErrTarget)
			}
			if tc.wantErrSubstr != "" {
				assert.Contains(t, err.Error(), tc.wantErrSubstr)
			}

			frozenAfter := s.Freeze()
			tc.assertState(t, frozenAfter)
			assertFrozenPlaneSetsEqual(t, frozenBefore, frozenAfter)
		})
	}
}

func assertFrozenPlaneSetsEqual(t *testing.T, before, after feature.FrozenPlaneSet) {
	t.Helper()
	assert.Equal(t, before, after, "FrozenPlaneSet before and after failed mutation must be deeply equal")

	assert.Equal(t, feature.Get(before, feature.PlaneSubmitHooks), feature.Get(after, feature.PlaneSubmitHooks))
	assert.Equal(t, feature.Get(before, feature.PlaneRequestPartHooks), feature.Get(after, feature.PlaneRequestPartHooks))
	assert.Equal(t, feature.Get(before, feature.PlaneResponsePartHooks), feature.Get(after, feature.PlaneResponsePartHooks))
	assert.Equal(t, feature.Get(before, feature.PlaneToolReactors), feature.Get(after, feature.PlaneToolReactors))
	assert.Equal(t, feature.Get(before, feature.PlaneSessionOpeners), feature.Get(after, feature.PlaneSessionOpeners))
	assert.Equal(t, feature.Get(before, feature.PlaneWorkspaceResolvers), feature.Get(after, feature.PlaneWorkspaceResolvers))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCatalogFilters), feature.Get(after, feature.PlaneToolCatalogFilters))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCallPolicies), feature.Get(after, feature.PlaneToolCallPolicies))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCallFinalizers), feature.Get(after, feature.PlaneToolCallFinalizers))
	assert.Equal(t, feature.Get(before, feature.PlaneToolCallFinalizationMaxArgsBytes), feature.Get(after, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, feature.Get(before, feature.PlaneRequestTransforms), feature.Get(after, feature.PlaneRequestTransforms))
	assert.Equal(t, feature.Get(before, feature.PlanePreRequestHandlers), feature.Get(after, feature.PlanePreRequestHandlers))
	assert.Equal(t, feature.Get(before, feature.PlaneRouteHintProviders), feature.Get(after, feature.PlaneRouteHintProviders))
	assert.Equal(t, feature.Get(before, feature.PlaneCompletionGates), feature.Get(after, feature.PlaneCompletionGates))
	assert.Equal(t, feature.Get(before, feature.PlaneAttemptTransforms), feature.Get(after, feature.PlaneAttemptTransforms))
	assert.Equal(t, feature.Get(before, feature.PlaneStreamObserverFactories), feature.Get(after, feature.PlaneStreamObserverFactories))
	assert.Equal(t, feature.Get(before, feature.PlaneTrafficObservers), feature.Get(after, feature.PlaneTrafficObservers))
	assert.Equal(t, feature.Get(before, feature.PlaneUsageObservers), feature.Get(after, feature.PlaneUsageObservers))
	assert.Equal(t, feature.Get(before, feature.PlaneRawCaptureSinks), feature.Get(after, feature.PlaneRawCaptureSinks))
	assert.Equal(t, feature.Get(before, feature.PlaneTrafficRedactors), feature.Get(after, feature.PlaneTrafficRedactors))
	assert.Equal(t, feature.Get(before, feature.PlaneCompactionObservers), feature.Get(after, feature.PlaneCompactionObservers))
	assert.Equal(t, feature.Get(before, feature.PlaneCompactionPreservers), feature.Get(after, feature.PlaneCompactionPreservers))
	assert.Equal(t, feature.Get(before, feature.PlaneSecretGuards), feature.Get(after, feature.PlaneSecretGuards))
	assert.Equal(t, feature.Get(before, feature.PlaneLocalTurnHandlers), feature.Get(after, feature.PlaneLocalTurnHandlers))
	assert.Equal(t, feature.Get(before, feature.PlaneTerminalDecisionProvider), feature.Get(after, feature.PlaneTerminalDecisionProvider))

	idBefore, okBefore := feature.FrozenIdentity(before, feature.PlaneTerminalDecisionProvider)
	idAfter, okAfter := feature.FrozenIdentity(after, feature.PlaneTerminalDecisionProvider)
	assert.Equal(t, okBefore, okAfter)
	assert.Equal(t, idBefore, idAfter)
}

// TestContribute_NonNilEmptySemantics_TableDriven proves that contributing an explicit
// empty slice ([]T{}) normalizes to a non-nil empty slice in FrozenPlaneSet, whereas
// uncontributed planes return nil.
func TestContribute_NonNilEmptySemantics_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("PlaneCompletionGates non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		// Uncontributed: Get returns nil
		frozenUnset := s.Freeze()
		gotUnset := feature.Get(frozenUnset, feature.PlaneCompletionGates)
		assert.Nil(t, gotUnset, "uncontributed plane must return nil")

		// Contributed with explicit empty slice []completion.Gate{}
		err := feature.Contribute(s, feature.PlaneCompletionGates, "plugin-1", []completion.Gate{})
		require.NoError(t, err)

		frozenEmpty := s.Freeze()
		gotEmpty := feature.Get(frozenEmpty, feature.PlaneCompletionGates)
		assert.NotNil(t, gotEmpty, "contributed empty slice must return non-nil empty slice")
		assert.Empty(t, gotEmpty)
	})

	t.Run("PlaneSubmitHooks non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneSubmitHooks)
		assert.NotNil(t, got, "contributed empty slice must return non-nil empty slice")
		assert.Empty(t, got)
	})

	t.Run("PlaneSessionOpeners non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneSessionOpeners, "plugin-1", []session.Opener{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneSessionOpeners)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneWorkspaceResolvers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneWorkspaceResolvers, "plugin-1", []workspace.Resolver{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneWorkspaceResolvers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneToolCatalogFilters non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneToolCatalogFilters, "plugin-1", []toolcatalog.Filter{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneToolCatalogFilters)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneToolCallPolicies non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneToolCallPolicies, "plugin-1", []toolpolicy.Policy{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneToolCallPolicies)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneToolCallFinalizers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneToolCallFinalizers, "plugin-1", []toolcall.Finalizer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneToolCallFinalizers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneRequestTransforms non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneRequestTransforms, "plugin-1", []request.Transform{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneRequestTransforms)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlanePreRequestHandlers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlanePreRequestHandlers, "plugin-1", []prerequest.Handler{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlanePreRequestHandlers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneRouteHintProviders non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneRouteHintProviders, "plugin-1", []routehint.Provider{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneRouteHintProviders)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneAttemptTransforms non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneAttemptTransforms, "plugin-1", []request.AttemptTransform{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneAttemptTransforms)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneStreamObserverFactories non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneStreamObserverFactories, "plugin-1", []response.StreamObserverFactory{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneStreamObserverFactories)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneTrafficObservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneTrafficObservers, "plugin-1", []traffic.Observer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneTrafficObservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneUsageObservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneUsageObservers, "plugin-1", []usage.Observer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneUsageObservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneRawCaptureSinks non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneRawCaptureSinks, "plugin-1", []traffic.RawCaptureSink{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneRawCaptureSinks)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneTrafficRedactors non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneTrafficRedactors, "plugin-1", []traffic.Redactor{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneTrafficRedactors)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneCompactionObservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionObservers, "plugin-1", []compaction.Observer{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneCompactionObservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneCompactionPreservers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneCompactionPreservers, "plugin-1", []compaction.Preserver{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneCompactionPreservers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneSecretGuards non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneSecretGuards, "plugin-1", []secretguard.Guard{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneSecretGuards)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("PlaneLocalTurnHandlers non-nil empty preservation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		err := feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-1", []localturn.Handler{})
		require.NoError(t, err)

		frozen := s.Freeze()
		got := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})
}

// TestBackingArrayIsolation_SourceAndGet_TableDriven proves that:
// 1. Mutating a source slice after Contribute does NOT affect the stored candidate or frozen snapshot.
// 2. Mutating a slice returned by Get does NOT corrupt the FrozenPlaneSet backing store.
func TestBackingArrayIsolation_SourceAndGet_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("PlaneCompletionGates backing array isolation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		source := []completion.Gate{
			freezeTestGate{id: "gate-1", ord: 1},
			freezeTestGate{id: "gate-2", ord: 2},
		}

		err := feature.Contribute(s, feature.PlaneCompletionGates, "plugin-1", source)
		require.NoError(t, err)

		// Mutate source slice after Contribute
		source[0] = freezeTestGate{id: "MUTATED_SOURCE", ord: 99}

		frozen := s.Freeze()
		got1 := feature.Get(frozen, feature.PlaneCompletionGates)
		require.Len(t, got1, 2)
		assert.Equal(t, "gate-1", got1[0].ID(), "source mutation after Contribute must not affect frozen store")

		// Mutate slice returned by Get
		got1[0] = freezeTestGate{id: "MUTATED_GET", ord: 999}

		// Subsequent Get must return uncorrupted data
		got2 := feature.Get(frozen, feature.PlaneCompletionGates)
		require.Len(t, got2, 2)
		assert.Equal(t, "gate-1", got2[0].ID(), "mutating slice from Get must not corrupt frozen store")
	})

	t.Run("PlaneSubmitHooks backing array isolation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		source := []hooks.SubmitHook{
			freezeTestSubmitHook{id: "hook-1", ord: 1},
			freezeTestSubmitHook{id: "hook-2", ord: 2},
		}

		err := feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", source)
		require.NoError(t, err)

		// Mutate source
		source[0] = freezeTestSubmitHook{id: "MUTATED_SOURCE", ord: 99}

		frozen := s.Freeze()
		got1 := feature.Get(frozen, feature.PlaneSubmitHooks)
		require.Len(t, got1, 2)
		assert.Equal(t, "hook-1", got1[0].ID())

		// Mutate Get result
		got1[0] = freezeTestSubmitHook{id: "MUTATED_GET", ord: 999}

		got2 := feature.Get(frozen, feature.PlaneSubmitHooks)
		require.Len(t, got2, 2)
		assert.Equal(t, "hook-1", got2[0].ID())
	})

	t.Run("PlaneLocalTurnHandlers backing array isolation", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()

		source := []localturn.Handler{
			freezeTestLocalTurnHandler{id: "turn-1", ord: 1},
		}

		err := feature.Contribute(s, feature.PlaneLocalTurnHandlers, "plugin-1", source)
		require.NoError(t, err)

		source[0] = freezeTestLocalTurnHandler{id: "MUTATED_SOURCE", ord: 99}

		frozen := s.Freeze()
		got1 := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
		require.Len(t, got1, 1)
		assert.Equal(t, "turn-1", got1[0].ID())

		got1[0] = freezeTestLocalTurnHandler{id: "MUTATED_GET", ord: 999}

		got2 := feature.Get(frozen, feature.PlaneLocalTurnHandlers)
		require.Len(t, got2, 1)
		assert.Equal(t, "turn-1", got2[0].ID())
	})
}

// TestFrozenPlaneSet_Immutability_TableDriven proves that calling Freeze multiple times
// or mutating the ContributionSet after Freeze produces independent, immutable snapshots.
func TestFrozenPlaneSet_Immutability_TableDriven(t *testing.T) {
	t.Parallel()

	s := feature.NewContributionSet()

	// Initial contributions across slice, scalar, and exclusive planes
	err := feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		freezeTestSubmitHook{id: "h1", ord: 1},
	})
	require.NoError(t, err)

	err = feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 4096)
	require.NoError(t, err)

	err = feature.Contribute(s, feature.PlaneTerminalDecisionProvider, "plugin-term", terminaldecision.Provider(freezeTestTerminalProvider{id: "term-1"}))
	require.NoError(t, err)

	// Snapshot 1
	snap1 := s.Freeze()

	// Verify Snapshot 1
	require.Len(t, feature.Get(snap1, feature.PlaneSubmitHooks), 1)
	assert.Equal(t, "h1", feature.Get(snap1, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, 4096, feature.Get(snap1, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, "term-1", feature.Get(snap1, feature.PlaneTerminalDecisionProvider).ID())

	// Mutate ContributionSet by adding more items
	err = feature.Contribute(s, feature.PlaneSubmitHooks, "plugin-2", []hooks.SubmitHook{
		freezeTestSubmitHook{id: "h2", ord: 2},
	})
	require.NoError(t, err)

	err = feature.Contribute(s, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-2", 2048)
	require.NoError(t, err)

	// Snapshot 2
	snap2 := s.Freeze()

	// PROVE Snapshot 1 is immutable and uncorrupted
	require.Len(t, feature.Get(snap1, feature.PlaneSubmitHooks), 1)
	assert.Equal(t, "h1", feature.Get(snap1, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, 4096, feature.Get(snap1, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, "term-1", feature.Get(snap1, feature.PlaneTerminalDecisionProvider).ID())

	// Snapshot 2 reflects the new accumulated state
	require.Len(t, feature.Get(snap2, feature.PlaneSubmitHooks), 2)
	assert.Equal(t, "h1", feature.Get(snap2, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, "h2", feature.Get(snap2, feature.PlaneSubmitHooks)[1].ID())
	assert.Equal(t, 2048, feature.Get(snap2, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Equal(t, "term-1", feature.Get(snap2, feature.PlaneTerminalDecisionProvider).ID())

	// Calling Freeze() again without mutating s produces identical results
	snap3 := s.Freeze()
	assert.Equal(t, feature.Get(snap2, feature.PlaneSubmitHooks), feature.Get(snap3, feature.PlaneSubmitHooks))
	assert.Equal(t, feature.Get(snap2, feature.PlaneToolCallFinalizationMaxArgsBytes), feature.Get(snap3, feature.PlaneToolCallFinalizationMaxArgsBytes))
}

// TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn proves that contributing
// to an interface-valued plane where the incoming value is a slice but the combiner returns
// a non-slice, non-nil-capable concrete type does not panic on reflect IsNil checks.
func TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn(t *testing.T) {
	t.Parallel()

	type dummyNonSlice struct {
		name string
	}

	testPlane := feature.Plane[any]{
		ID:           "test.interface_plane_non_slice_return",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc any) (any, error) {
			return dummyNonSlice{name: "combined_result"}, nil
		},
	}

	s := feature.NewContributionSet()
	// Contribute a slice (Kind == Slice, not nil) into interface plane Plane[any]
	err := feature.Contribute(s, testPlane, "plugin-1", any([]string{"item-1", "item-2"}))
	require.NoError(t, err)

	frozen := s.Freeze()
	got := feature.Get(frozen, testPlane)
	require.Equal(t, dummyNonSlice{name: "combined_result"}, got)
}
