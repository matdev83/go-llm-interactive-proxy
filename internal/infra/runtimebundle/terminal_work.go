package runtimebundle

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork"
	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metrics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

// TerminalWorkReadiness is composition-root status for terminal-work recovery (task 4.4/4.5).
type TerminalWorkReadiness struct {
	Configured            bool
	Running               bool
	UnresolvedProviderIDs []string
	StoreReady            bool
	// StoreError is deprecated and always empty; use ErrorCode.
	StoreError string
	// BacklogKnown is true only when MetricsObserver.Snapshot succeeded.
	BacklogKnown bool
	// Backlog is outstanding terminal-work count from MetricsObserver.
	Backlog int
	// ErrorCode is an operator-safe code (never raw store/snapshot error text).
	ErrorCode string
}

// terminalWorkRuntime is composition-root ownership for terminal-work recovery
// (tasks 4.4–4.5): processor, intents, queries, metrics, and readiness.
type terminalWorkRuntime struct {
	Processor *terminalworkapp.Processor
	Registry  *terminalworkapp.Registry
	Store     terminalworkapp.RecoveryStore
	Intents   *terminalworkapp.IntentService
	Queries   *terminalworkapp.QueryService
	Metrics   *terminalworkapp.MetricsObserver

	checkReady   func(context.Context) error
	storeBacking string
	clock        func() time.Time
	prom         *metrics.TerminalWorkProm
	snapshotPub  *snapshotgen.Publisher
}

func (rt *terminalWorkRuntime) bindSnapshotPublisher(pub *snapshotgen.Publisher) {
	if rt == nil {
		return
	}
	rt.snapshotPub = pub
	if rt.Processor == nil || pub == nil {
		return
	}
	rt.Processor.SetOnTerminalDone(func(rec terminalwork.WorkRecord) {
		gid, err := strconv.ParseInt(strings.TrimSpace(rec.Versions.GenerationID), 10, 64)
		if err != nil || gid <= 0 {
			return
		}
		pub.ClearPendingProvider(gid, rec.ProviderID)
	})
}

type terminalWorkBuildInput struct {
	Store         terminalworkapp.RecoveryStore
	Providers     []terminalworkapp.EffectProvider
	OwnerID       string
	ClaimTTL      time.Duration
	ClaimLimit    int
	GlobalMax     int
	PerProvMax    int
	TickInterval  time.Duration
	RenewInterval time.Duration
	Clock         func() time.Time
	StoreBacking  string
	Prom          *metrics.TerminalWorkProm
	SnapshotPub   *snapshotgen.Publisher
}

func buildTerminalWorkFromProduction(prod ProductionOptions, clock func() time.Time, bundle *metrics.Bundle) (
	*terminalWorkRuntime,
	[]func() error,
	error,
) {
	if prod.TerminalWorkStore == nil {
		return nil, nil, nil
	}
	providers, err := composeTerminalWorkProviders(prod)
	if err != nil {
		return nil, nil, err
	}
	var prom *metrics.TerminalWorkProm
	if bundle != nil {
		prom = bundle.TerminalWork
	}
	return buildTerminalWorkRuntime(terminalWorkBuildInput{
		Store:         prod.TerminalWorkStore,
		Providers:     providers,
		OwnerID:       prod.TerminalWorkOwnerID,
		ClaimTTL:      prod.TerminalWorkClaimTTL,
		ClaimLimit:    prod.TerminalWorkClaimLimit,
		GlobalMax:     prod.TerminalWorkGlobalMax,
		PerProvMax:    prod.TerminalWorkPerProviderMax,
		TickInterval:  prod.TerminalWorkTickInterval,
		RenewInterval: prod.TerminalWorkRenewInterval,
		Clock:         clock,
		StoreBacking:  "injected",
		Prom:          prom,
	})
}

