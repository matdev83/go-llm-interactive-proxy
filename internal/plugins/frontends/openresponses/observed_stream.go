package openresponses

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipcont "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/continuation"
)

// observedEventStream records canonical events without changing executor or
// transport commitment semantics.
type observedEventStream struct {
	lipapi.EventStream
	observer lipcont.StreamObserver
}

func (s *observedEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	event, err := s.EventStream.Recv(ctx)
	if err == nil {
		safeObserveFrontend(s.observer, ctx, event)
	}
	return event, err
}

func (s *observedEventStream) Close() error {
	safeCloseObserverFrontend(s.observer)
	return s.EventStream.Close()
}

// safeObserveFrontend runs observer callbacks outside the client stream
// contract: a recording panic must never abort a delivered response. Mirrors
// the core continuationObservedStream isolation seam.
func safeObserveFrontend(observer lipcont.StreamObserver, ctx context.Context, event lipapi.Event) {
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer.Observe(ctx, event)
}

func safeCloseObserverFrontend(observer lipcont.StreamObserver) {
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	_ = observer.Close()
}

var _ lipapi.EventStream = (*observedEventStream)(nil)
