package runtimebundle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/b2bualineage"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/lipapidenial"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/adapters/memory"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/securesession/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/workspace"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/billingspool"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestBuildReadinessReportService_secretGuardQuarantineDisabledWithoutSecureSession(t *testing.T) {
	t.Parallel()
	ex := newSecretGuardReadinessExecutor(t, false, nil)
	svc := buildReadinessReportService(readinessReportBuildInput{Executor: ex})

	report, err := svc.Report(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	row := mustReadinessRow(t, report, controlplane.ReadinessComponentSecretGuardQuarantine)
	if row.State != controlplane.CapabilityDisabled {
		t.Fatalf("state=%q want disabled", row.State)
	}
}

func TestBuildReadinessReportService_billingSpoolReadiness(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		sink       billing.TerminalUsageSink
		wantState  controlplane.CapabilityState
		wantReason controlplane.ReasonCode
	}{
		{
			name:       "no sink is disabled",
			wantState:  controlplane.CapabilityDisabled,
			wantReason: controlplane.ReasonDisabled,
		},
		{
			name:       "non health sink is disabled",
			sink:       readinessPlainSink{},
			wantState:  controlplane.CapabilityDisabled,
			wantReason: controlplane.ReasonDisabled,
		},
		{
			name:       "ready",
			sink:       &readinessBillingSpoolSink{health: billingspool.Health{State: billingspool.HealthReady}},
			wantState:  controlplane.CapabilityReady,
			wantReason: controlplane.ReasonNone,
		},
		{
			name:       "degraded",
			sink:       &readinessBillingSpoolSink{health: billingspool.Health{State: billingspool.HealthDegraded}},
			wantState:  controlplane.CapabilityDegraded,
			wantReason: controlplane.ReasonStoreNotReady,
		},
		{
			name:       "full",
			sink:       &readinessBillingSpoolSink{health: billingspool.Health{State: billingspool.HealthFull}},
			wantState:  controlplane.CapabilityUnavailable,
			wantReason: controlplane.ReasonStoreNotReady,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := buildReadinessReportService(readinessReportBuildInput{
				Production: ProductionOptions{BillingTerminalUsageSink: tc.sink},
			})
			report, err := svc.Report(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			row := mustReadinessRow(t, report, controlplane.ReadinessComponentBillingSpool)
			if row.State != tc.wantState {
				t.Fatalf("state=%q want %q", row.State, tc.wantState)
			}
			if row.Reason != tc.wantReason {
				t.Fatalf("reason=%q want %q", row.Reason, tc.wantReason)
			}
			if tc.sink != nil && row.State != controlplane.CapabilityDisabled {
				if row.EnforcementScope != controlplane.EnforcementScopeAdvisorySingleProcess {
					t.Fatalf("scope=%q want advisory_single_process", row.EnforcementScope)
				}
				if row.StoreBacking != "injected" {
					t.Fatalf("store_backing=%q want injected", row.StoreBacking)
				}
			}
		})
	}
}

type readinessBillingSpoolSink struct {
	health billingspool.Health
}

type readinessPlainSink struct{}

func (readinessPlainSink) AppendCall(context.Context, billing.CallUsageRecord) error {
	return nil
}

func (readinessPlainSink) AppendLeg(context.Context, billing.CallLegUsageRecord) error {
	return nil
}

func (s *readinessBillingSpoolSink) AppendCall(context.Context, billing.CallUsageRecord) error {
	return nil
}

func (s *readinessBillingSpoolSink) AppendLeg(context.Context, billing.CallLegUsageRecord) error {
	return nil
}

func (s *readinessBillingSpoolSink) Health() billingspool.Health { return s.health }

