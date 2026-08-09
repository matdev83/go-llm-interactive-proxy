package openresponses

import (
	"context"
	"time"

	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// ContinuationRecorderFactory creates the observer for one reserved response.
// The observer is deliberately best-effort: terminal persistence is not part of
// the downstream output commitment.
type ContinuationRecorderFactory interface {
	NewRecorder(store lipcont.Store, record lipcont.ContinuationRecord) lipcont.StreamObserver
}

type sdkContinuationRecorderFactory struct{}

var _ ContinuationRecorderFactory = sdkContinuationRecorderFactory{}

func (sdkContinuationRecorderFactory) NewRecorder(store lipcont.Store, record lipcont.ContinuationRecord) lipcont.StreamObserver {
	cleanup := func() {
		if store == nil || record.ID.IsZero() || record.Scope.IsZero() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = store.Delete(ctx, record.Scope, record.ID)
	}
	return lipcont.NewStreamRecorder(lipcont.TerminalRecorder{Store: store}, record, cleanup)
}

func defaultContinuationRecorderFactory() ContinuationRecorderFactory {
	return sdkContinuationRecorderFactory{}
}

// connectionContinuationRecorderFactory records directly into a connection-local
// store, which has no reservation to release when an in-flight turn is canceled.
type connectionContinuationRecorderFactory struct{}

func (connectionContinuationRecorderFactory) NewRecorder(store lipcont.Store, record lipcont.ContinuationRecord) lipcont.StreamObserver {
	return lipcont.NewStreamRecorder(lipcont.TerminalRecorder{Store: store}, record, func() {})
}
