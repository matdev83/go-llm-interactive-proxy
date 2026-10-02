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
	// Now supplies promotion timestamps. Nil selects time.Now.
	Now func() time.Time
}

// Classifier is the concrete standard-feature classifier bound to one immutable
// generation. It holds only bounded policy and the shared state authority; it
// never retains request content, raw identities, or per-session workers.
type Classifier struct {
	cfg   Config
	state StateAuthority
	now   func() time.Time
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
	return &Classifier{cfg: policy, state: deps.State, now: now}, nil
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
		return current, nil
	}
	if err := ValidateStoreContext(ctx); err != nil {
		return current, err
	}
	key, keyErr := ResolveKey(in.Session)
	if keyErr != nil {
		// Without proxy-owned authority there is nothing to scope state to, and
		// a client-controlled hint is never state authority (requirements 2.2/2.3).
		return current, nil
	}
	store, err := c.state.ClassificationState()
	if err != nil {
		return current, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if store == nil {
		return current, ErrStateUnavailable
	}

	record, found, err := store.Load(ctx, key)
	if err != nil {
		return current, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if found && record.Classification.IsCodingAgent() {
		return record.Classification, nil
	}

	decision := EvaluateLocal(c.cfg, in)
	if !decision.Promotes {
		// Unknown stays unknown. Absence of evidence is not a negative
		// classification and creates no durable state.
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
		return session.Classification{}, err
	}
	promoted, _, err := store.Promote(ctx, key, proposal, c.now())
	if err != nil {
		return session.Classification{}, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	if !promoted.Classification.IsCodingAgent() {
		return session.Classification{}, nil
	}
	return promoted.Classification, nil
}
