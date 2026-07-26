package runtimebundle_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "config", "config.yaml")
}

func TestBuildBootstrap_inspectLeavesBuiltNil(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: testConfigPath(t),
		Mode:       runtimebundle.BootstrapInspect,
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
	if res.Built != nil {
		t.Fatal("BootstrapInspect must not call Build; Built must be nil")
	}
	if res.Config == nil || res.Registry == nil || res.App == nil {
		t.Fatalf("expected config, registry, and app: cfg=%v reg=%v app=%v", res.Config != nil, res.Registry != nil, res.App != nil)
	}
}

func TestBuildBootstrap_serveSetsBuiltExecutor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	res, err := runtimebundle.BuildBootstrap(ctx, runtimebundle.BuildBootstrapInput{
		ConfigPath: testConfigPath(t),
		Mode:       runtimebundle.BootstrapServe,
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
	if res.Built == nil || res.Built.Executor == nil {
		t.Fatal("BootstrapServe must produce Built with Executor")
	}
}

func TestBuildBootstrap_serveSingleUserSecretGuardSnapshotsProcessEnv(t *testing.T) {
	const probe = "LIP_TEST_SECRETGUARD_INCLUDE"
	const secret = testkit.SyntheticOpenAIAPIKey
	t.Setenv(probe, secret)

	basePath := bpkit.MaterializeExampleConfig(t, filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml"))
	base, err := os.ReadFile(basePath)
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
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapServe,
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
	if res.Built == nil || res.Built.SecretGuardInventory == nil {
		t.Fatal("BootstrapServe must build secret-guard inventory")
	}
	if res.Built.SecretGuardInventory.SecretGuardCatalogEntryCount == 0 {
		t.Fatal("single-user serve must snapshot process env into a nonzero secret catalog")
	}
	if res.Built.RuntimeSnapshot == nil {
		t.Fatal("expected runtime snapshot")
	}
	m, err := res.Built.RuntimeSnapshot.SecretGuardPlane().MatcherResolver.Resolve(t.Context())
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
		Mode:       runtimebundle.BootstrapInspect,
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

func TestBuildBootstrap_inspectRejectsInvalidCustomBackendPrefix(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(testConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	customBackend := `    - id: openai-legacy-copy
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: openai-legacy
        base_url: http://127.0.0.1:9/v1
`
	text := strings.Replace(string(base), "  features:\n", customBackend+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = runtimebundle.BuildBootstrap(t.Context(), runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected custom backend prefix validation error")
	}
	if !strings.Contains(err.Error(), "custom backend prefix") || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error = %v, want custom backend prefix reserved", err)
	}
}

func TestBuildBootstrap_inspectRejectsDuplicateEnabledSecretsGuardRegistrations(t *testing.T) {
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
	_, err = runtimebundle.BuildBootstrap(t.Context(), runtimebundle.BuildBootstrapInput{
		ConfigPath: path,
		Mode:       runtimebundle.BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
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
