package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func promptCacheObservationSource(stream lipapi.ManagedEventStream) promptcache.ObservationSource {
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
	return c.backend.RenewPromptCache(ctx, req)
}

func (c backendPromptCacheController) Release(ctx context.Context, req promptcache.ReleaseRequest) error {
	return c.backend.ReleasePromptCache(ctx, req)
}

// commitSuccessfulTurn is the single response-to-maintenance boundary. The
// terminal handlers only announce that the response_finished event committed;
// this method snapshots attempt-local prompt-cache observations and logical
// response tool evidence exactly once.
func (p *responsePipeline) commitSuccessfulTurn(facts recvTurnFacts, attempt *attemptSession, committed bool) {
	if p == nil || p.keepwarm == nil || attempt == nil {
		return
	}
	p.keepwarmArmOnce.Do(func() {
		source, controller := attempt.promptCacheSideband()
		if source == nil || controller == nil {
			return
		}
		observations := source.DrainPromptCacheObservations()
		if len(observations) == 0 {
			return
		}
		p.keepwarm.ArmCommittedTurn(keepwarm.ArmInput{
			ALegID:              facts.aLegID,
			BLegID:              attempt.bleg.BLegID,
			CommittedSuccessful: committed,
			ToolEvents:          p.committedToolEventsSnapshot(),
			Observations:        observations,
			BackendInstanceID:   attempt.cand.Primary.Backend,
			CanonicalModelID:    attempt.cand.Primary.Model,
			Controller:          controller,
		})
	})
}
