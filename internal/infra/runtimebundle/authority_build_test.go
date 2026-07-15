package runtimebundle_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"gopkg.in/yaml.v3"
)

func TestBuildUsageAuthorityDisabledIsNoop(t *testing.T) {
	t.Parallel()
	built, err := runtimebundle.Build(baseAuthorityConfig(false, "fail_closed"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.UsageAuthority != nil {
		t.Fatal("usage authority should be nil when disabled")
	}
	if built.Executor.UsageAuthority != nil {
		t.Fatal("executor usage authority should be nil when disabled")
	}
}

func TestBuildRejectsRequiredAuthorityEvidenceWithoutControlPlane(t *testing.T) {
	t.Parallel()
	cfg := baseAuthorityConfig(true, "fail_closed")
	cfg.ControlPlane.RecordingPolicy = "required_pre_work"
	cfg.ControlPlane.RequiredCategories = []string{"accounting_authority"}
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err == nil || !strings.Contains(err.Error(), "control_plane.enabled") {
		t.Fatalf("Build must reject required authority evidence without control plane, got %v", err)
	}
}

func TestBuildUsageAuthorityWiresService(t *testing.T) {
	t.Parallel()
	built, err := runtimebundle.Build(baseAuthorityConfig(true, "fail_closed"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.UsageAuthority == nil {
		t.Fatal("expected usage authority service when enabled")
	}
	if built.Executor.UsageAuthority == nil {
		t.Fatal("expected executor usage authority handle when enabled")
	}
	status, err := built.UsageAuthority.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != controlplane.AccountingAuthorityReady {
		t.Fatalf("status.state = %q, want ready", status.State)
	}
}

func TestBuildUsageAuthorityStrictUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	override := &stubAuthorityStore{
		readiness:    authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateUnavailable, Reason: authoritydomain.StatusReasonBackingUnavailable},
		readinessErr: errors.New("backend not available"),
	}
	_, err := runtimebundle.Build(baseAuthorityConfig(true, "fail_closed"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, override))
	if err == nil {
		t.Fatal("expected strict authority startup to fail closed")
	}
	if strings.Contains(err.Error(), "backend not available") {
		t.Fatalf("startup error leaked raw infrastructure detail: %v", err)
	}
}

func TestBuildUsageAuthorityAdmitReserveUsesSeededLimitRows(t *testing.T) {
	t.Parallel()
	built, err := runtimebundle.Build(baseAuthorityConfig(true, "fail_closed"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, nil))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.UsageAuthority == nil {
		t.Fatal("expected usage authority service when enabled")
	}

	got, err := built.UsageAuthority.Admit(context.Background(), authorityAdmissionInput())
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !got.Allowed {
		t.Fatalf("Admit must allow: %#v", got)
	}
	if !got.Reserved || got.ReservationID == "" {
		t.Fatalf("Admit must reserve against seeded limit row: %#v", got)
	}
}

func TestBuildUsageAuthorityFailOpenStartsAdvisory(t *testing.T) {
	t.Parallel()
	override := &stubAuthorityStore{
		readiness:    authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateUnavailable, Reason: authoritydomain.StatusReasonBackingUnavailable},
		readinessErr: errors.New("backend not available"),
	}
	built, err := runtimebundle.Build(baseAuthorityConfig(true, "fail_open"), hooks.New(hooks.Config{}), testkit.DiscardLogger(), baseAuthorityOptions(t, override))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.UsageAuthority == nil {
		t.Fatal("expected usage authority service when enabled")
	}
	status, err := built.UsageAuthority.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != controlplane.AccountingAuthorityAdvisoryOnly {
		t.Fatalf("status.state = %q, want advisory_only", status.State)
	}
}

func TestBuildUsageAuthorityFailClosedPostgresLogsCauseWithoutLeakingDSN(t *testing.T) {
	t.Parallel()
	const secret = "NEVER_EMBED_THIS_AUTHORITY_SECRET"
	cfg := baseAuthorityConfig(true, "fail_closed")
	cfg.Accounting.Authority.Store = "postgres"
	cfg.Accounting.Authority.PostgresDSN = "postgres://u:" + secret + "@127.0.0.1:1/nosuch?sslmode=disable"

	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	_, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), log, baseAuthorityOptions(t, nil))
	if err == nil {
		t.Fatal("expected fail-closed postgres open to fail Build")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("returned error leaked DSN secret: %s", msg)
	}
	if !strings.Contains(msg, "usage authority durable postgres") {
		t.Fatalf("want opaque usage-authority postgres error, got: %s", msg)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, `"notice":"store_unavailable"`) {
		t.Fatalf("want Error log with store_unavailable notice, got: %s", logs)
	}
	if !strings.Contains(logs, `"store":"postgres"`) || !strings.Contains(logs, `"phase":"init"`) {
		t.Fatalf("want store=postgres phase=init attrs, got: %s", logs)
	}
	if !strings.Contains(logs, `"error":`) {
		t.Fatalf("want underlying error attr in diagnostics log, got: %s", logs)
	}
}

