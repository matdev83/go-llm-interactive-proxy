package runtimebundle

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/preflight"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	tiktokenlocal "github.com/matdev83/go-llm-interactive-proxy/internal/infra/tokenizers/tiktoken"
	"github.com/matdev83/go-llm-interactive-proxy/internal/pluginreg"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"gopkg.in/yaml.v3"
)

func TestBuildTokenCounters_routesPerBackendLocalCounter(t *testing.T) {
	t.Parallel()

	cl100k, err := tiktokenlocal.NewCounter(tiktokenlocal.Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatal(err)
	}
	o200k, err := tiktokenlocal.NewCounter(tiktokenlocal.Config{DefaultEncoding: "o200k_base"})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := tiktokenlocal.NewCounter(tiktokenlocal.Config{DefaultEncoding: "cl100k_base"})
	if err != nil {
		t.Fatal(err)
	}

	backends := map[string]execbackend.Backend{
		"compat-a": {LocalCounter: cl100k, TokenizerID: "cl100k_base"},
		"compat-b": {LocalCounter: o200k, TokenizerID: "o200k_base"},
		"native":   {},
	}
	local := newBackendLocalCounter(backends, fallback)

	const text = "tokenizer routing behavioral probe"
	inA := accountingapp.CountTextInput{Backend: "compat-a", Model: "probe-model", Text: text}
	inB := accountingapp.CountTextInput{Backend: "compat-b", Model: "probe-model", Text: text}
	inNative := accountingapp.CountTextInput{Backend: "native", Model: "probe-model", Text: text}

	gotA, err := local.CountText(context.Background(), inA)
	if err != nil {
		t.Fatalf("compat-a: %v", err)
	}
	gotB, err := local.CountText(context.Background(), inB)
	if err != nil {
		t.Fatalf("compat-b: %v", err)
	}
	gotNative, err := local.CountText(context.Background(), inNative)
	if err != nil {
		t.Fatalf("native: %v", err)
	}

	if gotA.Accounting.Tokenizer.ID != "cl100k_base" {
		t.Fatalf("compat-a encoding=%q want cl100k_base", gotA.Accounting.Tokenizer.ID)
	}
	if gotB.Accounting.Tokenizer.ID != "o200k_base" {
		t.Fatalf("compat-b encoding=%q want o200k_base", gotB.Accounting.Tokenizer.ID)
	}
	if gotNative.Accounting.Tokenizer.ID != "cl100k_base" {
		t.Fatalf("native encoding=%q want global fallback cl100k_base", gotNative.Accounting.Tokenizer.ID)
	}
	if gotA.InputTokens == gotB.InputTokens {
		t.Fatalf("expected distinct token counts for distinct encodings: A=%d B=%d", gotA.InputTokens, gotB.InputTokens)
	}
}

func TestBindTokenAccountingRuntime_compatibleInstancesUseDistinctLocalCounts(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)

	beA, err := buildCompatibleForAccounting(t, reg, "compat-a", srv.URL, "cl100k_base", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	beB, err := buildCompatibleForAccounting(t, reg, "compat-b", srv.URL, "o200k_base", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]execbackend.Backend{"compat-a": beA, "compat-b": beB}

	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Enabled:   true,
			Mode:      "local_only",
			Tokenizer: config.AccountingTokenizerConfig{DefaultEncoding: "cl100k_base"},
			Preflight: config.AccountingPreflightConfig{Mode: "advisory"},
		},
	}
	rt, err := bindTokenAccountingRuntime(&processAccountingStores{}, cfg, backends)
	if err != nil {
		t.Fatal(err)
	}
	if rt == nil || rt.Counter == nil {
		t.Fatal("expected token accounting runtime")
	}

	const text = "tokenizer routing behavioral probe"
	gotA, err := rt.Counter.CountText(context.Background(), accountingapp.CountTextInput{
		Backend: "compat-a", Model: "probe-model", Text: text,
	})
	if err != nil {
		t.Fatalf("compat-a count: %v", err)
	}
	gotB, err := rt.Counter.CountText(context.Background(), accountingapp.CountTextInput{
		Backend: "compat-b", Model: "probe-model", Text: text,
	})
	if err != nil {
		t.Fatalf("compat-b count: %v", err)
	}
	if gotA.InputTokens == gotB.InputTokens {
		t.Fatalf("expected distinct counts via accounting service: A=%d B=%d", gotA.InputTokens, gotB.InputTokens)
	}
	if gotA.Accounting.Tokenizer.ID != "cl100k_base" || gotB.Accounting.Tokenizer.ID != "o200k_base" {
		t.Fatalf("encoding metadata A=%q B=%q", gotA.Accounting.Tokenizer.ID, gotB.Accounting.Tokenizer.ID)
	}

	preflight := rt.Preflight.Check(context.Background(), accountingpreflight.Input{
		Backend: "compat-b",
		Model:   "probe-model",
		CallID:  "call-1",
		Call: lipapi.Call{Messages: []lipapi.Message{{
			Role:  lipapi.RoleUser,
			Parts: []lipapi.Part{lipapi.TextPart(text)},
		}}},
	})
	if !preflight.Allowed {
		t.Fatalf("preflight must route per-backend local counter: %+v", preflight)
	}

	stream, err := rt.StreamUsage.Reconstruct(context.Background(), accountingstream.Input{
		Backend: "compat-a",
		Model:   "probe-model",
		Call: lipapi.Call{
			ID: "call-2",
			Messages: []lipapi.Message{{
				Role:  lipapi.RoleUser,
				Parts: []lipapi.Part{lipapi.TextPart(text)},
			}},
		},
		OutputText: text,
		Events: []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: text},
			{Kind: lipapi.EventResponseFinished},
		},
	})
	if err != nil {
		t.Fatalf("stream reconstruction: %v", err)
	}
	if stream.Reconciled.BillableUsage.InputTokens <= 0 {
		t.Fatalf("stream reconstruction produced no input tokens: %+v", stream.Reconciled.BillableUsage)
	}
}

func buildCompatibleForAccounting(t *testing.T, reg *pluginreg.Registry, id, baseURL, tokenizer string, client *http.Client) (execbackend.Backend, error) {
	t.Helper()
	raw := `backend_prefix: ` + id + `
base_url: ` + baseURL + `/v1
tokenizer: ` + tokenizer
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return execbackend.Backend{}, err
	}
	res, err := reg.BuildBackendWithLifecycle(standardplugins.CustomOpenAILegacyCompatibleID, id, node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		return execbackend.Backend{}, err
	}
	be := res.Backend
	if strings.TrimSpace(be.TokenizerID) != tokenizer {
		t.Fatalf("TokenizerID=%q want %q", be.TokenizerID, tokenizer)
	}
	if be.LocalCounter == nil {
		t.Fatal("expected LocalCounter on compatible backend")
	}
	return be, nil
}
