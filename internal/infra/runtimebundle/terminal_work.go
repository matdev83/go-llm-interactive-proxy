package runtimebundle

import (
	"context"
	"fmt"
	"strings"
	"time"

	terminalworkapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/terminalwork/app"
)

// TerminalWorkReadiness is composition-root status for terminal-work recovery (task 4.4/4.5).
type TerminalWorkReadiness struct {
	Configured            bool
	Running               bool
	UnresolvedProviderIDs []string
	StoreReady            bool
	StoreError            string
}

// terminalWorkRuntime is composition-root ownership for the terminal-work processor
// (task 4.4). No live terminal-owner integration is wired here.
type terminalWorkRuntime struct {
	Processor *terminalworkapp.Processor
	Registry  *terminalworkapp.Registry
	Store     terminalworkapp.WorkStore

	checkReady func(context.Context) error
}

type terminalWorkBuildInput struct {
	Store         terminalworkapp.WorkStore
	Providers     []terminalworkapp.EffectProvider
	OwnerID       string
	ClaimTTL      time.Duration
	ClaimLimit    int
	GlobalMax     int
	PerProvMax    int
	TickInterval  time.Duration
	RenewInterval time.Duration
	Clock         func() time.Time
}

func buildTerminalWorkFromProduction(prod ProductionOptions, clock func() time.Time) (
	proc *terminalworkapp.Processor,
	reg *terminalworkapp.Registry,
	ready func(context.Context) error,
	closers []func() error,
	err error,
) {
	rt, closers, err := buildTerminalWorkRuntime(terminalWorkBuildInput{
		Store:         prod.TerminalWorkStore,
		Providers:     prod.TerminalWorkProviders,
		OwnerID:       prod.TerminalWorkOwnerID,
		ClaimTTL:      prod.TerminalWorkClaimTTL,
		ClaimLimit:    prod.TerminalWorkClaimLimit,
		GlobalMax:     prod.TerminalWorkGlobalMax,
		PerProvMax:    prod.TerminalWorkPerProviderMax,
		TickInterval:  prod.TerminalWorkTickInterval,
		RenewInterval: prod.TerminalWorkRenewInterval,
		Clock:         clock,
	})
	if err != nil || rt == nil {
		return nil, nil, nil, closers, err
	}
	return rt.Processor, rt.Registry, rt.checkReady, closers, nil
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
	cfg := terminalworkapp.Config{
		OwnerID:        owner,
		ClaimTTL:       claimTTL,
		ClaimLimit:     in.ClaimLimit,
		GlobalMax:      in.GlobalMax,
		PerProviderMax: in.PerProvMax,
		TickInterval:   tickInterval,
		RenewInterval:  renewInterval,
	}
	if in.Clock != nil {
		cfg.Clock = clockFunc{now: in.Clock}
	}
	proc, err := terminalworkapp.NewProcessor(in.Store, reg, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("runtimebundle: terminal work processor: %w", err)
	}
	rt := &terminalWorkRuntime{
		Processor: proc,
		Registry:  reg,
		Store:     in.Store,
	}
	if ready, ok := in.Store.(interface {
		CheckReadiness(context.Context) error
	}); ok {
		rt.checkReady = ready.CheckReadiness
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

// Readiness reports running state, unresolved provider IDs, and store readiness.
func (rt *terminalWorkRuntime) Readiness(ctx context.Context) TerminalWorkReadiness {
	out := TerminalWorkReadiness{}
	if rt == nil || rt.Processor == nil {
		return out
	}
	out.Configured = true
	snap := rt.Processor.Readiness()
	out.Running = snap.Running
	out.UnresolvedProviderIDs = append([]string(nil), snap.UnresolvedProviderIDs...)
	if rt.checkReady == nil {
		out.StoreReady = true
		return out
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := rt.checkReady(ctx); err != nil {
		out.StoreReady = false
		out.StoreError = err.Error()
		return out
	}
	out.StoreReady = true
	return out
}

// TerminalWorkReadiness returns terminal-work status when the bundle owns a processor.
func (b *Built) TerminalWorkReadiness(ctx context.Context) TerminalWorkReadiness {
	if b == nil || b.TerminalWorkProcessor == nil {
		return TerminalWorkReadiness{}
	}
	out := TerminalWorkReadiness{
		Configured:            true,
		Running:               b.TerminalWorkProcessor.Running(),
		UnresolvedProviderIDs: b.TerminalWorkProcessor.UnresolvedProviderIDs(),
		StoreReady:            true,
	}
	if b.terminalWorkReady != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := b.terminalWorkReady(ctx); err != nil {
			out.StoreReady = false
			out.StoreError = err.Error()
		}
	}
	return out
}
