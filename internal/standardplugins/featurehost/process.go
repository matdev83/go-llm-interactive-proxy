package featurehost

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// constructionStep represents a staged feature construction action during NewProcess.
// It is unexported: only package-local _test.go seams may stage construction;
// external callers cannot inject constructors (Tasks 2.1/2.3, Requirements 2.5/8.4).
type constructionStep struct {
	Name      string
	Construct func(r *Runtime) error
}

// NewProcess constructs the standard-distribution featurehost process facade.
// Every successfully constructed owned feature resource is recorded before later
// construction can fail; on failure, already-registered closers are unwound in reverse order.
func NewProcess(ctx context.Context, in ProcessInput) (*Runtime, error) {
	if in.Logger == nil {
		return nil, fmt.Errorf("featurehost: nil logger")
	}
	r := &Runtime{
		logger:   in.Logger,
		extState: in.ExtensionState,
		bgAux:    in.BackgroundAux,
	}

	// Fail-before-escape: if any step fails, unwind already-registered closers in reverse order.
	rollback := func(err error) (*Runtime, error) {
		var unwindErr error
		for _, closer := range slices.Backward(r.closers) {
			if cErr := closer(); cErr != nil {
				unwindErr = errors.Join(unwindErr, fmt.Errorf("featurehost: rollback closer: %w", cErr))
			}
		}
		if unwindErr != nil {
			return nil, errors.Join(err, unwindErr)
		}
		return nil, err
	}

	for _, step := range in.buildSteps {
		if step.Construct != nil {
			if err := step.Construct(r); err != nil {
				return rollback(fmt.Errorf("featurehost: construct %s: %w", step.Name, err))
			}
		}
	}

	// In Task 2.1, no legacy process feature resources are constructed yet.
	// Explicit handoff happens atomically in Tasks 3-7/10.

	return r, nil
}
