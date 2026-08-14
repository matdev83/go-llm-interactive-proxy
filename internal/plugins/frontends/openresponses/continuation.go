package openresponses

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// ContinuationResolver defines a narrow injected seam for resolving parent continuation state.
type ContinuationResolver interface {
	ResolveParent(ctx context.Context, scope lipcont.Scope, parentID string, baseCall lipapi.Call) (lipapi.Call, lipcont.ContinuationRecord, error)
}

// storeContinuationResolver delegates parent resolution to the protocol-neutral SDK materializer and store.
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
	materializedCall, _, err := lipcont.MaterializeCall(ctx, lipcont.MaterializeInput{
		Store:    r.store,
		Scope:    scope,
		StartID:  pid,
		NewInput: baseCall.Items,
		Bounds:   r.bounds,
	}, baseCall)
	if err != nil {
		if isContinuationPassThroughError(err) {
			return lipapi.Call{}, lipcont.ContinuationRecord{}, err
		}
		return lipapi.Call{}, lipcont.ContinuationRecord{}, lipcont.ErrPreviousResponseNotFound
	}
	return materializedCall, rec, nil
}

func isContinuationPassThroughError(err error) bool {
	return errors.Is(err, lipcont.ErrStorageFailure) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, lipcont.ErrChainDepthExceeded) ||
		errors.Is(err, lipcont.ErrStorageLimitExceeded) ||
		errors.Is(err, lipcont.ErrMaterializedSizeExceeded) ||
		errors.Is(err, lipcont.ErrMaterializedItemsExceeded)
}

func continuationResolverFor(resolver ContinuationResolver, store lipcont.Store, bounds lipcont.Bounds) ContinuationResolver {
	if resolver != nil {
		return resolver
	}
	if store != nil {
		return NewStoreContinuationResolver(store, bounds)
	}
	return nil
}

func applyParentLineage(model *string, call *lipapi.Call, previousID *string, parent lipcont.ContinuationRecord) {
	if parent.ID != "" && previousID != nil {
		*previousID = parent.ID.String()
	}
	if model != nil && *model == "" {
		*model = parent.Lineage.Model
	}
	if call == nil {
		return
	}
	if call.Route.Selector == "" {
		call.Route.Selector = parent.Lineage.RouteSelector
		if call.Route.Selector == "" {
			call.Route.Selector = parent.Lineage.Model
		}
	}
}
