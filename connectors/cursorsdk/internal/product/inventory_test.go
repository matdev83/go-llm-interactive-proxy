package product_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product"
	"github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk/internal/product/protocol"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
	"github.com/stretchr/testify/require"
)

type sanitizedFixtureFile struct {
	Models []protocol.ModelRow `json:"models"`
}

func loadSanitizedFixtureRows(t *testing.T) []protocol.ModelRow {
	t.Helper()
	path := filepath.Join("testdata", "fixtures", "models_sanitized.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc sanitizedFixtureFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if len(doc.Models) == 0 {
		t.Fatal("fixture models empty")
	}
	return doc.Models
}

func TestInventory_NormalizeSanitizedFixture(t *testing.T) {
	t.Parallel()
	rows := loadSanitizedFixtureRows(t)
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{Rows: rows}).Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want remote", snap.Source)
	}
	byNative := map[string]modelinventory.Model{}
	for _, m := range snap.Models {
		byNative[m.NativeID] = m
	}
	want := map[string]string{
		"gpt-5.3-codex":              "cursor/gpt-5.3-codex",
		"claude-4.6-sonnet-thinking": "cursor/claude-4.6-sonnet-thinking",
		"composer-2-fast":            "cursor/composer-2-fast",
	}
	for native, canonical := range want {
		m, ok := byNative[native]
		if !ok {
			t.Fatalf("missing native %q", native)
		}
		if m.CanonicalID != canonical {
			t.Fatalf("%s CanonicalID = %q, want %q", native, m.CanonicalID, canonical)
		}
		if m.DisplayName == "" {
			t.Fatalf("%s DisplayName empty", native)
		}
	}
}

func TestInventory_RejectEmptyAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{
		Rows: []protocol.ModelRow{{ID: "a"}, {ID: "a"}},
	}).Backend()
	_, err := be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("err = %v, want invalid_inventory OperationalError", err)
	}

	be = product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{
		Rows: []protocol.ModelRow{{ID: "  "}},
	}).Backend()
	_, err = be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected empty id error")
	}
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("err = %v, want invalid_inventory OperationalError", err)
	}
}

func TestInventory_AcceptsParameterCatalogWithoutVariants(t *testing.T) {
	t.Parallel()
	rows := []protocol.ModelRow{
		{
			ID:          "gpt-5.3-codex",
			DisplayName: "GPT-5.3 Codex",
			Parameters: []protocol.ModelParameter{
				{ID: "reasoning", Type: "string", Values: []string{"low", "medium", "high", "xhigh"}},
			},
			Variants: []protocol.ModelVariant{},
		},
	}
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{Rows: rows}).Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.Models, 1)
	require.Equal(t, "gpt-5.3-codex", snap.Models[0].NativeID)
	require.Equal(t, "cursor/gpt-5.3-codex", snap.Models[0].CanonicalID)
}

func TestInventory_AnonymousEffortThinkingVariantsAdvertiseReasoning(t *testing.T) {
	t.Parallel()
	rows := []protocol.ModelRow{
		{
			ID:          "claude-4.6-sonnet-thinking",
			DisplayName: "Claude 4.6 Sonnet Thinking",
			Parameters: []protocol.ModelParameter{
				{ID: "thinking", Values: []string{"true", "false"}},
				{ID: "effort", Values: []string{"low", "medium", "high", "extra-high"}},
			},
			Variants: []protocol.ModelVariant{
				{DisplayName: "High", Params: map[string]any{"effort": "high", "thinking": true}},
				{DisplayName: "Extra High", Params: map[string]any{"effort": "extra-high", "thinking": true}},
				{DisplayName: "High off", Params: map[string]any{"effort": "high", "thinking": false}},
			},
		},
	}
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{Rows: rows}).Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.Models, 1)
	require.Equal(t, "claude-4.6-sonnet-thinking", snap.Models[0].NativeID)
	for _, m := range snap.Models {
		require.NotEqual(t, "", m.NativeID)
		require.NotContains(t, m.CanonicalID, "effort-")
	}

	accepted, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
	require.True(t, ok)
	accepted.AcceptInventory(snap.Models)
	caps := be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "claude-4.6-sonnet-thinking"},
	})
	_, hasReasoning := caps[lipapi.CapabilityReasoning]
	require.True(t, hasReasoning, "anonymous thinking=true variants must advertise CapabilityReasoning")
}

