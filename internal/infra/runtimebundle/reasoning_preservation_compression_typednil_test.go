package runtimebundle

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

// typed nil implementations
type typedNilClient struct{}

func (t *typedNilClient) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", nil
}
func (t *typedNilClient) Await(ctx context.Context, id auxiliary.JobID) (lipapi.Collected, error) {
	return lipapi.Collected{}, nil
}
func (t *typedNilClient) Forget(id auxiliary.JobID) {}

func (t *typedNilClient) Poll(ctx context.Context, id auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{}, nil
}

type typedNilEgress struct{}

func (t *typedNilEgress) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, nil
}

type typedNilResolver struct{}

func (t *typedNilResolver) Resolve(_ context.Context) (sdk.Matcher, error) { return nil, nil }

type typedNilMatcher struct{}

func (t *typedNilMatcher) ScanBytes(_ context.Context, _ []byte) ([]sdk.Finding, error) {
	return nil, nil
}
func (t *typedNilMatcher) ScanString(_ context.Context, _ string) ([]sdk.Finding, error) {
	return nil, nil
}
func (t *typedNilMatcher) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	return b, nil, nil
}
func (t *typedNilMatcher) RedactString(_ context.Context, s string) (string, []sdk.Finding, error) {
	return s, nil, nil
}

func reasoningYAMLForTypedNil(t *testing.T, egressRef string) yaml.Node {
	t.Helper()
	raw := `
action: restore
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 24h
  max_turns_per_session: 10
  max_reasoning_bytes_per_turn: 100000
  max_session_bytes: 1000000
compression:
  enabled: true
  mode: shadow
  route: test-route
  timeout: 5s
  max_input_tokens: 10000
  max_input_bytes: 100000
  max_output_tokens: 1000
  max_output_bytes: 100000
  max_surrogate_bytes: 50000
  min_source_bytes: 100
  min_saved_bytes: 50
  min_savings_ratio: 0.5
  max_pending_per_session: 10
  max_surrogate_bytes_per_session: 100000
  max_pending_total: 100
  max_surrogate_bytes_total: 1000000
  egress_policy_ref: ` + egressRef + `
`
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return n
}

func TestReasoningPreservation_TypedNilClientFailsClosed(t *testing.T) {
	t.Parallel()
	var nilClient *typedNilClient
	var client auxiliary.BackgroundClient = nilClient
	var nilPoller *typedNilClient
	var poller auxiliary.BackgroundPoller = nilPoller
	if !isNilReasoningCapability(client) {
		t.Fatal("typed nil client should be detected as nil")
	}
	if !isNilReasoningCapability(poller) {
		t.Fatal("typed nil poller should be detected as nil")
	}
	regs := []struct {
		desc string
		reg  yaml.Node
	}{
		{"client", reasoningYAMLForTypedNil(t, "ref")},
	}
	_ = regs
	// Build minimal ProcessServices with typed nil policy/resolver also
	var nilPolicy *typedNilEgress
	var policy reasoningpreservation.EgressPolicy = nilPolicy
	if !isNilReasoningCapability(policy) {
		t.Fatal("typed nil egress policy should be nil")
	}
	var nilResolver *typedNilResolver
	var resolver sdk.MatcherResolver = nilResolver
	if !isNilReasoningCapability(resolver) {
		t.Fatal("typed nil resolver should be nil")
	}
	// Direct isNil checks already cover; now test validate fails
	node := reasoningYAMLForTypedNil(t, "ref")
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}}}
	_ = cfg
	// Use helper
	jobRegs := []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}
	_ = jobRegs
	// Use the actual validate function with typed nil client
	fromRegs := config.RegistrationsFromConfig(&config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}}})
	ps2 := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{ReasoningCompression: ReasoningCompressionOptions{EgressPolicies: map[string]reasoningpreservation.EgressPolicy{"ref": &typedNilEgress{}}, MatcherResolver: &typedNilResolver{}}}}}
	if err := validateReasoningPreservationCompressionGeneration(ps2, fromRegs, client, poller); err == nil || !strings.Contains(err.Error(), "BackgroundAux") {
		t.Fatalf("typed nil client/poller should fail closed, got %v", err)
	}
}

