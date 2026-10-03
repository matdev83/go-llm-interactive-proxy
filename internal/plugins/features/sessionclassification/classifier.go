package sessionclassification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	sdkclassification "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/sessionclassification"
)

var (
	// ErrInvalidClassifier reports a classifier value this feature cannot serve.
	ErrInvalidClassifier = errors.New("session classification: invalid classifier")
	// ErrStateUnavailable reports that process-owned classification state is
	// not usable for the current turn. It fails open: the caller preserves the
	// request and the conservative classification.
	ErrStateUnavailable = errors.New("session classification: process state unavailable")
)

// StateAuthority resolves the process-owned classification state used by one
// generation's classifier. Resolution is lazy by design: composing a generation
// must not initialize feature state, so the first request after candidate
// preparation resolves the already-initialized shared coordinator. A
// standard-distribution process holder satisfies this contract.
type StateAuthority interface {
	// ClassificationState returns the initialized process-owned state
	// authority, or an error when no enabled generation has prepared it.
	ClassificationState() (Store, error)
}

// ClassifierDeps binds one generation's immutable policy to the process-owned
// state authority. There is no per-request or per-generation state here: the
// authoritative store is shared across generations so positive classifications
// survive reload.
type ClassifierDeps struct {
	// State resolves the shared process coordinator. Required.
	State StateAuthority
	// Observer receives bounded classification observations. Nil disables
	// observation entirely, which is what keeps requirement 9.6 true for a
	// generation that never publishes a classifier.
	Observer Observer
	// Now supplies promotion timestamps. Nil selects time.Now.
	Now func() time.Time
}

// Classifier is the concrete standard-feature classifier bound to one immutable
// generation. It holds only bounded policy, the shared state authority, and a
// bounded observation sink; it never retains request content, raw identities,
// or per-session workers.
type Classifier struct {
	cfg      Config
	state    StateAuthority
	observer Observer
	now      func() time.Time
}

var _ sdkclassification.Classifier = (*Classifier)(nil)

// NewClassifier validates one generation's policy and binds it to the
// process-owned state authority. An unservable policy is rejected here so a
// candidate generation fails before publication instead of serving a partially
// configured mode.
func NewClassifier(cfg Config, deps ClassifierDeps) (*Classifier, error) {
	if deps.State == nil {
		return nil, fmt.Errorf("%w: state authority is required", ErrInvalidClassifier)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidClassifier, err)
	}
	policy := cfg
	if policy.Mode == "" {
		policy.Mode = ModeHeuristic
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Classifier{cfg: policy, state: deps.State, observer: deps.Observer, now: now}, nil
}

// ID returns the stable identity of the standard session-classification feature.
func (c *Classifier) ID() string {
	if c == nil {
		return ""
	}
	return ID
}

