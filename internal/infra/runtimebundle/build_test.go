package runtimebundle_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	lipstate "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/state"
	dto "github.com/prometheus/client_model/go"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestBuildExecutor_productionClockAndRNG(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ex := b.Executor
	if b.UpstreamHTTP == nil {
		t.Fatal("expected shared upstream HTTP client")
	}
	if b.PluginRegistry == nil {
		t.Fatal("expected PluginRegistry")
	}
	if ex.Now == nil {
		t.Fatal("expected non-nil Now")
	}
	if ex.Rand == nil {
		t.Fatal("expected non-nil Rand")
	}
	if ex.CandidateHealth == nil {
		t.Fatal("expected CandidateHealth wired")
	}
	if ex.RouteObserver == nil {
		t.Fatal("expected RouteObserver wired")
	}
	if b.RuntimeSnapshot == nil {
		t.Fatal("expected RuntimeSnapshot on Built")
	}
	if ex.RuntimeSnapshot != b.RuntimeSnapshot {
		t.Fatal("executor snapshot should match Built.RuntimeSnapshot")
	}
	ctx := context.Background()
	if err := b.RuntimeSnapshot.State().Put(ctx, lipstate.ScopeGlobal, "rtbundle", "probe", "1", 0); err != nil {
		t.Fatalf("runtime snapshot state: %v", err)
	}
	var out string
	found, err := b.RuntimeSnapshot.State().Get(ctx, lipstate.ScopeGlobal, "rtbundle", "probe", &out)
	if err != nil || !found || out != "1" {
		t.Fatalf("state get found=%v out=%q err=%v", found, out, err)
	}
}

func TestBuild_respectsHTTPClientInBuildOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	custom := &http.Client{}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		HTTPClient:     custom,
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.UpstreamHTTP != custom {
		t.Fatalf("UpstreamHTTP: got %p want %p", b.UpstreamHTTP, custom)
	}
}

func TestBuild_wrapsCustomHTTPClientForUpstreamMetrics(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	custom := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    req,
		}, nil
	})}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		HTTPClient:     custom,
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.UpstreamHTTP == custom {
		t.Fatal("expected wrapped client clone when metrics are enabled")
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.UpstreamHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	families, err := b.Metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	assertHistogramCount(t, families, "lip_upstream_request_duration_seconds", map[string]string{
		"host_bucket": "openai",
		"method":      http.MethodGet,
	}, 1)
}

func TestBuild_setsEffectiveDefaultRoute_defaultWireModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.EffectiveDefaultRoute == "" {
		t.Fatal("EffectiveDefaultRoute should be non-empty")
	}
	wantRaw := config.EffectiveDefaultRouteSelector(cfg, pluginreg.DefaultWireModel)
	ar, err := routing.NewAliasResolver(routing.ModelAliasRulesFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	want := ar.Resolve(wantRaw)
	if b.EffectiveDefaultRoute != want {
		t.Fatalf("EffectiveDefaultRoute: got %q want %q", b.EffectiveDefaultRoute, want)
	}
}

func TestBuild_respectsWireModelInBuildOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		WireModel:      func(string) string { return "wm-override" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "openai-responses:wm-override"; b.EffectiveDefaultRoute != want {
		t.Fatalf("EffectiveDefaultRoute: got %q want %q", b.EffectiveDefaultRoute, want)
	}
}

func TestBuild_defaultRouteAliasExpandsBeforeDefaultBackend(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "friendly-name",
		},
		ModelAliases: []config.ModelAliasConfig{
			{Pattern: `^friendly-name$`, Replacement: "openai-responses:gpt-4o-mini"},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	b, err := runtimebundle.Build(cfg, hooks.New(hooks.Config{}), testkit.DiscardLogger(), &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.EffectiveDefaultRoute != "openai-responses:gpt-4o-mini" {
		t.Fatalf("EffectiveDefaultRoute: got %q", b.EffectiveDefaultRoute)
	}
	if b.Executor.DefaultBackend != "openai-responses" {
		t.Fatalf("DefaultBackend: got %q want openai-responses", b.Executor.DefaultBackend)
	}
}

func assertHistogramCount(t *testing.T, families []*dto.MetricFamily, name string, wantLabels map[string]string, wantCount uint64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !hasLabels(metric.GetLabel(), wantLabels) {
				continue
			}
			if metric.GetHistogram().GetSampleCount() != wantCount {
				t.Fatalf("%s sample count = %d want %d", name, metric.GetHistogram().GetSampleCount(), wantCount)
			}
			return
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, wantLabels)
}

func hasLabels(labels []*dto.LabelPair, want map[string]string) bool {
	if len(labels) != len(want) {
		return false
	}
	for _, label := range labels {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}
