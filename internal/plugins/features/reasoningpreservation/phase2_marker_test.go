package reasoningpreservation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
)

const phase2CodexRuleYAML = `
action: restore
use_builtin_catalog: false
rules:
  - id: codex-native-context
    backend: codex-primary
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: log_skip
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 8
  max_reasoning_bytes_per_turn: 4096
  max_session_bytes: 32768
`

func TestPhase2_codexMarkerEligibilityIsBackendOnlyAndBounded(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase2CodexRuleYAML)
	exact := lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{
		lipapi.ReasoningDialectOpenAIResponsesItemV1,
	}}

	cases := []struct {
		name       string
		backend    string
		model      string
		buildCall  func(*testing.T, reasoningpreservation.TurnStore) lipapi.Call
		seed       func(*testing.T, reasoningpreservation.TurnStore)
		replay     lipapi.ReasoningReplaySupport
		wantMarker bool
	}{
		{
			name:    "first turn without artifact",
			backend: "codex-primary",
			model:   "gpt-5.6-codex",
			buildCall: func(t *testing.T, _ reasoningpreservation.TurnStore) lipapi.Call {
				t.Helper()
				return userOnlyCall()
			},
			replay:     exact,
			wantMarker: true,
		},
		{
			name:    "codex minor version is not a ceiling",
			backend: "codex-primary",
			model:   "gpt-9.99-codex-preview",
			buildCall: func(t *testing.T, _ reasoningpreservation.TurnStore) lipapi.Call {
				t.Helper()
				return userOnlyCall()
			},
			replay:     exact,
			wantMarker: true,
		},
		{
			name:    "client preserved exact reasoning",
			backend: "codex-primary",
			model:   "gpt-5.6-codex",
			buildCall: func(t *testing.T, store reasoningpreservation.TurnStore) lipapi.Call {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				visible := lipapi.TextPart("visible answer")
				part := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"rs_preserved","type":"reasoning","summary":[]}`))
				msg := assistantMsg(part, visible)
				artifact := turnArtifact("preserved", anchorFor(t, visible), placedReasoning(0, part))
				artifact.CreatedAt = time.Time{}
				if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("auth-session"), artifact); err != nil {
					t.Fatalf("Append: %v", err)
				}
				return lipapi.Call{Messages: []lipapi.Message{msg}}
			},
			replay:     exact,
			wantMarker: true,
		},
		{
			name:    "restored exact reasoning",
			backend: "codex-primary",
			model:   "gpt-5.6-codex",
			buildCall: func(t *testing.T, store reasoningpreservation.TurnStore) lipapi.Call {
				t.Helper()
				visible := lipapi.TextPart("visible answer")
				part := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"rs_restored","type":"reasoning","summary":[]}`))
				artifact := turnArtifact("restored", anchorFor(t, visible), placedReasoning(0, part))
				artifact.CreatedAt = time.Time{}
				if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("auth-session"), artifact); err != nil {
					t.Fatalf("Append: %v", err)
				}
				return lipapi.Call{Messages: []lipapi.Message{assistantMsg(visible)}}
			},
			replay:     exact,
			wantMarker: true,
		},
		{
			name:    "unrelated backend",
			backend: "openrouter-prod",
			model:   "gpt-5.6-codex",
			buildCall: func(t *testing.T, _ reasoningpreservation.TurnStore) lipapi.Call {
				t.Helper()
				return spoofedCall()
			},
			replay:     exact,
			wantMarker: false,
		},
		{
			name:    "ineligible replay dialect",
			backend: "codex-primary",
			model:   "gpt-5.6-codex",
			buildCall: func(t *testing.T, store reasoningpreservation.TurnStore) lipapi.Call {
				t.Helper()
				return missingCodexCall(t, store, lipapi.ReasoningDialectOpenAIResponsesItemV1)
			},
			replay:     lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIChatTextV1}},
			wantMarker: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryStore(t, reasoningpreservation.StoreOptions{
				TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
			})
			call := tc.buildCall(t, store)
			if tc.seed != nil {
				tc.seed(t, store)
			}
			dec, err := reasoningpreservation.NewAttemptTransform(cfg, store).HandleAttempt(context.Background(), &call, request.AttemptMeta{
				BackendID: tc.backend, Model: tc.model, ReplaySupport: tc.replay,
				Session: session.SessionView{AuthoritativeSessionID: "auth-session"},
			}, request.Services{})
			if err != nil {
				t.Fatalf("HandleAttempt: %v", err)
			}
			if tc.wantMarker {
				assertTrustedMarker(t, call)
			} else {
				assertNoMarker(t, call)
			}
			if tc.name == "ineligible replay dialect" && dec.Kind != request.AttemptContinue && dec.Kind != request.AttemptExcludeCandidate {
				t.Fatalf("unexpected decision: %+v", dec)
			}
		})
	}
}

