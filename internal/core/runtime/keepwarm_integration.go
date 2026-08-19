package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func promptCacheObservationSource(stream lipapi.ManagedEventStream) promptcache.ObservationSource {
	if stream == nil {
		return nil
	}
	source, _ := stream.(promptcache.ObservationSource)
	return source
}

type backendPromptCacheController struct {
	backend execbackend.Backend
}

func promptCacheControllerFor(backend execbackend.Backend) promptcache.Controller {
	// A renewable target is only safe when both halves of its lifecycle seam are
	// present. A release-only or renew-only backend remains observation-only;
	// otherwise the scheduler could arm a handle it cannot control or forget.
	if backend.RenewPromptCache == nil || backend.ReleasePromptCache == nil {
		return nil
	}
	return backendPromptCacheController{backend: backend}
}

func (c backendPromptCacheController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	if c.backend.RenewPromptCache == nil {
		return promptcache.RenewResponse{}, promptcache.ErrControlUnsupported
	}
	return c.backend.RenewPromptCache(ctx, req)
}

func (c backendPromptCacheController) Release(ctx context.Context, req promptcache.ReleaseRequest) error {
	return c.backend.ReleasePromptCache(ctx, req)
}

// commitSuccessfulTurn is the single stream-to-maintenance boundary. The
// terminal handlers only announce that the response_finished event committed;
// this method snapshots observations and tool evidence exactly once.
func (s *retryRecvStream) commitSuccessfulTurn() {
	if s == nil || s.executor == nil || s.executor.Keepwarm == nil {
		return
	}
	s.keepwarmArmOnce.Do(func() {
		if s.promptCacheSource == nil || s.promptCacheController == nil {
			return
		}
		observations := s.promptCacheSource.DrainPromptCacheObservations()
		if len(observations) == 0 {
			return
		}
		attempt := s.attempt.require()
		s.executor.Keepwarm.ArmCommittedTurn(keepwarm.ArmInput{
			ALegID:              s.facts.aLegID,
			BLegID:              attempt.bleg.BLegID,
			CommittedSuccessful: s.isCommitted(),
			ToolEvents:          s.committedToolEventsSnapshot(),
			Observations:        observations,
			BackendInstanceID:   attempt.cand.Primary.Backend,
			CanonicalModelID:    attempt.cand.Primary.Model,
			Controller:          s.promptCacheController,
		})
	})
}

func (s *retryRecvStream) committedToolEventsSnapshot() []lipapi.ToolEvent {
	if s == nil {
		return nil
	}
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	return append([]lipapi.ToolEvent(nil), s.committedTools...)
}
