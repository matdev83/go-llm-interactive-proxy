package runtimebundle_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/configsource"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func TestBootstrapCompatibility_LoadEffectiveHelperMatchesBuildBootstrap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// Example YAML expects a packaged discovery layout; tests stage connectors/localstub.
	path := bpkit.WriteDogfoodLocalStubConfig(t)

	eff, err := runtimebundle.LoadBootstrapEffective(ctx, path, config.StreamRecoveryOverrides{})
	if err != nil {
		t.Fatalf("LoadBootstrapEffective: %v", err)
	}
	if eff == nil || eff.Config == nil {
		t.Fatal("expected effective config")
	}
	if eff.Identity.PublicFingerprint == "" {
		t.Fatal("expected public fingerprint")
	}

	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(ctx)
		}
	}()

	if res.Config == nil {
		t.Fatal("expected bootstrap config")
	}
	// Shared pipeline must inject standard feature defaults before validation.
	if !hasFeatureID(res.Config, standardplugins.ToolCallRepairFeatureID) {
		t.Fatal("bootstrap must inject tool-call-repair via shared effective load")
	}
	if !hasFeatureID(res.Config, standardplugins.ReasoningOutputPreservationFeatureID) {
		t.Fatal("bootstrap must inject reasoning-output-preservation via shared effective load")
	}
	id, err := config.ComputeEffectiveIdentity(res.Config)
	if err != nil {
		t.Fatal(err)
	}
	if id.PublicFingerprint != eff.Identity.PublicFingerprint {
		t.Fatalf("fingerprint mismatch: bootstrap=%q helper=%q", id.PublicFingerprint, eff.Identity.PublicFingerprint)
	}
}

func TestBootstrapCompatibility_StrictFixtures_SecretSafeCategories(t *testing.T) {
	t.Parallel()
	secret := "sk-super-secret-token-value"
	cases := []struct {
		name    string
		raw     []byte
		wantCat configsource.Category
	}{
		{
			name:    "malformed_yaml",
			raw:     []byte("server: [\napi_key: " + secret + "\n"),
			wantCat: configsource.CategoryMalformedYAML,
		},
		{
			name: "multiple_documents",
			raw: []byte(`server:
  address: "127.0.0.1:0"
---
server:
  address: "127.0.0.1:1"
`),
			wantCat: configsource.CategoryMultipleDocuments,
		},
		{
			name: "unknown_core_field",
			raw: []byte(`server:
  address: "127.0.0.1:0"
not_a_core_field: true
`),
			wantCat: configsource.CategoryUnknownCoreField,
		},
		{
			name:    "empty",
			raw:     []byte{},
			wantCat: configsource.CategoryEmpty,
		},
		{
			name:    "whitespace",
			raw:     []byte("  \n\t\n"),
			wantCat: configsource.CategoryWhitespace,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "cfg.yaml")
			if err := os.WriteFile(path, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := runtimebundle.LoadBootstrapEffective(context.Background(), path, config.StreamRecoveryOverrides{})
			if err == nil {
				t.Fatal("LoadBootstrapEffective must reject strict fixture")
			}
			assertSecretSafeCategory(t, err, tc.wantCat, secret)

			_, err = runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
				ConfigPath: path,
				Mode:       runtimebundle.BootstrapInspect,
				Mandatory:  lipsdk.StandardDistributionRequirements(),
				LogWriter:  io.Discard,
			})
			if err == nil {
				t.Fatal("BuildBootstrap must reject strict fixture")
			}
			assertSecretSafeCategory(t, err, tc.wantCat, secret)
		})
	}
}

func TestBootstrapCompatibility_MissingPath_SourceMissingCategory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	_, err := runtimebundle.LoadBootstrapEffective(context.Background(), path, config.StreamRecoveryOverrides{})
	if err == nil {
		t.Fatal("expected missing source error")
	}
	cat, ok := configsource.CategoryOf(err)
	if !ok || cat != configsource.CategoryMissing {
		t.Fatalf("LoadBootstrapEffective category=%q ok=%v err=%v want %q", cat, ok, err, configsource.CategoryMissing)
	}

	_, err = runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected missing source error")
	}
	cat, ok = configsource.CategoryOf(err)
	if !ok || cat != configsource.CategoryMissing {
		t.Fatalf("BuildBootstrap category=%q ok=%v err=%v want %q", cat, ok, err, configsource.CategoryMissing)
	}
}