func TestPhase2_markerSpoofIsDeletedBeforeEligibility(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase2CodexRuleYAML)
	call := spoofedCall()
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	_, err := reasoningpreservation.NewAttemptTransform(cfg, store).HandleAttempt(context.Background(), &call, request.AttemptMeta{
		BackendID: "openrouter-prod", Model: "unrelated", ReplaySupport: exactResponsesSupport(),
	}, request.Services{})
	if err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	assertNoMarker(t, call)
}

func TestPhase2_markerAbsentForAmbiguityConflictAndStatePolicy(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase2CodexRuleYAML)
	exact := exactResponsesSupport()

	t.Run("ambiguity", func(t *testing.T) {
		t.Parallel()
		store := newMemoryStore(t, reasoningpreservation.StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now})
		visible := lipapi.TextPart("visible")
		part := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"rs_ambiguous","type":"reasoning","summary":[]}`))
		for _, id := range []string{"a", "b"} {
			artifact := turnArtifact(id, anchorFor(t, visible), placedReasoning(0, part))
			artifact.CreatedAt = time.Time{}
			if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("auth-session"), artifact); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		call := lipapi.Call{Messages: []lipapi.Message{assistantMsg(visible)}}
		invokeMarkerTransform(t, cfg, store, &call, exact)
		assertNoMarker(t, call)
	})

	t.Run("conflict", func(t *testing.T) {
		t.Parallel()
		store := newMemoryStore(t, reasoningpreservation.StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now})
		visible := lipapi.TextPart("visible")
		stored := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"rs_stored","type":"reasoning","summary":[]}`))
		artifact := turnArtifact("stored", anchorFor(t, visible), placedReasoning(0, stored))
		artifact.CreatedAt = time.Time{}
		if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("auth-session"), artifact); err != nil {
			t.Fatalf("Append: %v", err)
		}
		client := reasoningPart(lipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"rs_client","type":"reasoning","summary":[]}`))
		call := lipapi.Call{Messages: []lipapi.Message{assistantMsg(client, visible)}}
		invokeMarkerTransform(t, cfg, store, &call, exact)
		assertNoMarker(t, call)
	})

	for _, policy := range []string{reasoningpreservation.PolicyLogSkip, reasoningpreservation.PolicyReject} {
		t.Run("state_"+policy, func(t *testing.T) {
			t.Parallel()
			stateCfg := cfg
			stateCfg.OnStateError = policy
			call := userOnlyCall()
			store := snapshotErrorStore{}
			dec, err := reasoningpreservation.NewAttemptTransform(stateCfg, store).HandleAttempt(context.Background(), &call, request.AttemptMeta{
				BackendID: "codex-primary", Model: "gpt-5.6-codex", ReplaySupport: exact,
				Session: session.SessionView{AuthoritativeSessionID: "auth-session"},
			}, request.Services{})
			if err != nil {
				t.Fatalf("HandleAttempt: %v", err)
			}
			assertNoMarker(t, call)
			if policy == reasoningpreservation.PolicyReject && dec.Kind != request.AttemptExcludeCandidate {
				t.Fatalf("reject policy decision=%+v", dec)
			}
		})
	}
}

func TestPhase2_surfacedWinnerOnlyCommitsReasoning(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase2CodexRuleYAML)
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{
		TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
	})
	factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
	loser, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "codex-primary", Model: "gpt-5.6-codex",
		Session: session.SessionView{AuthoritativeSessionID: "auth-session"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open loser: %v", err)
	}
	if err := loser.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "losing"}); err != nil {
		t.Fatalf("Observe loser: %v", err)
	}
	if err := loser.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "loser"}); err != nil {
		t.Fatalf("Observe loser text: %v", err)
	}
	if err := loser.Finish(context.Background(), response.OutcomeReplaced); err != nil {
		t.Fatalf("Finish loser: %v", err)
	}

	winner, err := factory.Open(context.Background(), response.StreamMeta{
		BackendID: "codex-primary", Model: "gpt-5.6-codex",
		Session: session.SessionView{AuthoritativeSessionID: "auth-session"},
	}, response.Services{})
	if err != nil {
		t.Fatalf("Open winner: %v", err)
	}
	_ = winner.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "winning"})
	_ = winner.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "winner"})
	if err := winner.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatalf("Finish winner: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("auth-session"))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 || snap[0].Reasoning[0].Part.Reasoning.Text != "winning" {
		t.Fatalf("only surfaced winner may commit, snapshot=%+v", snap)
	}
}

func TestPhase2_nonSurfacedOutcomesNeverCommitReasoning(t *testing.T) {
	t.Parallel()
	cfg := decodeValidConfig(t, phase2CodexRuleYAML)
	for _, outcome := range []response.StreamOutcome{
		response.OutcomeCancelled,
		response.OutcomeClosed,
		response.OutcomeFailed,
		response.OutcomeReplaced,
		response.OutcomeGateReplaced,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			store := newMemoryStore(t, reasoningpreservation.StoreOptions{
				TTL: time.Hour, MaxTurnsPerSession: 8, MaxReasoningBytesPerTurn: 4096, MaxSessionBytes: 32768, Now: time.Now,
			})
			factory := reasoningpreservation.NewStreamObserverFactory(cfg, store)
			obs, err := factory.Open(context.Background(), response.StreamMeta{
				BackendID: "codex-primary", Model: "gpt-5.6-codex",
				Session: session.SessionView{AuthoritativeSessionID: "session-" + string(outcome)},
			}, response.Services{})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			_ = obs.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventReasoningDelta, Delta: "not surfaced"})
			_ = obs.Observe(context.Background(), lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "discarded"})
			if err := obs.Finish(context.Background(), outcome); err != nil {
				t.Fatalf("Finish: %v", err)
			}
			snap, err := store.Snapshot(context.Background(), reasoningpreservation.NewSessionPartition("session-"+string(outcome)))
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if len(snap) != 0 {
				t.Fatalf("non-surfaced outcome committed %d artifacts", len(snap))
			}
		})
	}
}

func userOnlyCall() lipapi.Call {
	return lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}}}
}

func spoofedCall() lipapi.Call {
	return lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello")}}}, Extensions: map[string]json.RawMessage{
		reasoningpreservation.ContinuityMarkerKey: json.RawMessage(`{"eligible":false,"dialect":"attacker"}`),
	}}
}

func missingCodexCall(t *testing.T, store reasoningpreservation.TurnStore, dialect lipapi.ReasoningDialect) lipapi.Call {
	t.Helper()
	visible := lipapi.TextPart("visible")
	part := reasoningPart(dialect, "", "", mustOpaqueJSON(t, `{"id":"rs_missing","type":"reasoning","summary":[]}`))
	if _, err := store.Append(context.Background(), reasoningpreservation.NewSessionPartition("auth-session"), turnArtifact("missing", anchorFor(t, visible), placedReasoning(0, part))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	return lipapi.Call{Messages: []lipapi.Message{{Role: lipapi.RoleAssistant, Parts: []lipapi.Part{visible}}}}
}

func exactResponsesSupport() lipapi.ReasoningReplaySupport {
	return lipapi.ReasoningReplaySupport{Dialects: []lipapi.ReasoningDialect{lipapi.ReasoningDialectOpenAIResponsesItemV1}}
}

func invokeMarkerTransform(t *testing.T, cfg reasoningpreservation.Config, store reasoningpreservation.TurnStore, call *lipapi.Call, replay lipapi.ReasoningReplaySupport) {
	t.Helper()
	if _, err := reasoningpreservation.NewAttemptTransform(cfg, store).HandleAttempt(context.Background(), call, request.AttemptMeta{
		BackendID: "codex-primary", Model: "gpt-5.6-codex", ReplaySupport: replay,
		Session: session.SessionView{AuthoritativeSessionID: "auth-session"},
	}, request.Services{}); err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
}

func assertTrustedMarker(t *testing.T, call lipapi.Call) {
	t.Helper()
	if string(call.Extensions[reasoningpreservation.ContinuityMarkerKey]) != string(reasoningpreservation.ContinuityMarkerValue) {
		t.Fatalf("marker=%s want %s extensions=%v", call.Extensions[reasoningpreservation.ContinuityMarkerKey], reasoningpreservation.ContinuityMarkerValue, call.Extensions)
	}
}

func assertNoMarker(t *testing.T, call lipapi.Call) {
	t.Helper()
	if _, ok := call.Extensions[reasoningpreservation.ContinuityMarkerKey]; ok {
		t.Fatalf("unexpected continuity marker: %s", call.Extensions[reasoningpreservation.ContinuityMarkerKey])
	}
}

type snapshotErrorStore struct{}

func (snapshotErrorStore) Append(context.Context, reasoningpreservation.SessionPartition, reasoningpreservation.TurnArtifact) (reasoningpreservation.EvictionSummary, error) {
	return reasoningpreservation.EvictionSummary{}, errors.New("snapshot store failure")
}

func (snapshotErrorStore) Snapshot(context.Context, reasoningpreservation.SessionPartition) ([]reasoningpreservation.TurnArtifact, error) {
	return nil, errors.New("snapshot store failure")
}

func (snapshotErrorStore) Delete(context.Context, reasoningpreservation.SessionPartition, ...string) error {
	return nil
}
