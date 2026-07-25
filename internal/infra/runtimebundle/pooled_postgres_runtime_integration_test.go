//go:build integration

package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/metering/journalstore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestPostgresPooled_BuildRuntimeSharesRegistryAndClosesCleanly(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	b := buildPooledPostgresRuntime(t, adminDSN, runtimeDSN)

	if got := gatheredGauge(t, b.Metrics().Registry, "lip_postgres_pool_open_connections"); got != 1 {
		t.Fatalf("open postgres runtime connections = %v want 1", got)
	}

	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		closeBundleClosers(t, b)
	})

	closeBundleClosers(t, b)
	closed = true

	if got := gatheredGauge(t, b.Metrics().Registry, "lip_postgres_pool_open_connections"); got != 0 {
		t.Fatalf("open postgres runtime connections after close = %v want 0", got)
	}
}

func TestPostgresPooled_BuildRuntimeLeaseAndJournalSmoke(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	b := buildPooledPostgresRuntime(t, adminDSN, runtimeDSN)

	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		closeBundleClosers(t, b)
	})

	if b.Executor() == nil || b.Executor().ConcurrencyProvider == nil {
		t.Fatal("expected concurrency provider on built executor")
	}
	if b.Executor().MeteringRecorder == nil {
		t.Fatal("expected metering recorder on built executor")
	}
	if b.ReadinessReport() == nil {
		t.Fatal("expected readiness report on CandidateRuntime")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	dec, err := b.Executor().ConcurrencyProvider.AdmitLease(ctx, authority.LeaseAdmission{
		RequestID: "rtbundle-pooled-req",
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
	})
	if err != nil {
		t.Fatalf("AdmitLease: %v", err)
	}
	if dec.Kind != authority.LeaseAllow || dec.LeaseID == "" {
		t.Fatalf("AdmitLease dec=%+v want allow with lease id", dec)
	}
	if err := b.Executor().ConcurrencyProvider.ReleaseLease(ctx, authority.LeaseRelease{
		LeaseID:   dec.LeaseID,
		RequestID: "rtbundle-pooled-req",
	}); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}

	fact := metering.Fact{
		FactID:      "rtbundle-pooled-fact",
		StreamID:    "rtbundle-pooled-stream",
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendEgress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "req-1", ALegID: "a-1"},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("prin-1"),
			TenantID:    scope.Known("ten-1"),
		},
		Source:     metering.SourceObserved,
		Authority:  metering.AuthorityAuthoritative,
		Presence:   metering.PresencePresent,
		RecordedAt: time.Unix(10, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     1,
			Present:   true,
		}},
	}
	if err := b.Executor().MeteringRecorder.Append(ctx, fact); err != nil {
		t.Fatalf("MeteringRecorder.Append: %v", err)
	}

	report, err := b.ReadinessReport().Report(ctx)
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	if len(report.Components) == 0 {
		t.Fatal("expected readiness components")
	}
	var sawConcurrency, sawJournal bool
	for _, c := range report.Components {
		if c.Component == controlplane.ReadinessComponentConcurrencyAuthority {
			sawConcurrency = true
			if c.State == "" {
				t.Fatal("concurrency readiness state empty")
			}
		}
		if c.Component == controlplane.ReadinessComponentMeteringJournal {
			sawJournal = true
			if c.State == "" {
				t.Fatal("metering journal readiness state empty")
			}
		}
	}
	if !sawConcurrency || !sawJournal {
		t.Fatalf("readiness missing components concurrency=%v journal=%v", sawConcurrency, sawJournal)
	}

	if got := gatheredGauge(t, b.Metrics().Registry, "lip_postgres_pool_open_connections"); got != 1 {
		t.Fatalf("open postgres runtime connections = %v want 1", got)
	}
	closeBundleClosers(t, b)
	closed = true
	if got := gatheredGauge(t, b.Metrics().Registry, "lip_postgres_pool_open_connections"); got != 0 {
		t.Fatalf("open postgres runtime connections after close = %v want 0", got)
	}
}

func buildPooledPostgresRuntime(t *testing.T, adminDSN, runtimeDSN string) *runtimebundle.CandidateHTTPCompile {
	t.Helper()
	concurrencyStoreID := testkit.UniquePostgresStoreID("rtbundle-concurrency")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, concurrencyStoreID, testkit.PostgresComponentLease)
		testkit.CleanupPostgresStoreByID(t, adminDSN, "metering-postgres", testkit.PostgresComponentJournal)
	})

	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() { _ = admin.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := leasestore.Migrate(ctx, admin); err != nil {
		t.Fatalf("migrate concurrency schema: %v", err)
	}
	if err := journalstore.Migrate(ctx, admin); err != nil {
		t.Fatalf("migrate metering schema: %v", err)
	}

	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		Database: config.DatabaseConfig{
			ConnectionMode: config.DatabaseConnectionModeTransactionPool,
			SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
			MaxOpenConns:   1,
			MaxIdleConns:   1,
		},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Accounting: config.AccountingConfig{
			Concurrency: config.ConcurrencyAuthorityConfig{
				Enabled:     true,
				Store:       "postgres",
				StoreID:     concurrencyStoreID,
				PostgresDSN: runtimeDSN,
				Rules: []config.ConcurrencyAuthorityRuleConfig{{
					ID:                "rtbundle-concurrency",
					MaxActiveRequests: 1,
				}},
			},
		},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{
				Store:       "postgres",
				PostgresDSN: runtimeDSN,
			},
		},
	}

	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Startup:        runtimebundle.StartupOptions{StartupContext: t.Context()},
	})
	if b.Metrics() == nil || b.Metrics().Registry == nil || b.Metrics().PostgresPool == nil {
		t.Fatal("expected postgres pool metrics bundle")
	}
	return b
}

func closeBundleClosers(t *testing.T, b *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if b == nil {
		return
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func gatheredGauge(t *testing.T, reg *prometheus.Registry, metricName string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather %s: %v", metricName, err)
	}
	for _, family := range families {
		if family.GetName() != metricName {
			continue
		}
		metrics := family.GetMetric()
		if len(metrics) != 1 {
			t.Fatalf("%s metric count = %d want 1", metricName, len(metrics))
		}
		return gaugeValue(metrics[0])
	}
	t.Fatalf("metric %s not found", metricName)
	return 0
}

func gaugeValue(metric *dto.Metric) float64 {
	if metric == nil || metric.GetGauge() == nil {
		return 0
	}
	return metric.GetGauge().GetValue()
}