func TestBuildReadinessReportService_secretGuardQuarantineReadyWhenSecureSessionPresent(t *testing.T) {
	t.Parallel()
	ex := newSecretGuardReadinessExecutor(t, true, nil)
	svc := buildReadinessReportService(readinessReportBuildInput{Executor: ex})

	report, err := svc.Report(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	row := mustReadinessRow(t, report, controlplane.ReadinessComponentSecretGuardQuarantine)
	if row.State != controlplane.CapabilityReady {
		t.Fatalf("state=%q want ready", row.State)
	}
	if row.Reason != controlplane.ReasonNone {
		t.Fatalf("reason=%q want none", row.Reason)
	}
	if row.StoreBacking != "injected" {
		t.Fatalf("store_backing=%q want injected when cfg is absent", row.StoreBacking)
	}
	if row.EnforcementScope != controlplane.EnforcementScopeAdvisorySingleProcess {
		t.Fatalf("scope=%q want advisory", row.EnforcementScope)
	}
}

func TestBuildReadinessReportService_secretGuardQuarantineUnavailableAfterPersistenceFault(t *testing.T) {
	t.Parallel()
	ex := newSecretGuardReadinessExecutor(t, true, errors.New("disk full"))
	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-fault"})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("trigger")},
		}},
	}
	if _, err := ex.Execute(ctx, call); err == nil {
		t.Fatal("expected secret-guard block denial")
	}
	svc := buildReadinessReportService(readinessReportBuildInput{Executor: ex})

	report, err := svc.Report(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	row := mustReadinessRow(t, report, controlplane.ReadinessComponentSecretGuardQuarantine)
	if row.State != controlplane.CapabilityUnavailable {
		t.Fatalf("state=%q want unavailable", row.State)
	}
	if row.Reason != controlplane.ReasonBackingUnavailable {
		t.Fatalf("reason=%q want backing_unavailable", row.Reason)
	}
}

func TestBuildReadinessReportService_secretGuardQuarantineStoreBackings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		store       string
		fault       error
		wantState   controlplane.CapabilityState
		wantReason  controlplane.ReasonCode
		wantScope   controlplane.EnforcementScope
		wantBacking string
	}{
		{
			name:        "default memory",
			store:       "",
			wantState:   controlplane.CapabilityReady,
			wantReason:  controlplane.ReasonNone,
			wantScope:   controlplane.EnforcementScopeAdvisorySingleProcess,
			wantBacking: "memory",
		},
		{
			name:        "sqlite",
			store:       "sqlite",
			wantState:   controlplane.CapabilityReady,
			wantReason:  controlplane.ReasonNone,
			wantScope:   controlplane.EnforcementScopeAdvisorySingleProcess,
			wantBacking: "sqlite",
		},
		{
			name:        "postgres",
			store:       "postgres",
			wantState:   controlplane.CapabilityReady,
			wantReason:  controlplane.ReasonNone,
			wantScope:   controlplane.EnforcementScopeDistributedStrict,
			wantBacking: "postgres",
		},
		{
			name:        "postgres faulted",
			store:       "postgres",
			fault:       errors.New("disk full"),
			wantState:   controlplane.CapabilityUnavailable,
			wantReason:  controlplane.ReasonBackingUnavailable,
			wantScope:   controlplane.EnforcementScopeDistributedStrict,
			wantBacking: "postgres",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex := newSecretGuardReadinessExecutor(t, true, tc.fault)
			if tc.fault != nil {
				ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-fault"})
				call := &lipapi.Call{
					Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
					Messages: []lipapi.Message{{
						Role:  lipapi.RoleUser,
						Parts: []lipapi.Part{lipapi.TextPart("trigger")},
					}},
				}
				if _, err := ex.Execute(ctx, call); err == nil {
					t.Fatal("expected secret-guard block denial")
				}
			}
			cfg := &config.Config{
				SecureSession: config.SecureSessionConfig{
					Enabled: new(true),
					Store:   tc.store,
				},
			}
			svc := buildReadinessReportService(readinessReportBuildInput{
				Cfg:      cfg,
				Executor: ex,
			})
			report, err := svc.Report(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			row := mustReadinessRow(t, report, controlplane.ReadinessComponentSecretGuardQuarantine)
			if row.Component != controlplane.ReadinessComponentSecretGuardQuarantine {
				t.Fatalf("component=%q", row.Component)
			}
			if row.State != tc.wantState {
				t.Fatalf("state=%q want %q", row.State, tc.wantState)
			}
			if row.Reason != tc.wantReason {
				t.Fatalf("reason=%q want %q", row.Reason, tc.wantReason)
			}
			if row.StoreBacking != tc.wantBacking {
				t.Fatalf("store_backing=%q want %q", row.StoreBacking, tc.wantBacking)
			}
			if row.EnforcementScope != tc.wantScope {
				t.Fatalf("scope=%q want %q", row.EnforcementScope, tc.wantScope)
			}
		})
	}
}

