package runtimebundle

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"

	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/secretguard"
)

// TestSecretGuardFromPlanes_ReadIsolation pins read-boundary frozen-value
// isolation: mutating a plane/inventory returned by secretGuardFromPlanes
// must not alter subsequent reads from the same frozen set. The reader builds
// fresh structs and clones the categories slice on every call, so consumers
// never alias frozen generation state.
func TestSecretGuardFromPlanes_ReadIsolation(t *testing.T) {
	t.Parallel()

	cfg := &secretguard.ExecutionConfig{
		AccessMode:       "single_user",
		ConfigVersion:    "v1",
		SourceCategories: []string{"env", "catalog"},
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.ContributeSource(cs, lipfeature.PlaneSecretGuardExecution, lipfeature.SourceGenerationBinder, "secret-guard-execution", cfg); err != nil {
		t.Fatalf("ContributeSource: %v", err)
	}
	frozen := cs.Freeze()

	plane, inv := secretGuardFromPlanes(frozen)
	if plane.AccessMode != "single_user" {
		t.Fatalf("plane AccessMode=%q want single_user", plane.AccessMode)
	}
	if inv == nil {
		t.Fatal("expected non-nil inventory")
	}

	// Mutate everything mutable in the returned values.
	plane.AccessMode = "mutated"
	inv.SecretGuardAccessMode = "mutated"
	inv.SecretGuardCatalogEntryCount = -1
	if len(inv.SecretGuardSourceCategories) > 0 {
		inv.SecretGuardSourceCategories[0] = "mutated"
	}

	againPlane, againInv := secretGuardFromPlanes(frozen)
	if againPlane.AccessMode != "single_user" {
		t.Fatalf("reread plane AccessMode=%q want single_user", againPlane.AccessMode)
	}
	if againInv == nil {
		t.Fatal("expected non-nil inventory on reread")
	}
	if againInv.SecretGuardAccessMode != "single_user" {
		t.Fatalf("reread inventory access mode=%q want single_user", againInv.SecretGuardAccessMode)
	}
	if againInv.SecretGuardCatalogEntryCount != 0 {
		t.Fatalf("reread catalog count=%d want 0", againInv.SecretGuardCatalogEntryCount)
	}
	if len(againInv.SecretGuardSourceCategories) != 2 || againInv.SecretGuardSourceCategories[0] != "env" {
		t.Fatalf("reread categories=%v want [env catalog]", againInv.SecretGuardSourceCategories)
	}
}

func TestSecretGuardFromPlanes_DiscoveryCapabilityAndInventory(t *testing.T) {
	t.Parallel()
	capability := &struct{ generation string }{generation: "fixture"}
	cfg := &secretguard.ExecutionConfig{
		AccessMode: "single_user", Capability: capability,
		LocalAutoDiscoveryEnabled: true, BetterLeaksEnabled: true,
		BetterLeaksVersion: "fixture-version", BetterLeaksConfigHash: "fixture-config",
		BetterLeaksRuleCount: 0, BetterLeaksConfidence: "high",
		BetterLeaksDecodeDepth: 0, BetterLeaksWorkers: 2, DiscoveryDetectorCount: 1,
	}
	cs := lipfeature.NewContributionSet()
	if err := lipfeature.ContributeSource(cs, lipfeature.PlaneSecretGuardExecution, lipfeature.SourceGenerationBinder, "secret-guard-execution", cfg); err != nil {
		t.Fatal(err)
	}
	plane, inventory := secretGuardFromPlanes(cs.Freeze())
	if plane.Capability != capability {
		t.Fatal("frozen discovery capability lost at runtime composition boundary")
	}
	want := &diag.InventoryExtras{
		SecretGuardAccessMode: "single_user", SecretGuardLocalAutoDiscovery: true,
		SecretGuardBetterLeaksEnabled: true, SecretGuardBetterLeaksVersion: "fixture-version",
		SecretGuardBetterLeaksConfigHash: "fixture-config", SecretGuardBetterLeaksRuleCount: 0,
		SecretGuardBetterLeaksConfidence: "high", SecretGuardBetterLeaksDecodeDepth: 0,
		SecretGuardBetterLeaksWorkers: 2, SecretGuardDiscoveryDetectorCount: 1,
	}
	if !reflect.DeepEqual(inventory, want) {
		t.Fatal("frozen discovery posture lost in diagnostics, including explicit zero settings")
	}
}
