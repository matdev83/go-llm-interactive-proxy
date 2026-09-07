package featurehost

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	compactiondetect "github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactiondetect"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/compactioncontinuity/state"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/interleavedthinking"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost/compaction"
	"github.com/uptrace/bun"
)

// constructionStep represents a staged feature construction action during NewProcess.
// It is unexported: only package-local _test.go seams may stage construction;
// external callers cannot inject constructors (Tasks 2.1/2.3, Requirements 2.5/8.4).
type constructionStep struct {
	Name      string
	Construct func(r *Runtime) error
}

// Package-local constructor seams for construction-counting tests.
// These variables alias the production constructors; package-local _test.go
// files may substitute counting delegates. They are unexported, so no external
// caller can substitute factories (Tasks 2.1/2.3, Requirements 2.5/8.4).
var (
	newCompactionDetector   = compactiondetect.New
	newBranchCoordinator    = state.NewBranchCoordinator
	newCompactionParentPort = compaction.NewParentPort
	newInterleavedProcessor = interleavedthinking.NewProcessor
)

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

	// Compaction process ownership (Task 3.3): detector, coordinator, parent port.
	// None of these resources implement io.Closer; no closer registration required.
	r.compactionDetector = newCompactionDetector(compactiondetect.Config{})

	coord, err := newBranchCoordinator(ctx, state.Config{Store: in.ExtensionState})
	if err != nil {
		return rollback(fmt.Errorf("featurehost: branch coordinator: %w", err))
	}
	r.branchCoordinator = coord

	port, err := newCompactionParentPort(coord)
	if err != nil {
		return rollback(fmt.Errorf("featurehost: parent port: %w", err))
	}
	r.compactionParentPort = port

	// Conversation view process ownership (Task 4.3): store.
	bunDB := in.BunDB
	if bunDB == nil && in.ContinuityStore != nil {
		for s := any(in.ContinuityStore); s != nil; {
			if provider, ok := s.(interface{ DB() *bun.DB }); ok {
				bunDB = provider.DB()
				break
			}
			if u, ok := s.(interface{ Unwrap() any }); ok {
				s = u.Unwrap()
			} else {
				break
			}
		}
	}
	if bunDB != nil {
		if err := conversationview.EnsureSchema(ctx, bunDB); err != nil {
			return rollback(fmt.Errorf("featurehost: conversationview ensure schema: %w", err))
		}
		r.conversationStore = wrapConversationStore(conversationview.NewBunStore(bunDB))
	} else {
		r.conversationStore = newConversationStore()
	}
	if in.ContinuityStore != nil {
		for s := any(in.ContinuityStore); s != nil; {
			if retired, ok := s.(b2bua.ALegRetirementObserver); ok {
				if deleter, ok := r.conversationStore.(interface {
					DeleteALeg(context.Context, string) error
				}); ok {
					retired.SetALegRetirementObserver(func(aLegID string) {
						_ = deleter.DeleteALeg(context.Background(), aLegID)
					})
				}
				break
			}
			if u, ok := s.(interface{ Unwrap() any }); ok {
				s = u.Unwrap()
			} else {
				break
			}
		}
	}

	return r, nil
}
