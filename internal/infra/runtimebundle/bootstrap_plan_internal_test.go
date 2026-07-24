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
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"
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

func TestBuildBootstrap_inspectDoesNotRequestSecretGuardEnvironment(t *testing.T) {
	env := &countingSecretGuardEnv{}
	res, err := buildBootstrap(t.Context(), BuildBootstrapInput{
		ConfigPath: filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml"),
		Mode:       BootstrapInspect,
		Mandatory:  lipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	}, env, LoadBootstrapEffectiveWithSource)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(t.Context())
		}
	}()
	if env.calls != 0 {
		t.Fatalf("BootstrapInspect consulted the secret-guard environment; calls=%d", env.calls)
	}
}

func TestBuildBootstrap_serveMultiUserSecretGuardDoesNotConsultEnvironment(t *testing.T) {
	env := &countingSecretGuardEnv{}
	res, err := buildBootstrap(t.Context(), BuildBootstrapInput{
		ConfigPath:      filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-multi-user.yaml"),
		Mode:            BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}, env, LoadBootstrapEffectiveWithSource)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if res.GenerationManager != nil {
			_ = res.GenerationManager.ShutdownDetached(t.Context(), runtimehost.NewLifecycleWorker())
		}
		if res.ProcessServices != nil {
			_ = res.ProcessServices.Close()
		}
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(t.Context())
		}
	}()
	if res.ProcessServices == nil || res.InitialGeneration == nil {
		t.Fatal("BootstrapServe must publish generation host handles")
	}
	if env.calls != 0 {
		t.Fatalf("multi_user serve consulted the secret-guard environment; calls=%d", env.calls)
	}
}

func TestBuildBootstrap_serveDisabledSecretGuardDoesNotConsultEnvironment(t *testing.T) {
	env := &countingSecretGuardEnv{}
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml"))
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

	res, err := buildBootstrap(t.Context(), BuildBootstrapInput{
		ConfigPath:      path,
		Mode:            BootstrapServe,
		Mandatory:       lipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}, env, LoadBootstrapEffectiveWithSource)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if res.GenerationManager != nil {
			_ = res.GenerationManager.ShutdownDetached(t.Context(), runtimehost.NewLifecycleWorker())
		}
		if res.ProcessServices != nil {
			_ = res.ProcessServices.Close()
		}
		if res.ShutdownTracing != nil {
			_ = res.ShutdownTracing(t.Context())
		}
	}()
	if res.ProcessServices == nil || res.InitialGeneration == nil {
		t.Fatal("BootstrapServe must publish generation host handles")
	}
	if env.calls != 0 {
		t.Fatalf("disabled serve consulted the secret-guard environment; calls=%d", env.calls)
	}
}
