package runtimebundle_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	compatibleadmission "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/compatible"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	coreruntime "github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/compatibleparity"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"gopkg.in/yaml.v3"
)

func TestCompatibleMultiInstance_IsolationAcrossFamilies(t *testing.T) {
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	for _, pair := range compatibleparity.IsolationPairs() {
		pair := pair
		t.Run(string(pair.Family), func(t *testing.T) {
			srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeMultiInstanceResponse(w, "A", pair.Family, r.URL.Path)
			}))
			srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeMultiInstanceResponse(w, "B", pair.Family, r.URL.Path)
			}))
			t.Cleanup(srvA.Close)
			t.Cleanup(srvB.Close)

			instA := pair.InstanceA
			instB := pair.InstanceB
			instA.BaseURL = multiInstanceBaseURL(pair.Family, srvA.URL)
			instB.BaseURL = multiInstanceBaseURL(pair.Family, srvB.URL)
			t.Setenv(instA.APIKeyEnvVarRoot, instA.EnvKeyValue)
			t.Setenv(instB.APIKeyEnvVarRoot, instB.EnvKeyValue)

			beA := buildMultiInstanceBackend(t, reg, pair.Factory, instA, srvA.Client())
			beB := buildMultiInstanceBackend(t, reg, pair.Factory, instB, srvB.Client())

			if beA.TokenizerID == beB.TokenizerID {
				t.Fatalf("tokenizer ids must differ: %q", beA.TokenizerID)
			}
			if beA.LocalCounter == beB.LocalCounter {
				t.Fatal("shared local counter across instances")
			}
			if beA.BackendPrefixes[0] == beB.BackendPrefixes[0] {
				t.Fatal("shared prefix")
			}

			if err := assertIndependentConcurrency(t, pair.Family, instA.InstanceID, instA.MaxConcurrentRequests); err != nil {
				t.Fatalf("instance A: %v", err)
			}
			if err := assertIndependentConcurrency(t, pair.Family, instB.InstanceID, instB.MaxConcurrentRequests); err != nil {
				t.Fatalf("instance B: %v", err)
			}
		})
	}
}

func TestCompatibleMultiInstance_runtimeBundleBuildPreservesProvenance(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	t.Cleanup(srv.Close)

	rows := fmt.Sprintf(`    - id: compat-a
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-a
        base_url: %s/v1
        tokenizer: cl100k_base
        max_concurrent_requests: 1
    - id: compat-b
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-b
        base_url: %s/v1
        tokenizer: o200k_base
        max_concurrent_requests: 2
`, srv.URL, srv.URL)
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	backends := hostActiveCompatibleBackends(t, host)
	beA, okA := backends["compat-a"]
	beB, okB := backends["compat-b"]
	if !okA || !okB {
		t.Fatalf("backends missing compat-a=%v compat-b=%v", okA, okB)
	}
	if beA.TokenizerID == beB.TokenizerID {
		t.Fatalf("tokenizer provenance collapsed: %q", beA.TokenizerID)
	}
	if beA.TokenizerID != "cl100k_base" || beB.TokenizerID != "o200k_base" {
		t.Fatalf("tokenizer ids A=%q B=%q", beA.TokenizerID, beB.TokenizerID)
	}
}

func hostActiveCompatibleBackends(t *testing.T, host *runtimebundle.Host) map[string]execbackend.Backend {
	t.Helper()
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("active generation missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor from active generation")
	}
	return ex.Backends
}

func buildMultiInstanceBackend(t *testing.T, reg *pluginreg.Registry, factory string, inst compatibleparity.InstanceConfig, client *http.Client) execbackend.Backend {
	t.Helper()
	raw := compatibleparity.CompatibleYAML(inst, inst.BaseURL)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(factory, inst.InstanceID, node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Backend
}

func assertIndependentConcurrency(t *testing.T, family compatibleparity.Family, backendID string, max int) error {
	t.Helper()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{backendID: max}, leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "compatible-admission-test"}))
	if err != nil {
		return err
	}
	coord := &authoritycoord.AttemptCoordinator{Slots: []authoritycoord.AttemptSlot{{
		ID: compatibleadmission.ProviderID, Provider: reg.Provider,
		Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
	}}}
	block := make(chan struct{})
	var peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < max+3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bleg := fmt.Sprintf("b%d", i)
			d, err := coord.Admit(context.Background(), authorityAttemptAdmission(backendID, bleg))
			if err != nil {
				return
			}
			cur := peak.Add(1)
			if cur > peak.Load() {
				peak.Store(cur)
			}
			<-block
			_ = coord.Release(context.Background(), d.Stack)
			peak.Add(-1)
		}(i)
	}
	time.Sleep(40 * time.Millisecond)
	close(block)
	wg.Wait()
	if peak.Load() > int32(max) {
		return fmt.Errorf("peak in-flight=%d exceeds max=%d", peak.Load(), max)
	}
	return nil
}

