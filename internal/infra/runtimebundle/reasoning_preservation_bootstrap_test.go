package runtimebundle_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/diag"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestBuildBootstrap_absentReasoningPreservationInjectsDefaultParticipants(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")

	t.Run("inspect", func(t *testing.T) {
		t.Parallel()
		snap, err := runtimebundle.InspectInventory(t.Context(), runtimebundle.InspectInput{
			ConfigPath: path,
			Mandatory:  lipsdk.StandardDistributionRequirements(),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertInventoryFeatureRowEnabled(t, snap, standardplugins.ReasoningOutputPreservationFeatureID, true)
	})

	t.Run("serve", func(t *testing.T) {
		t.Parallel()
		res, err := runtimebundle.BuildBootstrap(t.Context(), runtimebundle.BuildBootstrapInput{
			ConfigPath:      path,
			Mode:            runtimebundle.BootstrapServe,
			Mandatory:       lipsdk.StandardDistributionRequirements(),
			LogWriter:       io.Discard,
			HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		bootstrapServeCleanup(t, res)
		assertFeatureRowEnabled(t, res, standardplugins.ReasoningOutputPreservationFeatureID, true)
		assertReasoningRegistrationEnabled(t, res, true)
		assertHasReasoningParticipants(t, res, true)
		if res.ProcessServices == nil || res.InitialGeneration == nil {
			t.Fatal("BootstrapServe must publish generation host handles")
		}
		lease, ok := res.GenerationManager.Acquire()
		if !ok || lease.Handler() == nil {
			t.Fatal("BootstrapServe must publish an acquireable handler")
		}
		lease.Release()
	})
}

func TestBuildBootstrap_explicitReasoningPreservationFalseNoParticipants(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	marker := "    - id: tool-call-repair\n      enabled: true\n      config: {}\n"
	insert := marker + "    - id: reasoning-output-preservation\n      enabled: false\n"
	text := strings.Replace(string(base), marker, insert, 1)
	if text == string(base) {
		t.Fatal("expected dogfood feature insertion to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("inspect", func(t *testing.T) {
		t.Parallel()
		snap, err := runtimebundle.InspectInventory(t.Context(), runtimebundle.InspectInput{
			ConfigPath: path,
			Mandatory:  lipsdk.StandardDistributionRequirements(),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertInventoryFeatureRowEnabled(t, snap, standardplugins.ReasoningOutputPreservationFeatureID, false)
	})

	t.Run("serve", func(t *testing.T) {
		t.Parallel()
		res, err := runtimebundle.BuildBootstrap(t.Context(), runtimebundle.BuildBootstrapInput{
			ConfigPath:      path,
			Mode:            runtimebundle.BootstrapServe,
			Mandatory:       lipsdk.StandardDistributionRequirements(),
			LogWriter:       io.Discard,
			HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		bootstrapServeCleanup(t, res)
		assertFeatureRowEnabled(t, res, standardplugins.ReasoningOutputPreservationFeatureID, false)
		assertReasoningRegistrationEnabled(t, res, false)
		assertHasReasoningParticipants(t, res, false)
	})
}

func assertFeatureRowEnabled(t *testing.T, res runtimebundle.BootstrapResult, id string, wantEnabled bool) {
	t.Helper()
	if res.Config == nil {
		t.Fatal("nil config")
	}
	var found bool
	for _, p := range res.Config.Plugins.Features {
		if p.FactoryID() == id || p.InstanceID() == id {
			found = true
			if p.Enabled != wantEnabled {
				t.Fatalf("feature %q Enabled=%v want %v", id, p.Enabled, wantEnabled)
			}
		}
	}
	if !found {
		t.Fatalf("feature %q not present in config features", id)
	}
}

// assertInventoryFeatureRowEnabled is the [runtimebundle.InspectInventory]
// equivalent of assertFeatureRowEnabled: [diag.InventorySnapshot].Features is
// built directly from the same accepted config, so this proves the shared
// strict loader injects/honors the feature row without exposing raw config.
func assertInventoryFeatureRowEnabled(t *testing.T, snap diag.InventorySnapshot, id string, wantEnabled bool) {
	t.Helper()
	var found bool
	for _, row := range snap.Features {
		if row.FactoryKind == id || row.ID == id {
			found = true
			if row.Enabled != wantEnabled {
				t.Fatalf("feature %q Enabled=%v want %v", id, row.Enabled, wantEnabled)
			}
		}
	}
	if !found {
		t.Fatalf("feature %q not present in inventory features", id)
	}
}

func assertReasoningRegistrationEnabled(t *testing.T, res runtimebundle.BootstrapResult, wantEnabled bool) {
	t.Helper()
	id := standardplugins.ReasoningOutputPreservationFeatureID
	var found bool
	for _, r := range res.Registrations {
		if r.Kind != lipsdk.PluginKindFeature {
			continue
		}
		if r.FactoryKind == id || r.ID == id {
			found = true
			if r.Enabled != wantEnabled {
				t.Fatalf("registration %q Enabled=%v want %v", id, r.Enabled, wantEnabled)
			}
		}
	}
	if !found {
		t.Fatalf("feature %q missing from bootstrap Registrations (inject must precede RegistrationsFromConfig)", id)
	}
}

func assertHasReasoningParticipants(t *testing.T, res runtimebundle.BootstrapResult, want bool) {
	t.Helper()
	// BootstrapResult carries no FeatureSurface (buildBootstrap merges no
	// duplicate surface; CompileGeneration owns the sole merge for the
	// published generation). Recompute the same merge locally from the public
	// Registry/Registrations to characterize the participant pipeline.
	merged, err := featurebundle.MergeFeatureSurface(res.Registry, res.Registrations)
	if err != nil {
		t.Fatal(err)
	}
	wantTransform := reasoningpreservation.ID + "-transform"
	wantObserver := reasoningpreservation.ID + "-observer"
	var hasTransform, hasObserver bool
	for _, x := range merged.AttemptTransforms {
		if x != nil && x.ID() == wantTransform {
			hasTransform = true
		}
	}
	for _, o := range merged.StreamObserverFactories {
		if o != nil && o.ID() == wantObserver {
			hasObserver = true
		}
	}
	if want {
		if !hasTransform || !hasObserver {
			t.Fatalf("want reasoning participants transform=%v observer=%v", hasTransform, hasObserver)
		}
		return
	}
	if hasTransform || hasObserver {
		t.Fatalf("explicit opt-out must yield no reasoning participants transform=%v observer=%v", hasTransform, hasObserver)
	}
}
