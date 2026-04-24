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

var _ lipapi.EventStream = (*prependFirst)(nil)

// NewPrependFirst returns an EventStream whose first Recv returns first without calling rest.
// Later Recvs delegate to rest; if rest is nil, the second Recv returns io.EOF.
// Close forwards to rest when non-nil.
func NewPrependFirst(first lipapi.Event, rest lipapi.EventStream) lipapi.EventStream {
	return &prependFirst{first: first, rest: rest}
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
