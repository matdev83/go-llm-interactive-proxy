package anthropic

import (
	"context"
	"fmt"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

type CacheEnrollmentMode string

const (
	CacheEnrollmentDisabled  CacheEnrollmentMode = "disabled"
	CacheEnrollmentAutomatic CacheEnrollmentMode = "automatic"
)

func ValidateCacheConfig(mode CacheEnrollmentMode, ttl string) error {
	mode = CacheEnrollmentMode(strings.TrimSpace(string(mode)))
	if mode == "" {
		mode = CacheEnrollmentDisabled
	}
	switch mode {
	case CacheEnrollmentDisabled:
		if strings.TrimSpace(ttl) != "" {
			return fmt.Errorf("anthropic: cache ttl requires automatic enrollment")
		}
	case CacheEnrollmentAutomatic:
		if ttl != "5m" && ttl != "1h" {
			return fmt.Errorf("anthropic: cache ttl must be 5m or 1h")
		}
	default:
		return fmt.Errorf("anthropic: unsupported cache enrollment %q", mode)
	}
	return nil
}

func effectiveCacheEnrollment(mode CacheEnrollmentMode) CacheEnrollmentMode {
	if strings.TrimSpace(string(mode)) == "" {
		return CacheEnrollmentDisabled
	}
	return mode
}

func invalidConfigBackend(err error) execbackend.Backend {
	return execbackend.Backend{Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return nil, err
	}}
}

// cacheObservationHook adapts a provider-neutral stream observation into the
// bounded controller target store. It is supplied only for automatic
// enrollment; disabled enrollment leaves the hook nil so no observation is
// issued.
func cacheObservationHook(c *CacheController, ttl string) func(context.Context, anthropicmessages.CacheObservation) (promptcache.Observation, error) {
	if c == nil {
		return nil
	}
	return func(ctx context.Context, in anthropicmessages.CacheObservation) (promptcache.Observation, error) {
		return c.IssueTarget(CacheTarget{
			ALegID:            in.Lineage.ALegID,
			BLegID:            in.Lineage.BLegID,
			BackendInstanceID: in.Lineage.BackendInstanceID,
			Model:             in.Model,
			Renewal:           in.Renewal,
			TTL:               ttl,
			Evidence:          in.Evidence,
		}, in.ObservedAt)
	}
}
