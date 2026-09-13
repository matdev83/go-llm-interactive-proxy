package openaiusage

import (
	"context"
	"io"
	"strconv"
	"sync"

	coremetering "github.com/matdev83/go-llm-interactive-proxy/internal/core/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkmetering "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

// providerEventStream is the non-streaming companion to the family SSE
// adapters. It keeps canonical events available to the caller while retaining
// provider evidence in a host-only V2 buffer until the runtime binds the
// trusted B-leg identity.
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

// NewProviderEvidenceStream wraps a complete family response with the same
// host-only V2 observation seam used by streaming adapters. It is intended for
// non-streaming provider responses only; no money is rated or posted here.
func NewProviderEvidenceStream(events []lipapi.Event, mapping string) lipapi.ManagedEventStream {
	s := &providerEventStream{
		events:                 append([]lipapi.Event(nil), events...),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
	for index, event := range s.events {
		if event.Kind != lipapi.EventUsageDelta {
			continue
		}
		sourceKey := event.Accounting.DedupeKey
		if sourceKey == "" {
			sourceKey = mapping + ":usage:" + itoa(index)
		}
		s.ProviderEvidenceBuffer.Add(ProviderEvidenceDraft(event, mapping, sourceKey))
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

func (s *providerEventStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s != nil && s.ProviderEvidenceBuffer != nil {
		s.ProviderEvidenceBuffer.BindEconomicEvidence(identity)
	}
}

func (s *providerEventStream) DrainEconomicObservations() []sdkmetering.Observation {
	if s == nil || s.ProviderEvidenceBuffer == nil {
		return nil
	}
	return s.ProviderEvidenceBuffer.DrainEconomicObservations()
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	return strconv.Itoa(value)
}