func TestInventory_RejectsAnonymousVariantWithoutParams(t *testing.T) {
	t.Parallel()
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{
		Rows: []protocol.ModelRow{{
			ID:       "m",
			Variants: []protocol.ModelVariant{{DisplayName: "empty"}},
		}},
	}).Backend()
	_, err := be.ModelInventory.LoadModels(context.Background())
	require.Error(t, err)
	var op *modelinventory.OperationalError
	require.True(t, errors.As(err, &op))
	require.Equal(t, modelinventory.ErrorCodeInvalidInventory, op.Code)
}

func TestInventory_ResolveCapsEmptyUntilAccepted(t *testing.T) {
	t.Parallel()
	rows := loadSanitizedFixtureRows(t)
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{Rows: rows}).Backend()
	if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	caps := be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "gpt-5.3-codex"},
	})
	if len(caps) != 0 {
		t.Fatalf("caps before AcceptInventory = %#v, want empty", caps)
	}
	accepted, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
	if !ok {
		t.Fatalf("inventory %T missing AcceptedInventory", be.ModelInventory)
	}
	accepted.AcceptInventory([]modelinventory.Model{{
		CanonicalID: "cursor/gpt-5.3-codex",
		NativeID:    "gpt-5.3-codex",
		DisplayName: "GPT-5.3 Codex",
	}})
	caps = be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "gpt-5.3-codex"},
	})
	if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("expected streaming after accept")
	}
	if _, ok := caps[lipapi.CapabilityReasoning]; !ok {
		t.Fatal("expected reasoning after accept for gpt-5.3-codex")
	}
	rejected := be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "composer-2-fast"},
	})
	if len(rejected) != 0 {
		t.Fatalf("unaccepted model caps = %#v, want empty", rejected)
	}
}

func TestInventory_FailSoftOperationalListError(t *testing.T) {
	t.Parallel()
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{
		Err: errors.New("bridge unavailable"),
	}).Backend()
	_, err := be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !modelinventory.IsOperational(err) {
		t.Fatalf("err = %v, want operational", err)
	}
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("err = %v, want unavailable OperationalError", err)
	}
}

func TestInventory_UnreachableBridgeOperationalFailSoft(t *testing.T) {
	t.Parallel()
	cfg := mustConfig(t)
	cfg.BridgeExecutable = filepath.Join(t.TempDir(), "missing-cursor-sdk-bridge")
	cfg.BridgeStartTimeout = 200 * time.Millisecond
	be := product.NewScaffold(cfg).Backend()
	t.Cleanup(func() { _ = be.Close() })
	_, err := be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected unreachable-bridge inventory error")
	}
	if !modelinventory.IsOperational(err) {
		t.Fatalf("err = %v, want operational", err)
	}
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if strings.Contains(err.Error(), cfg.APIKey) {
		t.Fatalf("inventory error leaked api_key: %v", err)
	}
	if len(be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "gpt-5.3-codex"},
	})) != 0 {
		t.Fatal("unreachable-bridge backend must not advertise caps for unknown models")
	}
}

func TestInventory_FailedReloadKeepsLastKnownGoodCatalog(t *testing.T) {
	t.Parallel()
	rows := loadSanitizedFixtureRows(t)
	src := &scriptedModelListSource{rows: rows}
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(src).Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("initial LoadModels: %v", err)
	}
	acceptedInv, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
	require.True(t, ok)
	acceptedInv.AcceptInventory(snap.Models)

	caps := be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "gpt-5.3-codex"},
	})
	if _, ok := caps[lipapi.CapabilityReasoning]; !ok {
		t.Fatal("expected reasoning after successful load+accept")
	}

	src.set(nil, errors.New("bridge blip"))
	_, err = be.ModelInventory.LoadModels(context.Background())
	if err == nil || !modelinventory.IsOperational(err) {
		t.Fatalf("source error = %v, want operational", err)
	}
	caps = be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "gpt-5.3-codex"},
	})
	if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("fail-soft last-known-good: streaming must remain for accepted model after source error")
	}
	if _, ok := caps[lipapi.CapabilityReasoning]; !ok {
		t.Fatal("fail-soft last-known-good: catalog must not clear on source error")
	}

	src.set([]protocol.ModelRow{{ID: "dup"}, {ID: "dup"}}, nil)
	_, err = be.ModelInventory.LoadModels(context.Background())
	if err == nil {
		t.Fatal("expected duplicate reload error")
	}
	var op *modelinventory.OperationalError
	if !errors.As(err, &op) || op.Code != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("err = %v, want invalid_inventory", err)
	}
	caps = be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
		Primary: product.Primary{Model: "gpt-5.3-codex"},
	})
	if _, ok := caps[lipapi.CapabilityReasoning]; !ok {
		t.Fatal("duplicate reload must not install partial/bad catalog entries")
	}
	if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
		t.Fatal("accepted index must remain intact across failed reloads")
	}
}

