package featurehost

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/conversationprojection"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/conversationview"
	"github.com/uptrace/bun"
)

// autoRegisteringConversationStore wraps a conversationview.Store to automatically
// register/create A-legs on demand and safely return empty snapshots when no
// steering or exclusion tags have been registered for an A-leg yet.
type autoRegisteringConversationStore struct {
	inner conversationview.Store
}

var _ conversationview.Store = (*autoRegisteringConversationStore)(nil)

func (s *autoRegisteringConversationStore) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	if s.inner == nil {
		return conversationprojection.Snapshot{}, nil
	}
	snap, err := s.inner.Snapshot(ctx, aLegID)
	if err != nil {
		if errors.Is(err, conversationview.ErrALegNotFound) {
			return conversationprojection.Snapshot{}, nil
		}
		return conversationprojection.Snapshot{}, err
	}
	return snap, nil
}

func (s *autoRegisteringConversationStore) TagNeverBackend(ctx context.Context, aLegID string, tags []conversationview.TagRequest) (conversationview.TagResult, error) {
	if s.inner == nil {
		return conversationview.TagResult{}, errors.New("featurehost: conversation store is nil")
	}
	if creator, ok := s.inner.(interface {
		CreateALeg(context.Context, string) error
	}); ok {
		_ = creator.CreateALeg(ctx, aLegID)
	}
	return s.inner.TagNeverBackend(ctx, aLegID, tags)
}

func (s *autoRegisteringConversationStore) PutSteering(ctx context.Context, aLegID string, req conversationview.PutSteeringRequest) (conversationview.SteeringState, error) {
	if s.inner == nil {
		return conversationview.SteeringState{}, errors.New("featurehost: conversation store is nil")
	}
	if creator, ok := s.inner.(interface {
		CreateALeg(context.Context, string) error
	}); ok {
		_ = creator.CreateALeg(ctx, aLegID)
	}
	return s.inner.PutSteering(ctx, aLegID, req)
}

func (s *autoRegisteringConversationStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (conversationview.SteeringState, error) {
	if s.inner == nil {
		return conversationview.SteeringState{}, errors.New("featurehost: conversation store is nil")
	}
	return s.inner.DeactivateSteering(ctx, aLegID, overlayID)
}

func (s *autoRegisteringConversationStore) CreateALeg(ctx context.Context, aLegID string) error {
	if creator, ok := s.inner.(interface {
		CreateALeg(context.Context, string) error
	}); ok {
		return creator.CreateALeg(ctx, aLegID)
	}
	return nil
}

func (s *autoRegisteringConversationStore) DeleteALeg(ctx context.Context, aLegID string) error {
	if deleter, ok := s.inner.(interface {
		DeleteALeg(context.Context, string) error
	}); ok {
		return deleter.DeleteALeg(ctx, aLegID)
	}
	return nil
}

func (s *autoRegisteringConversationStore) DB() *bun.DB {
	if s == nil || s.inner == nil {
		return nil
	}
	if provider, ok := s.inner.(interface{ DB() *bun.DB }); ok {
		return provider.DB()
	}
	return nil
}

func wrapConversationStore(store conversationview.Store) conversationview.Store {
	if store == nil {
		return nil
	}
	if _, ok := store.(*autoRegisteringConversationStore); ok {
		return store
	}
	return &autoRegisteringConversationStore{inner: store}
}

// Package-local constructor seam for testing and custom injection.
var newConversationStore = func() conversationview.Store {
	return wrapConversationStore(conversationview.NewReferenceStore())
}

// ConversationStore returns the featurehost-owned conversation-view store, if owned.
func (r *Runtime) ConversationStore() conversationview.Store {
	if r == nil {
		return nil
	}
	return r.conversationStore
}

// ConversationReader returns the featurehost-owned conversation projection reader, if owned.
func (r *Runtime) ConversationReader() conversationprojection.Reader {
	if r == nil || r.conversationStore == nil {
		return nil
	}
	return r.conversationStore
}
