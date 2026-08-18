package execbackend

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

func TestEffectivePromptCacheProfile_IsModelAwareAndFailClosed(t *testing.T) {
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "model-b"}}
	be := Backend{ResolvePromptCacheProfile: func(_ context.Context, _ lipapi.Call, c routing.AttemptCandidate) promptcache.Profile {
		if c.Primary.Model == "model-b" {
			return promptcache.Profile{ObservationSupported: true, LifecycleKinds: []promptcache.LifecycleKind{promptcache.LifecycleMinimumResidency}}
		}
		return promptcache.Profile{RenewalSupported: true}
	}}
	got := EffectivePromptCacheProfile(context.Background(), be, lipapi.Call{}, cand)
	if !got.ObservationSupported || got.RenewalSupported || len(got.LifecycleKinds) != 1 {
		t.Fatalf("profile=%+v", got)
	}
	if got := EffectivePromptCacheProfile(context.Background(), Backend{}, lipapi.Call{}, cand); got.ObservationSupported || got.RenewalSupported {
		t.Fatalf("unsupported profile=%+v", got)
	}
}