func authorityAttemptAdmission(backendID, bleg string) authority.AttemptAdmission {
	return authority.AttemptAdmission{
		RequestID:   "req",
		AttemptID:   bleg,
		BLegID:      bleg,
		BackendID:   backendID,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Perspective: metering.PerspectiveOperator,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	}
}

func multiInstanceCall(family compatibleparity.Family) lipapi.Call {
	op := lipapi.OperationOpenAIChatCompletions
	if family == compatibleparity.FamilyOpenAIResponses {
		op = lipapi.OperationOpenAIResponses
	}
	if family == compatibleparity.FamilyAnthropic {
		op = ""
	}
	return lipapi.Call{
		Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("iso")}}},
		Invocation: lipapi.Invocation{
			Operation:     op,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
}

func multiInstanceBaseURL(family compatibleparity.Family, srvURL string) string {
	if family == compatibleparity.FamilyAnthropic {
		return srvURL
	}
	return srvURL + "/v1"
}

func factoryForMulti(family compatibleparity.Family) string {
	switch family {
	case compatibleparity.FamilyOpenAILegacy:
		return standardplugins.CustomOpenAILegacyCompatibleID
	case compatibleparity.FamilyOpenAIResponses:
		return standardplugins.CustomOpenAIResponsesCompatibleID
	case compatibleparity.FamilyAnthropic:
		return standardplugins.CustomAnthropicCompatibleID
	default:
		return ""
	}
}

func writeMultiInstanceResponse(w http.ResponseWriter, label string, family compatibleparity.Family, path string) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(path, "/responses"):
		_, _ = io.WriteString(w, `{"id":"iso","object":"response","created_at":1,"status":"completed","model":"iso-model","output":[{"type":"message","id":"m","status":"completed","role":"assistant","content":[{"type":"output_text","text":"iso-`+label+`"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	case strings.Contains(path, "/messages"):
		_, _ = io.WriteString(w, `{"id":"iso","type":"message","role":"assistant","model":"iso-model","content":[{"type":"text","text":"iso-`+label+`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	default:
		_, _ = io.WriteString(w, `{"id":"iso","object":"chat.completion","created":1,"model":"iso-model","choices":[{"index":0,"message":{"role":"assistant","content":"iso-`+label+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}
}

func TestCompatibleMultiInstance_routingPolicyIndependence(t *testing.T) {
	ctx := context.Background()
	var hitsA, hitsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		writeMultiInstanceResponse(w, "A", compatibleparity.FamilyOpenAILegacy, r.URL.Path)
	}))
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		writeMultiInstanceResponse(w, "B", compatibleparity.FamilyOpenAILegacy, r.URL.Path)
	}))
	failA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	slowA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		time.Sleep(250 * time.Millisecond)
		writeMultiInstanceResponse(w, "slow-A", compatibleparity.FamilyOpenAILegacy, r.URL.Path)
	}))
	t.Cleanup(func() {
		srvA.Close()
		srvB.Close()
		failA.Close()
		slowA.Close()
	})

	t.Setenv("COMPAT_ROUTE_A_KEY", "sk-a")
	t.Setenv("COMPAT_ROUTE_B_KEY", "sk-b")

	path := writeCompatibleRoutingConfig(t, map[string]routingInstanceSpec{
		"compat-a": {URL: srvA.URL, Tokenizer: "cl100k_base", MaxConcurrent: 1, EnvRoot: "COMPAT_ROUTE_A_KEY"},
		"compat-b": {URL: srvB.URL, Tokenizer: "o200k_base", MaxConcurrent: 2, EnvRoot: "COMPAT_ROUTE_B_KEY"},
	})

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	inv, err := runtimebundle.InspectInventory(ctx, runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.CompatibleBackends) != 2 {
		t.Fatalf("compatible_backends=%d", len(inv.CompatibleBackends))
	}
	byID := map[string]struct{}{}
	for _, row := range inv.CompatibleBackends {
		byID[row.InstanceID] = struct{}{}
		if row.Origin != "built_in_compatible" {
			t.Fatalf("origin=%q instance=%q", row.Origin, row.InstanceID)
		}
		if row.InventoryHealth == nil || row.InventoryHealth.ModelCount != 1 {
			t.Fatalf("inventory health=%+v instance=%q", row.InventoryHealth, row.InstanceID)
		}
	}
	if _, ok := byID["compat-a"]; !ok {
		t.Fatalf("missing compat-a provenance row: %+v", inv.CompatibleBackends)
	}
	if _, ok := byID["compat-b"]; !ok {
		t.Fatalf("missing compat-b provenance row: %+v", inv.CompatibleBackends)
	}

	ex := hostExecutor(t, host)
	ex.Rand = routing.NewSeededRng(1)

	t.Run("sequential", func(t *testing.T) {
		hitsA.Store(0)
		hitsB.Store(0)
		text, err := collectExecutorText(ctx, ex, "compat-a:compat-a/model-a")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-A" {
			t.Fatalf("text=%q", text)
		}
		if hitsA.Load() == 0 || hitsB.Load() != 0 {
			t.Fatalf("hits A=%d B=%d", hitsA.Load(), hitsB.Load())
		}
	})

	t.Run("failover", func(t *testing.T) {
		pathFail := writeCompatibleRoutingConfig(t, map[string]routingInstanceSpec{
			"compat-a": {URL: failA.URL, Tokenizer: "cl100k_base", MaxConcurrent: 1, EnvRoot: "COMPAT_ROUTE_A_KEY"},
			"compat-b": {URL: srvB.URL, Tokenizer: "o200k_base", MaxConcurrent: 2, EnvRoot: "COMPAT_ROUTE_B_KEY"},
		})
		hostFail, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
			ConfigPath: pathFail, Mandatory: lipsdk.StandardDistributionRequirements(), HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = hostFail.Close(context.Background()) })
		exFail := hostExecutor(t, hostFail)
		text, err := collectExecutorText(ctx, exFail, "compat-a:compat-a/model-a!compat-b:compat-b/model-b")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-B" {
			t.Fatalf("text=%q", text)
		}
	})

	t.Run("parallel", func(t *testing.T) {
		pathPar := writeCompatibleRoutingConfig(t, map[string]routingInstanceSpec{
			"compat-a": {URL: slowA.URL, Tokenizer: "cl100k_base", MaxConcurrent: 1, EnvRoot: "COMPAT_ROUTE_A_KEY"},
			"compat-b": {URL: srvB.URL, Tokenizer: "o200k_base", MaxConcurrent: 2, EnvRoot: "COMPAT_ROUTE_B_KEY"},
		})
		hostPar, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
			ConfigPath: pathPar, Mandatory: lipsdk.StandardDistributionRequirements(), HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = hostPar.Close(context.Background()) })
		exPar := hostExecutor(t, hostPar)
		exPar.Rand = routing.NewSeededRng(1)
		text, err := collectExecutorText(ctx, exPar, "compat-a:compat-a/model-a!compat-b:compat-b/model-b")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-B" {
			t.Fatalf("parallel winner text=%q want iso-B", text)
		}
	})

	t.Run("weighted", func(t *testing.T) {
		hitsA.Store(0)
		hitsB.Store(0)
		text, err := collectExecutorText(ctx, ex, "[weight=1]compat-a:compat-a/model-a^[weight=1]compat-b:compat-b/model-b")
		if err != nil {
			t.Fatal(err)
		}
		if text != "iso-A" && text != "iso-B" {
			t.Fatalf("unexpected weighted text=%q", text)
		}
		if hitsA.Load() == 0 && hitsB.Load() == 0 {
			t.Fatal("expected at least one backend hit")
		}
	})
}

