package runtimebundle_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestBuildBootstrap_absentReasoningPreservationInjectsDefaultParticipants(t *testing.T) {
	t.Parallel()
	path := bpkit.WriteDogfoodLocalStubConfig(t)
	for _, mode := range []runtimebundle.BootstrapMode{runtimebundle.BootstrapInspect, runtimebundle.BootstrapServe} {
		t.Run(modeName(mode), func(t *testing.T) {
			t.Parallel()
			res, err := runtimebundle.BuildBootstrap(t.Context(), runtimebundle.BuildBootstrapInput{
				ConfigPath: path,
				Mode:       mode,
				Mandatory:  lipsdk.StandardDistributionRequirements(),
				LogWriter:  io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if res.ShutdownTracing != nil {
					_ = res.ShutdownTracing(t.Context())
				}
			}()
			assertFeatureRowEnabled(t, res, standardplugins.ReasoningOutputPreservationFeatureID, true)
			assertReasoningRegistrationEnabled(t, res, true)
			assertHasReasoningParticipants(t, res, true)
			if mode == runtimebundle.BootstrapServe && (res.Built == nil || res.Built.Executor == nil) {
				t.Fatal("BootstrapServe must produce Built with Executor")
			}
			if mode == runtimebundle.BootstrapInspect && res.Built != nil {
				t.Fatal("BootstrapInspect must leave Built nil")
			}
		})
	}
}

func TestBuildBootstrap_explicitReasoningPreservationFalseNoParticipants(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(bpkit.WriteDogfoodLocalStubConfig(t))
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
	for _, mode := range []runtimebundle.BootstrapMode{runtimebundle.BootstrapInspect, runtimebundle.BootstrapServe} {
		t.Run(modeName(mode), func(t *testing.T) {
			t.Parallel()
			res, err := runtimebundle.BuildBootstrap(t.Context(), runtimebundle.BuildBootstrapInput{
				ConfigPath: path,
				Mode:       mode,
				Mandatory:  lipsdk.StandardDistributionRequirements(),
				LogWriter:  io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if res.ShutdownTracing != nil {
					_ = res.ShutdownTracing(t.Context())
				}
			}()
			assertFeatureRowEnabled(t, res, standardplugins.ReasoningOutputPreservationFeatureID, false)
			assertReasoningRegistrationEnabled(t, res, false)
			assertHasReasoningParticipants(t, res, false)
		})
	}
}

func modeName(mode runtimebundle.BootstrapMode) string {
	switch mode {
	case runtimebundle.BootstrapInspect:
		return "inspect"
	case runtimebundle.BootstrapServe:
		return "serve"
	default:
		return "unknown"
	}
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
	// Public construction seam: MergeFeatureSurface only calls BuildFeatureBundle for
	// Enabled registrations, and FeatureBundleWithParts is the sole store/telemetry ctor (D12).
	// Absent transform/observer therefore means store/telemetry were not constructed.
	wantTransform := reasoningpreservation.ID + "-transform"
	wantObserver := reasoningpreservation.ID + "-observer"
	var hasTransform, hasObserver bool
	for _, x := range res.FeatureSurface.AttemptTransforms {
		if x != nil && x.ID() == wantTransform {
			hasTransform = true
		}
	}
	for _, o := range res.FeatureSurface.StreamObserverFactories {
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