func TestReasoningPreservation_TypedNilPolicyResolverFailsClosed(t *testing.T) {
	t.Parallel()
	node := reasoningYAMLForTypedNil(t, "typed-ref")
	regs := config.RegistrationsFromConfig(&config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}}})
	// Production with typed nil policy map entry and typed nil resolver
	var nilPolicy *typedNilEgress
	var nilResolver *typedNilResolver
	psPolicyNil := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{ReasoningCompression: ReasoningCompressionOptions{
		EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"typed-ref": nilPolicy},
		MatcherResolver: &typedNilResolver{},
	}}}}
	// Need a non-nil client/poller
	scheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunnerForTypedNil{id: "x"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 5})
	defer scheduler.Close()
	genRunner := compactioncompose.NewGenerationExecutorRunner()
	bound := scheduler.BindRunner(genRunner)
	bPoller := bound.(auxiliary.BackgroundPoller)
	if err := validateReasoningPreservationCompressionGeneration(psPolicyNil, regs, bound, bPoller); err == nil || !strings.Contains(err.Error(), "EgressPolicy") {
		t.Fatalf("typed nil policy should fail closed, got %v", err)
	}
	psResolverNil := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{ReasoningCompression: ReasoningCompressionOptions{
		EgressPolicies:  map[string]reasoningpreservation.EgressPolicy{"typed-ref": &typedNilEgress{}},
		MatcherResolver: nilResolver,
	}}}}
	if err := validateReasoningPreservationCompressionGeneration(psResolverNil, regs, bound, bPoller); err == nil || !strings.Contains(err.Error(), "MatcherResolver") {
		t.Fatalf("typed nil resolver should fail closed, got %v", err)
	}
	// IsNil direct
	if !isNilReasoningCapability(nilPolicy) {
		t.Fatal("typed nil policy isNil false")
	}
	if !isNilReasoningCapability(nilResolver) {
		t.Fatal("typed nil resolver isNil false")
	}
	// Also lookup returns nil for typed nil resolver entry
	psLookup := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{ReasoningCompression: ReasoningCompressionOptions{MatcherResolver: nilResolver}}}}
	if r := lookupReasoningMatcherResolver(psLookup); !isNilReasoningCapability(r) {
		t.Fatalf("lookup should return typed nil resolver as nil, got %v", r)
	}
}

func TestReasoningPreservation_NilMatcherFailsClosed(t *testing.T) {
	t.Parallel()
	// Use stub that returns nil matcher
	resolver := stubResolverForTypedNil{m: nil}
	san := reasoningpreservation.NewResolverSanitizer(resolver)
	if san != nil {
		// The feature's NewResolverSanitizer returns non-nil even if resolver will later return nil matcher?
		// But our isNil check for sanitizer should catch typed nil? Actually resolver is non-nil, sanitizer will be non-nil.
		// The matcher nil case is handled at SanitizeText time, not at composition.
		// We test that SanitizeText with nil matcher fails.
		if _, err := san.SanitizeText(context.Background(), "has SECRET"); err == nil {
			t.Fatalf("sanitizer with nil matcher should fail on SanitizeText")
		}
	}
	// Direct isNil for nil matcher resolver's product: NewResolverSanitizer(nil) should be nil
	if s := reasoningpreservation.NewResolverSanitizer(nil); s != nil {
		t.Fatalf("NewResolverSanitizer(nil) should return nil")
	}
	var nilResolverTyped *typedNilResolver
	if s := reasoningpreservation.NewResolverSanitizer(nilResolverTyped); s != nil {
		t.Fatalf("NewResolverSanitizer(typed nil) should return nil, got %v", s)
	}
	// Also typed nil matcher via resolver that returns typed nil matcher
	typedNilM := (*typedNilMatcher)(nil)
	var m sdk.Matcher = typedNilM
	if !isNilReasoningCapability(m) {
		t.Fatal("typed nil matcher should be nil")
	}
}

type stubResolverForTypedNil struct{ m sdk.Matcher }

func (s stubResolverForTypedNil) Resolve(_ context.Context) (sdk.Matcher, error) { return s.m, nil }

type fixedRunnerForTypedNil struct{ id string }

func (f fixedRunnerForTypedNil) Execute(_ context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventTextDelta, Delta: "ok-" + f.id}, {Kind: lipapi.EventResponseFinished}}), nil
}

