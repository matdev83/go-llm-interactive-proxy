package runtimebundle

import (
	"testing"

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
