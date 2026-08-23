package stopguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// ProgressFingerprint encapsulates normalized canonical facts used to detect
// material progress across continuation attempts without retaining raw payloads.
type ProgressFingerprint struct {
	CandidateOutputDigest string
	ToolName              string
	ToolArgsDigest        string
	ToolResultDigest      string
	ToolErrorDigest       string
	ContinuationLineageID string
	VerdictKind           VerdictKind
	ObjectiveDigest       string
	ItemCount             int
	StateTransition       string
}

// Digest computes a deterministic, order-stable hash representation of all
// canonical progress facts in the fingerprint. Volatile context (request IDs,
// timestamps) is excluded by definition.
func (fp ProgressFingerprint) Digest() string {
	h := sha256.New()
	writeField := func(tag string, val string) {
		_, _ = fmt.Fprintf(h, "%s:%d:%s\n", tag, len(val), val)
	}
	writeField("out", fp.CandidateOutputDigest)
	writeField("tool", fp.ToolName)
	writeField("args", fp.ToolArgsDigest)
	writeField("res", fp.ToolResultDigest)
	writeField("err", fp.ToolErrorDigest)
	writeField("lin", fp.ContinuationLineageID)
	writeField("ver", string(fp.VerdictKind))
	writeField("obj", fp.ObjectiveDigest)
	_, _ = fmt.Fprintf(h, "cnt:%d\n", fp.ItemCount)
	writeField("st", fp.StateTransition)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ProgressResult describes the outcome of recording a progress observation.
type ProgressResult struct {
	NewProgress           bool
	NoProgressTripped     bool
	BudgetExhausted       bool
	ConsecutiveNoProgress int
	TotalContinuations    int
	Action                Action
}

// ProgressTracker manages the semantic continuation attempt budget and detects
// repeated no-progress cycles across continuation attempts for a logical request.
type ProgressTracker struct {
	mu sync.Mutex

	maxContinuations int
	noProgressLimit  int

	totalContinuations    int
	consecutiveNoProgress int
	lastDigest            string
	hasBaseline           bool
	noProgressTripped     bool
	budgetExhausted       bool
	cancelled             bool
	terminalReached       bool
}

// NewProgressTracker constructs a ProgressTracker with enforced positive bounds.
// Non-positive limits fallback safely to 1.
func NewProgressTracker(maxContinuations, noProgressLimit int) *ProgressTracker {
	if maxContinuations <= 0 {
		maxContinuations = 1
	}
	if noProgressLimit <= 0 {
		noProgressLimit = 1
	}
	return &ProgressTracker{
		maxContinuations: maxContinuations,
		noProgressLimit:  noProgressLimit,
	}
}

// MaxContinuations returns the configured maximum semantic continuation budget.
func (t *ProgressTracker) MaxContinuations() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.maxContinuations
}

// NoProgressLimit returns the configured consecutive no-progress limit.
func (t *ProgressTracker) NoProgressLimit() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.noProgressLimit
}

// Record observes a new progress fingerprint and evaluates continuation safety.
func (t *ProgressTracker) Record(fp ProgressFingerprint) ProgressResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.terminalReached {
		return ProgressResult{
			NewProgress:           false,
			NoProgressTripped:     t.noProgressTripped,
			BudgetExhausted:       t.budgetExhausted,
			ConsecutiveNoProgress: t.consecutiveNoProgress,
			TotalContinuations:    t.totalContinuations,
			Action:                ActionForwardTerminal,
		}
	}

	digest := fp.Digest()
	var newProgress bool

	if !t.hasBaseline {
		t.hasBaseline = true
		t.lastDigest = digest
		newProgress = true
		t.consecutiveNoProgress = 0
	} else if digest == t.lastDigest {
		newProgress = false
		t.consecutiveNoProgress++
	} else {
		newProgress = true
		t.lastDigest = digest
		t.consecutiveNoProgress = 0
	}

	t.totalContinuations++

	if t.consecutiveNoProgress >= t.noProgressLimit {
		t.noProgressTripped = true
	}
	if t.totalContinuations >= t.maxContinuations {
		t.budgetExhausted = true
	}

	action := ActionContinueLeg
	if t.noProgressTripped || t.budgetExhausted {
		t.terminalReached = true
		action = ActionForwardTerminal
	}

	return ProgressResult{
		NewProgress:           newProgress,
		NoProgressTripped:     t.noProgressTripped,
		BudgetExhausted:       t.budgetExhausted,
		ConsecutiveNoProgress: t.consecutiveNoProgress,
		TotalContinuations:    t.totalContinuations,
		Action:                action,
	}
}

// Cancel terminates tracking immediately and returns a terminal action.
func (t *ProgressTracker) Cancel() ProgressResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.cancelled = true
	t.terminalReached = true

	return ProgressResult{
		NewProgress:           false,
		NoProgressTripped:     t.noProgressTripped,
		BudgetExhausted:       t.budgetExhausted,
		ConsecutiveNoProgress: t.consecutiveNoProgress,
		TotalContinuations:    t.totalContinuations,
		Action:                ActionForwardTerminal,
	}
}