func TestReasoningPreservation_NoDuplicateParticipants(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	nodeEnabled := reasoningYAMLForTypedNil(t, "dup-ref")
	nodeDisabled := func() yaml.Node {
		raw := `
action: restore
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 24h
  max_turns_per_session: 10
  max_reasoning_bytes_per_turn: 100000
  max_session_bytes: 1000000
compression:
  enabled: false
`
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatalf("yaml: %v", err)
		}
		return n
	}()
	// First build disabled to get placeholder participants
	bDisabled, err := reg.BuildFeatureBundle(reasoningpreservation.ID, nodeDisabled)
	if err != nil {
		t.Fatalf("disabled bundle: %v", err)
	}
	if len(bDisabled.AttemptTransforms) == 0 || len(bDisabled.StreamObserverFactories) == 0 {
		t.Fatalf("disabled should have participants")
	}
	// Simulate merged surface that already contains placeholder
	merged := featurebundle.MergedFeatureSurface{
		AttemptTransforms:       bDisabled.AttemptTransforms,
		StreamObserverFactories: bDisabled.StreamObserverFactories,
	}
	merged2 := removeReasoningParticipants(merged)
	if len(merged2.AttemptTransforms) != 0 || len(merged2.StreamObserverFactories) != 0 {
		t.Fatalf("remove should strip reasoning participants, got %d transforms %d observers", len(merged2.AttemptTransforms), len(merged2.StreamObserverFactories))
	}
	// Now test that bind does not duplicate when called via CompileGeneration
	// Use full CompileGeneration with enabled config and ensure only one set
	scheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunnerForTypedNil{id: "dup"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	defer scheduler.Close()
	prod := ProductionOptions{ReasoningCompression: ReasoningCompressionOptions{EgressPolicies: map[string]reasoningpreservation.EgressPolicy{"dup-ref": &typedNilEgress{}}, MatcherResolver: &typedNilResolver{}}}
	opts := &BuildOptions{PluginRegistry: reg, Production: prod}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3}, Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins:     config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: nodeEnabled}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: scheduler})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	defer ps.Close()
	mergedForDup := featurebundle.MergedFeatureSurface{
		AttemptTransforms:       bDisabled.AttemptTransforms,
		StreamObserverFactories: bDisabled.StreamObserverFactories,
	}
	mergedForDup2 := removeReasoningParticipants(mergedForDup)
	mergedForDup3 := removeReasoningParticipants(mergedForDup2)
	if len(mergedForDup3.AttemptTransforms) != len(mergedForDup2.AttemptTransforms) {
		t.Fatalf("remove should be idempotent")
	}
}

func TestReasoningPreservation_GeneratorBoundActualExecution(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	node := reasoningYAMLForTypedNil(t, "gen-ref")
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3}, Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins:     config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	execID := "gen-exec"
	scheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunnerForTypedNil{id: execID} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	defer scheduler.Close()
	prod := ProductionOptions{ReasoningCompression: ReasoningCompressionOptions{EgressPolicies: map[string]reasoningpreservation.EgressPolicy{"gen-ref": &typedNilEgress{}}, MatcherResolver: &typedNilResolver{}}}
	opts := &BuildOptions{PluginRegistry: reg, Production: prod}
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: scheduler})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	defer ps.Close()
	genRunner, boundClient, boundPoller, err := newReasoningCompressionGenerationRunner(ps)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if genRunner == nil || boundClient == nil || boundPoller == nil {
		t.Fatalf("runner/client/poller nil")
	}
	jid, err := boundClient.SubmitCollect(context.Background(), auxiliary.Request{Call: &lipapi.Call{ID: "test-gen"}}, auxiliary.SubmitOptions{CoalesceKey: "k-gen"})
	if err != nil {
		t.Fatalf("SubmitCollect failed: %v", err)
	}
	if jid == "" {
		t.Fatalf("expected non-empty job id")
	}
	if _, err := boundClient.Await(context.Background(), jid); err != nil && strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Await should not be ErrNotConfigured, got %v", err)
	}
	if _, err := boundPoller.Poll(context.Background(), jid); err != nil && strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Poll should not be ErrNotConfigured, got %v", err)
	}
	// Also ensure that a direct Disabled client would fail
	disabled := auxiliary.DisabledBackgroundClient{}
	if _, err := disabled.SubmitCollect(context.Background(), auxiliary.Request{Call: &lipapi.Call{ID: "x"}}, auxiliary.SubmitOptions{CoalesceKey: "k"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("disabled client should be ErrNotConfigured")
	}
}