func TestInventory_CapsProvenMappingsOnly(t *testing.T) {
	t.Parallel()
	rows := loadSanitizedFixtureRows(t)
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(product.StaticModelListSource{Rows: rows}).Backend()
	if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	accepted, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
	require.True(t, ok)
	accepted.AcceptInventory([]modelinventory.Model{
		{CanonicalID: "cursor/gpt-5.3-codex", NativeID: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex"},
		{CanonicalID: "cursor/claude-4.6-sonnet-thinking", NativeID: "claude-4.6-sonnet-thinking", DisplayName: "Claude"},
		{CanonicalID: "cursor/composer-2-fast", NativeID: "composer-2-fast", DisplayName: "Composer"},
	})

	cases := []struct {
		native    string
		reasoning bool
	}{
		{"gpt-5.3-codex", true},
		{"claude-4.6-sonnet-thinking", true},
		{"composer-2-fast", false},
	}
	for _, tc := range cases {
		caps := be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
			Primary: product.Primary{Model: tc.native},
		})
		if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
			t.Fatalf("%s: missing streaming", tc.native)
		}
		_, hasReasoning := caps[lipapi.CapabilityReasoning]
		if hasReasoning != tc.reasoning {
			t.Fatalf("%s: reasoning = %v, want %v", tc.native, hasReasoning, tc.reasoning)
		}
		for _, forbidden := range []lipapi.Capability{
			lipapi.CapabilityTools,
			lipapi.CapabilityVision,
			lipapi.CapabilityDocuments,
			lipapi.CapabilityStructuredOutputs,
			lipapi.CapabilityParallelToolCalls,
		} {
			if _, ok := caps[forbidden]; ok {
				t.Fatalf("%s: must omit %s", tc.native, forbidden)
			}
		}
	}
}

func TestInventory_ConcurrentLoadAcceptResolve(t *testing.T) {
	t.Parallel()
	rows := loadSanitizedFixtureRows(t)
	src := &scriptedModelListSource{rows: rows}
	be := product.NewScaffold(mustConfig(t)).WithModelListSource(src).Backend()
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acceptedInv, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
	require.True(t, ok)
	acceptedInv.AcceptInventory(snap.Models)

	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	for range 8 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			acceptedInv, ok := be.ModelInventory.(modelinventory.AcceptedInventory)
			require.True(t, ok)
			acceptedInv.AcceptInventory(snap.Models)
		}()
		go func() {
			defer wg.Done()
			caps := be.ResolveCaps(context.Background(), lipapi.Call{}, product.AttemptCandidate{
				Primary: product.Primary{Model: "gpt-5.3-codex"},
			})
			if _, ok := caps[lipapi.CapabilityStreaming]; !ok {
				errCh <- errors.New("missing streaming under race")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestScaffold_TrackingInventoryType(t *testing.T) {
	t.Parallel()
	be := product.NewScaffold(mustConfig(t)).Backend()
	if _, ok := be.ModelInventory.(*acp.TrackingInventory); !ok {
		t.Fatalf("ModelInventory type %T, want *acp.TrackingInventory", be.ModelInventory)
	}
}

func mustConfig(t *testing.T) product.Config {
	t.Helper()
	cfg, err := product.Normalize(product.Input{
		APIKey:           "test-key",
		BridgeExecutable: os.Args[0],
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
