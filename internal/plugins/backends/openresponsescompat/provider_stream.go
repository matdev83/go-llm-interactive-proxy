package openresponsescompat

import (
	"context"
	"io"
	"strconv"
	"sync"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// providerEventStream retains complete non-streaming OpenResponses events and
// exposes provider evidence through the host-only V2 seam. It is deliberately
// local to the generic codec and has no concrete provider SDK dependency.
type providerEventStream struct {
	mu     sync.Mutex
	events []lipapi.Event
	index  int
	closed bool
	*coremetering.ProviderEvidenceBuffer
}

var (
	_ lipapi.ManagedEventStream           = (*providerEventStream)(nil)
	_ sdkmetering.ObservationSource       = (*providerEventStream)(nil)
	_ coremetering.ProviderEvidenceBinder = (*providerEventStream)(nil)
)

func newProviderEventStream(events []lipapi.Event, mapping string) lipapi.ManagedEventStream {
	s := &providerEventStream{events: append([]lipapi.Event(nil), events...), ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer()}
	for index, event := range s.events {
		if event.Kind != lipapi.EventUsageDelta {
			continue
		}
		key := event.Accounting.DedupeKey
		if key == "" {
			key = mapping + ":usage:" + strconv.Itoa(index)
		}
		s.Add(providerEvidenceDraft(event, mapping, key))
	}
	return s
}

func (s *providerEventStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return lipapi.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.index >= len(s.events) {
		return lipapi.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *providerEventStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *providerEventStream) Cancel(_ context.Context, _ lipapi.CancelCause) lipapi.CancelResult {
	return lipapi.CancelResult{Mode: lipapi.CancelModeCloseOnly, Err: s.Close()}
}

// BindEconomicEvidence delegates explicitly to the embedded buffer: the
// qualifier must stay because this method shadows the promoted one.
func (s *providerEventStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s != nil && s.ProviderEvidenceBuffer != nil {
		s.ProviderEvidenceBuffer.BindEconomicEvidence(identity)
	}
}

// DrainEconomicObservations delegates explicitly to the embedded buffer: the
// qualifier must stay because this method shadows the promoted one.
func (s *providerEventStream) DrainEconomicObservations() []sdkmetering.Observation {
	if s == nil || s.ProviderEvidenceBuffer == nil {
		return nil
	}
	return s.ProviderEvidenceBuffer.DrainEconomicObservations()
}
