package featurehost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/keepwarm"
	adminkeepwarm "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/admin/keepwarm"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	lipplugin "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/plugin"
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

// keepwarmGenerationLifecycle is the ledger-owned keep-warm generation handle.
// Start launches the generation manager at candidate prepare; Stop releases it
// (process-registry unregister plus manager quiesce) on quiesce, rollback, or
// generation close, so rejected candidates never leak managers into the
// process registry and retired generations stop maintenance work at quiesce.
// SafeUnderCandidateOverlap is satisfied structurally so the generic candidate
// ledger accepts the handle without featurehost importing generic runtimebundle.
type keepwarmGenerationLifecycle struct {
	manager  *keepwarm.Manager
	registry *keepwarm.ManagerRegistry
	regID    uint64
}

// QuiescePhaseName identifies the ledger phase carrying the keep-warm stop
// action. The generic candidate ledger runs PhaseQuiesce entries on Quiesce
// (at generation retirement), on Rollback, and skips them on Close only after
// a prior Quiesce, matching the previous dedicated quiesce-path semantics
// (Requirement 6.5: quiesce ordering — maintenance work stops at retirement,
// not at final close).
const QuiescePhaseName = "keepwarm-generation"

func (l *keepwarmGenerationLifecycle) Start(context.Context) error {
	if l == nil || l.manager == nil {
		return nil
	}
	// The manager starts and registers here (candidate prepare), not at
	// composition: failures before prepare leave no trace in the process
	// registry, and ledger rollback releases everything adopted after it.
	l.manager.Start()
	if l.registry != nil {
		id, err := l.registry.Register(l.manager)
		if err != nil {
			return err
		}
		l.regID = id
	}
	return nil
}

func (l *keepwarmGenerationLifecycle) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}
	var err error
	if l.registry != nil && l.regID != 0 {
		if uErr := l.registry.Unregister(l.regID); uErr != nil && !errors.Is(uErr, keepwarm.ErrManagerNotRegistered) {
			err = uErr
		}
	}
	if l.manager != nil {
		err = errors.Join(err, l.manager.Quiesce(ctx))
	}
	return err
}

// SafeUnderCandidateOverlap reports the handle safe under candidate overlap:
// generation managers are independent per candidate by construction.
func (l *keepwarmGenerationLifecycle) SafeUnderCandidateOverlap() bool { return true }

// QuiescePhase opts the handle into PhaseQuiesce stop semantics: the manager
// stops maintenance work at generation retirement, not only at final close
// (Requirement 6.5). Stop is idempotent, so the later Close-phase stop is a
// safe no-op after a prior Quiesce.
func (l *keepwarmGenerationLifecycle) QuiescePhase() bool { return true }

// keepwarmGenerationPorts composes the keep-warm generation attachments: the
// prompt-cache maintenance port, the ledger-owned lifecycle, the deferred
// metrics swap, and the opaque admin projection. Lifecycle and swap are nil
// when keep-warm is disabled for the generation.
func (r *Runtime) keepwarmGenerationPorts(in GenerationInput) (maint runtime.PromptCacheMaintenance, life lipplugin.Lifecycle, swap func(), admin adminkeepwarm.Options, err error) {
	maint, life, swap, err = r.compileKeepwarm(in)
	if err != nil {
		return nil, nil, nil, adminkeepwarm.Options{}, err
	}
	admin, _ = r.KeepwarmAdminProjection()
	return maint, life, swap, admin, nil
}

func (r *Runtime) compileKeepwarm(in GenerationInput) (runtime.PromptCacheMaintenance, lipplugin.Lifecycle, func(), error) {
	if r == nil {
		return nil, nil, nil, nil
	}

	cfg := in.KeepwarmConfig
	var featureFound, featureDisabled bool
	for _, reg := range in.Registrations {
		if reg.Kind != lipsdk.PluginKindFeature || (reg.ID != keepwarm.ID && reg.FactoryKind != keepwarm.ID) {
			continue
		}
		// Outer Registration.Enabled is authoritative: disabled entries are
		// skipped, a lone disabled entry disables the feature (no defaults).
		if !reg.Enabled {
			featureDisabled = true
			continue
		}
		featureFound = true
		decoded, err := keepwarm.DecodeConfig(reg.Config.Node)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("featurehost: keepwarm config: %w", err)
		}
		cfg = decoded
		break
	}
	if featureDisabled && !featureFound {
		return nil, nil, nil, nil
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

	// Registration happens in lifecycle Start (candidate prepare); a manager
	// that never starts is never registered and needs no release.

	// Featurehost-owned swap: publishes the concrete manager to the
	// process-registered collector when invoked once per published
	// generation; nil without a registered collector.
	var swap func()
	if mc, m := r.keepwarmMetrics, mgr; mc != nil && m != nil {
		swap = func() { mc.SetManager(m) }
	}

	orch := keepwarm.NewOrchestrator(mgr, r.keepwarmPolicy)
	return NewPromptCacheMaintenanceAdapter(orch),
		&keepwarmGenerationLifecycle{manager: mgr, registry: r.keepwarmRegistry},
		swap, nil
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