type routingInstanceSpec struct {
	URL           string
	Tokenizer     string
	MaxConcurrent int
	EnvRoot       string
}

func writeCompatibleRoutingConfig(t *testing.T, specs map[string]routingInstanceSpec) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var rows strings.Builder
	for id, spec := range specs {
		rows.WriteString(fmt.Sprintf(`    - id: %s
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: %s
        base_url: %s/v1
        api_key_env_var_root: %s
        tokenizer: %s
        max_concurrent_requests: %d
        models:
          source: inline
          items:
            - canonical_id: %s/model-a
              native_id: model-a
`, id, id, spec.URL, spec.EnvRoot, spec.Tokenizer, spec.MaxConcurrent, id))
	}
	text := strings.Replace(string(base), "  features:\n", rows.String()+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hostExecutor(t *testing.T, host *runtimebundle.Host) *coreruntime.Executor {
	t.Helper()
	return hostActiveCompatibleBackendsExecutor(t, host)
}

func hostActiveCompatibleBackendsExecutor(t *testing.T, host *runtimebundle.Host) *coreruntime.Executor {
	t.Helper()
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor")
	}
	return ex
}

func collectExecutorText(ctx context.Context, ex *coreruntime.Executor, selector string) (string, error) {
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: selector},
		Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart("route-test")},
		}},
		Invocation: lipapi.Invocation{
			Operation:     lipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  lipapi.DeliveryModeNonStreaming,
			TransportMode: lipapi.TransportModeNonStreaming,
		},
	}
	s, err := ex.Execute(ctx, call)
	if err != nil {
		return "", err
	}
	col, err := lipapi.Collect(ctx, s)
	if err != nil {
		return "", err
	}
	return col.Text.String(), nil
}
