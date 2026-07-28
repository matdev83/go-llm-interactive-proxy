package runtimebundle

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	httpcontract "github.com/matdev83/go-llm-interactive-proxy/internal/stdhttp/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func stubHandlerComposer(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
}

type countingSecretGuardEnv struct {
	calls int
}

func (e *countingSecretGuardEnv) Lookup(string) (string, bool) {
	e.calls++
	return "", false
}

func (e *countingSecretGuardEnv) Snapshot() []string {
	e.calls++
	return nil
}

// TestInspect_DoesNotRequestSecretGuardEnvironment characterizes the Task 5.3
// Inspect invariant: prepareInspect (shared by [InspectRoutes]/[InspectInventory])
// builds no ProcessServices and accepts no secret-guard environment seam at
// all, so it structurally cannot consult it (unlike BuildHost, which threads
// an explicit coresg.Environment into publishInitialGeneration).
func TestInspect_DoesNotRequestSecretGuardEnvironment(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml")
	in := InspectInput{
		ConfigPath: path,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
	}
	if _, _, _, err := prepareInspect(t.Context(), in, LoadBootstrapEffectiveWithSource); err != nil {
		t.Fatal(err)
	}
}

func TestBuildHost_serveMultiUserSecretGuardDoesNotConsultEnvironment(t *testing.T) {
	env := &countingSecretGuardEnv{}
	host, err := buildHostWithEnv(t.Context(), hostBuildInput{
		ConfigPath:      MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-multi-user.yaml")),
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}, LoadBootstrapEffectiveWithSource, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHost(t, host) })
	if host.process == nil || host.manager == nil || host.manager.Active() == nil {
		t.Fatal("BuildHost must publish generation host handles")
	}
	if env.calls != 0 {
		t.Fatalf("multi_user serve consulted the secret-guard environment; calls=%d", env.calls)
	}
}

func TestBuildHost_serveDisabledSecretGuardDoesNotConsultEnvironment(t *testing.T) {
	env := &countingSecretGuardEnv{}
	base, err := os.ReadFile(MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	original := `    - id: secrets-guard
      enabled: true
      config:
        action: block
        audit_failure_policy: fail_closed
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	replacement := `    - id: secrets-guard
      enabled: false
      config:
        action: block
        audit_failure_policy: fail_closed
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	text := strings.Replace(string(base), original, replacement, 1)
	if text == string(base) {
		t.Fatal("expected secrets-guard enabled flag replacement to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	host, err := buildHostWithEnv(t.Context(), hostBuildInput{
		ConfigPath:      path,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}, LoadBootstrapEffectiveWithSource, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHost(t, host) })
	if host.process == nil || host.manager == nil || host.manager.Active() == nil {
		t.Fatal("BuildHost must publish generation host handles")
	}
	if env.calls != 0 {
		t.Fatalf("disabled serve consulted the secret-guard environment; calls=%d", env.calls)
	}
}