func mustReadinessRow(t *testing.T, report controlplane.ReadinessReport, id controlplane.ReadinessComponentID) controlplane.ReadinessComponentStatus {
	t.Helper()
	for _, row := range report.Components {
		if row.Component == id {
			return row
		}
	}
	t.Fatalf("missing readiness component %q", id)
	return controlplane.ReadinessComponentStatus{}
}

func newSecretGuardReadinessExecutor(t *testing.T, withSecureSession bool, quarantineErr error) *runtime.Executor {
	t.Helper()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var mgr *app.Manager
	if withSecureSession {
		mgr = newReadinessSecureSessionManager(t, b2, quarantineErr)
	}
	bus := hooks.New(hooks.Config{})
	cset := lipfeature.NewContributionSet()
	_ = lipfeature.Contribute(cset, lipfeature.PlaneSecretGuards, "test", []secretguard.Guard{alwaysBlockSecretGuard{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{noopWorkspaceResolver{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: cset.Freeze(),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1902, 0).UTC() }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, nil
			},
		},
	}
	return ex
}

func newReadinessSecureSessionManager(t *testing.T, lineageStore b2bua.Store, quarantineErr error) *app.Manager {
	t.Helper()
	memSS := memory.New(memory.Options{SimulateDurable: true})
	store := app.Store(memSS)
	if quarantineErr != nil {
		store = &testkit.FakeSecureSessionStore{Delegate: memSS, QuarantineErr: quarantineErr}
	}
	key := readinessFingerprintKey()
	mgr, err := app.NewManager(store, app.NewRandGenerator(key), b2bualineage.New(lineageStore), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func readinessFingerprintKey() []byte {
	return []byte{
		0x11, 0x12, 0x13, 0x14,
		0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c,
		0x1d, 0x1e, 0x1f, 0x20,
		0x21, 0x22, 0x23, 0x24,
		0x25, 0x26, 0x27, 0x28,
		0x29, 0x2a, 0x2b, 0x2c,
		0x2d, 0x2e, 0x2f, 0x30,
	}
}

type noopWorkspaceResolver struct{}

func (noopWorkspaceResolver) Resolve(context.Context) (lipworkspace.WorkspaceView, error) {
	return lipworkspace.WorkspaceView{}, nil
}

type alwaysBlockSecretGuard struct{}

func (alwaysBlockSecretGuard) ID() string { return "block-all" }
func (alwaysBlockSecretGuard) Order() int { return 0 }
func (alwaysBlockSecretGuard) FailureMode() secretguard.FailureMode {
	return secretguard.FailClosed
}

func (alwaysBlockSecretGuard) Evaluate(context.Context, *lipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{
		Outcome: secretguard.OutcomeBlock,
		Findings: []secretguard.Finding{{
			SecretRefName:   "OPENAI_API_KEY",
			SourceCategory:  secretguard.SourceCategoryProxyEnv,
			Location:        "messages[0].parts[0].text",
			OccurrenceCount: 1,
		}},
	}, nil
}