func TestBootstrapCompatibility_FixedStreamRecoveryCLIWins(t *testing.T) {
	t.Parallel()
	path := bpkit.WriteDogfoodLocalStubConfig(t)
	cliOff := false
	cliIdle := 12 * time.Second

	eff, err := runtimebundle.LoadBootstrapEffective(context.Background(), path, config.StreamRecoveryOverrides{
		CLIEnabled:     &cliOff,
		CLIIdleTimeout: cliIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Config.StreamRecovery.AutoResume.Enabled == nil || *eff.Config.StreamRecovery.AutoResume.Enabled {
		t.Fatal("CLI disabled must materialize into effective config")
	}
	if eff.Config.StreamRecovery.AutoResume.IdleTimeout != cliIdle.String() {
		t.Fatalf("CLI idle timeout: got %q want %q", eff.Config.StreamRecovery.AutoResume.IdleTimeout, cliIdle)
	}

	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
		StreamRecoveryOverrides: config.StreamRecoveryOverrides{
			CLIEnabled:     &cliOff,
			CLIIdleTimeout: cliIdle,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
	}()
	if res.Config.StreamRecovery.AutoResume.Enabled == nil || *res.Config.StreamRecovery.AutoResume.Enabled {
		t.Fatal("BuildBootstrap must apply fixed CLI stream-recovery overrides")
	}
	if res.Config.StreamRecovery.AutoResume.IdleTimeout != cliIdle.String() {
		t.Fatalf("BuildBootstrap idle timeout: got %q", res.Config.StreamRecovery.AutoResume.IdleTimeout)
	}
}

func TestBootstrapCompatibility_InjectsStandardFeaturesWhenAbsent(t *testing.T) {
	t.Parallel()
	const body = `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
routing:
  default_route: "local-stub:stub-default"
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
    - kind: local-stub
      id: local-stub
      enabled: true
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	eff, err := runtimebundle.LoadBootstrapEffective(context.Background(), path, config.StreamRecoveryOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasFeatureID(eff.Config, standardplugins.ToolCallRepairFeatureID) {
		t.Fatal("expected tool-call-repair injection")
	}
	if !hasFeatureID(eff.Config, standardplugins.ReasoningOutputPreservationFeatureID) {
		t.Fatal("expected reasoning-output-preservation injection")
	}
}

func TestBootstrapCompatibility_BuildPartialCleanupOnCandidateFailure(t *testing.T) {
	t.Parallel()
	// Invalid backend kind fails during candidate compile after process services
	// may have opened resources; Build/CompileCandidate must dispose without
	// returning a Built aggregate.
	path := writeConfigWithUnknownBackendKind(t)
	res, err := runtimebundle.BuildBootstrap(context.Background(), runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(context.Background())
		}
		t.Fatal("expected candidate compile failure")
	}
	if res.Built != nil {
		t.Fatal("failed serve bootstrap must not return Built")
	}
	if !strings.Contains(err.Error(), "runtime assembly") && !strings.Contains(err.Error(), "backend") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Error-path tracing shutdown is idempotent when the result still exposes it.
	if res.ShutdownTracing != nil {
		if shutErr := res.ShutdownTracing(context.Background()); shutErr != nil {
			t.Fatalf("second tracing shutdown: %v", shutErr)
		}
	}
}

func hasFeatureID(cfg *config.Config, id string) bool {
	if cfg == nil {
		return false
	}
	for _, p := range cfg.Plugins.Features {
		if p.FactoryID() == id || p.InstanceID() == id {
			return true
		}
	}
	return false
}

func assertSecretSafeCategory(t *testing.T, err error, want configsource.Category, secret string) {
	t.Helper()
	cat, ok := configsource.CategoryOf(err)
	if !ok || cat != want {
		t.Fatalf("category=%q ok=%v err=%v want %q", cat, ok, err, want)
	}
	msg := err.Error()
	if secret != "" && strings.Contains(msg, secret) {
		t.Fatalf("error leaked secret: %q", msg)
	}
	if strings.Contains(msg, "not_a_core_field: true") {
		t.Fatalf("error leaked raw field: %q", msg)
	}
}

func writeConfigWithUnknownBackendKind(t *testing.T) string {
	t.Helper()
	base, err := os.ReadFile(bpkit.WriteDogfoodLocalStubConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(base), "kind: local-stub", "kind: not-a-real-backend-kind", 1)
	if text == string(base) {
		// dogfood may identify local-stub by id only; force an unknown kind row.
		text = strings.Replace(string(base), "id: dogfood-local\n      enabled: true", "id: dogfood-local\n      kind: not-a-real-backend-kind\n      enabled: true", 1)
	}
	if !strings.Contains(text, "not-a-real-backend-kind") {
		t.Fatal("failed to inject unknown backend kind into dogfood fixture")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
