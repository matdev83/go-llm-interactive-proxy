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
	id    string
	order int
}

func NewAttemptTransform(cfg Config, store TurnStore) *AttemptTransform {
	return &AttemptTransform{
		cfg:   cfg,
		store: store,
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
	partition, ok := sessionPartitionOrMiss(meta.Session.AuthoritativeSessionID)
	if !ok {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	match, err := ResolveMatch(t.cfg, CandidateIdentity{
		BackendID:       meta.BackendID,
		BackendPrefixes: meta.BackendPrefixes,
		Model:           meta.Model,
	})
	if err != nil {
		return t.stateErrorDecision()
	}
	eligible := MatchEligible(match.Kind)
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
		Eligible:          eligible,
	})
	if err != nil {
		return request.AttemptDecision{}, err
	}
	if res.Exclude {
		reason := res.ReasonCode
		if reason == "" {
			reason = "unrepresentable_replay"
		}
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: reason}, nil
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

func (t *AttemptTransform) stateErrorDecision() (request.AttemptDecision, error) {
	if t.cfg.Action == ActionObserve {
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
	if t.cfg.OnStateError == PolicyReject {
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "state_error"}, nil
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}
