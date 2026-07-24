package runtimebundle_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "config", "config.yaml")
}

// TestInspectRoutes_ReturnsFocusedReadModel characterizes the Task 5.3 Inspect
// invariant: [runtimebundle.InspectRoutes] returns only [runtimebundle.RoutesSnapshot]
// — never a broad bootstrap/process/generation aggregate — because it builds
// no process/generation runtime at all.
func TestInspectRoutes_ReturnsFocusedReadModel(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	snap, err := runtimebundle.InspectRoutes(ctx, runtimebundle.InspectInput{
		ConfigPath: testConfigPath(t),
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute == "" {
		t.Fatal("expected effective default route")
	}
}

// TestInspectInventory_ReturnsFocusedReadModel characterizes the Task 5.3
// Inspect invariant for [runtimebundle.InspectInventory]: only
// [diag.InventorySnapshot] is returned, never a broad bootstrap aggregate.
func TestInspectInventory_ReturnsFocusedReadModel(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	snap, err := runtimebundle.InspectInventory(ctx, runtimebundle.InspectInput{
		ConfigPath: testConfigPath(t),
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Frontends) == 0 {
		t.Fatal("expected frontend rows")
	}
}

func TestBuildBootstrap_servePublishesInitialGeneration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath:      testConfigPath(t),
		Mode:            runtimebundle.BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapServeCleanup(t, res)
	if res.ProcessServices == nil || res.GenerationManager == nil || res.InitialGeneration == nil {
		t.Fatal("BootstrapServe must publish process services and generation 1")
	}
	lease, ok := res.GenerationManager.Acquire()
	if !ok || lease.Handler() == nil {
		t.Fatal("BootstrapServe must publish an acquireable handler")
	}
	lease.Release()
}

func TestBuildBootstrap_serveRequiresHandlerComposer(t *testing.T) {
	t.Parallel()
	res, err := runtimebundle.BuildBootstrap(t.Context(), runtimebundle.BuildBootstrapInput{
		ConfigPath: testConfigPath(t),
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		bootstrapServeCleanup(t, res)
		t.Fatal("expected nil HandlerComposer failure")
	}
	if !strings.Contains(err.Error(), "HandlerComposer") {
		t.Fatalf("error=%v want HandlerComposer requirement", err)
	}
}

func TestBuildBootstrap_serveSingleUserSecretGuardSnapshotsProcessEnv(t *testing.T) {
	const probe = "LIP_TEST_SECRETGUARD_INCLUDE"
	const secret = testkit.SyntheticOpenAIAPIKey
	t.Setenv(probe, secret)

	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := bytes.Replace(base, []byte("include_popular_env: true\n          include_env: []\n"), []byte("include_popular_env: false\n          include_env: ["+probe+"]\n"), 1)
	if bytes.Equal(text, base) {
		t.Fatal("expected secrets-guard example replacement to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, text, 0o600); err != nil {
		t.Fatal(err)
	}

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

	cand := compileCandidateAfterBootstrap(t, res)
	if cand.SecretGuardInventory == nil {
		t.Fatal("BootstrapServe candidate must build secret-guard inventory")
	}
	if cand.SecretGuardInventory.SecretGuardCatalogEntryCount == 0 {
		t.Fatal("single-user serve must snapshot process env into a nonzero secret catalog")
	}
	if cand.RuntimeSnapshot == nil {
		t.Fatal("expected runtime snapshot")
	}
	m, err := cand.RuntimeSnapshot.SecretGuardPlane().MatcherResolver.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected matcher resolver")
	}
	findings, err := m.ScanString(t.Context(), "prefix="+secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("standard bootstrap serve did not load the synthetic env secret")
	}
}

func TestBuildBootstrap_nilContext(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.BuildBootstrap(nil, runtimebundle.BuildBootstrapInput{ //nolint:staticcheck // intentional nil ctx contract
		ConfigPath: testConfigPath(t),
		Mode:       runtimebundle.BootstrapServe,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildBootstrap_unspecifiedMode(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: testConfigPath(t),
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestInspectRoutes_RejectsInvalidCustomBackendPrefix(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(testConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	customBackend := `    - id: nvidia-copy
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: nvidia
        base_url: http://127.0.0.1:9/v1
`
	text := strings.Replace(string(base), "  features:\n", customBackend+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected custom backend prefix validation error")
	}
	if !strings.Contains(err.Error(), "custom backend prefix") || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error = %v, want custom backend prefix reserved", err)
	}
}

func TestInspectRoutes_RejectsDuplicateEnabledSecretsGuardRegistrations(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-log-single-user.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	original := `    - id: secrets-guard
      enabled: true
      config:
        action: log
        audit_failure_policy: best_effort
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	duplicate := `    - kind: secrets-guard
      id: sg-log-1
      enabled: true
      config:
        action: log
        audit_failure_policy: best_effort
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
    - kind: secrets-guard
      id: sg-log-2
      enabled: true
      config:
        action: block
        audit_failure_policy: fail_closed
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	text := strings.Replace(string(base), original, duplicate, 1)
	if text == string(base) {
		t.Fatal("expected secrets-guard block replacement to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected duplicate enabled secrets-guard registrations to fail")
	}
	for _, bad := range []string{"action:", "block", "log", "sg-log-1", "sg-log-2"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error leaked %q: %v", bad, err)
		}
	}
}
