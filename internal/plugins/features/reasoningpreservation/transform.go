//nolint:all
package reasoningpreservation

import (
	"context"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
)

type AttemptTransform struct {
	cfg               Config
	store             TurnStore
	tel               *Telemetry
	id                string
	order             int
	companion         CompanionPolicy
	svc               CompressionServices
	adoptionStage     CompletedAdoptionStage
	viewStage         ReasoningViewStage
	viewConsumerStage ReasoningViewConsumerStage
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
		cfg:               cfg,
		store:             store,
		tel:               t,
		id:                ID + "-transform",
		order:             0,
		adoptionStage:     identityAdoptionStage,
		viewStage:         identityReasoningViewStage,
		viewConsumerStage: identityReasoningViewConsumerStage,
	}
}

// NewAttemptTransformWithAdoptionStage creates a transform with an explicit adoption stage.
// The stage is immutable; if nil, identity is used. Task 5.3 will inject the decoder stage.
func NewAttemptTransformWithAdoptionStage(cfg Config, store TurnStore, stage CompletedAdoptionStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	if stage != nil {
		t.adoptionStage = stage
	}
	return t
}

// NewAttemptTransformWithViewStage creates a transform with an explicit view stage.
// The stage is immutable; if nil, identity is used. Task 6.1 injects the selection stage.
func NewAttemptTransformWithViewStage(cfg Config, store TurnStore, viewStage ReasoningViewStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	if viewStage != nil {
		t.viewStage = viewStage
	}
	return t
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

// NewAttemptTransformWithServicesAndStage creates a transform with explicit services and adoption stage immutably.
func NewAttemptTransformWithServicesAndStage(cfg Config, store TurnStore, svc CompressionServices, stage CompletedAdoptionStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransformWithServices(cfg, store, svc, tel...)
	if stage != nil {
		t.adoptionStage = stage
	}
	return t
}

// NewAttemptTransformWithServicesViewStage creates a transform with explicit services and view stage immutably.
func NewAttemptTransformWithServicesViewStage(cfg Config, store TurnStore, svc CompressionServices, viewStage ReasoningViewStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransformWithServices(cfg, store, svc, tel...)
	if viewStage != nil {
		t.viewStage = viewStage
	}
	return t
}

// NewAttemptTransformWithViewStageAndAdoptionStage creates a transform with explicit view and adoption stages.
func NewAttemptTransformWithViewStageAndAdoptionStage(cfg Config, store TurnStore, viewStage ReasoningViewStage, adoptionStage CompletedAdoptionStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	if viewStage != nil {
		t.viewStage = viewStage
	}
	if adoptionStage != nil {
		t.adoptionStage = adoptionStage
	}
	return t
}

// NewAttemptTransformWithViewConsumerStage creates a transform with an explicit view consumer stage.
func NewAttemptTransformWithViewConsumerStage(cfg Config, store TurnStore, consumer ReasoningViewConsumerStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	if consumer != nil {
		t.viewConsumerStage = consumer
	}
	return t
}

// NewAttemptTransformWithViewStageViewConsumer creates a transform with explicit view and consumer stages.
func NewAttemptTransformWithViewStageViewConsumer(cfg Config, store TurnStore, viewStage ReasoningViewStage, consumer ReasoningViewConsumerStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransform(cfg, store, tel...)
	if viewStage != nil {
		t.viewStage = viewStage
	}
	if consumer != nil {
		t.viewConsumerStage = consumer
	}
	return t
}

// NewAttemptTransformWithServicesViewStageAndAdoptionStage is the full view+services+adoption wiring.
func NewAttemptTransformWithServicesViewStageAndAdoptionStage(cfg Config, store TurnStore, svc CompressionServices, viewStage ReasoningViewStage, adoptionStage CompletedAdoptionStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransformWithServices(cfg, store, svc, tel...)
	if viewStage != nil {
		t.viewStage = viewStage
	}
	if adoptionStage != nil {
		t.adoptionStage = adoptionStage
	}
	return t
}

// NewAttemptTransformWithCompanionPolicyServicesAndStage is the full composition for bundle wiring.
func NewAttemptTransformWithCompanionPolicyServicesAndStage(cfg Config, store TurnStore, svc CompressionServices, policy CompanionPolicy, stage CompletedAdoptionStage, tel ...*Telemetry) *AttemptTransform {
	t := NewAttemptTransformWithCompanionPolicyAndServices(cfg, store, svc, policy, tel...)
	if stage != nil {
		t.adoptionStage = stage
	}
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
	// Gate surrogate use strictly on explicit active mode; invalid mode fails closed to original with no polling/selection.
	if t.cfg.Compression.Enabled && (t.cfg.Compression.Mode == CompressionShadow || t.cfg.Compression.Mode == CompressionActive) {
		if cs, ok := t.store.(CompressionStore); ok {
			// Shared classification for both poll and restore.
			classified, candidates, cerr := collectRestoreCandidates(call, arts, meta.ReplaySupport)
			var pollRes CompressionPollAttemptResult
			var resTmp RestoreResult
			var rerr error
			if cerr != nil {
				// Classification/validation error is state error for restore, poll-local error for poll.
				pollRes = CompressionPollAttemptResult{Kind: PollKindPollError, Err: cerr}
				adoption := handleCompletedPollCandidate(ctx, t.cfg, cs, t.svc, t.tel, pollRes, call)
				if t.adoptionStage != nil {
					adoption = t.adoptionStage(ctx, adoption)
				}
				if adoption.Outcome != AdoptionOutcomeNone {
					// poll-local error adoption, call unchanged
				}
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
			// Record content-free poll taxonomy before guard.
			if t.tel != nil && t.cfg.Compression.Enabled {
				switch pollRes.Kind {
				case PollKindPending:
					t.tel.RecordShadowMeasurement(OutcomePollPending, 0, 0, 0, 0, 0)
				case PollKindCompleted:
					t.tel.RecordShadowMeasurement(OutcomePollCompleted, 0, 0, 0, 0, 0)
				case PollKindFailed:
					t.tel.RecordShadowMeasurement(OutcomePollFailed, 0, 0, 0, 0, 0)
				case PollKindNotFound:
					t.tel.RecordShadowMeasurement(OutcomePollNotFound, 0, 0, 0, 0, 0)
				case PollKindUnavailable:
					t.tel.RecordShadowMeasurement(OutcomePollUnavailable, 0, 0, 0, 0, 0)
				case PollKindPollError:
					t.tel.RecordShadowMeasurement(OutcomePollError, 0, 0, 0, 0, 0)
				case PollKindNoPending:
					// no_pending is not a poll state per se; no telemetry needed (implicit original fallback)
				}
			}
			// 5.2 chain: single poll -> raw guard without double poll/decode; adoption carries bounded raw for 5.3 local handling.
			adoption := handlePollAndGuardRaw(ctx, t.cfg, cs, t.svc, t.tel, pollRes, call)
			if t.adoptionStage != nil {
				adoption = t.adoptionStage(ctx, adoption)
			}
			// Shadow ALWAYS originals: record original_fallback only when a pending compression existed (poll/adoption non-none).
			if t.tel != nil && t.cfg.Compression.Enabled && t.cfg.Compression.Mode == CompressionShadow {
				if pollRes.Kind != PollKindNoPending && pollRes.Kind != "" {
					t.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
				} else if adoption.Outcome != AdoptionOutcomeNone {
					t.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
				}
			}
			// adoption is local to this attempt; shadow keeps call unchanged.
			// Telemetry for raw guard already recorded; bounded_raw ready for 5.3 decode locally.
			if adoption.Outcome == AdoptionOutcomeBoundedRaw {
				// pass bounded raw to next stage via local adoption; no Forget yet (deferred to 5.3)
			} else if adoption.Outcome != AdoptionOutcomeNone {
				// rejection already cleared pending and forgotten once, content-free telemetry recorded
			}
			// 6.1 revalidation via immutable view stage seam (policy-aware, bounded, no model content).
			var viewDecisions map[string]ReasoningViewResult
			if t.viewStage != nil {
				viewDecisions = t.viewStage(ctx, t.cfg.Compression, cs, t.svc, partition, candidates, meta.ReplaySupport, meta)
			} else {
				viewDecisions = selectReasoningViews(ctx, t.cfg.Compression, cs, t.svc, partition, candidates, meta.ReplaySupport, meta)
			}
			// 6.2 ephemeral surrogate restoration view without mutating stored originals.
			// ACTIVE builds defensive ephemeral candidates; Shadow remains identity.
			var ephemeralCandidates []restoreCandidate
			if t.cfg.Compression.Enabled && t.cfg.Compression.Mode == CompressionActive {
				// Build ephemeral candidates defensively for ViewSurrogate only.
				getSur := func(id string) (*ReasoningSurrogate, bool) {
					st, ok, err := cs.GetCompressionState(ctx, partition, id)
					if err != nil || !ok || st.Surrogate == nil {
						return nil, false
					}
					return st.Surrogate, true
				}
				ephemeralCandidates = BuildEphemeralCandidates(candidates, viewDecisions, getSur)
				// Consumer seam remains immutable; ACTIVE may have distinct consumer but still identity for call.
				if t.viewConsumerStage != nil {
					if out := t.viewConsumerStage(ctx, call, viewDecisions); out != nil {
						call = out
					}
				}
			} else {
				// Shadow: ensure call unchanged via identity consumer (even if 6.2 builder injected).
				if t.viewConsumerStage != nil && t.cfg.Compression.Enabled {
					_ = t.viewConsumerStage // keep seam wired but force identity for shadow
				}
				_ = viewDecisions
				ephemeralCandidates = nil
			}
			// Restore using same classified/candidates without reclassifying.
			// When ACTIVE, use ephemeral candidates (defensive copies with surrogate text).
			restoreCandidates := candidates
			if ephemeralCandidates != nil {
				restoreCandidates = ephemeralCandidates
			}
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
				}, classified, restoreCandidates)
				if rerr != nil {
					return request.AttemptDecision{}, rerr
				}
			}
			t.recordRestoreOutcome(resTmp)
			// 6.3 gate telemetry: active_used only when actual surrogate text injected; shadow_ready not active; original_fallback proper.
			if t.tel != nil && t.cfg.Compression.Mode == CompressionActive {
				used := false
				if len(viewDecisions) > 0 && resTmp.Mutated && resTmp.RestoredCount > 0 {
					for _, dec := range viewDecisions {
						if dec.Kind == ViewSurrogate {
							used = true
							break
						}
					}
				}
				if used {
					t.tel.RecordCompressionMeasurement(OutcomeActiveUsed, 0, 0, 0)
				} else {
					// Active fallback: surrogate existed but not used due to any uncertainty.
					if len(viewDecisions) > 0 {
						t.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
					} else if pollRes.Kind != PollKindNoPending && pollRes.Kind != "" {
						t.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
					} else if adoption.Outcome != AdoptionOutcomeNone {
						t.tel.RecordShadowMeasurement(OutcomeOriginalFallback, 0, 0, 0, 0, 0)
					}
				}
			}
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
	// Invalid mode or disabled: fail closed to original without polling/selection. No surrogate use, no Exclude from compression.
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
