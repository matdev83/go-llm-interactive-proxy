package reasoningpreservation

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
)

type AttemptTransform struct {
	cfg       Config
	store     TurnStore
	tel       *Telemetry
	id        string
	order     int
	companion CompanionPolicy
	svc       CompressionServices
}

func NewAttemptTransform(cfg Config, store TurnStore, tel ...*Telemetry) *AttemptTransform {
	var t *Telemetry
	if len(tel) > 0 {
		t = tel[0]
	}
	if t == nil {
		t = NewTelemetry()
	}
	return &AttemptTransform{
		cfg:   cfg,
		store: store,
		tel:   t,
		id:    ID + "-transform",
		order: 0,
	}
}

func NewAttemptTransformWithCompanionPolicy(cfg Config, store TurnStore, policy CompanionPolicy, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	t.companion = policy
	return t
}

func NewAttemptTransformWithServices(cfg Config, store TurnStore, svc CompressionServices, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	t.svc = svc
	return t
}

func NewAttemptTransformWithCompanionPolicyAndServices(cfg Config, store TurnStore, svc CompressionServices, policy CompanionPolicy, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransformWithCompanionPolicy(cfg, store, policy, tel...)
	t.svc = svc
	return t
}

func (t *AttemptTransform) ID() string { return t.id }
func (t *AttemptTransform) Order() int { return t.order }
func (t *AttemptTransform) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (t *AttemptTransform) HandleAttempt(ctx context.Context, call *lipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if call == nil {
		return request.AttemptDecision{}, fmt.Errorf("%s: call is required", ID)
	}
	if t.companion.BeforeMatch != nil {
		t.companion.BeforeMatch(call, meta)
	}
	if t.store == nil {
		return request.AttemptDecision{}, fmt.Errorf("%s: store is required", ID)
	}
	match, err := ResolveMatch(t.cfg, CandidateIdentity{
		BackendID:       meta.BackendID,
		BackendPrefixes: meta.BackendPrefixes,
		Model:           meta.Model,
	})
	if err != nil {
		return t.stateErrorDecision()
	}
	if !MatchEligible(match.Kind) {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	partition, ok := sessionPartitionOrMiss(meta.Session.AuthoritativeSessionID)
	if !ok {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	arts, err := t.store.Snapshot(ctx, partition)
	if err != nil {
		return t.stateErrorDecision()
	}
	// One-shot non-blocking poll for matching restorable artifact with pending bound JobID.
	// Shares single ClassifyAssistantTurns via collectRestoreCandidates to avoid double Classify.
	// Poll is immutable-construction via CompressionServices; no cross-attempt state.
	// Completed candidate is captured locally and passed to placeholder for 5.2.
	if t.cfg.Compression.Enabled {
		if cs, ok := t.store.(CompressionStore); ok {
			// Shared classification for both poll and restore.
			classified, candidates, cerr := collectRestoreCandidates(call, arts, meta.ReplaySupport)
			var pollRes CompressionPollAttemptResult
			var resTmp RestoreResult
			var rerr error
			if cerr != nil {
				// Classification/validation error is state error for restore, poll-local error for poll.
				pollRes = CompressionPollAttemptResult{Kind: PollKindPollError, Err: cerr}
				_ = handleCompletedPollCandidate(ctx, pollRes, call)
				resTmp, rerr = applyStateErrorPolicy(t.cfg.OnStateError)
				t.recordRestoreOutcome(resTmp)
				if rerr != nil {
					return request.AttemptDecision{}, rerr
				}
				if resTmp.Exclude {
					reason := resTmp.ReasonCode
					if reason == "" {
						reason = "unrepresentable_replay"
					}
					return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: reason}, nil
				}
				if t.companion.AfterRestore != nil {
					t.companion.AfterRestore(ctx, call, meta, MatchResult{Kind: match.Kind, RuleID: match.RuleID}, resTmp)
				}
				return request.AttemptDecision{Kind: request.AttemptContinue}, nil
			}
			// Poll only supported missing candidates.
			var pollCandidates []restoreCandidate
			for _, c := range candidates {
				if !c.Unsupported {
					pollCandidates = append(pollCandidates, c)
				}
			}
			pollRes = pollOnceWithCandidates(ctx, cs, partition, pollCandidates, t.svc)
			_ = handleCompletedPollCandidate(ctx, pollRes, call)
			// Restore using same classified/candidates without reclassifying.
			if t.cfg.Action == ActionObserve {
				// Observe never mutates; reuse classified for outcomes.
				resTmp = RestoreResult{Outcomes: outcomesFromClassifications(classified, nil)}
			} else if t.cfg.Action != ActionRestore {
				return request.AttemptDecision{}, fmt.Errorf("%s: unknown action %q", ID, t.cfg.Action)
			} else {
				resTmp, rerr = restoreWithCandidates(RestoreInput{
					Action:            t.cfg.Action,
					OnUnrepresentable: t.cfg.OnUnrepresentable,
					OnStateError:      t.cfg.OnStateError,
					Call:              call,
					Artifacts:         arts,
					ReplaySupport:     meta.ReplaySupport,
					Eligible:          true,
				}, classified, candidates)
				if rerr != nil {
					return request.AttemptDecision{}, rerr
				}
			}
			t.recordRestoreOutcome(resTmp)
			if resTmp.Exclude {
				reason := resTmp.ReasonCode
				if reason == "" {
					reason = "unrepresentable_replay"
				}
				return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: reason}, nil
			}
			if t.companion.AfterRestore != nil {
				t.companion.AfterRestore(ctx, call, meta, match, resTmp)
			}
			return request.AttemptDecision{Kind: request.AttemptContinue}, nil
		}
	}
	res, err := RestoreMissingReasoning(RestoreInput{
		Action:            t.cfg.Action,
		OnUnrepresentable: t.cfg.OnUnrepresentable,
		OnStateError:      t.cfg.OnStateError,
		Call:              call,
		Artifacts:         arts,
		ReplaySupport:     meta.ReplaySupport,
		Eligible:          true,
	})
	if err != nil {
		return request.AttemptDecision{}, err
	}
	t.recordRestoreOutcome(res)
	if res.Exclude {
		reason := res.ReasonCode
		if reason == "" {
			reason = "unrepresentable_replay"
		}
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: reason}, nil
	}
	if t.companion.AfterRestore != nil {
		t.companion.AfterRestore(ctx, call, meta, match, res)
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

func (t *AttemptTransform) recordRestoreOutcome(res RestoreResult) {
	if t == nil || t.tel == nil {
		return
	}
	if len(res.Outcomes) > 0 {
		for _, o := range res.Outcomes {
			counts := map[string]int{"count": 1}
			if o == OutcomeRestored {
				counts = map[string]int{"restored": max(1, res.RestoredCount), "bytes": res.RestoredBytes}
			}
			t.tel.Record(o, counts)
		}
		return
	}
	switch {
	case res.Exclude && res.ReasonCode == "state_error":
		t.tel.Record(OutcomeStateError, map[string]int{"count": 1})
	case res.Exclude && res.ReasonCode == "unrepresentable_replay":
		t.tel.Record(OutcomeUnrepresentable, map[string]int{"count": 1})
	case res.Mutated && res.RestoredCount > 0:
		t.tel.Record(OutcomeRestored, map[string]int{"restored": res.RestoredCount, "bytes": res.RestoredBytes})
	case res.ReasonCode == "state_error":
		t.tel.Record(OutcomeStateError, map[string]int{"count": 1})
	case res.ReasonCode == "unrepresentable":
		t.tel.Record(OutcomeUnrepresentable, map[string]int{"count": 1})
	}
}

func (t *AttemptTransform) stateErrorDecision() (request.AttemptDecision, error) {
	if t.tel != nil {
		t.tel.Record(OutcomeStateError, map[string]int{"count": 1})
	}
	if t.cfg.Action == ActionObserve {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	if t.cfg.OnStateError == PolicyReject {
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "state_error"}, nil
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}
