package openresponses

import (
	"context"
	"errors"

	corecontinuation "github.com/matdev83/go-llm-interactive-proxy/internal/core/continuation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// ContinuationResolver defines a narrow injected seam for resolving parent continuation state.
type ContinuationResolver interface {
	ResolveParent(ctx context.Context, scope lipcont.Scope, parentID string, baseCall lipapi.Call) (lipapi.Call, lipcont.ContinuationRecord, error)
}

// storeContinuationResolver delegates parent resolution to core MaterializeCall and lipcont.Store.
type storeContinuationResolver struct {
	store  lipcont.Store
	bounds lipcont.Bounds
}

// NewStoreContinuationResolver constructs a ContinuationResolver backed by a lipcont.Store.
func NewStoreContinuationResolver(store lipcont.Store, bounds lipcont.Bounds) ContinuationResolver {
	return &storeContinuationResolver{
		store:  store,
		bounds: bounds,
	}
}

var _ ContinuationResolver = (*storeContinuationResolver)(nil)

func (r *storeContinuationResolver) ResolveParent(ctx context.Context, scope lipcont.Scope, parentID string, baseCall lipapi.Call) (lipapi.Call, lipcont.ContinuationRecord, error) {
	if r.store == nil || parentID == "" {
		return lipapi.Call{}, lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
	}
	pid := lipcont.ResponseID(parentID)
	rec, err := lipcont.Lookup(ctx, r.store, scope, pid)
	if err != nil {
		if errors.Is(err, lipcont.ErrStorageFailure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return lipapi.Call{}, lipcont.ContinuationRecord{}, err
		}
		return lipapi.Call{}, lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
	}
	materializedCall, _, err := corecontinuation.MaterializeCall(ctx, lipcont.MaterializeInput{
		Store:    r.store,
		Scope:    scope,
		StartID:  pid,
		NewInput: baseCall.Items,
		Bounds:   r.bounds,
	}, baseCall)
	if err != nil {
		if errors.Is(err, lipcont.ErrStorageFailure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return lipapi.Call{}, lipcont.ContinuationRecord{}, err
		}
		return lipapi.Call{}, lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
	}
	return materializedCall, rec, nil
}
