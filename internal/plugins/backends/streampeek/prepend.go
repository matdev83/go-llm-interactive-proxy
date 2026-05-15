package streampeek

import (
	"context"
	"io"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// prependFirst yields one buffered event already read from the producer, then delegates
// to rest for subsequent events (rest must not re-emit that first event).
type prependFirst struct {
	first lipapi.Event
	rest  lipapi.EventStream
	sent  bool
}

type managedPrependFirst struct {
	*prependFirst
}

var (
	_ lipapi.EventStream        = (*prependFirst)(nil)
	_ lipapi.ManagedEventStream = (*managedPrependFirst)(nil)
)

// NewPrependFirst returns an EventStream whose first Recv returns first without calling rest.
// Later Recvs delegate to rest; if rest is nil, the second Recv returns io.EOF.
// Close forwards to rest when non-nil.
func NewPrependFirst(first lipapi.Event, rest lipapi.EventStream) lipapi.EventStream {
	return &prependFirst{first: first, rest: rest}
}

// NewManagedPrependFirst is the backend lifecycle-preserving form of NewPrependFirst.
func NewManagedPrependFirst(first lipapi.Event, rest lipapi.ManagedEventStream) lipapi.ManagedEventStream {
	return &managedPrependFirst{prependFirst: &prependFirst{first: first, rest: rest}}
}

func (e *prependFirst) Recv(ctx context.Context) (lipapi.Event, error) {
	if !e.sent {
		e.sent = true
		return e.first, nil
	}
	if e.rest == nil {
		return lipapi.Event{}, io.EOF
	}
	return e.rest.Recv(ctx)
}

func (e *prependFirst) Close() error {
	if e.rest == nil {
		return nil
	}
	return e.rest.Close()
}

func (e *managedPrependFirst) Cancel(ctx context.Context, cause lipapi.CancelCause) lipapi.CancelResult {
	if e == nil || e.rest == nil {
		return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
	}
	rest, ok := e.rest.(lipapi.ManagedEventStream)
	if !ok {
		return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly}
	}
	return rest.Cancel(ctx, cause)
}
