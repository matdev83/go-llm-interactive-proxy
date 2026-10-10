package runtimebundle_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authoritycoord"
	compatibleadmission "github.com/matdev83/go-llm-interactive-proxy/internal/core/concurrencyauthority/compatible"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/concurrencyauthority/leasestore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/compatibleparity"
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

func assertIndependentConcurrency(t *testing.T, family compatibleparity.Family, backendID string, limit int) error {
	t.Helper()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{backendID: limit}, leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "compatible-admission-test"}))
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
	for i := 0; i < limit+3; i++ {
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
	if peak.Load() > int32(limit) {
		return fmt.Errorf("peak in-flight=%d exceeds limit=%d", peak.Load(), limit)
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

func multiInstanceBaseURL(family compatibleparity.Family, srvURL string) string {
	if family == compatibleparity.FamilyAnthropic {
		return srvURL
	}
	return srvURL + "/v1"
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
