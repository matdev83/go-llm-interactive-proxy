package lipapi

import (
	"errors"
	"fmt"
)

// ErrRecoverablePreOutput is a stable sentinel for upstream failures that the core
// may swallow and retry on another route candidate before client-visible output begins.
var ErrRecoverablePreOutput = errors.New("recoverable pre-output upstream failure")

// OutputPhase classifies whether visible output had started when the failure occurred.
type OutputPhase string

const (
	PhasePreOutput  OutputPhase = "pre_output"
	PhasePostOutput OutputPhase = "post_output"
)

// UpstreamFailure is a structured upstream error for orchestration (executor, diagnostics).
type UpstreamFailure struct {
	Phase        OutputPhase
	Recoverable  bool
	Reason       string
	CandidateKey string
}

func (e *UpstreamFailure) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	if e.Recoverable && e.Phase == PhasePreOutput {
		return "upstream failure (recoverable, pre-output)"
	}
	return "upstream failure"
}

// Unwrap returns ErrRecoverablePreOutput when a pre-output failure is recoverable.
func (e *UpstreamFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Recoverable && e.Phase == PhasePreOutput {
		return ErrRecoverablePreOutput
	}
	return nil
}

// RecoverablePreOutputError wraps err as a recoverable pre-output failure for errors.Is/As.
func RecoverablePreOutputError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrRecoverablePreOutput, err)
}

// IsRecoverablePreOutput reports whether err should allow another backend attempt
// before client-visible output has been committed for the active attempt.
func IsRecoverablePreOutput(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRecoverablePreOutput) {
		return true
	}
	var uf *UpstreamFailure
	return errors.As(err, &uf) && uf.Recoverable && uf.Phase == PhasePreOutput
}
