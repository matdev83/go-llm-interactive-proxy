package featurehost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/promptcache"
)

// promptCacheMaintenanceAdapter adapts keepwarm.Orchestrator to runtime.PromptCacheMaintenance.
type promptCacheMaintenanceAdapter struct {
	orchestrator *keepwarm.Orchestrator
}

var _ runtime.PromptCacheMaintenance = (*promptCacheMaintenanceAdapter)(nil)

// NewPromptCacheMaintenanceAdapter wraps a keepwarm.Orchestrator as runtime.PromptCacheMaintenance.
// Nil input produces nil output (disabled behavior).
func NewPromptCacheMaintenanceAdapter(orch *keepwarm.Orchestrator) runtime.PromptCacheMaintenance {
	if orch == nil {
		return nil
	}
	return &promptCacheMaintenanceAdapter{orchestrator: orch}
}

func (a *promptCacheMaintenanceAdapter) BeginRealTurn(aLegID string) {
	if a != nil && a.orchestrator != nil {
		a.orchestrator.BeginRealTurn(aLegID)
	}
}

func (a *promptCacheMaintenanceAdapter) EndSession(aLegID string) {
	if a != nil && a.orchestrator != nil {
		a.orchestrator.EndSession(aLegID)
	}
}

func (a *promptCacheMaintenanceAdapter) ArmCommittedTurn(turn runtime.PromptCacheCommittedTurn) {
	if a == nil || a.orchestrator == nil {
		return
	}
	a.orchestrator.ArmCommittedTurn(keepwarm.ArmInput{
		ALegID:              turn.ALegID,
		BLegID:              turn.BLegID,
		CommittedSuccessful: turn.CommittedSuccessful,
		ToolEvents:          turn.ToolEvents,
		Observations:        turn.Observations,
		BackendInstanceID:   turn.BackendInstanceID,
		CanonicalModelID:    turn.CanonicalModelID,
		Controller:          turn.Controller,
	})
}

func (r *Runtime) compileKeepwarm(in GenerationInput) (runtime.PromptCacheMaintenance, *keepwarm.Manager, func(context.Context) error, error) {
	if r == nil {
		return nil, nil, nil, nil
	}

	cfg := in.KeepwarmConfig
	var featureFound bool
	for _, reg := range in.Registrations {
		if reg.Kind == lipsdk.PluginKindFeature && (reg.ID == keepwarm.ID || reg.FactoryKind == keepwarm.ID) {
			featureFound = true
			decoded, err := keepwarm.DecodeConfig(reg.Config.Node)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("featurehost: keepwarm config: %w", err)
			}
			cfg = decoded
			break
		}
	}

	// If not explicitly configured via registration or input, default to DefaultConfig()
	if !featureFound && !cfg.Enabled && in.KeepwarmConfig.MaxRefreshesPerIdleEpoch == 0 {
		cfg = keepwarm.DefaultConfig()
	}

	if !cfg.Enabled {
		return nil, nil, nil, nil
	}

	if err := cfg.Validate(); err != nil {
		return nil, nil, nil, fmt.Errorf("featurehost: keepwarm config: %w", err)
	}

	nowFn := in.NowFn
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}

	hooks := keepwarmAccountingHooks(in.KeepwarmAccounting)
	mgr, err := keepwarm.NewManager(cfg, keepwarm.ClockFunc(nowFn), hooks)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("featurehost: keepwarm manager: %w", err)
	}

	// Reapply process-owned disabled-session policy to the new generation
	if r.keepwarmPolicy != nil {
		for _, aLegID := range r.keepwarmPolicy.DisabledALegIDs() {
			mgr.SetSessionDisabled(aLegID, true)
		}
	}

	mgr.Start()

	var regID uint64
	if r.keepwarmRegistry != nil {
		id, err := r.keepwarmRegistry.Register(mgr)
		if err != nil {
			_ = mgr.Quiesce(context.Background())
			return nil, nil, nil, fmt.Errorf("featurehost: register keep-warm manager: %w", err)
		}
		regID = id
	}

	quiesceFn := func(ctx context.Context) error {
		var uErr error
		if r.keepwarmRegistry != nil && regID != 0 {
			if err := r.keepwarmRegistry.Unregister(regID); err != nil && !errors.Is(err, keepwarm.ErrManagerNotRegistered) {
				uErr = err
			}
		}
		if mgr != nil {
			uErr = errors.Join(uErr, mgr.Quiesce(ctx))
		}
		return uErr
	}

	orch := keepwarm.NewOrchestrator(mgr, r.keepwarmPolicy)
	return NewPromptCacheMaintenanceAdapter(orch), mgr, quiesceFn, nil
}

func keepwarmAccountingHooks(observer billing.ProviderMaintenanceUsageObserver) keepwarm.Hooks {
	if observer == nil {
		return keepwarm.Hooks{}
	}
	return keepwarm.Hooks{
		Accounting: func(ctx context.Context, record keepwarm.RenewalRecord) error {
			if record.Accounting == nil {
				return nil
			}
			return observer.ObserveProviderMaintenance(ctx, billing.ProviderMaintenanceUsage{
				OperationID: record.OperationID,
				ALegID:      record.ALegID,
				TargetID:    string(record.TargetID),
				BackendID:   record.BackendID,
				ModelID:     record.ModelID,
				RecordedAt:  time.Now().UTC(),
				Evidence:    maintenanceEvidence(*record.Accounting, record.OperationID),
			})
		},
	}
}

func maintenanceEvidence(e promptcache.AccountingEvidence, dedupe string) billing.FinalBillingEvidence {
	return billing.FinalBillingEvidence{
		InputTokens:      maintenanceQuantity(e.InputTokens),
		OutputTokens:     maintenanceQuantity(e.OutputTokens),
		CacheReadTokens:  maintenanceQuantity(e.CacheReadTokens),
		CacheWriteTokens: maintenanceQuantity(e.CacheWriteTokens),
		ReasoningTokens:  maintenanceQuantity(e.ReasoningTokens),
		TotalTokens:      maintenanceQuantity(e.TotalTokens),
		Cost:             billing.MoneyEvidence{},
		Source:           billing.EvidenceSourceProviderReported,
		Authority:        billing.EvidenceAuthorityAuthoritative,
		DedupeKey:        dedupe,
	}
}

func maintenanceQuantity(value *int64) billing.Quantity {
	if value == nil {
		return billing.Quantity{}
	}
	return billing.Quantity{Value: *value, Present: true}
}
