package runtimebundle

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

type compositionAccountingController struct {
	response promptcache.RenewResponse
	started  chan promptcache.RenewRequest
}

func (c *compositionAccountingController) Renew(_ context.Context, request promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	c.started <- request
	return c.response, nil
}

func (c *compositionAccountingController) Release(context.Context, promptcache.ReleaseRequest) error {
	return nil
}

func TestBuildKeepwarmGenerationDeliversMaintenanceAccounting(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	current := now
	cfg := keepwarm.DefaultConfig()
	cfg.MaxRefreshesPerIdleEpoch = 1
	cfg.MaxConcurrentRenewals = 1
	cfg.RenewTimeout = time.Second
	candidate := &config.Config{
		PromptCache: config.PromptCacheConfig{
			Keepwarm:        cfg,
			KeepwarmPresent: true,
		},
	}

	inputTokens := int64(23)
	var observed billing.ProviderMaintenanceUsage
	observedUsage := make(chan billing.ProviderMaintenanceUsage, 1)
	observer := billing.ProviderMaintenanceUsageObserverFunc(func(_ context.Context, usage billing.ProviderMaintenanceUsage) {
		observedUsage <- usage
	})
	renewedObservation := promptcache.Observation{
		ALegID:            "a-leg-1",
		BLegID:            "b-leg-1",
		BackendInstanceID: "backend-1",
		TargetID:          "target-1",
		GenerationID:      "generation-renewed",
		Lifecycle:         promptcache.LifecycleSlidingExpiry,
		Timing: promptcache.Timing{
			ObservedAt: now,
			ExpiresAt:  timePtrForGenerationTest(now.Add(time.Hour)),
		},
		Renewable: true,
		Handle:    promptcache.Handle("renewed-handle"),
	}
	controller := &compositionAccountingController{
		started: make(chan promptcache.RenewRequest, 1),
		response: promptcache.RenewResponse{
			Result: promptcache.RenewResult{
				Status:      promptcache.Renewed,
				Observation: &renewedObservation,
			},
			Accounting: &promptcache.AccountingEvidence{
				InputTokens: &inputTokens,
				Presence:    lipapi.UsagePresence{InputTokens: true},
				Source:      promptcache.AccountingSourceProviderReported,
				Authority:   promptcache.AccountingAuthorityAuthoritative,
				Plane:       promptcache.AccountingPlaneProviderBillable,
				DedupeKey:   "controller-dedupe-key",
			},
		},
	}

	manager, _, err := buildKeepwarmGeneration(candidate, func() time.Time { return current }, nil, nil, observer)
	if err != nil {
		t.Fatalf("buildKeepwarmGeneration: %v", err)
	}
	defer func() {
		if err := manager.Quiesce(context.Background()); err != nil {
			t.Errorf("quiesce manager: %v", err)
		}
	}()

	inputTokensForObservation := int64(31)
	armed := manager.ArmFromCommittedTurn(keepwarm.ArmInput{
		ALegID:              "a-leg-1",
		BLegID:              "b-leg-1",
		CommittedSuccessful: true,
		ToolEvents: []lipapi.ToolEvent{{
			Kind:     lipapi.ToolEventFinished,
			Category: lipapi.ToolCategoryOSCommand,
		}},
		Observations: []promptcache.Observation{{
			ALegID:            "a-leg-1",
			BLegID:            "b-leg-1",
			BackendInstanceID: "backend-1",
			TargetID:          "target-1",
			GenerationID:      "generation-foreground",
			Lifecycle:         promptcache.LifecycleSlidingExpiry,
			Timing: promptcache.Timing{
				ObservedAt: now,
				ExpiresAt:  timePtrForGenerationTest(now.Add(time.Minute)),
			},
			Renewable: true,
			Handle:    promptcache.Handle("foreground-handle"),
			Evidence:  promptcache.CacheEvidence{TotalTokens: &inputTokensForObservation},
		}},
		BackendInstanceID: "backend-1",
		CanonicalModelID:  "model-1",
		Controller:        controller,
	})
	if !armed.Armed {
		t.Fatalf("arm result = %+v", armed)
	}

	current = current.Add(55 * time.Second)
	manager.RunDue(context.Background())

	var request promptcache.RenewRequest
	select {
	case request = <-controller.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for composed keep-warm renewal")
	}
	select {
	case observed = <-observedUsage:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for maintenance accounting delivery")
	}

	if request.OperationID == "" {
		t.Fatal("renewal operation ID is empty")
	}
	if observed.OperationID != request.OperationID || observed.ALegID != "a-leg-1" ||
		observed.TargetID != "target-1" || observed.BackendID != "backend-1" || observed.ModelID != "model-1" {
		t.Fatalf("composed maintenance usage = %+v, request = %+v", observed, request)
	}
	if observed.Evidence.InputTokens.Value != inputTokens || !observed.Evidence.InputTokens.Present ||
		observed.Evidence.OutputTokens.Present || observed.Evidence.TotalTokens.Present {
		t.Fatalf("composed maintenance evidence = %+v", observed.Evidence)
	}
	if observed.Evidence.Source != billing.EvidenceSourceProviderReported ||
		observed.Evidence.Authority != billing.EvidenceAuthorityAuthoritative ||
		observed.Evidence.DedupeKey != request.OperationID {
		t.Fatalf("composed maintenance metadata = %+v", observed.Evidence)
	}
}

func timePtrForGenerationTest(value time.Time) *time.Time {
	return &value
}