func composeTerminalWorkProviders(prod ProductionOptions) ([]terminalworkapp.EffectProvider, error) {
	byID := make(map[string]terminalworkapp.EffectProvider, len(prod.RequestRegistrations)+len(prod.TerminalWorkProviders))
	order := make([]string, 0, len(prod.RequestRegistrations)+len(prod.TerminalWorkProviders))
	for _, reg := range prod.RequestRegistrations {
		id := strings.TrimSpace(reg.Descriptor.ID)
		if id == "" || reg.Provider == nil {
			continue
		}
		effect, err := terminalworkapp.NewAuthorityRequestEffectProvider(terminalworkapp.AuthorityRequestEffectConfig{
			ProviderID: id,
			Provider:   reg.Provider,
			Version:    requestRegistrationEffectVersion(reg),
		})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: derive terminal work provider %q: %w", id, err)
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = effect
	}
	for _, p := range prod.TerminalWorkProviders {
		if p == nil {
			continue
		}
		id := strings.TrimSpace(p.ProviderID())
		if id == "" {
			return nil, fmt.Errorf("runtimebundle: terminal work provider: empty provider id")
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = p
	}
	out := make([]terminalworkapp.EffectProvider, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

func requestRegistrationEffectVersion(reg authority.RequestRegistration) string {
	type versioned interface{ Version() string }
	if v, ok := any(reg.Provider).(versioned); ok {
		if s := strings.TrimSpace(v.Version()); s != "" {
			return s
		}
	}
	return "1"
}

func buildTerminalWorkRuntime(in terminalWorkBuildInput) (*terminalWorkRuntime, []func() error, error) {
	if in.Store == nil {
		return nil, nil, nil
	}
	reg := terminalworkapp.NewRegistry()
	for _, p := range in.Providers {
		if err := reg.Register(p); err != nil {
			return nil, nil, fmt.Errorf("runtimebundle: terminal work provider: %w", err)
		}
	}
	owner := strings.TrimSpace(in.OwnerID)
	if owner == "" {
		owner = "runtimebundle"
	}
	claimTTL := in.ClaimTTL
	if claimTTL <= 0 {
		claimTTL = time.Minute
	}
	tickInterval := in.TickInterval
	if tickInterval <= 0 {
		tickInterval = time.Second
	}
	renewInterval := in.RenewInterval
	if renewInterval <= 0 {
		renewInterval = claimTTL / 3
		if renewInterval <= 0 {
			renewInterval = 20 * time.Second
		}
	}
	clock := in.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	metricsObs := terminalworkapp.NewMetricsObserver(in.Store, terminalworkapp.MetricsConfig{Clock: clock})
	cfg := terminalworkapp.Config{
		OwnerID:        owner,
		ClaimTTL:       claimTTL,
		ClaimLimit:     in.ClaimLimit,
		GlobalMax:      in.GlobalMax,
		PerProviderMax: in.PerProvMax,
		TickInterval:   tickInterval,
		RenewInterval:  renewInterval,
		Clock:          clockFunc{now: clock},
	}
	if pub := in.SnapshotPub; pub != nil {
		cfg.OnTerminalDone = func(rec terminalwork.WorkRecord) {
			gid, err := strconv.ParseInt(strings.TrimSpace(rec.Versions.GenerationID), 10, 64)
			if err != nil || gid <= 0 {
				return
			}
			pub.ClearPendingProvider(gid, rec.ProviderID)
		}
	}
	if obs := newTerminalWorkProcessObserver(metricsObs, in.Prom); obs != nil {
		cfg.Metrics = obs
	}
	proc, err := terminalworkapp.NewProcessor(in.Store, reg, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: terminal work processor: %w", err)
	}
	backing := strings.TrimSpace(in.StoreBacking)
	if backing == "" {
		backing = "injected"
	}
	rt := &terminalWorkRuntime{
		Processor:    proc,
		Registry:     reg,
		Store:        in.Store,
		Intents:      terminalworkapp.NewIntentService(in.Store, terminalworkapp.IntentServiceConfig{Clock: clock}),
		Queries:      terminalworkapp.NewQueryService(in.Store),
		Metrics:      metricsObs,
		storeBacking: backing,
		clock:        clock,
		prom:         in.Prom,
		snapshotPub:  in.SnapshotPub,
	}
	if ready, ok := in.Store.(interface {
		CheckReadiness(context.Context) error
	}); ok {
		rt.checkReady = ready.CheckReadiness
	}
	if err := proc.Start(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: terminal work start: %w", err)
	}
	closers := []func() error{
		func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return proc.Shutdown(ctx)
		},
	}
	return rt, closers, nil
}

// Readiness reports running state, unresolved providers, backlog, and store readiness.
func (rt *terminalWorkRuntime) Readiness(ctx context.Context) TerminalWorkReadiness {
	out := TerminalWorkReadiness{}
	if rt == nil || rt.Processor == nil {
		return out
	}
	out.Configured = true
	snap := rt.Processor.Readiness()
	out.Running = snap.Running
	out.UnresolvedProviderIDs = append([]string(nil), snap.UnresolvedProviderIDs...)
	if rt.snapshotPub != nil {
		for _, id := range rt.snapshotPub.UnresolvedProviderIDs() {
			out.UnresolvedProviderIDs = appendUniqueString(out.UnresolvedProviderIDs, id)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	safeUnavailable := string(controlplane.ReasonBackingUnavailable)
	if rt.Metrics == nil {
		out.BacklogKnown = false
		out.ErrorCode = safeUnavailable
	} else if metricsSnap, err := rt.Metrics.Snapshot(ctx); err != nil {
		out.BacklogKnown = false
		out.ErrorCode = safeUnavailable
	} else {
		out.BacklogKnown = true
		out.Backlog = metricsSnap.Backlog
	}
	if !out.Running {
		out.ErrorCode = safeUnavailable
	}
	if rt.checkReady == nil {
		out.StoreReady = true
		return out
	}
	if err := rt.checkReady(ctx); err != nil {
		out.StoreReady = false
		out.ErrorCode = safeUnavailable
		return out
	}
	out.StoreReady = true
	return out
}

func (rt *terminalWorkRuntime) publishMetrics(ctx context.Context) error {
	if rt == nil || rt.Metrics == nil || rt.prom == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snap, err := rt.Metrics.Snapshot(ctx)
	if err != nil {
		return err
	}
	rt.prom.ApplySnapshot(metrics.TerminalWorkSnapshot{
		Backlog:      snap.Backlog,
		OldestAgeSec: snap.OldestAge.Seconds(),
		Pending:      snap.Pending,
		Retrying:     snap.Retrying,
		Quarantined:  snap.Quarantined,
		Completed:    snap.Completed,
		Claimed:      snap.Claimed,
	})
	return nil
}

func (rt *terminalWorkRuntime) readinessComponent(ctx context.Context) (controlplane.ReadinessComponentStatus, error) {
	if rt == nil || rt.Processor == nil {
		return controlplane.ReadinessComponentStatus{
			Component:        controlplane.ReadinessComponentTerminalRecovery,
			State:            controlplane.CapabilityDisabled,
			Reason:           controlplane.ReasonDisabled,
			EnforcementScope: controlplane.EnforcementScopeDisabled,
		}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	row := controlplane.ReadinessComponentStatus{
		Component:        controlplane.ReadinessComponentTerminalRecovery,
		EnforcementScope: controlplane.EnforcementScopeForStoreBacking(rt.storeBacking, strings.EqualFold(rt.storeBacking, "postgres")),
		StoreBacking:     rt.storeBacking,
	}
	if rt.checkReady != nil {
		if err := rt.checkReady(ctx); err != nil {
			row.State = controlplane.CapabilityUnavailable
			row.Reason = controlplane.ReasonBackingUnavailable
			return row, nil
		}
	}
	if !rt.Processor.Running() {
		row.State = controlplane.CapabilityUnavailable
		row.Reason = controlplane.ReasonBackingUnavailable
		return row, nil
	}
	snap := rt.Processor.Readiness()
	if len(snap.UnresolvedProviderIDs) > 0 {
		row.State = controlplane.CapabilityDegraded
		row.Reason = controlplane.ReasonPendingTerminalWork
		row.ProviderIDs = append([]string(nil), snap.UnresolvedProviderIDs...)
		return row, nil
	}
	if rt.Metrics == nil {
		row.State = controlplane.CapabilityUnavailable
		row.Reason = controlplane.ReasonBackingUnavailable
		return row, nil
	}
	metricsSnap, err := rt.Metrics.Snapshot(ctx)
	if err != nil {
		row.State = controlplane.CapabilityUnavailable
		row.Reason = controlplane.ReasonBackingUnavailable
		return row, nil
	}
	_ = rt.publishMetrics(ctx)
	if metricsSnap.Backlog > 0 {
		row.State = controlplane.CapabilityDegraded
		row.Reason = controlplane.ReasonPendingTerminalWork
		return row, nil
	}
	row.State = controlplane.CapabilityReady
	row.Reason = controlplane.ReasonNone
	return row, nil
}

// TerminalWorkReadiness returns terminal-work status when the bundle owns a processor.
func (b *Built) TerminalWorkReadiness(ctx context.Context) TerminalWorkReadiness {
	if b == nil || b.terminalWorkRT == nil {
		if b == nil || b.TerminalWorkProcessor == nil {
			return TerminalWorkReadiness{}
		}
		// Fallback when runtime pointer not retained (should not happen after Build).
		safeUnavailable := string(controlplane.ReasonBackingUnavailable)
		out := TerminalWorkReadiness{
			Configured:            true,
			Running:               b.TerminalWorkProcessor.Running(),
			UnresolvedProviderIDs: b.TerminalWorkProcessor.UnresolvedProviderIDs(),
			StoreReady:            true,
		}
		if !out.Running {
			out.ErrorCode = safeUnavailable
		}
		if b.TerminalWorkMetrics == nil {
			out.BacklogKnown = false
			out.ErrorCode = safeUnavailable
		} else if snap, err := b.TerminalWorkMetrics.Snapshot(ctx); err != nil {
			out.BacklogKnown = false
			out.ErrorCode = safeUnavailable
		} else {
			out.BacklogKnown = true
			out.Backlog = snap.Backlog
		}
		if b.terminalWorkReady != nil {
			if ctx == nil {
				ctx = context.Background()
			}
			if err := b.terminalWorkReady(ctx); err != nil {
				out.StoreReady = false
				out.ErrorCode = safeUnavailable
			}
		}
		return out
	}
	return b.terminalWorkRT.Readiness(ctx)
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

// PublishTerminalWorkMetrics pushes MetricsObserver snapshot gauges onto the
// metrics.Bundle TerminalWorkProm series.
func (b *Built) PublishTerminalWorkMetrics(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if b.terminalWorkRT != nil {
		return b.terminalWorkRT.publishMetrics(ctx)
	}
	if b.TerminalWorkMetrics == nil || b.Metrics == nil || b.Metrics.TerminalWork == nil {
		return nil
	}
	snap, err := b.TerminalWorkMetrics.Snapshot(ctx)
	if err != nil {
		return err
	}
	b.Metrics.TerminalWork.ApplySnapshot(metrics.TerminalWorkSnapshot{
		Backlog:      snap.Backlog,
		OldestAgeSec: snap.OldestAge.Seconds(),
		Pending:      snap.Pending,
		Retrying:     snap.Retrying,
		Quarantined:  snap.Quarantined,
		Completed:    snap.Completed,
		Claimed:      snap.Claimed,
	})
	return nil
}
