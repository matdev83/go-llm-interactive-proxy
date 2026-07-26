package reasoningpreservation

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
)

type AttemptTransform struct {
	cfg   Config
	store TurnStore
	tel   *Telemetry
	id    string
	order int
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

func (t *AttemptTransform) ID() string { return t.id }
func (t *AttemptTransform) Order() int { return t.order }
func (t *AttemptTransform) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (t *AttemptTransform) HandleAttempt(ctx context.Context, call *lipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if call == nil {
		return request.AttemptDecision{}, fmt.Errorf("%s: call is required", ID)
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
