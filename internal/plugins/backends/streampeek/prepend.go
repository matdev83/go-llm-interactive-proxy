package streampeek

import (
	"context"
	"errors"
	"io"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipsdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// PeekFirst peeks the first event from a managed stream and returns a prepended
// stream. If the first Recv fails, the stream is closed and the error returned.
func PeekFirst(ctx context.Context, es lipapi.ManagedEventStream) (lipapi.ManagedEventStream, error) {
	if es == nil {
		return nil, io.EOF
	}
	ev, rerr := es.Recv(ctx)
	if rerr == nil {
		return NewManagedPrependFirst(ev, es), nil
	}
	if closeErr := es.Close(); closeErr != nil {
		return nil, errors.Join(rerr, closeErr)
	}
	return nil, rerr
}

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

// DrainPromptCacheObservations forwards the host-only sideband through the
// first-event adapter. The canonical stream wrapper must not hide backend
// observations from the runtime arm adapter.
func (e *managedPrependFirst) DrainPromptCacheObservations() []promptcache.Observation {
	if e == nil || e.rest == nil {
		return nil
	}
	source, ok := e.rest.(promptcache.ObservationSource)
	if !ok {
		return nil
	}
	return source.DrainPromptCacheObservations()
}

// DrainUsageEvidence forwards the legacy host-only V1 sideband through the
// first-event adapter. The canonical first event is already buffered, so this
// method only delegates the provider-owned evidence queue.
func (e *managedPrependFirst) DrainUsageEvidence() []lipapi.Event {
	if e == nil || e.rest == nil {
		return nil
	}
	source, ok := e.rest.(lipapi.UsageEvidenceSource)
	if !ok {
		return nil
	}
	return source.DrainUsageEvidence()
}

// DrainEconomicObservations forwards provider-neutral V2 observations through
// the first-event adapter. The wrapper must not hide an economic source from
// the runtime terminal owner.
func (e *managedPrependFirst) DrainEconomicObservations() []lipsdkmetering.Observation {
	if e == nil || e.rest == nil {
		return nil
	}
	source, ok := e.rest.(lipsdkmetering.ObservationSource)
	if !ok {
		return nil
	}
	return source.DrainEconomicObservations()
}

// AccountingEvidenceEnabled preserves the underlying stream's negotiation
// state. The wrapper always exposes a drain method for composition, so the
// runtime must not mistake an empty, disabled sideband for an authoritative
// economic source.
func (e *managedPrependFirst) AccountingEvidenceEnabled() bool {
	if e == nil || e.rest == nil {
		return false
	}
	if state, ok := e.rest.(interface{ AccountingEvidenceEnabled() bool }); ok {
		return state.AccountingEvidenceEnabled()
	}
	if _, ok := e.rest.(lipsdkmetering.ObservationSource); ok {
		return true
	}
	if _, ok := e.rest.(lipapi.UsageEvidenceSource); ok {
		return true
	}
	return false
}

// BindEconomicEvidence forwards the trusted B-leg binding to the underlying
// provider stream before its sideband is drained.
func (e *managedPrependFirst) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if e == nil || e.rest == nil {
		return
	}
	binder, ok := e.rest.(coremetering.ProviderEvidenceBinder)
	if ok {
		binder.BindEconomicEvidence(identity)
	}
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
