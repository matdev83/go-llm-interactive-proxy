package runtimebundle

import (
	"context"
	"strings"

	concurrencyapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	corecp "github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
)

type readinessReportBuildInput struct {
	Cfg                *config.Config
	ControlPlaneStatus *corecp.Status
	UsageAuthority     *authorityapp.Service
	Concurrency        *concurrencyapp.Service
	Metering           *meteringRuntime
	SnapshotGeneration *snapshotgen.Publisher
	Executor           *runtime.Executor
	Production         ProductionOptions
	TerminalWork       *terminalWorkRuntime
}

func buildReadinessReportService(in readinessReportBuildInput) *corecp.ReadinessReportService {
	src := corecp.ReadinessReportSources{
		StoreBackings: readinessStoreBackings(in.Cfg, in.Production),
	}
	if in.ControlPlaneStatus != nil {
		status := in.ControlPlaneStatus
		src.ControlPlane = func(ctx context.Context) (controlplane.CapabilityStatus, error) {
			_ = ctx
			return status.Snapshot(), nil
		}
	}
	if in.UsageAuthority != nil {
		svc := in.UsageAuthority
		src.UsageAuthority = svc.Status
	}
	if in.Concurrency != nil {
		svc := in.Concurrency
		src.ConcurrencyAuthority = func(ctx context.Context) (controlplane.ConcurrencyAuthorityStatus, error) {
			ready, err := svc.ReadinessDomain(ctx)
			if err != nil {
				return controlplane.ConcurrencyAuthorityStatus{}, err
			}
			st := controlplane.ConcurrencyAuthorityReady
			switch string(ready.State) {
			case "degraded":
				st = controlplane.ConcurrencyAuthorityDegraded
			case "unavailable":
				st = controlplane.ConcurrencyAuthorityUnavailable
			case "disabled":
				st = controlplane.ConcurrencyAuthorityDisabled
			}
			return controlplane.ConcurrencyAuthorityStatus{State: st, Reason: ready.Reason}, nil
		}
	}
	if in.Metering != nil {
		m := in.Metering
		src.MeteringJournal = func(ctx context.Context) (controlplane.ReadinessComponentStatus, error) {
			state := controlplane.CapabilityReady
			if m.checkReady != nil {
				if err := m.checkReady(ctx); err != nil {
					state = controlplane.CapabilityUnavailable
				}
			}
			return controlplane.ReadinessComponentStatus{
				Component:        controlplane.ReadinessComponentMeteringJournal,
				State:            state,
				EnforcementScope: controlplane.EnforcementScopeForStoreBacking(m.StoreBacking, strings.EqualFold(strings.TrimSpace(m.StoreBacking), "postgres")),
				StoreBacking:     m.StoreBacking,
			}, nil
		}
	}
	if in.SnapshotGeneration != nil {
		gen := in.SnapshotGeneration
		src.SnapshotStates = func() (controlplane.CapabilityState, controlplane.CapabilityState, controlplane.CapabilityState) {
			cur := gen.Current()
			if cur == nil {
				return controlplane.CapabilityDisabled, controlplane.CapabilityDisabled, controlplane.CapabilityDisabled
			}
			mapState := func(s economics.SnapshotState) controlplane.CapabilityState {
				switch s {
				case economics.SnapshotReady, economics.SnapshotStale:
					return controlplane.CapabilityReady
				case economics.SnapshotDegraded:
					return controlplane.CapabilityDegraded
				case economics.SnapshotUnavailable:
					return controlplane.CapabilityUnavailable
				case economics.SnapshotDisabled:
					return controlplane.CapabilityDisabled
				default:
					return controlplane.CapabilityUnavailable
				}
			}
			return mapState(cur.Usage.State), mapState(cur.Concurrency.State), mapState(cur.Rating.State)
		}
	}
	src.OperatorRaterIDs = raterIDsByPerspective(in.Production, metering.PerspectiveOperator)
	src.CustomerRaterIDs = raterIDsByPerspective(in.Production, metering.PerspectiveCustomer)
	if in.Executor != nil {
		exec := in.Executor
		src.RequestCoordinatorEnabled = exec.RequestCoordinator != nil && len(exec.RequestCoordinator.Slots) > 0
		src.AttemptCoordinatorEnabled = exec.AttemptCoordinator != nil && len(exec.AttemptCoordinator.Slots) > 0
		src.RequestCoordinatorIDs = requestRegistrationIDs(in.Production, &exec.AccountingRuntime)
		src.AttemptCoordinatorIDs = attemptRegistrationIDs(in.Production, &exec.AccountingRuntime)
		src.OperatorRaterAttached = len(src.OperatorRaterIDs) > 0
		if exec.SecureSession != nil && exec.RuntimeSnapshot != nil && len(exec.RuntimeSnapshot.SecretGuardPlane().Guards) > 0 {
			backing := "injected"
			if in.Cfg != nil {
				backing = strings.ToLower(strings.TrimSpace(in.Cfg.SecureSession.Store))
				if in.Cfg.SecureSessionEffectivelyEnabled() {
					if backing == "" {
						backing = "memory"
					}
				} else if backing == "" {
					backing = "injected"
				}
			}
			src.SecretGuardQuarantine = func(ctx context.Context) (controlplane.ReadinessComponentStatus, error) {
				_ = ctx
				state := controlplane.CapabilityReady
				reason := controlplane.ReasonNone
				if exec.QuarantinePersistenceFaulted() {
					state = controlplane.CapabilityUnavailable
					reason = controlplane.ReasonBackingUnavailable
				}
				return controlplane.ReadinessComponentStatus{
					Component:        controlplane.ReadinessComponentSecretGuardQuarantine,
					State:            state,
					Reason:           reason,
					EnforcementScope: controlplane.EnforcementScopeForStoreBacking(backing, strings.EqualFold(strings.TrimSpace(backing), "postgres")),
					StoreBacking:     backing,
				}, nil
			}
		}
	}
	src.CustomerRaterAttached = len(src.CustomerRaterIDs) > 0
	if in.TerminalWork != nil {
		tw := in.TerminalWork
		src.TerminalRecovery = tw.readinessComponent
	}
	return corecp.NewReadinessReportService(src)
}

func readinessStoreBackings(cfg *config.Config, prod ProductionOptions) corecp.ReadinessStoreBackings {
	out := corecp.ReadinessStoreBackings{}
	if cfg == nil {
		return out
	}
	out.ControlPlane = strings.ToLower(strings.TrimSpace(cfg.ControlPlane.Store))
	if out.ControlPlane == "" && cfg.ControlPlane.Enabled {
		out.ControlPlane = "memory"
	}
	if cfg.Metering.Enabled {
		out.Metering = strings.ToLower(strings.TrimSpace(cfg.Metering.Journal.Store))
		if out.Metering == "" {
			out.Metering = "memory"
		}
	}
	if prod.MeteringRecorder != nil {
		out.Metering = "injected"
	}
	if cfg.Accounting.Authority.Enabled {
		out.Usage = strings.ToLower(strings.TrimSpace(cfg.Accounting.Authority.Store))
		if out.Usage == "" {
			out.Usage = "memory"
		}
	}
	if cfg.Accounting.Concurrency.Enabled {
		out.Concurrency = strings.ToLower(strings.TrimSpace(cfg.Accounting.Concurrency.Store))
		if out.Concurrency == "" {
			out.Concurrency = "memory"
		}
	}
	return out
}
