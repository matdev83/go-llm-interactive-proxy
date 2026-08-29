package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	corecp "github.com/matdev83/go-llm-interactive-proxy/internal/core/controlplane"
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
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	lipworkspace "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestReadinessReport_secretGuardQuarantineFaultWithoutSecretMaterial(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fake := &testkit.FakeSecureSessionStore{
		Delegate:      memSS,
		QuarantineErr: errors.New("disk full"),
	}
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(fake, app.NewRandGenerator(key), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]lipworkspace.Resolver{voidWS2{}}),
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&blockingSecretGuard{}},
		}),
	})
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = lipapidenial.MapToSessionDenial
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1902, 0) }
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return nil, errors.New("unreachable")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(3)

	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: "user-rd"})
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	_, _ = ex.Execute(ctx, call)

	svc := corecp.NewReadinessReportService(corecp.ReadinessReportSources{
		SecretGuardQuarantine: func(context.Context) (controlplane.ReadinessComponentStatus, error) {
			state := controlplane.CapabilityReady
			reason := controlplane.ReasonNone
			if ex.QuarantinePersistenceFaulted() {
				state = controlplane.CapabilityUnavailable
				reason = controlplane.ReasonBackingUnavailable
			}
			return controlplane.ReadinessComponentStatus{
				Component:        controlplane.ReadinessComponentSecretGuardQuarantine,
				State:            state,
				Reason:           reason,
				EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess,
			}, nil
		},
	})
	report, err := svc.Report(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var row *controlplane.ReadinessComponentStatus
	for i := range report.Components {
		if report.Components[i].Component == controlplane.ReadinessComponentSecretGuardQuarantine {
			row = &report.Components[i]
			break
		}
	}
	if row == nil {
		t.Fatal("missing secret_guard_quarantine component")
	}
	if row.State != controlplane.CapabilityUnavailable {
		t.Fatalf("state=%q want unavailable", row.State)
	}
	if row.Reason != controlplane.ReasonBackingUnavailable {
		t.Fatalf("reason=%q", row.Reason)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, secret := range testkit.AllSyntheticSecretGuardValues() {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatal("readiness report must not contain synthetic secret material")
		}
	}
	for _, needle := range testkit.AllSyntheticSecretGuardNeedles() {
		if needle != "" && strings.Contains(body, needle) {
			t.Fatal("readiness report must not contain synthetic secret substrings")
		}
	}
	if strings.Contains(body, "disk full") {
		t.Fatal("readiness report must not expose internal quarantine error detail")
	}
}