func authorityAdmissionInput() authorityapp.AdmissionInput {
	return authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			SessionID:  "session-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
		},
		Dimensions: authoritydomain.Dimensions{
			Principal: scope.Known("principal-1"),
			Tenant:    scope.Known("tenant-1"),
			Backend:   scope.Known("backend-1"),
			Model:     scope.Known("model-1"),
		},
		Request:      authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 1},
		RequestCount: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 1},
		Spend:        authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 100, Currency: "usd"},
		Authority:    authoritydomain.AuthorityLevelAuthoritative,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           "tenant.requests",
			Sequence:         1,
		},
	}
}

func baseAuthorityConfig(enabled bool, startupPosture string) *config.Config {
	return &config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:     config.RoutingConfig{DefaultRoute: "stub:model", MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Database:    config.DatabaseConfig{MaxOpenConns: 8},
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("s", 12)},
		Plugins:     config.PluginsConfig{},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        enabled,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: startupPosture,
				Query:          config.AccountingAuthorityQueryConfig{Enabled: false},
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Match: config.AccountingAuthorityDimensionsConfig{
							Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
						},
					},
				},
			},
		},
	}
}

func baseAuthorityOptions(t *testing.T, override authorityapp.StateStore) *runtimebundle.BuildOptions {
	t.Helper()
	reg := pluginreg.NewRegistry()
	registerAuthorityBackend(t, reg, "stub")
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	if override != nil {
		opts.Testing.AuthorityStoreOverride = override
	}
	return opts
}

func registerAuthorityBackend(t *testing.T, reg *pluginreg.Registry, factoryID string) {
	t.Helper()
	if err := reg.RegisterBackend(factoryID, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

type stubAuthorityStore struct {
	readiness    authoritydomain.AuthorityStatus
	readinessErr error
}

func (s *stubAuthorityStore) Reserve(context.Context, authorityapp.ReserveCommand) (authorityapp.ReserveResult, error) {
	return authorityapp.ReserveResult{}, nil
}

func (s *stubAuthorityStore) Settle(context.Context, authorityapp.SettleCommand) (authorityapp.SettleResult, error) {
	return authorityapp.SettleResult{}, nil
}

func (s *stubAuthorityStore) Release(context.Context, authorityapp.ReleaseCommand) (authorityapp.ReleaseResult, error) {
	return authorityapp.ReleaseResult{}, nil
}

func (s *stubAuthorityStore) ApplyUsage(context.Context, authorityapp.ApplyUsageCommand) (authorityapp.ApplyUsageResult, error) {
	return authorityapp.ApplyUsageResult{}, nil
}

func (s *stubAuthorityStore) ActiveLimit(context.Context, authorityapp.ActiveLimitQuery) (controlplane.AccountingLimitStatusRow, bool, error) {
	return controlplane.AccountingLimitStatusRow{}, false, nil
}

func (s *stubAuthorityStore) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, nil
}

func (s *stubAuthorityStore) DecisionHistory(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return controlplane.Page[controlplane.AccountingDecisionRow]{}, nil
}

func (s *stubAuthorityStore) CheckReadiness(context.Context) (authoritydomain.AuthorityStatus, error) {
	return s.readiness, s.readinessErr
}