// Classify evaluates one bounded metadata snapshot against this generation's
// policy and the shared monotonic state. It never mutates request content,
// never scans a transcript, and never downgrades an established positive
// classification. State failures return an error with the conservative snapshot
// so the generic stage runner can fail open without rejecting the request.
func (c *Classifier) Classify(ctx context.Context, in sdkclassification.Input) (session.Classification, error) {
	if c == nil || c.state == nil {
		return session.Classification{}, ErrInvalidClassifier
	}
	current := in.Session.Classification
	// Requirement 1.4/6.5: an accepted positive classification is immutable for
	// the whole logical session, so no state work is required for later turns.
	if current.IsCodingAgent() {
		c.observeEvaluation(EvaluationPreserved)
		return current, nil
	}
	if err := ValidateStoreContext(ctx); err != nil {
		// A nil or already-canceled context is a caller-side rejection, not a
		// classification decision. It is still reported through the bounded
		// fail-open outcome so no evaluation is silently unobserved.
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, err
	}
	key, keyErr := ResolveKey(in.Session)
	if keyErr != nil {
		// Without proxy-owned authority there is nothing to scope state to, and
		// a client-controlled hint is never state authority (requirements 2.2/2.3).
		c.observeEvaluation(EvaluationNoAuthority)
		return current, nil
	}
	store, err := c.state.ClassificationState()
	if err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if store == nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, ErrStateUnavailable
	}

	record, found, err := store.Load(ctx, key)
	if err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return current, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if found && record.Classification.IsCodingAgent() {
		c.observeEvaluation(EvaluationRestored)
		return record.Classification, nil
	}

	decision := EvaluateLocal(c.cfg, in)
	if !decision.Promotes {
		// Unknown stays unknown. Absence of evidence is not a negative
		// classification and creates no durable state.
		c.observeEvaluation(c.noPromotionOutcome(decision, in.Evidence.ClientUserAgent))
		return session.Classification{}, nil
	}
	proposal := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     decision.Source,
		Confidence: session.ConfidenceHigh,
		Evidence:   decision.EvidenceCode,
		Revision:   1,
	}
	if err := ValidatePositiveProposal(proposal); err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return session.Classification{}, err
	}
	promoted, didPromote, err := store.Promote(ctx, key, proposal, c.now())
	if err != nil {
		c.observeEvaluation(EvaluationStateUnavailable)
		return session.Classification{}, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if !promoted.Classification.IsCodingAgent() {
		// A store that neither accepted the proposal nor holds a positive leaves
		// the session unknown; absence of evidence is not a negative
		// classification.
		c.observeEvaluation(EvaluationRestored)
		return session.Classification{}, nil
	}
	// Requirement 9.1/2.5: the transition observation belongs to the caller that
	// actually established the first positive. A store coalesces concurrent
	// attempts for one authority key and returns the winner's positive record
	// with promoted=false to a coalesced-flight waiter or a cached-positive
	// short-circuit, so record positivity alone cannot identify the owner.
	if !didPromote {
		// A concurrent turn owns this session's first positive. Requirement 1.4
		// still requires this turn to project the accepted positive, so only the
		// observation is suppressed here.
		c.observeEvaluation(EvaluationRestored)
		return promoted.Classification, nil
	}
	c.observeTransition(promoted.Classification)
	c.observeEvaluation(EvaluationPromoted)
	return promoted.Classification, nil
}

// noPromotionOutcome maps a non-promoting evaluation to the closed diagnostic
// vocabulary of requirement 9.2. It reads only bounded policy and the already
// captured bounded evidence code, so it can never derive a label from the raw
// User-Agent itself.
func (c *Classifier) noPromotionOutcome(decision LocalDecision, clientUserAgent string) EvaluationOutcome {
	// Requirement 3.7: an exclusion prevented a prospective local match. This is
	// the most specific bounded fact available, so it is reported ahead of the
	// generic remote-eligibility diagnostic and stays visible in every mode. It
	// is reported without echoing the excluded value.
	if ExcludedIdentity(c.cfg, clientUserAgent) {
		return EvaluationExcluded
	}
	if c.remoteEligibleAfterLocal(decision) {
		return EvaluationRemoteSkipped
	}
	return EvaluationUnknown
}

// remoteEligibleAfterLocal reports whether this generation's mode leaves the
// still-unknown session eligible for a configured remote decision.
//
// Requirement 6.3 makes a jev-mode promotion depend entirely on the configured
// remote decision, so every still-unknown jev turn is remote-eligible.
// Requirement 6.4 makes a hybrid-mode turn remote-eligible exactly when local
// evaluation found no decisive evidence, which is the only way a hybrid turn
// reaches here because decisive local evidence promotes in hybrid mode. No remote
// attempt is made yet, so the faithful bounded diagnostic is a skipped remote
// decision rather than a bare unknown.
func (c *Classifier) remoteEligibleAfterLocal(decision LocalDecision) bool {
	switch c.cfg.Mode {
	case ModeJev:
		return true
	case ModeHybrid:
		return decision.EvidenceCode == ""
	default:
		return false
	}
}

// observeEvaluation records one bounded outcome. The observation sink is
// optional, so a deployment that never configures one performs no work beyond a
// nil check (requirements 9.6, 10.8).
func (c *Classifier) observeEvaluation(outcome EvaluationOutcome) {
	if c == nil || c.observer == nil {
		return
	}
	c.observer.ObserveEvaluation(EvaluationObservation{Mode: c.cfg.Mode, Outcome: outcome})
}

// observeTransition records the single first accepted positive transition for a
// logical session, projecting only the bounded snapshot the store assigned
// (requirements 9.1, 9.5).
func (c *Classifier) observeTransition(classification session.Classification) {
	if c == nil || c.observer == nil {
		return
	}
	c.observer.ObserveTransition(TransitionObservation{
		Source:     classification.Source,
		Confidence: classification.Confidence,
		Evidence:   classification.Evidence,
		Revision:   classification.Revision,
	})
}
